package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/keep"
	"github.com/sadayamuthu/unasked/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sc(id, file string, start, n int, v model.Verdict, l model.Label, sev model.Severity) model.Scored {
	return model.Scored{
		Hunk:    model.Hunk{ID: id, File: file, NewStart: start, NewLines: n, Hash: hashOf(id)},
		Verdict: v, Label: l, Severity: sev, HighRisk: l.HighRisk(),
	}
}

func hashOf(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:12]
}

func fixture() (*adapter.Selection, []model.Scored, []keep.Entry) {
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	sel := &adapter.Selection{
		Sessions: []adapter.Session{{Agent: "claude-code", ID: "8f2c1d", Start: t0, End: t0.Add(22 * time.Minute)}},
		Transcript: &model.Transcript{
			Prompts: []model.Prompt{{Turn: 0, Text: "Add rate limiting to the public API: 60/min anon, 600/min keyed, 429 + Retry-After, limits configurable via env."}},
			Version: "2.1.0", Start: t0, End: t0.Add(22 * time.Minute), Turns: 41,
		},
		Caps:       adapter.Capabilities{Level: adapter.Full},
		FromTree:   "9f3c1ad",
		ToTree:     "worktree",
		BaseSource: "captured",
		Warnings:   []string{"pre-existing changes may be included"},
		Binary:     1,
	}
	var s []model.Scored
	for i, f := range []string{"internal/rate/limiter.go", "internal/rate/limiter_test.go", "internal/http/server.go", "internal/config/env.go",
		"internal/http/respond.go", "cmd/api/main.go", "README.md", "internal/rate/keys.go"} {
		h := sc("h0"+string(rune('1'+i)), f, 10+i, 5, model.VerdictInvited, model.LabelFeature, model.SeverityNone)
		h.Reason = "matches your request"
		if i == 0 {
			h.Reason = "asked: rate limiting"
		}
		s = append(s, h)
	}
	u1 := sc("h12", "internal/log/middleware.go", 41, 18, model.VerdictUnasked, model.LabelRefactor, model.SeverityWarning)
	u1.Summary = "rewrote timestamp formatting in the logging middleware"
	u1.Event = &model.EditEvent{Statement: "while here, standardizing the timestamp format"}
	u1.Tell = "while here"
	u1.Turn = 22
	u2 := sc("h13", "internal/http/headers.go", 12, 8, model.VerdictUnasked, model.LabelDeletion, model.SeverityError)
	u2.Summary = "deleted a response header still set in 3 handlers"
	u2.Event = &model.EditEvent{Statement: "removing the deprecated X-Request-Id header"}
	u2.Turn = 23
	u2.RemovedSymbols = []string{"X-Request-Id"}
	u2.XRefs = 3
	u2.Said = 0.04
	c := sc("h09", "internal/config/env.go", 70, 19, model.VerdictUnclear, model.LabelFeature, model.SeverityWarning)
	c.Summary = "added helper parseDuration"
	c.Reason = "used by invited hunk h07"
	c.Asked, c.Said = 0.09, 0.31
	s = append(s, u1, u2, c)
	for i, f := range []string{"a.go", "b.go", "b.go"} {
		h := sc("h2"+string(rune('0'+i)), f, 1, 1, model.VerdictInvited, model.LabelCollateral, model.SeverityInfo)
		h.Reason = "collateral"
		s = append(s, h)
	}
	k := []keep.Entry{{Hash: "999999999999", Branch: "main", Reason: "harmless", By: keep.ByUser}}
	return sel, s, k
}

