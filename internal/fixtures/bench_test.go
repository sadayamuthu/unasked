package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/pipeline"
)

// The performance target (global constraints): a report for 40 hunks, a
// 300-turn transcript and a 5,000-file repo in under a second.
const (
	benchFiles  = 5000
	benchHunks  = 40
	benchTurns  = 300
	benchBudget = time.Second
	benchAgent  = "claude-code"
)

var (
	benchOnce sync.Once
	benchRoot string
	benchRepo *gitx.Repo
	benchErr  error
)

// cleanupBench removes the generated repository; TestMain calls it.
func cleanupBench() {
	if benchRoot != "" {
		os.RemoveAll(benchRoot)
	}
}

// generated returns the shared benchmark repository, building it on first
// use: 5,000 committed Go files, a captured session start, 40 one-line agent
// edits (one deleting a function) and a 300-turn Claude Code transcript.
func generated(tb testing.TB) *gitx.Repo {
	tb.Helper()
	benchOnce.Do(func() { benchRepo, benchErr = buildBench() })
	if benchErr != nil {
		tb.Fatal(benchErr)
	}
	return benchRepo
}

func benchFile(i int) string { return fmt.Sprintf("pkg%02d/file%04d.go", i/100, i) }

func benchSource(i int, body string) string {
	return fmt.Sprintf("package pkg%02d\n\n// Value%d returns a constant.\nfunc Value%d() int {\n\t%s\n}\n\n// Name%d names the file.\nfunc Name%d() string {\n\treturn %q\n}\n",
		i/100, i, i, body, i, i, benchFile(i))
}

func buildBench() (*gitx.Repo, error) {
	root, err := os.MkdirTemp("", "unasked-bench")
	if err != nil {
		return nil, err
	}
	benchRoot = root
	dir, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "repo") // the transcript stays outside the work tree
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@example.com",
			"GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@example.com", "LC_ALL=C")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	write := func(rel, content string) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o644)
	}
	if err := git("init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	for i := 0; i < benchFiles; i++ {
		if err := write(benchFile(i), benchSource(i, fmt.Sprintf("return %d", i))); err != nil {
			return nil, err
		}
	}
	if err := git("add", "-A"); err != nil {
		return nil, err
	}
	if err := git("commit", "-q", "-m", "generated"); err != nil {
		return nil, err
	}
	r, err := gitx.Open(dir)
	if err != nil {
		return nil, err
	}
	if _, err := adapter.RecordStart(r, benchAgent, "bench", dir); err != nil {
		return nil, err
	}

	// The agent edits every 125th file; turn k of 40 edits one of them, the
	// other turns edit files outside the diff's hunks.
	cwd, _ := json.Marshal(dir)
	start := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	var lines []string
	rec := func(typ string, at time.Time, content any) {
		c, _ := json.Marshal(content)
		lines = append(lines, fmt.Sprintf(`{"type":%q,"cwd":%s,"version":"2.1.289","timestamp":%q,"sessionId":"bench","message":{"role":%q,"content":%s}}`,
			typ, cwd, at.Format(time.RFC3339Nano), typ, c))
	}
	for turn := 0; turn < benchTurns; turn++ {
		at := start.Add(time.Duration(turn) * time.Minute)
		rec("user", at, fmt.Sprintf("turn %d: make Value%d return its index times two for the pricing table", turn, turn))
		i := turn*17%benchFiles + 1 // an untouched file, by default
		newBody := fmt.Sprintf("return %d * 2", i)
		after := ""
		if turn < benchHunks {
			i = turn * (benchFiles / benchHunks)
			after = benchSource(i, newBody)
			if turn == benchHunks-1 { // one deletion: drop NameN, which is referenced nowhere
				newBody = fmt.Sprintf("return %d", i)
				after = strings.SplitAfter(benchSource(i, newBody), "}\n")[0]
			}
			if err := write(benchFile(i), after); err != nil {
				return nil, err
			}
		}
		f, _ := json.Marshal(filepath.Join(dir, filepath.FromSlash(benchFile(i))))
		rec("assistant", at.Add(10*time.Second), []map[string]any{
			{"type": "text", "text": fmt.Sprintf("I'll make Value%d return its index times two.", i)},
			{"type": "tool_use", "id": fmt.Sprintf("toolu_%03d", turn), "name": "Edit",
				"input": map[string]any{"file_path": json.RawMessage(f), "old_string": fmt.Sprintf("return %d", i), "new_string": "\t" + newBody}},
		})
		rec("user", at.Add(11*time.Second), []map[string]any{{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%03d", turn), "content": "ok"}})
	}
	tp := filepath.Join(root, "transcript.jsonl")
	if err := os.WriteFile(tp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	if _, err := adapter.RecordStop(r, benchAgent, "bench", tp, nil); err != nil {
		return nil, err
	}
	return r, nil
}

// TestPerformanceBudget fails when pipeline.Run on the generated repository
// takes a second or more. Generation is excluded; the fastest of three runs
// is compared so one scheduler hiccup on a shared CI runner does not fail it.
func TestPerformanceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a 5,000-file repository")
	}
	r := generated(t)
	best := time.Duration(1<<63 - 1)
	for range 3 {
		began := time.Now()
		rep, _, _, err := pipeline.Run(r, adapter.Request{}, invite.Default)
		took := time.Since(began)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Hunks) != benchHunks || rep.Turns != benchTurns || rep.Transcript != "present" {
			t.Fatalf("generated session: %d hunks, %d turns, transcript %q; want %d, %d, present",
				len(rep.Hunks), rep.Turns, rep.Transcript, benchHunks, benchTurns)
		}
		best = min(best, took)
	}
	t.Logf("pipeline.Run: %d files, %d hunks, %d turns: %v (budget %v)", benchFiles, benchHunks, benchTurns, best, benchBudget)
	if best >= benchBudget {
		t.Errorf("pipeline.Run took %v, budget %v", best, benchBudget)
	}
}

func BenchmarkPipeline(b *testing.B) {
	if testing.Short() {
		b.Skip("generates a 5,000-file repository")
	}
	r := generated(b)
	for b.Loop() {
		if _, _, _, err := pipeline.Run(r, adapter.Request{}, invite.Default); err != nil {
			b.Fatal(err)
		}
	}
}
