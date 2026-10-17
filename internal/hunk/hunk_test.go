package hunk

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

const modAddDel = `diff --git a/f.go b/f.go
index 1111111..2222222 100644
--- a/f.go
+++ b/f.go
@@ -2 +2 @@ func x
-old
+new
@@ -5,0 +6,2 @@
+add1
+add2
@@ -9,2 +10,0 @@
-del1
-del2
`

func TestParseModifyAddDelete(t *testing.T) {
	hs, bin, err := Parse([]byte(modAddDel))
	if err != nil || bin != 0 {
		t.Fatalf("err=%v bin=%d", err, bin)
	}
	want := []struct {
		os, ol, ns, nl int
		added, removed []string
	}{
		{2, 1, 2, 1, []string{"new"}, []string{"old"}},
		{5, 0, 6, 2, []string{"add1", "add2"}, nil},
		{9, 2, 10, 0, nil, []string{"del1", "del2"}},
	}
	if len(hs) != len(want) {
		t.Fatalf("got %d hunks", len(hs))
	}
	for i, w := range want {
		h := hs[i]
		if h.File != "f.go" || h.OldStart != w.os || h.OldLines != w.ol || h.NewStart != w.ns || h.NewLines != w.nl {
			t.Errorf("hunk %d: %+v", i, h)
		}
		if !reflect.DeepEqual(h.Added, w.added) || !reflect.DeepEqual(h.Removed, w.removed) {
			t.Errorf("hunk %d text: +%q -%q", i, h.Added, h.Removed)
		}
		if h.Hash != HashOf("f.go", w.removed, w.added) {
			t.Errorf("hunk %d hash mismatch", i)
		}
	}
}

func TestParseNewAndDeletedFile(t *testing.T) {
	d := `diff --git a/n.txt b/n.txt
new file mode 100644
index 0000000..e69de29
--- /dev/null
+++ b/n.txt
@@ -0,0 +1,2 @@
+a
+b
diff --git a/d.txt b/d.txt
deleted file mode 100644
index e69de29..0000000
--- a/d.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
`
	hs, _, err := Parse([]byte(d))
	if err != nil || len(hs) != 2 {
		t.Fatalf("err=%v n=%d", err, len(hs))
	}
	if !hs[0].NewFile || hs[0].DeletedFile || hs[0].File != "n.txt" {
		t.Errorf("new: %+v", hs[0])
	}
	if !hs[1].DeletedFile || hs[1].NewFile || hs[1].File != "d.txt" {
		t.Errorf("deleted: %+v", hs[1])
	}
	if !bytes.Contains(hs[0].Patch, []byte("new file mode 100644\n")) || !bytes.Contains(hs[1].Patch, []byte("deleted file mode")) {
		t.Errorf("patch headers missing")
	}
}

func TestParseRename(t *testing.T) {
	d := `diff --git a/old.go b/new.go
similarity index 90%
rename from old.go
rename to new.go
index 1..2 100644
--- a/old.go
+++ b/new.go
@@ -1 +1 @@
-a
+b
diff --git a/p.go b/q.go
similarity index 100%
rename from p.go
rename to q.go
`
	hs, bin, err := Parse([]byte(d))
	if err != nil || bin != 0 || len(hs) != 1 {
		t.Fatalf("err=%v bin=%d n=%d", err, bin, len(hs))
	}
	if hs[0].OldFile != "old.go" || hs[0].File != "new.go" {
		t.Errorf("%+v", hs[0])
	}
}

func TestParseNoNewlineAtEOF(t *testing.T) {
	d := `diff --git a/f b/f
--- a/f
+++ b/f
@@ -1 +1 @@
-a
\ No newline at end of file
+b
\ No newline at end of file
`
	hs, _, err := Parse([]byte(d))
	if err != nil || len(hs) != 1 {
		t.Fatalf("err=%v", err)
	}
	h := hs[0]
	if !h.NoNewlineOld || !h.NoNewlineNew {
		t.Errorf("flags %v %v", h.NoNewlineOld, h.NoNewlineNew)
	}
	if !reflect.DeepEqual(h.Removed, []string{"a"}) || !reflect.DeepEqual(h.Added, []string{"b"}) {
		t.Errorf("text %q %q", h.Removed, h.Added)
	}
	if !bytes.HasSuffix(h.Patch, []byte("+b\n\\ No newline at end of file\n")) {
		t.Errorf("patch lost marker: %q", h.Patch)
	}
}

