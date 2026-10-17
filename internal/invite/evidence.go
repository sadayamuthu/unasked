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
	depWords  = keywords{"upgrad*", "bump", "downgrad*", "depend*"}
)

// verbWindow is how many words from a removal verb a one-word removed name
// may stand (R36e).
const verbWindow = 5

// minWordTerm is the fewest characters a one-word removed name needs to count
// as naming the deletion (R36e).
const minWordTerm = 4

// match reports whether any keyword occurs in ws.
func (ks keywords) match(ws []string) bool { return len(ks.find(ws)) > 0 }

// find returns the [start, end) word spans of every keyword occurrence in ws.
func (ks keywords) find(ws []string) [][2]int {
	var spans [][2]int
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
				spans = append(spans, [2]int{i, i + len(parts)})
			}
		}
	}
	return spans
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
// label l in file specifically (R36b–e). terms are the hunk's removed symbols
// and literals, or its full dependency names (Input.Terms).
//
//   - deletion: the prompt names a removed term and has a removal verb
//     (removalVerbs). A term of several words (nameWords) is named when all
//     of its words are among the prompt's, or it occurs whole (see
//     containsBounded), with the verb anywhere. A one-word term needs at
//     least 4 characters and a verb within 5 words of it. No term, no
//     evidence.
//   - test: in a CI file (model.IsCIPath) a CI word (ciWords); in any other
//     test file a test word (testWords).
//   - dependency: a dependency word (depWords), or a full dependency name
//     occurring whole, ignoring case.
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
		if depWords.match(words(text)) {
			return true
		}
		lower := strings.ToLower(text)
		for _, term := range terms {
			if containsBounded(lower, strings.ToLower(term)) {
				return true
			}
		}
		return false
	}
	return true
}

// namesDeletion applies the deletion rule of evidence.
func namesDeletion(text string, terms []string) bool {
	nw := nameWords(text)
	verbs := removalVerbs.find(nw)
	if len(verbs) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range nw {
		have[w] = true
	}
	lower := strings.ToLower(text)
	for _, term := range terms {
		tw := nameWords(term)
		switch {
		case len(tw) == 0:
		case len(tw) == 1:
			if len([]rune(tw[0])) < minWordTerm {
				continue
			}
			for i, w := range nw {
				if w != tw[0] {
					continue
				}
				for _, v := range verbs {
					if i >= v[0]-verbWindow && i < v[1]+verbWindow {
						return true
					}
				}
			}
		default:
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
