package keep

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

func sc(hash string, v model.Verdict) model.Scored {
	return model.Scored{Hunk: model.Hunk{Hash: hash}, Verdict: v}
}

func TestUserKeepRemovesHunk(t *testing.T) {
	s := []model.Scored{sc("a", model.VerdictUnasked), sc("b", model.VerdictUnasked)}
	kept, out := Merge(s, []Entry{{Hash: "a", Branch: "main", By: ByUser}}, "main")
	if len(kept) != 1 || len(out) != 1 || out[0].Hunk.Hash != "b" {
		t.Fatalf("kept=%v out=%v", kept, out)
	}
}

func TestAgentKeepStaysUnclear(t *testing.T) {
	s := []model.Scored{sc("a", model.VerdictUnasked), sc("b", model.VerdictInvited), sc("c", model.VerdictUnclear)}
	es := []Entry{
		{Hash: "a", Branch: "main", By: ByAgent, Reason: "needed"},
		{Hash: "b", Branch: "main", By: ByAgent, Reason: "needed"},
		{Hash: "c", Branch: "main", By: ByAgent, Reason: "needed"},
	}
	kept, out := Merge(s, es, "main")
	if len(kept) != 3 || len(out) != 3 {
		t.Fatalf("kept=%v out=%v", kept, out)
	}
	want := []model.Verdict{model.VerdictUnclear, model.VerdictInvited, model.VerdictUnclear}
	for i, o := range out {
		if o.Verdict != want[i] || o.Reason != "kept by agent: needed" {
			t.Errorf("out[%d] = %s %q", i, o.Verdict, o.Reason)
		}
	}
}

func TestKeepScopedToBranch(t *testing.T) {
	s := []model.Scored{sc("a", model.VerdictUnasked)}
	kept, out := Merge(s, []Entry{{Hash: "a", Branch: "other", By: ByUser}, {Hash: "zzz", Branch: "main", By: ByUser}}, "main")
	if len(kept) != 0 || len(out) != 1 {
		t.Fatalf("kept=%v out=%v", kept, out)
	}
}

func TestKeepSurvivesLineShift(t *testing.T) {
	h := sc("a", model.VerdictUnasked)
	h.Hunk.NewStart = 99
	kept, out := Merge([]model.Scored{h}, []Entry{{Hash: "a", Branch: "main", By: ByUser}}, "main")
	if len(kept) != 1 || len(out) != 0 {
		t.Fatalf("kept=%v out=%v", kept, out)
	}
}

func TestDetectByEnvAndTTY(t *testing.T) {
	none := func(string) string { return "" }
	if got := DetectBy(none, true); got != ByUser {
		t.Errorf("tty, no env = %s", got)
	}
	if got := DetectBy(none, false); got != ByAgent {
		t.Errorf("no tty = %s", got)
	}
	for _, k := range agentEnv {
		env := func(n string) string {
			if n == k {
				return "1"
			}
			return ""
		}
		if got := DetectBy(env, true); got != ByAgent {
			t.Errorf("%s = %s", k, got)
		}
	}
}

func TestCheckAgentKeep(t *testing.T) {
	if err := CheckAgentKeep(model.Scored{HighRisk: true}); !errors.Is(err, ErrAgentHighRisk) {
		t.Errorf("err = %v", err)
	}
	if err := CheckAgentKeep(model.Scored{}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestAddLoadReplace(t *testing.T) {
	g := gittest.New(t)
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if es, err := Load(r); err != nil || len(es) != 0 {
		t.Fatalf("empty load = %v, %v", es, err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, e := range []Entry{
		{Hash: "a", Branch: "main", By: ByUser, Reason: "one", At: at},
		{Hash: "a", Branch: "other", By: ByUser, At: at},
		{Hash: "a", Branch: "main", By: ByAgent, Reason: "two", At: at},
	} {
		if err := Add(r, e); err != nil {
			t.Fatal(err)
		}
	}
	es, err := Load(r)
	if err != nil || len(es) != 2 {
		t.Fatalf("load = %v, %v", es, err)
	}
	var main Entry
	for _, e := range es {
		if e.Branch == "main" {
			main = e
		}
	}
	if main.Reason != "two" || main.By != ByAgent || !main.At.Equal(at) {
		t.Errorf("main = %+v", main)
	}
	dir, err := r.UnaskedDir()
	if err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "keep.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
}

func TestLoadCorrupt(t *testing.T) {
	g := gittest.New(t)
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := r.UnaskedDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(r); err == nil {
		t.Error("want error")
	}
}

func TestUnknownAuthorFailsClosed(t *testing.T) {
	for _, by := range []By{"", "bogus", "User"} {
		s := []model.Scored{sc("a", model.VerdictUnasked)}
		kept, out := Merge(s, []Entry{{Hash: "a", Branch: "main", By: by, Reason: "x"}}, "main")
		if len(kept) != 1 || len(out) != 1 || out[0].Verdict != model.VerdictUnclear {
			t.Errorf("by %q: kept=%v out=%v", by, kept, out)
		}
	}
}

func TestAgentWinsOverUserDuplicate(t *testing.T) {
	for _, es := range [][]Entry{
		{{Hash: "a", Branch: "main", By: ByUser}, {Hash: "a", Branch: "main", By: ByAgent, Reason: "x"}},
		{{Hash: "a", Branch: "main", By: ByAgent, Reason: "x"}, {Hash: "a", Branch: "main", By: ByUser}},
	} {
		_, out := Merge([]model.Scored{sc("a", model.VerdictUnasked)}, es, "main")
		if len(out) != 1 {
			t.Errorf("hunk dropped: %v", out)
		}
	}
}

func TestAddRejectsBadAuthor(t *testing.T) {
	g := gittest.New(t)
	r, err := gitx.Open(g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(r, Entry{Hash: "a", Branch: "main", By: "bogus"}); err == nil {
		t.Error("want error")
	}
}
