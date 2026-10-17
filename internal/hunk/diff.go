package hunk

import (
	"bytes"
	"strconv"
	"strings"

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

// fillSymbols sets Hunk.Symbols, reading every file's old and new blob with
// one git process per tree. A missing side (added or deleted file) is
// treated as empty.
func fillSymbols(r *gitx.Repo, fromTree, toTree string, hs []model.Hunk) {
	var oldPaths, newPaths []string
	for _, h := range hs {
		if !h.NewFile {
			oldPaths = append(oldPaths, oldPath(h))
		}
		if !h.DeletedFile {
			newPaths = append(newPaths, h.File)
		}
	}
	oldBlobs, newBlobs := blobs(r, fromTree, oldPaths), blobs(r, toTree, newPaths)
	for i := range hs {
		h := &hs[i]
		var oldSrc, newSrc []byte
		if !h.NewFile {
			oldSrc = oldBlobs[oldPath(*h)]
		}
		if !h.DeletedFile {
			newSrc = newBlobs[h.File]
		}
		h.Symbols = Symbols(h.File, oldSrc, newSrc, *h)
	}
}

func oldPath(h model.Hunk) string {
	if h.OldFile != "" {
		return h.OldFile
	}
	return h.File
}

// blobs returns the content of each path in tree that is a blob, read with
// one `git cat-file --batch`; missing paths and non-blobs are absent. A path
// the line-based batch protocol cannot carry (CR or LF in the name), or any
// answer the batch does not give cleanly, falls back to one cat-file each.
func blobs(r *gitx.Repo, tree string, paths []string) map[string][]byte {
	out := make(map[string][]byte, len(paths))
	var batch []string
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if strings.ContainsAny(p, "\r\n") {
			if b := blob(r, tree, p); b != nil {
				out[p] = b
			}
			continue
		}
		batch = append(batch, p)
	}
	if len(batch) == 0 {
		return out
	}
	var in strings.Builder
	for _, p := range batch {
		in.WriteString(tree + ":" + p + "\n")
	}
	data, err := r.RunEnv(nil, strings.NewReader(in.String()), "cat-file", "--batch")
	done := 0
	for err == nil && done < len(batch) {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		line := string(data[:nl])
		data = data[nl+1:]
		if strings.HasSuffix(line, " missing") || strings.HasSuffix(line, " ambiguous") {
			done++
			continue
		}
		header := strings.Fields(line) // "<sha> <type> <size>"
		if len(header) != 3 {
			break
		}
		size, perr := strconv.Atoi(header[2])
		if perr != nil || size < 0 || len(data) < size+1 {
			break
		}
		if header[1] == "blob" {
			out[batch[done]] = data[:size:size]
		}
		data = data[size+1:]
		done++
	}
	for _, p := range batch[done:] {
		if b := blob(r, tree, p); b != nil {
			out[p] = b
		}
	}
	return out
}

func blob(r *gitx.Repo, tree, p string) []byte {
	out, err := r.Run("cat-file", "blob", tree+":"+p)
	if err != nil {
		return nil
	}
	return out
}
