package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/model"
)

type record struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Cwd              string `json:"cwd"`
	Version          string `json:"version"`
	Timestamp        string `json:"timestamp"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

var skipPrefixes = []string{"<command-", "<local-command", "<system-reminder>", "[unasked report]"}

// state accumulates one session: the main file first, then its subagents.
type state struct {
	tr          model.Transcript
	promptTimes []time.Time
	roots       map[string]string
	spans       bool // Start/End already set
}

func newState() *state { return &state{roots: map[string]string{}} }

// readFile streams one JSONL file into st. sub marks a subagent file: its
// edits are kept, its prompts are not.
func (st *state) readFile(path string, sub bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return st.read(f, sub)
}

func (st *state) read(r io.Reader, sub bool) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var pending strings.Builder // assistant text since the previous tool_use
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			st.line(line, sub, &pending)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (st *state) line(line []byte, sub bool, pending *strings.Builder) {
	if !bytes.Contains(line, []byte(`"type":"user"`)) && !bytes.Contains(line, []byte(`"type":"assistant"`)) {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil || (rec.Type != "user" && rec.Type != "assistant") {
		return
	}
	ts, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if !sub {
		if st.tr.Version == "" {
			st.tr.Version = rec.Version
		}
		if !ts.IsZero() {
			if !st.spans {
				st.tr.Start, st.spans = ts, true
			}
			st.tr.End = ts
		}
	}
	if rec.Type == "user" {
		if sub || rec.IsMeta || rec.IsCompactSummary {
			return
		}
		if text, ok := promptText(rec.Message.Content); ok {
			st.tr.Prompts = append(st.tr.Prompts, model.Prompt{Turn: len(st.tr.Prompts) + 1, Text: text})
			st.promptTimes = append(st.promptTimes, ts)
			pending.Reset()
		}
		return
	}
	var blocks []block
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if pending.Len() > 0 {
				pending.WriteByte('\n')
			}
			pending.WriteString(b.Text)
		case "tool_use":
			statement := adapter.LastSentences(pending.String(), 3)
			pending.Reset()
			if ev, ok := st.edit(b, rec.Cwd, statement); ok {
				ev.Turn = st.turnAt(ts, sub)
				st.tr.Edits = append(st.tr.Edits, ev)
			}
		}
	}
}

// turnAt is the number of prompts so far for the main file; for a subagent it
// is the number of main prompts at or before the edit's timestamp.
func (st *state) turnAt(ts time.Time, sub bool) int {
	if !sub || ts.IsZero() {
		return len(st.tr.Prompts)
	}
	n := 0
	for _, pt := range st.promptTimes {
		if !pt.After(ts) {
			n++
		}
	}
	return n
}

func promptText(raw json.RawMessage) (string, bool) {
	var text string
	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = s
	} else {
		var blocks []block
		if json.Unmarshal(raw, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return "", false
			}
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	trimmed := strings.TrimLeft(text, " \t\r\n")
	for _, p := range skipPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return "", false
		}
	}
	return text, true
}

func (st *state) edit(b block, cwd, statement string) (model.EditEvent, bool) {
	ev := model.EditEvent{Statement: statement}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		NewString    string `json:"new_string"`
		Content      string `json:"content"`
		NewSource    string `json:"new_source"`
		Command      string `json:"command"`
		Edits        []struct {
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	switch b.Name {
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "Bash":
	default:
		return ev, false
	}
	if json.Unmarshal(b.Input, &in) != nil {
		return ev, false
	}
	switch b.Name {
	case "Edit":
		ev.File, ev.After = in.FilePath, in.NewString
	case "MultiEdit":
		ev.File = in.FilePath
		parts := make([]string, len(in.Edits))
		for i, e := range in.Edits {
			parts[i] = e.NewString
		}
		ev.After = strings.Join(parts, "\n")
	case "Write":
		ev.File, ev.After = in.FilePath, in.Content
	case "NotebookEdit":
		ev.File, ev.After = in.NotebookPath, in.NewSource
	case "Bash":
		if in.Command == "" {
			return ev, false
		}
		ev.Command = in.Command
		ev.Mechanical = statement != "" && adapter.IsMechanical(in.Command)
		return ev, true
	}
	if ev.File == "" {
		return ev, false
	}
	ev.File = st.relative(cwd, ev.File)
	return ev, true
}

// relative makes p repo-relative with forward slashes when it lies inside the
// repo containing cwd; other paths are returned unchanged.
func (st *state) relative(cwd, p string) string {
	if cwd == "" || !filepath.IsAbs(p) {
		return p
	}
	root, ok := st.roots[cwd]
	if !ok {
		root = repoRoot(cwd)
		st.roots[cwd] = root
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.ToSlash(rel)
}

// repoRoot walks up from dir to the nearest directory holding a .git entry,
// falling back to dir itself. It never runs git.
func repoRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// finish turns the accumulated state into a transcript and its capabilities,
// applying the known-major check.
func (st *state) finish() (*model.Transcript, adapter.Capabilities) {
	tr := st.tr
	tr.Turns = len(tr.Prompts)
	caps := adapter.Capabilities{Level: adapter.Full, Hook: adapter.HookBlock}
	if v := tr.Version; v != "" && !knownMajor(v) {
		caps.Degraded = "claude-code " + v + ": unknown format version"
		caps.Level = adapter.DiffOnly
		tr.Edits = nil
		if len(tr.Prompts) > 0 {
			caps.Level = adapter.PromptsOnly
			caps.Prompts = true
		}
		return &tr, caps
	}
	caps.Prompts = len(tr.Prompts) > 0
	caps.Edits = len(tr.Edits) > 0
	for _, e := range tr.Edits {
		if e.Statement != "" {
			caps.Statements = true
			break
		}
	}
	return &tr, caps
}

func knownMajor(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	return major == "1" || major == "2"
}

// parseSession reads main, then every subagent file, into one transcript.
func parseSession(main string, subagents []string) (*model.Transcript, adapter.Capabilities, error) {
	st := newState()
	if err := st.readFile(main, false); err != nil {
		return nil, adapter.Capabilities{}, err
	}
	for _, p := range subagents {
		_ = st.readFile(p, true) // a vanished or unreadable subagent file is not fatal
	}
	tr, caps := st.finish()
	return tr, caps, nil
}
