package adapter

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
)

func openRepo(t *testing.T, g *gittest.Repo) *gitx.Repo {
	t.Helper()
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newRepo(t *testing.T) (*gittest.Repo, *gitx.Repo) {
	g := gittest.New(t)
	g.Write("f.txt", "x\n")
	g.Commit("init")
	return g, openRepo(t, g)
}

func TestRecordStartIdempotent(t *testing.T) {
	g, r := newRepo(t)
	m1, err := RecordStart(r, "claude-code", "s1", g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	g.Write("f.txt", "changed\n")
	m2, err := RecordStart(r, "claude-code", "s1", g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Start.Tree == "" || m2.Start.Tree != m1.Start.Tree {
		t.Errorf("Start.Tree = %q, want first %q", m2.Start.Tree, m1.Start.Tree)
	}
}

func TestRecordStopCopiesTranscript(t *testing.T) {
	g, r := newRepo(t)
	src := filepath.Join(t.TempDir(), "t.jsonl")
	data := []byte("{\"a\":1}\n{\"b\":2}\n")
	os.WriteFile(src, data, 0o644)
	sub := filepath.Join(t.TempDir(), "subagents")
	os.MkdirAll(filepath.Join(sub, "deep"), 0o755)
	os.WriteFile(filepath.Join(sub, "deep", "a.jsonl"), []byte("z"), 0o644)

	if _, err := RecordStart(r, "claude-code", "s1", g.Dir); err != nil {
		t.Fatal(err)
	}
	m, err := RecordStop(r, "claude-code", "s1", src, []string{sub, filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Stop == nil || m.Stop.Tree == "" {
		t.Fatal("Stop not recorded")
	}
	dir, _ := CaptureDir(r, "claude-code", "s1")
	got, err := os.ReadFile(filepath.Join(dir, "transcript.jsonl"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("copy mismatch: %v %q", err, got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "subagents", "deep", "a.jsonl")); err != nil || string(b) != "z" {
		t.Errorf("extra dir not copied: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{"transcript.jsonl", "meta.json", "subagents/deep/a.jsonl"} {
			st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v mode %v", p, err, st.Mode())
			}
		}
		st, _ := os.Stat(filepath.Join(dir, "subagents"))
		if st.Mode().Perm() != 0o700 {
			t.Errorf("subagents mode %v", st.Mode().Perm())
		}
	}
	l, err := LoadMeta(dir)
	if err != nil || l.Start.Tree != m.Start.Tree {
		t.Errorf("LoadMeta: %v %+v", err, l)
	}
}

func TestRecordStopWithoutStartAndMissingTranscript(t *testing.T) {
	_, r := newRepo(t)
	if _, err := RecordStop(r, "a", "s", filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Fatal("want error for missing transcript")
	}
	src := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(src, []byte("x\n"), 0o600)
	m, err := RecordStop(r, "a", "s", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Start.Tree != "" || m.Stop == nil {
		t.Errorf("Start should be zero, Stop set: %+v", m)
	}
}

func TestListCapturedNewestFirst(t *testing.T) {
	g, r := newRepo(t)
	for _, id := range []string{"a", "b"} {
		if _, err := RecordStart(r, "codex", id, g.Dir); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ss, err := ListCaptured(r)
	if err != nil || len(ss) != 2 {
		t.Fatalf("%v %v", err, ss)
	}
	if ss[0].ID != "b" || !ss[0].Captured || ss[0].Agent != "codex" || ss[0].Meta == nil || ss[0].Cwd != g.Dir {
		t.Errorf("bad session %+v", ss[0])
	}
}

func mkSessions(t *testing.T, r *gitx.Repo, n, old int) {
	for i := 0; i < n; i++ {
		dir, err := CaptureDir(r, "x", fmt.Sprintf("s%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		age := time.Duration(i+1) * time.Minute
		if i < old {
			age = 31*24*time.Hour + time.Duration(i)*time.Minute
		}
		m := &Meta{Agent: "x", Session: fmt.Sprint(i), Updated: time.Now().Add(-age)}
		if err := writeMeta(dir, m); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneKeepsFewer(t *testing.T) {
	for _, c := range []struct{ n, old, want int }{{60, 10, 50}, {20, 15, 5}} {
		_, r := newRepo(t)
		mkSessions(t, r, c.n, c.old)
		if err := Prune(r, 50, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		ss, _ := ListCaptured(r)
		if len(ss) != c.want {
			t.Errorf("n=%d old=%d: %d remain, want %d", c.n, c.old, len(ss), c.want)
		}
	}
}

func TestCaptureDirPerWorktree(t *testing.T) {
	g, r := newRepo(t)
	wt := filepath.Join(filepath.Dir(g.Dir), filepath.Base(g.Dir)+"-wt")
	g.Git("worktree", "add", "-q", wt, "-b", "other")
	r2, err := gitx.Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := CaptureDir(r, "a", "s")
	d2, _ := CaptureDir(r2, "a", "s")
	if d1 == d2 || !strings.HasPrefix(d2, r2.GitDir) {
		t.Errorf("dirs not per worktree: %q %q", d1, d2)
	}
}

func TestSessionIDSanitized(t *testing.T) {
	_, r := newRepo(t)
	base, _ := r.UnaskedDir("sessions")
	for _, id := range []string{"../evil", "..", ".", "", "a/b\\c", strings.Repeat("x", 500)} {
		d, err := CaptureDir(r, "a", id)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(d) != base {
			t.Errorf("id %q escapes: %q", id, d)
		}
		if len(filepath.Base(d)) > 140 {
			t.Errorf("name too long for %q", id)
		}
	}
	x1, _ := CaptureDir(r, "a", "a/b")
	x2, _ := CaptureDir(r, "a", "a?b")
	if x1 == x2 {
		t.Error("sanitised collisions must differ")
	}
	if ok, _ := CaptureDir(r, "a", "plain-1.2_x"); filepath.Base(ok) != "a-plain-1.2_x" {
		t.Errorf("clean id altered: %q", ok)
	}
	d1, _ := CaptureDir(r, "a", "..")
	d2, _ := CaptureDir(r, "a", ".")
	if d1 == d2 {
		t.Error("hashed ids should differ")
	}
}

func TestPruneRemovesOrphanDir(t *testing.T) {
	_, r := newRepo(t)
	dir, _ := CaptureDir(r, "x", "orphan")
	os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte("secret"), 0o600)
	old := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(dir, old, old)
	fresh, _ := CaptureDir(r, "x", "fresh")
	if err := Prune(r, 50, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("orphan dir not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh orphan removed")
	}
}

func TestCorruptMetaRecovers(t *testing.T) {
	g, r := newRepo(t)
	dir, _ := CaptureDir(r, "a", "s")
	os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{trunc"), 0o600)
	src := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(src, []byte("x\n"), 0o600)
	if _, err := RecordStop(r, "a", "s", src, nil); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadMeta(dir); err != nil || m.Stop == nil {
		t.Errorf("meta not rewritten: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{trunc"), 0o600)
	if _, err := RecordStart(r, "a", "s", g.Dir); err != nil {
		t.Fatal(err)
	}
}

func TestRecordStopRejectsBadInputs(t *testing.T) {
	_, r := newRepo(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "t.jsonl")
	os.WriteFile(src, []byte("x\n"), 0o600)
	if runtime.GOOS != "windows" {
		link := filepath.Join(tmp, "link.jsonl")
		os.Symlink(src, link)
		if _, err := RecordStop(r, "a", "s", link, nil); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("symlink transcript err = %v", err)
		}
	}
	d1, d2 := filepath.Join(tmp, "one"), filepath.Join(tmp, "two", "one")
	os.MkdirAll(d1, 0o755)
	os.MkdirAll(d2, 0o755)
	os.WriteFile(filepath.Join(d1, "f"), []byte("first"), 0o600)
	os.WriteFile(filepath.Join(d2, "f"), []byte("second"), 0o600)
	meta := filepath.Join(tmp, "meta.json")
	os.WriteFile(meta, []byte("evil"), 0o600)
	if _, err := RecordStop(r, "a", "s", src, []string{d1, d2, meta, "..", "."}); err != nil {
		t.Fatal(err)
	}
	dir, _ := CaptureDir(r, "a", "s")
	if b, _ := os.ReadFile(filepath.Join(dir, "one", "f")); string(b) != "first" {
		t.Errorf("duplicate overwrote: %q", b)
	}
	if m, err := LoadMeta(dir); err != nil || m.Agent != "a" {
		t.Errorf("meta.json clobbered: %v", err)
	}
}
