package parser

import "strings"

// ParseList splits a comma-separated list, trimming spaces around items.
func ParseList(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, ',')
		if i < 0 {
			break
		}
		out = append(out, strings.TrimSpace(s[:i]))
		s = s[i+1:]
	}
	return out
}