func TestResultAndExitCode(t *testing.T) {
	mk := func(vs ...model.Verdict) []model.Scored {
		var s []model.Scored
		for _, v := range vs {
			s = append(s, sc("h01", "a.go", 1, 1, v, model.LabelFeature, model.SeverityNone))
		}
		return s
	}
	coll := sc("h02", "a.go", 5, 1, model.VerdictInvited, model.LabelCollateral, model.SeverityInfo)
	coll.Reason = "collateral"
	for _, tc := range []struct {
		name   string
		scored []model.Scored
		level  adapter.Level
		result string
		code   int
	}{
		{"none", nil, adapter.Full, "clean", 0},
		{"diffonly zero hunks", nil, adapter.DiffOnly, "clean", 0},
		{"invited", mk(model.VerdictInvited), adapter.Full, "clean", 0},
		{"collateral only", []model.Scored{coll}, adapter.Full, "clean", 0},
		{"none transcript all invited", mk(model.VerdictInvited), -1, "unclear", 1},
		{"diffonly all invited", mk(model.VerdictInvited), adapter.DiffOnly, "unclear", 1},
		{"degraded partial all invited", mk(model.VerdictInvited), -2, "clean", 0},
		{"binary only", nil, adapter.Full, "unclear", 1},
		{"binary plus unasked", mk(model.VerdictUnasked), adapter.Full, "unasked", 2},
		{"unclear", mk(model.VerdictInvited, model.VerdictUnclear), adapter.Full, "unclear", 1},
		{"unasked", mk(model.VerdictUnclear, model.VerdictUnasked), adapter.Full, "unasked", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel := &adapter.Selection{Transcript: &model.Transcript{}, Caps: adapter.Capabilities{Level: tc.level}}
			switch tc.level {
			case -1: // no session
				sel.Caps.Level = adapter.Full
			case -2: // degraded session
				sel.Caps = adapter.Capabilities{Level: adapter.Partial, Degraded: "unknown record"}
				sel.Sessions = []adapter.Session{{Agent: "x"}}
			default:
				sel.Sessions = []adapter.Session{{Agent: "x"}}
			}
			if strings.HasPrefix(tc.name, "binary") {
				sel.Binary = 2
			}
			r := Build(sel, tc.scored, nil)
			if r.Result != tc.result || r.ExitCode() != tc.code {
				t.Fatalf("got %s/%d want %s/%d", r.Result, r.ExitCode(), tc.result, tc.code)
			}
		})
	}
}

func TestAgentKeepMakesUnclearNotClean(t *testing.T) {
	sel := &adapter.Selection{Transcript: &model.Transcript{}}
	inv := sc("h01", "a.go", 1, 1, model.VerdictInvited, model.LabelFeature, model.SeverityNone)
	inv.Reason = "kept by agent: fine"
	un := sc("h02", "b.go", 1, 1, model.VerdictUnclear, model.LabelFeature, model.SeverityWarning)
	un.Reason = "kept by agent: ok"
	for _, h := range []model.Scored{inv, un} {
		r := Build(sel, []model.Scored{h}, []keep.Entry{{Hash: h.Hunk.Hash, Reason: "x", By: keep.ByAgent}})
		if r.Result != "unclear" || r.ExitCode() != 1 {
			t.Fatalf("%s: got %s/%d", h.Hunk.ID, r.Result, r.ExitCode())
		}
		if r.Stats.Unclear != 1 {
			t.Fatalf("stats %+v", r.Stats)
		}
	}
}

func TestBuildFields(t *testing.T) {
	sel, s, k := fixture()
	r := Build(sel, s, k)
	if r.Agent != "claude-code" || r.Session != "8f2c1d" || r.Transcript != "present" || r.Duration != "22 min" || r.Turns != 41 {
		t.Fatalf("%+v", r)
	}
	if r.Stats != (Stats{Hunks: 11, Invited: 8, Unasked: 2, Unclear: 1, Collateral: 3, Binary: 1}) {
		t.Fatalf("stats %+v", r.Stats)
	}
	if r.Adapter.Level != "full" || r.Range.BaseSource != "captured" || r.Range.Base != "9f3c1ad" {
		t.Fatalf("%+v %+v", r.Adapter, r.Range)
	}
	sel.Sessions = nil
	if r := Build(sel, nil, nil); r.Transcript != "none" || r.Agent != "" {
		t.Fatalf("%+v", r)
	}
	sel.Sessions = []adapter.Session{{Agent: "x"}}
	sel.Caps.Degraded = "unknown format"
	if r := Build(sel, nil, nil); r.Transcript != "degraded" || r.Adapter.DegradedReason != "unknown format" {
		t.Fatalf("%+v", r)
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs (run with -update):\n%s", name, got)
	}
}

func TestGoldenText(t *testing.T) {
	sel, s, k := fixture()
	r := Build(sel, s, k)
	var b bytes.Buffer
	if err := WriteText(&b, r, false, false); err != nil {
		t.Fatal(err)
	}
	golden(t, "report.txt", b.Bytes())
	b.Reset()
	WriteText(&b, r, true, false)
	golden(t, "report_all.txt", b.Bytes())
	b.Reset()
	WriteText(&b, r, false, true)
	golden(t, "report_color.txt", b.Bytes())
	if !strings.Contains(b.String(), "\x1b[4mwhile here\x1b[24m") {
		t.Fatal("tell not underlined")
	}
}

