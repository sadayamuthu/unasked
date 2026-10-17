package codex

// Record shapes (confirmed against openai/codex codex-rs/protocol/src/models.rs
// and a real rollout's key names; no tool calls existed locally, so tool-call
// shapes come from models.rs):
//
//   every line: {"timestamp":str,"type":str,"payload":obj}
//   session_meta payload: id, cwd, cli_version, timestamp, git.commit_hash
//   event_msg payload.type "user_message": message (string)  <- the prompt
//   response_item payload.type:
//     "message": role, content[] of {type:"output_text"|"input_text", text}
//     "function_call": name, arguments (STRING holding JSON), call_id,
//         optional encrypted_content (string), encrypted_function_args ([]string)
//         shell / shell_command: arguments {"command": string | [argv...]}
//         exec_command:          arguments {"cmd": string}
//         apply_patch (function form): arguments {"input": "<patch>"}
//     "custom_tool_call": name, input (STRING, raw patch for apply_patch),
//         optional encrypted_content
//     "local_shell_call": action {type:"exec", command:[argv...]},
//         optional encrypted_content
//   apply_patch body: "*** Begin Patch" / "*** Add|Update|Delete File: p" /
//     "*** Move to: q" / "@@" hunks with +/-/space lines / "*** End Patch".

import (
	"bufio"
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
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Message   string `json:"message"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
	Action struct {
		Command json.RawMessage `json:"command"`
	} `json:"action"`
	Cwd                   string          `json:"cwd"`
	EncryptedContent      string          `json:"encrypted_content"`
	EncryptedFunctionArgs json.RawMessage `json:"encrypted_function_args"`
}

func (p payload) encrypted() bool {
	if p.EncryptedContent != "" {
		return true
	}
	a := strings.TrimSpace(string(p.EncryptedFunctionArgs))
	return a != "" && a != "null" && a != "[]"
}

var shellTools = map[string]bool{"shell": true, "exec_command": true, "local_shell": true, "shell_command": true, "container.exec": true}

var skipPrefixes = []string{"[unasked report]", "<environment_context>", "<user_instructions>"}

type state struct {
	tr        model.Transcript
	cwd       string
	roots     map[string]string
	sawMeta   bool
	encrypted bool
	nonEmpty  bool
	records   int
	pending   strings.Builder
}

func parseFile(path string) (*model.Transcript, adapter.Capabilities, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, adapter.Capabilities{}, err
	}
	defer f.Close()
	st := &state{roots: map[string]string{}}
	if err := st.read(f); err != nil {
		return nil, adapter.Capabilities{}, err
	}
	tr, caps := st.finish()
	return tr, caps, nil
}

func (st *state) read(r io.Reader) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			st.line(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (st *state) line(line []byte) {
	if len(strings.TrimSpace(string(line))) > 0 {
		st.nonEmpty = true
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return // truncated or undecodable
	}
	switch rec.Type {
	case "session_meta", "event_msg", "response_item", "turn_context":
	default:
		return
	}
	st.records++
	ts, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if !ts.IsZero() {
		if st.tr.Start.IsZero() {
			st.tr.Start = ts
		}
		st.tr.End = ts
	}
	var p payload
	if json.Unmarshal(rec.Payload, &p) != nil {
		return
	}
	switch rec.Type {
	case "session_meta":
		m := parseMeta(rec.Payload)
		if m.Version != "" {
			st.sawMeta = true
			if st.tr.Version == "" {
				st.tr.Version = m.Version
			}
		}
		if st.cwd == "" {
			st.cwd = m.Cwd
		}
		if m.Commit != "" && len(st.tr.Commits) == 0 {
			st.tr.Commits = []string{m.Commit}
		}
	case "turn_context":
		if st.cwd == "" {
			st.cwd = p.Cwd
		}
	case "event_msg":
		if p.Type == "user_message" && !skipBlock(p.Message) {
			st.tr.Prompts = append(st.tr.Prompts, model.Prompt{Turn: len(st.tr.Prompts) + 1, Text: p.Message})
			st.pending.Reset()
		}
	case "response_item":
		st.item(p)
	}
}

func (st *state) item(p payload) {
	switch p.Type {
	case "message":
		if p.Role != "assistant" || p.encrypted() {
			return
		}
		for _, c := range p.Content {
			if c.Text == "" {
				continue
			}
			if st.pending.Len() > 0 {
				st.pending.WriteByte('\n')
			}
			st.pending.WriteString(c.Text)
		}
	case "function_call", "custom_tool_call", "local_shell_call":
		isPatch := p.Name == "apply_patch"
		if p.Type != "local_shell_call" && !isPatch && !shellTools[p.Name] {
			return // other tools keep the pending statement
		}
		statement := adapter.LastSentences(st.pending.String(), 3)
		st.pending.Reset()
		for _, ev := range st.events(p, isPatch) {
			ev.Statement = statement
			ev.Turn = len(st.tr.Prompts)
			st.tr.Edits = append(st.tr.Edits, ev)
		}
	}
}

// events extracts the edit events of one edit-producing call.
func (st *state) events(p payload, isPatch bool) []model.EditEvent {
	body, command := p.Input, ""
	if p.Type == "local_shell_call" {
		command = joinCommand(p.Action.Command, "")
	} else if p.Type == "function_call" {
		var a struct {
			Input   string          `json:"input"`
			Patch   string          `json:"patch"`
			Command json.RawMessage `json:"command"`
			Cmd     string          `json:"cmd"`
		}
		if json.Unmarshal([]byte(p.Arguments), &a) == nil {
			body = a.Input
			if body == "" {
				body = a.Patch
			}
			command = joinCommand(a.Command, a.Cmd)
		} else if isPatch {
			body = p.Arguments
		}
	}
	if isPatch {
		return st.patchEvents(body, p)
	}
	if command == "" {
		if p.encrypted() {
			st.encrypted = true
		}
		return nil
	}
	if strings.Contains(command, beginPatch) { // apply_patch via shell heredoc
		if evs := st.patchEvents(command, p); len(evs) > 0 {
			return evs
		}
	}
	return []model.EditEvent{{Command: command, Mechanical: adapter.IsMechanical(command)}}
}

func (st *state) patchEvents(body string, p payload) []model.EditEvent {
	if strings.TrimSpace(body) == "" && p.encrypted() {
		st.encrypted = true
		return nil
	}
	evs := parsePatch(body)
	for i := range evs {
		evs[i].File = st.relative(evs[i].File)
	}
	return evs
}

// joinCommand renders a command given as a JSON string or argv array.
func joinCommand(raw json.RawMessage, cmd string) string {
	if cmd != "" {
		return cmd
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) == nil {
		return strings.Join(argv, " ")
	}
	return ""
}

func skipBlock(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.TrimSpace(trimmed) == "" {
		return true
	}
	for _, p := range skipPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// relative makes p repo-relative with forward slashes. Codex patch paths are
// relative to the session cwd, so a relative p is joined with cwd first. Paths
// resolving outside the repo are returned cleaned (and absolute when cwd is
// known); they will not intersect the diff.
func (st *state) relative(p string) string {
	if st.cwd == "" {
		return filepath.ToSlash(filepath.Clean(p))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(st.cwd, p)
	}
	p = filepath.Clean(p)
	root, ok := st.roots[st.cwd]
	if !ok {
		root = repoRoot(st.cwd)
		st.roots[st.cwd] = root
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(p)
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

func (st *state) finish() (*model.Transcript, adapter.Capabilities) {
	tr := st.tr
	tr.Turns = len(tr.Prompts)
	caps := adapter.Capabilities{Level: adapter.Full, Hook: adapter.HookBlock}
	if st.records == 0 && st.nonEmpty {
		caps.Level = adapter.DiffOnly
		caps.Degraded = "codex: no recognised records"
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
	if !st.sawMeta && st.records > 0 {
		caps.Level = adapter.Partial
		caps.Degraded = "codex: version unknown"
	}
	if st.encrypted {
		caps.Degraded = "codex: encrypted tool arguments"
		switch {
		case caps.Statements:
			caps.Level = adapter.Partial
		case caps.Prompts:
			caps.Level = adapter.PromptsOnly
		default:
			caps.Level = adapter.DiffOnly
		}
	}
	return &tr, caps
}
