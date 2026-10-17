package invite

import (
	"testing"

	"github.com/sadayamuthu/unasked/internal/model"
)

// ruleTh is a fixed pair for rule tests, independent of the calibrated Default.
var ruleTh = Thresholds{Invite: 0.40, Low: 0.20}

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
		{"1 asked unknown, event, said high", model.Scored{Said: 0.9, Event: ev}, false, false, model.VerdictUnclear, "no usable prompt"},
		{"1 asked unknown, event, high risk", model.Scored{Said: 0.9, Event: ev, HighRisk: true}, false, false, model.VerdictUnasked, "no usable prompt"},
		{"4 high risk said ignored", model.Scored{Asked: 0.1, Said: 0.9, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictUnasked, ""},
		{"5 high risk weak asked is unasked (R33, §6.4)", model.Scored{Asked: 0.25, Said: 0.9, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictUnasked, "no match"},
		{"5 high risk asked just below invite is unasked", model.Scored{Asked: 0.39, AskedKnown: true, Event: ev, HighRisk: true}, false, false, model.VerdictUnasked, "no match"},
		{"4 tell blocks said", model.Scored{Asked: 0.25, Said: 0.9, AskedKnown: true, Event: ev, Tell: "minor"}, false, false, model.VerdictUnclear, ""},
		{"5 low asked", model.Scored{Asked: 0.2, AskedKnown: true, Event: ev}, false, false, model.VerdictUnclear, ""},
		{"5 low said", model.Scored{Asked: 0.05, Said: 0.3, AskedKnown: true, Event: ev}, false, false, model.VerdictUnclear, ""},
		{"6 nothing", model.Scored{Asked: 0.19, Said: 0.1, AskedKnown: true, Event: ev}, false, false, model.VerdictUnasked, ""},
	}
	for _, c := range cases {
		s := c.s
		decide(&s, 3, ruleTh, c.diffOnly, !c.noTranscript)
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
	rideAlong := model.Scored{Hunk: model.Hunk{ID: "h08", File: "g.go",
		Removed: []string{"\tn := Foo(3)"}, Added: []string{"\tn := Bar(3)", "\tos.RemoveAll(dataDir)"}},
		Verdict: model.VerdictUnasked, Reason: "no match"}
	substr := model.Scored{Hunk: model.Hunk{ID: "h06", File: "e.go",
		Removed: []string{"\tFooter()"}, Added: []string{"\tBarter()"}}, Verdict: model.VerdictUnasked, Reason: "no match"}

	behaviour := func(id string, removed, added []string) model.Scored {
		return model.Scored{Hunk: model.Hunk{ID: id, File: id + ".go", Removed: removed, Added: added},
			Verdict: model.VerdictUnasked, Reason: "no match"}
	}
	in := []model.Scored{def, caller, newCall, risky, extra, substr, rideAlong,
		behaviour("h09", []string{"\tn := Foo(x)"}, []string{"\tn := Bar(x)", "\treturn nil"}),
		behaviour("h10", []string{"\tif !ok { Foo(x) }"}, []string{"\tif ok { Bar(x) }"}),
		behaviour("h11", []string{"Foo(1)"}, []string{"Bar(0)"}),
		behaviour("h12", []string{"Foo(true)"}, []string{"Bar(false)"}),
		behaviour("h13", []string{"Foo(x)"}, []string{"Bar(x)", "Bar(x)"}),
		behaviour("h14", []string{"Foo(a, b)"}, []string{"Bar(b, a)"}),
		behaviour("h15", []string{"  n  :=   Foo(x)", "m := Foo"}, []string{"n := Bar(x)", "m := Bar  "}),
	}
	got := Forced(in)
	if got[1].Verdict != model.VerdictInvited || got[1].Reason != "required by h01" {
		t.Errorf("caller = %s %q, want invited / required by h01", got[1].Verdict, got[1].Reason)
	}
	if got[13].Verdict != model.VerdictInvited {
		t.Errorf("whitespace-only difference blocked promotion: %+v", got[13])
	}
	for _, i := range []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12} {
		if got[i].Verdict == model.VerdictInvited {
			t.Errorf("%s promoted: %q", got[i].Hunk.ID, got[i].Reason)
		}
	}
	if in[1].Verdict != model.VerdictUnasked {
		t.Error("Forced mutated its input")
	}

	// One pass: a hunk only reachable through a hunk promoted in this pass stays put.
	caller2 := caller
	caller2.Hunk.Symbols = model.SymbolDelta{Removed: []string{"Baz"}, Added: []string{"Qux"}}
	chained := model.Scored{Hunk: model.Hunk{ID: "h07", File: "f.go",
		Removed: []string{"Baz()"}, Added: []string{"Qux()"}}, Verdict: model.VerdictUnasked, Reason: "no match"}
	got = Forced([]model.Scored{def, caller2, chained})
	if got[1].Verdict != model.VerdictInvited || got[2].Verdict == model.VerdictInvited {
		t.Errorf("one-hop violated: %s, %s", got[1].Verdict, got[2].Verdict)
	}
}

