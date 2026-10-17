// Package invite scores whether an agent's hunks were asked for by comparing
// word tokens of the user's prompts with tokens of each hunk.
package invite

import (
	"strings"
	"unicode"

	"github.com/kljensen/snowball/english"
	"github.com/sadayamuthu/unasked/internal/model"
)

// stopWords is the standard Snowball English stop-word list (without the
// contraction forms, which never survive tokenization) plus code noise.
var stopWords = toSet(`
i me my myself we our ours ourselves you your yours yourself yourselves
he him his himself she her hers herself it its itself they them their theirs
themselves what which who whom this that these those am is are was were be
been being have has had having do does did doing would should could ought
cannot couldn mustn lets thats whos whats heres theres whens wheres whys hows
a an the and but if or because as until while of at by for with about
against between into through during before after above below to from up down
in out on off over under again further then once here there when where why
how all any both each few more most other some such no nor not only own same
so than too very
func return if else nil null true false var const let def self this new
`)

func toSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		set[w] = true
	}
	return set
}

// Tokens splits s into lowercased, stemmed word tokens. It splits on any
// non-letter/digit rune and on CamelCase boundaries, drops single-character
// tokens and stop words, and keeps duplicates in order.
func Tokens(s string) []string {
	var out []string
	for _, chunk := range strings.FieldsFunc(s, notWordRune) {
		for _, word := range splitCamel(chunk) {
			word = strings.ToLower(word)
			if len([]rune(word)) < 2 || stopWords[word] {
				continue
			}
			out = append(out, english.Stem(word, false))
		}
	}
	return out
}

func notWordRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// splitCamel splits a run of letters/digits at lower-to-upper transitions and
// before the last upper of an acronym run (HTTPServer -> HTTP, Server).
func splitCamel(s string) []string {
	runes := []rune(s)
	var parts []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		boundary := unicode.IsLower(prev) && unicode.IsUpper(cur)
		if unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
			boundary = true
		}
		if boundary {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	return append(parts, string(runes[start:]))
}

// HunkTokens returns the tokens of a hunk's added lines, removed lines, file
// path (without extension) and enclosing symbol, in that order.
func HunkTokens(h model.Hunk) []string {
	var out []string
	for _, line := range h.Added {
		out = append(out, Tokens(line)...)
	}
	for _, line := range h.Removed {
		out = append(out, Tokens(line)...)
	}
	out = append(out, Tokens(stripExt(h.File))...)
	out = append(out, Tokens(h.Symbols.Enclosing)...)
	return out
}

// stripExt removes the extension from the final path segment only. A leading
// dot (".gitignore") is part of the name, not an extension.
func stripExt(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	dir, base := "", p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		dir, base = p[:i+1], p[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	return dir + base
}
