package label

import (
	"fmt"
	"testing"

	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

func hk(file string, added, removed []string, sd model.SymbolDelta) model.Hunk {
	return model.Hunk{File: file, Added: added, Removed: removed, Symbols: sd, NewStart: 10, NewLines: 3}
}

func TestClassifyTable(t *testing.T) {
	rows := []struct {
		name string
		h    model.Hunk
		want model.Label
	}{
		{"go test file", hk("pkg/a_test.go", []string{"x := 1"}, nil, model.SymbolDelta{}), model.LabelTest},
		{"workflow", hk(".github/workflows/ci.yml", []string{"run: go test"}, nil, model.SymbolDelta{}), model.LabelTest},
		{"lockfile", hk("web/yarn.lock", []string{"a"}, []string{"b"}, model.SymbolDelta{}), model.LabelDependency},
		{"requirements", hk("requirements-dev.txt", []string{"a==1"}, nil, model.SymbolDelta{}), model.LabelDependency},
		{"removed symbol", hk("a.go", nil, []string{"func Old() {}"}, model.SymbolDelta{Removed: []string{"Old"}}), model.LabelDeletion},
		{"renamed symbol", hk("a.go", []string{"func New() {}"}, []string{"func Old() {}"}, model.SymbolDelta{Added: []string{"New"}, Removed: []string{"Old"}}), model.LabelFeature},
		{"header literal", hk("a.go", nil, []string{`w.Header().Set("X-Request-Id", id)`}, model.SymbolDelta{Enclosing: "serve"}), model.LabelDeletion},
		{"route literal", hk("a.go", []string{`mux.Handle("/v2/users", h)`}, []string{`mux.Handle("/v1/users", h)`}, model.SymbolDelta{Enclosing: "routes"}), model.LabelDeletion},
		{"flag literal", hk("a.go", nil, []string{`fs.Bool("--verbose", false, "")`}, model.SymbolDelta{Enclosing: "main"}), model.LabelDeletion},
		{"env literal", hk("a.go", nil, []string{`os.Getenv("RATE_LIMIT")`}, model.SymbolDelta{Enclosing: "main"}), model.LabelDeletion},
		{"literal kept", hk("a.go", []string{`h.Set("X-Request-Id", id2)`}, []string{`h.Set("X-Request-Id", id)`}, model.SymbolDelta{Enclosing: "f"}), model.LabelRefactor},
		{"whole file deleted", model.Hunk{File: "a.go", DeletedFile: true, Removed: []string{"package a"}}, model.LabelDeletion},
		{"exported added", hk("a.go", []string{"func Hello() {}"}, nil, model.SymbolDelta{Added: []string{"Hello"}}), model.LabelFeature},
		{"unexported added only", hk("a.go", []string{"func hello() {}"}, nil, model.SymbolDelta{Added: []string{"hello"}}), model.LabelUnknown},
		{"added route", hk("a.go", []string{`mux.Handle("/v1/new", h)`}, nil, model.SymbolDelta{}), model.LabelFeature},
		{"changed", hk("a.go", []string{"x"}, []string{"y"}, model.SymbolDelta{Changed: []string{"F"}}), model.LabelRefactor},
		{"enclosing", hk("a.go", []string{"x := 2"}, []string{"x := 1"}, model.SymbolDelta{Enclosing: "F"}), model.LabelRefactor},
		{"whitespace only", hk("a.go", []string{"\tx := 1"}, []string{"    x := 1"}, model.SymbolDelta{Enclosing: "F"}), model.LabelCollateral},
		{"go import block", hk("a.go", []string{`"os"`, `"fmt"`}, []string{`"fmt"`}, model.SymbolDelta{}), model.LabelCollateral},
		{"js import", hk("a.ts", []string{`import x from "x"`}, nil, model.SymbolDelta{}), model.LabelCollateral},
		{"py import", hk("a.py", []string{`from a import b`}, nil, model.SymbolDelta{}), model.LabelCollateral},
		{"unknown", hk("a.txt", []string{"hello"}, []string{"bye"}, model.SymbolDelta{}), model.LabelUnknown},
	}
	for _, r := range rows {
		got, hr := Classify(r.h, nil)
		if got != r.want || hr != r.want.HighRisk() {
			t.Errorf("%s: got %s/%v, want %s", r.name, got, hr, r.want)
		}
	}
}

func TestMechanicalEvent(t *testing.T) {
	h := hk("a.go", []string{"x := 2"}, []string{"x := 1"}, model.SymbolDelta{Enclosing: "F"})
	if l, _ := Classify(h, &model.EditEvent{Mechanical: true}); l != model.LabelCollateral {
		t.Errorf("mechanical: got %s", l)
	}
	if l, _ := ClassifyHunk(h); l != model.LabelRefactor {
		t.Errorf("adapter: got %s", l)
	}
}

func TestPureRemovalIsDeletion(t *testing.T) {
	h := hk("a.go", nil, []string{"\tcheck(err)"}, model.SymbolDelta{})
	if l, hr := Classify(h, nil); l != model.LabelDeletion || !hr {
		t.Errorf("code removal: got %s/%v", l, hr)
	}
	for name, h := range map[string]model.Hunk{
		"blank":   hk("a.go", nil, []string{"", "  "}, model.SymbolDelta{}),
		"comment": hk("a.go", nil, []string{"// old note"}, model.SymbolDelta{}),
		"import":  hk("a.go", nil, []string{`"fmt"`}, model.SymbolDelta{}),
	} {
		if l, _ := Classify(h, nil); l == model.LabelDeletion {
			t.Errorf("%s removal must not be a deletion", name)
		}
	}
}

func TestPrecedence(t *testing.T) {
	h := hk("x/a_test.go", nil, []string{"func TestOld() {}"}, model.SymbolDelta{Removed: []string{"TestOld"}})
	if l, hr := Classify(h, nil); l != model.LabelTest || !hr {
		t.Errorf("test+deletion: got %s/%v", l, hr)
	}
	h = hk("go.mod", []string{"require a v1"}, []string{"require  a v1"}, model.SymbolDelta{})
	if l, hr := Classify(h, nil); l != model.LabelDependency || !hr {
		t.Errorf("go.mod whitespace: got %s/%v", l, hr)
	}
}

func scored(id, file string, label model.Label, v model.Verdict, reason string) model.Scored {
	// Hunks that classify as the wanted label under Apply.
	h := model.Hunk{ID: id, File: file, NewStart: 1, NewLines: 1}
	switch label {
	case model.LabelCollateral:
		h.Added, h.Removed = []string{"  x"}, []string{"x"}
	case model.LabelRefactor:
		h.Added, h.Removed = []string{"y"}, []string{"x"}
		h.Symbols = model.SymbolDelta{Enclosing: "F"}
	case model.LabelDeletion:
		h.Removed = []string{"doThing()"}
	case model.LabelUnknown:
		h.Added, h.Removed = []string{"b"}, []string{"a"}
	}
	return model.Scored{Hunk: h, Verdict: v, Reason: reason}
}

func TestCollateralFoldOnlyWithInvitedSibling(t *testing.T) {
	in := []model.Scored{
		scored("h1", "a.go", model.LabelCollateral, model.VerdictUnasked, ""),
		scored("h2", "a.go", model.LabelRefactor, model.VerdictInvited, "asked"),
		scored("h3", "b.go", model.LabelCollateral, model.VerdictUnclear, ""),
		scored("h4", "b.go", model.LabelRefactor, model.VerdictUnasked, ""),
		scored("h5", "c.go", model.LabelCollateral, model.VerdictUnasked, ""),
		scored("h6", "c.go", model.LabelCollateral, model.VerdictInvited, "collateral"),
		scored("h7", "d.go", model.LabelCollateral, model.VerdictUnasked, ""),
	}
	out := Apply(nil, in)
	if o := out[0]; o.Verdict != model.VerdictInvited || o.Reason != "collateral" || o.Severity != model.SeverityInfo {
		t.Errorf("h1 not folded: %+v", o)
	}
	if o := out[2]; o.Verdict != model.VerdictUnclear || o.Severity != model.SeverityWarning {
		t.Errorf("h3 must keep verdict: %+v", o)
	}
	if o := out[4]; o.Verdict != model.VerdictUnasked {
		t.Errorf("h5 folded by a collateral-only sibling: %+v", o)
	}
	if o := out[6]; o.Verdict != model.VerdictUnasked {
		t.Errorf("h7 folded without sibling: %+v", o)
	}
	if o := out[3]; o.Verdict != model.VerdictUnasked {
		t.Errorf("non-collateral changed verdict: %+v", o)
	}
	if in[0].Verdict != model.VerdictUnasked {
		t.Error("Apply mutated its input")
	}
}

func TestSeverityTable(t *testing.T) {
	rows := []struct {
		label model.Label
		v     model.Verdict
		want  model.Severity
	}{
		{model.LabelDeletion, model.VerdictUnasked, model.SeverityError},
		{model.LabelUnknown, model.VerdictUnasked, model.SeverityWarning},
		{model.LabelRefactor, model.VerdictUnasked, model.SeverityWarning},
		{model.LabelDeletion, model.VerdictUnclear, model.SeverityWarning},
		{model.LabelRefactor, model.VerdictUnclear, model.SeverityWarning},
		{model.LabelDeletion, model.VerdictInvited, model.SeverityNone},
		{model.LabelRefactor, model.VerdictInvited, model.SeverityNone},
	}
	for _, r := range rows {
		out := Apply(nil, []model.Scored{scored("h", "a.go", r.label, r.v, "x")})
		if out[0].Severity != r.want || out[0].Label != r.label {
			t.Errorf("%s/%s: got %s/%q, want %q", r.label, r.v, out[0].Label, out[0].Severity, r.want)
		}
	}
	for _, f := range []string{"a_test.go", "go.mod"} {
		s := scored("h", f, model.LabelUnknown, model.VerdictUnasked, "")
		if out := Apply(nil, []model.Scored{s}); out[0].Severity != model.SeverityError || !out[0].HighRisk {
			t.Errorf("%s: %+v", f, out[0])
		}
	}
}

func TestXRefsCountsOtherFiles(t *testing.T) {
	r := gittest.New(t)
	r.Write("a.go", "package a\n"+`var _ = "X-Request-Id"`+"\n")
	for i := 0; i < 3; i++ {
		r.Write(fmt.Sprintf("o%d.go", i), `h.Set("X-Request-Id", "1")`+"\n")
	}
	r.Write("none.go", "nothing\n")
	r.Git("add", ".")
	r.Commit("init")
	repo, err := gitx.Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	h := hk("a.go", nil, []string{`var _ = "X-Request-Id"`}, model.SymbolDelta{})
	out := Apply(repo, []model.Scored{{Hunk: h, Verdict: model.VerdictUnasked}})
	if out[0].XRefs != 3 {
		t.Fatalf("XRefs = %d, want 3", out[0].XRefs)
	}
	if want := "deleted header X-Request-Id still referenced in 3 files"; out[0].Summary != want {
		t.Errorf("summary = %q, want %q", out[0].Summary, want)
	}
	if got := out[0].RemovedSymbols; len(got) != 1 || got[0] != "X-Request-Id" {
		t.Errorf("RemovedSymbols = %v", got)
	}
}

func TestSummaryTemplates(t *testing.T) {
	rows := []struct {
		s    model.Scored
		want string
	}{
		{model.Scored{Label: model.LabelDeletion, Hunk: hk("a.go", nil, []string{"func Old() {}"}, model.SymbolDelta{Removed: []string{"Old"}})}, "deleted function Old"},
		{model.Scored{Label: model.LabelDeletion, Hunk: model.Hunk{File: "a.go", DeletedFile: true}}, "deleted file a.go"},
		{model.Scored{Label: model.LabelTest, Hunk: hk("a_test.go", nil, nil, model.SymbolDelta{})}, "changed test/CI file a_test.go"},
		{model.Scored{Label: model.LabelDependency, Hunk: hk("go.mod", nil, nil, model.SymbolDelta{})}, "changed dependencies in go.mod"},
		{model.Scored{Label: model.LabelFeature, Hunk: hk("a.go", nil, nil, model.SymbolDelta{Added: []string{"Hello"}})}, "added Hello"},
		{model.Scored{Label: model.LabelRefactor, Hunk: hk("a.go", nil, nil, model.SymbolDelta{Enclosing: "F"})}, "changed body of F"},
		{model.Scored{Label: model.LabelCollateral, Hunk: hk("a.go", nil, nil, model.SymbolDelta{})}, "formatting/import changes in a.go"},
		{model.Scored{Label: model.LabelUnknown, Hunk: hk("a.txt", nil, nil, model.SymbolDelta{})}, "changed a.txt:10-12"},
	}
	for _, r := range rows {
		if got := Summary(r.s); got != r.want {
			t.Errorf("got %q, want %q", got, r.want)
		}
	}
}

func TestXRefsOverCapUsesBatches(t *testing.T) {
	r := gittest.New(t)
	for i := 0; i < 2100; i++ {
		r.Write(fmt.Sprintf("f/%04d.txt", i), "x\n")
	}
	r.Write("a.go", "needle_symbol\n")
	r.Write("f/0005.txt", "needle_symbol\n")
	r.Write("f/0300.txt", "needle_symbol\n")
	r.Git("add", ".")
	r.Commit("init")
	repo, _ := gitx.Open(r.Dir)
	if n := countXRefs(repo, "a.go", []string{"needle_symbol"}); n != 2 {
		t.Errorf("got %d, want 2", n)
	}
}
