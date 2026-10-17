package invite

import (
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/label"
	"github.com/sadayamuthu/unasked/internal/model"
)

const realisticPrompt = "Add rate limiting to the public API: 60 requests per minute per client, answer 429 with a Retry-After header"

// TestHighRiskNeedsSpecificEvidence: Test 1 invites a high-risk hunk only when
// some single prompt names it specifically (R36); generic shared words are
// capped below Invite for the verdict, while Asked keeps the raw score.
func TestHighRiskNeedsSpecificEvidence(t *testing.T) {
	xreq := model.Hunk{ID: "h01", File: "internal/api/headers.go",
		Removed: []string{`		w.Header().Set("X-Request-Id", id)`},
		Symbols: model.SymbolDelta{Enclosing: "withRequestID"}}
	ping := model.Hunk{ID: "h01", File: "internal/server/health.go",
		Removed: []string{"func legacyPing(w http.ResponseWriter, r *http.Request) {", `	w.Write([]byte("pong"))`, "}"},
		Symbols: model.SymbolDelta{Removed: []string{"legacyPing"}}}
	assertEdit := model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Removed: []string{`	if got := Parse(input); got != "nested \"quotes\"" {`},
		Added:   []string{`	if got := Parse(input); got == "" {`},
		Symbols: model.SymbolDelta{Enclosing: "TestParseNestedQuotes"}}
	newTest := model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Added: []string{"func TestParseNestedQuotes(t *testing.T) {", `	if got := Parse(input); got != "nested \"quotes\"" {`,
			`		t.Errorf("parser nested quotes: got %q", got)`, "	}", "}"},
		Symbols: model.SymbolDelta{Added: []string{"TestParseNestedQuotes"}}}
	zap := model.Hunk{ID: "h01", File: "go.mod",
		Removed: []string{"	go.uber.org/zap v1.26.0"}, Added: []string{"	go.uber.org/zap v1.27.0"}}
	limiterDep := model.Hunk{ID: "h01", File: "go.mod",
		Added: []string{"	github.com/ulule/limiter/v3 v3.11.2 // rate limit middleware"}}

	th := Default
	cases := []struct {
		name   string
		prompt string
		h      model.Hunk
		label  model.Label
		want   model.Verdict
		reason string
	}{
		{"realistic prompt, X-Request-Id deletion", realisticPrompt, xreq, model.LabelDeletion, model.VerdictUnasked, "asked: generic overlap only"},
		{"named removal of X-Request-Id", "remove the deprecated X-Request-Id header", xreq, model.LabelDeletion, model.VerdictInvited, "asked: turn 1"},
		{"delete legacyPing", "delete legacyPing from the health server", ping, model.LabelDeletion, model.VerdictInvited, "asked: turn 1"},
		{"bug fix, test assertion edit", "fix the parser bug with nested quotes", assertEdit, model.LabelTest, model.VerdictUnasked, ""},
		{"add tests, test edit", "add tests for the parser nested quotes", newTest, model.LabelTest, model.VerdictInvited, "asked: turn 1"},
		{"bump zap, go.mod change", "bump zap to 1.27 in go.mod", zap, model.LabelDependency, model.VerdictInvited, "asked: turn 1"},
		{"rate-limit prompt, go.mod change", realisticPrompt, limiterDep, model.LabelDependency, model.VerdictUnasked, ""},
	}
	for _, c := range cases {
		tr := &model.Transcript{
			Prompts: []model.Prompt{{Turn: 1, Text: c.prompt}},
			Edits:   []model.EditEvent{{Turn: 2, File: c.h.File, After: strings.Join(c.h.Added, "\n"), Statement: "updating it"}},
		}
		got := Score(Input{Hunks: []model.Hunk{c.h}, Transcript: tr, Th: th,
			Classify: label.ClassifyHunk, Terms: label.Terms})[0]
		if got.Label != c.label || !got.HighRisk {
			t.Errorf("%s: label = %s (high-risk %v), want %s", c.name, got.Label, got.HighRisk, c.label)
		}
		if got.Verdict != c.want || (c.reason != "" && got.Reason != c.reason) {
			t.Errorf("%s: verdict = %s (%q), asked %.2f; want %s (%q)", c.name, got.Verdict, got.Reason, got.Asked, c.want, c.reason)
		}
		if c.reason == "asked: generic overlap only" && got.Asked < th.Invite {
			t.Errorf("%s: Asked = %.2f, want the raw score >= Invite %.2f (the cap is for the verdict only)", c.name, got.Asked, th.Invite)
		}
	}
}

// TestGenericOverlapReasonOnlyWhenCapDecides: a high-risk hunk whose raw
// asked score is below Invite keeps the ordinary "no match" reason.
func TestGenericOverlapReasonOnlyWhenCapDecides(t *testing.T) {
	ev := &model.EditEvent{Turn: 7, File: "a.go"}
	s := model.Scored{Asked: 0.30, AskedKnown: true, Event: ev, HighRisk: true}
	decide(&s, 0.30, 3, ruleTh, false, true)
	if s.Verdict != model.VerdictUnasked || s.Reason != "no match" {
		t.Errorf("below invite: %s %q", s.Verdict, s.Reason)
	}
	s = model.Scored{Asked: 0.90, AskedKnown: true, Event: ev, HighRisk: true}
	decide(&s, ruleTh.Invite-0.01, 3, ruleTh, false, true)
	if s.Verdict != model.VerdictUnasked || s.Reason != "asked: generic overlap only" || s.Asked != 0.90 {
		t.Errorf("capped: %s %q asked %.2f", s.Verdict, s.Reason, s.Asked)
	}
}

func TestKeywordsMatch(t *testing.T) {
	for _, c := range []struct {
		ks   keywords
		text string
		want bool
	}{
		{removalVerbs, "please drop the old handler", true},
		{removalVerbs, "Removing the ping route", true},
		{removalVerbs, "the header is deprecated now", true},
		{removalVerbs, "get rid of the ping route", true},
		{removalVerbs, "get the ping route rid", false},
		{removalVerbs, "add rate limiting to the api", false},
		{testWords, "the assertions are wrong", true},
		{testWords, "CI is red", true},
		{testWords, "fix the flaky one", true},
		{testWords, "the latest parser", false},
		{depWords, "upgrading the http library", true},
		{depWords, "add rate limiting", false},
	} {
		if got := c.ks.match(words(c.text)); got != c.want {
			t.Errorf("%q: match = %v, want %v", c.text, got, c.want)
		}
	}
}

// A removal verb plus one removed-name token rarer than the hunk's median
// token is evidence; a common token is not.
func TestDeletionVerbAndRareToken(t *testing.T) {
	hunkToks := []string{"legaci", "ping", "http", "respons", "writer", "request"}
	w := map[string]float64{"legaci": 2, "ping": 1.5, "http": 0.2, "respons": 0.3, "writer": 0.3, "request": 0.4}
	terms := []string{"legacyPing"}
	for _, c := range []struct {
		prompt string
		want   bool
	}{
		{"remove the old ping handler from the server", true},
		{"the old ping handler from the server is slow", false},
		{"remove the http request writer from the server", false},
	} {
		if got := evidence(model.LabelDeletion, c.prompt, Tokens(c.prompt), terms, hunkToks, w); got != c.want {
			t.Errorf("%q: evidence = %v, want %v", c.prompt, got, c.want)
		}
	}
	w["ping"] = 0.3 // at or below the median (0.35): too common to count
	if evidence(model.LabelDeletion, "remove the old ping handler", Tokens("remove the old ping handler"), terms, hunkToks, w) {
		t.Error("common token counted as evidence")
	}
}