func TestParseBinarySkipped(t *testing.T) {
	d := `diff --git a/b.bin b/b.bin
new file mode 100644
index 0000000..abc
GIT binary patch
literal 4
LcmZQzU|?uq0RR9~

literal 0
HcmV?d00001

diff --git a/t.txt b/t.txt
--- a/t.txt
+++ b/t.txt
@@ -1 +1 @@
-a
+b
`
	hs, bin, err := Parse([]byte(d))
	if err != nil || bin != 1 || len(hs) != 1 || hs[0].File != "t.txt" {
		t.Fatalf("err=%v bin=%d hs=%+v", err, bin, hs)
	}
}

func snap(t *testing.T, g *gittest.Repo) (*gitx.Repo, string) {
	t.Helper()
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := baseline.Snapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, b.Tree
}

func TestParseOddPaths(t *testing.T) {
	g := gittest.New(t)
	odd := "dir with space/naïve \"q\".go"
	g.Write("plain.go", "package a\n\nfunc A() {}\n\nfunc B() {}\n")
	g.Write("mv me.go", "one\ntwo\nthree\nfour\nfive\n")
	g.Commit("init")
	r, from := snap(t, g)
	g.Write(odd, "package a\n")
	g.Write("plain.go", "package a\n\nfunc A() { x() }\n\nfunc B() {}\n")
	g.Remove("mv me.go")
	g.Write("dir with space/renamed é.go", "one\ntwo\nTHREE\nfour\nfive\n")
	_, to := snap(t, g)
	hs, _, err := Diff(r, from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range hs {
		got[h.File] = true
		if h.File == "dir with space/renamed é.go" && h.OldFile != "mv me.go" {
			t.Errorf("OldFile = %q", h.OldFile)
		}
	}
	for _, p := range []string{odd, "plain.go", "dir with space/renamed é.go"} {
		if !got[p] {
			t.Errorf("missing %q in %v", p, got)
		}
	}
}

func TestHashStable(t *testing.T) {
	const pinned = "9fb7573359a0"
	if got := HashOf("a.go", []string{"x"}, []string{"y"}); got != pinned {
		t.Errorf("hash = %s, want %s", got, pinned)
	}
	a, _, _ := Parse([]byte(modAddDel))
	moved := strings.ReplaceAll(modAddDel, "@@ -2 +2 @@", "@@ -20 +30 @@")
	b, _, _ := Parse([]byte(moved))
	if a[0].Hash != b[0].Hash || a[0].NewStart == b[0].NewStart {
		t.Errorf("hash should ignore position: %s %s", a[0].Hash, b[0].Hash)
	}
}

func TestAssignIDs(t *testing.T) {
	hs, _, _ := Parse([]byte(modAddDel))
	hs[0], hs[2] = hs[2], hs[0]
	AssignIDs(hs)
	for i, id := range []string{"h01", "h02", "h03"} {
		if hs[i].ID != id || hs[i].NewStart != []int{2, 6, 10}[i] {
			t.Errorf("%d: %s %d", i, hs[i].ID, hs[i].NewStart)
		}
	}
	many := make([]model.Hunk, 101)
	AssignIDs(many)
	if many[98].ID != "h99" || many[99].ID != "h100" {
		t.Errorf("%s %s", many[98].ID, many[99].ID)
	}
}

func TestHunkPatchAppliesAlone(t *testing.T) {
	g := gittest.New(t)
	var base, edit []string
	for i := 1; i <= 30; i++ {
		base = append(base, "line"+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	edit = append(edit, base...)
	edit[2] = "CHANGED"
	edit = append(edit[:10], append([]string{"inserted"}, edit[10:]...)...)
	edit = append(edit[:25], edit[26:]...)
	g.Write("f.txt", strings.Join(base, "\n")+"\n")
	g.Commit("init")
	r, from := snap(t, g)
	g.Write("f.txt", strings.Join(edit, "\n")+"\n")
	_, to := snap(t, g)
	hs, _, err := Diff(r, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 3 {
		t.Fatalf("got %d hunks", len(hs))
	}
	for _, h := range hs {
		if _, err := r.RunEnv(nil, bytes.NewReader(h.Patch), "apply", "--unidiff-zero", "-R", "--check"); err != nil {
			t.Errorf("%s: %v\n%s", h.ID, err, h.Patch)
		}
	}
	if hs[0].ID != "h01" || hs[2].ID != "h03" {
		t.Errorf("ids %s %s", hs[0].ID, hs[2].ID)
	}
}

func TestCRLFPreserved(t *testing.T) {
	d := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\r\n+b\r\n"
	hs, _, _ := Parse([]byte(d))
	if hs[0].Added[0] != "b\r" || !bytes.Contains(hs[0].Patch, []byte("+b\r\n")) {
		t.Errorf("%q %q", hs[0].Added, hs[0].Patch)
	}
}
