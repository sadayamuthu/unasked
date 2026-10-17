package hunk

import (
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

// Diff returns the hunks that turn fromTree into toTree, with IDs assigned,
// plus the number of binary files skipped.
func Diff(r *gitx.Repo, fromTree, toTree string) ([]model.Hunk, int, error) {
	out, err := r.Run("-c", "core.autocrlf=false", "diff-tree", "-p", "-U0", "--no-color",
		"--find-renames", "--no-ext-diff", "--binary", "--src-prefix=a/", "--dst-prefix=b/",
		fromTree, toTree)
	if err != nil {
		return nil, 0, err
	}
	hs, bin, err := Parse(out)
	if err != nil {
		return nil, 0, err
	}
	AssignIDs(hs)
	return hs, bin, nil
}
