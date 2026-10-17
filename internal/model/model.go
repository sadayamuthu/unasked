// Package model holds the types shared across unasked packages.
package model

import "time"

type Verdict string

const (
	VerdictInvited Verdict = "invited"
	VerdictUnclear Verdict = "unclear"
	VerdictUnasked Verdict = "unasked"
)

type Label string

const (
	LabelDeletion   Label = "deletion"
	LabelTest       Label = "test"
	LabelDependency Label = "dependency"
	LabelFeature    Label = "feature"
	LabelRefactor   Label = "refactor"
	LabelCollateral Label = "collateral"
	LabelUnknown    Label = "unknown"
)

// HighRisk reports whether the label is one of deletion, test or dependency.
func (l Label) HighRisk() bool {
	return l == LabelDeletion || l == LabelTest || l == LabelDependency
}

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
	SeverityNone    Severity = ""
)

type SymbolDelta struct {
	Added, Removed, Changed []string
	Enclosing               string
}

type Hunk struct {
	ID, File, OldFile                      string // ID "h01".. display only; OldFile set on rename
	OldStart, OldLines, NewStart, NewLines int
	Added, Removed                         []string // line text without newline
	NoNewlineOld, NoNewlineNew             bool
	Hash                                   string
	NewFile, DeletedFile                   bool
	Patch                                  []byte // standalone single-hunk patch with file header
	Symbols                                SymbolDelta
}

type Prompt struct {
	Turn int
	Text string
}

type EditEvent struct {
	Turn                   int
	File, After, Statement string
	Mechanical             bool
	Command                string
}

type Transcript struct {
	Prompts    []Prompt
	Edits      []EditEvent
	Commits    []string
	Version    string
	Start, End time.Time
	Turns      int
}

type Scored struct {
	Hunk           Hunk
	Asked, Said    float64
	AskedKnown     bool
	Tell           string
	Event          *EditEvent
	Verdict        Verdict
	Reason         string
	Label          Label
	Severity       Severity
	HighRisk       bool
	Summary        string
	XRefs          int
	RemovedSymbols []string
	Turn           int
}
