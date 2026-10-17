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
		if _, err := r.RunEnv(env, nil, "-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "add", "-A", "--", "."); err != nil {
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
	if src, err := os.Open(real); err == nil {
		_, cerr := io.Copy(f, src)
		src.Close()
		if cerr != nil {
			return Baseline{}, cerr
		}
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
	return fn([]string{"GIT_INDEX_FILE=" + tmp})
}
