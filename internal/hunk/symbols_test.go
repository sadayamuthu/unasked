package hunk

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/model"
)

const goOld = `package p

type S struct{ n int }

func (s *S) Run() {
	s.n++
}

func Bar() {}

func Baz(a int) {}
`

const goNew = `package p

type S struct{ n int }

func (s *S) Run() {
	s.n += 2
}

func Foo() {}

func Baz(a, b int) {}
`

func TestGoSymbols(t *testing.T) {
	// One spanning hunk: lines 9-11 of old replaced by lines 9-11 of new.
	h := model.Hunk{File: "p.go", OldStart: 9, OldLines: 3, NewStart: 9, NewLines: 3}
	got := Symbols("p.go", []byte(goOld), []byte(goNew), h)
	want := model.SymbolDelta{Added: []string{"Foo"}, Removed: []string{"Bar"}, Changed: []string{"Baz"}, Enclosing: "Foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	// Body edit inside (*S).Run.
	h = model.Hunk{File: "p.go", OldStart: 6, OldLines: 1, NewStart: 6, NewLines: 1}
	got = Symbols("p.go", []byte(goOld), []byte(goNew), h)
	if got.Enclosing != "S.Run" || len(got.Added)+len(got.Removed)+len(got.Changed) != 0 {
		t.Fatalf("body edit: %+v", got)
	}
}

func TestRegexSymbolsPerLanguage(t *testing.T) {
	cases := []struct {
		path, old, new string
		want           model.SymbolDelta
	}{
		{"a.ts", "function keep() {\n  return 1\n}\n", "function keep() {\n  return 1\n}\nexport function fresh() {}\n",
			model.SymbolDelta{Added: []string{"fresh"}, Enclosing: "fresh"}},
		{"a.tsx", "const A = 1\nexport interface Props {}\n", "const A = 1\nexport interface Props {}\nexport type Id = string\n",
			model.SymbolDelta{Added: []string{"Id"}, Enclosing: "Id"}},
		{"a.js", "class K {}\n", "class K {}\nclass L {}\n", model.SymbolDelta{Added: []string{"L"}, Enclosing: "L"}},
		{"a.jsx", "export const X = 1\n", "export const X = 2\n", model.SymbolDelta{Changed: []string{"X"}, Enclosing: "X"}},
		{"a.mjs", "function a() {}\n", "function a() {}\nfunction b() {}\n", model.SymbolDelta{Added: []string{"b"}, Enclosing: "b"}},
		{"a.py", "def a():\n    pass\n", "def a():\n    pass\n\nclass B:\n    pass\n", model.SymbolDelta{Added: []string{"B"}, Enclosing: "B"}},
		{"a.rs", "fn a() {}\n", "fn a() {}\npub struct Z;\n", model.SymbolDelta{Added: []string{"Z"}, Enclosing: "Z"}},
		{"b.rs", "impl Foo {\n    fn m() {}\n}\n", "impl Foo {\n    pub fn m(x: i32) {}\n}\n", model.SymbolDelta{Changed: []string{"m"}, Enclosing: "m"}},
		{"A.java", "class A {\n  public void run() {}\n}\n", "class A {\n  public void run() {}\n  private int go(int x) { return x; }\n}\n",
			model.SymbolDelta{Added: []string{"go"}, Enclosing: "go"}},
		{"A.cs", "class A {\n  public void Run() {}\n}\n", "class A {\n}\n", model.SymbolDelta{Removed: []string{"Run"}, Enclosing: "Run"}},
		{"a.rb", "def a\nend\n", "def a\nend\n\nmodule M\nend\n", model.SymbolDelta{Added: []string{"M"}, Enclosing: "M"}},
	}
	for _, c := range cases {
		// Hunk covers the differing region: find first differing line.
		ol, nl := strings.Split(c.old, "\n"), strings.Split(c.new, "\n")
		i := 0
		for i < len(ol) && i < len(nl) && ol[i] == nl[i] {
			i++
		}
		// common suffix
		j := 0
		for j < len(ol)-i && j < len(nl)-i && ol[len(ol)-1-j] == nl[len(nl)-1-j] {
			j++
		}
		h := model.Hunk{File: c.path, OldStart: i + 1, OldLines: len(ol) - i - j, NewStart: i + 1, NewLines: len(nl) - i - j}
		if h.OldLines < 0 {
			h.OldLines = 0
		}
		got := Symbols(c.path, []byte(c.old), []byte(c.new), h)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.path, got, c.want)
		}
	}
}

func TestUnparseableGoFallsBack(t *testing.T) {
	src := "package p\n\nfunc (s *S) Run() {\n\tx := (\n\ty()\n}\n"
	h := model.Hunk{File: "p.go", OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1}
	got := Symbols("p.go", []byte(src), []byte(src), h)
	if got.Enclosing != "S.Run" {
		t.Fatalf("got %+v", got)
	}
}

func TestSymbolsNeverPanic(t *testing.T) {
	for _, p := range []string{"x.go", "x.py", "x.unknown", "x"} {
		for _, h := range []model.Hunk{
			{}, {OldStart: -3, OldLines: 99, NewStart: 1000, NewLines: 5},
		} {
			Symbols(p, nil, nil, h)
			Symbols(p, []byte("\x00\xff(((\n"), []byte("func"), h)
		}
	}
}

func TestGenericEnclosing(t *testing.T) {
	src := "int compute(int a)\n{\n    if (a) {\n        a++;\n    }\n}\n"
	h := model.Hunk{File: "x.c", NewStart: 4, NewLines: 1, OldStart: 4, OldLines: 1}
	got := Symbols("x.c", []byte(src), []byte(src), h)
	if got.Enclosing != "compute" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiffFillsSymbols(t *testing.T) {
	g := gittest.New(t)
	g.Write("p.go", goOld)
	g.Commit("init")
	r, from := snap(t, g)
	g.Write("p.go", strings.Replace(goOld, "s.n++", "s.n += 2", 1))
	_, to := snap(t, g)
	hs, _, err := Diff(r, from, to)
	if err != nil || len(hs) != 1 {
		t.Fatalf("err=%v hs=%d", err, len(hs))
	}
	if hs[0].Symbols.Enclosing != "S.Run" {
		t.Fatalf("symbols %+v", hs[0].Symbols)
	}
}
