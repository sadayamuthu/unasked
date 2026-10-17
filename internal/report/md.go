package report

import (
	"bufio"
	"fmt"
	"io"

	"github.com/sadayamuthu/unasked/internal/model"
)

func mdHunk(w io.Writer, h HunkJSON) {
	fmt.Fprintf(w, "- `%s` `%s` **%s** %s\n", h.ID, h.location(), h.Label, h.blurb())
	if showReason(h) {
		fmt.Fprintf(w, "  - %s\n", oneLine(h.Reason))
	}
	if h.Statement != "" {
		var tell string
		if h.Tell != nil {
			tell = *h.Tell
		}
		fmt.Fprintf(w, "  - agent said: \"%s\"\n", markTell(oneLine(h.Statement), tell, "_", "_"))
	}
}

// WriteMarkdown writes the report as a PR-comment-sized markdown document.
func WriteMarkdown(w io.Writer, r *Report) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "### unasked: %s\n\n", r.Result)
	fmt.Fprintf(bw, "%s  \n%s\n", r.sessionLine(), r.transcriptLine())
	for _, wn := range r.Warnings {
		fmt.Fprintf(bw, "\n> warning: %s\n", oneLine(wn))
	}
	if lines := r.firstPrompt(72); len(lines) > 0 {
		fmt.Fprintf(bw, "\n> You asked: \"%s\"\n", joinLines(lines))
	}
	fmt.Fprintf(bw, "\n%s\n", r.countsLine())

	unasked := r.byVerdict(model.VerdictUnasked)
	unclear := r.byVerdict(model.VerdictUnclear)
	if len(unasked) > 0 {
		fmt.Fprint(bw, "\n#### UNASKED\n\n")
		for _, h := range unasked {
			mdHunk(bw, h)
		}
	}
	if len(unclear) > 0 {
		fmt.Fprintf(bw, "\n<details>\n<summary>UNCLEAR (%d)</summary>\n\n", len(unclear))
		for _, h := range unclear {
			mdHunk(bw, h)
		}
		fmt.Fprint(bw, "\n</details>\n")
	}
	if len(r.collateralHunks()) > 0 {
		fmt.Fprint(bw, "\n<details>\n<summary>COLLATERAL (not counted)</summary>\n\n")
		fmt.Fprintf(bw, "%s\n\n</details>\n", r.collateralLine()[2:])
	}
	if len(r.Kept) > 0 {
		fmt.Fprint(bw, "\n#### KEPT\n\n")
		for _, k := range r.Kept {
			fmt.Fprintf(bw, "- `%s` %s (by %s)\n", k.Hash, oneLine(k.Reason), k.By)
		}
	}
	if d := droppable(unasked); len(d) > 0 {
		fmt.Fprintf(bw, "\n```\nunasked --drop %s\n```\n", idList(d))
	}
	return bw.Flush()
}

func joinLines(ls []string) string {
	out := ""
	for i, l := range ls {
		if i > 0 {
			out += " "
		}
		out += l
	}
	return out
}
