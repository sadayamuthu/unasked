// Package hook installs unasked's hooks into coding agents' config files and
// handles the hook calls those agents make: record the session baseline at
// start, and at stop capture the transcript, report, and feed unasked hunks
// back to the agent.
//
// Installing never damages the user's config: it merges into the existing
// JSON, keeps unknown keys and their order, keeps the file mode, writes
// atomically, backs the original up once, and uninstall removes only entries
// whose command contains " hook run --agent ".
package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/sadayamuthu/unasked/internal/report"
)

// Spec describes one agent's hook config and protocol.
type Spec struct {
	Agent string
	// ConfigPath is the hooks config file: user scope under home, or the
	// repo-local file when project is set.
	ConfigPath func(home, repo string, project bool) string
	// Install adds unasked's entries for bin, reporting whether cfg changed.
	Install func(cfg map[string]any, bin string) (map[string]any, bool)
	// Uninstall removes every unasked entry, reporting whether cfg changed.
	Uninstall func(cfg map[string]any) (map[string]any, bool)
	// ParsePayload reads the JSON the agent sends on the hook's stdin.
	ParsePayload func(stdin []byte) (Payload, error)
	// Respond writes the stop hook's answer; with feedback it asks the agent
	// to continue with the report. It returns the hook's exit code.
	Respond func(w io.Writer, rep *report.Report, feedback bool) (exit int)
	// Extra lists paths captured beside the transcript (subagent transcripts).
	Extra func(p Payload) []string
	// TrustNote is printed after an install that changed the config.
	TrustNote string
}

// Payload is the part of a hook call unasked uses.
type Payload struct {
	Event, Session, TranscriptPath, Cwd string
	StopHookActive                      bool
}

// Specs returns every supported agent, sorted by name.
func Specs() []Spec { return []Spec{ClaudeCode, Codex} }

// Get returns the spec for agent.
func Get(agent string) (Spec, bool) {
	for _, s := range Specs() {
		if s.Agent == agent {
			return s, true
		}
	}
	return Spec{}, false
}

// marker identifies unasked's hook entries regardless of the binary path, so
// reinstalling from a new location replaces rather than duplicates.
const marker = " hook run --agent "

const (
	backupSuffix = ".unasked-bak"
	hookTimeout  = 30 // seconds
)

// Bin returns the absolute path of the running unasked binary, symlinks resolved.
func Bin() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Abs(p)
}

// Command is the hook command line for agent, quoted for the platform's shell.
func Command(bin, agent string) string {
	return command(bin, agent, runtime.GOOS == "windows")
}

func command(bin, agent string, windows bool) string {
	return quote(bin, windows) + marker + agent
}

// quote makes bin one shell word. Windows paths are double-quoted (cmd,
// PowerShell and Git Bash all accept that); POSIX paths are single-quoted
// unless they need no quoting.
func quote(bin string, windows bool) string {
	if windows {
		return `"` + bin + `"`
	}
	safe := bin != ""
	for _, c := range bin {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-+:@%", c)) {
			safe = false
			break
		}
	}
	if safe {
		return bin
	}
	return "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
}

// Install merges s's hook entries for bin into its config file. The original
// file is backed up to <file>.unasked-bak unless a backup already exists; its
// mode is kept. A second install with the same bin changes nothing.
func Install(s Spec, home, repo string, project bool, bin string) (path string, changed bool, err error) {
	return edit(s.ConfigPath(home, repo, project), func(cfg map[string]any) (map[string]any, bool) {
		return s.Install(cfg, bin)
	})
}

// Uninstall removes unasked's entries from s's config file, leaving every
// other entry and key in place. A missing file is not an error.
func Uninstall(s Spec, home, repo string, project bool) (string, bool, error) {
	path := s.ConfigPath(home, repo, project)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return path, false, nil
	}
	return edit(path, s.Uninstall)
}

// Status reports whether s's hook is installed, checking the user config and
// then (when repo is set) the project config, and where it was found.
func Status(s Spec, home, repo string) (installed bool, path string) {
	paths := []string{s.ConfigPath(home, repo, false)}
	if repo != "" {
		paths = append(paths, s.ConfigPath(home, repo, true))
	}
	for _, p := range paths {
		doc, err := load(p)
		if err != nil || doc.cfg == nil {
			continue
		}
		if _, changed := s.Uninstall(clone(doc.cfg)); changed {
			return true, p
		}
	}
	return false, ""
}

