package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gittest"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/report"
)

// TestMain re-runs the test binary with UNASKED_HOME (and HOME) pointing at a
// temp dir: the adapters read UNASKED_HOME in their init functions, which
// run before TestMain, so the variable must be set before the process starts.
func TestMain(m *testing.M) {
	if os.Getenv("UNASKED_TEST_CHILD") == "" {
		home, err := os.MkdirTemp("", "unasked-home")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), "UNASKED_TEST_CHILD=1", "UNASKED_HOME="+home, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
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
	os.Exit(m.Run())
}

// cli runs the command in-process against one test repo.
type cli struct {
	t   *testing.T
	g   *gittest.Repo
	env map[string]string
	tty bool // stdin is a terminal (a person typing)
}

func (c *cli) run(args ...string) (int, string, string) {
	c.t.Helper()
	oldEnv, oldTTY := getenv, stdinIsTTY
	getenv = func(k string) string { return c.env[k] }
	stdinIsTTY = func(io.Reader) bool { return c.tty }
	defer func() { getenv, stdinIsTTY = oldEnv, oldTTY }()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, c.g.Dir, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func (c *cli) want(code int, args ...string) string {
	c.t.Helper()
	got, out, errs := c.run(args...)
	if got != code {
		c.t.Fatalf("unasked %s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), got, code, out, errs)
	}
	return out
}

const retry = "\t// retry the fetch with exponential backoff\n\tfor attempt := 0; attempt < 3; attempt++ {\n\t\tif err := get(url); err == nil {\n\t\t\treturn nil\n\t\t}\n\t\tbackoff(attempt)\n\t}\n\treturn get(url)\n"

// setup commits fetch.go and colors.txt and writes a Claude Code session
// under $UNASKED_HOME. The agent adds a retry loop (asked for) and, unless
// clean, deletes "green" from colors.txt (not asked for; high-risk).
func setup(t *testing.T, clean bool) *cli {
	t.Helper()
	g := gittest.New(t)
	g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n\treturn get(url)\n}\n")
	g.Write("colors.txt", "red\ngreen\nblue\n")
	g.Commit("init")
	g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n"+retry+"}\n")

	cwd, _ := json.Marshal(g.Dir)
	ts := time.Now().UTC().Format(time.RFC3339)
	edit := func(id, file, oldS, newS, text string) string {
		f, _ := json.Marshal(filepath.Join(g.Dir, file))
		o, _ := json.Marshal(oldS)
		n, _ := json.Marshal(newS)
		x, _ := json.Marshal(text)
		return fmt.Sprintf(`{"type":"assistant","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Edit","input":{"file_path":%s,"old_string":%s,"new_string":%s}}]}}`, cwd, ts, x, id, f, o, n)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","cwd":%s,"version":"2.1.289","timestamp":%q,"message":{"role":"user","content":"add a retry loop with exponential backoff to the fetch function"}}`, cwd, ts),
		edit("t1", "fetch.go", "\treturn get(url)\n", retry, "I will add the retry loop with exponential backoff to fetch."),
	}
	if !clean {
		g.Write("colors.txt", "red\nblue\n")
		lines = append(lines, edit("t2", "colors.txt", "red\ngreen\n", "red\n", "While I'm here, I removed green from the colors list."))
	}
	id := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	p := filepath.Join(os.Getenv("UNASKED_HOME"), ".claude", "projects", id, id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(p)) })
	return &cli{t: t, g: g, env: map[string]string{}, tty: true}
}

func jsonReport(t *testing.T, s string) *report.Report {
	t.Helper()
	var r report.Report
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return &r
}

func hunkID(t *testing.T, r *report.Report, file string) string {
	t.Helper()
	for _, h := range r.Hunks {
		if h.File == file {
			return h.ID
		}
	}
	t.Fatalf("no hunk in %s: %+v", file, r.Hunks)
	return ""
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"version"}, t.TempDir(), strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "unasked dev") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, t.TempDir(), strings.NewReader(""), &out, &errb)
	if code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}

func TestHelpPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, t.TempDir(), strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != usage {
		t.Errorf("help = %q", out.String())
	}
	if !strings.HasPrefix(usage, "usage: unasked [flags]                 report on the latest agent session\n") ||
		!strings.Contains(usage, "       --all  --format text|md|json  --strict  --lenient  --no-color\n") {
		t.Errorf("usage text drifted:\n%s", usage)
	}
}

