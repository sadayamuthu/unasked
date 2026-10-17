package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/model"
)

// fixture loads testdata/name with @CWD@ replaced by cwd.
func fixture(t *testing.T, name, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(cwd)
	return strings.ReplaceAll(string(b), "@CWD@", string(q[1:len(q)-1]))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// capture lays out a captured dir holding transcript.jsonl and parses it.
func capture(t *testing.T, content string) (*model.Transcript, adapter.Capabilities) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "transcript.jsonl"), content)
	tr, caps, err := New(t.TempDir()).ParseCaptured(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tr, caps
}

func line(typ, cwd, extra string) string {
	q, _ := json.Marshal(cwd)
	return `{"type":"` + typ + `","cwd":` + string(q) + `,"version":"2.1.289","timestamp":"2026-10-14T10:00:00.000Z",` + extra + `}`
}

func userLine(cwd, content string) string {
	return line("user", cwd, `"message":{"role":"user","content":`+content+`}`)
}

func TestParsePromptsAndEdits(t *testing.T) {
	cwd := t.TempDir()
	tr, caps := capture(t, fixture(t, "main.jsonl", cwd))

	wantPrompts := []model.Prompt{{Turn: 1, Text: "add a retry to the fetcher"}, {Turn: 2, Text: "yes"}}
	if len(tr.Prompts) != 2 || tr.Prompts[0] != wantPrompts[0] || tr.Prompts[1] != wantPrompts[1] {
		t.Fatalf("prompts = %+v", tr.Prompts)
	}
	wantEdits := []model.EditEvent{
		{Turn: 1, File: "pkg/fetch.go", After: "retry()", Statement: "I will add the retry now."},
		{Turn: 1, File: "README.md", After: "first\nsecond", Statement: "Now the docs."},
		{Turn: 2, File: "new.txt", After: "hello\n", Statement: "Creating the file."},
		{Turn: 2, Command: "gofmt -w .", Mechanical: true, Statement: "Formatting."},
		{Turn: 2, File: "/elsewhere/n.ipynb", After: "print(1)", Statement: "Notebook."},
	}
	if len(tr.Edits) != len(wantEdits) {
		t.Fatalf("edits = %+v", tr.Edits)
	}
	for i, w := range wantEdits {
		if tr.Edits[i] != w {
			t.Errorf("edit %d = %+v, want %+v", i, tr.Edits[i], w)
		}
	}
	if tr.Version != "2.1.289" || tr.Turns != 2 {
		t.Errorf("version=%q turns=%d", tr.Version, tr.Turns)
	}
	if !tr.Start.Equal(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)) || !tr.End.Equal(time.Date(2026, 10, 14, 10, 1, 9, 0, time.UTC)) {
		t.Errorf("start=%v end=%v", tr.Start, tr.End)
	}
	if caps.Level != adapter.Full || !caps.Prompts || !caps.Edits || !caps.Statements || caps.Degraded != "" {
		t.Errorf("caps = %+v", caps)
	}
}

func TestSkipsMetaCompactAndCommandLines(t *testing.T) {
	cwd := t.TempDir()
	in := strings.Join([]string{
		line("user", cwd, `"isMeta":true,"message":{"content":"meta text"}`),
		line("user", cwd, `"isCompactSummary":true,"message":{"content":"summary text"}`),
		userLine(cwd, `"<command-name>/clear</command-name>"`),
		userLine(cwd, `"<local-command-stdout>x</local-command-stdout>"`),
		userLine(cwd, `"<system-reminder>x</system-reminder>"`),
		userLine(cwd, `[{"type":"tool_result","tool_use_id":"t","content":"out"}]`),
		userLine(cwd, `"real prompt"`),
	}, "\n") + "\n"
	tr, _ := capture(t, in)
	if len(tr.Prompts) != 1 || tr.Prompts[0].Text != "real prompt" || tr.Prompts[0].Turn != 1 {
		t.Fatalf("prompts = %+v", tr.Prompts)
	}
}

func TestExcludesUnaskedFeedback(t *testing.T) {
	cwd := t.TempDir()
	in := userLine(cwd, `"[unasked report]\n2 hunks unasked"`) + "\n" + userLine(cwd, `"do the thing"`) + "\n"
	tr, _ := capture(t, in)
	if len(tr.Prompts) != 1 || tr.Prompts[0].Text != "do the thing" {
		t.Fatalf("prompts = %+v", tr.Prompts)
	}
}

