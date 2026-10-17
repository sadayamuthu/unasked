package revert

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/hunk"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/report"
)

// fixture is a repo whose report diffs the base tree against the working tree.
type fixture struct {
	t    testing.TB
	g    *gittest.Repo
	r    *gitx.Repo
	base string
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	g := gittest.New(t)
	return &fixture{t: t, g: g}
}

// setBase commits everything and makes that commit's tree the report base.
func (f *fixture) setBase() {
	f.t.Helper()
	f.g.Commit("base")
	f.base = f.g.Git("rev-parse", "HEAD^{tree}")
	r, err := gitx.Open(f.g.Dir)
	if err != nil {
		f.t.Fatal(err)
	}
	f.r = r
}

// hunks re-diffs the base against the working tree.
func (f *fixture) hunks() ([]model.Hunk, error) {
	b, err := baseline.Snapshot(f.r)
	if err != nil {
		return nil, err
	}
	hs, _, err := hunk.Diff(f.r, f.base, b.Tree)
	return hs, err
}

// scored returns every hunk as unasked unless verdicts names it by file.
func (f *fixture) scored(verdicts map[string]model.Verdict) []model.Scored {
	f.t.Helper()
	hs, err := f.hunks()
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]model.Scored, len(hs))
	for i, h := range hs {
		v := model.VerdictUnasked
		if x, ok := verdicts[h.File]; ok {
			v = x
		}
		out[i] = model.Scored{Hunk: h, Verdict: v}
	}
	return out
}

func (f *fixture) rescore() ([]model.Scored, error) {
	hs, err := f.hunks()
	if err != nil {
		return nil, err
	}
	out := make([]model.Scored, len(hs))
	for i, h := range hs {
		out[i] = model.Scored{Hunk: h, Verdict: model.VerdictUnasked}
	}
	return out, nil
}

// state is the sha256 of every file in the work tree (".git" excluded).
func (f *fixture) state() map[string][32]byte {
	f.t.Helper()
	m := map[string][32]byte{}
	err := filepath.WalkDir(f.g.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.g.Dir, p)
		m[filepath.ToSlash(rel)] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func sameState(a, b map[string][32]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (f *fixture) indexTree() string {
	f.t.Helper()
	tr, err := baseline.IndexTree(f.r)
	if err != nil {
		f.t.Fatal(err)
	}
	return tr
}

func hashesOf(s []model.Scored, pred func(model.Scored) bool) []string {
	var out []string
	for _, x := range s {
		if pred(x) {
			out = append(out, x.Hunk.Hash)
		}
	}
	return out
}

func byFile(file string) func(model.Scored) bool {
	return func(s model.Scored) bool { return s.Hunk.File == file }
}

func hashSet(s []model.Scored) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x.Hunk.Hash] = true
	}
	return m
}

func (f *fixture) drop(cur []model.Scored, hashes []string) (string, error) {
	f.t.Helper()
	p, err := Prepare(f.r, nil, cur, hashes, true)
	if err != nil {
		return "", err
	}
	return Drop(f.r, p, f.rescore)
}

const fourLines = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"

// fourHunks makes two files with two separated hunks each.
func fourHunks(t *testing.T) *fixture {
	f := newFixture(t)
	f.g.Write("a.txt", fourLines)
	f.g.Write("b.txt", fourLines)
	f.setBase()
	f.g.Write("a.txt", "one\nTWO\nthree\nfour\nfive\nsix\nSEVEN\nseven2\neight\n")
	f.g.Write("b.txt", "one\nthree\nfour\nfive\nsix\nseven\nEIGHT\nnine\n")
	return f
}

func TestDropRemovesOnlyChosen(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	if len(cur) != 4 {
		t.Fatalf("want 4 hunks, got %d", len(cur))
	}
	idx := f.indexTree()
	// Drop the second hunk of a.txt and the first of b.txt.
	var drop []string
	var keepHashes []string
	seen := map[string]int{}
	for _, s := range cur {
		seen[s.Hunk.File]++
		if (s.Hunk.File == "a.txt" && seen["a.txt"] == 2) || (s.Hunk.File == "b.txt" && seen["b.txt"] == 1) {
			drop = append(drop, s.Hunk.Hash)
		} else {
			keepHashes = append(keepHashes, s.Hunk.Hash)
		}
	}
	id, err := f.drop(cur, drop)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty snapshot id")
	}
	if got, want := f.g.Read("a.txt"), "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n"; got != want {
		t.Fatalf("a.txt = %q, want %q", got, want)
	}
	if got, want := f.g.Read("b.txt"), "one\ntwo\nthree\nfour\nfive\nsix\nseven\nEIGHT\nnine\n"; got != want {
		t.Fatalf("b.txt = %q, want %q", got, want)
	}
	after := hashSet(f.scored(nil))
	for _, h := range drop {
		if after[h] {
			t.Fatalf("dropped %s still present", h)
		}
	}
	for _, h := range keepHashes {
		if !after[h] {
			t.Fatalf("kept %s missing", h)
		}
	}
	if f.indexTree() != idx {
		t.Fatal("index changed")
	}
}

