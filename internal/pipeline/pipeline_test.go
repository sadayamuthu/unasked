package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/adapter/claudecode"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/keep"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/report"
)

// scenario commits fetch.go and colors.txt, then applies an agent session
// that adds a retry loop (asked for) and deletes "green" (not asked for).
func scenario(t *testing.T) (*gittest.Repo, *gitx.Repo) {
	t.Helper()
	g := gittest.New(t)
	g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n\treturn get(url)\n}\n")
	g.Write("colors.txt", "red\ngreen\nblue\n")
	g.Commit("init")
	retry := "\t// retry the fetch with exponential backoff\n\tfor attempt := 0; attempt < 3; attempt++ {\n\t\tif err := get(url); err == nil {\n\t\t\treturn nil\n\t\t}\n\t\tbackoff(attempt)\n\t}\n\treturn get(url)\n"
	g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n"+retry+"}\n")
	g.Write("colors.txt", "red\nblue\n")

	home := t.TempDir()
	p := filepath.Join(home, ".claude", "projects", "p", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := json.Marshal(g.Dir)
	ts := time.Now().UTC().Format(time.RFC3339)
	edit := func(id, file, oldS, newS, text string) string {
		f, _ := json.Marshal(filepath.Join(g.Dir, file))
		o, _ := json.Marshal(oldS)
		n, _ := json.Marshal(newS)
		x, _ := json.Marshal(text)
		return fmt.Sprintf(`{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Edit","input":{"file_path":%s,"old_string":%s,"new_string":%s}}]}}`, cwd, ts, x, id, f, o, n)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"user","content":"add a retry loop with exponential backoff to the fetch function"}}`, cwd, ts),
		edit("t1", "fetch.go", "\treturn get(url)\n", retry, "I will add the retry loop with exponential backoff to fetch."),
		edit("t2", "colors.txt", "red\ngreen\n", "red\n", "While I'm here, I removed green from the colors list."),
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := adapters
	adapters = func() []adapter.Adapter { return []adapter.Adapter{claudecode.New(home)} }
	t.Cleanup(func() { adapters = old })

	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return g, r
}

func verdicts(scored []model.Scored) map[string]model.Verdict {
	out := map[string]model.Verdict{}
	for _, s := range scored {
		out[s.Hunk.File] = s.Verdict
	}
	return out
}

func TestRunScoresSession(t *testing.T) {
	_, r := scenario(t)
	rep, scored, sel, err := Run(r, adapter.Request{}, invite.Default)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Sessions) != 1 || rep.Agent != "claude-code" || rep.Transcript != "present" {
		t.Fatalf("sessions %v, agent %q, transcript %q", sel.Sessions, rep.Agent, rep.Transcript)
	}
	v := verdicts(scored)
	if v["fetch.go"] != model.VerdictInvited || v["colors.txt"] != model.VerdictUnasked {
		t.Fatalf("verdicts %v", v)
	}
	if rep.Result != report.ResultUnasked || rep.ExitCode() != model.ExitUnasked {
		t.Errorf("result %q", rep.Result)
	}
	if rep.Thresholds != invite.Default {
		t.Errorf("thresholds %+v", rep.Thresholds)
	}
}

func TestRunShiftedThresholdsRecorded(t *testing.T) {
	_, r := scenario(t)
	th := invite.Default.Shift(invite.Step)
	rep, _, _, err := Run(r, adapter.Request{}, th)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Thresholds != th {
		t.Errorf("thresholds %+v, want %+v", rep.Thresholds, th)
	}
}

func TestRunUserKeepSilencesHunkButScoredKeepsIt(t *testing.T) {
	_, r := scenario(t)
	_, scored, _, err := Run(r, adapter.Request{}, invite.Default)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, s := range scored {
		if s.Hunk.File == "colors.txt" {
			hash = s.Hunk.Hash
		}
	}
	br, err := Branch(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := keep.Add(r, keep.Entry{Hash: hash, Branch: br, By: keep.ByUser, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rep, scored, _, err := Run(r, adapter.Request{}, invite.Default)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Result != report.ResultClean || len(rep.Kept) != 1 {
		t.Errorf("result %q kept %v", rep.Result, rep.Kept)
	}
	if len(scored) != 2 {
		t.Errorf("scored has %d hunks, want both (pre-keep)", len(scored))
	}
}

func TestRescoreUsesSameBase(t *testing.T) {
	g, r := scenario(t)
	_, _, sel, err := Run(r, adapter.Request{}, invite.Default)
	if err != nil {
		t.Fatal(err)
	}
	g.Write("colors.txt", "red\ngreen\nblue\n")
	_, scored, err := Rescore(r, sel, invite.Default)
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) != 1 || scored[0].Hunk.File != "fetch.go" || scored[0].Verdict != model.VerdictInvited {
		t.Fatalf("rescored %+v", verdicts(scored))
	}
}

func TestBranchDetached(t *testing.T) {
	g, r := scenario(t)
	if b, err := Branch(r); err != nil || b != "main" {
		t.Fatalf("branch %q, %v", b, err)
	}
	g.Git("checkout", "-q", "--detach")
	short := g.Git("rev-parse", "--short", "HEAD")
	if b, err := Branch(r); err != nil || b != "HEAD@"+short {
		t.Fatalf("detached branch %q, %v", b, err)
	}
}
