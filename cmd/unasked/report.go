package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/pipeline"
	"github.com/sadayamuthu/unasked/internal/report"
)

// options are the flags of the report, --drop, --patch and --keep forms.
type options struct {
	req             adapter.Request
	format          string
	all, noColor    bool
	strict, lenient bool
	drop, patch     bool
	keep            string
	keepSet         bool
	reason          string
	ids             []string
}

// thresholds returns the effective thresholds and whether a flag set them.
func (o *options) thresholds() (invite.Thresholds, bool) {
	switch {
	case o.strict:
		return invite.Default.Shift(invite.Step), true
	case o.lenient:
		return invite.Default.Shift(-invite.Step), true
	}
	return invite.Default, false
}

func parseOptions(args []string) (*options, error) {
	o := &options{format: "text"}
	var since, staged bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			o.ids = append(o.ids, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		var err error
		switch name {
		case "--since":
			since = true
			o.req.Mode = adapter.RangeSince
			o.req.Since, err = value()
		case "--session":
			o.req.SessionID, err = value()
		case "--agent":
			o.req.Agent, err = value()
		case "--format":
			o.format, err = value()
		case "--keep":
			o.keepSet = true
			o.keep, err = value()
		case "--reason":
			o.reason, err = value()
		default:
			if hasVal {
				return nil, fmt.Errorf("%s takes no value", name)
			}
			switch name {
			case "--staged":
				staged = true
				o.req.Mode = adapter.RangeStaged
			case "--all-sessions":
				o.req.AllSessions = true
			case "--all":
				o.all = true
			case "--strict":
				o.strict = true
			case "--lenient":
				o.lenient = true
			case "--no-color":
				o.noColor = true
			case "--drop":
				o.drop = true
			case "--patch":
				o.patch = true
			default:
				return nil, fmt.Errorf("unknown flag %s (see unasked --help)", name)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	switch {
	case since && staged:
		return nil, errors.New("--since and --staged cannot be combined")
	case o.strict && o.lenient:
		return nil, errors.New("--strict and --lenient cannot be combined")
	case o.drop && o.patch:
		return nil, errors.New("--drop and --patch cannot be combined")
	case o.keepSet && (o.drop || o.patch):
		return nil, errors.New("--keep cannot be combined with --drop or --patch")
	case (o.drop || o.patch) && staged:
		return nil, errors.New("--drop and --patch change the working tree, not the index: run them without --staged")
	case o.keepSet && len(o.ids) > 0:
		return nil, fmt.Errorf("--keep takes one id; unexpected %q", o.ids[0])
	case !o.drop && !o.patch && len(o.ids) > 0:
		return nil, fmt.Errorf("unknown command %q (see unasked --help)", o.ids[0])
	case o.reason != "" && !o.keepSet:
		return nil, errors.New("--reason goes with --keep")
	}
	switch o.format {
	case "text", "md", "json":
	default:
		return nil, fmt.Errorf("unknown format %q: use text, md or json", o.format)
	}
	return o, nil
}

// runMain handles everything that is not a subcommand: the report, --drop,
// --patch and --keep.
func runMain(args []string, dir string, stdin io.Reader, stdout io.Writer) (int, error) {
	o, err := parseOptions(args)
	if err != nil {
		return 0, err
	}
	r, err := gitx.Open(dir)
	if err != nil {
		return 0, err
	}
	switch {
	case o.keepSet:
		return runKeep(r, o, stdin, stdout)
	case o.drop || o.patch:
		return runDrop(r, o, stdin, stdout)
	}
	th, _ := o.thresholds()
	rep, _, _, err := pipeline.Run(r, o.req, th)
	if err != nil {
		return 0, err
	}
	if err := report.SaveLastFile(r, lastFile(stdin), rep); err != nil {
		return 0, fmt.Errorf("save last report: %w", err)
	}
	switch o.format {
	case "json":
		err = report.WriteJSON(stdout, rep)
	case "md":
		err = report.WriteMarkdown(stdout, rep)
	default:
		color := !o.noColor && getenv("NO_COLOR") == "" && isTerminal(stdout)
		err = report.WriteText(stdout, rep, o.all, color)
	}
	if err != nil {
		return 0, err
	}
	return rep.ExitCode(), nil
}
