package invite

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tellSamples = map[string]string{
	`while (i'm|i am|we're|we are) (here|at it|in there)`: "While I'm here, I also renamed the helper.",
	`clean ?up`:         "Cleanup of the old config loader.",
	`tidy`:              "I'll tidy the imports.",
	`minor`:             "A minor tweak to logging.",
	`unrelated`:         "Fixed an unrelated typo.",
	`while refactoring`: "While refactoring I dropped the cache.",
	`noticed`:           "I noticed the test was flaky and removed it.",
	`took the liberty`:  "I took the liberty of bumping the version.",
	`might as well`:     "Might as well update the README.",
	`for consistency`:   "Renamed for consistency.",
	`small improvement`: "A small improvement to errors.",
	`quick fix`:         "Quick fix for the lint warning.",
	`bonus`:             "Bonus: faster startup.",
}

func TestTellsMatch(t *testing.T) {
	tells, err := LoadTells("")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(strings.NewReader(embeddedTells))
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
		sample, ok := tellSamples[line]
		if !ok {
			t.Errorf("no sample sentence for tell %q", line)
			continue
		}
		if got := tells.Match(sample); got == "" {
			t.Errorf("tell %q did not fire on %q", line, sample)
		}
	}
	if n != len(tellSamples) {
		t.Errorf("tells.txt has %d patterns, samples cover %d", n, len(tellSamples))
	}
	for _, s := range []string{"I'll also update the caller", "", "minority report", "unrelatedness", "the tidyness"} {
		if got := tells.Match(s); got != "" {
			t.Errorf("Match(%q) = %q, want no tell", s, got)
		}
	}
	if got := tells.Match("While I\u2019m here, I renamed it."); got != "While I'm here" {
		t.Errorf("curly apostrophe: Match = %q", got)
	}
	if got := tells.Match("WHILE WE ARE AT IT"); got != "WHILE WE ARE AT IT" {
		t.Errorf("case-insensitive match = %q", got)
	}

	user := filepath.Join(t.TempDir(), "tells.txt")
	if err := os.WriteFile(user, []byte("# mine\n\nsneaked in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ut, err := LoadTells(user)
	if err != nil {
		t.Fatal(err)
	}
	if got := ut.Match("I sneaked in a rename"); got != "sneaked in" {
		t.Errorf("user tell = %q", got)
	}
	if got := ut.Match("a bonus change"); got != "bonus" {
		t.Errorf("embedded tells lost after appending user file: %q", got)
	}
}

func TestLoadTellsMissingAndBadUserFile(t *testing.T) {
	if _, err := LoadTells(filepath.Join(t.TempDir(), "nope.txt")); err != nil {
		t.Errorf("missing user file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "tells.txt")
	if err := os.WriteFile(bad, []byte("(unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// m3: a bad user pattern is skipped with a one-time warning; the other
	// lines still load and nothing fails (the hook must keep running).
	if err := os.WriteFile(bad, []byte("(unclosed\nsneaky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	old := warnOut
	warnOut = &warn
	t.Cleanup(func() { warnOut = old })
	for range 2 {
		ut, err := LoadTells(bad)
		if err != nil {
			t.Fatalf("bad user pattern: %v", err)
		}
		if got := ut.Match("a sneaky change"); got != "sneaky" {
			t.Errorf("good user line lost: %q", got)
		}
	}
	if n := strings.Count(warn.String(), "unasked: warning: "); n != 1 || !strings.Contains(warn.String(), bad+":1") {
		t.Errorf("warnings (want one naming %s:1):\n%s", bad, warn.String())
	}
}
