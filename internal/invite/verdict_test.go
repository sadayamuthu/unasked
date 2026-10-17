package invite

import (
	"testing"

	"github.com/sadayamuthu/unasked/internal/model"
)

func TestVerdictRules(t *testing.T) {
	ev := &model.EditEvent{Turn: 7, File: "a.go"}
	cases := []struct {
		name         string
		s            model.Scored
		diffOnly     bool
		noTranscript bool
		want         model.Verdict
		reason       string
	}{
		{"1 diff-only low risk", model.Scored{Asked: 0.9, AskedKnown: true, Event: ev}, true, true, model.VerdictUnclear, "no usable prompt"},
		{"1 diff-only high risk", model.Scored{HighRisk: true}, true, true, model.VerdictUnasked, "no usable prompt"},
		{"1 asked unknown, no event", model.Scored{}, false, false, model.VerdictUnclear, "no usable prompt"},
		{"1 asked unknown, no event, high risk", model.Scored{HighRisk: true}, false, false, model.VerdictUnasked, "no usable prompt"},
		{"2 no agent edit", model.Scored{Asked: 0.9, AskedKnown: true}, false, false, model.VerdictUnclear, "no agent edit found"},
		{"3 asked", model.Scored{Asked: 0.4, AskedKnown: true, Event: ev}, false, false, model.VerdictInvited, "asked: turn 3"},
		{"3 asked invites high risk", model.Scored{Asked: 0.5, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictInvited, "asked: turn 3"},
		{"3 tell with asked", model.Scored{Asked: 0.5, AskedKnown: true, Event: ev, Tell: "minor", Said: 0.9}, false, false, model.VerdictInvited, "asked: turn 3"},
		{"4 said", model.Scored{Asked: 0.1, Said: 0.4, AskedKnown: true, Event: ev}, false, false, model.VerdictInvited, "agent said: turn 7"},
		{"4 said, asked unknown", model.Scored{Said: 0.6, Event: ev}, false, false, model.VerdictInvited, "agent said: turn 7"},
		{"4 high risk said ignored", model.Scored{Asked: 0.1, Said: 0.9, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictUnasked, ""},
		{"5 high risk weak asked, said ignored", model.Scored{Asked: 0.25, Said: 0.9, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictUnclear, "weak match"},
		{"4 tell blocks said", model.Scored{Asked: 0.25, Said: 0.9, AskedKnown: true, Event: ev, Tell: "minor"}, false, false, model.VerdictUnclear, ""},
		{"5 low asked", model.Scored{Asked: 0.2, AskedKnown: true, Event: ev}, false, false, model.VerdictUnclear, ""},
		{"5 low said", model.Scored{Asked: 0.05, Said: 0.3, AskedKnown: true, Event: ev}, false, false, model.VerdictUnclear, ""},
		{"6 nothing", model.Scored{Asked: 0.19, Said: 0.1, AskedKnown: true, Event: ev}, false, false, model.VerdictUnasked, ""},
		{"6 asked unknown, said weak", model.Scored{Said: 0.1, Event: ev}, false, false, model.VerdictUnasked, ""},
	}
	for _, c := range cases {
		s := c.s
		decide(&s, 3, Default, c.diffOnly, !c.noTranscript)
		if s.Verdict != c.want {
			t.Errorf("%s: verdict = %s (%q), want %s", c.name, s.Verdict, s.Reason, c.want)
		}
		if c.reason != "" && s.Reason != c.reason {
			t.Errorf("%s: reason = %q, want %q", c.name, s.Reason, c.reason)
		}
		if s.Reason == "" {
			t.Errorf("%s: empty reason", c.name)
		}
	}
}

func TestForcedOneHop(t *testing.T) {
	def := model.Scored{Hunk: model.Hunk{ID: "h01", File: "a.go",
		Removed: []string{"func Foo(x int) int {"}, Added: []string{"func Bar(x int) int {"},
		Symbols: model.SymbolDelta{Removed: []string{"Foo"}, Added: []string{"Bar"}}},
		Verdict: model.VerdictInvited, Reason: "asked: turn 1"}
	caller := model.Scored{Hunk: model.Hunk{ID: "h02", File: "b.go",
		Removed: []string{"\tn := Foo(3)"}, Added: []string{"\tn := Bar(3)"}},
		Verdict: model.VerdictUnasked, Reason: "no match"}
	newCall := model.Scored{Hunk: model.Hunk{ID: "h03", File: "c.go",
		Added: []string{"\tm := Bar(4)"}}, Verdict: model.VerdictUnasked, Reason: "no match"}
	risky := caller
	risky.Hunk.ID, risky.Hunk.File, risky.HighRisk = "h04", "b_test.go", true
	extra := model.Scored{Hunk: model.Hunk{ID: "h05", File: "d.go",
		Removed: []string{"\tn := Foo(cache)"}, Added: []string{"\tn := Bar(3)"}},
		Verdict: model.VerdictUnclear, Reason: "weak match"}
	substr := model.Scored{Hunk: model.Hunk{ID: "h06", File: "e.go",
		Removed: []string{"\tFooter()"}, Added: []string{"\tBarter()"}}, Verdict: model.VerdictUnasked, Reason: "no match"}

	in := []model.Scored{def, caller, newCall, risky, extra, substr}
	got := Forced(in)
	if got[1].Verdict != model.VerdictInvited || got[1].Reason != "required by h01" {
		t.Errorf("caller = %s %q, want invited / required by h01", got[1].Verdict, got[1].Reason)
	}
	for _, i := range []int{2, 3, 4, 5} {
		if got[i].Verdict == model.VerdictInvited {
			t.Errorf("%s promoted: %q", got[i].Hunk.ID, got[i].Reason)
		}
	}
	if in[1].Verdict != model.VerdictUnasked {
		t.Error("Forced mutated its input")
	}

	// One pass: a hunk only reachable through a hunk promoted in this pass stays put.
	caller2 := caller
	caller2.Hunk.Symbols = model.SymbolDelta{Changed: []string{"Baz"}}
	chained := model.Scored{Hunk: model.Hunk{ID: "h07", File: "f.go",
		Removed: []string{"Baz()"}, Added: []string{"Baz()", ""}}, Verdict: model.VerdictUnasked, Reason: "no match"}
	got = Forced([]model.Scored{def, caller2, chained})
	if got[1].Verdict != model.VerdictInvited || got[2].Verdict == model.VerdictInvited {
		t.Errorf("one-hop violated: %s, %s", got[1].Verdict, got[2].Verdict)
	}
}

func TestForcedMethodSymbol(t *testing.T) {
	def := model.Scored{Hunk: model.Hunk{ID: "h01", Symbols: model.SymbolDelta{Changed: []string{"Server.Start"}}},
		Verdict: model.VerdictInvited}
	caller := model.Scored{Hunk: model.Hunk{ID: "h02",
		Removed: []string{"srv.Start()"}, Added: []string{"srv.Start(ctx)"}}, Verdict: model.VerdictUnasked}
	if got := Forced([]model.Scored{def, caller}); got[1].Verdict != model.VerdictInvited {
		t.Errorf("method caller not promoted: %+v", got[1])
	}
}