func TestDropAllNoIDsSkipsUnclear(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(map[string]model.Verdict{"a.txt": model.VerdictUnclear})
	p, err := Prepare(f.r, nil, cur, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(p.Hunks))
	}
	for _, h := range p.Hunks {
		if h.File != "b.txt" {
			t.Fatalf("selected %s", h.File)
		}
	}
	// Selection from the last report also skips unclear.
	last := &report.Report{Transcript: "present"}
	for _, s := range cur {
		last.Hunks = append(last.Hunks, report.HunkJSON{ID: s.Hunk.ID, Hash: s.Hunk.Hash, Verdict: string(s.Verdict)})
	}
	p, err = Prepare(f.r, last, cur, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hunks) != 2 || p.Hunks[0].File != "b.txt" || p.Hunks[1].File != "b.txt" {
		t.Fatalf("bad selection %+v", p.Hunks)
	}
	a := f.g.Read("a.txt")
	if _, err := Drop(f.r, p, f.rescore); err != nil {
		t.Fatal(err)
	}
	if f.g.Read("a.txt") != a || f.g.Read("b.txt") != fourLines {
		t.Fatalf("a=%q b=%q", f.g.Read("a.txt"), f.g.Read("b.txt"))
	}
}

func TestDropRefusesStale(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	_, err := Prepare(f.r, nil, cur, []string{"0123456789ab"}, true)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	// A hash from an older report that no longer matches the tree.
	old := cur[0].Hunk.Hash
	f.g.Write("a.txt", "one\nTWO!\nthree\nfour\nfive\nsix\nSEVEN\nseven2\neight\n")
	_, err = Prepare(f.r, nil, f.scored(nil), []string{old}, true)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestDropRefusesStagedOverlap(t *testing.T) {
	f := fourHunks(t)
	f.g.Write("c.txt", "c\n")
	f.g.Git("add", "a.txt", "c.txt")
	cur := f.scored(nil)
	_, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	if !errors.Is(err, ErrStagedOverlap) {
		t.Fatalf("err = %v, want ErrStagedOverlap", err)
	}
	// Unrelated staged changes are fine.
	if _, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("b.txt")), true); err != nil {
		t.Fatal(err)
	}
}

func TestDropRefusesDependency(t *testing.T) {
	f := newFixture(t)
	f.g.Write("a.go", "package a\n\nfunc A() int { return 1 }\n")
	f.g.Write("b.go", "package a\n\nfunc B() int {\n\treturn 2\n}\n")
	f.setBase()
	f.g.Write("a.go", "package a\n\nfunc A() int { return 1 }\n\nfunc computeTotal() int { return 3 }\n")
	f.g.Write("b.go", "package a\n\nfunc B() int {\n\treturn computeTotal()\n}\n")
	cur := f.scored(map[string]model.Verdict{"b.go": model.VerdictInvited})
	var defID, useID string
	for _, s := range cur {
		if s.Hunk.File == "a.go" {
			defID = s.Hunk.ID
		} else {
			useID = s.Hunk.ID
		}
	}
	_, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.go")), true)
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("err = %v, want ErrDependency", err)
	}
	if !strings.Contains(err.Error(), useID) || !strings.Contains(err.Error(), defID) {
		t.Fatalf("message %q should name %s and %s", err, useID, defID)
	}
	// Dropping both together is fine.
	if _, err := Prepare(f.r, nil, cur, hashesOf(cur, func(model.Scored) bool { return true }), true); err != nil {
		t.Fatal(err)
	}
	// A "required by" link also blocks.
	for i := range cur {
		if cur[i].Hunk.File == "b.go" {
			cur[i].Verdict, cur[i].Reason = model.VerdictUnclear, "required by "+defID
		}
	}
	for i := range cur {
		if cur[i].Hunk.File == "a.go" {
			cur[i].Hunk.Symbols = model.SymbolDelta{}
		}
	}
	_, err = Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.go")), true)
	if !errors.Is(err, ErrDependency) || !strings.Contains(err.Error(), useID) {
		t.Fatalf("err = %v, want ErrDependency naming %s", err, useID)
	}
}

func TestDropRefusesDiffOnlyWithoutIDs(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	last := &report.Report{Transcript: "none"}
	_, err := Prepare(f.r, last, cur, nil, false)
	if err == nil || err.Error() != "nothing scored: name hunks explicitly" {
		t.Fatalf("err = %v", err)
	}
	if _, err := Prepare(f.r, last, cur, hashesOf(cur, byFile("a.txt")), true); err != nil {
		t.Fatal(err)
	}
}

