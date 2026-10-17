package invite

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/model"
)

func seq(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func TestOverlapCoverage(t *testing.T) {
	prompt := []string{"a", "b", "c"}
	hunk := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	if got := Overlap(prompt, hunk, nil); got != 1.0 {
		t.Errorf("covered prompt: Overlap = %v, want 1.0", got)
	}
	if got := Overlap(prompt, []string{"x", "y"}, nil); got != 0 {
		t.Errorf("disjoint: Overlap = %v, want 0", got)
	}
	if got := Overlap(nil, hunk, nil); got != 0 {
		t.Errorf("empty side: Overlap = %v, want 0", got)
	}
	// Jaccard counter-example (R2): Jaccard = 4/48 ≈ 0.08; coverage = 4/12.
	p := append(seq("s", 4), seq("p", 8)...)
	h := append(seq("s", 4), seq("h", 36)...)
	if got := Overlap(p, h, nil); got < 0.33 {
		t.Errorf("R2 example: Overlap = %v, want >= 0.33", got)
	}
	// Sets, not multisets: repeating a shared token changes nothing.
	if got := Overlap([]string{"a", "a", "a", "z"}, []string{"a"}, nil); got != 1.0 {
		t.Errorf("multiset: Overlap = %v, want 1.0", got)
	}
}

func TestIDFDownweightsCommon(t *testing.T) {
	docs := [][]string{{"err", "rate"}, {"err", "limit"}, {"err", "header"}, {"err"}}
	w := IDF(docs)
	if !(w["err"] < w["rate"]) {
		t.Errorf("w(err)=%v should be < w(rate)=%v", w["err"], w["rate"])
	}
	if want := math.Log(1 + 4.0/4.0); math.Abs(w["err"]-want) > 1e-12 {
		t.Errorf("w(err)=%v, want %v", w["err"], want)
	}
	if want := math.Log(1 + 4.0/1.0); math.Abs(w["rate"]-want) > 1e-12 {
		t.Errorf("w(rate)=%v, want %v", w["rate"], want)
	}
	// df counts documents, not occurrences.
	if w2 := IDF([][]string{{"x", "x", "x"}, {"y"}}); w2["x"] != w2["y"] {
		t.Errorf("repeated token in one doc changed df: %v", w2)
	}
}

func TestThresholdsShift(t *testing.T) {
	s := Default.Shift(Step)
	if math.Abs(s.Invite-0.50) > 1e-9 || math.Abs(s.Low-0.30) > 1e-9 {
		t.Errorf("strict = %+v", s)
	}
	l := Default.Shift(-Step).Shift(-Step)
	if math.Abs(l.Invite-0.20) > 1e-9 || l.Low != 0.05 {
		t.Errorf("lenient twice = %+v, want Low floored at 0.05", l)
	}
}

const workedPrompt = "Add rate limiting to the public API: 60/min anon, 600/min keyed, 429 + Retry-After, limits configurable via env."

func TestSpecWorkedExample(t *testing.T) {
	h := model.Hunk{ID: "h01", File: "internal/http/headers.go", Removed: []string{`	w.Header().Set("X-Request-Id", id)`}}
	tr := &model.Transcript{
		Prompts: []model.Prompt{{Turn: 1, Text: workedPrompt}},
		Edits: []model.EditEvent{{Turn: 3, File: "internal/http/headers.go", After: "",
			Statement: "removing the deprecated X-Request-Id header"}},
	}
	tells, err := LoadTells("")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	got := Score(Input{
		Hunks: []model.Hunk{h}, Transcript: tr, Th: Default, Tells: tells,
		Classify: func(model.Hunk) (model.Label, bool) { calls++; return model.LabelDeletion, true },
	})
	if calls != 1 {
		t.Errorf("Classify called %d times, want 1", calls)
	}
	s := got[0]
	if s.Verdict != model.VerdictUnasked {
		t.Errorf("verdict = %s (%s), asked=%.2f said=%.2f; want unasked", s.Verdict, s.Reason, s.Asked, s.Said)
	}
	if s.Event == nil || s.Event.Turn != 3 {
		t.Errorf("event = %+v, want turn-3 edit", s.Event)
	}
	if !s.HighRisk || s.Label != model.LabelDeletion || !s.AskedKnown {
		t.Errorf("scored = %+v", s)
	}
}

func notRisky(model.Hunk) (model.Label, bool) { return model.LabelFeature, false }

func TestScoreInvitesAskedHunk(t *testing.T) {
	h := model.Hunk{ID: "h01", File: "internal/http/ratelimit.go",
		Added: []string{"func RateLimit(next http.Handler) http.Handler {", "\tlimit := envInt(\"RATE_LIMIT_ANON\", 60) // per min anon",
			"\tkeyed := envInt(\"RATE_LIMIT_KEYED\", 600) // 429 with Retry-After"}}
	other := model.Hunk{ID: "h02", File: "docs/colors.css", Added: []string{"body { color: purple; }"}}
	tr := &model.Transcript{
		Prompts: []model.Prompt{{Turn: 2, Text: workedPrompt}, {Turn: 4, Text: "[unasked report]\nrate limit api anon keyed env"}},
		Edits: []model.EditEvent{
			{Turn: 5, File: h.File, After: strings.Join(h.Added, "\n"), Statement: "adding the limiter"},
			{Turn: 6, File: other.File, After: other.Added[0], Statement: "minor color tweak"},
		},
	}
	tells, _ := LoadTells("")
	got := Score(Input{Hunks: []model.Hunk{h, other}, Transcript: tr, Th: Default, Tells: tells, Classify: notRisky})
	if got[0].Verdict != model.VerdictInvited || got[0].Reason != "asked: turn 2" || got[0].Turn != 2 {
		t.Errorf("h01 = %s %q turn %d (asked %.2f)", got[0].Verdict, got[0].Reason, got[0].Turn, got[0].Asked)
	}
	if got[1].Verdict != model.VerdictUnasked || got[1].Tell != "minor" {
		t.Errorf("h02 = %s %q tell %q", got[1].Verdict, got[1].Reason, got[1].Tell)
	}
}

func TestScorePromptFiltering(t *testing.T) {
	h := model.Hunk{ID: "h01", File: "a.go", Added: []string{"fixBug()"}}
	ev := model.EditEvent{Turn: 2, File: "a.go", After: "fixBug()"}
	for name, prompts := range map[string][]model.Prompt{
		"short":  {{Turn: 1, Text: "fix the bug"}},
		"report": {{Turn: 1, Text: "[unasked report] fix bug parser crash header tokens"}},
		"none":   nil,
	} {
		got := Score(Input{Hunks: []model.Hunk{h}, Th: Default, Classify: notRisky,
			Transcript: &model.Transcript{Prompts: prompts}})
		if got[0].AskedKnown || got[0].Verdict != model.VerdictUnclear || got[0].Reason != "no usable prompt" {
			t.Errorf("%s: %+v", name, got[0])
		}
		// With an aligned event, rule 1 no longer applies (R4): Test 1 is
		// skipped and the hunk falls through to rules 2–6.
		got = Score(Input{Hunks: []model.Hunk{h}, Th: Default, Classify: notRisky,
			Transcript: &model.Transcript{Prompts: prompts, Edits: []model.EditEvent{ev}}})
		if got[0].AskedKnown || got[0].Asked != 0 || got[0].Reason == "no usable prompt" {
			t.Errorf("%s with event: %+v", name, got[0])
		}
	}
}

func TestScoreDiffOnly(t *testing.T) {
	hs := []model.Hunk{{ID: "h01", File: "a.go", Added: []string{"x()"}}, {ID: "h02", File: "a_test.go", Added: []string{"y()"}}}
	got := Score(Input{Hunks: hs, Th: Default, DiffOnly: true, Classify: func(h model.Hunk) (model.Label, bool) {
		if h.ID == "h02" {
			return model.LabelTest, true
		}
		return model.LabelFeature, false
	}})
	if got[0].Verdict != model.VerdictUnclear || got[1].Verdict != model.VerdictUnasked {
		t.Errorf("diff-only verdicts = %s, %s", got[0].Verdict, got[1].Verdict)
	}
	for _, s := range got {
		if s.Reason != "no usable prompt" {
			t.Errorf("%s reason = %q", s.Hunk.ID, s.Reason)
		}
	}
}