func TestStatementLastThreeSentences(t *testing.T) {
	cwd := t.TempDir()
	in := strings.Join([]string{
		userLine(cwd, `"go"`),
		line("assistant", cwd, `"message":{"content":[{"type":"text","text":"First one. Second one!"}]}`),
		line("assistant", cwd, `"message":{"content":[{"type":"text","text":"Third? Fourth.\nFifth"},{"type":"tool_use","name":"Write","input":{"file_path":"a.txt","content":"x"}},{"type":"tool_use","name":"Write","input":{"file_path":"b.txt","content":"y"}}]}`),
	}, "\n") + "\n"
	tr, _ := capture(t, in)
	if len(tr.Edits) != 2 {
		t.Fatalf("edits = %+v", tr.Edits)
	}
	if tr.Edits[0].Statement != "Third? Fourth. Fifth" {
		t.Errorf("statement = %q", tr.Edits[0].Statement)
	}
	if tr.Edits[1].Statement != "" {
		t.Errorf("second tool_use statement = %q, want empty", tr.Edits[1].Statement)
	}
}

func TestStatementSurvivesReadOnlyTools(t *testing.T) {
	cwd := t.TempDir()
	w := func(text string) string {
		return line("assistant", cwd, `"message":{"content":[{"type":"text","text":"`+text+`"}]}`)
	}
	tool := func(name, input string) string {
		return line("assistant", cwd, `"message":{"content":[{"type":"tool_use","name":"`+name+`","input":`+input+`}]}`)
	}
	in := strings.Join([]string{
		userLine(cwd, `"go"`),
		w("I will remove the header."),
		tool("Read", `{"file_path":"a.go"}`),
		tool("Write", `{"file_path":"a.go","content":"1"}`),
		w("Now text two."),
		tool("Grep", `{"pattern":"x"}`),
		tool("Write", `{"file_path":"b.go","content":"2"}`),
	}, "\n") + "\n"
	tr, _ := capture(t, in)
	if len(tr.Edits) != 2 || tr.Edits[0].Statement != "I will remove the header." || tr.Edits[1].Statement != "Now text two." {
		t.Fatalf("edits = %+v", tr.Edits)
	}
}

func TestBashMechanicalRegardlessOfStatement(t *testing.T) {
	cwd := t.TempDir()
	in := userLine(cwd, `"go"`) + "\n" +
		line("assistant", cwd, `"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gofmt -w ."}}]}`) + "\n"
	tr, _ := capture(t, in)
	if len(tr.Edits) != 1 || !tr.Edits[0].Mechanical || tr.Edits[0].Statement != "" || tr.Edits[0].Command != "gofmt -w ." {
		t.Fatalf("edits = %+v", tr.Edits)
	}
}

func TestSubagentEditsMergedNotPrompts(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "transcript.jsonl"), fixture(t, "main.jsonl", cwd))
	write(t, filepath.Join(dir, "subagents", "agent-1.jsonl"), fixture(t, "sub.jsonl", cwd))
	tr, _, err := New(t.TempDir()).ParseCaptured(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Prompts) != 2 || tr.Turns != 2 {
		t.Fatalf("prompts = %+v", tr.Prompts)
	}
	var got *model.EditEvent
	for i := range tr.Edits {
		if tr.Edits[i].File == "helper.go" {
			got = &tr.Edits[i]
		}
	}
	if got == nil || got.After != "b" || got.Statement != "Fixing the helper." || got.Turn != 1 {
		t.Fatalf("subagent edit = %+v (all %+v)", got, tr.Edits)
	}
}

func TestReadTruncatedLastLine(t *testing.T) {
	cwd := t.TempDir()
	full := fixture(t, "main.jsonl", cwd)
	cut := full[:len(full)-40] // chop the final record mid-JSON
	tr, caps := capture(t, cut)
	if len(tr.Prompts) != 2 || len(tr.Edits) != 4 {
		t.Fatalf("prompts=%d edits=%d", len(tr.Prompts), len(tr.Edits))
	}
	if caps.Degraded != "" {
		t.Errorf("degraded = %q", caps.Degraded)
	}
	// Garbage in the middle is skipped too.
	tr, _ = capture(t, "not json\n"+full)
	if len(tr.Prompts) != 2 || len(tr.Edits) != 5 {
		t.Fatalf("prompts=%d edits=%d", len(tr.Prompts), len(tr.Edits))
	}
}