func TestGoldenMarkdown(t *testing.T) {
	sel, s, k := fixture()
	var b bytes.Buffer
	if err := WriteMarkdown(&b, Build(sel, s, k)); err != nil {
		t.Fatal(err)
	}
	golden(t, "report.md", b.Bytes())
}

func TestGoldenJSON(t *testing.T) {
	sel, s, k := fixture()
	var b bytes.Buffer
	if err := WriteJSON(&b, Build(sel, s, k)); err != nil {
		t.Fatal(err)
	}
	golden(t, "report.json", b.Bytes())
}

func TestJSONMatchesSchema(t *testing.T) {
	schema, err := os.ReadFile("../../schema/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var sch map[string]any
	if err := json.Unmarshal(schema, &sch); err != nil {
		t.Fatal(err)
	}
	check := func(name string, data []byte) {
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if errs := validate(sch, doc, "$"); len(errs) > 0 {
			t.Fatalf("%s: %v", name, errs)
		}
	}
	golden, _ := os.ReadFile("testdata/report.json")
	check("golden", golden)
	var b bytes.Buffer
	WriteJSON(&b, Build(&adapter.Selection{Transcript: &model.Transcript{}}, nil, nil))
	check("empty", b.Bytes())
	// the validator really rejects
	if errs := validate(sch, map[string]any{"version": 2.0}, "$"); len(errs) == 0 {
		t.Fatal("validator accepted a bad document")
	}
}

func TestResolveIDsAndHashes(t *testing.T) {
	sel, s, k := fixture()
	r := Build(sel, s, k)
	got, err := r.Resolve([]string{"h12", "0123456789ab"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != r.Hunks[8].Hash || got[1] != "0123456789ab" {
		t.Fatalf("%v", got)
	}
}

func TestResolveUnknownID(t *testing.T) {
	sel, s, k := fixture()
	_, err := Build(sel, s, k).Resolve([]string{"h99"})
	if err == nil || !strings.Contains(err.Error(), "h99") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Build(sel, s, k).Resolve([]string{"zzz"}); err == nil {
		t.Fatal("want error")
	}
}

func TestSaveLoadLast(t *testing.T) {
	repo := gittest.New(t)
	g, err := gitx.Open(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLast(g); err == nil {
		t.Fatal("want error before save")
	}
	sel, s, k := fixture()
	r := Build(sel, s, k)
	if err := SaveLast(g, r); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(g.GitDir, "unasked", "last.json")
	if fi, err := os.Stat(p); err != nil || (fi.Mode().Perm() != 0o600 && os.PathSeparator == '/') {
		t.Fatalf("%v %v", fi, err)
	}
	r2 := Build(sel, s[:1], k)
	if err := SaveLast(g, r2); err != nil {
		t.Fatal(err)
	}
	if back, err := LoadLast(g); err != nil || len(back.Hunks) != 1 {
		t.Fatalf("overwrite: %v %v", back, err)
	}
	if err := SaveLast(g, r); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("stray files: %v", ents)
	}
	back, err := LoadLast(g)
	if err != nil {
		t.Fatal(err)
	}
	h, err := back.Resolve([]string{"h13"})
	if err != nil || h[0] != r.Hunks[9].Hash {
		t.Fatalf("%v %v", h, err)
	}
}

func TestSessionLineTurns(t *testing.T) {
	for _, c := range []struct {
		turns int
		want  string
	}{{0, "0 turns"}, {1, "1 turn,"}, {2, "2 turns"}} {
		r := &Report{Agent: "claude-code", Session: "s", Turns: c.turns, Duration: "1 min"}
		if got := r.sessionLine(); !strings.Contains(got, c.want) {
			t.Errorf("Turns=%d: %q, want %q", c.turns, got, c.want)
		}
	}
}

// The suggested drop command names only hunks an agent edit made: a hunk a
// shell command or the developer made (R39) is listed but left out of it.
func TestDropLineSkipsShellMade(t *testing.T) {
	sel, s, k := fixture()
	sh := sc("h14", "go.sum", 1, 2, model.VerdictUnasked, model.LabelDependency, model.SeverityError)
	sh.Reason = invite.ShellReason
	r := Build(sel, append(s, sh), k)
	var txt, md bytes.Buffer
	if err := WriteText(&txt, r, false, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&md, r); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": txt.String(), "md": md.String()} {
		if !strings.Contains(out, "go.sum") || !strings.Contains(out, "unasked --drop h12 h13\n") && !strings.Contains(out, "unasked --drop h12 h13 ") {
			t.Errorf("%s:\n%s", name, out)
		}
		if strings.Contains(out, "--drop h12 h13 h14") {
			t.Errorf("%s suggests dropping the shell-made hunk:\n%s", name, out)
		}
	}
}

// m6: text an agent or a prompt supplies (prompt, statement, summary,
// reason, file name, warnings) is printed without ANSI escapes or other
// control characters, so a transcript cannot repaint the terminal.
func TestControlCharactersStripped(t *testing.T) {
	sel, s, k := fixture()
	sel.Transcript.Prompts[0].Text = "add \x1b[31mrate\x1b[0m limiting\x07 to the API"
	sel.Warnings = append(sel.Warnings, "odd\x1b]0;pwned\x07 warning")
	for i := range s {
		if s[i].Hunk.ID == "h12" {
			s[i].Event.Statement = "while here\x1b[2J, standardizing\r the timestamp format"
			s[i].Summary = "rewrote \x1b[1mtimestamps\x1b[0m"
			s[i].Hunk.File = "internal/log/mid\x1bdleware.go"
		}
	}
	r := Build(sel, s, k)
	var txt, md bytes.Buffer
	if err := WriteText(&txt, r, true, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&md, r); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": txt.String(), "md": md.String()} {
		for _, c := range out {
			if c < 0x20 && c != '\n' || c == 0x7f || c >= 0x80 && c <= 0x9f {
				t.Errorf("%s output has control character %q:\n%q", name, c, out)
				break
			}
		}
		for _, want := range []string{"add rate limiting to the API", "rewrote timestamps", "standardizing the timestamp format", "odd warning"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s output lacks %q:\n%s", name, want, out)
			}
		}
	}
	// With color on, only our own underline escapes remain.
	txt.Reset()
	WriteText(&txt, r, false, true)
	if n := strings.Count(txt.String(), "\x1b"); n != 2 {
		t.Errorf("color output has %d escapes, want 2 (the tell underline):\n%q", n, txt.String())
	}
}

