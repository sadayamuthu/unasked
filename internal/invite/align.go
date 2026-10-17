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
//  3. else nil.
//
// A pure deletion (no Added lines) aligns to the latest same-file event: the
// edit that removed text leaves nothing to match on.
func Align(h model.Hunk, edits []model.EditEvent) *model.EditEvent {
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
	for _, i := range order {
		after := strings.Split(normalizeEOL(edits[i].After), "\n")
		if float64(lcsLen(h.Added, after)) >= lcsMin*float64(len(h.Added)) {
			return &edits[i]
		}
	}
	return nil
}

func normalizeEOL(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// lcsLen is the length of the longest common subsequence of lines a and b.
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
