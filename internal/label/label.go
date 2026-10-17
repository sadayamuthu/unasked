// Package label assigns each scored hunk a label, severity, summary and
// cross-reference count, and applies the collateral fold rule.
package label

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

var (
	testPathRes = []*regexp.Regexp{
		regexp.MustCompile(`_test\.go$`),
		regexp.MustCompile(`(^|/)test_[^/]*\.py$`),
		regexp.MustCompile(`_test\.py$`),
		regexp.MustCompile(`\.test\.[jt]sx?$`),
		regexp.MustCompile(`\.spec\.[jt]sx?$`),
		regexp.MustCompile(`(^|/)tests?/`),
		regexp.MustCompile(`(^|/)__tests__/`),
		regexp.MustCompile(`(^|/)testdata/`),
		regexp.MustCompile(`(^|/)\.github/workflows/`),
		regexp.MustCompile(`(^|/)\.circleci/`),
		regexp.MustCompile(`(^|/)(\.gitlab-ci\.yml|Jenkinsfile|azure-pipelines\.yml)$`),
	}
	depBases = map[string]bool{
		"go.mod": true, "go.sum": true, "package.json": true, "package-lock.json": true,
		"yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true, "Cargo.toml": true,
		"Cargo.lock": true, "poetry.lock": true, "Pipfile": true, "Pipfile.lock": true,
		"pyproject.toml": true, "Gemfile": true, "Gemfile.lock": true, "composer.json": true,
		"composer.lock": true,
	}

	literalRe     = regexp.MustCompile("\"([^\"\\\\]|\\\\.)*\"|'([^'\\\\]|\\\\.)*'|`[^`]*`")
	headerRe      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*(-[A-Z][A-Za-z0-9]*)+$`)
	routeRe       = regexp.MustCompile(`^/[\w/:{}-]+$`)
	flagRe        = regexp.MustCompile(`^--?[a-z][\w-]*$`)
	envRe         = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)
	goImportRe    = regexp.MustCompile(`^(import\b.*|(\w+|\.|_)?\s*"[^"]+"|\))$`)
	jsImportRe    = regexp.MustCompile(`^(import\b.*|\}?\s*from\s+['"].*|(const|let|var)\s+.*=\s*require\(.*|require\(.*)$`)
	pyImportRe    = regexp.MustCompile(`^(import\s+\S.*|from\s+\S+\s+import\b.*)$`)
	funcLineRe    = regexp.MustCompile(`^(func|def|function|fn|async function|export function)\b`)
	typeLineRe    = regexp.MustCompile(`^(type|class|struct|interface|enum|export class|export interface)\b`)
	commentPrefix = []string{"//", "#", "/*", "*", "<!--", "--"}
)

// ClassifyHunk adapts Classify for invite.Input.Classify, which has no edit
// event. Mechanical (ev.Mechanical) is applied later, in Apply.
func ClassifyHunk(h model.Hunk) (model.Label, bool) { return Classify(h, nil) }

// Classify labels a hunk and reports whether the label is high-risk. ev may
// be nil.
//
// Precedence: test, dependency, deletion, then collateral for whitespace-only
// and import-only hunks, feature, refactor on changed symbols, collateral for
// a mechanical edit, refactor on an enclosing symbol, and unknown. Whitespace
// and import hunks come before refactor because every hunk inside a function
// has an enclosing symbol.
func Classify(h model.Hunk, ev *model.EditEvent) (model.Label, bool) {
	l := classify(h, ev)
	return l, l.HighRisk()
}

func classify(h model.Hunk, ev *model.EditEvent) model.Label {
	switch {
	case isTestPath(h.File):
		return model.LabelTest
	case depBases[path.Base(h.File)] || isRequirements(path.Base(h.File)):
		return model.LabelDependency
	}
	if d := deletionOf(h); d.found {
		return model.LabelDeletion
	}
	switch {
	case whitespaceOnly(h) || importOnly(h):
		return model.LabelCollateral
	case hasFeature(h):
		return model.LabelFeature
	case len(h.Symbols.Changed) > 0:
		return model.LabelRefactor
	case ev != nil && ev.Mechanical:
		return model.LabelCollateral
	case h.Symbols.Enclosing != "":
		return model.LabelRefactor
	}
	return model.LabelUnknown
}

