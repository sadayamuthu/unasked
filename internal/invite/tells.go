package invite

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed tells.txt
var embeddedTells string

// Tells is the compiled set of tell phrases: wording with which an agent
// admits an edit was incidental ("while I'm here", "minor", "bonus").
type Tells struct {
	res []*regexp.Regexp
}

// UserTellsFile is the user's extra tells file,
// os.UserConfigDir()/unasked/tells.txt, or "" if there is no config dir.
func UserTellsFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "unasked", "tells.txt")
}

// LoadTells compiles the embedded tells.txt plus, appended after it, the
// patterns in userFile. Each non-blank line not starting with '#' is a Go
// regexp, matched case-insensitively at word boundaries. An empty userFile
// or a missing file is not an error; an unreadable file or a bad pattern is.
func LoadTells(userFile string) (*Tells, error) {
	t := &Tells{}
	if err := t.add("tells.txt (embedded)", embeddedTells); err != nil {
		return nil, err
	}
	if userFile == "" {
		return t, nil
	}
	b, err := os.ReadFile(userFile)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tells: %w", err)
	}
	if err := t.add(userFile, string(b)); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tells) add(name, text string) error {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(`(?i)\b(?:` + line + `)\b`)
		if err != nil {
			return fmt.Errorf("tells: %s:%d: %w", name, i+1, err)
		}
		t.res = append(t.res, re)
	}
	return nil
}

// quotes folds typographic quotes to ASCII so "I’m" matches "i'm".
var quotes = strings.NewReplacer("\u2019", "'", "\u2018", "'", "\u201c", `"`, "\u201d", `"`)

// Match returns the text of the first tell phrase found in statement (with
// curly quotes folded to ASCII), in file order, or "" if none fires. A nil
// *Tells matches nothing.
func (t *Tells) Match(statement string) string {
	if t == nil {
		return ""
	}
	statement = quotes.Replace(statement)
	for _, re := range t.res {
		if m := re.FindString(statement); m != "" {
			return m
		}
	}
	return ""
}
