package report

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

const (
	ulOn  = "\x1b[4m"
	ulOff = "\x1b[24m"
)

// markTell wraps the tell phrase inside statement with on/off markers.
func markTell(statement, tell, on, off string) string {
	if tell == "" {
		return statement
	}
	low := strings.ToLower(statement)
	i := strings.Index(low, strings.ToLower(tell))
	if i < 0 || len(low) != len(statement) {
		return statement
	}
	return statement[:i] + on + statement[i:i+len(tell)] + off + statement[i+len(tell):]
}

func hunkLine(h HunkJSON) string {
	return fmt.Sprintf("  %s  %s   %s  %s", h.ID, h.location(), h.Label, h.blurb())
}

func writeHunkText(w io.Writer, h HunkJSON, color bool) {
	fmt.Fprintln(w, hunkLine(h))
	if h.Verdict == string(model.VerdictUnclear) && h.Reason != "" && h.Reason != h.Summary {
		fmt.Fprintf(w, "        %s\n", h.Reason)
	}
	if h.Statement != "" {
		on, off := "_", "_"
		if color {
			on, off = ulOn, ulOff
		}
		var tell string
		if h.Tell != nil {
			tell = *h.Tell
		}
		fmt.Fprintf(w, "        agent said: \"%s\"\n", markTell(oneLine(h.Statement), tell, on, off))
	}
}

func idList(hs []HunkJSON) string {
	ids := make([]string, len(hs))
	for i, h := range hs {
		ids[i] = h.ID
	}
	return strings.Join(ids, " ")
}

func (r *Report) collateralLine() string {
	hs := r.collateralHunks()
	files := map[string]bool{}
	for _, h := range hs {
		files[h.File] = true
	}
	return fmt.Sprintf("  %d hunks in %d files", len(hs), len(files))
}

// WriteText writes the human report. all adds the INVITED section; color
// underlines the agent's tell phrase with ANSI.
func WriteText(w io.Writer, r *Report, all bool, color bool) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, r.sessionLine())
	fmt.Fprintln(bw, r.transcriptLine())
	for _, wn := range r.Warnings {
		fmt.Fprintf(bw, "warning: %s\n", wn)
	}
	if lines := r.firstPrompt(72); len(lines) > 0 {
		for i, l := range lines {
			switch {
			case i == 0 && len(lines) == 1:
				fmt.Fprintf(bw, "You asked: \"%s\"\n", l)
			case i == 0:
				fmt.Fprintf(bw, "You asked: \"%s\n", l)
			case i == len(lines)-1:
				fmt.Fprintf(bw, "           %s\"\n", l)
			}
		}
	}
	fmt.Fprintln(bw)
	fmt.Fprintln(bw, r.countsLine())
	fmt.Fprintf(bw, "result: %s\n", r.Result)

	unasked := r.byVerdict(model.VerdictUnasked)
	unclear := r.byVerdict(model.VerdictUnclear)
	if len(unasked) > 0 {
		fmt.Fprintln(bw, "\nUNASKED")
		for _, h := range unasked {
			writeHunkText(bw, h, color)
		}
	}
	if len(unclear) > 0 {
		fmt.Fprintln(bw, "\nUNCLEAR")
		for _, h := range unclear {
			writeHunkText(bw, h, color)
		}
	}
	if all {
		if inv := r.byVerdict(model.VerdictInvited); len(inv) > 0 {
			fmt.Fprintln(bw, "\nINVITED")
			for _, h := range inv {
				fmt.Fprintln(bw, strings.TrimRight(fmt.Sprintf("  %s  %s   %s  %s", h.ID, h.location(), h.Label, oneLine(h.Summary)), " "))
				if h.Reason != "" {
					fmt.Fprintf(bw, "        why: %s\n", h.Reason)
				}
			}
		}
	}
	if len(r.collateralHunks()) > 0 {
		fmt.Fprintln(bw, "\nCOLLATERAL (not counted)")
		fmt.Fprintln(bw, r.collateralLine())
	}
	if len(r.Kept) > 0 {
		fmt.Fprintln(bw, "\nKEPT")
		for _, k := range r.Kept {
			fmt.Fprintf(bw, "  %s  %s (by %s)\n", k.Hash, k.Reason, k.By)
		}
	}
	first := ""
	switch {
	case len(unasked) > 0:
		first = unasked[0].ID
	case len(unclear) > 0:
		first = unclear[0].ID
	}
	if first != "" {
		fmt.Fprintln(bw)
		if len(unasked) > 0 {
			fmt.Fprintf(bw, "unasked --drop %s   revert the unasked hunks\n", idList(unasked))
		}
		fmt.Fprintf(bw, "unasked --keep %s       accept it on this branch\n", first)
	}
	return bw.Flush()
}
