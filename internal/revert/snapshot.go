package revert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

const (
	keepSnapshots = 20
	idLayout      = "20060102T150405.000000000Z"
	manifestName  = "manifest.json"
	indexTreeName = "index.tree"
	filesDir      = "files"
)

// SnapshotInfo describes one drop snapshot, newest first in ListSnapshots.
type SnapshotInfo struct {
	ID       string
	Created  time.Time
	Hunks    []string // content hashes dropped
	Files    []string // touched paths
	Complete bool     // the drop finished and its after-state is recorded
	Undone   bool
}

// fileEntry is one touched path. The "before" fields describe the working
// tree just before the drop; the "after" fields just after it.
type fileEntry struct {
	Path        string   `json:"path"`
	Absent      bool     `json:"absent"`
	Mode        uint32   `json:"mode,omitempty"`
	SHA256      string   `json:"sha256,omitempty"`
	NewDirs     []string `json:"new_dirs,omitempty"` // parents missing before the drop, deepest first
	AfterAbsent bool     `json:"after_absent,omitempty"`
	AfterSHA256 string   `json:"after_sha256,omitempty"`
}

type manifestHunk struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
	File string `json:"file"`
}

type manifest struct {
	Version   int            `json:"version"`
	ID        string         `json:"id"`
	Created   time.Time      `json:"created"`
	Head      string         `json:"head,omitempty"`
	IndexTree string         `json:"index_tree"`
	Hunks     []manifestHunk `json:"hunks"`
	Files     []fileEntry    `json:"files"`
	Complete  bool           `json:"complete"`
	UndoneAt  *time.Time     `json:"undone_at,omitempty"`
}

type snapshot struct {
	id, dir string
	m       manifest
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func workPath(root, p string) string { return filepath.Join(root, filepath.FromSlash(p)) }

// takeSnapshot records the index tree and the raw bytes and mode of every
// touched path under <git-dir>/unasked/snapshots/<UTC timestamp>/.
func takeSnapshot(r *gitx.Repo, paths []string, hs []model.Hunk) (*snapshot, error) {
	idx, err := baseline.IndexTree(r)
	if err != nil {
		return nil, err
	}
	root, err := r.UnaskedDir("snapshots")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s := &snapshot{}
	for t := now; ; t = t.Add(time.Nanosecond) {
		s.id = t.Format(idLayout)
		s.dir = filepath.Join(root, s.id)
		err := os.Mkdir(s.dir, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	s.m = manifest{Version: 1, ID: s.id, Created: now, IndexTree: idx}
	if out, err := r.Run("rev-parse", "--verify", "-q", "HEAD"); err == nil {
		s.m.Head = strings.TrimSpace(string(out))
	}
	for _, h := range hs {
		s.m.Hunks = append(s.m.Hunks, manifestHunk{ID: h.ID, Hash: h.Hash, File: h.File})
	}
	if err := s.capture(r.Dir, paths); err != nil {
		os.RemoveAll(s.dir)
		return nil, err
	}
	return s, nil
}

func (s *snapshot) capture(work string, paths []string) error {
	for _, p := range paths {
		e := fileEntry{Path: p}
		st, err := os.Lstat(workPath(work, p))
		switch {
		case errors.Is(err, os.ErrNotExist):
			e.Absent = true
			for d := path.Dir(p); d != "."; d = path.Dir(d) {
				if _, err := os.Lstat(workPath(work, d)); errors.Is(err, os.ErrNotExist) {
					e.NewDirs = append(e.NewDirs, d)
				}
			}
		case err != nil:
			return err
		case !st.Mode().IsRegular():
			return fmt.Errorf("%w: %s is not a regular file", ErrApplyCheck, p)
		default:
			b, err := os.ReadFile(workPath(work, p))
			if err != nil {
				return err
			}
			e.Mode, e.SHA256 = uint32(st.Mode().Perm()), sum(b)
			dst := filepath.Join(s.dir, filesDir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o600); err != nil {
				return err
			}
		}
		s.m.Files = append(s.m.Files, e)
	}
	if err := os.WriteFile(filepath.Join(s.dir, indexTreeName), []byte(s.m.IndexTree+"\n"), 0o600); err != nil {
		return err
	}
	return s.save()
}

// save writes the manifest atomically, mode 0600.
func (s *snapshot) save() error {
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, manifestName), b, 0o600)
}

// writeAtomic writes data to a temp file beside path and renames it over path.
func writeAtomic(p string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".unasked-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, perm)); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func (s *snapshot) before(e fileEntry) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, filesDir, filepath.FromSlash(e.Path)))
}

// current reads a working-tree path; ok is false when it does not exist.
func current(work, p string) (b []byte, ok bool, err error) {
	st, err := os.Lstat(workPath(work, p))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%s is not a regular file", p)
	}
	b, err = os.ReadFile(workPath(work, p))
	return b, true, err
}

