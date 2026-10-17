// Package fixtures replays labelled agent sessions through the real report
// pipeline and measures them against the release gates (spec §10): no
// hand-labelled unasked hunk may be reported invited, and at most 20% of the
// listed hunks may be ones a human labelled invited.
//
// A case directory (testdata/fixtures/<agent>/<case>/) holds:
//
//	before/           files committed before the session
//	pre/              optional: uncommitted developer edits made before the
//	                  session starts (they belong to the captured baseline)
//	after/            files the agent changed or created; "<path>.deleted"
//	                  marks a deletion
//	transcript.jsonl  the agent's transcript; "@CWD@" stands for the repo
//	labels.json       {"<file>:<key line>": "invited"|"unasked"}
//	expected.json     the report JSON, regenerated with -update
//
// A file name ending in ".nofmt" is written without that suffix; it marks Go
// sources that are deliberately not gofmt-clean, so `gofmt -l .` stays quiet.
package fixtures

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/adapter"
	_ "github.com/sadayamuthu/unasked/internal/adapter/claudecode" // registers claude-code
	_ "github.com/sadayamuthu/unasked/internal/adapter/codex"      // registers codex
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/pipeline"
	"github.com/sadayamuthu/unasked/internal/report"
)

const (
	cwdPlaceholder = "@CWD@"
	deletedSuffix  = ".deleted"
	nofmtSuffix    = ".nofmt"
)

// Labels a human may give a hunk.
const (
	Invited = "invited"
	Unasked = "unasked"
)

// Case is one labelled session.
type Case struct {
	Name   string // "<agent>/<case>"
	Agent  string // adapter name: the parent directory
	Dir    string
	Labels map[string]string // key (see Key) → Invited | Unasked
}

