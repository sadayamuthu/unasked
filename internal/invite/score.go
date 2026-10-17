package invite

import (
	"math"
	"strings"

	"github.com/sadayamuthu/unasked/internal/model"
)

// Thresholds are the verdict cut-offs applied to the asked and said scores.
type Thresholds struct {
	Invite float64 `json:"invite"`
	Low    float64 `json:"low"`
}

// Default is calibrated on the labelled fixtures (internal/fixtures,
// `go test ./internal/fixtures -run TestCalibrate -calibrate -v`): the
// zero-miss pair with the lowest false-alarm rate, then the highest Invite.
var Default = Thresholds{Invite: 0.20, Low: 0.15}

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
// weighs 1, so a nil w gives uniform weights. Sums run in input order, so
// equal inputs give bit-identical results.
func Overlap(a, b []string, w map[string]float64) float64 {
	weight := func(t string) float64 {
		if v, ok := w[t]; ok {
			return v
		}
		return 1
	}
	bs := toTokenSet(b)
	var sumA, sumB, shared float64
	seen := make(map[string]bool, len(a))
	for _, t := range a {
		if seen[t] {
			continue
		}
		seen[t] = true
		sumA += weight(t)
		if bs[t] {
			shared += weight(t)
		}
	}
	clear(seen)
	for _, t := range b {
		if !seen[t] {
			seen[t] = true
			sumB += weight(t)
		}
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	return shared / min(sumA, sumB)
}

// Input is everything Score needs. Classify supplies each hunk's label and
// whether it is high-risk (deletion, test, dependency); it is called exactly
// once per hunk. Terms supplies a high-risk hunk's specific names (removed
// symbols and literals, dependency names; see evidence); it may be nil (no
// names). Tells may be nil (no tell fires).
type Input struct {
	Hunks      []model.Hunk
	Transcript *model.Transcript
	Classify   func(model.Hunk) (model.Label, bool /*highRisk*/)
	Th         Thresholds
	Terms      func(model.Hunk) []string
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
//   - Evidence cap (R36): for the verdict only, a high-risk hunk's overlap
//     with a prompt that lacks specific evidence for it (see evidence) counts
//     at most Invite - 0.01, so generic shared words never invite it. Asked
//     keeps the raw maximum; when the cap alone keeps the hunk from being
//     invited the reason is "asked: generic overlap only".
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
		text string
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
				prompts = append(prompts, prompt{p.Turn, p.Text, toks})
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
		var terms []string
		if s.HighRisk && in.Terms != nil {
			terms = in.Terms(h)
		}
		asked, askedTurn := 0.0, 0
		for j, p := range prompts {
			o := Overlap(hunkToks[i], p.toks, w)
			s.Asked = max(s.Asked, o)
			if s.HighRisk && o >= in.Th.Invite-capMargin && !evidence(s.Label, p.text, p.toks, terms, hunkToks[i], w) {
				o = in.Th.Invite - capMargin
			}
			if o > asked || j == 0 {
				asked, askedTurn = o, p.turn
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
		decide(&s, asked, askedTurn, in.Th, in.DiffOnly, hasTranscript)
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
