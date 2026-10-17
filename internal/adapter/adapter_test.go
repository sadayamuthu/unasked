package adapter

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/model"
)

// fake is a test adapter whose Read delegates to a closure.
type fake struct {
	name string
	read func() (*model.Transcript, Capabilities, error)
}

func (f fake) Name() string                              { return f.name }
func (f fake) Detect() bool                              { return true }
func (f fake) Find(string, time.Time) ([]Session, error) { return nil, nil }
func (f fake) Read(Session) (*model.Transcript, Capabilities, error) {
	return f.read()
}
func (f fake) ParseCaptured(string) (*model.Transcript, Capabilities, error) {
	return nil, Capabilities{}, nil
}
func (f fake) MaxLevel() Level { return Full }

// resetForTest empties the package registry so tests do not see each other's adapters.
func resetForTest() { registry = map[string]Adapter{} }

func TestGuardedRecoversPanic(t *testing.T) {
	a := fake{name: "panicky"}
	tr, caps := Guarded(a, func() (*model.Transcript, Capabilities, error) {
		panic("boom")
	})
	if tr == nil {
		t.Fatal("Guarded returned nil transcript after panic")
	}
	if len(tr.Prompts) != 0 || len(tr.Edits) != 0 {
		t.Errorf("transcript not empty: %+v", tr)
	}
	if caps.Level != DiffOnly {
		t.Errorf("Level = %v, want %v", caps.Level, DiffOnly)
	}
	if !strings.HasPrefix(caps.Degraded, "panicky: ") || !strings.Contains(caps.Degraded, "boom") {
		t.Errorf("Degraded = %q, want prefix %q and mention of panic value", caps.Degraded, "panicky: ")
	}
}

func TestGuardedErrorDegrades(t *testing.T) {
	a := fake{name: "errs"}
	want := &model.Transcript{Prompts: []model.Prompt{{Turn: 1, Text: "partial"}}}
	tr, caps := Guarded(a, func() (*model.Transcript, Capabilities, error) {
		return want, Capabilities{Level: Full}, errors.New("bad header")
	})
	if tr == nil || len(tr.Prompts) != 0 {
		t.Errorf("error case must return an empty transcript, got %+v", tr)
	}
	if caps.Level != DiffOnly {
		t.Errorf("Level = %v, want %v", caps.Level, DiffOnly)
	}
	if caps.Degraded != "errs: bad header" {
		t.Errorf("Degraded = %q, want %q", caps.Degraded, "errs: bad header")
	}
}

func TestGuardedPassesThroughSuccess(t *testing.T) {
	a := fake{name: "ok"}
	want := &model.Transcript{Turns: 3}
	tr, caps := Guarded(a, func() (*model.Transcript, Capabilities, error) {
		return want, Capabilities{Level: Partial, Degraded: "unknown record types skipped: 2"}, nil
	})
	if tr != want {
		t.Errorf("transcript not passed through")
	}
	if caps.Level != Partial || caps.Degraded != "unknown record types skipped: 2" {
		t.Errorf("caps = %+v, want passthrough", caps)
	}
}

func TestGuardedNilTranscriptIsEmpty(t *testing.T) {
	a := fake{name: "nilt"}
	tr, _ := Guarded(a, func() (*model.Transcript, Capabilities, error) {
		return nil, Capabilities{Level: Full}, nil
	})
	if tr == nil {
		t.Fatal("nil transcript from successful read must be replaced with an empty one")
	}
}

func TestLevelString(t *testing.T) {
	for l, want := range map[Level]string{
		DiffOnly:    "diff-only",
		PromptsOnly: "prompts-only",
		Partial:     "partial",
		ReportOnly:  "report-only",
		Full:        "full",
	} {
		if got := l.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", int(l), got, want)
		}
	}
}

func TestRegistryOrderStable(t *testing.T) {
	resetForTest()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		Register(fake{name: n})
	}
	got := All()
	var names []string
	for _, a := range got {
		names = append(names, a.Name())
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Errorf("All() names = %v, want sorted [alpha mid zeta]", names)
	}
	again := All()
	if len(again) != 3 || again[0].Name() != "alpha" || again[2].Name() != "zeta" {
		t.Errorf("second All() differs: %v", again)
	}
	// The returned slice is a copy: mutating it must not change the registry.
	got[0] = nil
	if All()[0] == nil {
		t.Error("All() shares backing storage with the registry")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetForTest()
	Register(fake{name: "dup"})
	defer func() {
		if recover() == nil {
			t.Error("Register with a duplicate name did not panic")
		}
	}()
	Register(fake{name: "dup"})
}

func TestGet(t *testing.T) {
	resetForTest()
	Register(fake{name: "claude"})
	if a, ok := Get("claude"); !ok || a.Name() != "claude" {
		t.Errorf("Get(claude) = %v, %v", a, ok)
	}
	if _, ok := Get("codex"); ok {
		t.Error("Get(codex) found an unregistered adapter")
	}
}
