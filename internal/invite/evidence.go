package invite

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kljensen/snowball/english"
	"github.com/sadayamuthu/unasked/internal/model"
)

// genericReason is the reason when the evidence cap alone keeps a high-risk
// hunk from being invited.
const genericReason = "asked: generic overlap only"

// capMargin is how far below Invite the evidence cap holds an asked score.
const capMargin = 0.01

// keywords match prompt words (see words). An entry is one or more
// space-separated parts matched against consecutive words: a part ending in
// "*" matches a word with that prefix ("assert*" → assertions); any other
// part matches a word with the same Snowball stem ("tests" → test).
type keywords []string

var (
	testWords = keywords{"test", "tests", "testing", "spec", "specs", "assert*", "coverage", "flaky"}
	ciWords   = keywords{"ci", "workflow", "pipeline", "github action*"}
	depWords  = keywords{"upgrad*", "bump", "downgrad*", "depend*"}
)

// removalVerbs are the base (imperative) forms that can govern a removed
// term (R36f), matched exactly as consecutive words: "drops", "removing" or
// "unused" never count.
var removalVerbs = [][]string{
	{"remove"}, {"delete"}, {"drop"}, {"deprecate"}, {"strip"}, {"eliminate"},
	{"get", "rid", "of"}, {"clean", "out"}, {"rip", "out"},
}

// installWords make a one-word dependency name count when one is within
// depWindow words of it (R36f).
var installWords = map[string]bool{
	"add": true, "install": true, "dependency": true, "dependencies": true,
	"package": true, "library": true, "bump": true, "upgrade": true, "pin": true,
}

const (
	// verbReach: a removed term must start within this many words after the
	// end of a removal verb (R36f).
	verbReach = 4
	// depWindow: a one-word dependency name needs an install word within
	// this many words, either side (R36f).
	depWindow = 3
	// minWordTerm is the fewest characters a one-word removed name needs
	// (R36e).
	minWordTerm = 4
)

// termSeparators may stand between the words of a removed term written in
// a prompt ("X-Request-Id", "x request id", "x_request.id").
const termSeparators = " -_."

// match reports whether any keyword occurs in ws.
func (ks keywords) match(ws []string) bool {
	stems := make([]string, len(ws))
	for i, w := range ws {
		stems[i] = english.Stem(w, false)
	}
	partMatch := func(part string, i int) bool {
		if p, ok := strings.CutSuffix(part, "*"); ok {
			return strings.HasPrefix(ws[i], p)
		}
		return stems[i] == english.Stem(part, false)
	}
	for _, k := range ks {
		parts := strings.Fields(k)
		for i := 0; i+len(parts) <= len(ws); i++ {
			ok := true
			for j, p := range parts {
				if !partMatch(p, i+j) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// words splits s into raw lowercased words on any non-letter/digit rune,
// keeping one-character words and stop words ("GitHub Actions" → github,
// actions). Keywords match these.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), notWordRune)
}

// word is one lowercased word of a text and its byte span in that text.
type word struct {
	w          string
	start, end int
}

// scan splits s like words, also at CamelCase boundaries when camel is set
// ("legacyPing" → legacy, ping), keeping each word's byte span.
func scan(s string, camel bool) []word {
	var out []word
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if notWordRune(r) {
			i += n
			continue
		}
		j := i
		for j < len(s) {
			r, n := utf8.DecodeRuneInString(s[j:])
			if notWordRune(r) {
				break
			}
			j += n
		}
		parts := []string{s[i:j]}
		if camel {
			parts = splitCamel(s[i:j])
		}
		k := i
		for _, p := range parts {
			out = append(out, word{strings.ToLower(p), k, k + len(p)})
			k += len(p)
		}
		i = j
	}
	return out
}

// evidence reports whether one prompt (text) asks for a high-risk hunk with
// label l in file specifically (R36b–f). terms are the hunk's removed symbols
// and literals, or its full dependency names (Input.Terms).
//
//   - deletion: see namesDeletion.
//   - test: in a CI file (model.IsCIPath) a CI word (ciWords); in any other
//     test file a test word (testWords).
//   - dependency: a dependency word (depWords), or a dependency's full name
//     (see namesDependency).
func evidence(l model.Label, file, text string, terms []string) bool {
	switch l {
	case model.LabelDeletion:
		return namesDeletion(text, terms)
	case model.LabelTest:
		if model.IsCIPath(file) {
			return ciWords.match(words(text))
		}
		return testWords.match(words(text))
	case model.LabelDependency:
		return depWords.match(words(text)) || namesDependency(text, terms)
	}
	return true
}

// namesDeletion reports whether a base-form removal verb governs a removed
// term (R36f): the term starts within 4 words after the verb. The term is
// found as a contiguous phrase of its words (CamelCase split, case ignored,
// only termSeparators between them) or as the literal itself with no letter
// or digit on either side. A one-word term needs at least 4 characters.
// No term, no evidence.
func namesDeletion(text string, terms []string) bool {
	ws := scan(text, true)
	var verbEnds []int
	for i := range ws {
		for _, v := range removalVerbs {
			if i+len(v) > len(ws) {
				continue
			}
			ok := true
			for j, p := range v {
				if ws[i+j].w != p {
					ok = false
					break
				}
			}
			if ok {
				verbEnds = append(verbEnds, i+len(v))
			}
		}
	}
	if len(verbEnds) == 0 {
		return false
	}
	for _, term := range terms {
		tw := scan(term, true)
		if len(tw) == 0 || (len(tw) == 1 && utf8.RuneCountInString(tw[0].w) < minWordTerm) {
			continue
		}
		for _, s := range termStarts(text, ws, term, tw) {
			for _, e := range verbEnds {
				if s >= e && s < e+verbReach {
					return true
				}
			}
		}
	}
	return false
}

// termStarts returns the indexes in ws where term (its words tw) starts in
// text, as a phrase or as the bounded literal.
func termStarts(text string, ws []word, term string, tw []word) []int {
	var out []int
	for i := 0; i+len(tw) <= len(ws); i++ {
		ok := true
		for k := range tw {
			if ws[i+k].w != tw[k].w ||
				(k > 0 && strings.Trim(text[ws[i+k-1].end:ws[i+k].start], termSeparators) != "") {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	for _, p := range boundedIndexes(text, term) {
		for i, w := range ws {
			if w.end > p {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// namesDependency reports whether the prompt names a dependency by its full
// name (R36d, R36f): a name containing '/', '.', '@' or '-' counts wherever
// it occurs bounded (case ignored); a one-word name counts only with an
// install word (installWords) within 3 words of it.
func namesDependency(text string, terms []string) bool {
	ws := scan(text, false)
	for _, term := range terms {
		idx := boundedIndexes(text, term)
		if len(idx) == 0 {
			continue
		}
		if strings.ContainsAny(term, "/.@-") {
			return true
		}
		for _, p := range idx {
			at := -1
			for i, w := range ws {
				if w.start == p {
					at = i
					break
				}
			}
			for i := max(at-depWindow, 0); at >= 0 && i <= at+depWindow && i < len(ws); i++ {
				if installWords[ws[i].w] {
					return true
				}
			}
		}
	}
	return false
}

// boundedIndexes returns the byte offsets where sub occurs in s, ignoring
// case, with no letter or digit immediately before or after it.
func boundedIndexes(s, sub string) []int {
	var out []int
	if sub == "" {
		return nil
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if !strings.EqualFold(s[i:i+len(sub)], sub) {
			continue
		}
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(sub):])
		if (i == 0 || !isWordRune(before)) && (i+len(sub) == len(s) || !isWordRune(after)) {
			out = append(out, i)
		}
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
