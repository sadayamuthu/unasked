package fixtures

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/pipeline"
	"github.com/sadayamuthu/unasked/internal/report"
)

var update = flag.Bool("update", false, "rewrite each case's expected.json")

// fixtureDir is the repository's testdata/fixtures.
var fixtureDir = filepath.Join("..", "..", "testdata", "fixtures")

// maxFalseAlarmRate is the spec §10 ceiling on listed hunks a human labelled
// invited.
const maxFalseAlarmRate = 0.20

// TestMain re-runs the test binary with UNASKED_HOME, HOME and
// XDG_CONFIG_HOME pointing at an empty temp dir: the adapters read
// UNASKED_HOME in their init functions, so it must be set before the process
// starts, and neither the developer's own agent sessions nor their tells file
// may leak into the fixtures.
func TestMain(m *testing.M) {
	if os.Getenv("UNASKED_TEST_CHILD") == "" {
		home, err := os.MkdirTemp("", "unasked-home")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), "UNASKED_TEST_CHILD=1", "UNASKED_HOME="+home, "HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "APPDATA="+filepath.Join(home, "AppData"))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		os.RemoveAll(home)
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			os.Exit(ee.ExitCode())
		case err != nil:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	code := m.Run()
	cleanupBench()
	os.Exit(code)
}

func loadCases(t testing.TB) []Case {
	t.Helper()
	cases, err := Load(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	perAgent := map[string]int{}
	for _, c := range cases {
		perAgent[c.Agent]++
	}
	for _, a := range []string{"claude-code", "codex"} {
		if perAgent[a] < 3 {
			t.Fatalf("%s has %d fixture cases, want at least 3", a, perAgent[a])
		}
	}
	return cases
}

// TestGates is the release gate (spec §10). It replays every case through
// pipeline.Run with invite.Default, checks expected.json, and fails on any
// miss. The false-alarm ceiling fails too unless UNASKED_RELAX_CEILING=1, in
// which case recall wins and only the measured rate is printed.
func TestGates(t *testing.T) {
	var total Gate
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			rep, truth := RunCase(t, c, invite.Default)
			g := truth.Gate()
			total = total.Add(g)
			for _, h := range truth.Hunks {
				switch {
				case h.Miss():
					t.Errorf("MISS %q: labelled unasked, reported invited (%s)", h.Key, h.Reason)
				case h.FalseAlarm():
					t.Logf("false alarm %q: labelled invited, reported %s (%s)", h.Key, h.Verdict, h.Reason)
				}
			}
			t.Logf("%s", g)
			checkExpected(t, c, rep)
		})
	}
	t.Logf("TOTAL %s (thresholds invite=%.2f low=%.2f)", total, invite.Default.Invite, invite.Default.Low)
	if total.Misses > 0 {
		t.Errorf("recall gate: %d labelled-unasked hunks reported invited, want 0", total.Misses)
	}
	if total.Rate() > maxFalseAlarmRate {
		if os.Getenv("UNASKED_RELAX_CEILING") == "1" {
			t.Logf("false-alarm ceiling relaxed (UNASKED_RELAX_CEILING=1): measured rate %.3f > %.2f — publish this figure", total.Rate(), maxFalseAlarmRate)
		} else {
			t.Errorf("false-alarm ceiling: rate %.3f (%d/%d) > %.2f", total.Rate(), total.FalseAlarms, total.Listed, maxFalseAlarmRate)
		}
	}
}

