package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gitx"
)

// Captured transcripts may contain secrets. Everything here lives under
// <git-dir>/unasked/sessions, with files 0600 and directories 0700.
//
// On Windows the mode bits are applied through os.Chmod on a best-effort
// basis, which only toggles the read-only attribute. Real owner-only ACLs are
// Plan 4 hardening; until then the doctor command warns once on Windows.

const (
	metaFile       = "meta.json"
	transcriptFile = "transcript.jsonl"
	maxIDLen       = 128

	defaultKeep   = 50
	defaultMaxAge = 30 * 24 * time.Hour
)

// Meta is the record kept beside a captured transcript. Start is the
// working-tree baseline at the first capture; it stays zero-valued when the
// stop hook ran without a prior start (the report then falls back to the
// commit-based baseline and warns).
type Meta struct {
	Agent, Session, AgentVersion, Cwd string
	Start                             baseline.Baseline
	Stop                              *baseline.Baseline
	Feedbacks                         int // consecutive hook feedbacks (loop cap)
	Created, Updated                  time.Time
}

// sanitize keeps [A-Za-z0-9._-], replaces everything else with '_', caps the
// length, and hashes the raw value when the result is empty, "." or "..".
func sanitize(raw string) string {
	var b strings.Builder
	for _, c := range raw {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > maxIDLen {
		s = s[:maxIDLen]
	}
	sum := sha256.Sum256([]byte(raw))
	h := hex.EncodeToString(sum[:])
	if s == "" || s == "." || s == ".." {
		return h[:16]
	}
	if s != raw {
		s += "-" + h[:8] // distinct raw ids must not collide after sanitising
	}
	return s
}

// CaptureDir returns (creating it) <git-dir>/unasked/sessions/<agent>-<session>.
func CaptureDir(r *gitx.Repo, agent, session string) (string, error) {
	return r.UnaskedDir("sessions", sanitize(agent)+"-"+sanitize(session))
}

// RecordStart records the working-tree baseline for a session. It is
// idempotent: a second call keeps the first Start.
func RecordStart(r *gitx.Repo, agent, session, cwd string) (*Meta, error) {
	dir, err := CaptureDir(r, agent, session)
	if err != nil {
		return nil, err
	}
	m := metaOrNil(dir)
	if m != nil && !m.Start.Taken.IsZero() {
		return m, nil
	}
	b, err := baseline.Snapshot(r)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if m == nil {
		m = &Meta{Agent: agent, Session: session, Created: now}
	}
	m.Start, m.Updated = b, now
	if m.Cwd == "" {
		m.Cwd = cwd
	}
	return m, writeMeta(dir, m)
}

// RecordStop copies the transcript to <dir>/transcript.jsonl, copies each
// extra path (files or directories, recursively) under <dir> keeping its base
// name, and records the Stop snapshot. Missing extras are skipped; a missing
// transcript is an error.
func RecordStop(r *gitx.Repo, agent, session, transcriptPath string, extra []string) (*Meta, error) {
	dir, err := CaptureDir(r, agent, session)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("capture transcript: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("capture transcript: transcript is not a regular file")
	}
	if err := copyFile(transcriptPath, filepath.Join(dir, transcriptFile)); err != nil {
		return nil, fmt.Errorf("capture transcript: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range extra {
		name := filepath.Base(p)
		if name == "." || name == ".." || name == string(filepath.Separator) || name == metaFile || name == transcriptFile || seen[name] {
			continue
		}
		seen[name] = true
		if err := copyTree(p, filepath.Join(dir, filepath.Base(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("capture %s: %w", p, err)
		}
	}
	b, err := baseline.Snapshot(r)
	if err != nil {
		return nil, err
	}
	m := metaOrNil(dir)
	now := time.Now()
	if m == nil {
		m = &Meta{Agent: agent, Session: session, Created: now}
	}
	m.Stop, m.Updated = &b, now
	return m, writeMeta(dir, m)
}

// metaOrNil returns the meta in dir, or nil when it is missing or unreadable;
// callers then rewrite it, so a corrupt meta.json never blocks capturing.
func metaOrNil(dir string) *Meta {
	m, err := LoadMeta(dir)
	if err != nil {
		return nil
	}
	return m
}

// LoadMeta reads dir/meta.json. A missing file satisfies errors.Is(err, fs.ErrNotExist).
func LoadMeta(dir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, metaFile), err)
	}
	return &m, nil
}

// SaveMeta writes dir/meta.json atomically, mode 0600. Callers doing a
// read-modify-write (the hook's feedback counter) serialise it themselves.
func SaveMeta(dir string, m *Meta) error { return writeMeta(dir, m) }

func writeMeta(dir string, m *Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, metaFile), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// atomicWrite writes via a temp file in the same directory and renames it, so
// a concurrent reader never sees a partial file.
func atomicWrite(path string, fill func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o600); err != nil && !isWindows() {
		return err
	}
	if err := fill(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func isWindows() bool { return filepath.Separator == '\\' }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return atomicWrite(dst, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

// copyTree copies a file or directory. Symlinks and other non-regular files
// are skipped so a capture can never pull in data from outside the source.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode().IsRegular():
		return copyFile(src, dst)
	case st.IsDir():
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dst, 0o700); err != nil && !isWindows() {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListCaptured returns the captured sessions, newest first by Meta.Updated.
// Directories without a readable meta.json are skipped.
func ListCaptured(r *gitx.Repo) ([]Session, error) {
	base, err := r.UnaskedDir("sessions")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		m, err := LoadMeta(dir)
		if err != nil {
			continue
		}
		s := Session{Agent: m.Agent, ID: m.Session, Path: dir, Cwd: m.Cwd, Start: m.Created, End: m.Updated, Captured: true, Meta: m}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Meta.Updated.After(out[j].Meta.Updated) })
	return out, nil
}

// Prune removes captured session directories beyond the newest keep and those
// not updated within maxAge, so whichever rule leaves fewer wins. Directories
// without a valid meta.json count too, aged by their mtime, so orphaned
// transcripts cannot linger. Non-positive arguments select the defaults
// (50, 30 days).
func Prune(r *gitx.Repo, keep int, maxAge time.Duration) error {
	if keep <= 0 {
		keep = defaultKeep
	}
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	base, err := r.UnaskedDir("sessions")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	type item struct {
		path    string
		updated time.Time
	}
	var items []item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if m := metaOrNil(dir); m != nil {
			items = append(items, item{dir, m.Updated})
		} else if info, err := e.Info(); err == nil {
			items = append(items, item{dir, info.ModTime()})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].updated.After(items[j].updated) })
	cutoff := time.Now().Add(-maxAge)
	var errs []error
	for i, it := range items {
		if i >= keep || it.updated.Before(cutoff) {
			if err := os.RemoveAll(it.path); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
