package adapter

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/hunk"
	"github.com/sadayamuthu/unasked/internal/model"
)

// RangeMode picks which diff range is scored.
type RangeMode int

const (
	RangeSession RangeMode = iota // the session's own range (captured baseline or commit fallback)
	RangeSince                    // merge-base(Since, HEAD) to the working tree
	RangeStaged                   // HEAD to the index
)

// Request says what to select. Agent and SessionID filter the candidate
// sessions; AllSessions unions every intersecting session.
type Request struct {
	Mode             RangeMode
	Since            string
	SessionID, Agent string
	AllSessions      bool
}

// Selection is the chosen transcript together with the diff range it is
// scored against. Hunks and Binary are the diff of FromTree..ToTree so
// callers need not diff again. BaseSource is "captured", "reflog", "head",
// "since" or "staged".
type Selection struct {
	Sessions         []Session
	Transcript       *model.Transcript // never nil
	Caps             Capabilities
	FromTree, ToTree string
	BaseSource       string
	Warnings         []string
	Hunks            []model.Hunk
	Binary           int
}

const (
	lookback        = 7 * 24 * time.Hour
	warnPreexisting = "pre-existing changes may be included"
)

// candidate is a session with the range it would be scored against.
type candidate struct {
	s    Session
	a    Adapter
	tr   *model.Transcript
	caps Capabilities
	from string
	src  string
	warn bool // from is a commit-based guess
	hs   []model.Hunk
	bin  int
}

// Select decides which session transcript and which diff range to score.
func Select(r *gitx.Repo, req Request, adapters []Adapter) (*Selection, error) {
	var toTree string
	var err error
	if req.Mode == RangeStaged {
		toTree, err = baseline.IndexTree(r)
	} else {
		var b baseline.Baseline
		b, err = baseline.Snapshot(r)
		toTree = b.Tree
	}
	if err != nil {
		return nil, err
	}

	// The fixed range for since/staged modes is computed once.
	var fixedFrom, fixedSrc string
	switch req.Mode {
	case RangeSince:
		if fixedFrom, err = sinceTree(r, req.Since); err != nil {
			return nil, err
		}
		fixedSrc = "since"
	case RangeStaged:
		if fixedFrom, err = headTree(r); err != nil {
			return nil, err
		}
		fixedSrc = "staged"
	}

	cands, err := candidates(r, req, adapters)
	if err != nil {
		return nil, err
	}

	type diffKey struct{ from string }
	type diffVal struct {
		hs  []model.Hunk
		bin int
	}
	diffs := map[diffKey]diffVal{}
	diff := func(from string) ([]model.Hunk, int, error) {
		if v, ok := diffs[diffKey{from}]; ok {
			return v.hs, v.bin, nil
		}
		hs, bin, err := hunk.Diff(r, from, toTree)
		if err != nil {
			return nil, 0, err
		}
		diffs[diffKey{from}] = diffVal{hs, bin}
		return hs, bin, nil
	}

	var hits []*candidate
	failed := map[*candidate]string{}
	var failedOrder []*candidate
	for _, c := range cands {
		c.tr, c.caps = Guarded(c.a, func() (*model.Transcript, Capabilities, error) {
			if c.s.Captured {
				return c.a.ParseCaptured(c.s.Path)
			}
			return c.a.Read(c.s)
		})
		if c.caps.Degraded != "" {
			failed[c] = c.caps.Degraded
			failedOrder = append(failedOrder, c)
		}
		if len(c.tr.Edits) == 0 {
			continue
		}
		if fixedFrom != "" {
			c.from, c.src = fixedFrom, fixedSrc
		} else if err := rangeFor(r, c); err != nil {
			return nil, err
		}
		if c.hs, c.bin, err = diff(c.from); err != nil {
			return nil, err
		}
		if intersects(c.tr, c.hs) {
			hits = append(hits, c)
			if !req.AllSessions {
				break
			}
		}
	}

	sel := &Selection{ToTree: toTree, Transcript: &model.Transcript{}, Caps: Capabilities{Level: DiffOnly}}
	if len(hits) == 0 {
		sel.FromTree, sel.BaseSource = fixedFrom, fixedSrc
		if fixedFrom == "" {
			c := &candidate{}
			if err := rangeFor(r, c); err != nil {
				return nil, err
			}
			sel.FromTree, sel.BaseSource = c.from, c.src
			sel.Warnings = append(sel.Warnings, warnPreexisting)
		}
		if sel.Hunks, sel.Binary, err = diff(sel.FromTree); err != nil {
			return nil, err
		}
		sel.Warnings = append(sel.Warnings, "transcript: none")
		if notes := failedNotes(failedOrder, failed, nil); len(notes) > 0 {
			sel.Caps.Degraded = strings.Join(notes, "; ")
			sel.Warnings = append(sel.Warnings, "transcript: "+sel.Caps.Degraded)
		}
		return sel, nil
	}

	first := hits[0] // newest
	sel.FromTree, sel.BaseSource, sel.Hunks, sel.Binary = first.from, first.src, first.hs, first.bin
	if first.warn {
		sel.Warnings = append(sel.Warnings, warnPreexisting)
	}
	if len(hits) == 1 {
		sel.Sessions = []Session{first.s}
		sel.Transcript, sel.Caps = first.tr, first.caps
	} else {
		sel.Transcript, sel.Caps = union(hits)
		if fixedFrom == "" {
			// Widest range: the oldest baseline, so an earlier session's
			// changes are not hidden by a later session's start baseline.
			w, err := widest(r, hits)
			if err != nil {
				return nil, err
			}
			sel.FromTree, sel.BaseSource = w.from, w.src
			if sel.Hunks, sel.Binary, err = diff(w.from); err != nil {
				return nil, err
			}
			sel.Warnings = nil
			if w.warn {
				sel.Warnings = append(sel.Warnings, warnPreexisting)
			}
		}
		for i := len(hits) - 1; i >= 0; i-- {
			sel.Sessions = append(sel.Sessions, hits[i].s)
		}
	}
	// Sessions that failed to read must not vanish just because another
	// session intersected.
	if notes := failedNotes(failedOrder, failed, hits); len(notes) > 0 {
		joined := strings.Join(notes, "; ")
		sel.Warnings = append(sel.Warnings, "transcript: "+joined)
		if sel.Caps.Degraded != "" {
			joined = sel.Caps.Degraded + "; " + joined
		}
		sel.Caps.Degraded = joined
	}
	return sel, nil
}

