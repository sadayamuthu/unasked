package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/hook"
	"github.com/sadayamuthu/unasked/internal/model"
)

// hookHome is the home directory hook configs live under: UNASKED_HOME (as
// the adapters use it) or the user's home. Tests replace it.
var hookHome = func() string {
	if h := os.Getenv("UNASKED_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

type hookOptions struct {
	agent   string
	project bool
}

// runHook dispatches `unasked hook install|uninstall|status|run`.
func runHook(args []string, dir string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("hook: want install, uninstall, status or run")
	}
	sub, rest := args[0], args[1:]
	if sub == "run" {
		// Called by the agent: never fail it, whatever the arguments.
		agent := ""
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--agent" && i+1 < len(rest) {
				agent = rest[i+1]
				i++
			}
		}
		return hook.Run(agent, stdin, stdout, stderr, time.Now()), nil
	}
	o, err := parseHookOptions(rest)
	if err != nil {
		return 0, err
	}
	switch sub {
	case "install":
		return hookInstall(o, dir, stdout)
	case "uninstall":
		return hookUninstall(o, dir, stdout)
	case "status":
		return hookStatusCmd(o, dir, stdout)
	}
	return 0, fmt.Errorf("hook: unknown subcommand %q", sub)
}

func parseHookOptions(args []string) (*hookOptions, error) {
	o := &hookOptions{}
	global := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				return nil, errors.New("hook: --agent needs a name")
			}
			i++
			o.agent = args[i]
		case "--project":
			o.project = true
		case "--global":
			global = true
		default:
			return nil, fmt.Errorf("hook: unexpected %q", args[i])
		}
	}
	if global && o.project {
		return nil, errors.New("hook: use --global or --project, not both")
	}
	if o.agent != "" {
		if _, ok := hook.Get(o.agent); !ok {
			return nil, fmt.Errorf("hook: unknown agent %q (supported: claude-code, codex)", o.agent)
		}
	}
	return o, nil
}

// specs returns the named agent's spec, or every spec when none is named.
func (o *hookOptions) specs() []hook.Spec {
	if o.agent == "" {
		return hook.Specs()
	}
	s, _ := hook.Get(o.agent)
	return []hook.Spec{s}
}

// repoDir is the repository top level, required for --project.
func repoDir(dir string, required bool) (string, error) {
	r, err := gitx.Open(dir)
	if err != nil {
		if required {
			return "", err
		}
		return "", nil
	}
	return r.Dir, nil
}

// hookInstall installs the named agent's hook, or with no --agent every
// agent detected on this machine.
func hookInstall(o *hookOptions, dir string, stdout io.Writer) (int, error) {
	repo, err := repoDir(dir, o.project)
	if err != nil {
		return 0, err
	}
	bin, err := hook.Bin()
	if err != nil {
		return 0, fmt.Errorf("hook: locate the unasked binary: %w", err)
	}
	var specs []hook.Spec
	for _, s := range o.specs() {
		if o.agent == "" {
			if a, ok := adapter.Get(s.Agent); !ok || !a.Detect() {
				continue
			}
		}
		specs = append(specs, s)
	}
	if len(specs) == 0 {
		fmt.Fprintln(stdout, "no supported agent detected; name one with --agent claude-code or --agent codex")
		return model.ExitClean, nil
	}
	for _, s := range specs {
		path, changed, err := hook.Install(s, hookHome(), repo, o.project, bin)
		if err != nil {
			return 0, fmt.Errorf("hook: %s: %w", s.Agent, err)
		}
		if changed {
			fmt.Fprintf(stdout, "installed %s hook in %s\n", s.Agent, path)
		} else {
			fmt.Fprintf(stdout, "already installed %s hook in %s\n", s.Agent, path)
		}
		if changed && s.TrustNote != "" {
			fmt.Fprintln(stdout, s.TrustNote)
		}
	}
	return model.ExitClean, nil
}

func hookUninstall(o *hookOptions, dir string, stdout io.Writer) (int, error) {
	repo, err := repoDir(dir, o.project)
	if err != nil {
		return 0, err
	}
	for _, s := range o.specs() {
		path, changed, err := hook.Uninstall(s, hookHome(), repo, o.project)
		if err != nil {
			return 0, fmt.Errorf("hook: %s: %w", s.Agent, err)
		}
		if changed {
			fmt.Fprintf(stdout, "removed %s hook from %s\n", s.Agent, path)
		} else {
			fmt.Fprintf(stdout, "no %s hook in %s\n", s.Agent, path)
		}
	}
	return model.ExitClean, nil
}

func hookStatusCmd(o *hookOptions, dir string, stdout io.Writer) (int, error) {
	if o.project {
		return 0, errors.New("hook status: checks both scopes; drop --project")
	}
	repo, _ := repoDir(dir, false)
	for _, s := range o.specs() {
		if ok, path := hook.Status(s, hookHome(), repo); ok {
			fmt.Fprintf(stdout, "%s  installed  %s\n", s.Agent, path)
		} else {
			fmt.Fprintf(stdout, "%s  not installed\n", s.Agent)
		}
	}
	return model.ExitClean, nil
}

// hookStatus is doctor's HOOK column: "user" or "project" (where unasked's
// hook is installed), "no", or "-" for agents unasked has no hook for.
func hookStatus(name, repo string) string {
	s, ok := hook.Get(name)
	if !ok {
		return "-"
	}
	installed, path := hook.Status(s, hookHome(), repo)
	switch {
	case !installed:
		return "no"
	case repo != "" && path == s.ConfigPath(hookHome(), repo, true):
		return "project"
	}
	return "user"
}
