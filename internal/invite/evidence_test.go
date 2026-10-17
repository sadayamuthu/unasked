package invite

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/label"
	"github.com/sadayamuthu/unasked/internal/model"
)

const realisticPrompt = "Add rate limiting to the public API: 60 requests per minute per client, answer 429 with a Retry-After header"

// Hunks for the evidence tables.
var (
	xreq = model.Hunk{ID: "h01", File: "internal/api/headers.go",
		Removed: []string{`		w.Header().Set("X-Request-Id", id)`},
		Symbols: model.SymbolDelta{Enclosing: "withRequestID"}}
	ping = model.Hunk{ID: "h01", File: "internal/server/health.go",
		Removed: []string{"func legacyPing(w http.ResponseWriter, r *http.Request) {", `	w.Write([]byte("pong"))`, "}"},
		Symbols: model.SymbolDelta{Removed: []string{"legacyPing"}}}
	flush = model.Hunk{ID: "h01", File: "internal/store/store.go",
		Removed: []string{"\tcache.Flush()"},
		Symbols: model.SymbolDelta{Enclosing: "Close"}}
	assertEdit = model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Removed: []string{`	if got = Parse(input); got != "nested \"quotes\"" {`},
		Added:   []string{`	if got = Parse(input); got == "" {`},
		Symbols: model.SymbolDelta{Enclosing: "TestParseNestedQuotes"}}
	newTest = model.Hunk{ID: "h01", File: "internal/parser/parser_test.go",
		Added: []string{"func TestParseNestedQuotes(t *testing.T) {", `	if got = Parse(input); got != "nested \"quotes\"" {`,
			`		t.Errorf("parser nested quotes: got %q", got)`, "	}", "}"},
		Symbols: model.SymbolDelta{Added: []string{"TestParseNestedQuotes"}}}
	ciRace = model.Hunk{ID: "h01", File: ".github/workflows/ci.yml",
		Removed: []string{"      - run: go test ./parser/..."}, Added: []string{"      - run: go test -race ./parser/..."}}
	zap = model.Hunk{ID: "h01", File: "go.mod",
		Removed: []string{"	go.uber.org/zap v1.26.0"}, Added: []string{"	go.uber.org/zap v1.27.0"}}
	xtime   = model.Hunk{ID: "h01", File: "go.mod", Added: []string{"	golang.org/x/time v0.5.0 // rate limit package"}}
	idConst = model.Hunk{ID: "h01", File: "internal/api/ids.go",
		Removed: []string{`const ID = "X-Request-Id"`},
		Symbols: model.SymbolDelta{Removed: []string{"ID"}}}
	closeMethod = model.Hunk{ID: "h01", File: "internal/store/store.go",
		Removed: []string{"\tClose() error"},
		Symbols: model.SymbolDelta{Removed: []string{"Close"}, Enclosing: "Store"}}
	requestsDep = model.Hunk{ID: "h01", File: "requirements.txt", Added: []string{"requests==2.31.0"}}
)

// evidenceRow is one prompt and one hunk scored end to end.
type evidenceRow struct {
	name   string
	prompt string
	h      model.Hunk
	label  model.Label
	want   model.Verdict
	capped bool // raw asked >= Invite; reason "asked: generic overlap only"
}

// checkRows scores each row alone with th. With strict, an unasked row must
// carry the generic-overlap reason exactly when it is marked capped.
func checkRows(t *testing.T, th Thresholds, rows []evidenceRow, strict bool) {
	t.Helper()
	const generic = "asked: generic overlap only"
	for _, c := range rows {
		tr := &model.Transcript{
			Prompts: []model.Prompt{{Turn: 1, Text: c.prompt}},
			Edits:   []model.EditEvent{{Turn: 2, File: c.h.File, After: strings.Join(c.h.Added, "\n"), Statement: "updating it"}},
		}
		got := Score(Input{Hunks: []model.Hunk{c.h}, Transcript: tr, Th: th,
			Classify: label.ClassifyHunk, Terms: label.Terms})[0]
		t.Logf("%-45s asked=%.3f %s %q", c.name, got.Asked, got.Verdict, got.Reason)
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
		case strict && c.want == model.VerdictUnasked && !c.capped && got.Reason == generic:
			t.Errorf("%s: reason %q but the row is not marked capped (asked %.2f)", c.name, got.Reason, got.Asked)
		}
	}
}

// TestHighRiskEvidenceRuledRows are the reviewer's rows (R36f) at the
// shipped thresholds.
func TestHighRiskEvidenceRuledRows(t *testing.T) {
	checkRows(t, Default, []evidenceRow{
		{"store drops connections", "fix the bug where the store drops connections on Close and leaks them", closeMethod, model.LabelDeletion, model.VerdictUnasked, false},
		{"Close leaves unused connections", "Close leaves unused connections open in the store, fix that leak", closeMethod, model.LabelDeletion, model.VerdictUnasked, false},
		{"x axis, drop stale samples", "log the request id on the x axis of the latency chart and drop stale samples", xreq, model.LabelDeletion, model.VerdictUnasked, false},
		{"remove the extra sleep", "the legacy health check ping is slow, remove the extra sleep before it", ping, model.LabelDeletion, model.VerdictUnasked, false},
		{"requests without install word", "rate limit incoming requests per client and log the rejected requests", requestsDep, model.LabelDependency, model.VerdictUnasked, false},
		{"remove X-Request-Id", "remove the deprecated X-Request-Id header", xreq, model.LabelDeletion, model.VerdictInvited, false},
		{"delete the legacyPing handler", "delete the legacyPing handler", ping, model.LabelDeletion, model.VerdictInvited, false},
		{"remove Close", "remove Close from the store interface", closeMethod, model.LabelDeletion, model.VerdictInvited, false},
		{"drop the legacy ping endpoint", "drop the legacy ping endpoint (legacyPing)", ping, model.LabelDeletion, model.VerdictInvited, false},
		{"add the requests library", "add the requests library for the http client", requestsDep, model.LabelDependency, model.VerdictInvited, false},
		{"bump zap", "bump zap to 1.27 in go.mod", zap, model.LabelDependency, model.VerdictInvited, false},
		{"add golang.org/x/time", "add golang.org/x/time for the limiter", xtime, model.LabelDependency, model.VerdictInvited, false},
	}, false)
}