// failedNotes returns the degradation notes of failed candidates that are not
// among skip, in candidate order.
func failedNotes(order []*candidate, failed map[*candidate]string, skip []*candidate) []string {
	var out []string
next:
	for _, c := range order {
		for _, h := range skip {
			if h == c {
				continue next
			}
		}
		out = append(out, failed[c])
	}
	return out
}

// candidates lists captured and native sessions for the repo, newest first,
// filtered by the request. A native session that also has a captured copy is
// dropped in favour of the capture.
func candidates(r *gitx.Repo, req Request, adapters []Adapter) ([]*candidate, error) {
	lookup := func(name string) (Adapter, bool) {
		for _, a := range adapters {
			if a.Name() == name {
				return a, true
			}
		}
		return Get(name)
	}
	keep := func(s Session) bool {
		return (req.Agent == "" || s.Agent == req.Agent) && (req.SessionID == "" || s.ID == req.SessionID)
	}
	var out []*candidate
	seen := map[[2]string]bool{}
	caps, err := ListCaptured(r)
	if err != nil {
		return nil, err
	}
	for _, s := range caps {
		a, ok := lookup(s.Agent)
		if !ok {
			continue
		}
		seen[[2]string{s.Agent, s.ID}] = true
		if keep(s) {
			out = append(out, &candidate{s: s, a: a})
		}
	}
	since := time.Now().Add(-lookback)
	for _, a := range adapters {
		found, err := a.Find(r.Dir, since)
		if err != nil {
			continue // one unreadable agent must not hide the others
		}
		for _, s := range found {
			if !seen[[2]string{s.Agent, s.ID}] && keep(s) {
				out = append(out, &candidate{s: s, a: a})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return stamp(out[i].s).After(stamp(out[j].s)) })
	return out, nil
}

func stamp(s Session) time.Time {
	if !s.End.IsZero() {
		return s.End
	}
	return s.Start
}

// rangeFor sets c.from, c.src and c.warn for session mode.
func rangeFor(r *gitx.Repo, c *candidate) error {
	if c.s.Meta != nil && c.s.Meta.Start.Tree != "" {
		c.from, c.src, c.warn = c.s.Meta.Start.Tree, "captured", false
		return nil
	}
	c.warn = true
	start := time.Time{}
	if c.tr != nil {
		start = c.tr.Start
	}
	if start.IsZero() {
		start = c.s.Start
	}
	if !start.IsZero() {
		out, err := r.Run("log", "-1", "--before="+start.UTC().Format(time.RFC3339), "--format=%H")
		var ge *gitx.Error
		if err != nil && !errors.As(err, &ge) {
			return err
		}
		if commit := strings.TrimSpace(string(out)); err == nil && commit != "" {
			tree, err := revTree(r, commit)
			if err != nil {
				return err
			}
			c.from, c.src = tree, "reflog"
			return nil
		}
	}
	tree, err := headTree(r)
	if err != nil {
		return err
	}
	c.from, c.src = tree, "head"
	return nil
}

// widest picks the range covering every unioned session: the earliest
// captured Start when all have one, else the commit fallback before the
// earliest transcript start (with the usual warning).
func widest(r *gitx.Repo, hits []*candidate) (*candidate, error) {
	var oldest *candidate
	allCaptured := true
	var earliest time.Time
	for _, c := range hits {
		if c.s.Meta == nil || c.s.Meta.Start.Tree == "" {
			allCaptured = false
		} else if oldest == nil || c.s.Meta.Start.Taken.Before(oldest.s.Meta.Start.Taken) {
			oldest = c
		}
		st := c.tr.Start
		if st.IsZero() {
			st = c.s.Start
		}
		if !st.IsZero() && (earliest.IsZero() || st.Before(earliest)) {
			earliest = st
		}
	}
	if allCaptured {
		return &candidate{from: oldest.s.Meta.Start.Tree, src: "captured"}, nil
	}
	w := &candidate{tr: &model.Transcript{Start: earliest}}
	if err := rangeFor(r, w); err != nil {
		return nil, err
	}
	return w, nil
}

// intersects reports whether any edit touches a file in the diff.
func intersects(tr *model.Transcript, hs []model.Hunk) bool {
	files := map[string]bool{}
	for _, h := range hs {
		files[h.File] = true
		if h.OldFile != "" {
			files[h.OldFile] = true
		}
	}
	for _, e := range tr.Edits {
		if e.File != "" && files[e.File] {
			return true
		}
	}
	return false
}

// union concatenates the transcripts oldest first, shifting turn numbers so
// they stay monotonic. Caps take the lowest Level and join the Degraded notes.
func union(hits []*candidate) (*model.Transcript, Capabilities) {
	out := &model.Transcript{}
	var caps Capabilities
	var degraded []string
	offset := 0
	for i := len(hits) - 1; i >= 0; i-- {
		tr, c := hits[i].tr, hits[i].caps
		maxTurn := tr.Turns
		for _, p := range tr.Prompts {
			out.Prompts = append(out.Prompts, model.Prompt{Turn: p.Turn + offset, Text: p.Text})
			maxTurn = max(maxTurn, p.Turn)
		}
		for _, e := range tr.Edits {
			e.Turn += offset
			out.Edits = append(out.Edits, e)
			maxTurn = max(maxTurn, e.Turn-offset)
		}
		offset += maxTurn
		out.Commits = append(out.Commits, tr.Commits...)
		if out.Version == "" {
			out.Version = tr.Version
		}
		if !tr.Start.IsZero() && (out.Start.IsZero() || tr.Start.Before(out.Start)) {
			out.Start = tr.Start
		}
		if tr.End.After(out.End) {
			out.End = tr.End
		}
		if i == len(hits)-1 || c.Level < caps.Level {
			caps = c
		}
		if c.Degraded != "" {
			degraded = append(degraded, c.Degraded)
		}
	}
	out.Turns = offset
	caps.Degraded = strings.Join(degraded, "; ")
	return out, caps
}

func revTree(r *gitx.Repo, rev string) (string, error) {
	out, err := r.Run("rev-parse", "--verify", "-q", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// headTree is HEAD's tree, or the empty tree in a repo with no commits.
func headTree(r *gitx.Repo) (string, error) {
	if _, err := r.Run("rev-parse", "--verify", "-q", "HEAD"); err != nil {
		var ge *gitx.Error
		if !errors.As(err, &ge) || ge.Code != 1 {
			return "", err
		}
		out, err := r.RunEnv(nil, strings.NewReader(""), "mktree")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return revTree(r, "HEAD")
}

// sinceTree is the tree of merge-base(ref, HEAD). An unknown ref is an error.
func sinceTree(r *gitx.Repo, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("--since needs a ref")
	}
	if _, err := r.Run("rev-parse", "--verify", "-q", ref+"^{commit}"); err != nil {
		return "", fmt.Errorf("unknown ref %q", ref)
	}
	out, err := r.Run("merge-base", ref, "HEAD")
	if err != nil {
		return "", fmt.Errorf("no merge base between %q and HEAD: %w", ref, err)
	}
	return revTree(r, strings.TrimSpace(string(out)))
}
