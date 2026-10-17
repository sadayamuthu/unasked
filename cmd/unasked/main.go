// Command unasked classifies an AI agent's diff hunks as invited or unasked.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/sadayamuthu/unasked/internal/adapter/claudecode" // registers the adapter
	_ "github.com/sadayamuthu/unasked/internal/adapter/codex"      // registers the adapter
	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/revert"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: unasked [flags]                 report on the latest agent session
       unasked --drop [ids…]           revert unasked hunks (all unasked if no ids)
       unasked --patch [ids…]          print the reverse patch instead
       unasked --keep <id> [--reason "…"]
       unasked undo [--list] [--force] [snapshot]
       unasked hook install|uninstall [--agent name] [--global|--project]
       unasked hook status [--agent name]
       unasked doctor | stats [--since 30d] | version
flags: --since <ref>  --staged  --session <id>  --agent <name>  --all-sessions
       --all  --format text|md|json  --strict  --lenient  --no-color
`

// getenv and stdinIsTTY are the process environment as the command sees it;
// tests replace them.
var (
	getenv     = os.Getenv
	stdinIsTTY = func(r io.Reader) bool { return isTerminal(r) }
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "unasked:", err)
		os.Exit(model.ExitError)
	}
	os.Exit(run(context.Background(), os.Args[1:], dir, os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, dir string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintln(stderr, "unasked: internal error:", p)
			code = model.ExitError
		}
	}()
	var err error
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version":
		return runVersion(stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return model.ExitClean
	case "undo":
		code, err = runUndo(args[1:], dir, stdout)
	case "doctor":
		code, err = runDoctor(args[1:], dir, stdout)
	case "stats":
		code, err = runStats(args[1:], dir, stdout)
	case "hook":
		code, err = runHook(args[1:], dir, stdin, stdout, stderr)
	default:
		code, err = runMain(args, dir, stdin, stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, "unasked: "+describe(err))
		return model.ExitError
	}
	return code
}

func runVersion(stdout io.Writer) int {
	gv := "unknown"
	if out, err := gitx.RunGlobal("version"); err == nil {
		gv = strings.TrimPrefix(strings.TrimSpace(string(out)), "git version ")
	}
	fmt.Fprintf(stdout, "unasked %s (git %s)\n", version, gv)
	return model.ExitClean
}

// describe turns an error into the one-line message printed after
// "unasked: ", naming the fix for the errors a user can act on.
func describe(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, gitx.ErrNotRepo):
		msg = "not a git repository: run unasked inside a git working tree"
	case errors.Is(err, baseline.ErrUnmerged):
		msg += ": finish or abort the merge or rebase, then re-run unasked"
	case errors.Is(err, baseline.ErrNoCommits):
		msg += ": make a first commit, then re-run unasked"
	case errors.Is(err, revert.ErrLocked):
		msg += "; if no other unasked is running, delete the lock file"
	case errors.Is(err, revert.ErrNothingScored):
		msg += ", for example: unasked --drop h01 h02"
	case errors.Is(err, revert.ErrChangedSinceDrop) && !strings.Contains(msg, "--force"):
		msg += "; run `unasked undo --force` to overwrite them"
	}
	return strings.Join(strings.Fields(msg), " ")
}

// isTerminal reports whether v is an *os.File attached to a character device.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok || f == nil {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