func isTestPath(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, re := range testPathRes {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

func isRequirements(base string) bool {
	ok, _ := path.Match("requirements*.txt", base)
	return ok
}

func literals(lines []string) []string {
	var out []string
	for _, l := range lines {
		for _, m := range literalRe.FindAllString(l, -1) {
			if len(m) >= 2 {
				out = append(out, m[1:len(m)-1])
			}
		}
	}
	return out
}

// literalKind names the shape of a string literal, or "" if it has none of
// the header, route, flag or config-key shapes.
func literalKind(s string) string {
	switch {
	case headerRe.MatchString(s):
		return "header"
	case routeRe.MatchString(s):
		return "route"
	case flagRe.MatchString(s):
		return "flag"
	case envRe.MatchString(s):
		return "config key"
	}
	return ""
}

func isExported(file, name string) bool {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return false
	}
	if strings.EqualFold(path.Ext(file), ".go") {
		r, _ := utf8.DecodeRuneInString(name)
		return unicode.IsUpper(r)
	}
	return !strings.HasPrefix(name, "_")
}

func hasFeature(h model.Hunk) bool {
	for _, s := range h.Symbols.Added {
		if isExported(h.File, s) {
			return true
		}
	}
	for _, l := range literals(h.Added) {
		switch literalKind(l) {
		case "route", "flag", "config key":
			return true
		}
	}
	return false
}

// deletion describes why a hunk is a deletion.
type deletion struct {
	found bool
	file  bool
	terms []string // removed symbols, then removed literals
	what  string   // function|type|header|route|flag|config key|file|symbol
}

func deletionOf(h model.Hunk) deletion {
	if h.DeletedFile {
		return deletion{found: true, file: true, what: "file"}
	}
	var d deletion
	// A removed symbol re-added under another name (a rename) is not a
	// deletion; only the net loss counts.
	if len(h.Symbols.Removed) > len(h.Symbols.Added) {
		d.terms = append(d.terms, h.Symbols.Removed...)
		d.what = symbolKind(h.Removed)
	}
	addedLits := map[string]bool{}
	for _, l := range literals(h.Added) {
		addedLits[l] = true
	}
	seen := map[string]bool{}
	for _, l := range literals(h.Removed) {
		if k := literalKind(l); k != "" && !addedLits[l] && !seen[l] {
			seen[l] = true
			d.terms = append(d.terms, l)
			if d.what == "" {
				d.what = k
			}
		}
	}
	if len(d.terms) > 0 {
		d.found = true
		return d
	}
	// Pure removal of real code: never let it be invited by a statement.
	if len(h.Added) == 0 && removesCode(h) {
		return deletion{found: true, what: "symbol"}
	}
	return d
}

func symbolKind(removed []string) string {
	for _, l := range removed {
		l = strings.TrimSpace(l)
		if funcLineRe.MatchString(l) {
			return "function"
		}
		if typeLineRe.MatchString(l) {
			return "type"
		}
	}
	return "symbol"
}

func isComment(l string) bool {
	for _, p := range commentPrefix {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// removesCode reports whether a removed line is neither blank, a comment,
// nor an import line.
func removesCode(h model.Hunk) bool {
	for _, l := range h.Removed {
		t := strings.TrimSpace(l)
		if t == "" || isComment(t) || isImportLine(h.File, t) {
			continue
		}
		return true
	}
	return false
}

func stripSpace(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		for _, r := range l {
			if !unicode.IsSpace(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func whitespaceOnly(h model.Hunk) bool {
	if len(h.Added)+len(h.Removed) == 0 {
		return false
	}
	return stripSpace(h.Added) == stripSpace(h.Removed)
}

func isImportLine(file, t string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".go":
		return goImportRe.MatchString(t)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return jsImportRe.MatchString(t)
	case ".py":
		return pyImportRe.MatchString(t)
	}
	return false
}

func importOnly(h model.Hunk) bool {
	n := 0
	for _, ls := range [][]string{h.Added, h.Removed} {
		for _, l := range ls {
			t := strings.TrimSpace(l)
			if t == "" {
				continue
			}
			if !isImportLine(h.File, t) {
				return false
			}
			n++
		}
	}
	return n > 0
}

// Apply sets Label, HighRisk, Severity, Summary, XRefs and RemovedSymbols on
// every hunk and applies the collateral rule: a collateral hunk that is not
// already invited is folded (verdict invited, reason "collateral") only if
// another hunk in the same file is invited for a reason other than
// collateral. Nothing else ever changes a verdict. r may be nil (no xrefs).
func Apply(r *gitx.Repo, s []model.Scored) []model.Scored {
	out := make([]model.Scored, len(s))
	copy(out, s)

	invitedFiles := map[string]bool{}
	for _, h := range s {
		if h.Verdict == model.VerdictInvited && h.Reason != "collateral" {
			invitedFiles[h.Hunk.File] = true
		}
	}
	for i := range out {
		h := &out[i]
		h.Label, h.HighRisk = Classify(h.Hunk, h.Event)
		h.RemovedSymbols = nil
		h.XRefs = 0
		if h.Label == model.LabelDeletion {
			if d := deletionOf(h.Hunk); !d.file {
				h.RemovedSymbols = d.terms
				if r != nil {
					h.XRefs = countXRefs(r, h.Hunk.File, d.terms)
				}
			}
		}
		folded := false
		if h.Label == model.LabelCollateral && h.Verdict != model.VerdictInvited && invitedFiles[h.Hunk.File] {
			h.Verdict, h.Reason = model.VerdictInvited, "collateral"
			folded = true
		}
		switch {
		case folded:
			h.Severity = model.SeverityInfo
		case h.Verdict == model.VerdictInvited:
			h.Severity = model.SeverityNone
		case h.HighRisk && h.Verdict == model.VerdictUnasked:
			h.Severity = model.SeverityError
		default:
			h.Severity = model.SeverityWarning
		}
		h.Summary = Summary(*h)
	}
	return out
}

// Summary renders the fixed one-line description for the scored hunk's label.
func Summary(s model.Scored) string {
	h := s.Hunk
	switch s.Label {
	case model.LabelDeletion:
		d := deletionOf(h)
		what, name := d.what, h.File
		if what == "" {
			what = "symbol"
		}
		switch {
		case d.file:
		case len(d.terms) > 0:
			name = d.terms[0]
		case h.Symbols.Enclosing != "":
			name = h.Symbols.Enclosing
		}
		out := fmt.Sprintf("deleted %s %s", what, name)
		if s.XRefs > 0 {
			out += fmt.Sprintf(" still referenced in %d files", s.XRefs)
		}
		return out
	case model.LabelTest:
		return "changed test/CI file " + h.File
	case model.LabelDependency:
		return "changed dependencies in " + h.File
	case model.LabelFeature:
		return "added " + featureName(h)
	case model.LabelRefactor:
		name := h.Symbols.Enclosing
		if name == "" && len(h.Symbols.Changed) > 0 {
			name = h.Symbols.Changed[0]
		}
		return "changed body of " + name
	case model.LabelCollateral:
		return "formatting/import changes in " + h.File
	}
	start := h.NewStart
	return fmt.Sprintf("changed %s:%d-%d", h.File, start, max(start, start+h.NewLines-1))
}

func featureName(h model.Hunk) string {
	for _, a := range h.Symbols.Added {
		if isExported(h.File, a) {
			return a
		}
	}
	for _, l := range literals(h.Added) {
		switch literalKind(l) {
		case "route", "flag", "config key":
			return l
		}
	}
	return h.File
}