func TestReportExitCodes(t *testing.T) {
	c := setup(t, true)
	out := c.want(0)
	if !strings.Contains(out, "invited") {
		t.Errorf("clean report:\n%s", out)
	}

	c = setup(t, false)
	out = c.want(2)
	if !strings.Contains(out, "UNASKED") || !strings.Contains(out, "colors.txt") {
		t.Errorf("unasked report:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("colour on a non-terminal:\n%q", out)
	}
	r, err := gitx.Open(c.g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	last, err := report.LoadLast(r)
	if err != nil || last.Result != report.ResultUnasked {
		t.Fatalf("last.json %v, %v", last, err)
	}

	// A developer edit with no agent edit behind it is unclear: exit 1.
	c = setup(t, true)
	c.g.Write("fetch.go", "package main\n\nfunc fetch(url string) error {\n"+retry+"}\n\nfunc get(string) error { return nil }\n")
	c.want(1)
}

func TestReportJSONFormat(t *testing.T) {
	c := setup(t, false)
	r := jsonReport(t, c.want(2, "--format", "json"))
	if r.Version != 1 || r.Result != report.ResultUnasked || r.Agent != "claude-code" || r.Transcript != "present" {
		t.Fatalf("report %+v", r)
	}
	if r.Thresholds != invite.Default {
		t.Errorf("thresholds %+v", r.Thresholds)
	}
	md := c.want(2, "--format=md")
	if !strings.Contains(md, "unasked --drop") {
		t.Errorf("markdown:\n%s", md)
	}
	if code, _, errs := c.run("--format", "xml"); code != 3 || !strings.HasPrefix(errs, "unasked: ") {
		t.Errorf("bad format: exit %d %q", code, errs)
	}
}

func TestStrictLenientShiftThresholds(t *testing.T) {
	c := setup(t, false)
	strict := jsonReport(t, c.want(2, "--format", "json", "--strict"))
	if want := invite.Default.Shift(invite.Step); strict.Thresholds != want {
		t.Errorf("strict %+v, want %+v", strict.Thresholds, want)
	}
	lenient := jsonReport(t, c.want(2, "--format", "json", "--lenient"))
	if want := invite.Default.Shift(-invite.Step); lenient.Thresholds != want {
		t.Errorf("lenient %+v, want %+v", lenient.Thresholds, want)
	}
	if code, _, _ := c.run("--strict", "--lenient"); code != 3 {
		t.Errorf("--strict --lenient: exit %d", code)
	}
}

func TestDropThenUndoCLI(t *testing.T) {
	c := setup(t, false)
	r := jsonReport(t, c.want(2, "--format", "json"))
	id := hunkID(t, r, "colors.txt")
	fetch := c.g.Read("fetch.go")

	out := c.want(0, "--drop")
	if !regexp.MustCompile(`^dropped ` + id + ` \(snapshot \S+; undo with: unasked undo\)\n$`).MatchString(out) {
		t.Errorf("drop output %q", out)
	}
	if got := c.g.Read("colors.txt"); got != "red\ngreen\nblue\n" {
		t.Errorf("colors.txt after drop %q", got)
	}
	if c.g.Read("fetch.go") != fetch {
		t.Error("fetch.go changed by the drop")
	}
	c.want(0) // only the invited hunk is left

	list := c.want(0, "undo", "--list")
	if !strings.Contains(list, "colors.txt") {
		t.Errorf("undo --list:\n%s", list)
	}
	if out := c.want(0, "undo"); !strings.HasPrefix(out, "undone ") {
		t.Errorf("undo output %q", out)
	}
	if got := c.g.Read("colors.txt"); got != "red\nblue\n" {
		t.Errorf("colors.txt after undo %q", got)
	}
	if code, _, errs := c.run("undo"); code != 3 || !strings.Contains(errs, "no drop to undo") {
		t.Errorf("second undo: exit %d %q", code, errs)
	}
}

func TestDropByIDAndStale(t *testing.T) {
	c := setup(t, false)
	r := jsonReport(t, c.want(2, "--format", "json"))
	id := hunkID(t, r, "colors.txt")
	c.g.Write("colors.txt", "red\nteal\nblue\n") // the hunk no longer matches
	code, _, errs := c.run("--drop", id)
	if code != 3 || !strings.Contains(errs, "re-run unasked") {
		t.Errorf("stale drop: exit %d %q", code, errs)
	}
	if code, _, errs := c.run("--drop", "h99"); code != 3 || !strings.Contains(errs, "unknown hunk id") {
		t.Errorf("unknown id: exit %d %q", code, errs)
	}
}

func TestPatchPrintsReversePatch(t *testing.T) {
	c := setup(t, false)
	c.want(2)
	patch := c.want(0, "--patch")
	if c.g.Read("colors.txt") != "red\nblue\n" {
		t.Fatal("--patch changed the working tree")
	}
	if !strings.Contains(patch, "+green\n") {
		t.Errorf("patch does not restore the deleted line:\n%s", patch)
	}
	// Applying the printed patch forward has the same effect as --drop.
	cmd := exec.Command("git", "apply", "--unidiff-zero")
	cmd.Dir, cmd.Stdin = c.g.Dir, strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\n%s", err, out, patch)
	}
	if got := c.g.Read("colors.txt"); got != "red\ngreen\nblue\n" {
		t.Errorf("after applying the patch: %q", got)
	}
}

func TestReversePatchShapes(t *testing.T) {
	in := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -2,1 +2,2 @@\n-old\n+new\n+x\n\\ No newline at end of file\n" +
		"diff --git a/n b/n\nnew file mode 100644\n--- /dev/null\n+++ b/n\n@@ -0,0 +1,1 @@\n+hi\n"
	want := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -2,2 +2,1 @@\n-new\n-x\n\\ No newline at end of file\n+old\n" +
		"diff --git a/n b/n\ndeleted file mode 100644\n--- a/n\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-hi\n"
	if got := string(reversePatch([]byte(in))); got != want {
		t.Errorf("reversePatch:\n%s\nwant:\n%s", got, want)
	}
}

// File-level sections (R41) reverse too: a rename swaps its names, a mode
// change its modes.
func TestReversePatchFileLevel(t *testing.T) {
	in := "diff --git a/old name b/new name\nsimilarity index 100%\nrename from old name\nrename to new name\n" +
		"diff --git a/m.sh b/m.sh\nold mode 100644\nnew mode 100755\n" +
		"diff --git \"a/\\303\\251\" \"b/x\"\nrename from \"\\303\\251\"\nrename to x\n"
	want := "diff --git a/new name b/old name\nsimilarity index 100%\nrename from new name\nrename to old name\n" +
		"diff --git a/m.sh b/m.sh\nold mode 100755\nnew mode 100644\n" +
		"diff --git a/x \"b/\\303\\251\"\nrename from x\nrename to \"\\303\\251\"\n"
	if got := string(reversePatch([]byte(in))); got != want {
		t.Errorf("reversePatch:\n%s\nwant:\n%s", got, want)
	}
}

func TestKeepUserThenClean(t *testing.T) {
	c := setup(t, false)
	r := jsonReport(t, c.want(2, "--format", "json"))
	id := hunkID(t, r, "colors.txt")
	out := c.want(0, "--keep", id, "--reason", "green is retired")
	if !strings.Contains(out, "kept "+id) || !strings.Contains(out, "user") {
		t.Errorf("keep output %q", out)
	}
	r = jsonReport(t, c.want(0, "--format", "json"))
	if r.Result != report.ResultClean || len(r.Kept) != 1 || r.Kept[0].By != "user" || r.Kept[0].Reason != "green is retired" {
		t.Errorf("after keep: %+v", r)
	}
	// A no-id drop never touches a user-kept hunk.
	if out := c.want(0, "--drop"); !strings.Contains(out, "nothing to drop") {
		t.Errorf("drop after keep %q", out)
	}
	if c.g.Read("colors.txt") != "red\nblue\n" {
		t.Error("kept hunk was dropped")
	}
}

func TestAgentKeepHighRiskRefused(t *testing.T) {
	c := setup(t, false)
	c.env["CLAUDECODE"] = "1"
	r := jsonReport(t, c.want(2, "--format", "json"))
	id := hunkID(t, r, "colors.txt")
	code, _, errs := c.run("--keep", id, "--reason", "fine")
	if code != 3 || !strings.Contains(errs, "agents cannot keep deletions, test/CI or dependency changes") {
		t.Fatalf("agent keep: exit %d %q", code, errs)
	}
	c.want(2) // nothing was kept

	// Not a terminal on stdin also counts as an agent.
	c.env = map[string]string{}
	c.tty = false
	if code, _, _ := c.run("--keep", id); code != 3 {
		t.Errorf("non-tty keep: exit %d", code)
	}
}

func TestDoctorListsAdapters(t *testing.T) {
	c := setup(t, false)
	out := c.want(0, "doctor")
	for _, a := range adapter.All() {
		if !strings.Contains(out, a.Name()) || !strings.Contains(out, a.MaxLevel().String()) {
			t.Errorf("doctor misses %s:\n%s", a.Name(), out)
		}
	}
	if !strings.Contains(out, "claude-code") || !strings.Contains(out, "codex") {
		t.Errorf("doctor:\n%s", out)
	}
	// doctor also works outside a repository.
	var o, e bytes.Buffer
	if code := run(context.Background(), []string{"doctor"}, t.TempDir(), strings.NewReader(""), &o, &e); code != 0 {
		t.Errorf("doctor outside a repo: exit %d %q", code, e.String())
	}
}

func TestStats(t *testing.T) {
	c := setup(t, false)
	if out := c.want(0, "stats"); !strings.Contains(out, "no captured sessions yet") {
		t.Errorf("empty stats %q", out)
	}
	r, err := gitx.Open(c.g.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rep := jsonReport(t, c.want(2, "--format", "json"))
	for _, s := range []string{"a", "b"} {
		if _, err := adapter.RecordStart(r, "claude-code", s, c.g.Dir); err != nil {
			t.Fatal(err)
		}
		dir, err := adapter.CaptureDir(r, "claude-code", s)
		if err != nil {
			t.Fatal(err)
		}
		if s == "b" {
			rep.Hunks, rep.Result, rep.Stats.Unasked = rep.Hunks[:0], report.ResultClean, 0
			for _, h := range jsonReport(t, c.want(2, "--format", "json")).Hunks {
				if h.Verdict == "invited" {
					rep.Hunks = append(rep.Hunks, h)
				}
			}
		}
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := c.want(0, "stats")
	for _, s := range []string{"sessions: 2", "1 with unasked hunks", "deletion"} {
		if !strings.Contains(out, s) {
			t.Errorf("stats missing %q:\n%s", s, out)
		}
	}
	if code, _, _ := c.run("stats", "--since", "bogus"); code != 3 {
		t.Errorf("bad --since: exit %d", code)
	}
}

func TestErrorsExit3(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{nil, {"--drop"}, {"--keep", "h01"}, {"undo"}} {
		var out, errb bytes.Buffer
		code := run(context.Background(), args, dir, strings.NewReader(""), &out, &errb)
		if code != 3 {
			t.Errorf("%v outside a repo: exit %d", args, code)
		}
		if lines := strings.Split(strings.TrimSuffix(errb.String(), "\n"), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "unasked: ") || !strings.Contains(lines[0], "not a git repository") {
			t.Errorf("%v stderr %q", args, errb.String())
		}
	}
	c := setup(t, false)
	for _, args := range [][]string{{"--staged", "--since", "HEAD"}, {"--since"}, {"--since", "nope"}, {"--keep"}, {"--keep", "h01", "h02"}, {"h01"}, {"--drop", "--staged"}} {
		if code, _, errs := c.run(args...); code != 3 || !strings.HasPrefix(errs, "unasked: ") {
			t.Errorf("%v: exit %d %q", args, code, errs)
		}
	}
	if code, _, errs := c.run("--keep", "h01"); code != 3 || !strings.Contains(errs, "run unasked first") {
		t.Errorf("keep before any report: exit %d %q", code, errs)
	}
	// An unmerged index names the fix.
	c.g.Git("checkout", "-q", "-b", "side")
	c.g.Write("colors.txt", "side\n")
	c.g.Commit("side")
	c.g.Git("checkout", "-q", "main")
	c.g.Write("colors.txt", "main\n")
	c.g.Commit("main")
	cmd := exec.Command("git", "merge", "side")
	cmd.Dir = c.g.Dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	_ = cmd.Run() // conflicts on purpose
	if code, _, errs := c.run(); code != 3 || !strings.Contains(errs, "conflicts") {
		t.Errorf("unmerged: exit %d %q", code, errs)
	}
}

func TestHookCLI(t *testing.T) {
	c := setup(t, false)
	home := t.TempDir()
	old := hookHome
	hookHome = func() string { return home }
	t.Cleanup(func() { hookHome = old })
	path := filepath.Join(home, ".claude", "settings.json")

	if out := c.want(0, "hook", "install", "--agent", "claude-code"); out != "installed claude-code hook in "+path+"\n" {
		t.Errorf("install: %q", out)
	}
	if out := c.want(0, "hook", "install", "--agent", "claude-code"); out != "already installed claude-code hook in "+path+"\n" {
		t.Errorf("reinstall: %q", out)
	}
	if out := c.want(0, "hook", "status"); !strings.Contains(out, "claude-code  installed  "+path) || !strings.Contains(out, "codex  not installed") {
		t.Errorf("status:\n%s", out)
	}
	if out := c.want(0, "doctor"); !regexp.MustCompile(`(?m)^claude-code .* user `).MatchString(out) {
		t.Errorf("doctor hook column:\n%s", out)
	}
	out := c.want(0, "hook", "install", "--agent", "codex", "--project")
	codexPath := filepath.Join(c.g.Dir, ".codex", "hooks.json")
	if !strings.Contains(out, "installed codex hook in "+codexPath) || !strings.Contains(out, "Codex: open /hooks in Codex once to trust the unasked hook.") {
		t.Errorf("codex install: %q", out)
	}
	if out := c.want(0, "hook", "install", "--agent", "codex", "--project"); strings.Contains(out, "Codex: open /hooks") {
		t.Errorf("trust note printed for an unchanged config: %q", out)
	}
	if out := c.want(0, "doctor"); !regexp.MustCompile(`(?m)^codex .* project `).MatchString(out) {
		t.Errorf("doctor project hook:\n%s", out)
	}
	if out := c.want(0, "hook", "uninstall", "--agent", "claude-code"); out != "removed claude-code hook from "+path+"\n" {
		t.Errorf("uninstall: %q", out)
	}
	if out := c.want(0, "hook", "uninstall", "--agent", "claude-code"); out != "no claude-code hook in "+path+"\n" {
		t.Errorf("second uninstall: %q", out)
	}
	if _, _, errs := c.run("hook", "install", "--agent", "nope"); !strings.Contains(errs, "unknown agent") {
		t.Errorf("unknown agent: %q", errs)
	}
	if code, _, errs := c.run("hook", "install", "--global", "--project"); code != 3 || errs == "" {
		t.Errorf("both scopes: exit %d %q", code, errs)
	}
}

func TestHookInstallDetected(t *testing.T) {
	setup(t, false) // creates $UNASKED_HOME/.claude/projects, so claude-code is detected
	home := os.Getenv("UNASKED_HOME")
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(home, ".claude", "settings.json"))
		os.RemoveAll(filepath.Join(home, ".codex", "hooks.json"))
	})
	c := &cli{t: t, g: gittest.New(t), env: map[string]string{}, tty: true}
	out := c.want(0, "hook", "install")
	if !strings.Contains(out, "installed claude-code hook in "+filepath.Join(home, ".claude", "settings.json")) {
		t.Errorf("install all:\n%s", out)
	}
}