func TestDropNewFileDeletesIt(t *testing.T) {
	f := newFixture(t)
	f.g.Write("a.txt", "a\n")
	f.setBase()
	f.g.Write("dir/new.txt", "fresh\nlines\n")
	cur := f.scored(nil)
	before := f.state()
	id, err := f.drop(cur, hashesOf(cur, byFile("dir/new.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.g.Dir, "dir", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file still there: %v", err)
	}
	t.Run("TestUndoRestoresIt", func(t *testing.T) {
		if err := Undo(f.r, id, false); err != nil {
			t.Fatal(err)
		}
		if !sameState(before, f.state()) {
			t.Fatal("undo did not restore the tree")
		}
	})
}

func TestUndoRestoresIt(t *testing.T) {
	f := newFixture(t)
	f.g.Write("gone.txt", "old\ncontent\n")
	f.g.Write("keep.txt", "k\n")
	f.setBase()
	f.g.Remove("gone.txt")
	f.g.Write("sub/new.txt", "n\n")
	cur := f.scored(nil)
	before := f.state()
	if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
		t.Fatal(err)
	}
	if f.g.Read("gone.txt") != "old\ncontent\n" {
		t.Fatal("deleted file not restored by drop")
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
	if _, err := os.Stat(filepath.Join(f.g.Dir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("gone.txt should be absent after undo")
	}
}

func TestDropCommittedHunkLeavesUncommittedRevert(t *testing.T) {
	f := newFixture(t)
	f.g.Write("a.txt", fourLines)
	f.setBase()
	f.g.Write("a.txt", "one\ntwo\nthree\nfour\nAGENT\nfive\nsix\nseven\neight\n")
	head := f.g.Commit("agent work")
	cur := f.scored(nil)
	if _, err := f.drop(cur, hashesOf(cur, byFile("a.txt"))); err != nil {
		t.Fatal(err)
	}
	if f.g.Git("rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
	if f.g.Read("a.txt") != fourLines {
		t.Fatalf("a.txt = %q", f.g.Read("a.txt"))
	}
	if st := f.g.Git("status", "--porcelain"); st != "M a.txt" {
		t.Fatalf("status = %q, want an unstaged revert", st)
	}
}

func TestUndoByteExact(t *testing.T) {
	f := fourHunks(t)
	f.g.WriteBytes("bin.sh", []byte("#!/bin/sh\necho hi\n"))
	if err := os.Chmod(filepath.Join(f.g.Dir, "bin.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.g.Git("add", "bin.sh")
	f.g.Git("commit", "-q", "-m", "x")
	f.g.Write("bin.sh", "#!/bin/sh\necho hi\necho more\n")
	cur := f.scored(nil)
	before := f.state()
	idx := f.indexTree()
	if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
		t.Fatal(err)
	}
	if sameState(before, f.state()) {
		t.Fatal("drop changed nothing")
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
	if f.indexTree() != idx {
		t.Fatal("index changed")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(f.g.Dir, "bin.sh"))
		if err != nil || st.Mode().Perm()&0o100 == 0 {
			t.Fatalf("executable bit lost: %v %v", st.Mode(), err)
		}
	}
}

func TestUndoRefusesChangedFile(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	if _, err := f.drop(cur, hashesOf(cur, byFile("a.txt"))); err != nil {
		t.Fatal(err)
	}
	f.g.Write("a.txt", "user edit\n")
	err := Undo(f.r, "", false)
	if !errors.Is(err, ErrChangedSinceDrop) {
		t.Fatalf("err = %v, want ErrChangedSinceDrop", err)
	}
	if !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "unasked undo --force ") {
		t.Fatalf("message %q should name a.txt and the --force command", err)
	}
	if f.g.Read("a.txt") != "user edit\n" {
		t.Fatal("refused undo touched the file")
	}
}

func TestUndoForce(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	before := f.state()
	if _, err := f.drop(cur, hashesOf(cur, byFile("a.txt"))); err != nil {
		t.Fatal(err)
	}
	f.g.Write("a.txt", "user edit\n")
	if err := Undo(f.r, "", true); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("forced undo not byte-exact")
	}
}

func TestUndoRefusesChangedIndex(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	if _, err := f.drop(cur, hashesOf(cur, byFile("a.txt"))); err != nil {
		t.Fatal(err)
	}
	f.g.Write("c.txt", "c\n")
	f.g.Git("add", "c.txt")
	if err := Undo(f.r, "", false); !errors.Is(err, ErrChangedSinceDrop) {
		t.Fatalf("err = %v, want ErrChangedSinceDrop", err)
	}
	idx := f.indexTree()
	if err := Undo(f.r, "", true); err != nil {
		t.Fatal(err)
	}
	if f.indexTree() == idx {
		t.Fatal("forced undo should restore the index")
	}
}

func TestApplyFailureRestores(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	before := f.state()
	p, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	if err != nil {
		t.Fatal(err)
	}
	p.Patch = append(append([]byte{}, p.Patch...), []byte("diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -3,0 +3 @@\n+not there\n")...)
	_, err = Drop(f.r, p, f.rescore)
	if !errors.Is(err, ErrApplyCheck) {
		t.Fatalf("err = %v, want ErrApplyCheck", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("tree changed")
	}
	if snaps, _ := ListSnapshots(f.r); len(snaps) != 0 {
		t.Fatalf("failed drop left %d snapshots", len(snaps))
	}
}

func TestVerifyFailureRestores(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	before := f.state()
	p, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	if err != nil {
		t.Fatal(err)
	}
	// A patch for a file outside the plan is refused before anything is written.
	other, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("b.txt")), true)
	if err != nil {
		t.Fatal(err)
	}
	p.Patch = other.Patch
	if _, err = Drop(f.r, p, f.rescore); !errors.Is(err, ErrApplyCheck) {
		t.Fatalf("err = %v, want ErrApplyCheck", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("tree changed")
	}
	// The patch applies cleanly but reverts only one of the two planned hunks.
	p, _ = Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	one, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt"))[:1], true)
	if err != nil {
		t.Fatal(err)
	}
	p.Patch = one.Patch
	_, err = Drop(f.r, p, f.rescore)
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v, want ErrVerify", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("tree changed")
	}
	// A rescore that disagrees also restores.
	p, _ = Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	_, err = Drop(f.r, p, func() ([]model.Scored, error) { return cur, nil })
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v, want ErrVerify", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("tree changed")
	}
}

