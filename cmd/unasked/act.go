package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/keep"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/pipeline"
	"github.com/sadayamuthu/unasked/internal/report"
	"github.com/sadayamuthu/unasked/internal/revert"
)

var errNoReport = errors.New("no report yet: run unasked first")

// loadLast reads last.json; a missing file is (nil, nil).
func loadLast(r *gitx.Repo) (*report.Report, error) {
	last, err := report.LoadLast(r)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return last, err
}

// runKeep stores a keep for one hunk of the last report. An agent (agent
// marker in the environment, or stdin not a terminal) may not keep a
// high-risk hunk.
func runKeep(r *gitx.Repo, o *options, stdin io.Reader, stdout io.Writer) (int, error) {
	if o.keep == "" {
		return 0, errors.New("--keep needs a hunk id")
	}
	last, err := loadLast(r)
	if err != nil {
		return 0, err
	}
	if last == nil {
		return 0, errNoReport
	}
	hashes, err := last.Resolve([]string{o.keep})
	if err != nil {
		return 0, err
	}
	var h *report.HunkJSON
	for i := range last.Hunks {
		if last.Hunks[i].Hash == hashes[0] {
			h = &last.Hunks[i]
			break
		}
	}
	if h == nil {
		return 0, fmt.Errorf("hunk %s is not in the last report: re-run unasked", o.keep)
	}
	by := keep.DetectBy(getenv, stdinIsTTY(stdin))
	if by == keep.ByAgent {
		if err := keep.CheckAgentKeep(model.Scored{HighRisk: h.HighRisk}); err != nil {
			return 0, fmt.Errorf("%w: drop it (unasked --drop %s) or leave it for the user", err, h.ID)
		}
	}
	branch, err := pipeline.Branch(r)
	if err != nil {
		return 0, err
	}
	e := keep.Entry{Hash: h.Hash, Branch: branch, Reason: o.reason, By: by, At: time.Now().UTC()}
	if err := keep.Add(r, e); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, "kept %s (%s) on %s by %s\n", h.ID, h.Hash, branch, by)
	return model.ExitClean, nil
}

// runDrop reverts hunks (--drop) or prints the patch that would (--patch).
// Ids resolve against last.json; with no ids every unasked hunk of the last
// report is taken. The drop is verified by rescoring the same range.
func runDrop(r *gitx.Repo, o *options, stdout io.Writer) (int, error) {
	last, err := loadLast(r)
	if err != nil {
		return 0, err
	}
	th, set := o.thresholds()
	if !set && last != nil && last.Thresholds.Invite > 0 {
		th = last.Thresholds // judge with the thresholds of the report the ids came from
	}
	rep, scored, sel, err := pipeline.Run(r, o.req, th)
	if err != nil {
		return 0, err
	}
	if last == nil {
		last = rep
	}
	explicit := len(o.ids) > 0
	var hashes []string
	if explicit {
		if hashes, err = last.Resolve(o.ids); err != nil {
			return 0, err
		}
	}
	plan, err := revert.Prepare(r, last, scored, hashes, explicit)
	if err != nil {
		return 0, err
	}
	if len(plan.Hunks) == 0 {
		fmt.Fprintln(stdout, "nothing to drop")
		return model.ExitClean, nil
	}
	if o.patch {
		_, err := stdout.Write(reversePatch(plan.Patch))
		return model.ExitClean, err
	}
	var after *report.Report
	id, err := revert.Drop(r, plan, func() ([]model.Scored, error) {
		a, s, err := pipeline.Rescore(r, sel, th)
		after = a
		return s, err
	})
	if err != nil {
		return 0, err
	}
	// The display ids of last.json no longer match the tree; replace it with
	// the verified post-drop report. Failing to save loses nothing the drop
	// did, so it is not an error.
	if after != nil {
		_ = report.SaveLast(r, after)
	}
	display := map[string]string{}
	for _, h := range last.Hunks {
		display[h.Hash] = h.ID
	}
	ids := make([]string, 0, len(plan.Hunks))
	for _, h := range plan.Hunks {
		if d := display[h.Hash]; d != "" {
			ids = append(ids, d)
		} else {
			ids = append(ids, h.ID)
		}
	}
	fmt.Fprintf(stdout, "dropped %s (snapshot %s; undo with: unasked undo)\n", strings.Join(ids, " "), id)
	return model.ExitClean, nil
}