func TestHookRunCLINeverFails(t *testing.T) {
	c := setup(t, false)
	for _, args := range [][]string{{"hook", "run"}, {"hook", "run", "--agent", "claude-code"}, {"hook", "run", "--agent", "zzz"}} {
		var o, e bytes.Buffer
		code := run(context.Background(), args, c.g.Dir, strings.NewReader("garbage"), &o, &e)
		if code != 0 || o.Len() != 0 {
			t.Errorf("%v: exit %d stdout %q", args, code, o.String())
		}
	}
}

func TestStartDirHookRunWithoutCwd(t *testing.T) {
	gone := func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	if d, err := startDir([]string{"hook", "run", "--agent", "codex"}, gone); err != nil || d != "" {
		t.Errorf("hook run: %q, %v", d, err)
	}
	if _, err := startDir([]string{"doctor"}, gone); err == nil {
		t.Error("doctor without a cwd: no error")
	}
	ok := func() (string, error) { return "/w", nil }
	if d, err := startDir([]string{"doctor"}, ok); err != nil || d != "/w" {
		t.Errorf("doctor: %q, %v", d, err)
	}
}

// withFiles commits extra files under the session's agent edit, which stays
// uncommitted.
func (c *cli) withFiles(files map[string]string) {
	c.t.Helper()
	fetch := c.g.Read("fetch.go")
	c.g.Git("checkout", "--", "fetch.go")
	for p, s := range files {
		c.g.Write(p, s)
	}
	c.g.Commit("more")
	c.g.Write("fetch.go", fetch)
}

