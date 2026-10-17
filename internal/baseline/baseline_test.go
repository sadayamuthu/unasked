package baseline

import (
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
)

func open(t *testing.T, g *gittest.Repo) *gitx.Repo {
	t.Helper()
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSnapshotIncludesUntracked(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Commit("init")
	g.Write("new.txt", "n\n")
	r := open(t, g)
	before := g.Git("diff", "--cached", "--name-only")
	b, err := Snapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.Git("ls-tree", "-r", "--name-only", b.Tree), "new.txt") {
		t.Fatal("new.txt missing from tree")
	}
	if after := g.Git("diff", "--cached", "--name-only"); after != before {
		t.Fatalf("index changed: %q -> %q", before, after)
	}
	if b.Commit != g.Git("rev-parse", "HEAD") || b.Taken.IsZero() {
		t.Fatalf("bad baseline %+v", b)
	}
}

func TestSnapshotExcludesIgnored(t *testing.T) {
	g := gittest.New(t)
	g.Write(".gitignore", "*.log\n")
	g.Commit("init")
	g.Write("x.log", "log\n")
	g.Write("y.txt", "y\n")
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	out := g.Git("ls-tree", "-r", "--name-only", b.Tree)
	if strings.Contains(out, "x.log") || !strings.Contains(out, "y.txt") {
		t.Fatalf("tree: %s", out)
	}
}

func TestSnapshotLeavesIndexByteIdentical(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Commit("init")
	g.Write("a.txt", "changed\n")
	g.Write("new.txt", "n\n")
	g.Git("add", "a.txt")
	sum := func() [32]byte {
		b, err := os.ReadFile(g.Dir + "/.git/index")
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(b)
	}
	before := sum()
	r := open(t, g)
	if _, err := Snapshot(r); err != nil {
		t.Fatal(err)
	}
	if _, err := IndexTree(r); err != nil {
		t.Fatal(err)
	}
	if sum() != before {
		t.Fatal("index bytes changed")
	}
}

func TestSnapshotRawBytes(t *testing.T) {
	g := gittest.New(t)
	g.Git("config", "core.autocrlf", "true")
	g.Write("seed", "s\n")
	g.Commit("init")
	g.WriteBytes("crlf.txt", []byte("a\r\nb\r\n"))
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	blob := g.Git("rev-parse", b.Tree+":crlf.txt")
	content := g.Git("cat-file", "-p", blob)
	// Git() trims only outer whitespace; inner \r\n must survive.
	if !strings.Contains(content, "a\r\nb") {
		t.Fatalf("CRLF lost: %q", content)
	}
}

func TestSnapshotUnmergedIndex(t *testing.T) {
	g := gittest.New(t)
	g.Write("f", "base\n")
	g.Commit("base")
	g.Git("checkout", "-q", "-b", "other")
	g.Write("f", "other\n")
	g.Commit("other")
	g.Git("checkout", "-q", "main")
	g.Write("f", "main\n")
	g.Commit("main")
	merge := g.Cmd("merge", "other")
	if err := merge.Run(); err == nil {
		t.Fatal("expected merge conflict")
	}
	r := open(t, g)
	if _, err := Snapshot(r); !errors.Is(err, ErrUnmerged) {
		t.Fatalf("Snapshot err = %v", err)
	}
	if _, err := IndexTree(r); !errors.Is(err, ErrUnmerged) {
		t.Fatalf("IndexTree err = %v", err)
	}
}

func TestSnapshotNoCommits(t *testing.T) {
	g := gittest.New(t)
	g.Write("u.txt", "u\n")
	r := open(t, g)
	b, err := Snapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	if b.Commit != "" || b.Tree == "" {
		t.Fatalf("%+v", b)
	}
	if !strings.Contains(g.Git("ls-tree", "-r", "--name-only", b.Tree), "u.txt") {
		t.Fatal("u.txt missing")
	}
	if _, err := IndexTree(r); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDetachedHead(t *testing.T) {
	g := gittest.New(t)
	g.Write("a", "a\n")
	sha := g.Commit("one")
	g.Write("a", "b\n")
	g.Commit("two")
	g.Git("checkout", "-q", sha)
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	if b.Commit != sha {
		t.Fatalf("commit %s want %s", b.Commit, sha)
	}
}

func indexSum(t *testing.T, g *gittest.Repo) [32]byte {
	t.Helper()
	b, err := os.ReadFile(g.Dir + "/.git/index")
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func blobAt(g *gittest.Repo, tree, path string) string {
	return g.Git("cat-file", "-p", g.Git("rev-parse", tree+":"+path))
}

func TestSnapshotSeesAssumeUnchangedEdit(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Write("b.txt", "b\n")
	g.Commit("init")
	g.Git("update-index", "--assume-unchanged", "a.txt")
	g.Write("a.txt", "edited\n")
	g.Write("b.txt", "edited too\n")
	before := indexSum(t, g)
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	if got := blobAt(g, b.Tree, "a.txt"); got != "edited" {
		t.Fatalf("a.txt in snapshot = %q", got)
	}
	if indexSum(t, g) != before {
		t.Fatal("real index changed")
	}
	if !strings.HasPrefix(g.Git("ls-files", "-v", "a.txt"), "h") {
		t.Fatal("real index lost its assume-unchanged flag")
	}
}

func TestSnapshotSeesSkipWorktreeEdit(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Commit("init")
	g.Git("update-index", "--skip-worktree", "a.txt")
	g.Write("a.txt", "edited\n")
	before := indexSum(t, g)
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	if got := blobAt(g, b.Tree, "a.txt"); got != "edited" {
		t.Fatalf("a.txt in snapshot = %q", got)
	}
	if indexSum(t, g) != before {
		t.Fatal("real index changed")
	}
}

func TestSnapshotSparseAbsentNotDeleted(t *testing.T) {
	g := gittest.New(t)
	g.Write("a.txt", "a\n")
	g.Write("b.txt", "b\n")
	g.Commit("init")
	g.Git("update-index", "--skip-worktree", "b.txt")
	g.Remove("b.txt")
	g.Write("a.txt", "edited\n")
	before := indexSum(t, g)
	b, err := Snapshot(open(t, g))
	if err != nil {
		t.Fatal(err)
	}
	if got := blobAt(g, b.Tree, "b.txt"); got != "b" {
		t.Fatalf("absent sparse file should be kept, got %q", got)
	}
	if got := blobAt(g, b.Tree, "a.txt"); got != "edited" {
		t.Fatalf("a.txt = %q", got)
	}
	if indexSum(t, g) != before {
		t.Fatal("real index changed")
	}
}
