// Package pipeline runs the whole report: select a session and range, diff,
// score, label, apply keeps and build the report. The CLI, drop rescoring and
// hook run all go through it so they judge hunks the same way.
package pipeline

import (
	"strings"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/baseline"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/hunk"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/keep"
	"github.com/sadayamuthu/unasked/internal/label"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/report"
)

// adapters is the adapter set Select considers; tests replace it.
var adapters = adapter.All

// Run selects the session and range for req and scores it. The report
// reflects keeps; scored holds every hunk in the diff before keeps are
// applied, which is what revert.Prepare and its rescore compare.
func Run(r *gitx.Repo, req adapter.Request, th invite.Thresholds) (*report.Report, []model.Scored, *adapter.Selection, error) {
	sel, err := adapter.Select(r, req, adapters())
	if err != nil {
		return nil, nil, nil, err
	}
	rep, scored, err := Score(r, sel, th)
	if err != nil {
		return nil, nil, nil, err
	}
	return rep, scored, sel, nil
}

// Rescore re-diffs sel's base against the current working tree (the index
// for a staged selection) and scores it with the same transcript. It never
// re-selects a session, so a drop is verified against the range it was
// chosen from.
func Rescore(r *gitx.Repo, sel *adapter.Selection, th invite.Thresholds) (*report.Report, []model.Scored, error) {
	var to string
	var err error
	if sel.BaseSource == "staged" {
		to, err = baseline.IndexTree(r)
	} else {
		var b baseline.Baseline
		b, err = baseline.Snapshot(r)
		to = b.Tree
	}
	if err != nil {
		return nil, nil, err
	}
	hs, bin, err := hunk.Diff(r, sel.FromTree, to)
	if err != nil {
		return nil, nil, err
	}
	next := *sel
	next.ToTree, next.Hunks, next.Binary = to, hs, bin
	return Score(r, &next, th)
}

// Score judges sel.Hunks against sel.Transcript: invite.Score, the forced
// pass, labels, then keeps and the report.
func Score(r *gitx.Repo, sel *adapter.Selection, th invite.Thresholds) (*report.Report, []model.Scored, error) {
	tells, err := invite.LoadTells(invite.UserTellsFile())
	if err != nil {
		return nil, nil, err
	}
	scored := invite.Score(invite.Input{
		Hunks:      sel.Hunks,
		Transcript: sel.Transcript,
		Classify:   label.ClassifyHunk,
		Terms:      label.Terms,
		Th:         th,
		Tells:      tells,
		DiffOnly:   sel.Caps.Level == adapter.DiffOnly,
	})
	scored = invite.Forced(scored)
	scored = label.Apply(r, scored)

	entries, err := keep.Load(r)
	if err != nil {
		return nil, nil, err
	}
	branch, err := Branch(r)
	if err != nil {
		return nil, nil, err
	}
	kept, listed := keep.Merge(scored, entries, branch)
	rep := report.Build(sel, listed, kept)
	rep.Thresholds = th
	return rep, scored, nil
}

// Branch names the branch keeps are scoped to: the current branch, or
// "HEAD@<short sha>" on a detached HEAD, or "HEAD" before the first commit.
func Branch(r *gitx.Repo) (string, error) {
	out, err := r.Run("symbolic-ref", "-q", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	sha, err := r.Run("rev-parse", "-q", "--verify", "--short", "HEAD")
	if err != nil {
		return "HEAD", nil
	}
	return "HEAD@" + strings.TrimSpace(string(sha)), nil
}
