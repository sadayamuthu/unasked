package invite

import (
	"sort"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

// lcsMin is the minimum share of a hunk's added lines that must appear, in
// order, in an edit's After text for a same-file fuzzy alignment.
const lcsMin = 0.8

// Align finds the agent edit event that produced hunk h. Events are tried
// latest turn first (later events within a turn first), and only events on
// the hunk's own file are considered:
//
//  1. the first event whose After contains all of h's Added lines joined by
//     "\n" as a substring (CRLF in After is normalised to LF);
//  2. else the first event with LCS(added lines, After lines)/len(added) ≥ 0.8;
//  3. else the first event that covers every non-blank added line (see
//     covers): Claude Code's Edit replaces any unique substring, so
//     new_string often starts or ends mid-line or drops indentation;
//  4. else nil.
//
// A pure deletion (no Added lines) aligns to the latest same-file event: the
// edit that removed text leaves nothing to match on. A file-level hunk
// (rename, mode, empty file) never aligns.
func Align(h model.Hunk, edits []model.EditEvent) *model.EditEvent {
	if h.FileChange != "" {
		return nil // a file-level hunk has no content to align (R41)
	}
	order := make([]int, 0, len(edits))
	for i, e := range edits {
		if e.File == h.File {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ea, eb := edits[order[a]], edits[order[b]]
		if ea.Turn != eb.Turn {
			return ea.Turn > eb.Turn
		}
		return order[a] > order[b]
	})
	if len(order) == 0 {
		return nil
	}
	if len(h.Added) == 0 {
		return &edits[order[0]]
	}
	want := strings.Join(h.Added, "\n")
	for _, i := range order {
		if strings.Contains(normalizeEOL(edits[i].After), want) {
			return &edits[i]
		}
	}
	need := lcsMin * float64(len(h.Added))
	for _, i := range order {
		after := strings.Split(normalizeEOL(edits[i].After), "\n")
		if float64(len(after)) < need {
			continue // LCS ≤ len(after): 0.8 is out of reach
		}
		if float64(lcsLen(h.Added, after)) >= need {
			return &edits[i]
		}
	}
	for _, i := range order {
		if covers(edits[i].After, h.Added) {
			return &edits[i]
		}
	}
	return nil
}

// covers reports whether an edit's After text, read as a substring of the
// new file, accounts for every non-blank added line. Lines are compared
// trimmed. A one-line After covers a line containing it; otherwise a line is
// covered when it equals an After line, ends with the first After line (the
// edit began mid-line) or starts with the last (it ended mid-line). A
// fragment match needs a content token, and so does at least one covered
// line, so "}" or ")" alone aligns nothing.
func covers(after string, added []string) bool {
	var ev []string
	for _, l := range strings.Split(normalizeEOL(after), "\n") {
		ev = append(ev, strings.TrimSpace(l))
	}
	for len(ev) > 0 && ev[len(ev)-1] == "" {
		ev = ev[:len(ev)-1]
	}
	for len(ev) > 0 && ev[0] == "" {
		ev = ev[1:]
	}
	if len(ev) == 0 {
		return false
	}
	first, last := ev[0], ev[len(ev)-1]
	whole := map[string]bool{}
	for _, l := range ev {
		whole[l] = true
	}
	frag := func(f string) bool { return f != "" && len(Tokens(f)) > 0 }
	content := false
	for _, l := range added {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		switch {
		case len(ev) == 1 && frag(first) && strings.Contains(l, first):
		case len(ev) > 1 && (whole[l] ||
			frag(first) && strings.HasSuffix(l, first) ||
			frag(last) && strings.HasPrefix(l, last)):
		default:
			return false
		}
		if len(Tokens(l)) > 0 {
			content = true
		}
	}
	return content
}

func normalizeEOL(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// lcsLen is the length of the longest common subsequence of lines a and b,
// in O(len(a)·len(b)) time and O(len(b)) space.
func lcsLen(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			switch {
			case a[i] == b[j]:
				cur[j+1] = prev[j] + 1
			case prev[j+1] >= cur[j]:
				cur[j+1] = prev[j+1]
			default:
				cur[j+1] = cur[j]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
