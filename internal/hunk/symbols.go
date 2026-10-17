package hunk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

// sym is one declaration found in a source file. Lines are 1-based and
// inclusive: [start,end] is the whole declaration (end==start when unknown,
// as with regex extraction) and [start,sigEnd] its declaration line(s).
type sym struct {
	name       string
	start, end int
	sigEnd     int
	sig        string
	hasExtent  bool
}

// Symbols computes the symbol delta and enclosing name for one hunk, given the
// old and new contents of the file (either may be empty for added/deleted
// files). It never panics: Go files that fail to parse fall back to regex
// extraction, and unknown languages to a generic enclosing-name heuristic.
func Symbols(filePath string, oldSrc, newSrc []byte, h model.Hunk) (out model.SymbolDelta) {
	defer func() {
		if recover() != nil {
			out = model.SymbolDelta{}
		}
	}()
	ext := strings.ToLower(path.Ext(filePath))
	oldSyms := extract(ext, oldSrc)
	newSyms := extract(ext, newSrc)

	oldRange := lineRange{h.OldStart, h.OldLines}
	newRange := lineRange{h.NewStart, h.NewLines}

	oldBy, newBy := byName(oldSyms), byName(newSyms)
	added, removed, changed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, s := range newSyms {
		if !newRange.overlaps(s.start, s.sigEnd) {
			continue
		}
		if _, ok := oldBy[s.name]; !ok {
			added[s.name] = true
		} else if sigOf(oldBy[s.name]) != sigOf(newBy[s.name]) {
			changed[s.name] = true
		}
	}
	for _, s := range oldSyms {
		if !oldRange.overlaps(s.start, s.sigEnd) {
			continue
		}
		if _, ok := newBy[s.name]; !ok {
			removed[s.name] = true
		} else if sigOf(oldBy[s.name]) != sigOf(newBy[s.name]) {
			changed[s.name] = true
		}
	}
	out.Added, out.Removed, out.Changed = sortedKeys(added), sortedKeys(removed), sortedKeys(changed)

	// Enclosing: removals are located in the old file, everything else in the new.
	src, syms, line := newSrc, newSyms, h.NewStart
	if (h.NewLines == 0 && h.OldLines > 0 && len(oldSrc) > 0) || len(newSrc) == 0 {
		src, syms, line = oldSrc, oldSyms, h.OldStart
	}
	if line < 1 {
		line = 1
	}
	out.Enclosing = enclosing(syms, src, ext, line)
	return out
}

type lineRange struct{ start, count int }

func (r lineRange) overlaps(a, b int) bool {
	if r.count <= 0 {
		return false
	}
	return a <= r.start+r.count-1 && b >= r.start
}

func byName(ss []sym) map[string][]sym {
	m := map[string][]sym{}
	for _, s := range ss {
		m[s.name] = append(m[s.name], s)
	}
	return m
}

func sigOf(ss []sym) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = s.sig
	}
	return strings.Join(parts, "\n")
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func splitLines(src []byte) []string {
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func extract(ext string, src []byte) []sym {
	if len(src) == 0 {
		return nil
	}
	if ext == ".go" {
		if ss, ok := goSymbols(src); ok {
			return ss
		}
	}
	return regexSymbols(ext, src)
}

// enclosing names the declaration containing line, falling back to the generic
// heuristic when the language tables find nothing.
func enclosing(syms []sym, src []byte, ext string, line int) string {
	best := -1
	for i, s := range syms {
		if s.start > line {
			continue
		}
		if s.hasExtent {
			if line > s.end {
				continue
			}
			// innermost: latest start wins
			if best < 0 || s.start >= syms[best].start {
				best = i
			}
		} else if best < 0 || (!syms[best].hasExtent || s.start >= syms[best].start) {
			best = i
		}
	}
	if best >= 0 {
		return syms[best].name
	}
	if ext == ".go" && hasGoExtent(syms) {
		return "" // parsed fine; line is outside any declaration
	}
	return genericEnclosing(src, line)
}

func hasGoExtent(syms []sym) bool {
	for _, s := range syms {
		if s.hasExtent {
			return true
		}
	}
	return false
}

// --- Go -------------------------------------------------------------------

func goSymbols(src []byte) (ss []sym, ok bool) {
	defer func() {
		if recover() != nil {
			ss, ok = nil, false
		}
	}()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil || f == nil {
		return nil, false
	}
	lines := splitLines(src)
	line := func(p token.Pos) int { return fset.Position(p).Line }
	sigText := func(a, b int) string {
		if a < 1 {
			a = 1
		}
		if b > len(lines) {
			b = len(lines)
		}
		if a > b {
			return ""
		}
		parts := make([]string, 0, b-a+1)
		for _, l := range lines[a-1 : b] {
			parts = append(parts, strings.TrimSpace(l))
		}
		return strings.Join(parts, "\n")
	}
	add := func(name string, start, end, sigEnd int) {
		if name == "_" || name == "" {
			return
		}
		ss = append(ss, sym{name: name, start: start, end: end, sigEnd: sigEnd,
			sig: sigText(start, sigEnd), hasExtent: true})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if t := recvTypeName(d.Recv.List[0].Type); t != "" {
					name = t + "." + name
				}
			}
			start, end := line(d.Pos()), line(d.End())
			sigEnd := end
			if d.Body != nil {
				sigEnd = line(d.Body.Lbrace)
			}
			add(name, start, end, sigEnd)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				start, end := line(spec.Pos()), line(spec.End())
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name.Name, start, end, start)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n.Name, start, end, start)
					}
				}
			}
		}
	}
	return ss, true
}

func recvTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// --- Regex tables ---------------------------------------------------------

const (
	tsMods  = `(?:export\s+)?(?:default\s+)?(?:declare\s+)?`
	rsVis   = `(?:pub(?:\([^)]*\))?\s+)?`
	jvMods  = `(?:(?:public|private|protected|internal|static|final|abstract|sealed|partial|readonly|synchronized|override|virtual|async|native|unsafe|extern|new)\s+)`
	jvType  = `(?:[\w.?]+(?:<[^>]*>)?(?:\[\])*\s+)?`
	rsQuals = `(?:(?:async|const|unsafe)\s+|extern\s+"[^"]*"\s+)*`
)

var regexTables = map[string][]*regexp.Regexp{}

func init() {
	ts := []*regexp.Regexp{
		regexp.MustCompile(`^` + tsMods + `(?:async\s+)?function\s*\*?\s*(\w+)`),
		regexp.MustCompile(`^` + tsMods + `(?:abstract\s+)?class\s+(\w+)`),
		regexp.MustCompile(`^` + tsMods + `(?:const|let|var)\s+(\w+)`),
		regexp.MustCompile(`^` + tsMods + `interface\s+(\w+)`),
		regexp.MustCompile(`^` + tsMods + `type\s+(\w+)`),
		regexp.MustCompile(`^` + tsMods + `(?:const\s+)?enum\s+(\w+)`),
	}
	for _, e := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs"} {
		regexTables[e] = ts
	}
	regexTables[".py"] = []*regexp.Regexp{
		regexp.MustCompile(`^\s*(?:async\s+)?def\s+(\w+)`),
		regexp.MustCompile(`^\s*class\s+(\w+)`),
	}
	regexTables[".rs"] = []*regexp.Regexp{
		regexp.MustCompile(`^\s*` + rsVis + rsQuals + `fn\s+(\w+)`),
		regexp.MustCompile(`^\s*` + rsVis + `(?:struct|enum|trait|union)\s+(\w+)`),
		regexp.MustCompile(`^\s*(?:unsafe\s+)?impl(?:\s*<[^>]*>)?\s+(?:!?[\w:]+(?:<[^>]*>)?\s+for\s+)?&?(\w+)`),
	}
	jv := []*regexp.Regexp{
		regexp.MustCompile(`^\s*` + jvMods + `*(?:class|interface|enum|record|struct)\s+(\w+)`),
		regexp.MustCompile(`^\s*` + jvMods + `+` + jvType + `(\w+)\s*\(`),
	}
	regexTables[".java"] = jv
	regexTables[".cs"] = jv
	regexTables[".rb"] = []*regexp.Regexp{
		regexp.MustCompile(`^\s*def\s+(?:self\.)?(\w+[?!=]?)`),
		regexp.MustCompile(`^\s*(?:class|module)\s+(\w+)`),
	}
	regexTables[".go"] = nil // handled by goRegex
}

var (
	goFuncRe = regexp.MustCompile(`^func\s+(?:\(\s*(?:\w+\s+)?\*?\s*(\w+)[^)]*\)\s*)?(\w+)`)
	goTypeRe = regexp.MustCompile(`^(?:type|const|var)\s+(\w+)`)
)

func regexSymbols(ext string, src []byte) []sym {
	table, known := regexTables[ext]
	if !known {
		return nil
	}
	var ss []sym
	for i, l := range splitLines(src) {
		var name string
		if ext == ".go" {
			if m := goFuncRe.FindStringSubmatch(l); m != nil {
				name = m[2]
				if m[1] != "" {
					name = m[1] + "." + m[2]
				}
			} else if m := goTypeRe.FindStringSubmatch(l); m != nil {
				name = m[1]
			}
		} else {
			for _, re := range table {
				if m := re.FindStringSubmatch(l); m != nil {
					name = m[1]
					break
				}
			}
		}
		if name == "" || name == "_" {
			continue
		}
		ss = append(ss, sym{name: name, start: i + 1, end: i + 1, sigEnd: i + 1, sig: strings.TrimSpace(l)})
	}
	return ss
}

// --- Generic fallback -----------------------------------------------------

var (
	identParenRe = regexp.MustCompile(`(\w+)\s*\(`)
	notNames     = map[string]bool{"if": true, "for": true, "while": true, "switch": true,
		"return": true, "catch": true, "else": true, "sizeof": true, "do": true, "case": true}
)

// genericEnclosing finds the identifier before "(" on the nearest preceding
// non-indented line (the line itself included).
func genericEnclosing(src []byte, line int) string {
	lines := splitLines(src)
	if line > len(lines) {
		line = len(lines)
	}
	for i := line - 1; i >= 0; i-- {
		l := lines[i]
		if l == "" || l[0] == ' ' || l[0] == '\t' {
			continue
		}
		for _, m := range identParenRe.FindAllStringSubmatch(l, -1) {
			if !notNames[m[1]] {
				return m[1]
			}
		}
	}
	return ""
}
