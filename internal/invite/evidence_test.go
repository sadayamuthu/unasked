package invite

import (
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/label"
	"github.com/sadayamuthu/unasked/internal/model"
)

const realisticPrompt = "Add rate limiting to the public API: 60 requests per minute per client, answer 429 with a Retry-After header"

// TestHighRiskNeedsSpecificEvidence: Test 1 invites a high-risk hunk only when
// a single prompt names it specifically (R36b, R36c); generic shared words
// are capped below Invite for the verdict, while Asked keeps the raw score.
// Rows marked capped have a raw asked score at or above Invite, so the cap
// itself decides them.
func TestHighRiskNeedsSpecificEvidence(t *testing.T) {
	xreq := model.Hunk{ID: "h01", File: "internal/api/headers.go",
		Removed: []string{`		w.Header().Set("X-Request-Id", id)`},
		Symbols: model.SymbolDelta{Enclosing: "withRequestID"}}
	ping := model.Hunk{ID: "h01", File: "internal/server/health.go",
		Removed: []string{"func legacyPing(w http.ResponseWriter, r *http.Request) {", `	w.Write([]byte("pong"))`, "}"},
		Symbols: model.SymbolDelta{Removed: []string{"legacyPing"}}}
	flush := model.Hunk{ID: "h01", File: "internal/store/store.go",
		Removed: []string{"\tcache.Flush()"},
		Symbols: model.SymbolDelta{Enclosing: "Close"}}
	assertEdit := model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Removed: []string{`	if got := Parse(input); got != "nested \"quotes\"" {`},
		Added:   []string{`	if got := Parse(input); got == "" {`},
		Symbols: model.SymbolDelta{Enclosing: "TestParseNestedQuotes"}}
	newTest := model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Added: []string{"func TestParseNestedQuotes(t *testing.T) {", `	if got := Parse(input); got != "nested \"quotes\"" {`,
			`		t.Errorf("parser nested quotes: got %q", got)`, "	}", "}"},
		Symbols: model.SymbolDelta{Added: []string{"TestParseNestedQuotes"}}}
	ciRace := model.Hunk{ID: "h01", File: ".github/workflows/ci.yml",
		Removed: []string{"      - run: go test ./parser/..."}, Added: []string{"      - run: go test -race ./parser/..."}}
	zap := model.Hunk{ID: "h01", File: "go.mod",
		Removed: []string{"	go.uber.org/zap v1.26.0"}, Added: []string{"	go.uber.org/zap v1.27.0"}}
	xtime := model.Hunk{ID: "h01", File: "go.mod", Added: []string{"	golang.org/x/time v0.5.0 // rate limit package"}}
	idConst := model.Hunk{ID: "h01", File: "internal/api/ids.go",
		Removed: []string{`const ID = "X-Request-Id"`},
		Symbols: model.SymbolDelta{Removed: []string{"ID"}}}
	closeMethod := model.Hunk{ID: "h01", File: "internal/store/store.go",
		Removed: []string{"\tClose() error"},
		Symbols: model.SymbolDelta{Removed: []string{"Close"}, Enclosing: "Store"}}

	th := Thresholds{Invite: 0.30, Low: 0.15}
	const generic = "asked: generic overlap only"
	cases := []struct {
		name   string
		prompt string
		h      model.Hunk
		label  model.Label
		want   model.Verdict
		capped bool // raw asked >= Invite; reason "asked: generic overlap only"
	}{
		// deletion (R36b)
		{"realistic prompt, X-Request-Id deletion", realisticPrompt, xreq, model.LabelDeletion, model.VerdictUnasked, true},
		{"log the request id", "Log the request id of every rate limited request with the client address", xreq, model.LabelDeletion, model.VerdictUnasked, false},
		{"request id in error body", "Add the request id to the error response body for support tickets", xreq, model.LabelDeletion, model.VerdictUnasked, false},
		{"named removal of X-Request-Id", "remove the deprecated X-Request-Id header", xreq, model.LabelDeletion, model.VerdictInvited, false},
		{"delete legacyPing", "delete legacyPing from the health server", ping, model.LabelDeletion, model.VerdictInvited, false},
		{"delete the legacyPing handler", "delete the legacyPing handler", ping, model.LabelDeletion, model.VerdictInvited, false},
		{"one-word name: remove Close", "remove Close from the store interface", closeMethod, model.LabelDeletion, model.VerdictInvited, false},
		{"verb and words apart, no x", "the client drops the request id header when unused, log it", idConst, model.LabelDeletion, model.VerdictUnasked, true},
		{"one-word name under 4 chars", "remove the unused ID constant", idConst, model.LabelDeletion, model.VerdictUnasked, false},
		{"connection leak, cache.Flush removal", "Fix the connection leak in store Close when the cache is full", flush, model.LabelDeletion, model.VerdictUnasked, true},
		{"Close returns error, cache.Flush removal", "Make Close return the error from closing the cache store", flush, model.LabelDeletion, model.VerdictUnasked, true},
		// test (R36c)
		{"bug fix, test assertion edit", "fix the parser bug with nested quotes", assertEdit, model.LabelTest, model.VerdictUnasked, true},
		{"add tests, test edit", "add tests for the parser nested quotes", newTest, model.LabelTest, model.VerdictInvited, false},
		{"test word, CI file", "run the parser tests with the race detector", ciRace, model.LabelTest, model.VerdictUnasked, true},
		{"CI word, CI file", "add the race detector to the parser CI workflow", ciRace, model.LabelTest, model.VerdictInvited, false},
		// dependency (R36c)
		{"bump zap, go.mod change", "bump zap to 1.27 in go.mod", zap, model.LabelDependency, model.VerdictInvited, false},
		{"rate-limit prompt, new dependency", realisticPrompt, xtime, model.LabelDependency, model.VerdictUnasked, false},
		{"rate limit package, last segment only", "add a rate limit package to the api server, reset each time window", xtime, model.LabelDependency, model.VerdictUnasked, true},
		{"log the time, last segment only", "Add rate limiting to the api server and log the time of each rejected request", xtime, model.LabelDependency, model.VerdictUnasked, false},
		{"full module path", "add golang.org/x/time for the limiter", xtime, model.LabelDependency, model.VerdictInvited, false},
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
		if got.Verdict != c.want {
			t.Errorf("%s: verdict = %s (%q), asked %.2f; want %s", c.name, got.Verdict, got.Reason, got.Asked, c.want)
		}
		switch {
		case c.want == model.VerdictInvited && got.Reason != "asked: turn 1":
			t.Errorf("%s: reason = %q, want %q", c.name, got.Reason, "asked: turn 1")
		case c.capped && (got.Asked < th.Invite || got.Reason != generic):
			t.Errorf("%s: asked %.2f reason %q; want raw asked >= %.2f (the cap decides) and %q", c.name, got.Asked, got.Reason, th.Invite, generic)
		case c.want == model.VerdictUnasked && !c.capped && got.Reason == generic:
			t.Errorf("%s: reason %q but the row is not marked capped (asked %.2f)", c.name, got.Reason, got.Asked)
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
		{removalVerbs, "rip out the ping route", true},
		{removalVerbs, "eliminate the ping route", true},
		{removalVerbs, "the ping route is unused", true},
		{removalVerbs, "get the ping route rid", false},
		{removalVerbs, "add rate limiting to the api", false},
		{testWords, "the assertions are wrong", true},
		{testWords, "more testing please", true},
		{testWords, "fix the flaky one", true},
		{testWords, "the latest parser", false},
		{testWords, "CI is red", false},
		{ciWords, "CI is red", true},
		{ciWords, "fix the GitHub Actions job", true},
		{ciWords, "the release pipelines", true},
		{ciWords, "add more tests", false},
		{depWords, "upgrading the http client", true},
		{depWords, "downgrade it", true},
		{depWords, "vendor the module", false},
		{depWords, "add a rate limit package", false},
		{depWords, "install the library at version 2", false},
	} {
		if got := c.ks.match(words(c.text)); got != c.want {
			t.Errorf("%q: match = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestEvidence(t *testing.T) {
	for _, c := range []struct {
		l      model.Label
		file   string
		prompt string
		terms  []string
		want   bool
	}{
		{model.LabelDeletion, "a.go", "remove the deprecated X-Request-Id header", []string{"X-Request-Id"}, true},
		{model.LabelDeletion, "a.go", "remove the request id header", []string{"X-Request-Id"}, false},  // "x" is part of the name
		{model.LabelDeletion, "a.go", "X-Request-Id is fine, keep it", []string{"X-Request-Id"}, false}, // no removal verb
		{model.LabelDeletion, "a.go", "drop the /v1/users route", []string{"/v1/users"}, true},
		{model.LabelDeletion, "a.go", "delete legacyPing", []string{"legacyPing"}, true},
		{model.LabelDeletion, "a.go", "delete the legacy ping handler", []string{"legacyPing"}, true},
		{model.LabelDeletion, "a.go", "delete the ping handler", []string{"legacyPing"}, false},
		{model.LabelDeletion, "a.go", "remove Close from the store interface", []string{"Close"}, true},
		{model.LabelDeletion, "a.go", "remove the unused ID constant", []string{"ID"}, false},                                          // under 4 characters
		{model.LabelDeletion, "a.go", "Close the pool, then much later on in the code remove stale entries", []string{"Close"}, false}, // verb 9 words away
		{model.LabelDeletion, "a.go", "Close it: one two three, remove", []string{"Close"}, true},                                      // verb 5 words away
		{model.LabelDeletion, "a.go", "remove everything from Close", nil, false},                                                      // no removed term
		{model.LabelTest, "a_test.go", "add tests", nil, true},
		{model.LabelTest, ".github/workflows/ci.yml", "add tests", nil, false},
		{model.LabelTest, ".github/workflows/ci.yml", "fix CI", nil, true},
		{model.LabelDependency, "go.mod", "use zap for logging", []string{"go.uber.org/zap"}, false},
		{model.LabelDependency, "go.mod", "use go.uber.org/zap for logging", []string{"go.uber.org/zap"}, true},
		{model.LabelDependency, "go.mod", "use Go.Uber.Org/Zap for logging", []string{"go.uber.org/zap"}, true},
		{model.LabelDependency, "go.mod", "use go.uber.org/zapx for logging", []string{"go.uber.org/zap"}, false},
		{model.LabelDependency, "package.json", "add @types/node", []string{"@types/node"}, true},
		{model.LabelDependency, "package.json", "add node types", []string{"@types/node"}, false},
		{model.LabelDependency, "go.mod", "add a logging package", []string{"go.uber.org/zap"}, false},
		{model.LabelDependency, "go.mod", "bump everything", nil, true},
	} {
		if got := evidence(c.l, c.file, c.prompt, c.terms); got != c.want {
			t.Errorf("%s %s %q %q: evidence = %v, want %v", c.l, c.file, c.prompt, c.terms, got, c.want)
		}
	}
	if !containsBounded("drop /v1/users now", "/v1/users") || containsBounded("drop /v1/users2", "/v1/users") {
		t.Error("containsBounded")
	}
}