// edit applies change to the JSON config at path and writes it back when it
// changed. A symlinked config is edited at its target.
func edit(path string, change func(map[string]any) (map[string]any, bool)) (string, bool, error) {
	target := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		target = r
	}
	doc, err := load(target)
	if err != nil {
		return path, false, err
	}
	cfg := doc.cfg
	if cfg == nil {
		cfg = map[string]any{}
	}
	if err := checkShape(cfg); err != nil {
		return path, false, fmt.Errorf("%s: %w", path, err)
	}
	cfg, changed := change(cfg)
	if !changed {
		return path, false, nil
	}
	var buf bytes.Buffer
	encode(&buf, cfg, "", doc.order, "")
	buf.WriteByte('\n')
	if err := write(target, doc, buf.Bytes()); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// document is a parsed config file.
type document struct {
	exists bool
	raw    []byte
	mode   fs.FileMode
	cfg    map[string]any      // nil for a missing or empty file
	order  map[string][]string // object key order by JSON path
}

func load(path string) (*document, error) {
	d := &document{mode: 0o600, order: map[string][]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	d.exists, d.raw, d.mode = true, b, fi.Mode().Perm()
	if len(bytes.TrimSpace(b)) == 0 {
		return d, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: not valid JSON: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s: unexpected data after the JSON object", path)
	}
	cfg, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: top level is not a JSON object", path)
	}
	d.cfg = cfg
	if err := keyOrder(json.NewDecoder(bytes.NewReader(b)), "", d.order); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// checkShape refuses configs whose hooks section unasked cannot merge into
// without guessing: "hooks" must be an object of arrays.
func checkShape(cfg map[string]any) error {
	h, ok := cfg["hooks"]
	if !ok {
		return nil
	}
	hm, ok := h.(map[string]any)
	if !ok {
		return errors.New(`"hooks" is not an object; fix it by hand, then re-run`)
	}
	for ev, v := range hm {
		if _, ok := v.([]any); !ok {
			return fmt.Errorf(`"hooks.%s" is not an array; fix it by hand, then re-run`, ev)
		}
	}
	return nil
}

// write backs the original up once, then replaces path atomically with data,
// keeping its mode (0600 for a new file).
func write(path string, d *document, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if d.exists {
		bak := path + backupSuffix
		if _, err := os.Lstat(bak); errors.Is(err, fs.ErrNotExist) {
			if err := atomicWrite(bak, d.raw, d.mode); err != nil {
				return fmt.Errorf("back up %s: %w", path, err)
			}
		}
	}
	return atomicWrite(path, data, d.mode)
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// --- shared merge for the {"hooks": {Event: [{matcher?, hooks: [...]}]}} shape

// ourEntry is the matcher group unasked adds to each event.
func ourEntry(cmd string) map[string]any {
	return map[string]any{"hooks": []any{map[string]any{
		"type": "command", "command": cmd, "timeout": json.Number(strconv.Itoa(hookTimeout)),
	}}}
}

func isOurs(h any) bool {
	m, ok := h.(map[string]any)
	if !ok {
		return false
	}
	c, _ := m["command"].(string)
	return strings.Contains(c, marker)
}

// installEvents makes each event hold exactly one unasked entry, running cmd.
func installEvents(cfg map[string]any, events []string, cmd string) (map[string]any, bool) {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	changed := false
	for _, ev := range events {
		groups, _ := hooks[ev].([]any)
		if hasExactly(groups, cmd) {
			continue
		}
		groups, _ = removeOurs(groups)
		hooks[ev] = append(groups, ourEntry(cmd))
		changed = true
	}
	if changed {
		cfg["hooks"] = hooks
	}
	return cfg, changed
}

// hasExactly reports whether groups hold one unasked hook, in its own group,
// with exactly cmd and our type and timeout.
func hasExactly(groups []any, cmd string) bool {
	found := 0
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			if !isOurs(h) {
				continue
			}
			found++
			hm := h.(map[string]any)
			t, _ := hm["timeout"].(json.Number)
			if len(hs) != 1 || len(gm) != 1 || len(hm) != 3 || hm["command"] != cmd || hm["type"] != "command" || t.String() != strconv.Itoa(hookTimeout) {
				return false
			}
		}
	}
	return found == 1
}

