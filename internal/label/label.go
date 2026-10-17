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
	// Every removed symbol not re-added under the same name is a deletion
	// term, unless the hunk is a pure single-symbol rename (R11).
	if !isRename(h) {
		added := map[string]bool{}
		for _, a := range h.Symbols.Added {
			added[a] = true
		}
		for _, r := range h.Symbols.Removed {
			if !added[r] {
				d.terms = append(d.terms, r)
			}
		}
		if len(d.terms) > 0 {
			d.what = symbolKind(h.Removed)
		}
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
	if allBlankOrComment(h.Added) && removesCode(h) {
		return deletion{found: true, what: "symbol"}
	}
	return d
}

// isRename reports whether the hunk removes exactly one symbol and adds
// exactly one, and its removed and added lines are equal (whitespace
// normalised, same count) once the old name is replaced by the new as a
// whole word.
func isRename(h model.Hunk) bool {
	if len(h.Symbols.Removed) != 1 || len(h.Symbols.Added) != 1 || len(h.Removed) != len(h.Added) {
		return false
	}
	oldN, newN := h.Symbols.Removed[0], h.Symbols.Added[0]
	if oldN == newN {
		return false
	}
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(oldN) + `\b`)
	if err != nil {
		return false
	}
	for i, l := range h.Removed {
		l = re.ReplaceAllLiteralString(l, newN)
		if strings.Join(strings.Fields(l), " ") != strings.Join(strings.Fields(h.Added[i]), " ") {
			return false
		}
	}
	return true
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

func allBlankOrComment(lines []string) bool {
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !isComment(t) {
			return false
		}
	}
	return true
}

// removesCode reports whether a removed line is neither blank, a comment,
// nor an import line.
func removesCode(h model.Hunk) bool {
	imp := importFlags(h, h.Removed)
	for i, l := range h.Removed {
		t := strings.TrimSpace(l)
		if t == "" || isComment(t) || imp[i] {
			continue
		}
		return true
	}
	return false
}

var wsRunRe = regexp.MustCompile(`\s+`)

var importKwRe = regexp.MustCompile(`^import\b`)

var wsSignificantExt = map[string]bool{
	".py": true, ".pyi": true, ".yml": true, ".yaml": true, ".mk": true,
	".haml": true, ".pug": true, ".slim": true, ".coffee": true, ".sass": true, ".styl": true,
}

func wsSignificant(file string) bool {
	return wsSignificantExt[strings.ToLower(path.Ext(file))] || path.Base(file) == "Makefile"
}

// normLine trims the line and collapses interior whitespace runs outside
// string literals; literal contents are kept exactly.
func normLine(l string) string {
	l = strings.TrimSpace(l)
	var b strings.Builder
	last := 0
	for _, m := range literalRe.FindAllStringIndex(l, -1) {
		b.WriteString(wsRunRe.ReplaceAllString(l[last:m[0]], " "))
		b.WriteString(l[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(wsRunRe.ReplaceAllString(l[last:], " "))
	return b.String()
}

func leadingWS(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

func nonBlank(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// whitespaceOnly: the non-blank added and removed lines pair up one to one
// and are equal after normLine; in whitespace-significant files the leading
// indentation must be identical too.
func whitespaceOnly(h model.Hunk) bool {
	if len(h.Added)+len(h.Removed) == 0 {
		return false
	}
	a, r := nonBlank(h.Added), nonBlank(h.Removed)
	if len(a) != len(r) {
		return false
	}
	sig := wsSignificant(h.File)
	for i := range a {
		if normLine(a[i]) != normLine(r[i]) || (sig && leadingWS(a[i]) != leadingWS(r[i])) {
			return false
		}
	}
	return true
}

var goQuotedRe = regexp.MustCompile(`^(\w+|\.|_)?\s*"[^"]+"$`)

// importFlags marks which of lines are import lines. Go lines count only
// after an import keyword or inside an import ( ... ) block seen in the same
// list; with no opener, a bare quoted path counts only when every non-blank
// line is one and the hunk has no enclosing symbol.
func importFlags(h model.Hunk, lines []string) []bool {
	flags := make([]bool, len(lines))
	ext := strings.ToLower(path.Ext(h.File))
	if ext != ".go" {
		for i, l := range lines {
			t := strings.TrimSpace(l)
			switch ext {
			case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
				flags[i] = jsImportRe.MatchString(t)
			case ".py":
				flags[i] = pyImportRe.MatchString(t)
			}
		}
		return flags
	}
	inBlock, sawOpener, allQuoted := false, false, true
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
		case importKwRe.MatchString(t):
			flags[i] = true
			sawOpener = true
			inBlock = strings.HasSuffix(t, "(")
			allQuoted = false
		case inBlock && t == ")":
			flags[i], inBlock = true, false
		case inBlock && goQuotedRe.MatchString(t):
			flags[i] = true
		default:
			if !goQuotedRe.MatchString(t) {
				allQuoted = false
			}
		}
	}
	if !sawOpener && allQuoted && h.Symbols.Enclosing == "" {
		for i, l := range lines {
			flags[i] = strings.TrimSpace(l) != ""
		}
	}
	return flags
}

func importOnly(h model.Hunk) bool {
	n := 0
	for _, ls := range [][]string{h.Added, h.Removed} {
		for i, f := range importFlags(h, ls) {
			if strings.TrimSpace(ls[i]) == "" {
				continue
			}
			if !f {
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