func TestDropCRLFRoundTrip(t *testing.T) {
	crlf := "one\r\ntwo\r\nthree\r\nfour\r\nfive\r\n"
	edited := "one\r\nTWO\r\nthree\r\nfour\r\nfive\r\nsix\r\n"
	for _, tc := range []struct {
		name  string
		setup func(f *fixture)
	}{
		{"autocrlf", func(f *fixture) { f.g.Git("config", "core.autocrlf", "true") }},
		{"gitattributes", func(f *fixture) { f.g.Write(".gitattributes", "* text eol=crlf\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			f.g.WriteBytes("a.txt", []byte(crlf))
			f.g.WriteBytes("b.txt", []byte(crlf))
			f.setBase()
			f.g.WriteBytes("a.txt", []byte(edited))
			f.g.WriteBytes("b.txt", []byte(edited))
			cur := f.scored(nil)
			if len(cur) == 0 {
				t.Fatal("no hunks")
			}
			before := f.state()
			idx := f.indexTree()
			// Drop the last hunk of a.txt only.
			var drop []string
			for _, s := range cur {
				if s.Hunk.File == "a.txt" {
					drop = []string{s.Hunk.Hash}
				}
			}
			_, err := f.drop(cur, drop)
			if errors.Is(err, ErrApplyCheck) {
				if !sameState(before, f.state()) {
					t.Fatal("refused drop changed the tree")
				}
				t.Logf("refused: %v", err)
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want success or ErrApplyCheck", err)
			}
			if f.g.Read("b.txt") != edited {
				t.Fatalf("b.txt changed: %q", f.g.Read("b.txt"))
			}
			// With autocrlf the base blob is LF, so the dropped hunk may turn
			// the whole file back into the base's LF form; endings stay uniform.
			if got := f.g.Read("a.txt"); strings.Contains(got, "\r\n") && strings.Count(got, "\r\n") != strings.Count(got, "\n") {
				t.Fatalf("a.txt line endings mixed: %q", got)
			}
			if err := Undo(f.r, "", false); err != nil {
				t.Fatal(err)
			}
			if !sameState(before, f.state()) || f.indexTree() != idx {
				t.Fatal("undo not byte-exact")
			}
		})
	}
}

func TestDropOddPath(t *testing.T) {
	name := "dir with space/ünï cödé \"q\".txt"
	if runtime.GOOS == "windows" {
		name = "dir with space/ünï cödé 'q'.txt"
	}
	f := newFixture(t)
	f.g.Write(name, fourLines)
	f.g.Write("old name.txt", fourLines)
	f.setBase()
	f.g.Write(name, "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\n")
	f.g.Remove("old name.txt")
	f.g.Write("new näme.txt", "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\n")
	cur := f.scored(nil)
	var renamed bool
	for _, s := range cur {
		if s.Hunk.File == "new näme.txt" && s.Hunk.OldFile == "old name.txt" {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("expected a rename hunk, got %+v", cur)
	}
	before := f.state()
	if _, err := f.drop(cur, hashesOf(cur, func(s model.Scored) bool { return s.Hunk.FileChange == "" })); err != nil {
		t.Fatal(err)
	}
	if f.g.Read(name) != fourLines {
		t.Fatalf("odd path not reverted: %q", f.g.Read(name))
	}
	// Dropping a renamed file's content hunk reverts its content, not the
	// rename, which is a file-level hunk of its own.
	if f.g.Read("new näme.txt") != fourLines {
		t.Fatalf("renamed file not reverted: %q", f.g.Read("new näme.txt"))
	}
	if _, err := os.Stat(filepath.Join(f.g.Dir, "old name.txt")); !os.IsNotExist(err) {
		t.Fatal("rename should be left alone")
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
}

func TestSnapshotLayoutAndPrune(t *testing.T) {
	f := fourHunks(t)
	root, err := f.r.UnaskedDir("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	// 21 older snapshots, then one real drop: the oldest two are pruned.
	var old []string
	for i := 0; i < 21; i++ {
		id := time.Date(2020, 1, 1, 0, 0, i, 0, time.UTC).Format(idLayout)
		old = append(old, id)
		if err := os.Mkdir(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
		m := []byte(`{"version":1,"id":"` + id + `","index_tree":"x","hunks":[],"files":[]}`)
		if err := os.WriteFile(filepath.Join(root, id, "manifest.json"), m, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cur := f.scored(nil)
	id, err := f.drop(cur, hashesOf(cur, byFile("a.txt")))
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := ListSnapshots(f.r)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 20 {
		t.Fatalf("kept %d snapshots, want 20", len(snaps))
	}
	if snaps[0].ID != id || snaps[19].ID != old[2] {
		t.Fatalf("newest first and oldest pruned: got %s..%s", snaps[0].ID, snaps[19].ID)
	}
	if !snaps[0].Complete || len(snaps[0].Files) != 1 || snaps[0].Files[0] != "a.txt" || len(snaps[0].Hunks) != 2 {
		t.Fatalf("bad info %+v", snaps[0])
	}
	dir := filepath.Join(root, id)
	for _, p := range []string{"index.tree", "manifest.json", filepath.Join("files", "a.txt")} {
		st, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, st.Mode())
		}
	}
	if st, err := os.Stat(dir); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o700) {
		t.Fatalf("snapshot dir mode: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "index.tree")); strings.TrimSpace(string(b)) != f.indexTree() {
		t.Fatalf("index.tree = %q", b)
	}
}

func TestDropEmptyPlan(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(map[string]model.Verdict{"a.txt": model.VerdictInvited, "b.txt": model.VerdictInvited})
	p, err := Prepare(f.r, nil, cur, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := Drop(f.r, p, f.rescore)
	if err != nil || id != "" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestUndoNothing(t *testing.T) {
	f := fourHunks(t)
	if err := Undo(f.r, "", false); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("err = %v", err)
	}
}

// Under eol=crlf git rewrites the whole file on apply, which would turn
// untouched LF lines into CRLF. The drop must refuse with the tree unchanged.
func TestDropCRLFMixedEndingsRefuses(t *testing.T) {
	f := newFixture(t)
	f.g.Write(".gitattributes", "* text eol=crlf\n")
	f.g.WriteBytes("a.txt", []byte("one\r\ntwo\nthree\r\nfour\nfive\r\n"))
	f.setBase()
	f.g.WriteBytes("a.txt", []byte("one\r\ntwo\nthree\r\nfour\nfive\r\nsix\r\n"))
	cur := f.scored(nil)
	before := f.state()
	_, err := f.drop(cur, hashesOf(cur, byFile("a.txt")))
	if err == nil {
		if got := f.g.Read("a.txt"); got != "one\r\ntwo\nthree\r\nfour\nfive\r\n" {
			t.Fatalf("drop rewrote untouched lines: %q", got)
		}
		return
	}
	if !errors.Is(err, ErrApplyCheck) && !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("refused drop changed the tree")
	}
	t.Logf("refused: %v", err)
}

// When the restore itself fails, the error names the snapshot, the tree is
// left exactly as the drop made it, the after-state is recorded, and undo of
// that id brings the tree back (refusing first if the user edited since).
func TestRestoreFailureNamesSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs unix directory permissions")
	}
	f := newFixture(t)
	f.g.Write("sub/a.txt", fourLines)
	f.setBase()
	f.g.Write("sub/a.txt", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n")
	cur := f.scored(nil)
	before := f.state()
	sub := filepath.Join(f.g.Dir, "sub")
	failDrop := func() (string, map[string][32]byte) {
		t.Helper()
		p, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("sub/a.txt")), true)
		if err != nil {
			t.Fatal(err)
		}
		var dropped map[string][32]byte
		_, err = Drop(f.r, p, func() ([]model.Scored, error) {
			dropped = f.state()
			if err := os.Chmod(sub, 0o500); err != nil {
				t.Fatal(err)
			}
			return cur, nil // disagrees with the drop
		})
		os.Chmod(sub, 0o755)
		if !errors.Is(err, ErrVerify) {
			t.Fatalf("err = %v, want ErrVerify", err)
		}
		snaps, _ := ListSnapshots(f.r)
		if len(snaps) == 0 || snaps[0].Complete || !strings.Contains(err.Error(), "unasked undo "+snaps[0].ID) {
			t.Fatalf("error %q should name the incomplete snapshot (%+v)", err, snaps)
		}
		if !sameState(dropped, f.state()) {
			t.Fatal("failed restore left a partly restored tree")
		}
		return snaps[0].ID, dropped
	}

	id, _ := failDrop()
	if err := Undo(f.r, id, false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo did not restore the tree")
	}

	id, _ = failDrop()
	f.g.Write("sub/a.txt", "user edit after the failed drop\n")
	err := Undo(f.r, "", false)
	if !errors.Is(err, ErrChangedSinceDrop) || !strings.Contains(err.Error(), "undo --force "+id) {
		t.Fatalf("err = %v, want ErrChangedSinceDrop naming --force %s", err, id)
	}
	if f.g.Read("sub/a.txt") != "user edit after the failed drop\n" {
		t.Fatal("refused undo touched the file")
	}
	if err := Undo(f.r, id, true); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("forced undo did not restore the tree")
	}
}

// A drop interrupted after the snapshot (no after-state recorded) can only
// be undone with --force.
func TestUndoInterruptedDropNeedsForce(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	before := f.state()
	p, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(f.r, touchedPaths(p.Hunks), p.Hunks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.RunEnv(nil, bytes.NewReader(p.Patch), applyArgs...); err != nil {
		t.Fatal(err)
	}
	err = Undo(f.r, "", false)
	if !errors.Is(err, ErrChangedSinceDrop) || !strings.Contains(err.Error(), "undo --force "+snap.id) {
		t.Fatalf("err = %v, want ErrChangedSinceDrop naming --force %s", err, snap.id)
	}
	if err := Undo(f.r, "", true); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("forced undo not byte-exact")
	}
}

func threeFiles(t *testing.T) *fixture {
	f := newFixture(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		f.g.Write(n, fourLines)
	}
	f.setBase()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		f.g.Write(n, "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n")
	}
	return f
}

