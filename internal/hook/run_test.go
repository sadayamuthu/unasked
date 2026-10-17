package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	_ "github.com/sadayamuthu/unasked/internal/adapter/claudecode" // registers the adapter
	_ "github.com/sadayamuthu/unasked/internal/adapter/codex"      // registers the adapter
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/report"
)

// TestMain re-runs the test binary with UNASKED_HOME and HOME pointing at a
// temp dir: the adapters read UNASKED_HOME in their init functions, so it
// must be set before the process starts, and no test may read the real home.
func TestMain(m *testing.M) {
	if os.Getenv("UNASKED_TEST_CHILD") == "" {
		home, err := os.MkdirTemp("", "unasked-home")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), "UNASKED_TEST_CHILD=1", "UNASKED_HOME="+home, "HOME="+home,
			"USERPROFILE="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "CODEX_HOME=")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		os.RemoveAll(home)
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			os.Exit(ee.ExitCode())
		case err != nil:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const retry = "\t// retry the fetch with exponential backoff\n\tfor attempt := 0; attempt < 3; attempt++ {\n\t\tif err := get(url); err == nil {\n\t\t\treturn nil\n\t\t}\n\t\tbackoff(attempt)\n\t}\n\treturn get(url)\n"

// session is a test repo with a Claude Code transcript outside it.
type session struct {
	t          *testing.T
	g          *gittest.Repo
	transcript string
	lines      []string
}

func newSession(t *testing.T) *session {
	t.Helper()
	g := gittest.New(t)
	g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n\treturn get(url)\n}\n")
	g.Write("colors.txt", "red\ngreen\nblue\n")
	g.Commit("init")
	dir := filepath.Join(t.TempDir(), "projects", "p")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &session{t: t, g: g, transcript: filepath.Join(dir, "s1.jsonl")}
	cwd, _ := json.Marshal(g.Dir)
	s.add(fmt.Sprintf(`{"type":"user","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"user","content":"add a retry loop with exponential backoff to the fetch function"}}`, cwd, stamp()))
	return s
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *session) add(line string) {
	s.lines = append(s.lines, line)
	if err := os.WriteFile(s.transcript, []byte(strings.Join(s.lines, "\n")+"\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// edit applies an agent edit to the work tree and records it in the transcript.
func (s *session) edit(id, file, oldS, newS, text string) {
	full := s.g.Read(file)
	s.g.Write(file, strings.Replace(full, oldS, newS, 1))
	cwd, _ := json.Marshal(s.g.Dir)
	f, _ := json.Marshal(filepath.Join(s.g.Dir, file))
	o, _ := json.Marshal(oldS)
	n, _ := json.Marshal(newS)
	x, _ := json.Marshal(text)
	s.add(fmt.Sprintf(`{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Edit","input":{"file_path":%s,"old_string":%s,"new_string":%s}}]}}`, cwd, stamp(), x, id, f, o, n))
}

func (s *session) askedEdit() {
	s.edit("t1", "fetch.go", "\treturn get(url)\n", retry, "I will add the retry loop with exponential backoff to fetch.")
}

func (s *session) unaskedEdit() {
	s.edit("t2", "colors.txt", "red\ngreen\n", "red\n", "While I'm here, I removed green from the colors list.")
}

func (s *session) payload(event string, active bool) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "transcript_path": s.transcript, "cwd": s.g.Dir,
		"hook_event_name": event, "stop_hook_active": active, "permission_mode": "default",
	})
	return string(b)
}

// promptPayload is a UserPromptSubmit payload carrying prompt.
func (s *session) promptPayload(prompt string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "transcript_path": s.transcript, "cwd": s.g.Dir,
		"hook_event_name": "UserPromptSubmit", "prompt": prompt, "permission_mode": "default",
	})
	return string(b)
}

// run calls Run and returns its exit code, stdout and stderr.
func (s *session) run(agent, stdin string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(agent, strings.NewReader(stdin), &out, &errb, time.Now())
	return code, out.String(), errb.String()
}

func (s *session) repo() *gitx.Repo {
	r, err := gitx.Open(s.g.Dir)
	if err != nil {
		s.t.Fatal(err)
	}
	return r
}

func (s *session) captureDir() string {
	d, err := adapter.CaptureDir(s.repo(), "claude-code", "s1")
	if err != nil {
		s.t.Fatal(err)
	}
	return d
}