// TestFileLevelChangesReported is the C3 probe: a renamed-away test and a
// deleted empty file have no content lines, and used to report clean.
func TestFileLevelChangesReported(t *testing.T) {
	c := setup(t, true)
	c.withFiles(map[string]string{"parser_test.go": "package main\n\nfunc TestParse(t *testing.T) {}\n", ".keep": ""})
	c.g.Git("mv", "parser_test.go", "parser_test.go.disabled")
	c.g.Git("rm", "-q", ".keep")
	r := jsonReport(t, c.want(1, "--format", "json"))
	got := map[string]report.HunkJSON{}
	for _, h := range r.Hunks {
		got[h.File] = h
	}
	if h := got["parser_test.go.disabled"]; h.FileChange != "rename" || h.OldFile != "parser_test.go" || h.Label != "test" || h.Verdict != "unclear" || !h.HighRisk {
		t.Errorf("rename hunk %+v", h)
	}
	if h := got[".keep"]; h.FileChange != "delete" || h.Label != "deletion" || h.Verdict != "unclear" {
		t.Errorf("delete hunk %+v", h)
	}
	text := c.want(1)
	if !strings.Contains(text, "renamed parser_test.go to parser_test.go.disabled") {
		t.Errorf("text report:\n%s", text)
	}
}

