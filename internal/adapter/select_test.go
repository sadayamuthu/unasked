package adapter_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/adapter/claudecode"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
)

func setup(t *testing.T) (*gittest.Repo, *gitx.Repo) {
	t.Helper()
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Write("b.txt", "b\n")
	g.Commit("init")
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return g, r
}

type ev struct{ prompt, file string }

// session renders a Claude Code transcript with one prompt+edit per ev.
func session(cwd string, ts time.Time, evs ...ev) string {
	q, _ := json.Marshal(cwd)
	var b strings.Builder
	for i, e := range evs {
		t := ts.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
		f, _ := json.Marshal(filepath.Join(cwd, e.file))
		p, _ := json.Marshal(e.prompt)
		fmt.Fprintf(&b, `{"type":"user","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"user","content":%s}}`+"\n", q, t, p)
		fmt.Fprintf(&b, `{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t%d","name":"Edit","input":{"file_path":%s,"old_string":"a","new_string":"b"}}]}}`+"\n", q, t, i, f)
	}
	return b.String()
}

// native installs a native Claude session under a fresh home and returns the adapter.
func native(t *testing.T, id string, mod time.Time, content string) adapter.Adapter {
	t.Helper()
	home := t.TempDir()
	p := filepath.Join(home, ".claude", "projects", "p", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return claudecode.New(home)
}

// claude returns an adapter list that resolves captured Claude sessions
// regardless of what other tests did to the global registry.
func claude() []adapter.Adapter { return []adapter.Adapter{claudecode.New("")} }

func capture(t *testing.T, r *gitx.Repo, id, content string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RecordStop(r, "claude-code", id, src, nil); err != nil {
		t.Fatal(err)
	}
}

func hasHunkFor(sel *adapter.Selection, file string) bool {
	for _, h := range sel.Hunks {
		if h.File == file {
			return true
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// commitAt commits everything with the given committer date.
func commitAt(t *testing.T, g *gittest.Repo, date, msg string) {
	t.Helper()
	g.Git("add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-m", msg)
	cmd.Dir = g.Dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
}

func TestPrefersCapturedBaseline(t *testing.T) {
	g, r := setup(t)
	g.Write("b.txt", "developer wip\n") // pre-existing uncommitted work
	if _, err := adapter.RecordStart(r, "claude-code", "s1", g.Dir); err != nil {
		t.Fatal(err)
	}
	g.Write("a.txt", "agent change\n")
	capture(t, r, "s1", session(g.Dir, time.Now(), ev{"change a", "a.txt"}))

	sel, err := adapter.Select(r, adapter.Request{}, claude())
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource != "captured" || len(sel.Warnings) != 0 {
		t.Fatalf("source=%q warnings=%v", sel.BaseSource, sel.Warnings)
	}
	if !hasHunkFor(sel, "a.txt") || hasHunkFor(sel, "b.txt") {
		t.Fatalf("hunks = %+v", sel.Hunks)
	}
	if len(sel.Transcript.Edits) != 1 || sel.Caps.Level != adapter.Full || len(sel.Sessions) != 1 {
		t.Fatalf("sel = %+v", sel)
	}
}

func TestStopWithoutStartFallsBack(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "agent change\n")
	capture(t, r, "s1", session(g.Dir, time.Now(), ev{"change a", "a.txt"}))
	sel, err := adapter.Select(r, adapter.Request{}, claude())
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource == "captured" || !contains(sel.Warnings, "pre-existing changes may be included") {
		t.Fatalf("source=%q warnings=%v", sel.BaseSource, sel.Warnings)
	}
}

func TestFallbackBeforeStartCommit(t *testing.T) {
	g, r := setup(t) // init commit at 2026-01-01
	g.Write("b.txt", "committed before the session\n")
	commitAt(t, g, "2026-02-01T00:00:00Z", "mid")
	mid := g.Git("rev-parse", "HEAD^{tree}")
	g.Write("a.txt", "agent\n")
	commitAt(t, g, "2026-04-01T00:00:00Z", "agent commit")

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ad := native(t, "n1", time.Now(), session(g.Dir, start, ev{"change a", "a.txt"}))
	sel, err := adapter.Select(r, adapter.Request{}, []adapter.Adapter{ad})
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource != "reflog" || sel.FromTree != mid {
		t.Fatalf("source=%q from=%q want %q", sel.BaseSource, sel.FromTree, mid)
	}
	if !contains(sel.Warnings, "pre-existing changes may be included") {
		t.Fatalf("warnings = %v", sel.Warnings)
	}
	if !hasHunkFor(sel, "a.txt") || hasHunkFor(sel, "b.txt") {
		t.Fatalf("hunks = %+v", sel.Hunks)
	}
}

func TestFallbackHeadWhenNoOlderCommit(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "agent\n")
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // before every commit
	ad := native(t, "n1", time.Now(), session(g.Dir, start, ev{"change a", "a.txt"}))
	sel, err := adapter.Select(r, adapter.Request{}, []adapter.Adapter{ad})
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource != "head" || sel.FromTree != g.Git("rev-parse", "HEAD^{tree}") || !contains(sel.Warnings, "pre-existing changes may be included") {
		t.Fatalf("sel = %+v", sel)
	}
}

func TestNoTranscriptIsDiffOnly(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "changed\n")
	// A session that never touched a changed file does not intersect.
	ad := native(t, "n1", time.Now(), session(g.Dir, time.Now(), ev{"other", "b.txt"}))
	for _, ads := range [][]adapter.Adapter{nil, {ad}} {
		sel, err := adapter.Select(r, adapter.Request{}, ads)
		if err != nil {
			t.Fatal(err)
		}
		if sel.Transcript == nil || len(sel.Transcript.Edits) != 0 || len(sel.Sessions) != 0 {
			t.Fatalf("sel = %+v", sel)
		}
		if sel.Caps.Level != adapter.DiffOnly || !contains(sel.Warnings, "transcript: none") {
			t.Fatalf("caps=%+v warnings=%v", sel.Caps, sel.Warnings)
		}
		if !hasHunkFor(sel, "a.txt") {
			t.Fatalf("hunks = %+v", sel.Hunks)
		}
	}
}

func TestAllSessionsUnion(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "x\n")
	g.Write("b.txt", "y\n")
	now := time.Now()
	older := native(t, "old", now.Add(-2*time.Hour), session(g.Dir, now.Add(-3*time.Hour), ev{"p1", "a.txt"}, ev{"p2", "a.txt"}))
	newer := native(t, "new", now.Add(-1*time.Hour), session(g.Dir, now.Add(-90*time.Minute), ev{"p3", "b.txt"}))
	ads := []adapter.Adapter{older, newer}

	one, err := adapter.Select(r, adapter.Request{}, ads)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Sessions) != 1 || one.Sessions[0].ID != "new" {
		t.Fatalf("single selection = %+v", one.Sessions)
	}

	all, err := adapter.Select(r, adapter.Request{AllSessions: true}, ads)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Sessions) != 2 || len(all.Transcript.Edits) != 3 || len(all.Transcript.Prompts) != 3 {
		t.Fatalf("union = %+v", all.Transcript)
	}
	var turns, eturns []int
	for _, p := range all.Transcript.Prompts {
		turns = append(turns, p.Turn)
	}
	for _, e := range all.Transcript.Edits {
		eturns = append(eturns, e.Turn)
	}
	if fmt.Sprint(turns) != "[1 2 3]" || fmt.Sprint(eturns) != "[1 2 3]" || all.Transcript.Turns != 3 {
		t.Fatalf("prompt turns=%v edit turns=%v Turns=%d", turns, eturns, all.Transcript.Turns)
	}
	if all.Caps.Level != adapter.Full {
		t.Fatalf("caps = %+v", all.Caps)
	}
}

