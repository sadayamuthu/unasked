package revert

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
)

var fuzzNames = []string{"a.txt", "src/b.go", "ünï cödé.txt", "dir with space/c.txt", "d.md"}

var fuzzWords = []string{"alpha", "beta", "gamma", "delta", "", "  indented", "func x() {}", "}", "épsilon", "alpha"}

type fuzzFile struct {
	lines  []string
	crlf   bool
	noEOL  bool
	exists bool
}

func (ff fuzzFile) bytes() []byte {
	if len(ff.lines) == 0 {
		return nil
	}
	eol := "\n"
	if ff.crlf {
		eol = "\r\n"
	}
	s := strings.Join(ff.lines, eol)
	if !ff.noEOL {
		s += eol
	}
	return []byte(s)
}

func randLines(rng *rand.Rand, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fuzzWords[rng.IntN(len(fuzzWords))]
	}
	return out
}

func mutate(rng *rand.Rand, lines []string) []string {
	out := append([]string(nil), lines...)
	for k := rng.IntN(4); k >= 0; k-- {
		pos := 0
		if len(out) > 0 {
			pos = rng.IntN(len(out) + 1)
		}
		switch rng.IntN(3) {
		case 0: // insert
			ins := randLines(rng, 1+rng.IntN(3))
			out = append(out[:pos], append(ins, out[pos:]...)...)
		case 1: // delete
			if pos < len(out) {
				n := min(1+rng.IntN(2), len(out)-pos)
				out = append(out[:pos], out[pos+n:]...)
			}
		default: // replace
			if pos < len(out) {
				out[pos] = fuzzWords[rng.IntN(len(fuzzWords))] + "!"
			}
		}
	}
	return out
}

// FuzzDropUndo builds a random small repo, makes random edits, drops a random
// subset of the hunks and checks that the drop succeeds (dropped hashes gone,
// every other hash still present, untouched files byte-identical, index
// unchanged, undo restores byte-exact). Only when the seed turns on eol
// conversion may it refuse for any check; otherwise only the hash check may
// refuse (zero-context re-diff realignment, see errRealigned). Every refusal
// must leave the tree unchanged.
func FuzzDropUndo(f *testing.F) {
	for seed := uint64(1); seed <= 16; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		fx := newFixture(t)
		// Only seeds that turn on eol conversion may see a refusal: there
		// git rewrites whole files and the drop is allowed to refuse.
		conversion := rng.IntN(5) == 0
		if conversion {
			fx.g.Write(".gitattributes", "* text eol=crlf\n")
		}
		nFiles := 1 + rng.IntN(len(fuzzNames))
		files := make([]fuzzFile, nFiles)
		for i := range files {
			files[i] = fuzzFile{lines: randLines(rng, 1+rng.IntN(10)), crlf: rng.IntN(4) == 0, noEOL: rng.IntN(4) == 0, exists: rng.IntN(5) != 0}
			if files[i].exists {
				fx.g.WriteBytes(fuzzNames[i], files[i].bytes())
			}
		}
		fx.g.Write(".keep", "x\n")
		fx.setBase()
		for i := range files {
			ff := &files[i]
			switch {
			case !ff.exists:
				if rng.IntN(2) == 0 {
					ff.exists, ff.lines = true, randLines(rng, 1+rng.IntN(5))
					fx.g.WriteBytes(fuzzNames[i], ff.bytes())
				}
			case rng.IntN(6) == 0:
				fx.g.Remove(fuzzNames[i])
			case rng.IntN(5) != 0:
				ff.lines = mutate(rng, ff.lines)
				if rng.IntN(5) == 0 {
					ff.noEOL = !ff.noEOL
				}
				fx.g.WriteBytes(fuzzNames[i], ff.bytes())
			}
		}
		cur := fx.scored(nil)
		if len(cur) == 0 {
			return
		}
		var drop []string
		for _, s := range cur {
			if rng.IntN(2) == 0 {
				drop = append(drop, s.Hunk.Hash)
			}
		}
		if len(drop) == 0 {
			drop = []string{cur[rng.IntN(len(cur))].Hunk.Hash}
		}
		before := fx.state()
		idx := fx.indexTree()
		id, err := fx.drop(cur, drop)
		if err != nil {
			// Without conversion the only acceptable refusal is a re-diff
			// realignment, which is reported only after the byte-level
			// region check has passed.
			ok := errors.Is(err, errRealigned)
			if conversion {
				ok = errors.Is(err, ErrApplyCheck) || errors.Is(err, ErrVerify)
			}
			if !ok {
				t.Fatalf("unexpected error (conversion=%v): %v", conversion, err)
			}
			if !sameState(before, fx.state()) {
				t.Fatalf("refused drop changed the tree: %v", err)
			}
			t.Logf("refused: %v", err)
			return
		}
		dropped := map[string]bool{}
		touched := map[string]bool{}
		for _, h := range drop {
			dropped[h] = true
		}
		for _, s := range cur {
			if dropped[s.Hunk.Hash] {
				touched[s.Hunk.File] = true
			}
		}
		after := hashSet(fx.scored(nil))
		for _, s := range cur {
			if dropped[s.Hunk.Hash] == after[s.Hunk.Hash] {
				t.Fatalf("hunk %s (%s) dropped=%v present=%v", s.Hunk.Hash, s.Hunk.File, dropped[s.Hunk.Hash], after[s.Hunk.Hash])
			}
		}
		now := fx.state()
		for p, sum := range before {
			if !touched[p] && now[p] != sum {
				t.Fatalf("untouched file %s changed", p)
			}
		}
		if fx.indexTree() != idx {
			t.Fatal("index changed")
		}
		if err := Undo(fx.r, id, false); err != nil {
			t.Fatal(err)
		}
		if !sameState(before, fx.state()) {
			t.Fatal("undo not byte-exact")
		}
	})
}