// restore writes every snapshotted file back byte-exact with its mode,
// removes files that were absent, then re-checks every sha256.
func (s *snapshot) restore(work string) error {
	var errs []error
	for _, e := range s.m.Files {
		full := workPath(work, e.Path)
		if e.Absent {
			if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			for _, d := range e.NewDirs {
				os.Remove(workPath(work, d)) // only succeeds when empty
			}
			continue
		}
		b, err := s.before(e)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := writeAtomic(full, b, os.FileMode(e.Mode)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, e := range s.m.Files {
		b, ok, err := current(work, e.Path)
		switch {
		case err != nil:
			return err
		case ok == e.Absent || (ok && sum(b) != e.SHA256):
			return fmt.Errorf("%s does not match the snapshot after restore", e.Path)
		}
	}
	return nil
}

// verifyRegions checks each touched file against its snapshot: new files
// are gone, deleted files are back, and modified files differ only inside
// the dropped hunks.
func (s *snapshot) verifyRegions(work string, hs []model.Hunk) error {
	byFile := map[string][]model.Hunk{}
	for _, h := range hs {
		byFile[h.File] = append(byFile[h.File], h)
	}
	for _, e := range s.m.Files {
		fh := byFile[e.Path]
		post, ok, err := current(work, e.Path)
		if err != nil {
			return err
		}
		var pre []byte
		if !e.Absent {
			if pre, err = s.before(e); err != nil {
				return err
			}
		}
		switch {
		case fh[0].NewFile:
			if ok {
				return fmt.Errorf("%s should have been removed", e.Path)
			}
			if e.Absent {
				return fmt.Errorf("%s was missing before the drop", e.Path)
			}
			continue
		case fh[0].DeletedFile != e.Absent:
			return fmt.Errorf("%s: unexpected state before the drop", e.Path)
		case !ok:
			return fmt.Errorf("%s is missing after the drop", e.Path)
		}
		if err := regionCheck(pre, post, fh); err != nil {
			return err
		}
	}
	return nil
}

// complete records the after-drop state and marks the snapshot usable for
// change detection in undo.
func (s *snapshot) complete(work string) error {
	for i := range s.m.Files {
		e := &s.m.Files[i]
		b, ok, err := current(work, e.Path)
		if err != nil {
			return err
		}
		e.AfterAbsent = !ok
		if ok {
			e.AfterSHA256 = sum(b)
		}
	}
	s.m.Complete = true
	return s.save()
}

func loadSnapshot(r *gitx.Repo, id string) (*snapshot, error) {
	if _, err := time.Parse(idLayout, id); err != nil {
		return nil, fmt.Errorf("%w: unknown snapshot %q", ErrNoSnapshot, id)
	}
	root, err := r.UnaskedDir("snapshots")
	if err != nil {
		return nil, err
	}
	s := &snapshot{id: id, dir: filepath.Join(root, id)}
	b, err := os.ReadFile(filepath.Join(s.dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: unknown snapshot %q", ErrNoSnapshot, id)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", id, err)
	}
	for _, e := range s.m.Files {
		if err := checkPath(e.Path); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", id, err)
		}
		for _, d := range e.NewDirs {
			if err := checkPath(d); err != nil {
				return nil, fmt.Errorf("snapshot %s: %w", id, err)
			}
		}
	}
	return s, nil
}

// snapshotIDs lists snapshot directory names, newest first.
func snapshotIDs(r *gitx.Repo) ([]string, error) {
	root, err := r.UnaskedDir("snapshots")
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if _, err := time.Parse(idLayout, e.Name()); e.IsDir() && err == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// ListSnapshots returns the readable snapshots, newest first.
func ListSnapshots(r *gitx.Repo) ([]SnapshotInfo, error) {
	ids, err := snapshotIDs(r)
	if err != nil {
		return nil, err
	}
	var out []SnapshotInfo
	for _, id := range ids {
		s, err := loadSnapshot(r, id)
		if err != nil {
			continue
		}
		info := SnapshotInfo{ID: id, Created: s.m.Created, Complete: s.m.Complete, Undone: s.m.UndoneAt != nil}
		for _, h := range s.m.Hunks {
			info.Hunks = append(info.Hunks, h.Hash)
		}
		for _, e := range s.m.Files {
			info.Files = append(info.Files, e.Path)
		}
		out = append(out, info)
	}
	return out, nil
}

func prune(r *gitx.Repo, keep int) error {
	ids, err := snapshotIDs(r)
	if err != nil || len(ids) <= keep {
		return err
	}
	root := filepath.Join(r.GitDir, "unasked", "snapshots")
	var errs []error
	for _, id := range ids[keep:] {
		errs = append(errs, os.RemoveAll(filepath.Join(root, id)))
	}
	return errors.Join(errs...)
}

// Undo restores a snapshot (§9.3): id "" is the newest one not yet undone.
// Without force it refuses when a touched file or the index changed since
// the drop. The index is reset only when it differs from the snapshot.
func Undo(r *gitx.Repo, id string, force bool) error {
	if id == "" {
		list, err := ListSnapshots(r)
		if err != nil {
			return err
		}
		for _, s := range list {
			if !s.Undone {
				id = s.ID
				break
			}
		}
		if id == "" {
			return ErrNoSnapshot
		}
	}
	s, err := loadSnapshot(r, id)
	if err != nil {
		return err
	}
	idx, err := baseline.IndexTree(r)
	if err != nil {
		return err
	}
	if !force {
		if s.m.Complete {
			for _, e := range s.m.Files {
				b, ok, err := current(r.Dir, e.Path)
				if err != nil {
					return err
				}
				if ok == e.AfterAbsent || (ok && sum(b) != e.AfterSHA256) {
					return fmt.Errorf("%s %w", e.Path, ErrChangedSinceDrop)
				}
			}
		}
		if idx != s.m.IndexTree {
			return fmt.Errorf("the index %w", ErrChangedSinceDrop)
		}
	}
	if err := s.restore(r.Dir); err != nil {
		return fmt.Errorf("undo %s: %w", id, err)
	}
	if idx != s.m.IndexTree {
		if _, err := r.Run("read-tree", s.m.IndexTree); err != nil {
			return fmt.Errorf("undo %s: files restored, index not: %w", id, err)
		}
		// Refresh stat data so restored files do not all look modified; a
		// non-zero exit only means some files differ from the index.
		if _, err := r.Run("update-index", "-q", "--refresh"); err != nil {
			var ge *gitx.Error
			if !errors.As(err, &ge) {
				return err
			}
		}
	}
	now := time.Now().UTC()
	s.m.UndoneAt = &now
	return s.save()
}
