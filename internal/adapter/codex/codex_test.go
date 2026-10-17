package codex

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

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(cwd)
	return strings.ReplaceAll(string(b), "@CWD@", string(q[1:len(q)-1]))
}

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

const metaLine = `{"timestamp":"2026-10-14T10:00:00.000Z","type":"session_meta","payload":{"id":"s","cwd":"/r","cli_version":"0.99.0"}}` + "\n"

func fcall(name, args string) string {
	q, _ := json.Marshal(args)
	return `{"timestamp":"2026-10-14T10:00:01.000Z","type":"response_item","payload":{"type":"function_call","name":"` + name + `","arguments":` + string(q) + `}}` + "\n"
}

func prompt(msg string) string {
	q, _ := json.Marshal(msg)
	return `{"timestamp":"2026-10-14T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":` + string(q) + `}}` + "\n"
}

func TestParseApplyPatchUpdateAddDeleteMove(t *testing.T) {
	body := "ignored\n*** Begin Patch\n*** Update File: a.go\n@@ func f\n ctx\n-gone\n+one\n+two\n*** Add File: b.txt\n+hello\n+world\n*** Delete File: c.txt\n*** Update File: d.go\n*** Move to: e.go\n@@\n-x\n+y\n*** Frobnicate: z\n+stray\n*** Update File: \n+bad\n*** End Patch\n"
	got := parsePatch(body)
	want := []model.EditEvent{
		{File: "a.go", After: "one\ntwo"},
		{File: "b.txt", After: "hello\nworld"},
		{File: "c.txt"},
		{File: "e.go", After: "y"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v want %+v", i, got[i], want[i])
		}
	}
	if parsePatch("no markers\n+x") != nil {
		t.Error("patch without Begin marker should yield nothing")
	}
}

func TestParseSessionPromptsEditsShell(t *testing.T) {
	cwd := t.TempDir()
	tr, caps := capture(t, fixture(t, "session.jsonl", cwd))
	wantP := []model.Prompt{{Turn: 1, Text: "add a retry to the fetcher"}, {Turn: 2, Text: "thanks, now run the tests"}}
	if len(tr.Prompts) != 2 || tr.Prompts[0] != wantP[0] || tr.Prompts[1] != wantP[1] {
		t.Fatalf("prompts = %+v", tr.Prompts)
	}
	want := []model.EditEvent{
		{Turn: 1, File: "pkg/fetch.go", After: "retry()", Statement: "Let me look first. I will add the retry now."},
		{Turn: 1, Command: "bash -lc gofmt -w .", Mechanical: true, Statement: "Formatting."},
		{Turn: 2, Command: "go test ./..."},
		{Turn: 2, Command: "npm install", Mechanical: true},
	}
	if len(tr.Edits) != len(want) {
		t.Fatalf("edits = %+v", tr.Edits)
	}
	for i, w := range want {
		if tr.Edits[i] != w {
			t.Errorf("edit %d = %+v, want %+v", i, tr.Edits[i], w)
		}
	}
	if tr.Version != "0.99.0" || tr.Turns != 2 || len(tr.Commits) != 1 || tr.Commits[0] != "abc123" {
		t.Errorf("tr = %+v", tr)
	}
	if caps.Level != adapter.Full || caps.Degraded != "" || !caps.Statements || caps.Hook != adapter.HookBlock {
		t.Errorf("caps = %+v", caps)
	}
}

func TestEncryptedArgsDegrade(t *testing.T) {
	enc := `{"timestamp":"2026-10-14T10:00:01.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"","encrypted_function_args":["xx"]}}` + "\n"
	// edits unreadable, no statements remain -> PromptsOnly
	tr, caps := capture(t, metaLine+prompt("do it")+enc)
	if caps.Level != adapter.PromptsOnly || caps.Degraded != "codex: encrypted tool arguments" || len(tr.Edits) != 0 {
		t.Errorf("caps = %+v edits=%+v", caps, tr.Edits)
	}
	// a readable statement-bearing edit remains -> Partial
	asst := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Adding it."}]}}` + "\n"
	patch := `{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** Add File: n.txt\n+x\n*** End Patch"}}` + "\n"
	tr, caps = capture(t, metaLine+prompt("do it")+enc+asst+patch)
	if caps.Level != adapter.Partial || caps.Degraded == "" || len(tr.Edits) != 1 {
		t.Errorf("caps = %+v edits=%+v", caps, tr.Edits)
	}
}

func TestDegradeUnrecognisedAndVersionUnknown(t *testing.T) {
	_, caps := capture(t, "garbage\n{\"foo\":1}\n")
	if caps.Level != adapter.DiffOnly || caps.Degraded != "codex: no recognised records" {
		t.Errorf("caps = %+v", caps)
	}
	_, caps = capture(t, "")
	if caps.Degraded != "" {
		t.Errorf("empty file degraded: %+v", caps)
	}
	_, caps = capture(t, prompt("hi"))
	if caps.Level != adapter.Partial || caps.Degraded != "codex: version unknown" {
		t.Errorf("caps = %+v", caps)
	}
}