func TestForcedMethodSymbol(t *testing.T) {
	def := model.Scored{Hunk: model.Hunk{ID: "h01",
		Symbols: model.SymbolDelta{Removed: []string{"Server.Start"}, Added: []string{"Server.Run"}}},
		Verdict: model.VerdictInvited}
	caller := model.Scored{Hunk: model.Hunk{ID: "h02",
		Removed: []string{"srv.Start()"}, Added: []string{"srv.Run()"}}, Verdict: model.VerdictUnasked}
	if got := Forced([]model.Scored{def, caller}); got[1].Verdict != model.VerdictInvited {
		t.Errorf("method caller not promoted: %+v", got[1])
	}
}

func TestForcedSignatureChangeNewArgNotPromoted(t *testing.T) {
	// Tight both ways: an argument the caller gains is not a new name of the
	// symbol, so the hunk is not purely a reference update.
	def := model.Scored{Hunk: model.Hunk{ID: "h01", Symbols: model.SymbolDelta{Changed: []string{"Foo"}}},
		Verdict: model.VerdictInvited}
	caller := model.Scored{Hunk: model.Hunk{ID: "h02",
		Removed: []string{"Foo(a)"}, Added: []string{"Foo(a, ctx)"}}, Verdict: model.VerdictUnasked}
	if got := Forced([]model.Scored{def, caller}); got[1].Verdict == model.VerdictInvited {
		t.Errorf("caller gaining ctx promoted: %+v", got[1])
	}
}

