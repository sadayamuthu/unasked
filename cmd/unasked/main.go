// Command unasked classifies an AI agent's diff hunks as invited or unasked.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: unasked <command> [arguments]

commands:
  version
        print version and git version
`

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
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return model.ExitError
	}
	switch args[0] {
	case "version":
		return runVersion(stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return model.ExitClean
	}
	fmt.Fprintf(stderr, "unasked: unknown command %q\n\n%s", args[0], usage)
	return model.ExitError
}

func runVersion(stdout io.Writer) int {
	gv := "unknown"
	if out, err := gitx.RunGlobal("version"); err == nil {
		gv = strings.TrimPrefix(strings.TrimSpace(string(out)), "git version ")
	}
	fmt.Fprintf(stdout, "unasked %s (git %s)\n", version, gv)
	return model.ExitClean
}