// TestHighRiskNeedsSpecificEvidence: Test 1 invites a high-risk hunk only when
// a single prompt names it specifically (R36b–f); generic shared words are
// capped below Invite for the verdict, while Asked keeps the raw score. At
// Invite 0.30 rows marked capped have a raw asked score at or above Invite,
// so the cap itself decides them: at least one per evidence branch.
func TestHighRiskNeedsSpecificEvidence(t *testing.T) {
	checkRows(t, Thresholds{Invite: 0.30, Low: 0.15}, []evidenceRow{
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
	}, true)
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
	xr, lp, cl, id := []string{"X-Request-Id"}, []string{"legacyPing"}, []string{"Close"}, []string{"ID"}
	for _, c := range []struct {
		l      model.Label
		file   string
		prompt string
		terms  []string
		want   bool
	}{
		// deletion: a base-form verb followed within 4 words by the term (R36f)
		{model.LabelDeletion, "a.go", "remove the deprecated X-Request-Id header", xr, true},
		{model.LabelDeletion, "a.go", "remove the x request id header", xr, true},          // separators may differ
		{model.LabelDeletion, "a.go", "remove the x_request.id header", xr, true},          // _ and . are separators too
		{model.LabelDeletion, "a.go", "remove the x, request, id header", xr, false},       // "," is not
		{model.LabelDeletion, "a.go", "remove the request id header", xr, false},           // "x" is part of the name
		{model.LabelDeletion, "a.go", "X-Request-Id: remove it", xr, false},                // verb after the term
		{model.LabelDeletion, "a.go", "remove one two three four X-Request-Id", xr, false}, // 5th word after the verb
		{model.LabelDeletion, "a.go", "remove one two three X-Request-Id", xr, true},       // 4th word
		{model.LabelDeletion, "a.go", "removes the X-Request-Id header", xr, false},        // inflected
		{model.LabelDeletion, "a.go", "the X-Request-Id header is unused", xr, false},      // not a verb
		{model.LabelDeletion, "a.go", "get rid of the X-Request-Id header", xr, true},
		{model.LabelDeletion, "a.go", "drop the /v1/users route", []string{"/v1/users"}, true},
		{model.LabelDeletion, "a.go", "drop the /v1/users2 route", []string{"/v1/users"}, false},
		{model.LabelDeletion, "a.go", "delete legacyPing", lp, true},
		{model.LabelDeletion, "a.go", "delete the legacy ping handler", lp, true},
		{model.LabelDeletion, "a.go", "delete the ping handler, it is legacy", lp, false},
		{model.LabelDeletion, "a.go", "remove Close from the store interface", cl, true},
		{model.LabelDeletion, "a.go", "remove the unused ID constant", id, false}, // under 4 characters
		{model.LabelDeletion, "a.go", "remove everything", nil, false},            // no removed term
		// test and CI
		{model.LabelTest, "a_test.go", "add tests", nil, true},
		{model.LabelTest, ".github/workflows/ci.yml", "add tests", nil, false},
		{model.LabelTest, ".github/workflows/ci.yml", "fix CI", nil, true},
		// dependency: full names, one-word names near an install word (R36d, R36f)
		{model.LabelDependency, "go.mod", "use zap for logging", []string{"go.uber.org/zap"}, false},
		{model.LabelDependency, "go.mod", "use Go.Uber.Org/Zap for logging", []string{"go.uber.org/zap"}, true},
		{model.LabelDependency, "go.mod", "use go.uber.org/zapx for logging", []string{"go.uber.org/zap"}, false},
		{model.LabelDependency, "package.json", "use @types/node", []string{"@types/node"}, true},
		{model.LabelDependency, "package.json", "use node types", []string{"@types/node"}, false},
		{model.LabelDependency, "requirements.txt", "add the requests library", []string{"requests"}, true},
		{model.LabelDependency, "requirements.txt", "count requests per client and log them for the api", []string{"requests"}, false},
		{model.LabelDependency, "requirements.txt", "pin requests", []string{"requests"}, true},
		{model.LabelDependency, "requirements.txt", "install it: one two three four requests", []string{"requests"}, false}, // 5 words away
		{model.LabelDependency, "go.mod", "bump everything", nil, true},
		{model.LabelDependency, "go.mod", "vendor the module", nil, false},
	} {
		if got := evidence(c.l, c.file, c.prompt, c.terms); got != c.want {
			t.Errorf("%s %s %q %q: evidence = %v, want %v", c.l, c.file, c.prompt, c.terms, got, c.want)
		}
	}
}

func TestBoundedIndexes(t *testing.T) {
	if got := boundedIndexes("Drop /V1/users, then /v1/users2 and /v1/users", "/v1/users"); fmt.Sprint(got) != "[5 36]" {
		t.Errorf("boundedIndexes = %v, want [5 36]", got)
	}
}