// A new helper whose every symbol is called from an invited hunk's added
// lines is forced by that hunk (one hop, never high-risk, pure additions).
func TestForcedHelperDefinition(t *testing.T) {
	user := model.Scored{Hunk: model.Hunk{ID: "h01", File: "config.go",
		Added: []string{"\tc.Window = parseDuration(v, time.Minute)", "\tc.Rate = cfg.envInt(\"RATE\", 60)"}},
		Verdict: model.VerdictInvited, Reason: "asked: turn 1"}
	helper := func(id string, syms []string, removed []string) model.Scored {
		return model.Scored{Hunk: model.Hunk{ID: id, File: "config.go",
			Added:   []string{"// helper", "func x() {}"},
			Removed: removed, Symbols: model.SymbolDelta{Added: syms}},
			Event:   &model.EditEvent{Turn: 1, File: "config.go"},
			Verdict: model.VerdictUnclear, Reason: "weak match"}
	}
	noEdit := helper("h09", []string{"parseDuration"}, nil)
	noEdit.Event, noEdit.Reason = nil, "no agent edit found"
	elsewhere := helper("h10", []string{"parseDuration"}, nil)
	elsewhere.Hunk.File = "util.go"
	risky := helper("h06", []string{"parseDuration"}, nil)
	risky.HighRisk, risky.Verdict = true, model.VerdictUnasked
	in := []model.Scored{user,
		helper("h02", []string{"parseDuration"}, nil),                  // promoted
		helper("h03", []string{"Config.envInt"}, nil),                  // method: promoted by bare name
		helper("h04", []string{"parseDuration", "unused"}, nil),        // one symbol nobody calls
		helper("h05", []string{"parseDuration"}, []string{"old := 1"}), // also removes code
		risky,
		helper("h07", []string{"parseDur"}, nil), // only a substring of a call
		helper("h08", nil, nil),                  // no symbols: nothing to tie it to
		noEdit,                                   // no agent edit wrote it
		elsewhere,                                // defined in another file
	}
	got := Forced(in)
	for i, want := range []bool{true, true, true, false, false, false, false, false, false, false} {
		if (got[i].Verdict == model.VerdictInvited) != want {
			t.Errorf("%s: verdict %s %q, want invited=%v", got[i].Hunk.ID, got[i].Verdict, got[i].Reason, want)
		}
	}
	if got[1].Reason != "required by h01" {
		t.Errorf("reason %q", got[1].Reason)
	}

	// Only the invited hunk's added lines count: a symbol it mentions only
	// in removed lines does not tie a new helper to it (R35).
	removedOnly := model.Scored{Hunk: model.Hunk{ID: "h01", File: "config.go",
		Removed: []string{"\tw := parseDuration(v)"}, Added: []string{"\tw := time.Minute"}},
		Verdict: model.VerdictInvited}
	if got := Forced([]model.Scored{removedOnly, helper("h02", []string{"parseDuration"}, nil)}); got[1].Verdict == model.VerdictInvited {
		t.Errorf("helper promoted by a removed-line mention: %q", got[1].Reason)
	}

	// One hop: a helper called only from a hunk promoted in this pass stays.
	def := model.Scored{Hunk: model.Hunk{ID: "h01", File: "config.go", Removed: []string{"func Foo() {"}, Added: []string{"func Bar() {"},
		Symbols: model.SymbolDelta{Removed: []string{"Foo"}, Added: []string{"Bar"}}}, Verdict: model.VerdictInvited}
	caller := model.Scored{Hunk: model.Hunk{ID: "h02", File: "config.go", Removed: []string{"Foo(newHelper())"}, Added: []string{"Bar(newHelper())"}},
		Verdict: model.VerdictUnasked}
	h := helper("h03", []string{"newHelper"}, nil)
	got = Forced([]model.Scored{def, caller, h})
	if got[1].Verdict != model.VerdictInvited || got[2].Verdict == model.VerdictInvited {
		t.Errorf("one-hop violated: caller %s, helper %s", got[1].Verdict, got[2].Verdict)
	}
}

// replaceWord must keep the rune before each match: "FooFoo" holds no
// whole-word Foo, so it is not a Foo→Bar reference update.
func TestReplaceWordAdjacentMatches(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FooFoo", "FooFoo"},
		{"xFooFoo Foo", "xFooFoo Bar"},
		{"Foo Foo", "Bar Bar"},
		{"Foo(Foo)", "Bar(Bar)"},
		{"aFoo", "aFoo"},
	} {
		if got := replaceWord(tc.in, "Foo", "Bar"); got != tc.want {
			t.Errorf("replaceWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	def := model.Scored{Hunk: model.Hunk{ID: "h01", Symbols: model.SymbolDelta{Removed: []string{"Foo"}, Added: []string{"Bar"}}},
		Verdict: model.VerdictInvited}
	h := model.Scored{Hunk: model.Hunk{ID: "h02", Removed: []string{"FooFoo()"}, Added: []string{"FooBar()"}}, Verdict: model.VerdictUnasked}
	if got := Forced([]model.Scored{def, h}); got[1].Verdict == model.VerdictInvited {
		t.Errorf("FooFoo→FooBar treated as a reference update: %q", got[1].Reason)
	}
}