// m9: every count reads in the singular when it is one.
func TestSingularCounts(t *testing.T) {
	sel, _, _ := fixture()
	sel.Binary = 1
	inv := sc("h01", "a.go", 1, 1, model.VerdictUnasked, model.LabelRefactor, model.SeverityWarning)
	col := sc("h02", "b.go", 1, 1, model.VerdictInvited, model.LabelCollateral, model.SeverityInfo)
	col.Reason = "collateral"
	r := Build(sel, []model.Scored{inv, col}, nil)
	var b bytes.Buffer
	if err := WriteText(&b, r, false, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&b, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"1 hunk changed", "1 hunk in 1 file\n", "1 binary file changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"1 hunks", "1 files", "file(s)"} {
		if strings.Contains(out, bad) {
			t.Errorf("has %q:\n%s", bad, out)
		}
	}
}

// m10: an UNASKED hunk shows its reason when it is a special one: generic
// overlap only, or made by a shell command or by you.
func TestUnaskedSpecialReasonShown(t *testing.T) {
	sel, _, _ := fixture()
	g := sc("h01", "a.go", 1, 1, model.VerdictUnasked, model.LabelDeletion, model.SeverityError)
	g.Summary, g.Reason = "deleted function Ping", invite.GenericReason
	sh := sc("h02", "go.sum", 1, 1, model.VerdictUnasked, model.LabelDependency, model.SeverityError)
	sh.Summary, sh.Reason = "changed dependencies in go.sum", invite.ShellReason
	plain := sc("h03", "c.go", 1, 1, model.VerdictUnasked, model.LabelRefactor, model.SeverityWarning)
	plain.Summary, plain.Reason = "changed body of f", "no match"
	r := Build(sel, []model.Scored{g, sh, plain}, nil)
	var txt, md bytes.Buffer
	if err := WriteText(&txt, r, false, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&md, r); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": txt.String(), "md": md.String()} {
		for _, want := range []string{invite.GenericReason, invite.ShellReason} {
			if !strings.Contains(out, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "no match") {
			t.Errorf("%s shows a plain reason:\n%s", name, out)
		}
	}
}