func TestHugeLine(t *testing.T) {
	cwd := t.TempDir()
	big := strings.Repeat("x", 3<<20)
	in := userLine(cwd, `"`+big+`"`) + "\n" + userLine(cwd, `"after"`) + "\n"
	tr, _ := capture(t, in)
	if len(tr.Prompts) != 2 || tr.Prompts[1].Text != "after" {
		t.Fatalf("prompts = %d", len(tr.Prompts))
	}
}

func TestUnknownMajorDegrades(t *testing.T) {
	cwd := t.TempDir()
	in := strings.ReplaceAll(fixture(t, "main.jsonl", cwd), "2.1.289", "3.0.1")
	tr, caps := capture(t, in)
	if caps.Level != adapter.PromptsOnly || caps.Degraded != "claude-code 3.0.1: unknown format version" {
		t.Errorf("caps = %+v", caps)
	}
	if len(tr.Prompts) != 2 || len(tr.Edits) != 0 || caps.Edits || caps.Statements {
		t.Errorf("prompts=%d edits=%d caps=%+v", len(tr.Prompts), len(tr.Edits), caps)
	}
	_, caps = capture(t, line("assistant", cwd, `"message":{"content":[]}`)+"\n")
	if caps.Level != adapter.Full {
		t.Errorf("known version, no prompts: %+v", caps)
	}
	_, caps = capture(t, strings.ReplaceAll(line("assistant", cwd, `"message":{"content":[]}`), "2.1.289", "0.9")+"\n")
	if caps.Level != adapter.DiffOnly || caps.Degraded == "" {
		t.Errorf("unknown major, no prompts: %+v", caps)
	}
}

func TestFindMatchesSubdirCwd(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg", "deep")
	other := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects")
	write(t, filepath.Join(proj, "enc-repo", "a.jsonl"), userLine(repo, `"x"`)+"\n")
	write(t, filepath.Join(proj, "enc-sub", "b.jsonl"), userLine(sub, `"x"`)+"\n")
	write(t, filepath.Join(proj, "enc-other", "c.jsonl"), userLine(other, `"x"`)+"\n")
	write(t, filepath.Join(proj, "enc-old", "d.jsonl"), userLine(repo, `"x"`)+"\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(proj, "enc-old", "d.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}

	a := New(home)
	if !a.Detect() {
		t.Error("Detect = false")
	}
	got, err := a.Find(repo, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]adapter.Session{}
	for _, s := range got {
		ids[s.ID] = s
	}
	if len(got) != 2 || ids["a"].Agent != "claude-code" || ids["b"].Cwd != sub || ids["a"].Path == "" {
		t.Fatalf("sessions = %+v", got)
	}
	if !ids["a"].Start.Equal(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", ids["a"].Start)
	}

	tr, caps, err := a.Read(ids["a"])
	if err != nil || len(tr.Prompts) != 1 || caps.Level != adapter.Full {
		t.Fatalf("Read: %+v %+v %v", tr, caps, err)
	}
}

func TestReadMergesSubagentsOfSession(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "enc")
	write(t, filepath.Join(proj, "sess.jsonl"), fixture(t, "main.jsonl", repo))
	write(t, filepath.Join(proj, "sess", "subagents", "agent-1.jsonl"), fixture(t, "sub.jsonl", repo))
	a := New(home)
	ss, err := a.Find(repo, time.Time{})
	if err != nil || len(ss) != 1 {
		t.Fatalf("find: %v %v", ss, err)
	}
	tr, _, err := a.Read(ss[0])
	if err != nil || len(tr.Edits) != 6 {
		t.Fatalf("edits = %d err=%v", len(tr.Edits), err)
	}
}

func TestIdentity(t *testing.T) {
	a := New(t.TempDir())
	if a.Name() != "claude-code" || a.MaxLevel() != adapter.Full {
		t.Error("identity")
	}
	if a.Detect() {
		t.Error("Detect true with no projects dir")
	}
	if _, ok := adapter.Get("claude-code"); !ok {
		t.Error("not registered")
	}
}

func TestRepoRelativeUsesGitRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg")
	in := userLine(sub, `"go"`) + "\n" +
		line("assistant", sub, `"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":`+jsonStr(filepath.Join(sub, "x.go"))+`,"content":"x"}}]}`) + "\n"
	tr, _ := capture(t, in)
	if len(tr.Edits) != 1 || tr.Edits[0].File != "pkg/x.go" {
		t.Fatalf("edits = %+v", tr.Edits)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }
