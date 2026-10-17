package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/gittest"
)

func TestOpenFromSubdir(t *testing.T) {
	g := gittest.New(t)
	g.Write("a/b/f.txt", "x\n")
	g.Commit("init")
	r, err := Open(filepath.Join(g.Dir, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir != g.Dir {
		t.Errorf("Dir = %q, want %q", r.Dir, g.Dir)
	}
	if !strings.HasSuffix(filepath.ToSlash(r.GitDir), ".git") {
		t.Errorf("GitDir = %q, want suffix .git", r.GitDir)
	}
}

func TestOpenNotRepo(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if _, err := Open(dir); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("err = %v, want ErrNotRepo", err)
	}
}

func TestUnaskedDirPerWorktree(t *testing.T) {
	g := gittest.New(t)
	g.Write("f.txt", "x\n")
	g.Commit("init")
	wt := filepath.Join(filepath.Dir(g.Dir), filepath.Base(g.Dir)+"-wt")
	g.Git("worktree", "add", "-q", wt, "-b", "other")
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.UnaskedDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(".git", "worktrees", filepath.Base(wt), "unasked"))
	if !strings.HasSuffix(filepath.ToSlash(d), want) {
		t.Errorf("UnaskedDir = %q, want suffix %q", d, want)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Errorf("mode = %o, want 700", st.Mode().Perm())
		}
	}
}
