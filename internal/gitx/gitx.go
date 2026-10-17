// Package gitx runs the git binary. It is the only package that executes git.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrNotRepo = errors.New("not a git repository")

// Error is a git invocation that exited non-zero.
type Error struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.Code, strings.TrimSpace(e.Stderr))
}

// Repo is a git working tree. Dir is the top-level directory; GitDir is this
// worktree's own git dir.
type Repo struct {
	Dir, GitDir string
}

func run(dir string, extraEnv []string, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), &Error{Args: args, Code: ee.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Open finds the repository containing dir.
func Open(dir string) (*Repo, error) {
	out, err := run(dir, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && ge.Code == 128 && strings.Contains(ge.Stderr, "not a git repository") {
			return nil, ErrNotRepo
		}
		return nil, err
	}
	top := filepath.FromSlash(strings.TrimSpace(string(out)))
	out, err = run(dir, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	return &Repo{Dir: top, GitDir: filepath.FromSlash(strings.TrimSpace(string(out)))}, nil
}

// Run executes git in the repo's top-level directory.
func (r *Repo) Run(args ...string) ([]byte, error) {
	return run(r.Dir, nil, nil, args...)
}

// RunEnv is Run with extra environment entries and optional stdin.
func (r *Repo) RunEnv(env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return run(r.Dir, env, stdin, args...)
}

// UnaskedDir returns (creating it with mode 0700) <GitDir>/unasked/<sub...>.
func (r *Repo) UnaskedDir(sub ...string) (string, error) {
	p := filepath.Join(append([]string{r.GitDir, "unasked"}, sub...)...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// RunGlobal runs git outside any repository (for example "version").
func RunGlobal(args ...string) ([]byte, error) {
	return run("", nil, nil, args...)
}
