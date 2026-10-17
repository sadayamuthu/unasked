// Package adapter defines the interface every coding-agent transcript reader
// implements, the support levels an adapter can report, and the version guard
// that turns a broken or changed transcript into a lower level instead of a crash.
package adapter

import (
	"fmt"
	"sort"
	"time"

	"github.com/sadayamuthu/unasked/internal/model"
)

// Level is how much of the agent's session an adapter can recover.
type Level int

const (
	DiffOnly Level = iota
	PromptsOnly
	Partial
	ReportOnly
	Full
)

var levelNames = [...]string{"diff-only", "prompts-only", "partial", "report-only", "full"}

// String returns the level's stable name, as shown in reports.
func (l Level) String() string {
	if l < 0 || int(l) >= len(levelNames) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return levelNames[l]
}

// HookKind is what an agent's hook mechanism can do with unasked's feedback.
type HookKind int

const (
	HookNone HookKind = iota
	HookObserve
	HookFeedBack
	HookBlock
)

// Capabilities describes what a Read or ParseCaptured call recovered.
// Degraded is empty unless the adapter fell back to a lower level.
type Capabilities struct {
	Level                      Level
	Prompts, Statements, Edits bool
	Hook                       HookKind
	Degraded                   string
}

// Session identifies one agent session found for a repository.
type Session struct {
	Agent, ID, Path, Cwd string
	Start, End           time.Time
	Captured             bool
	Meta                 *Meta // set for captured sessions
}

// Adapter reads one agent's session transcripts.
type Adapter interface {
	Name() string
	Detect() bool
	Find(repoDir string, since time.Time) ([]Session, error)
	Read(s Session) (*model.Transcript, Capabilities, error)
	ParseCaptured(dir string) (*model.Transcript, Capabilities, error)
	MaxLevel() Level
}

var registry = map[string]Adapter{}

// Register adds an adapter under its Name. It panics on a duplicate name.
// Adapters register from init functions, so a duplicate is a programming error.
func Register(a Adapter) {
	name := a.Name()
	if _, dup := registry[name]; dup {
		panic("adapter: duplicate registration of " + name)
	}
	registry[name] = a
}

// All returns the registered adapters sorted by Name. The slice is a copy.
func All() []Adapter {
	out := make([]Adapter, 0, len(registry))
	for _, a := range registry {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Get returns the adapter registered under name.
func Get(name string) (Adapter, bool) {
	a, ok := registry[name]
	return a, ok
}

// Guarded runs f, the adapter's read of one transcript, and converts a panic
// or an error into an empty transcript at DiffOnly with Degraded set to
// "<adapter name>: <reason>". A successful read with a nil transcript is
// replaced with an empty one, so callers never dereference nil.
func Guarded(a Adapter, f func() (*model.Transcript, Capabilities, error)) (tr *model.Transcript, caps Capabilities) {
	name := a.Name()
	defer func() {
		if r := recover(); r != nil {
			tr, caps = &model.Transcript{}, degraded(name, fmt.Sprintf("panic: %v", r))
		}
	}()
	t, c, err := f()
	if err != nil {
		return &model.Transcript{}, degraded(name, err.Error())
	}
	if t == nil {
		t = &model.Transcript{}
	}
	return t, c
}

func degraded(name, reason string) Capabilities {
	return Capabilities{Level: DiffOnly, Degraded: name + ": " + reason}
}