func TestStatementResetAndHeredocPatch(t *testing.T) {
	asst := func(s string) string {
		return `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + s + `"}]}}` + "\n"
	}
	in := metaLine + prompt("go") + asst("Stale.") + prompt("next") + asst("Fresh one.") +
		fcall("shell", `{"command":"apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: h.txt\n+hi\n*** End Patch\nEOF"}`)
	tr, _ := capture(t, in)
	if len(tr.Edits) != 1 || tr.Edits[0].File != "h.txt" || tr.Edits[0].Statement != "Fresh one." || tr.Edits[0].Command != "" {
		t.Errorf("edits = %+v", tr.Edits)
	}
}

func TestReadTruncatedLastLine(t *testing.T) {
	full := fixture(t, "session.jsonl", "/r")
	tr, caps := capture(t, full+`{"timestamp":"2026-10-14T10:00:14.000Z","type":"event_msg","payload":{"type":"user_mess`)
	if len(tr.Prompts) != 2 || len(tr.Edits) != 4 || caps.Level != adapter.Full {
		t.Errorf("prompts=%d edits=%d caps=%+v", len(tr.Prompts), len(tr.Edits), caps)
	}
	// a huge line must not break the reader
	huge := `{"type":"world_state","payload":{"x":"` + strings.Repeat("a", 3<<20) + `"}}` + "\n"
	tr, _ = capture(t, huge+full)
	if len(tr.Prompts) != 2 {
		t.Errorf("prompts after huge line = %d", len(tr.Prompts))
	}
}

func TestFindByCwdAcrossDateDirs(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	root := filepath.Join(home, ".codex", "sessions")
	a := filepath.Join(root, "2026", "10", "13", "rollout-2026-10-13T09-00-00-aaa.jsonl")
	b := filepath.Join(root, "2026", "10", "14", "rollout-2026-10-14T09-00-00-bbb.jsonl")
	c := filepath.Join(root, "2026", "10", "14", "rollout-2026-10-14T10-00-00-ccc.jsonl")
	d := filepath.Join(root, "2026", "10", "12", "rollout-2026-10-12T10-00-00-ddd.jsonl")
	write(t, a, fixture(t, "session.jsonl", repo))
	write(t, b, fixture(t, "session.jsonl", sub))
	write(t, c, fixture(t, "session.jsonl", other))
	write(t, d, fixture(t, "session.jsonl", repo))
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(d, old, old); err != nil {
		t.Fatal(err)
	}
	ad := New(home)
	if !ad.Detect() {
		t.Fatal("Detect false")
	}
	got, err := ad.Find(repo, time.Now().Add(-24*time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v err %v", got, err)
	}
	for _, s := range got {
		if s.Agent != "codex" || s.ID != "sess-1" || s.Start.IsZero() {
			t.Errorf("session = %+v", s)
		}
	}
	tr, _, err := ad.Read(got[0])
	if err != nil || len(tr.Prompts) != 2 {
		t.Errorf("Read: %v %+v", err, tr)
	}
	if New("").Detect() {
		t.Error("empty home must not detect")
	}
	if s, err := New("").Find(repo, time.Time{}); err != nil || s != nil {
		t.Error("empty home Find")
	}
	if ad.Name() != "codex" || ad.MaxLevel() != adapter.Full {
		t.Error("name/level")
	}
}

func TestPatchPathsRelativeToSessionCwd(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(sub)
	m := `{"type":"session_meta","payload":{"cwd":` + string(q) + `,"cli_version":"0.99.0"}}` + "\n"
	abs := filepath.Join(repo, "top.go")
	patch := "*** Begin Patch\n*** Update File: fetch.go\n+a\n*** Update File: ./x.go\n+b\n*** Add File: " + abs + "\n+c\n*** Update File: ../up.go\n+d\n*** End Patch"
	pq, _ := json.Marshal(patch)
	custom := `{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":` + string(pq) + `}}` + "\n"
	tr, _ := capture(t, m+prompt("go")+custom)
	var files []string
	for _, e := range tr.Edits {
		files = append(files, e.File)
	}
	if strings.Join(files, ",") != "pkg/fetch.go,pkg/x.go,top.go,up.go" {
		t.Errorf("files = %v", files)
	}
	// function-form apply_patch: arguments JSON with "input"
	args, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Update File: fetch.go\n+z\n*** End Patch"})
	tr, _ = capture(t, m+prompt("go")+fcall("apply_patch", string(args)))
	if len(tr.Edits) != 1 || tr.Edits[0].File != "pkg/fetch.go" || tr.Edits[0].After != "z" {
		t.Errorf("edits = %+v", tr.Edits)
	}
}
