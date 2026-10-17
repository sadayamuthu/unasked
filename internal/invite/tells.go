package invite

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// warnOut receives warnings about the user's tells file; tests replace it.
var warnOut io.Writer = os.Stderr

// warned holds the warnings already written, so each is written once per
// process.
var warned sync.Map

func warnOnce(msg string) {
	if _, dup := warned.LoadOrStore(msg, true); !dup {
		fmt.Fprintln(warnOut, "unasked: warning: "+msg)
	}
}

// LoadTells compiles the embedded tells.txt plus, appended after it, the
// patterns in userFile. Each non-blank line not starting with '#' is a Go
// regexp, matched case-insensitively at word boundaries. An empty userFile
// or a missing file is not an error. The user's file never fails the run (a
// broken tells file must not stop reports or disable the hook): a bad
// pattern is skipped, and an unreadable file ignored, with a one-time
// warning on stderr. Only a bad embedded pattern is an error.
func LoadTells(userFile string) (*Tells, error) {
	t := &Tells{}
	if err := t.add("tells.txt (embedded)", embeddedTells, false); err != nil {
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
		warnOnce(fmt.Sprintf("tells: %v; your tells are ignored", err))
		return t, nil
	}
	_ = t.add(userFile, string(b), true)
	return t, nil
}

// add compiles each pattern line of text. With skipBad a bad pattern is
// skipped with a warning instead of failing.
func (t *Tells) add(name, text string, skipBad bool) error {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(`(?i)\b(?:` + line + `)\b`)
		if err != nil {
			err = fmt.Errorf("tells: %s:%d: %w", name, i+1, err)
			if !skipBad {
				return err
			}
			warnOnce(err.Error() + "; line skipped")
			continue
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
