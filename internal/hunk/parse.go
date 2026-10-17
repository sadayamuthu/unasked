// Package hunk parses zero-context git diffs into content-addressed hunks.
package hunk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

// HashOf is the frozen hunk identity: the first 12 hex chars of
// sha256(path NUL removed-lines NUL added-lines), lines joined by "\n".
func HashOf(path string, removed, added []string) string {
	s := path + "\x00" + strings.Join(removed, "\n") + "\x00" + strings.Join(added, "\n")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// AssignIDs orders hunks by file then new start and numbers them h01..h99, h100...
func AssignIDs(h []model.Hunk) {
	sort.SliceStable(h, func(i, j int) bool {
		if h[i].File != h[j].File {
			return h[i].File < h[j].File
		}
		return h[i].NewStart < h[j].NewStart
	})
	for i := range h {
		h[i].ID = fmt.Sprintf("h%02d", i+1)
	}
}

// fileState is the per-file header being accumulated.
type fileState struct {
	header                    []byte // diff --git ... through the +++ line
	oldPath, newPath          string // from ---/+++ lines ("" for /dev/null)
	renameFrom, renameTo      string
	isNew, isDeleted, hasPlus bool
	binary                    bool
	plusRaw                   string // raw (possibly quoted) "b/<new>" from the +++ line
	inHunks                   bool
}

// Parse reads a `git diff -U0` stream. It returns the hunks and the number of
// binary files that were skipped. Lines keep their raw bytes (a trailing \r
// stays) except for the final \n.
func Parse(diff []byte) ([]model.Hunk, int, error) {
	var (
		hunks   []model.Hunk
		binary  int
		fs      *fileState
		cur     *model.Hunk
		curBuf  []byte
		lastTag byte
	)
	flush := func() {
		if cur != nil {
			hdr := fs.header
			if fs.renameFrom != "" {
				hdr = modifyHeader(fs.plusRaw)
			}
			cur.Patch = append(append([]byte{}, hdr...), curBuf...)
			cur.Hash = HashOf(cur.File, cur.Removed, cur.Added)
			hunks = append(hunks, *cur)
			cur, curBuf = nil, nil
		}
	}
	endFile := func() {
		flush()
		if fs != nil && fs.binary {
			binary++
		}
		fs = nil
	}

	for pos := 0; pos < len(diff); {
		end := bytes.IndexByte(diff[pos:], '\n')
		var raw []byte
		if end < 0 {
			raw = append(append([]byte{}, diff[pos:]...), '\n')
			pos = len(diff)
		} else {
			raw = diff[pos : pos+end+1]
			pos += end + 1
		}
		line := string(raw[:len(raw)-1])

		if strings.HasPrefix(line, "diff --git ") {
			// Content lines always start with + - space or \, so this is a new file.
			endFile()
			fs = &fileState{header: append([]byte{}, raw...)}
			continue
		}
		if fs == nil {
			continue
		}
		if fs.binary {
			continue
		}
		if !fs.inHunks {
			switch {
			case strings.HasPrefix(line, "GIT binary patch"), strings.HasPrefix(line, "Binary files "):
				fs.binary = true
			case strings.HasPrefix(line, "--- "):
				fs.oldPath = headerPath(line[4:], "a/")
				fs.header = append(fs.header, raw...)
			case strings.HasPrefix(line, "+++ "):
				fs.newPath = headerPath(line[4:], "b/")
				fs.plusRaw = strings.TrimSuffix(line[4:], "\t")
				fs.hasPlus = true
				fs.inHunks = true
				fs.header = append(fs.header, raw...)
			default:
				switch {
				case strings.HasPrefix(line, "new file mode "):
					fs.isNew = true
				case strings.HasPrefix(line, "deleted file mode "):
					fs.isDeleted = true
				case strings.HasPrefix(line, "rename from "):
					fs.renameFrom = unquote(line[len("rename from "):])
				case strings.HasPrefix(line, "rename to "):
					fs.renameTo = unquote(line[len("rename to "):])
				}
				fs.header = append(fs.header, raw...)
			}
			continue
		}
		// Inside the hunk section.
		switch {
		case strings.HasPrefix(line, "@@ "):
			flush()
			h, err := parseHeader(line)
			if err != nil {
				return nil, 0, err
			}
			h.File = fs.newPath
			if fs.isDeleted || h.File == "" {
				h.File = fs.oldPath
			}
			if fs.renameFrom != "" && fs.renameFrom != h.File {
				h.OldFile = fs.renameFrom
			}
			h.NewFile, h.DeletedFile = fs.isNew, fs.isDeleted
			cur, curBuf, lastTag = &h, append([]byte{}, raw...), 0
		case cur == nil:
			// stray line between +++ and first @@
		case len(line) > 0 && line[0] == '+':
			cur.Added = append(cur.Added, line[1:])
			curBuf = append(curBuf, raw...)
			lastTag = '+'
		case len(line) > 0 && line[0] == '-':
			cur.Removed = append(cur.Removed, line[1:])
			curBuf = append(curBuf, raw...)
			lastTag = '-'
		case len(line) > 0 && line[0] == '\\':
			if lastTag == '-' {
				cur.NoNewlineOld = true
			} else if lastTag == '+' {
				cur.NoNewlineNew = true
			}
			curBuf = append(curBuf, raw...)
		}
	}
	endFile()
	return hunks, binary, nil
}

// parseHeader parses "@@ -a[,b] +c[,d] @@ ...".
func parseHeader(line string) (model.Hunk, error) {
	var h model.Hunk
	rest := strings.TrimPrefix(line, "@@ -")
	i := strings.Index(rest, " @@")
	if i < 0 {
		return h, fmt.Errorf("bad hunk header %q", line)
	}
	oldPart, newPart, ok := strings.Cut(rest[:i], " +")
	if !ok {
		return h, fmt.Errorf("bad hunk header %q", line)
	}
	var err error
	if h.OldStart, h.OldLines, err = parseRange(oldPart); err != nil {
		return h, fmt.Errorf("bad hunk header %q: %w", line, err)
	}
	if h.NewStart, h.NewLines, err = parseRange(newPart); err != nil {
		return h, fmt.Errorf("bad hunk header %q: %w", line, err)
	}
	return h, nil
}

func parseRange(s string) (start, n int, err error) {
	a, b, hasN := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return
	}
	n = 1
	if hasN {
		n, err = strconv.Atoi(b)
	}
	return
}