func failSecond(t *testing.T) {
	n := 0
	restoreHook = func(string) error {
		if n++; n == 2 {
			return errors.New("injected failure")
		}
		return nil
	}
	t.Cleanup(func() { restoreHook = nil })
}

// Undo whose second file fails to stage leaves all three files untouched.
func TestUndoRestoreIsAllOrNothing(t *testing.T) {
	f := threeFiles(t)
	cur := f.scored(nil)
	before := f.state()
	if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
		t.Fatal(err)
	}
	dropped := f.state()
	failSecond(t)
	if err := Undo(f.r, "", false); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("err = %v", err)
	}
	if !sameState(dropped, f.state()) {
		t.Fatal("failed undo changed the tree (or left temp files)")
	}
	restoreHook = nil
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
}

// A drop rollback whose second file fails to stage leaves the tree exactly
// as the drop made it, and names the snapshot for undo.
func TestDropRollbackIsAllOrNothing(t *testing.T) {
	f := threeFiles(t)
	cur := f.scored(nil)
	before := f.state()
	p, err := Prepare(f.r, nil, cur, hashesOf(cur, func(model.Scored) bool { return true }), true)
	if err != nil {
		t.Fatal(err)
	}
	var dropped map[string][32]byte
	_, err = Drop(f.r, p, func() ([]model.Scored, error) {
		dropped = f.state()
		failSecond(t)
		return cur, nil
	})
	if !errors.Is(err, ErrVerify) || !strings.Contains(err.Error(), "unasked undo ") {
		t.Fatalf("err = %v", err)
	}
	if !sameState(dropped, f.state()) {
		t.Fatal("failed rollback left a partly restored tree")
	}
	restoreHook = nil
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
}

func TestDropKeepsFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix permissions")
	}
	f := newFixture(t)
	f.g.Write("secret.txt", fourLines)
	f.g.Write("run.sh", fourLines)
	f.setBase()
	f.g.Write("secret.txt", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n")
	f.g.Write("run.sh", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n")
	modes := map[string]os.FileMode{"secret.txt": 0o600, "run.sh": 0o755}
	for n, m := range modes {
		if err := os.Chmod(filepath.Join(f.g.Dir, n), m); err != nil {
			t.Fatal(err)
		}
	}
	cur := f.scored(nil)
	check := func(when string) {
		t.Helper()
		for n, m := range modes {
			st, err := os.Stat(filepath.Join(f.g.Dir, n))
			if err != nil || st.Mode().Perm() != m {
				t.Fatalf("%s: %s mode %v, want %v (%v)", when, n, st.Mode().Perm(), m, err)
			}
		}
	}
	// run.sh's mode change is a file-level hunk of its own (R41); dropping
	// only the content hunks leaves every mode as it is.
	modeHunk := false
	for _, s := range cur {
		modeHunk = modeHunk || s.Hunk.File == "run.sh" && s.Hunk.FileChange == model.FileMode
	}
	if !modeHunk {
		t.Fatalf("no mode hunk for run.sh: %+v", cur)
	}
	if _, err := f.drop(cur, hashesOf(cur, func(s model.Scored) bool { return s.Hunk.FileChange == "" })); err != nil {
		t.Fatal(err)
	}
	check("after drop")
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	check("after undo")
}

func TestVerifyRejectsExtraHunk(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	before := f.state()
	p, err := Prepare(f.r, nil, cur, hashesOf(cur, byFile("a.txt")), true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Drop(f.r, p, func() ([]model.Scored, error) {
		s, err := f.rescore()
		extra := model.Scored{Hunk: model.Hunk{ID: "h99", File: "x.txt", Hash: "ffffffffffff"}}
		return append(s, extra), err
	})
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v, want ErrVerify", err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("tree changed")
	}
}

func TestRegionCheckTerminators(t *testing.T) {
	h := model.Hunk{ID: "h01", File: "f", OldStart: 2, OldLines: 1, NewStart: 2, NewLines: 1, Removed: []string{"x"}, Added: []string{"b"}}
	pre := []byte("a\nb\nc\n")
	if err := regionCheck(pre, []byte("a\nx\nc\n"), []model.Hunk{h}); err != nil {
		t.Fatal(err)
	}
	if err := regionCheck(pre, []byte("a\nx\r\nc\n"), []model.Hunk{h}); err != nil {
		t.Fatalf("eol conversion CR: %v", err)
	}
	last := h
	pre2 := []byte("a\nb\n")
	if err := regionCheck(pre2, []byte("a\nx"), []model.Hunk{last}); err == nil {
		t.Fatal("missing terminator accepted")
	}
	last.NoNewlineOld = true
	if err := regionCheck(pre2, []byte("a\nx"), []model.Hunk{last}); err != nil {
		t.Fatal(err)
	}
	if err := regionCheck(pre2, []byte("a\nx\n"), []model.Hunk{last}); err == nil {
		t.Fatal("extra terminator accepted")
	}
}

func TestLockAndIDOrder(t *testing.T) {
	f := fourHunks(t)
	cur := f.scored(nil)
	lockPath := filepath.Join(f.r.GitDir, "unasked", "revert.lock")
	if err := os.WriteFile(lockPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.drop(cur, hashesOf(cur, byFile("a.txt"))); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if err := Undo(f.r, "", false); !errors.Is(err, ErrLocked) {
		t.Fatalf("undo err = %v, want ErrLocked", err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	// Snapshots dated in the future (clock skew): the new id still sorts
	// last and survives pruning.
	root, err := f.r.UnaskedDir("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		id := time.Date(2099, 1, 1, 0, 0, i, 0, time.UTC).Format(idLayout)
		if err := os.Mkdir(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
		m := []byte(`{"version":1,"id":"` + id + `","index_tree":"x","hunks":[],"files":[],"undone_at":"2099-01-01T00:00:00Z"}`)
		if err := os.WriteFile(filepath.Join(root, id, "manifest.json"), m, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := f.drop(cur, hashesOf(cur, byFile("a.txt")))
	if err != nil {
		t.Fatal(err)
	}
	snaps, _ := ListSnapshots(f.r)
	if len(snaps) != 20 || snaps[0].ID != id {
		t.Fatalf("got %d snapshots, newest %s, want 20 with %s first", len(snaps), snaps[0].ID, id)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock not released")
	}
}

// git apply --unidiff-zero -R, reverting a pure insertion of a final line
// without a newline, also strips the newline of the line above. The patch
// carries that line on both sides so the result is exact.
func TestDropEOFNoNewlineInsertion(t *testing.T) {
	f := newFixture(t)
	f.g.Write("a.txt", "func x() {}\ndelta\n")
	f.setBase()
	f.g.Write("a.txt", "alpha\ngamma\ndelta\ndelta")
	cur := f.scored(nil)
	if len(cur) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(cur))
	}
	before := f.state()
	if _, err := f.drop(cur, []string{cur[1].Hunk.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := f.g.Read("a.txt"); got != "alpha\ngamma\ndelta\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.drop(cur, []string{cur[0].Hunk.Hash, cur[1].Hunk.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := f.g.Read("a.txt"); got != "func x() {}\ndelta\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
}

// threeFilesAndDeletion is threeFiles plus a file the agent deleted, so an
// undo of the drop has to delete the re-created file again.
func threeFilesAndDeletion(t *testing.T) *fixture {
	f := newFixture(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "gone.txt"} {
		f.g.Write(n, fourLines)
	}
	f.setBase()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		f.g.Write(n, "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\n")
	}
	if err := os.Remove(filepath.Join(f.g.Dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	return f
}

// failStep makes the nth restore step of kind ("delete" or "place") fail.
func failStep(t *testing.T, kind string, nth int) {
	n := 0
	stepHook = func(k, _ string) error {
		if k != kind {
			return nil
		}
		if n++; n == nth {
			return errors.New("injected " + kind + " failure")
		}
		return nil
	}
	t.Cleanup(func() { stepHook = nil })
}

// An undo whose second rename into place fails rolls every completed
// rename back, puts the file it was deleting back, and names the snapshot.
func TestUndoPlaceFailureRollsBack(t *testing.T) {
	for _, tc := range []struct{ kind string }{{"place"}, {"delete"}} {
		t.Run(tc.kind, func(t *testing.T) {
			f := threeFilesAndDeletion(t)
			cur := f.scored(nil)
			before := f.state()
			if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
				t.Fatal(err)
			}
			dropped := f.state()
			if _, ok := dropped["gone.txt"]; !ok {
				t.Fatal("drop did not re-create gone.txt")
			}
			list, err := ListSnapshots(f.r)
			if err != nil || len(list) == 0 {
				t.Fatalf("list %v %v", list, err)
			}
			nth := 2
			if tc.kind == "delete" {
				nth = 1
			}
			failStep(t, tc.kind, nth)
			err = Undo(f.r, "", false)
			if err == nil || !strings.Contains(err.Error(), "injected "+tc.kind+" failure") || !strings.Contains(err.Error(), list[0].ID) {
				t.Fatalf("err = %v, want the injected failure naming snapshot %s", err, list[0].ID)
			}
			if !sameState(dropped, f.state()) {
				t.Fatalf("failed undo left a partly restored tree (or temp files):\n%v\nwant\n%v", f.state(), dropped)
			}
			stepHook = nil
			if err := Undo(f.r, "", false); err != nil {
				t.Fatal(err)
			}
			if !sameState(before, f.state()) {
				t.Fatal("undo not byte-exact")
			}
		})
	}
}

// Dropping a rename together with its file's content hunks renames the file
// back with its old content; dropping the rename alone while a content hunk
// of the file stays is refused.
func TestDropRenameWithContent(t *testing.T) {
	f := newFixture(t)
	f.g.Write("old name.txt", fourLines)
	f.setBase()
	f.g.Remove("old name.txt")
	f.g.Write("new näme.txt", "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\n")
	cur := f.scored(nil)
	if _, err := Prepare(f.r, nil, cur, hashesOf(cur, func(s model.Scored) bool { return s.Hunk.FileChange != "" }), true); !errors.Is(err, ErrDependency) {
		t.Fatalf("rename alone: err = %v, want ErrDependency", err)
	}
	before := f.state()
	if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
		t.Fatal(err)
	}
	if f.g.Read("old name.txt") != fourLines {
		t.Fatalf("old name.txt after drop %q", f.g.Read("old name.txt"))
	}
	if _, err := os.Stat(filepath.Join(f.g.Dir, "new näme.txt")); !os.IsNotExist(err) {
		t.Fatal("new name still there")
	}
	if err := Undo(f.r, "", false); err != nil {
		t.Fatal(err)
	}
	if !sameState(before, f.state()) {
		t.Fatal("undo not byte-exact")
	}
}

// Empty files added or deleted are file-level hunks: dropping them deletes
// or re-creates the empty file, and undo reverses it byte-exact. (Both in
// one diff would pair up as a rename.)
func TestDropEmptyFileAddAndDelete(t *testing.T) {
	for _, kind := range []string{model.FileDelete, model.FileAdd} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.g.Write(".keep", "")
			f.g.Write("a.txt", fourLines)
			f.setBase()
			file := ".keep"
			if kind == model.FileDelete {
				f.g.Remove(".keep")
			} else {
				file = "new/empty.txt"
				f.g.Write(file, "")
			}
			cur := f.scored(nil)
			if len(cur) != 1 || cur[0].Hunk.File != file || cur[0].Hunk.FileChange != kind {
				t.Fatalf("hunks %+v", cur)
			}
			before := f.state()
			if _, err := f.drop(cur, hashesOf(cur, func(model.Scored) bool { return true })); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(f.g.Dir, filepath.FromSlash(file)))
			if kind == model.FileDelete && (err != nil || len(b) != 0) {
				t.Fatalf("%s after drop: %q %v", file, b, err)
			}
			if kind == model.FileAdd && !os.IsNotExist(err) {
				t.Fatalf("%s still there: %v", file, err)
			}
			if err := Undo(f.r, "", false); err != nil {
				t.Fatal(err)
			}
			if !sameState(before, f.state()) {
				t.Fatal("undo not byte-exact")
			}
		})
	}
}