func (s *session) meta() *adapter.Meta {
	m, err := adapter.LoadMeta(s.captureDir())
	if err != nil {
		s.t.Fatal(err)
	}
	return m
}

func (s *session) hookLog() string {
	b, _ := os.ReadFile(filepath.Join(s.repo().GitDir, "unasked", "hook.log"))
	return string(b)
}

// started returns a session whose start hook has run.
func started(t *testing.T) *session {
	s := newSession(t)
	if code, out, errs := s.run("claude-code", s.payload("SessionStart", false)); code != 0 || out != "" || errs != "" {
		t.Fatalf("start: exit %d stdout %q stderr %q", code, out, errs)
	}
	return s
}

func decode(t *testing.T, out string) (decision, reason string) {
	t.Helper()
	var v struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	return v.Decision, v.Reason
}

func TestRunStartRecordsBaseline(t *testing.T) {
	s := newSession(t)
	head := s.g.Git("rev-parse", "HEAD")
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		if code, out, _ := s.run("claude-code", s.payload(ev, false)); code != 0 || out != "" {
			t.Fatalf("%s: exit %d stdout %q", ev, code, out)
		}
	}
	m := s.meta()
	if m.Start.Commit != head || m.Start.Tree == "" || m.Stop != nil || m.Cwd != s.g.Dir {
		t.Errorf("meta %+v", m)
	}
	if s.hookLog() != "" {
		t.Errorf("hook.log written: %s", s.hookLog())
	}
}

func TestRunStopBlocksOnUnasked(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errs)
	}
	decision, reason := decode(t, out)
	if decision != "block" || !strings.HasPrefix(reason, "[unasked report]\n") {
		t.Fatalf("decision %q reason:\n%s", decision, reason)
	}
	if !strings.Contains(reason, "colors.txt") || !strings.HasSuffix(strings.TrimSpace(reason), instruction) {
		t.Errorf("reason:\n%s", reason)
	}
	dir := s.captureDir()
	if m := s.meta(); m.Feedbacks != 1 || m.Stop == nil {
		t.Errorf("meta feedbacks %d stop %v", m.Feedbacks, m.Stop)
	}
	var rep report.Report
	if err := json.Unmarshal(readFile(t, filepath.Join(dir, "report.json")), &rep); err != nil || rep.Result != report.ResultUnasked || rep.Session != "s1" {
		t.Errorf("report.json result %q session %q err %v", rep.Result, rep.Session, err)
	}
	// The agent's `unasked --drop <ids>` resolves against last.json.
	last, err := report.LoadLast(s.repo())
	if err != nil || last.Result != report.ResultUnasked {
		t.Errorf("last.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transcript.jsonl")); err != nil {
		t.Error("transcript not captured")
	}
}

