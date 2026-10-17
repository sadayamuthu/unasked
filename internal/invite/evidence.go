package invite

import (
	"strings"
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
// "*" matches a word with that prefix ("deprecat*" → deprecated); any other
// part matches a word with the same Snowball stem ("tests" → test).
type keywords []string

var (
	removalVerbs = keywords{"remove", "delete", "drop", "deprecat*", "strip", "get rid of", "clean out",
		"rip out", "eliminate", "unused"}
	testWords = keywords{"test", "tests", "testing", "spec", "specs", "assert*", "coverage", "flaky"}
	ciWords   = keywords{"ci", "workflow", "pipeline", "github action*"}
	depWords  = keywords{"upgrad*", "bump", "downgrad*", "depend*", "vendor"}
)

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

// nameWords is words with CamelCase runs split too, for matching names
// ("X-Request-Id" → x, request, id; "legacyPing" → legacy, ping).
func nameWords(s string) []string {
	var out []string
	for _, chunk := range strings.FieldsFunc(s, notWordRune) {
		for _, w := range splitCamel(chunk) {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

// evidence reports whether one prompt (text) asks for a high-risk hunk with
// label l in file specifically (R36b, R36c). terms are the hunk's removed
// symbols and literals, or its dependency names (Input.Terms).
//
//   - deletion: a removal verb (removalVerbs) AND a whole removed term: every
//     word of it among the prompt's words, or the term itself as a
//     case-insensitive substring bounded by non-word runes. No term, no
//     evidence.
//   - test: in a CI file (model.IsCIPath) a CI word (ciWords); in any other
//     test file a test word (testWords).
//   - dependency: a whole dependency name (as for deletion terms) or a
//     dependency word (depWords).
func evidence(l model.Label, file, text string, terms []string) bool {
	ws := words(text)
	switch l {
	case model.LabelDeletion:
		return removalVerbs.match(ws) && namesTerm(text, terms)
	case model.LabelTest:
		if model.IsCIPath(file) {
			return ciWords.match(ws)
		}
		return testWords.match(ws)
	case model.LabelDependency:
		return depWords.match(ws) || namesTerm(text, terms)
	}
	return true
}

// namesTerm reports whether the prompt text names some term whole: all of
// the term's nameWords are among the prompt's, or the term occurs in text,
// ignoring case, with no letter or digit on either side.
func namesTerm(text string, terms []string) bool {
	have := map[string]bool{}
	for _, w := range nameWords(text) {
		have[w] = true
	}
	lower := strings.ToLower(text)
	for _, term := range terms {
		tw := nameWords(term)
		if len(tw) == 0 {
			continue
		}
		all := true
		for _, w := range tw {
			if !have[w] {
				all = false
				break
			}
		}
		if all || containsBounded(lower, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

// containsBounded reports whether sub occurs in s with no letter or digit
// immediately before or after it.
func containsBounded(s, sub string) bool {
	for i := 0; sub != ""; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return false
		}
		j += i
		before, _ := utf8.DecodeLastRuneInString(s[:j])
		after, _ := utf8.DecodeRuneInString(s[j+len(sub):])
		if (j == 0 || notWordRune(before)) && (j+len(sub) == len(s) || notWordRune(after)) {
			return true
		}
		i = j + 1
	}
	return false
}
