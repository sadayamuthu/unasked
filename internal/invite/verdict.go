package invite

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sadayamuthu/unasked/internal/model"
)

// decide sets s.Verdict, s.Reason and s.Turn from the scores already on s.
// askedTurn is the turn of the prompt that produced s.Asked; hasTranscript
// reports whether a transcript was supplied. Rules, first match wins (§6.4,
// "flag generously, drop carefully"):
//
//  1. DiffOnly, or Test 1 unknown (!AskedKnown), whether or not an event
//     aligned: high-risk → unasked, else unclear; reason "no usable prompt"
//     (§6.4 "No usable prompt").
//  2. No aligned event but a transcript is present → unclear, reason
//     "no agent edit found" (Test 2 can never invite it).
//  3. asked ≥ Invite → invited, reason "asked: turn N" (N = prompt turn).
//     Only Test 1 can invite a high-risk hunk.
//  4. Not high-risk, no tell, said ≥ Invite → invited, reason
//     "agent said: turn N" (N = edit event turn).
//  5. max(asked, said) ≥ Low → unclear. For a high-risk hunk said is
//     ignored here too: the agent's words never lift it out of unasked.
//  6. Otherwise unasked.
func decide(s *model.Scored, askedTurn int, th Thresholds, diffOnly, hasTranscript bool) {
	set := func(v model.Verdict, reason string, turn int) {
		s.Verdict, s.Reason, s.Turn = v, reason, turn
	}
	eventTurn := 0
	if s.Event != nil {
		eventTurn = s.Event.Turn
	}
	asked := s.Asked
	switch {
	case diffOnly || !s.AskedKnown:
		if s.HighRisk {
			set(model.VerdictUnasked, "no usable prompt", 0)
		} else {
			set(model.VerdictUnclear, "no usable prompt", 0)
		}
	case s.Event == nil && hasTranscript:
		set(model.VerdictUnclear, "no agent edit found", 0)
	case asked >= th.Invite:
		set(model.VerdictInvited, fmt.Sprintf("asked: turn %d", askedTurn), askedTurn)
	case !s.HighRisk && s.Tell == "" && s.Said >= th.Invite:
		set(model.VerdictInvited, fmt.Sprintf("agent said: turn %d", eventTurn), eventTurn)
	case asked >= th.Low || (!s.HighRisk && s.Said >= th.Low):
		set(model.VerdictUnclear, "weak match", eventTurn)
	default:
		set(model.VerdictUnasked, "no match", eventTurn)
	}
}

// Forced is the one-hop forced-change pass (R4, R7b), run after Score. For
// each hunk H that is neither invited nor high-risk, if some hunk I invited
// before this pass changed or removed a symbol S (I.Symbols.Changed ∪
// Removed) and H's Removed lines contain S as a whole word, H is promoted to
// invited with reason "required by <I.ID>" only when H is purely a reference
// update, compared as raw text, not tokens:
//
//   - H has as many added lines as removed lines, and
//   - for one new name N from I.Symbols.Added ∪ Changed, every removed[i]
//     with each whole-word S replaced by N equals added[i], after collapsing
//     runs of whitespace and trimming.
//
// A same-name signature change (N == S) therefore only matches whitespace-only
// edits: a caller that gains an argument is listed, not auto-invited.
//
// The pass also promotes a new helper the invited change calls: H is
// invited with reason "required by <I.ID>" when an agent edit produced it
// (H.Event is set), it removes no lines, adds at least one symbol, and every
// symbol it adds is called, as a whole word, from the added lines of a hunk
// in the same file invited before this pass (I). Same file only, so a common
// name defined here cannot be tied to an unrelated call elsewhere. A hunk
// that merely calls new invited code is not forced; only definitions are,
// and never ones no agent edit explains.
//
// Hunks promoted in this pass never promote others. The input is not
// modified.
func Forced(s []model.Scored) []model.Scored {
	out := append([]model.Scored(nil), s...)
	type source struct {
		id       string
		name     string
		newNames []string
	}
	var sources []source
	for _, sc := range s {
		if sc.Verdict != model.VerdictInvited {
			continue
		}
		var newNames []string
		for _, sym := range append(append([]string(nil), sc.Hunk.Symbols.Added...), sc.Hunk.Symbols.Changed...) {
			if n := bareName(sym); n != "" {
				newNames = append(newNames, n)
			}
		}
		for _, sym := range append(append([]string(nil), sc.Hunk.Symbols.Changed...), sc.Hunk.Symbols.Removed...) {
			if name := bareName(sym); name != "" {
				sources = append(sources, source{sc.Hunk.ID, name, newNames})
			}
		}
	}
	for i := range out {
		h := &out[i]
		if h.Verdict == model.VerdictInvited || h.HighRisk {
			continue
		}
		if len(h.Hunk.Removed) == 0 {
			if id := helperOf(h.Hunk, s); id != "" && h.Event != nil {
				h.Verdict, h.Reason = model.VerdictInvited, "required by "+id
			}
			continue
		}
		for _, src := range sources {
			if referenceUpdate(h.Hunk, src.name, src.newNames) {
				h.Verdict, h.Reason = model.VerdictInvited, "required by "+src.id
				break
			}
		}
	}
	return out
}

// helperOf returns the ID of the first hunk in h's file invited in s whose
// added lines call every symbol h adds, or "" when there is none.
func helperOf(h model.Hunk, s []model.Scored) string {
	if len(h.Symbols.Added) == 0 {
		return ""
	}
	for _, sc := range s {
		if sc.Verdict != model.VerdictInvited || sc.Hunk.ID == h.ID || sc.Hunk.File != h.File {
			continue
		}
		all := true
		for _, sym := range h.Symbols.Added {
			name := bareName(sym)
			called := false
			for _, l := range sc.Hunk.Added {
				if name != "" && replaceWord(l, name, "") != l {
					called = true
					break
				}
			}
			if !called {
				all = false
				break
			}
		}
		if all {
			return sc.Hunk.ID
		}
	}
	return ""
}

// bareName is the referenced part of a symbol: "T.Method" is called as
// ".Method", so only the last dotted segment is matched.
func bareName(sym string) string {
	return sym[strings.LastIndex(sym, ".")+1:]
}

// referenceUpdate reports whether h's added lines are exactly its removed
// lines with whole-word old replaced by one of newNames (whitespace-collapsed).
func referenceUpdate(h model.Hunk, old string, newNames []string) bool {
	if len(h.Removed) != len(h.Added) {
		return false
	}
	found := false
	for _, l := range h.Removed {
		if replaceWord(l, old, "") != l {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	for _, n := range newNames {
		ok := true
		for i, l := range h.Removed {
			if squash(replaceWord(l, old, n)) != squash(h.Added[i]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// replaceWord replaces every whole-word occurrence of old in s with repl. A
// word boundary is any rune that is not a letter, digit, '_' or '$'.
func replaceWord(s, old, repl string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	i := 0 // s[:i] is written; the rune before a match is read from all of s
	for {
		j := strings.Index(s[i:], old)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		j += i
		before, _ := utf8.DecodeLastRuneInString(s[:j])
		after, _ := utf8.DecodeRuneInString(s[j+len(old):])
		b.WriteString(s[i:j])
		if (j == 0 || !identRune(before)) && (j+len(old) == len(s) || !identRune(after)) {
			b.WriteString(repl)
		} else {
			b.WriteString(old)
		}
		i = j + len(old)
	}
}

func identRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
