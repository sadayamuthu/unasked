package fixtures

import (
	"flag"
	"math"
	"testing"

	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/model"
)

var calibrate = flag.Bool("calibrate", false, "run TestCalibrate: grid-search the verdict thresholds")

// candidate is one threshold pair with its gate figures. Low never changes
// the gate (unasked and unclear are both listed); it decides which listed
// hunks are unasked, the verdict a no-id --drop reverts and the hook feeds
// back to the agent. So after the brief's three keys, ties go to the pair
// whose unasked verdicts agree best with the labels: caught (labelled
// unasked, reported unasked) minus droppable (labelled invited, reported
// unasked).
type candidate struct {
	th        invite.Thresholds
	gate      Gate
	caught    int
	droppable int
}

func (c candidate) agreement() int { return c.caught - c.droppable }

// better reports whether a beats b: zero misses, then the lowest false-alarm
// rate, then the highest Invite, then the best unasked agreement, then the
// highest Low.
func better(a, b candidate) bool {
	if (a.gate.Misses == 0) != (b.gate.Misses == 0) {
		return a.gate.Misses == 0
	}
	if a.gate.Misses != b.gate.Misses {
		return a.gate.Misses < b.gate.Misses
	}
	if ra, rb := a.gate.Rate(), b.gate.Rate(); ra != rb {
		return ra < rb
	}
	if a.th.Invite != b.th.Invite {
		return a.th.Invite > b.th.Invite
	}
	if a.agreement() != b.agreement() {
		return a.agreement() > b.agreement()
	}
	return a.th.Low > b.th.Low
}

// grid is Invite ∈ [0.20, 0.70] and Low ∈ [0.05, Invite-0.05], step 0.05,
// computed in integer hundredths so every value is exact to two places.
func grid() []invite.Thresholds {
	var out []invite.Thresholds
	for inv := 20; inv <= 70; inv += 5 {
		for low := 5; low <= inv-5; low += 5 {
			out = append(out, invite.Thresholds{Invite: float64(inv) / 100, Low: float64(low) / 100})
		}
	}
	return out
}

// TestCalibrate replays every case once, re-scores it at each grid pair and
// prints the chosen pair. invite.Default is then edited to match by hand.
func TestCalibrate(t *testing.T) {
	if !*calibrate {
		t.Skip("run with -calibrate")
	}
	var sessions []*Session
	for _, c := range loadCases(t) {
		s := Replay(t, c)
		s.Run(t, invite.Default)
		sessions = append(sessions, s)
	}
	var best *candidate
	for _, th := range grid() {
		cand := candidate{th: th}
		for _, s := range sessions {
			_, truth := s.Score(t, th)
			cand.gate = cand.gate.Add(truth.Gate())
			for _, h := range truth.Hunks {
				switch {
				case h.Verdict != model.VerdictUnasked:
				case h.Label == Unasked:
					cand.caught++
				default:
					cand.droppable++
				}
			}
		}
		t.Logf("invite=%.2f low=%.2f %s caught=%d droppable=%d", th.Invite, th.Low, cand.gate, cand.caught, cand.droppable)
		if best == nil || better(cand, *best) {
			c := cand
			best = &c
		}
	}
	t.Logf("SELECTED invite=%.2f low=%.2f misses=%d false_alarms=%d listed=%d rate=%.3f",
		best.th.Invite, best.th.Low, best.gate.Misses, best.gate.FalseAlarms, best.gate.Listed, best.gate.Rate())
	if best.gate.Misses != 0 {
		t.Errorf("no threshold pair reaches zero misses: fix the scoring rule the missing case exposes")
	}
	if !approx(best.th, invite.Default) {
		t.Logf("invite.Default is %+v: set it to the selected pair", invite.Default)
	}
}

func approx(a, b invite.Thresholds) bool {
	return math.Abs(a.Invite-b.Invite) < 1e-9 && math.Abs(a.Low-b.Low) < 1e-9
}

func TestGrid(t *testing.T) {
	g := grid()
	if g[0] != (invite.Thresholds{Invite: 0.20, Low: 0.05}) || g[len(g)-1] != (invite.Thresholds{Invite: 0.70, Low: 0.65}) {
		t.Fatalf("grid ends %+v .. %+v", g[0], g[len(g)-1])
	}
	for _, th := range g {
		if th.Low < 0.05 || th.Low > th.Invite-0.05+1e-9 {
			t.Errorf("pair out of range: %+v", th)
		}
	}
}

func TestBetter(t *testing.T) {
	th := func(i, l float64) invite.Thresholds { return invite.Thresholds{Invite: i, Low: l} }
	for _, tc := range []struct {
		name string
		a, b candidate
	}{
		{"zero misses first", candidate{th: th(0.2, 0.1), gate: Gate{0, 9, 10}}, candidate{th: th(0.5, 0.1), gate: Gate{1, 0, 10}}},
		{"lower rate", candidate{th: th(0.2, 0.1), gate: Gate{0, 1, 10}}, candidate{th: th(0.5, 0.1), gate: Gate{0, 2, 10}}},
		{"higher invite", candidate{th: th(0.5, 0.1), gate: Gate{0, 1, 10}}, candidate{th: th(0.4, 0.1), gate: Gate{0, 1, 10}}},
		{"better agreement", candidate{th: th(0.5, 0.1), gate: Gate{0, 1, 10}, caught: 3, droppable: 1}, candidate{th: th(0.5, 0.3), gate: Gate{0, 1, 10}, caught: 4, droppable: 3}},
		{"higher low", candidate{th: th(0.5, 0.3), gate: Gate{0, 1, 10}}, candidate{th: th(0.5, 0.1), gate: Gate{0, 1, 10}}},
	} {
		if !better(tc.a, tc.b) || better(tc.b, tc.a) {
			t.Errorf("%s: ordering wrong", tc.name)
		}
	}
}
