// Package gittest builds throwaway git repositories for tests.
package gittest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type Repo struct {
	t   testing.TB
	Dir string
}

// New creates an empty repository on branch main in a temp dir.
func New(t testing.TB) *Repo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Repo{t: t, Dir: dir}
	r.Git("init", "-q", "-b", "main")
	return r
}

// Env is the isolated environment every test git process runs in: no global
// or system config (so a runner's core.autocrlf or user identity cannot leak
// in), a fixed identity and date, and the C locale. Append to it to override.
func Env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"LC_ALL=C")
}

// Cmd returns a git command rooted in the repo with the isolated Env.
func (r *Repo) Cmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = Env()
	return cmd
}

// Git runs git in the repo and returns trimmed stdout. Any failure fails the test.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := r.Cmd(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// Write creates or overwrites a file, creating parent directories.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	r.WriteBytes(path, []byte(content))
}

// WriteBytes is Write for raw bytes.
func (r *Repo) WriteBytes(path string, b []byte) {
	r.t.Helper()
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Read returns a working-tree file's content.
func (r *Repo) Read(path string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(path)))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// Remove deletes a file from the working tree; the next Commit records it.
func (r *Repo) Remove(path string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.Dir, filepath.FromSlash(path))); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits, returning the new commit SHA.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}
