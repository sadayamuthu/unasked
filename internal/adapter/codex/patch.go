package codex

import (
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

const (
	beginPatch = "*** Begin Patch"
	endPatch   = "*** End Patch"
	addFile    = "*** Add File: "
	deleteFile = "*** Delete File: "
	updateFile = "*** Update File: "
	moveTo     = "*** Move to: "
)

// parsePatch turns an apply_patch body into one EditEvent per file section.
// File is the destination path (the Move-to target when present); After is
// the section's added ("+") lines joined with "\n". Text before the Begin
// marker (a shell heredoc prefix) is ignored. Unknown or malformed sections
// are skipped, never fatal; a body without a Begin marker yields nothing.
func parsePatch(body string) []model.EditEvent {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == beginPatch {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []model.EditEvent
	var cur *model.EditEvent
	var added []string
	flush := func() {
		if cur != nil && cur.File != "" {
			cur.After = strings.Join(added, "\n")
			out = append(out, *cur)
		}
		cur, added = nil, nil
	}
	for _, l := range lines[start:] {
		switch {
		case strings.TrimSpace(l) == endPatch:
			flush()
			return out
		case strings.HasPrefix(l, addFile):
			flush()
			cur = &model.EditEvent{File: strings.TrimSpace(l[len(addFile):])}
		case strings.HasPrefix(l, deleteFile):
			flush()
			cur = &model.EditEvent{File: strings.TrimSpace(l[len(deleteFile):])}
		case strings.HasPrefix(l, updateFile):
			flush()
			cur = &model.EditEvent{File: strings.TrimSpace(l[len(updateFile):])}
		case strings.HasPrefix(l, moveTo):
			if cur != nil {
				if q := strings.TrimSpace(l[len(moveTo):]); q != "" {
					cur.File = q
				}
			}
		case strings.HasPrefix(l, "*** "):
			if l != "*** End of File" {
				flush() // unknown section header: drop it
			}
		case strings.HasPrefix(l, "+"):
			if cur != nil {
				added = append(added, l[1:])
			}
		}
	}
	flush()
	return out
}