// TestHighRiskNeverInvited is the R36g property over the fixtures: at
// every threshold pair from Invite 0.05 to 0.95, no high-risk hunk of any
// case is reported invited, before or after keeps.
func TestHighRiskNeverInvited(t *testing.T) {
	for _, c := range loadCases(t) {
		s := Replay(t, c)
		s.Run(t, invite.Default)
		for inv := 5; inv <= 95; inv += 5 {
			th := invite.Thresholds{Invite: float64(inv) / 100, Low: max(float64(inv)/200, 0.05)}
			rep, scored, err := pipeline.Score(s.Repo, s.Selection, th)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			for _, h := range scored {
				if h.HighRisk && h.Verdict == model.VerdictInvited {
					t.Errorf("%s %+v: high-risk %s %s scored invited (%s)", c.Name, th, Key(h.Hunk), h.Label, h.Reason)
				}
			}
			for _, h := range rep.Hunks {
				if h.HighRisk && h.Verdict == string(model.VerdictInvited) {
					t.Errorf("%s %+v: high-risk %s reported invited (%s)", c.Name, th, h.File, h.Reason)
				}
			}
		}
	}
}

func checkExpected(t *testing.T, c Case, rep *report.Report) {
	t.Helper()
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Dir, "expected.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("report differs from %s (run with -update after checking the change):\n%s", path, buf.String())
	}
}

// TestPreexistingWorkExcluded pins the baseline case: the developer's
// uncommitted edits made before the session never reach the report.
func TestPreexistingWorkExcluded(t *testing.T) {
	for _, c := range loadCases(t) {
		if c.Name != "codex/preexisting-work" {
			continue
		}
		rep, _ := RunCase(t, c, invite.Default)
		if rep.Range.BaseSource != "captured" {
			t.Errorf("base source %q, want captured", rep.Range.BaseSource)
		}
		for _, h := range rep.Hunks {
			if h.File == "README.md" {
				t.Errorf("pre-existing README.md edit reported as %s", h.ID)
			}
		}
		if len(rep.Warnings) != 0 {
			t.Errorf("warnings %v", rep.Warnings)
		}
		return
	}
	t.Fatal("codex/preexisting-work not found")
}

func TestKey(t *testing.T) {
	for _, tc := range []struct {
		h    model.Hunk
		want string
	}{
		{model.Hunk{File: "a.go", Added: []string{"", "\tx := 1 "}, Removed: []string{"y"}}, "a.go:x := 1"},
		{model.Hunk{File: "a.go", Added: []string{"  "}, Removed: []string{"", "\tdelete(m, k)"}}, "a.go:delete(m, k)"},
		{model.Hunk{File: "b.txt"}, "b.txt:"},
	} {
		if got := Key(tc.h); got != tc.want {
			t.Errorf("Key = %q, want %q", got, tc.want)
		}
	}
}

func TestTruthGate(t *testing.T) {
	tr := Truth{Hunks: []TruthHunk{
		{Label: Invited, Verdict: model.VerdictInvited},
		{Label: Invited, Verdict: model.VerdictUnclear},                       // false alarm
		{Label: Unasked, Verdict: model.VerdictUnasked},                       // listed
		{Label: Unasked, Verdict: model.VerdictInvited},                       // miss
		{Label: Unasked, Verdict: model.VerdictUnclear},                       // listed
		{Label: Invited, Verdict: model.VerdictUnasked},                       // false alarm
		{Label: Unasked, Verdict: model.VerdictInvited, Reason: "collateral"}, // folded: still a miss
	}}
	got := tr.Gate()
	if want := (Gate{Misses: 2, FalseAlarms: 2, Listed: 4}); got != want {
		t.Fatalf("gate %+v, want %+v", got, want)
	}
	if got.Rate() != 0.5 {
		t.Errorf("rate %v", got.Rate())
	}
}

func TestLoadRejectsBadLabel(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, "codex", "x")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cdir, "transcript.jsonl"), nil, 0o644)
	os.WriteFile(filepath.Join(cdir, "labels.json"), []byte(`{"a.go:x": "unclear"}`), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted label \"unclear\"")
	}
}

// m11: a case with labels but no transcript is a broken case, not one to
// skip silently (its labels would never be checked).
func TestLoadRejectsCaseWithoutTranscript(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, "codex", "x")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "labels.json"), []byte(`{"a.go:x": "unasked"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "transcript.jsonl") {
		t.Fatalf("Load err = %v, want a missing transcript.jsonl error", err)
	}
}
