// Package baseline captures the working tree as a git tree object without
// touching the real index.
package baseline

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/gitx"
)

var (
	ErrUnmerged  = errors.New("index has unresolved conflicts")
	ErrNoCommits = errors.New("repository has no commits")
)

// Baseline is a snapshot of the working tree. Commit is "" when the repo has
// no commits.
type Baseline struct {
	Commit string
	Tree   string
	Taken  time.Time
}

// Snapshot writes the whole working tree (tracked edits plus untracked,
// non-ignored files) as a tree object, using a private temporary index.
func Snapshot(r *gitx.Repo) (Baseline, error) {
	return withTempIndex(r, func(env []string) (Baseline, error) {
		if _, err := r.RunEnv(env, nil, "-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "-c", "core.fsmonitor=false", "add", "-A", "--", "."); err != nil {
			return Baseline{}, err
		}
		tree, err := writeTree(r, env)
		if err != nil {
			return Baseline{}, err
		}
		b := Baseline{Tree: tree, Taken: time.Now()}
		if out, err := r.Run("rev-parse", "--verify", "-q", "HEAD"); err == nil {
			b.Commit = strings.TrimSpace(string(out))
		} else {
			var ge *gitx.Error
			if !errors.As(err, &ge) || ge.Code != 1 {
				return Baseline{}, err
			}
		}
		return b, nil
	})
}

// IndexTree returns the tree of the current real index (for --staged).
func IndexTree(r *gitx.Repo) (string, error) {
	b, err := withTempIndex(r, func(env []string) (Baseline, error) {
		tree, err := writeTree(r, env)
		return Baseline{Tree: tree}, err
	})
	return b.Tree, err
}

// clearHidingFlags drops, on the temporary index only, the per-entry flags
// that make git skip the working tree: assume-unchanged, skip-worktree (only
// for files that exist, so a sparse checkout's absent files are not read as
// deletions) and fsmonitor-valid. Otherwise edits to such files are invisible.
func clearHidingFlags(r *gitx.Repo, env []string) error {
	out, err := r.RunEnv(env, nil, "-c", "core.fsmonitor=false", "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	var assume, skip, fsm []string
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 3 {
			continue
		}
		tag, path := rec[0], rec[2:]
		if tag >= 'a' && tag <= 'z' {
			assume = append(assume, path)
		}
		if tag == 'S' || tag == 's' {
			if _, err := os.Lstat(filepath.Join(r.Dir, filepath.FromSlash(path))); err == nil {
				skip = append(skip, path)
			}
		}
		fsm = append(fsm, path)
	}
	for _, c := range []struct {
		flag  string
		paths []string
	}{{"--no-assume-unchanged", assume}, {"--no-skip-worktree", skip}, {"--no-fsmonitor-valid", fsm}} {
		if len(c.paths) == 0 {
			continue
		}
		in := strings.NewReader(strings.Join(c.paths, "\x00") + "\x00")
		if _, err := r.RunEnv(env, in, "-c", "core.fsmonitor=false", "update-index", c.flag, "-z", "--stdin"); err != nil {
			return err
		}
	}
	return nil
}

func writeTree(r *gitx.Repo, env []string) (string, error) {
	out, err := r.RunEnv(env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// withTempIndex checks for conflicts, copies the real index to a temp file
// (write-tree would otherwise rewrite the real one) and runs fn against it.
func withTempIndex(r *gitx.Repo, fn func(env []string) (Baseline, error)) (Baseline, error) {
	out, err := r.Run("ls-files", "-u")
	if err != nil {
		return Baseline{}, err
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return Baseline{}, ErrUnmerged
	}
	tmpDir, err := r.UnaskedDir("tmp")
	if err != nil {
		return Baseline{}, err
	}
	f, err := os.CreateTemp(tmpDir, "index-*")
	if err != nil {
		return Baseline{}, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()

	pout, err := r.Run("rev-parse", "--git-path", "index")
	if err != nil {
		return Baseline{}, err
	}
	real := filepath.FromSlash(strings.TrimSpace(string(pout)))
	if !filepath.IsAbs(real) {
		real = filepath.Join(r.Dir, real)
	}
	var realMtime time.Time
	if src, err := os.Open(real); err == nil {
		// Stat the open handle so a concurrent index replace cannot pair the
		// copied bytes with a newer file's mtime.
		st, serr := src.Stat()
		_, cerr := io.Copy(f, src)
		src.Close()
		if serr != nil {
			return Baseline{}, serr
		}
		if cerr != nil {
			return Baseline{}, cerr
		}
		realMtime = st.ModTime()
	} else if os.IsNotExist(err) {
		// An empty file is not a valid index; let git create it.
		f.Close()
		if err := os.Remove(tmp); err != nil {
			return Baseline{}, err
		}
	} else {
		return Baseline{}, err
	}
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return Baseline{}, err
	}
	// Git decides whether an entry is "racy" by comparing file mtimes with the
	// index file's mtime. The copy must keep the real index's mtime, or an
	// edit made in the same second as the last index write can be missed.
	env := []string{"GIT_INDEX_FILE=" + tmp}
	if !realMtime.IsZero() {
		if err := os.Chtimes(tmp, realMtime, realMtime); err != nil {
			return Baseline{}, err
		}
		if err := clearHidingFlags(r, env); err != nil {
			return Baseline{}, err
		}
	}
	return fn(env)
}
