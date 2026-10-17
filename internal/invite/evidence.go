package invite

import (
	"sort"
	"strings"

	"github.com/kljensen/snowball/english"
	"github.com/sadayamuthu/unasked/internal/model"
)

// genericReason is the reason when the evidence cap alone keeps a high-risk
// hunk from being invited.
const genericReason = "asked: generic overlap only"

// capMargin is how far below Invite the evidence cap holds an asked score.
const capMargin = 0.01

// keywords match a prompt word by stem ("tests" → test) or, for an entry
// ending in "*", by prefix of the lowercased word ("deprecated" → deprecat*).
// An entry with spaces is a phrase of consecutive words, each by stem.
type keywords []string

var (
	removalVerbs = keywords{"remove", "delete", "drop", "deprecat*", "strip", "get rid of", "clean out"}
	testWords    = keywords{"test", "tests", "spec", "assert*", "ci", "workflow", "pipeline", "coverage", "flaky"}
	depWords     = keywords{"depend*", "package", "librar*", "upgrad*", "bump", "install", "version", "module"}
)

// match reports whether any keyword occurs in words (lowercased, unstemmed).
func (ks keywords) match(words []string) bool {
	stems := make([]string, len(words))
	for i, w := range words {
		stems[i] = english.Stem(w, false)
	}
	for _, k := range ks {
		if p, ok := strings.CutSuffix(k, "*"); ok {
			for _, w := range words {
				if strings.HasPrefix(w, p) {
					return true
				}
			}
			continue
		}
		phrase := strings.Fields(k)
		for i := range phrase {
			phrase[i] = english.Stem(phrase[i], false)
		}
		for i := 0; i+len(phrase) <= len(stems); i++ {
			ok := true
			for j, p := range phrase {
				if stems[i+j] != p {
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

// words splits s into lowercased words on any non-letter/digit rune.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), notWordRune)
}

// evidence reports whether prompt text (tokens toks) asks for a high-risk
// hunk specifically (R36). terms are the hunk's specific names (Input.Terms),
// hunkToks its tokens and w the IDF weights.
//
//   - deletion: the prompt has every token of some term; or it has a removal
//     verb and a token of some term weighing more than the median weight of
//     the hunk's distinct tokens.
//   - test: the prompt has a test word (testWords).
//   - dependency: the prompt has a dependency word (depWords) or every token
//     of some term (a dependency name).
func evidence(l model.Label, text string, toks, terms, hunkToks []string, w map[string]float64) bool {
	ws := words(text)
	have := toTokenSet(toks)
	allOf := func() bool {
		for _, term := range terms {
			tt := Tokens(term)
			if len(tt) == 0 {
				continue
			}
			all := true
			for _, t := range tt {
				if !have[t] {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
		return false
	}
	switch l {
	case model.LabelDeletion:
		if allOf() {
			return true
		}
		if !removalVerbs.match(ws) {
			return false
		}
		med := medianWeight(hunkToks, w)
		for _, term := range terms {
			for _, t := range Tokens(term) {
				if have[t] && w[t] > med {
					return true
				}
			}
		}
		return false
	case model.LabelTest:
		return testWords.match(ws)
	case model.LabelDependency:
		return depWords.match(ws) || allOf()
	}
	return true
}

// medianWeight is the median IDF weight of the distinct tokens in toks.
func medianWeight(toks []string, w map[string]float64) float64 {
	var ws []float64
	for t := range toTokenSet(toks) {
		ws = append(ws, w[t])
	}
	if len(ws) == 0 {
		return 0
	}
	sort.Float64s(ws)
	n := len(ws)
	if n%2 == 1 {
		return ws[n/2]
	}
	return (ws[n/2-1] + ws[n/2]) / 2
}
