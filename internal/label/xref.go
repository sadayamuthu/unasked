package label

import (
	"errors"
	"strings"

	"github.com/sadayamuthu/unasked/internal/gitx"
)

const (
	xrefFileCap   = 2000 // tracked files beyond this are not searched
	xrefBatch     = 200  // pathspecs per git grep when over the cap
	xrefMaxTerms  = 5    // symbols/literals searched per hunk
	xrefMinLength = 3    // shorter terms match too much to mean anything
)

// countXRefs counts distinct tracked files other than file that contain any
// of terms as a fixed string. Errors count as no references.
func countXRefs(r *gitx.Repo, file string, terms []string) int {
	var long []string
	for _, t := range terms {
		if len(t) >= xrefMinLength {
			long = append(long, t)
		}
	}
	if len(long) > xrefMaxTerms {
		long = long[:xrefMaxTerms]
	}
	terms = long
	var paths []string
	over := false
	if out, err := r.Run("ls-files", "-z"); err == nil {
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				paths = append(paths, p)
			}
		}
		over = len(paths) > xrefFileCap
	}
	found := map[string]bool{}
	for _, t := range terms {
		if len(t) < xrefMinLength {
			continue
		}
		if !over {
			grep(r, found, t, file, []string{".", ":(exclude,literal)" + file})
			continue
		}
		var batch []string
		for _, p := range paths[:xrefFileCap] {
			if p == file {
				continue
			}
			batch = append(batch, ":(literal)"+p)
			if len(batch) == xrefBatch {
				grep(r, found, t, file, batch)
				batch = nil
			}
		}
		if len(batch) > 0 {
			grep(r, found, t, file, batch)
		}
	}
	return len(found)
}

func grep(r *gitx.Repo, found map[string]bool, term, file string, pathspec []string) {
	args := append([]string{"grep", "-F", "-I", "-l", "-z", "-e", term, "--"}, pathspec...)
	out, err := r.Run(args...)
	if err != nil {
		var ge *gitx.Error
		if !errors.As(err, &ge) || ge.Code != 1 { // 1 means no matches
			return
		}
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && p != file {
			found[p] = true
		}
	}
}
