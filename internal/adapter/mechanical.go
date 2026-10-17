package adapter

import "regexp"

var mechanicalRE = regexp.MustCompile(`\b(gofmt|goimports|gofumpt|prettier|eslint\s+.*--fix|black|ruff\s+(format|check\s+.*--fix)|rustfmt|cargo\s+fmt|go\s+generate|go\s+mod\s+tidy|npm\s+(i|install|ci)|yarn(\s+install)?|pnpm\s+(i|install)|pip\s+install|sed\s+-i)\b`)

// IsMechanical reports whether a shell command is a formatter, generator or
// dependency installer whose file changes are mechanical rather than authored.
func IsMechanical(command string) bool {
	return mechanicalRE.MatchString(command)
}