// headerPath extracts a path from the text after "--- " or "+++ ". It returns
// "" for /dev/null.
func headerPath(s, prefix string) string {
	s = strings.TrimSuffix(s, "\t") // git appends a tab after paths containing spaces
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(unquote(s), prefix)
}

// unquote decodes a git C-style quoted path; unquoted input is returned as is.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch c = s[i]; c {
		case 'a':
			out = append(out, 7)
		case 'b':
			out = append(out, 8)
		case 'f':
			out = append(out, 12)
		case 'n':
			out = append(out, 10)
		case 'r':
			out = append(out, 13)
		case 't':
			out = append(out, 9)
		case 'v':
			out = append(out, 11)
		case '0', '1', '2', '3':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i:i+3], 8, 8); err == nil {
					out = append(out, byte(v))
					i += 2
					continue
				}
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// modifyHeader builds a plain-modify header against the new path on both
// sides, so applying a renamed file's hunk never touches the rename itself.
// newRaw is the "b/<path>" token exactly as git printed it (maybe quoted).
func modifyHeader(newRaw string) []byte {
	oldRaw := newRaw
	if strings.HasPrefix(newRaw, `"b/`) {
		oldRaw = `"a/` + newRaw[3:]
	} else if strings.HasPrefix(newRaw, "b/") {
		oldRaw = "a/" + newRaw[2:]
	}
	tab := ""
	if strings.Contains(newRaw, " ") && !strings.HasPrefix(newRaw, `"`) {
		tab = "\t"
	}
	return []byte("diff --git " + oldRaw + " " + newRaw + "\n--- " + oldRaw + tab + "\n+++ " + newRaw + tab + "\n")
}
