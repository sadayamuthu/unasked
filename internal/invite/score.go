package invite

import (
	"math"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

// Thresholds are the verdict cut-offs applied to the asked and said scores.
type Thresholds struct{ Invite, Low float64 }

// Default is provisional; Task 18 recalibrates and edits these literals.
var Default = Thresholds{Invite: 0.40, Low: 0.20}

// Step is the threshold shift: --strict adds, --lenient subtracts (floor 0.05).
const Step = 0.10

// minThreshold is the floor a --lenient shift cannot go below.
const minThreshold = 0.05

// Shift moves both thresholds by d, flooring each at 0.05.
func (t Thresholds) Shift(d float64) Thresholds {
	return Thresholds{Invite: max(t.Invite+d, minThreshold), Low: max(t.Low+d, minThreshold)}
}

// minPromptTokens is the fewest content tokens a prompt needs to be judged on.
const minPromptTokens = 4

// reportMarker begins unasked's own hook feedback, which is never a prompt.
const reportMarker = "[unasked report]"

// IDF weights each token by w(t) = ln(1 + N/df(t)), where N is len(docs) and
// df(t) the number of docs containing t.
func IDF(docs [][]string) map[string]float64 {
	df := make(map[string]int)
	for _, d := range docs {
		for t := range toTokenSet(d) {
			df[t]++
		}
	}
	n := float64(len(docs))
	w := make(map[string]float64, len(df))
	for t, c := range df {
		w[t] = math.Log(1 + n/float64(c))
	}
	return w
}

// Overlap is the weighted overlap coefficient of token sets A and B:
// Σ_{t∈A∩B} w(t) / min(Σ_{t∈A} w(t), Σ_{t∈B} w(t)), or 0 if either is empty.
// Duplicates are ignored (sets, not multisets). A token missing from w
// weighs 1, so a nil w gives uniform weights.
func Overlap(a, b []string, w map[string]float64) float64 {
	weight := func(t string) float64 {
		if v, ok := w[t]; ok {
			return v
		}
		return 1
	}
	as, bs := toTokenSet(a), toTokenSet(b)
	var sumA, sumB, shared float64
	for t := range as {
		sumA += weight(t)
		if bs[t] {
			shared += weight(t)
		}
	}
	for t := range bs {
		sumB += weight(t)
	}
	if len(as) == 0 || len(bs) == 0 {
		return 0
	}
	return shared / min(sumA, sumB)
}

// Input is everything Score needs. Classify supplies each hunk's label and
// whether it is high-risk (deletion, test, dependency); it is called exactly
// once per hunk. Tells may be nil (no tell fires).
type Input struct {
	Hunks      []model.Hunk
	Transcript *model.Transcript
	Classify   func(model.Hunk) (model.Label, bool /*highRisk*/)
	Th         Thresholds
	Tells      *Tells
	DiffOnly   bool
}

// Score runs Test 1 (asked) and Test 2 (said) on every hunk and applies the
// verdict rules (see decide).
//
//   - Prompts: those with < 4 content tokens after Tokens, and any whose text
//     starts with "[unasked report]", are dropped. If none remain, AskedKnown
//     is false.
//   - asked = max over kept prompts p of Overlap(HunkTokens(h), Tokens(p)),
//     with IDF docs = every hunk's tokens + every kept prompt's tokens.
//   - The hunk is aligned to an edit event (Align). said =
//     Overlap(Tokens(event.Statement), union of kept prompt tokens); 0 with
//     no event or a statement of < 4 content tokens (R9, as for prompts). Statement tokens absent from the IDF docs weigh ln(1+N), as
//     the rarest possible token. A matching tell is recorded in Tell and
//     stops Test 2 from inviting.
//
// In DiffOnly mode the transcript is ignored.
func Score(in Input) []model.Scored {
	type prompt struct {
		turn int
		toks []string
	}
	var prompts []prompt
	var edits []model.EditEvent
	hasTranscript := in.Transcript != nil && !in.DiffOnly
	if hasTranscript {
		for _, p := range in.Transcript.Prompts {
			if strings.HasPrefix(strings.TrimSpace(p.Text), reportMarker) {
				continue
			}
			if toks := Tokens(p.Text); len(toks) >= minPromptTokens {
				prompts = append(prompts, prompt{p.Turn, toks})
			}
		}
		edits = in.Transcript.Edits
	}

	hunkToks := make([][]string, len(in.Hunks))
	docs := make([][]string, 0, len(in.Hunks)+len(prompts))
	for i, h := range in.Hunks {
		hunkToks[i] = HunkTokens(h)
		docs = append(docs, hunkToks[i])
	}
	var union []string
	for _, p := range prompts {
		docs = append(docs, p.toks)
		union = append(union, p.toks...)
	}
	w := IDF(docs)
	rarest := math.Log(1 + float64(len(docs)))

	out := make([]model.Scored, len(in.Hunks))
	for i, h := range in.Hunks {
		s := model.Scored{Hunk: h, AskedKnown: len(prompts) > 0, Label: model.LabelUnknown}
		if in.Classify != nil {
			s.Label, s.HighRisk = in.Classify(h)
		}
		askedTurn := 0
		for j, p := range prompts {
			if o := Overlap(hunkToks[i], p.toks, w); o > s.Asked || j == 0 {
				s.Asked, askedTurn = o, p.turn
			}
		}
		if !in.DiffOnly {
			s.Event = Align(h, edits)
		}
		if s.Event != nil {
			st := Tokens(s.Event.Statement)
			for _, t := range st {
				if _, ok := w[t]; !ok {
					w[t] = rarest
				}
			}
			if len(st) >= minPromptTokens {
				s.Said = Overlap(st, union, w)
			}
			s.Tell = in.Tells.Match(s.Event.Statement)
		}
		decide(&s, askedTurn, in.Th, in.DiffOnly, hasTranscript)
		out[i] = s
	}
	return out
}

func toTokenSet(ts []string) map[string]bool {
	m := make(map[string]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
}
