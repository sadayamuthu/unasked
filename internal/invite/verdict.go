package invite

import (
	"fmt"
	"regexp"
	"strings"

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

// Forced is the one-hop forced-change pass (R4), run after Score. For each
// hunk H that is neither invited nor high-risk, if some hunk I invited before
// this pass changed or removed a symbol S (I.Symbols.Changed ∪ Removed), H's
// Removed lines contain S as a word, and H differs only by that reference
// being updated, checked both ways so nothing else can ride along:
//
//	Tokens(H.Removed) − Tokens(S) ⊆ Tokens(H.Added), and
//	Tokens(H.Added) − Tokens(new names) ⊆ Tokens(H.Removed),
//
// where the new names are I.Symbols.Added ∪ Changed. H is then promoted to
// invited with reason "required by <I.ID>".
// Hunks promoted in this pass never promote others. The input is not modified.
func Forced(s []model.Scored) []model.Scored {
	out := append([]model.Scored(nil), s...)
	type source struct {
		id       string
		name     string
		newNames map[string]bool // tokens of I's added/changed symbol names
		word     *regexp.Regexp
	}
	var sources []source
	for _, sc := range s {
		if sc.Verdict != model.VerdictInvited {
			continue
		}
		newNames := map[string]bool{}
		for _, sym := range append(append([]string(nil), sc.Hunk.Symbols.Added...), sc.Hunk.Symbols.Changed...) {
			for _, t := range Tokens(bareName(sym)) {
				newNames[t] = true
			}
		}
		syms := append(append([]string(nil), sc.Hunk.Symbols.Changed...), sc.Hunk.Symbols.Removed...)
		for _, sym := range syms {
			name := bareName(sym)
			if name == "" {
				continue
			}
			word := regexp.MustCompile(`(^|[^\p{L}\p{N}_$])` + regexp.QuoteMeta(name) + `($|[^\p{L}\p{N}_$])`)
			sources = append(sources, source{sc.Hunk.ID, name, newNames, word})
		}
	}
	for i := range out {
		h := &out[i]
		if h.Verdict == model.VerdictInvited || h.HighRisk || len(h.Hunk.Removed) == 0 {
			continue
		}
		removed := strings.Join(h.Hunk.Removed, "\n")
		for _, src := range sources {
			if !src.word.MatchString(removed) || !onlyReferenceChanged(h.Hunk, src.name, src.newNames) {
				continue
			}
			h.Verdict, h.Reason = model.VerdictInvited, "required by "+src.id
			break
		}
	}
	return out
}

// bareName is the referenced part of a symbol: "T.Method" is called as
// ".Method", so only the last dotted segment is matched.
func bareName(sym string) string {
	return sym[strings.LastIndex(sym, ".")+1:]
}

// onlyReferenceChanged reports whether h's removed and added tokens are the
// same apart from the old name (tokens of name) leaving and new names
// (newNames) arriving.
func onlyReferenceChanged(h model.Hunk, name string, newNames map[string]bool) bool {
	removed := toTokenSet(Tokens(strings.Join(h.Removed, "\n")))
	added := toTokenSet(Tokens(strings.Join(h.Added, "\n")))
	return subsetExcept(removed, added, toTokenSet(Tokens(name))) &&
		subsetExcept(added, removed, newNames)
}

// subsetExcept reports whether a − except ⊆ b.
func subsetExcept(a, b, except map[string]bool) bool {
	for t := range a {
		if !except[t] && !b[t] {
			return false
		}
	}
	return true
}

func toTokenSet(ts []string) map[string]bool {
	m := make(map[string]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
}
