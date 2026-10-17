// Package report assembles scored hunks into the report that unasked prints:
// the frozen JSON v1 contract, a text view for humans, a markdown view for PR
// comments, and the last.json cache that lets --drop resolve display ids.
package report

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/keep"
	"github.com/sadayamuthu/unasked/internal/model"
)

const (
	ResultClean   = "clean"
	ResultUnclear = "unclear"
	ResultUnasked = "unasked"
)

// AdapterInfo describes how the transcript was read.
type AdapterInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Level          string `json:"level"`
	DegradedReason string `json:"degraded_reason"`
}

// RangeInfo is the diffed range. BaseSource is captured, reflog, head, since or staged.
type RangeInfo struct {
	Base       string `json:"base"`
	Head       string `json:"head"`
	BaseSource string `json:"base_source"`
}

// HunkJSON is one hunk in the report. The ID is display-only; Hash is the identity.
type HunkJSON struct {
	ID                  string   `json:"id"`
	File                string   `json:"file"`
	Lines               string   `json:"lines"`
	Verdict             string   `json:"verdict"`
	Label               string   `json:"label"`
	Severity            string   `json:"severity,omitempty"`
	Asked               float64  `json:"asked"`
	Said                float64  `json:"said"`
	Tell                *string  `json:"tell"`
	Statement           string   `json:"statement,omitempty"`
	Turn                int      `json:"turn"`
	RemovedSymbols      []string `json:"removed_symbols,omitempty"`
	ReferencesElsewhere int      `json:"references_elsewhere"`
	Summary             string   `json:"summary,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	Hash                string   `json:"hash"`
	HighRisk            bool     `json:"high_risk"`
	FileChange          string   `json:"file_change,omitempty"` // rename|mode|add|delete for a file-level hunk
	OldFile             string   `json:"old_file,omitempty"`
}

// KeptJSON is a hunk silenced by --keep. ID is empty when the hunk is not in this diff.
type KeptJSON struct {
	ID     string `json:"id"`
	Hash   string `json:"hash"`
	Reason string `json:"reason"`
	By     string `json:"by"`
}

// Stats counts hunks. Collateral hunks are counted only in Collateral.
type Stats struct {
	Hunks      int `json:"hunks"`
	Invited    int `json:"invited"`
	Unasked    int `json:"unasked"`
	Unclear    int `json:"unclear"`
	Collateral int `json:"collateral"`
	Binary     int `json:"binary"`
}

// Report is the JSON v1 document.
type Report struct {
	Version    int               `json:"version"`
	Agent      string            `json:"agent"`
	Session    string            `json:"session"`
	Transcript string            `json:"transcript"`
	Adapter    AdapterInfo       `json:"adapter"`
	Range      RangeInfo         `json:"range"`
	Thresholds invite.Thresholds `json:"thresholds"`
	Prompts    []model.Prompt    `json:"prompts"`
	Result     string            `json:"result"`
	Hunks      []HunkJSON        `json:"hunks"`
	Kept       []KeptJSON        `json:"kept"`
	Stats      Stats             `json:"stats"`
	Warnings   []string          `json:"warnings"`
	Turns      int               `json:"turns"`
	Duration   string            `json:"duration,omitempty"`
}

func (h HunkJSON) collateral() bool {
	return h.Verdict == string(model.VerdictInvited) && h.Reason == "collateral"
}

// Build assembles the report. scored is the post-keep list (keep.Merge) and
// kept the entries that matched. Thresholds default to invite.Default; callers
// that shifted them set Report.Thresholds afterwards.
func Build(sel *adapter.Selection, scored []model.Scored, kept []keep.Entry) *Report {
	r := &Report{
		Version:    1,
		Thresholds: invite.Default,
		Prompts:    []model.Prompt{},
		Hunks:      []HunkJSON{},
		Kept:       []KeptJSON{},
		Warnings:   []string{},
	}
	tr := sel.Transcript
	if tr == nil {
		tr = &model.Transcript{}
	}
	if len(sel.Sessions) > 0 {
		r.Agent, r.Session = sel.Sessions[0].Agent, sel.Sessions[0].ID
	}
	switch {
	case len(sel.Sessions) == 0:
		r.Transcript = "none"
	case sel.Caps.Degraded != "":
		r.Transcript = "degraded"
	default:
		r.Transcript = "present"
	}
	r.Adapter = AdapterInfo{Name: r.Agent, Version: tr.Version, Level: sel.Caps.Level.String(), DegradedReason: sel.Caps.Degraded}
	r.Range = RangeInfo{Base: sel.FromTree, Head: sel.ToTree, BaseSource: sel.BaseSource}
	r.Prompts = append(r.Prompts, tr.Prompts...)
	r.Warnings = append(r.Warnings, sel.Warnings...)
	r.Turns = tr.Turns
	if !tr.Start.IsZero() && tr.End.After(tr.Start) {
		r.Duration = formatDuration(tr.End.Sub(tr.Start))
	}
	r.Stats.Binary = sel.Binary

	agentKept := map[string]bool{}
	for _, e := range kept {
		if e.By != keep.ByUser { // anything but a user keep counts as the agent's
			agentKept[e.Hash] = true
		}
	}
	for _, s := range scored {
		if agentKept[s.Hunk.Hash] && s.Verdict == model.VerdictInvited {
			s.Verdict = model.VerdictUnclear
		}
		h := hunkJSON(s)
		r.Hunks = append(r.Hunks, h)
		switch {
		case h.collateral():
			r.Stats.Collateral++
			continue
		case s.Verdict == model.VerdictInvited:
			r.Stats.Invited++
		case s.Verdict == model.VerdictUnasked:
			r.Stats.Unasked++
		default:
			r.Stats.Unclear++
		}
		r.Stats.Hunks++
	}

	ids := map[string]string{}
	for _, h := range sel.Hunks {
		ids[h.Hash] = h.ID
	}
	for _, h := range r.Hunks {
		ids[h.Hash] = h.ID
	}
	for _, e := range kept {
		r.Kept = append(r.Kept, KeptJSON{ID: ids[e.Hash], Hash: e.Hash, Reason: e.Reason, By: string(e.By)})
	}

	switch {
	case r.Stats.Unasked > 0:
		r.Result = ResultUnasked
	case r.Stats.Unclear > 0:
		r.Result = ResultUnclear
	default:
		r.Result = ResultClean // includes DiffOnly with zero hunks: nothing to judge
	}
	if r.Result == ResultClean && r.Stats.Hunks > 0 && (r.Transcript == "none" || sel.Caps.Level == adapter.DiffOnly) {
		r.Result = ResultUnclear
		r.Warnings = append(r.Warnings, "no transcript: hunks could not be checked against your prompts")
	}
	if r.Stats.Binary > 0 {
		r.Warnings = append(r.Warnings, count(r.Stats.Binary, "binary file")+" changed — not judged")
		if r.Result == ResultClean {
			r.Result = ResultUnclear
		}
	}
	return r
}

func hunkJSON(s model.Scored) HunkJSON {
	h := s.Hunk
	end := h.NewStart
	if h.NewLines > 1 {
		end = h.NewStart + h.NewLines - 1
	}
	out := HunkJSON{
		ID: h.ID, File: h.File, Lines: fmt.Sprintf("%d-%d", h.NewStart, end),
		Verdict: string(s.Verdict), Label: string(s.Label), Severity: string(s.Severity),
		Asked: s.Asked, Said: s.Said, Turn: s.Turn,
		RemovedSymbols: s.RemovedSymbols, ReferencesElsewhere: s.XRefs,
		Summary: s.Summary, Reason: s.Reason, Hash: h.Hash, HighRisk: s.HighRisk,
		FileChange: h.FileChange, OldFile: h.OldFile,
	}
	if s.Tell != "" {
		t := s.Tell
		out.Tell = &t
	}
	if s.Event != nil {
		out.Statement = s.Event.Statement
	}
	return out
}

// ExitCode maps the result to the process exit code.
func (r *Report) ExitCode() int {
	switch r.Result {
	case ResultUnasked:
		return model.ExitUnasked
	case ResultUnclear:
		return model.ExitUnclear
	}
	return model.ExitClean
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d sec", int(d.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
}

// byVerdict returns the non-collateral hunks with the given verdict.
func (r *Report) byVerdict(v model.Verdict) []HunkJSON {
	var out []HunkJSON
	for _, h := range r.Hunks {
		if h.Verdict == string(v) && !h.collateral() {
			out = append(out, h)
		}
	}
	return out
}

func (r *Report) collateralHunks() []HunkJSON {
	var out []HunkJSON
	for _, h := range r.Hunks {
		if h.collateral() {
			out = append(out, h)
		}
	}
	return out
}

func (r *Report) countsLine() string {
	s := fmt.Sprintf("%s changed · %d invited · %d unasked · %d unclear",
		count(r.Stats.Hunks, "hunk"), r.Stats.Invited, r.Stats.Unasked, r.Stats.Unclear)
	if r.Stats.Binary > 0 {
		s += fmt.Sprintf(" · %d binary", r.Stats.Binary)
	}
	return s
}

func (r *Report) sessionLine() string {
	var parts []string
	if r.Agent != "" {
		parts = append(parts, r.Agent)
	}
	parts = append(parts, count(r.Turns, "turn"))
	if r.Duration != "" {
		parts = append(parts, r.Duration)
	}
	id := oneLine(r.Session)
	if id == "" {
		id = "(none)"
	}
	return fmt.Sprintf("Session %s (%s)", id, strings.Join(parts, ", "))
}

func (r *Report) transcriptLine() string {
	t := r.Transcript
	if t == "degraded" && r.Adapter.DegradedReason != "" {
		t += ": " + oneLine(r.Adapter.DegradedReason)
	}
	return fmt.Sprintf("transcript: %s · adapter level: %s", t, r.Adapter.Level)
}

// firstPrompt wraps the first prompt to at most two lines of width cols.
func (r *Report) firstPrompt(width int) []string {
	if len(r.Prompts) == 0 {
		return nil
	}
	words := strings.Fields(clean(r.Prompts[0].Text))
	var lines []string
	cur := ""
	i := 0
	for ; i < len(words); i++ {
		next := words[i]
		if cur != "" {
			next = cur + " " + words[i]
		}
		if len([]rune(next)) > width && cur != "" {
			if len(lines) == 1 {
				break
			}
			lines = append(lines, cur)
			cur = words[i]
			continue
		}
		cur = next
	}
	if i < len(words) {
		cur += "…"
	}
	return append(lines, cur)
}

func (h HunkJSON) location() string {
	if h.FileChange != "" {
		return clean(h.File) // a file-level hunk has no lines
	}
	return clean(h.File) + ":" + h.Lines
}

func (h HunkJSON) blurb() string {
	s := oneLine(h.Summary)
	if s == "" {
		s = oneLine(h.Reason)
	}
	if h.HighRisk {
		s = "⚠ " + s
	}
	return s
}

// count writes n and noun, with an "s" unless n is 1.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// oneLine strips control characters (see clean) and collapses all
// whitespace, including newlines, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }

// escapeRe matches ANSI CSI and OSC escape sequences.
var escapeRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)

// clean removes ANSI escape sequences and every other control character
// except newline and tab from text a transcript, a file name or a keep
// reason supplies, so printing it cannot move the cursor or recolour the
// terminal (m6).
func clean(s string) string {
	s = escapeRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return -1
		}
		return r
	}, s)
}