// Load returns the cases under dir (dir/<agent>/<case>/), sorted by name.
// A directory is a case when it holds transcript.jsonl.
func Load(dir string) ([]Case, error) {
	agents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		cases, err := os.ReadDir(filepath.Join(dir, a.Name()))
		if err != nil {
			return nil, err
		}
		for _, c := range cases {
			cdir := filepath.Join(dir, a.Name(), c.Name())
			if _, err := os.Stat(filepath.Join(cdir, "transcript.jsonl")); err != nil {
				continue
			}
			labels, err := loadLabels(filepath.Join(cdir, "labels.json"))
			if err != nil {
				return nil, err
			}
			out = append(out, Case{Name: a.Name() + "/" + c.Name(), Agent: a.Name(), Dir: cdir, Labels: labels})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadLabels(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range m {
		if v != Invited && v != Unasked {
			return nil, fmt.Errorf("%s: %q: label %q is not %q or %q", path, k, v, Invited, Unasked)
		}
	}
	return m, nil
}

// Key identifies a hunk in labels.json by content: "<file>:<line>", where
// line is the hunk's first non-blank added line, or its first non-blank
// removed line when it adds none, with surrounding whitespace trimmed.
func Key(h model.Hunk) string {
	for _, ls := range [][]string{h.Added, h.Removed} {
		for _, l := range ls {
			if t := strings.TrimSpace(l); t != "" {
				return h.File + ":" + t
			}
		}
	}
	return h.File + ":"
}

// Gate counts the release-gate figures. Listed hunks have verdict unasked or
// unclear; a miss is a hunk labelled unasked but reported invited (folded
// collateral included); a false alarm is a listed hunk labelled invited.
type Gate struct{ Misses, FalseAlarms, Listed int }

// Add returns the sum of g and o.
func (g Gate) Add(o Gate) Gate {
	return Gate{g.Misses + o.Misses, g.FalseAlarms + o.FalseAlarms, g.Listed + o.Listed}
}

// Rate is FalseAlarms/Listed, 0 when nothing is listed.
func (g Gate) Rate() float64 {
	if g.Listed == 0 {
		return 0
	}
	return float64(g.FalseAlarms) / float64(g.Listed)
}

func (g Gate) String() string {
	return fmt.Sprintf("Misses=%d FalseAlarms=%d Listed=%d rate=%.3f", g.Misses, g.FalseAlarms, g.Listed, g.Rate())
}

// TruthHunk is one reported hunk beside its human label.
type TruthHunk struct {
	Key, Label, Reason string
	Verdict            model.Verdict
}

// Listed reports whether the report lists the hunk (unasked or unclear).
func (h TruthHunk) Listed() bool { return h.Verdict != model.VerdictInvited }

// Miss reports whether a hunk labelled unasked was reported invited.
func (h TruthHunk) Miss() bool { return h.Label == Unasked && h.Verdict == model.VerdictInvited }

// FalseAlarm reports whether a hunk labelled invited was listed.
func (h TruthHunk) FalseAlarm() bool { return h.Label == Invited && h.Listed() }

// Truth pairs every hunk of one report with its label.
type Truth struct{ Hunks []TruthHunk }

// Gate tallies t.
func (t Truth) Gate() Gate {
	var g Gate
	for _, h := range t.Hunks {
		if h.Listed() {
			g.Listed++
		}
		if h.Miss() {
			g.Misses++
		}
		if h.FalseAlarm() {
			g.FalseAlarms++
		}
	}
	return g
}

// Session is a case replayed into a fresh repository: the before files
// committed, the start captured, the agent's changes applied and the
// transcript captured at stop. Selection is what the last Run chose.
type Session struct {
	Case      Case
	Repo      *gitx.Repo
	Selection *adapter.Selection
}

// RunCase replays c and reports it through pipeline.Run with thresholds th.
// It fails t when a reported hunk has no label, when a label matches no
// reported hunk, or when two hunks share a key.
func RunCase(t testing.TB, c Case, th invite.Thresholds) (*report.Report, Truth) {
	t.Helper()
	return Replay(t, c).Run(t, th)
}

// Replay builds c's repository and captured session. RunCase calls it; the
// calibration reuses one replay across many thresholds.
func Replay(t testing.TB, c Case) *Session {
	t.Helper()
	g := gittest.New(t)
	write := func(sub string) {
		root := filepath.Join(c.Dir, sub)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if sub == "after" && strings.HasSuffix(rel, deletedSuffix) {
				return os.Remove(filepath.Join(g.Dir, filepath.FromSlash(strings.TrimSuffix(rel, deletedSuffix))))
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			g.WriteBytes(strings.TrimSuffix(rel, nofmtSuffix), data)
			return nil
		})
		if err != nil && !(sub == "pre" && errors.Is(err, fs.ErrNotExist)) {
			t.Fatalf("%s: %s: %v", c.Name, sub, err)
		}
	}
	write("before")
	g.Commit("before")
	write("pre")

	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Base(c.Dir)
	if _, err := adapter.RecordStart(r, c.Agent, session, g.Dir); err != nil {
		t.Fatalf("%s: RecordStart: %v", c.Name, err)
	}
	write("after")

	raw, err := os.ReadFile(filepath.Join(c.Dir, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := json.Marshal(g.Dir)
	text := strings.ReplaceAll(string(raw), cwdPlaceholder, string(cwd[1:len(cwd)-1]))
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RecordStop(r, c.Agent, session, tp, nil); err != nil {
		t.Fatalf("%s: RecordStop: %v", c.Name, err)
	}
	return &Session{Case: c, Repo: r}
}

// Run reports s through pipeline.Run with th and keeps the selection for
// Score.
func (s *Session) Run(t testing.TB, th invite.Thresholds) (*report.Report, Truth) {
	t.Helper()
	rep, scored, sel, err := pipeline.Run(s.Repo, adapter.Request{}, th)
	if err != nil {
		t.Fatalf("%s: pipeline.Run: %v", s.Case.Name, err)
	}
	s.Selection = sel
	return rep, s.truth(t, rep, scored)
}

// Score re-scores the selection of an earlier Run with th through
// pipeline.Score, the scoring half of pipeline.Run, without re-selecting.
func (s *Session) Score(t testing.TB, th invite.Thresholds) (*report.Report, Truth) {
	t.Helper()
	if s.Selection == nil {
		t.Fatalf("%s: Score before Run", s.Case.Name)
	}
	rep, scored, err := pipeline.Score(s.Repo, s.Selection, th)
	if err != nil {
		t.Fatalf("%s: pipeline.Score: %v", s.Case.Name, err)
	}
	return rep, s.truth(t, rep, scored)
}

// truth matches the report's hunks (verdicts as reported, after keeps) to
// the labels by content key.
func (s *Session) truth(t testing.TB, rep *report.Report, scored []model.Scored) Truth {
	t.Helper()
	c := s.Case
	if rep.Agent != c.Agent || rep.Transcript != "present" {
		t.Fatalf("%s: agent %q transcript %q (warnings %v): the captured session was not used", c.Name, rep.Agent, rep.Transcript, rep.Warnings)
	}
	keys := map[string]string{} // hash → key
	for _, sc := range scored {
		keys[sc.Hunk.Hash] = Key(sc.Hunk)
	}
	var tr Truth
	bad := false
	seen := map[string]bool{}
	for _, h := range rep.Hunks {
		k := keys[h.Hash]
		if seen[k] {
			t.Fatalf("%s: two hunks share the label key %q", c.Name, k)
		}
		seen[k] = true
		label, ok := c.Labels[k]
		if !ok {
			t.Errorf("%s: hunk %s (%s, %s) has no label; add %q to labels.json", c.Name, h.ID, h.Verdict, h.Reason, k)
			bad = true
			continue
		}
		tr.Hunks = append(tr.Hunks, TruthHunk{Key: k, Label: label, Verdict: model.Verdict(h.Verdict), Reason: h.Reason})
	}
	for k := range c.Labels {
		if !seen[k] {
			t.Errorf("%s: label %q matches no reported hunk", c.Name, k)
			bad = true
		}
	}
	if bad {
		t.FailNow()
	}
	return tr
}