// Dropping a rename's file-level hunk renames the file back; undo renames
// it again. Both are byte-exact.
func TestDropFileLevelRenameThenUndo(t *testing.T) {
	c := setup(t, true)
	body := "package main\n\nfunc TestParse(t *testing.T) {}\n"
	c.withFiles(map[string]string{"parser_test.go": body, "run.sh": "#!/bin/sh\necho hi\n"})
	if err := os.Rename(filepath.Join(c.g.Dir, "parser_test.go"), filepath.Join(c.g.Dir, "parser_test.go.disabled")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(c.g.Dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := jsonReport(t, c.want(1, "--format", "json"))
	id := hunkID(t, r, "parser_test.go.disabled")
	c.want(0, "--drop", id)
	if _, err := os.Stat(filepath.Join(c.g.Dir, "parser_test.go.disabled")); !os.IsNotExist(err) {
		t.Errorf("renamed file still there: %v", err)
	}
	if got := c.g.Read("parser_test.go"); got != body {
		t.Errorf("parser_test.go after drop %q", got)
	}
	c.want(0, "undo")
	if _, err := os.Stat(filepath.Join(c.g.Dir, "parser_test.go")); !os.IsNotExist(err) {
		t.Errorf("old name back after undo: %v", err)
	}
	if got := c.g.Read("parser_test.go.disabled"); got != body {
		t.Errorf("parser_test.go.disabled after undo %q", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// The mode change is its own hunk and drops alone.
	r = jsonReport(t, c.want(1, "--format", "json"))
	c.want(0, "--drop", hunkID(t, r, "run.sh"))
	if st, err := os.Stat(filepath.Join(c.g.Dir, "run.sh")); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("run.sh mode after drop: %v %v", st.Mode(), err)
	}
	c.want(0, "undo")
	if st, err := os.Stat(filepath.Join(c.g.Dir, "run.sh")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode after undo: %v %v", st.Mode(), err)
	}
}