// runUndo restores a drop snapshot, or lists them with --list.
func runUndo(args []string, dir string, stdout io.Writer) (int, error) {
	var list, force bool
	id := ""
	for _, a := range args {
		switch {
		case a == "--list":
			list = true
		case a == "--force":
			force = true
		case strings.HasPrefix(a, "-"):
			return 0, fmt.Errorf("undo: unknown flag %s", a)
		case id != "":
			return 0, fmt.Errorf("undo: one snapshot at a time; unexpected %q", a)
		default:
			id = a
		}
	}
	r, err := gitx.Open(dir)
	if err != nil {
		return 0, err
	}
	snaps, err := revert.ListSnapshots(r)
	if err != nil {
		return 0, err
	}
	if list {
		if len(snaps) == 0 {
			fmt.Fprintln(stdout, "no snapshots")
			return model.ExitClean, nil
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SNAPSHOT\tCREATED\tHUNKS\tSTATE\tFILES")
		for _, s := range snaps {
			state := "ready"
			switch {
			case s.Undone:
				state = "undone"
			case !s.Complete:
				state = "incomplete"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.Created.Local().Format("2006-01-02 15:04"), len(s.Hunks), state, strings.Join(s.Files, ", "))
		}
		return model.ExitClean, tw.Flush()
	}
	if id == "" {
		for _, s := range snaps {
			if !s.Undone {
				id = s.ID
				break
			}
		}
		if id == "" {
			return 0, revert.ErrNoSnapshot
		}
	}
	if err := revert.Undo(r, id, force); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, "undone (snapshot %s)\n", id)
	return model.ExitClean, nil
}

// reversePatch turns the plan's patch, which Drop applies with -R, into the
// patch that reverts the hunks when applied forward with
// `git apply --unidiff-zero`: sides swapped in the headers and @@ lines, and
// in each hunk the lines to remove written before the lines to add. Hunk
// bodies are consumed by the @@ line counts, so content lines that look like
// headers ("--- x" for a removed "-- x") are never mistaken for them.
func reversePatch(p []byte) []byte {
	var out bytes.Buffer
	lines := bytes.SplitAfter(p, []byte("\n"))
	var hdr map[string]string // the section's rename and mode values, swapped
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if v, ok := swappedHeader(l, hdr); ok {
			out.WriteString(v)
			continue
		}
		switch {
		case bytes.HasPrefix(l, []byte("diff --git ")):
			hdr = sectionHeader(lines[i+1:])
			from, to := hdr["rename from "], hdr["rename to "] // already swapped
			if from != "" && to != "" {
				out.WriteString("diff --git " + withSide(from, 'a') + " " + withSide(to, 'b') + "\n")
			} else {
				out.Write(l)
			}
		case len(l) == 0:
		case bytes.HasPrefix(l, []byte("@@ ")):
			oldN, newN, ok := hunkCounts(l)
			if !ok {
				out.Write(l)
				continue
			}
			out.Write(swapRanges(l))
			var minus, plus [][]byte // the reversed body: lines to remove, lines to add
			var last *[][]byte
		body:
			for i+1 < len(lines) && len(lines[i+1]) > 0 {
				n := lines[i+1]
				switch {
				case n[0] == '-' && oldN > 0:
					oldN--
					plus = append(plus, append([]byte{'+'}, n[1:]...))
					last = &plus
				case n[0] == '+' && newN > 0:
					newN--
					minus = append(minus, append([]byte{'-'}, n[1:]...))
					last = &minus
				case n[0] == '\\' && last != nil:
					*last = append(*last, n)
					last = nil
				default:
					break body
				}
				i++
			}
			for _, b := range minus {
				out.Write(b)
			}
			for _, b := range plus {
				out.Write(b)
			}
		case bytes.HasPrefix(l, []byte("--- ")) && i+1 < len(lines) && bytes.HasPrefix(lines[i+1], []byte("+++ ")):
			out.WriteString("--- " + swapSide(string(lines[i+1][4:]), 'a'))
			out.WriteString("+++ " + swapSide(string(l[4:]), 'b'))
			i++
		case bytes.HasPrefix(l, []byte("new file mode ")):
			out.WriteString("deleted file mode " + string(l[len("new file mode "):]))
		case bytes.HasPrefix(l, []byte("deleted file mode ")):
			out.WriteString("new file mode " + string(l[len("deleted file mode "):]))
		default:
			out.Write(l)
		}
	}
	return out.Bytes()
}