func TestSessionFilters(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "x\n")
	now := time.Now()
	a1 := native(t, "one", now.Add(-time.Hour), session(g.Dir, now.Add(-2*time.Hour), ev{"p", "a.txt"}))
	a2 := native(t, "two", now, session(g.Dir, now.Add(-time.Minute), ev{"p", "a.txt"}))
	sel, err := adapter.Select(r, adapter.Request{SessionID: "one"}, []adapter.Adapter{a1, a2})
	if err != nil || len(sel.Sessions) != 1 || sel.Sessions[0].ID != "one" {
		t.Fatalf("sel=%+v err=%v", sel, err)
	}
	sel, err = adapter.Select(r, adapter.Request{Agent: "codex"}, []adapter.Adapter{a1, a2})
	if err != nil || len(sel.Sessions) != 0 || sel.Caps.Level != adapter.DiffOnly {
		t.Fatalf("sel=%+v err=%v", sel, err)
	}
}

func TestSinceUsesMergeBase(t *testing.T) {
	g, r := setup(t)
	base := g.Git("rev-parse", "HEAD^{tree}")
	g.Git("checkout", "-q", "-b", "feat")
	g.Write("f.txt", "feature\n")
	g.Commit("feat")
	g.Git("checkout", "-q", "main")
	g.Write("m.txt", "main\n")
	g.Commit("main moves")
	g.Git("checkout", "-q", "feat")

	sel, err := adapter.Select(r, adapter.Request{Mode: adapter.RangeSince, Since: "main"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource != "since" || sel.FromTree != base {
		t.Fatalf("source=%q from=%q want %q", sel.BaseSource, sel.FromTree, base)
	}
	if !hasHunkFor(sel, "f.txt") || hasHunkFor(sel, "m.txt") {
		t.Fatalf("hunks = %+v", sel.Hunks)
	}
	if _, err := adapter.Select(r, adapter.Request{Mode: adapter.RangeSince, Since: "no-such-ref"}, nil); err == nil {
		t.Fatal("unknown ref must be an error")
	}
}

func TestStagedRange(t *testing.T) {
	g, r := setup(t)
	g.Write("a.txt", "staged\n")
	g.Git("add", "a.txt")
	g.Write("b.txt", "unstaged\n")
	sel, err := adapter.Select(r, adapter.Request{Mode: adapter.RangeStaged}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sel.BaseSource != "staged" || sel.FromTree != g.Git("rev-parse", "HEAD^{tree}") {
		t.Fatalf("sel = %+v", sel)
	}
	if !hasHunkFor(sel, "a.txt") || hasHunkFor(sel, "b.txt") {
		t.Fatalf("hunks = %+v", sel.Hunks)
	}
}

func TestStagedNoCommitsUsesEmptyTree(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "new\n")
	g.Git("add", "a.txt")
	r, _ := gitx.Open(g.Dir)
	sel, err := adapter.Select(r, adapter.Request{Mode: adapter.RangeStaged}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sel.FromTree != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" || !hasHunkFor(sel, "a.txt") {
		t.Fatalf("sel = %+v", sel)
	}
}

func TestFindSessionFromSubdir(t *testing.T) {
	g, _ := setup(t)
	sub := filepath.Join(g.Dir, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	g.Write("pkg/deep/x.txt", "new\n")
	// The agent ran in a subdirectory; Select runs from another one.
	ad := native(t, "n1", time.Now(), session(sub, time.Now(), ev{"add x", "x.txt"}))
	r, err := gitx.Open(filepath.Join(g.Dir, "pkg"))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := adapter.Select(r, adapter.Request{}, []adapter.Adapter{ad})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Sessions) != 1 || !hasHunkFor(sel, "pkg/deep/x.txt") {
		t.Fatalf("sel = %+v", sel)
	}
}

func TestAllSessionsWidestCapturedRange(t *testing.T) {
	g, r := setup(t)
	now := time.Now()
	// Session 1 edits a.txt; session 2 starts after that, so its own
	// baseline already contains the change.
	if _, err := adapter.RecordStart(r, "claude-code", "s1", g.Dir); err != nil {
		t.Fatal(err)
	}
	g.Write("a.txt", "from session one\n")
	capture(t, r, "s1", session(g.Dir, now.Add(-2*time.Hour), ev{"p1", "a.txt"}))
	setTimes(t, r, "s1", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	if _, err := adapter.RecordStart(r, "claude-code", "s2", g.Dir); err != nil {
		t.Fatal(err)
	}
	g.Write("b.txt", "from session two\n")
	capture(t, r, "s2", session(g.Dir, now.Add(-time.Hour), ev{"p2", "b.txt"}))
	setTimes(t, r, "s2", now.Add(-time.Hour), now.Add(-30*time.Minute))

	one, err := adapter.Select(r, adapter.Request{}, claude())
	if err != nil {
		t.Fatal(err)
	}
	if hasHunkFor(one, "a.txt") {
		t.Fatalf("newest-only range should not contain a.txt: %+v", one.Hunks)
	}
	all, err := adapter.Select(r, adapter.Request{AllSessions: true}, claude())
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Sessions) != 2 || all.BaseSource != "captured" || len(all.Warnings) != 0 {
		t.Fatalf("sel = %+v", all)
	}
	if !hasHunkFor(all, "a.txt") || !hasHunkFor(all, "b.txt") {
		t.Fatalf("hunks = %+v", all.Hunks)
	}
}

// setTimes pins a captured session's baseline and update times so ordering
// does not depend on the clock.
func setTimes(t *testing.T, r *gitx.Repo, id string, taken, updated time.Time) {
	t.Helper()
	dir, err := adapter.CaptureDir(r, "claude-code", id)
	if err != nil {
		t.Fatal(err)
	}
	m, err := adapter.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Start.Taken, m.Updated = taken, updated
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFailedReadSurvivesIntersectingSession(t *testing.T) {
	g, r := setup(t)
	now := time.Now()
	if _, err := adapter.RecordStart(r, "claude-code", "good", g.Dir); err != nil {
		t.Fatal(err)
	}
	g.Write("a.txt", "agent\n")
	capture(t, r, "good", session(g.Dir, now, ev{"p", "a.txt"}))
	setTimes(t, r, "good", now.Add(-2*time.Hour), now.Add(-time.Hour))
	capture(t, r, "bad", "not json at all\n")
	setTimes(t, r, "bad", now.Add(-time.Hour), now) // newer, examined first

	sel, err := adapter.Select(r, adapter.Request{}, claude())
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Sessions) != 1 || sel.Sessions[0].ID != "good" {
		t.Fatalf("sessions = %+v", sel.Sessions)
	}
	if !strings.Contains(sel.Caps.Degraded, "claude-code") {
		t.Fatalf("Degraded = %q", sel.Caps.Degraded)
	}
	found := false
	for _, w := range sel.Warnings {
		found = found || strings.HasPrefix(w, "transcript: claude-code")
	}
	if !found {
		t.Fatalf("warnings = %v", sel.Warnings)
	}
}