// removeOurs drops unasked hooks from groups, and groups left empty by that.
func removeOurs(groups []any) ([]any, bool) {
	out := make([]any, 0, len(groups))
	changed := false
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		hs, ok2 := gm["hooks"].([]any)
		if !ok || !ok2 {
			out = append(out, g)
			continue
		}
		kept := make([]any, 0, len(hs))
		for _, h := range hs {
			if isOurs(h) {
				changed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 && len(hs) > 0 {
			continue
		}
		if len(kept) != len(hs) {
			gm["hooks"] = kept
		}
		out = append(out, gm)
	}
	return out, changed
}

// uninstallAll removes unasked hooks from every event, and events (and the
// hooks object) that only held unasked hooks.
func uninstallAll(cfg map[string]any) (map[string]any, bool) {
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		return cfg, false
	}
	changed := false
	for ev, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		kept, c := removeOurs(groups)
		if !c {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if changed && len(hooks) == 0 {
		delete(cfg, "hooks")
	}
	return cfg, changed
}

// parseCommon reads the payload fields Claude Code and Codex share.
func parseCommon(stdin []byte) (Payload, error) {
	var v struct {
		Event          string `json:"hook_event_name"`
		Session        string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if err := json.Unmarshal(stdin, &v); err != nil {
		return Payload{}, fmt.Errorf("hook payload: %w", err)
	}
	if v.Event == "" {
		return Payload{}, errors.New("hook payload: no hook_event_name")
	}
	return Payload{Event: v.Event, Session: v.Session, TranscriptPath: v.TranscriptPath, Cwd: v.Cwd, StopHookActive: v.StopHookActive}, nil
}

// blockResponse writes {"decision":"block","reason":<feedback text>}, the
// stop-hook answer both Claude Code and Codex read as "keep going with this".
func blockResponse(w io.Writer, rep *report.Report, feedback bool) int {
	if !feedback {
		return 0
	}
	text, err := FeedbackText(rep)
	if err != nil {
		return 0
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(map[string]string{"decision": "block", "reason": text}) != nil {
		return 0
	}
	w.Write(buf.Bytes())
	return 0
}

// instruction ends every feedback message.
const instruction = `Drop with: unasked --drop <ids>  — or keep one with a reason: unasked --keep <id> --reason "<why the task needed it>". Deletions, test/CI and dependency changes can only be kept by the user.`

// feedbackMarker begins every feedback message; adapters exclude messages
// starting with it from the user's prompts.
const feedbackMarker = "[unasked report]"

// FeedbackText is the report as the agent sees it: the marker line, the text
// report, and the drop/keep instruction.
func FeedbackText(rep *report.Report) (string, error) {
	var b strings.Builder
	b.WriteString(feedbackMarker + "\n")
	if err := report.WriteText(&b, rep, false, false); err != nil {
		return "", err
	}
	b.WriteString("\n" + instruction)
	return b.String(), nil
}

// --- order-preserving JSON

// keyOrder records the key order of every object in the document, keyed by
// its JSON path.
func keyOrder(dec *json.Decoder, path string, out map[string][]string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		var keys []string
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			k := kt.(string)
			keys = append(keys, k)
			if err := keyOrder(dec, path+"\x00"+k, out); err != nil {
				return err
			}
		}
		out[path] = keys
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if err := keyOrder(dec, path+"\x00"+strconv.Itoa(i), out); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}

// newKeyOrder orders the keys of objects unasked creates.
var newKeyOrder = []string{"matcher", "hooks", "type", "command", "timeout"}

// encode writes v as two-space-indented JSON, objects in their original key
// order with new keys after them, numbers verbatim and no HTML escaping.
func encode(w *bytes.Buffer, v any, path string, order map[string][]string, indent string) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			w.WriteString("{}")
			return
		}
		keys := orderedKeys(x, order[path])
		w.WriteString("{\n")
		for i, k := range keys {
			w.WriteString(indent + "  ")
			writeScalar(w, k)
			w.WriteString(": ")
			encode(w, x[k], path+"\x00"+k, order, indent+"  ")
			if i < len(keys)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString(indent + "}")
	case []any:
		if len(x) == 0 {
			w.WriteString("[]")
			return
		}
		w.WriteString("[\n")
		for i, e := range x {
			w.WriteString(indent + "  ")
			encode(w, e, path+"\x00"+strconv.Itoa(i), order, indent+"  ")
			if i < len(x)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString(indent + "]")
	default:
		writeScalar(w, v)
	}
}

func orderedKeys(m map[string]any, known []string) []string {
	keys := make([]string, 0, len(m))
	seen := map[string]bool{}
	add := func(k string) {
		if _, ok := m[k]; ok && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, k := range known {
		add(k)
	}
	if len(known) == 0 {
		for _, k := range newKeyOrder {
			add(k)
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func writeScalar(w *bytes.Buffer, v any) {
	if n, ok := v.(json.Number); ok {
		w.WriteString(n.String())
		return
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		w.WriteString("null")
		return
	}
	w.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

// clone deep-copies a decoded JSON value so Status can probe with Uninstall.
func clone(m map[string]any) map[string]any {
	return cloneAny(m).(map[string]any)
}

func cloneAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cloneAny(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneAny(e)
		}
		return out
	}
	return v
}