// headerKeys are the extended header lines whose values a reversal swaps
// in pairs: rename from/to and old/new mode.
var headerKeys = [][2]string{{"rename from ", "rename to "}, {"old mode ", "new mode "}}

// sectionHeader reads the extended header lines of one file section (up to
// its ---, @@ or next diff --git line) and returns, for each paired key
// present on both sides, the value the reversed line carries.
func sectionHeader(lines [][]byte) map[string]string {
	vals := map[string]string{}
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("diff --git ")) || bytes.HasPrefix(l, []byte("--- ")) || bytes.HasPrefix(l, []byte("@@ ")) {
			break
		}
		for _, k := range headerKeys {
			for _, key := range k {
				if v, ok := bytes.CutPrefix(l, []byte(key)); ok {
					vals[key] = strings.TrimRight(string(v), "\r\n")
				}
			}
		}
	}
	out := map[string]string{}
	for _, k := range headerKeys {
		if a, b := vals[k[0]], vals[k[1]]; a != "" && b != "" {
			out[k[0]], out[k[1]] = b, a
		}
	}
	return out
}

// swappedHeader rewrites a rename or mode line with its swapped value.
func swappedHeader(l []byte, hdr map[string]string) (string, bool) {
	for key, v := range hdr {
		if bytes.HasPrefix(l, []byte(key)) {
			return key + v + "\n", true
		}
	}
	return "", false
}

// withSide prefixes a header path as git prints it (maybe C-quoted) with
// the side's "a/" or "b/".
func withSide(p string, side byte) string {
	if strings.HasPrefix(p, `"`) {
		return `"` + string(side) + "/" + p[1:]
	}
	return string(side) + "/" + p
}

var atRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// hunkCounts returns the old and new line counts of an @@ line.
func hunkCounts(l []byte) (oldN, newN int, ok bool) {
	m := atRe.FindSubmatch(bytes.TrimRight(l, "\r\n"))
	if m == nil {
		return 0, 0, false
	}
	count := func(b []byte) int {
		if len(b) == 0 {
			return 1
		}
		n, _ := strconv.Atoi(string(b))
		return n
	}
	return count(m[2]), count(m[4]), true
}

// swapRanges swaps the old and new ranges of an @@ line.
func swapRanges(l []byte) []byte {
	m := atRe.FindSubmatch(bytes.TrimRight(l, "\r\n"))
	side := func(start, n []byte) string {
		if len(n) == 0 {
			return string(start)
		}
		return string(start) + "," + string(n)
	}
	return []byte(fmt.Sprintf("@@ -%s +%s @@%s\n", side(m[3], m[4]), side(m[1], m[2]), m[5]))
}

// swapSide rewrites a ---/+++ path ("a/x", "b/x", quoted, or /dev/null) to
// carry the given side prefix.
func swapSide(path string, side byte) string {
	if strings.HasPrefix(path, "/dev/null") {
		return path
	}
	q := 0
	if strings.HasPrefix(path, `"`) {
		q = 1
	}
	if len(path) > q+1 && (path[q] == 'a' || path[q] == 'b') && path[q+1] == '/' {
		return path[:q] + string(side) + path[q+1:]
	}
	return path
}