// TestRunStopRequestedDeletionDoesNotBlock: a deletion the prompt names
// specifically is unclear, never invited (R36g), and only an unasked result
// is fed back, so the agent is not blocked.
func TestRunStopRequestedDeletionDoesNotBlock(t *testing.T) {
	s := newSession(t)
	legacy := "// legacyPing answers the old health check.\nfunc legacyPing() string {\n\treturn \"pong\"\n}\n"
	s.g.Write("legacy.go", "package main\n\n"+legacy)
	s.g.Commit("legacy")
	if code, out, errs := s.run("claude-code", s.payload("SessionStart", false)); code != 0 || out != "" || errs != "" {
		t.Fatalf("start: exit %d stdout %q stderr %q", code, out, errs)
	}
	cwd, _ := json.Marshal(s.g.Dir)
	s.add(fmt.Sprintf(`{"type":"user","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"user","content":"delete the legacyPing function from legacy.go, nothing calls it any more"}}`, cwd, stamp()))
	s.edit("t3", "legacy.go", legacy, "", "Deleted legacyPing as you asked.")
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	var rep report.Report
	if err := json.Unmarshal(readFile(t, filepath.Join(s.captureDir(), "report.json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Result != report.ResultUnclear || len(rep.Hunks) != 1 {
		t.Fatalf("result %q hunks %+v", rep.Result, rep.Hunks)
	}
	h := rep.Hunks[0]
	if !h.HighRisk || h.Verdict != "unclear" || h.Severity != "warning" || h.Reason != "you may have asked for this (turn 2) — confirm with unasked --keep" {
		t.Errorf("hunk %s high-risk %v verdict %s severity %s reason %q", h.File, h.HighRisk, h.Verdict, h.Severity, h.Reason)
	}
}

func TestRunStopCopiesSubagents(t *testing.T) {
	s := started(t)
	sub := filepath.Join(strings.TrimSuffix(s.transcript, ".jsonl"), "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.askedEdit()
	s.run("claude-code", s.payload("Stop", false))
	if _, err := os.Stat(filepath.Join(s.captureDir(), "subagents", "agent-1.jsonl")); err != nil {
		t.Errorf("subagent transcript not captured: %v", err)
	}
}

func TestRunStopCleanSilent(t *testing.T) {
	s := started(t)
	s.askedEdit()
	m := s.meta()
	m.Feedbacks = 2
	if err := adapter.SaveMeta(s.captureDir(), m); err != nil {
		t.Fatal(err)
	}
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	if m := s.meta(); m.Feedbacks != 0 {
		t.Errorf("feedbacks %d, want reset to 0", m.Feedbacks)
	}
	if _, err := os.Stat(filepath.Join(s.captureDir(), "report.json")); err != nil {
		t.Error("report.json not written")
	}
}

func TestRunRespectsStopHookActive(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	code, out, _ := s.run("claude-code", s.payload("Stop", true))
	if code != 0 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if m := s.meta(); m.Feedbacks != 0 {
		t.Errorf("feedbacks %d", m.Feedbacks)
	}
}

func TestRunCapsAtThree(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	for i := 1; i <= 3; i++ {
		_, out, _ := s.run("claude-code", s.payload("Stop", false))
		if d, _ := decode(t, out); d != "block" {
			t.Fatalf("stop %d: not blocked", i)
		}
	}
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" {
		t.Fatalf("4th stop: exit %d stdout %q", code, out)
	}
	if !strings.Contains(errs, "unasked: 3 consecutive reports ignored; not blocking again this session") {
		t.Errorf("stderr %q", errs)
	}
	if m := s.meta(); m.Feedbacks != 3 {
		t.Errorf("feedbacks %d", m.Feedbacks)
	}
}

func TestRunNewPromptResetsCap(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	for i := 1; i <= 3; i++ {
		if _, out, _ := s.run("claude-code", s.payload("Stop", false)); out == "" {
			t.Fatalf("stop %d: not blocked", i)
		}
	}
	if _, out, _ := s.run("claude-code", s.payload("Stop", false)); out != "" {
		t.Fatalf("4th stop blocked: %q", out)
	}
	if code, out, _ := s.run("claude-code", s.promptPayload("now also add a timeout to fetch")); code != 0 || out != "" {
		t.Fatalf("prompt: exit %d stdout %q", code, out)
	}
	if m := s.meta(); m.Feedbacks != 0 {
		t.Fatalf("feedbacks %d after a new prompt, want 0", m.Feedbacks)
	}
	_, out, _ := s.run("claude-code", s.payload("Stop", false))
	if d, _ := decode(t, out); d != "block" {
		t.Fatal("stop after a new prompt not blocked")
	}
	if m := s.meta(); m.Feedbacks != 1 {
		t.Errorf("feedbacks %d, want 1", m.Feedbacks)
	}
}

// An agent that delivers the block reason as a new prompt (a Stop-block
// continuation) must not reset the cap, or it would loop forever.
func TestRunReportPromptKeepsCap(t *testing.T) {
	s := newSession(t)
	if code, out, _ := s.run("codex", s.payload("SessionStart", false)); code != 0 || out != "" {
		t.Fatalf("start: exit %d stdout %q", code, out)
	}
	s.askedEdit()
	s.unaskedEdit()
	s.codexTranscript()
	for i := 1; i <= 3; i++ {
		_, out, _ := s.run("codex", s.payload("Stop", false))
		_, reason := decode(t, out)
		if code, _, _ := s.run("codex", s.promptPayload("  "+reason)); code != 0 {
			t.Fatalf("prompt exit %d", code)
		}
	}
	if _, out, errs := s.run("codex", s.payload("Stop", false)); out != "" || !strings.Contains(errs, capWarning) {
		t.Fatalf("4th stop: stdout %q stderr %q", out, errs)
	}
	d, err := adapter.CaptureDir(s.repo(), "codex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := adapter.LoadMeta(d); err != nil || m.Feedbacks != 3 {
		t.Errorf("feedbacks %v, %v; want 3", m, err)
	}
}

func TestRunDeadLockOwnerTakenOver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PID liveness is not checked on Windows")
	}
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	cmd := exec.Command("git", "--version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := cmd.Process.Pid
	lock := filepath.Join(s.captureDir(), lockName)
	if err := os.WriteFile(lock, []byte(strconv.Itoa(dead)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, out, _ := s.run("claude-code", s.payload("Stop", false)); out == "" {
		t.Fatalf("lock of a dead process not taken over; log %s", s.hookLog())
	}
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Errorf("took %v: waited for a dead owner", el)
	}
}

func TestRunCodexStartAndRespond(t *testing.T) {
	s := newSession(t)
	code, out, _ := s.run("codex", s.payload("SessionStart", false))
	if code != 0 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	d, err := adapter.CaptureDir(s.repo(), "codex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := adapter.LoadMeta(d); err != nil || m.Start.Tree == "" {
		t.Fatalf("codex start not recorded: %v", err)
	}
	var b bytes.Buffer
	rep := &report.Report{Version: 1, Agent: "codex", Session: "s1", Result: report.ResultUnasked}
	if code := Codex.Respond(&b, rep, false); code != 0 || b.Len() != 0 {
		t.Fatalf("no-feedback respond: exit %d output %q", code, b.String())
	}
	if code := Codex.Respond(&b, rep, true); code != 0 {
		t.Fatalf("respond exit %d", code)
	}
	if dec, reason := decode(t, b.String()); dec != "block" || !strings.HasPrefix(reason, "[unasked report]\n") || !strings.HasSuffix(reason, instruction) {
		t.Errorf("codex response %q", b.String())
	}
}

func TestRunNeverFails(t *testing.T) {
	s := newSession(t)
	t.Chdir(s.g.Dir)
	for _, in := range []string{"not json", `{"hook_event_name":"Stop"}`, `{"hook_event_name":"Stop","session_id":"x","cwd":` + mustJSON(s.g.Dir) + `,"transcript_path":"/nonexistent/t.jsonl"}`} {
		code, out, _ := s.run("claude-code", in)
		if code != 0 || out != "" {
			t.Errorf("%q: exit %d stdout %q", in, code, out)
		}
	}
	if code, _, _ := s.run("no-such-agent", s.payload("Stop", false)); code != 0 {
		t.Errorf("unknown agent exit %d", code)
	}
	log := s.hookLog()
	if strings.Count(log, "\n") < 4 || !strings.Contains(log, "claude-code") {
		t.Errorf("hook.log:\n%s", log)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRunNotARepoExit0(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	in := `{"hook_event_name":"Stop","session_id":"s1","cwd":` + mustJSON(dir) + `,"transcript_path":"/x.jsonl"}`
	var out, errb bytes.Buffer
	if code := Run("claude-code", strings.NewReader(in), &out, &errb, time.Now()); code != 0 || out.Len() != 0 || errb.Len() != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote into a non-repo: %v", entries)
	}
}

func TestRunLockedSkipsFeedback(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	lock := filepath.Join(s.captureDir(), lockName)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, _ := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if el := time.Since(start); el > 1900*time.Millisecond {
		t.Errorf("took %v waiting for the lock", el)
	}
	if !strings.Contains(s.hookLog(), "lock") {
		t.Errorf("hook.log: %q", s.hookLog())
	}
	// A stale lock (crashed hook) is taken over.
	old := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := s.run("claude-code", s.payload("Stop", false)); out == "" {
		t.Error("stale lock not taken over")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lock left behind")
	}
}

func TestHookRunUnder2s(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	start := time.Now()
	s.run("claude-code", s.payload("Stop", false))
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("hook run took %v, want < 2s", el)
	}
}

// TestRunStopQuestionOnlyKeepsPreexistingWork is the C1 regression: the
// developer's own uncommitted test edit predates the session, and the turn
// only answered a question. The Stop hook must not block (it would tell the
// agent to drop the developer's work), and the pre-existing file must not
// appear in the report: the session's captured baseline already holds it.
func TestRunStopQuestionOnlyKeepsPreexistingWork(t *testing.T) {
	s := newSession(t)
	s.g.Write("fetch_test.go", "package main\n\nimport \"testing\"\n\nfunc TestFetch(t *testing.T) {}\n")
	s.g.Commit("test")
	s.g.Write("fetch_test.go", "package main\n\nimport \"testing\"\n\nfunc TestFetch(t *testing.T) {\n\tt.Skip(\"flaky\")\n}\n")
	if code, out, errs := s.run("claude-code", s.payload("SessionStart", false)); code != 0 || out != "" || errs != "" {
		t.Fatalf("start: exit %d stdout %q stderr %q", code, out, errs)
	}
	cwd, _ := json.Marshal(s.g.Dir)
	s.add(fmt.Sprintf(`{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"fetch calls get once and returns its error; there is no retry today."}]}}`, cwd, stamp()))
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	var rep report.Report
	if err := json.Unmarshal(readFile(t, filepath.Join(s.captureDir(), "report.json")), &rep); err != nil {
		t.Fatal(err)
	}
	for _, h := range rep.Hunks {
		if h.File == "fetch_test.go" {
			t.Errorf("pre-existing file in the report: %+v", h)
		}
	}
	if rep.Session != "s1" || rep.Range.BaseSource != "captured" {
		t.Errorf("session %q base source %q", rep.Session, rep.Range.BaseSource)
	}
}

// TestRunStopNoFeedbackWithoutAlignedEdit: with the transcript's edits
// unreadable (an unknown Claude Code major version keeps prompts only), an
// unasked hunk has no agent edit behind it, so the hook never tells the
// agent to drop it.
func TestRunStopNoFeedbackWithoutAlignedEdit(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.unaskedEdit()
	for i, l := range s.lines {
		s.lines[i] = strings.ReplaceAll(l, `"version":"2.1.289"`, `"version":"9.0.0"`)
	}
	s.add(`{"type":"system"}`)
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	if m := s.meta(); m.Feedbacks != 0 {
		t.Errorf("feedbacks %d", m.Feedbacks)
	}
}

// TestRunStopNoFeedbackWhenDegraded: an unreadable transcript leaves the
// report at diff-only, where nothing ties a hunk to the agent; the hook must
// not block and tell the agent to drop it.
func TestRunStopNoFeedbackWhenDegraded(t *testing.T) {
	s := started(t)
	s.unaskedEdit()
	s.lines = nil
	s.add("not json at all")
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	var rep report.Report
	if err := json.Unmarshal(readFile(t, filepath.Join(s.captureDir(), "report.json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Result != report.ResultUnasked || rep.Transcript != "degraded" {
		t.Errorf("result %q transcript %q (the report itself must still list it)", rep.Result, rep.Transcript)
	}
}

// codexTranscript replaces the transcript with a Codex rollout in which the
// agent was asked for the retry loop and also removed green from colors.txt
// (the work tree edits are the ones askedEdit and unaskedEdit made).
func (s *session) codexTranscript() {
	cwd, _ := json.Marshal(s.g.Dir)
	retryPatch, _ := json.Marshal("*** Begin Patch\n*** Update File: fetch.go\n@@\n-\treturn get(url)\n+" + strings.ReplaceAll(strings.TrimSuffix(retry, "\n"), "\n", "\n+") + "\n*** End Patch")
	colorsPatch, _ := json.Marshal("*** Begin Patch\n*** Update File: colors.txt\n@@\n red\n-green\n*** End Patch")
	ts := stamp()
	s.lines = nil
	s.add(fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"s1","cwd":%s,"cli_version":"0.99.0","timestamp":%q}}`, ts, cwd, ts))
	s.add(fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"user_message","message":"add a retry loop with exponential backoff to the fetch function"}}`, ts))
	s.add(fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":%s,"call_id":"c1"}}`, ts, retryPatch))
	s.add(fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"While I'm here, I removed green from the colors list."}]}}`, ts))
	s.add(fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":%s,"call_id":"c2"}}`, ts, colorsPatch))
}

// TestRunStopShellMadeDoesNotBlock (R39): a hunk only a shell command (or
// the developer) explains is listed as unasked but never fed back on its
// own.
func TestRunStopShellMadeDoesNotBlock(t *testing.T) {
	s := started(t)
	s.askedEdit()
	s.g.Write("colors.txt", "red\nblue\n")
	cwd, _ := json.Marshal(s.g.Dir)
	s.add(fmt.Sprintf(`{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"sed -i '' /green/d colors.txt"}}]}}`, cwd, stamp()))
	code, out, errs := s.run("claude-code", s.payload("Stop", false))
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("exit %d stdout %q stderr %q log %s", code, out, errs, s.hookLog())
	}
	var rep report.Report
	if err := json.Unmarshal(readFile(t, filepath.Join(s.captureDir(), "report.json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Result != report.ResultUnasked {
		t.Errorf("result %q: the shell-made deletion must still be listed", rep.Result)
	}
}
