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
	fillSymbols(r, fromTree, toTree, hs)
	return hs, bin, nil
}

// fillSymbols sets Hunk.Symbols, reading each file's old and new blob once.
// A missing side (added or deleted file) is treated as empty.
func fillSymbols(r *gitx.Repo, fromTree, toTree string, hs []model.Hunk) {
	type pair struct{ old, new []byte }
	cache := map[string]pair{}
	for i := range hs {
		h := &hs[i]
		oldPath := h.File
		if h.OldFile != "" {
			oldPath = h.OldFile
		}
		key := oldPath + "\x00" + h.File
		p, ok := cache[key]
		if !ok {
			if !h.NewFile {
				p.old = blob(r, fromTree, oldPath)
			}
			if !h.DeletedFile {
				p.new = blob(r, toTree, h.File)
			}
			cache[key] = p
		}
		h.Symbols = Symbols(h.File, p.old, p.new, *h)
	}
}

func blob(r *gitx.Repo, tree, p string) []byte {
	out, err := r.Run("cat-file", "blob", tree+":"+p)
	if err != nil {
		return nil
	}
	return out
}
