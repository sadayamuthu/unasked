package adapter

import "strings"

// LastSentences returns the last n sentences of text joined by single spaces.
// Sentences end at ". ", "! ", "? " or a newline; the terminating punctuation
// stays with its sentence. An n of zero or less yields "".
func LastSentences(text string, n int) string {
	if n <= 0 {
		return ""
	}
	var sentences []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			sentences = append(sentences, s)
		}
		cur.Reset()
	}
	rs := []rune(text)
	for i, r := range rs {
		switch {
		case r == '\n' || r == '\r':
			flush()
		case r == ' ' && i > 0 && (rs[i-1] == '.' || rs[i-1] == '!' || rs[i-1] == '?'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	if len(sentences) > n {
		sentences = sentences[len(sentences)-n:]
	}
	return strings.Join(sentences, " ")
}
