// Package revert drops chosen hunks from the working tree. A drop is checked
// first, snapshotted, applied all-or-nothing, verified (dropped hunks gone,
// every other hunk and every byte outside the dropped regions unchanged) and
// restored from the snapshot on any mismatch. Undo restores a snapshot
// byte-exact.
package revert

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/report"
)

var (
	ErrStale            = errors.New("hunk no longer matches the working tree: re-run unasked")
	ErrStagedOverlap    = errors.New("staged changes in a file the drop touches: commit or unstage them first")
	ErrDependency       = errors.New("another hunk depends on this one")
	ErrApplyCheck       = errors.New("the reverse patch does not apply cleanly; nothing was changed")
	ErrVerify           = errors.New("the drop did not verify; the working tree was restored")
	ErrChangedSinceDrop = errors.New("changed since the drop")
	ErrNoSnapshot       = errors.New("no drop to undo")
	// ErrNothingScored is returned for a drop without ids when there was no transcript.
	ErrNothingScored = errors.New("nothing scored: name hunks explicitly")
)

// Plan is a checked drop: the hunks to revert and the reverse-ready patch
// (applied with -R). An empty plan has nothing to drop.
type Plan struct {
	Hunks []model.Hunk
	Patch []byte

	present []string // every hash in the diff the plan was made from
}

// applyArgs applies the plan's patch in reverse to the working tree only.
var applyArgs = []string{"-c", "core.autocrlf=false", "apply", "--unidiff-zero", "-R", "--whitespace=nowarn"}

// Prepare selects and checks the hunks to drop (§9.1). current is the fresh
// re-diff; hashes are the requested content hashes when explicit. Without
// explicit ids it takes every unasked hunk (never unclear) from last, or from
// current when last is nil. Hashes in last.Kept and hunks whose reason starts
// with "kept" count as kept.
func Prepare(r *gitx.Repo, last *report.Report, current []model.Scored, hashes []string, explicit bool) (*Plan, error) {
	byHash := map[string][]model.Scored{}
	present := make([]string, 0, len(current))
	for _, s := range current {
		byHash[s.Hunk.Hash] = append(byHash[s.Hunk.Hash], s)
		present = append(present, s.Hunk.Hash)
	}
	kept := map[string]bool{}
	if last != nil {
		for _, k := range last.Kept {
			kept[k.Hash] = true
		}
	}

	want := map[string]bool{}
	switch {
	case explicit:
		for _, h := range hashes {
			if len(byHash[h]) == 0 {
				return nil, fmt.Errorf("%w (%s)", ErrStale, h)
			}
			want[h] = true
		}
	case last != nil && last.Transcript == "none":
		return nil, ErrNothingScored
	case last != nil:
		for _, h := range last.Hunks {
			if h.Verdict != string(model.VerdictUnasked) || kept[h.Hash] {
				continue
			}
			ss := byHash[h.Hash]
			if len(ss) == 0 || ss[0].Verdict != model.VerdictUnasked {
				return nil, fmt.Errorf("%w (%s %s)", ErrStale, h.ID, h.Hash)
			}
			want[h.Hash] = true
		}
	default:
		for _, s := range current {
			if s.Verdict == model.VerdictUnasked && !kept[s.Hunk.Hash] && !strings.HasPrefix(s.Reason, "kept") {
				want[s.Hunk.Hash] = true
			}
		}
	}

	p := &Plan{present: present}
	for _, s := range current {
		if want[s.Hunk.Hash] {
			p.Hunks = append(p.Hunks, s.Hunk)
		}
	}
	if len(p.Hunks) == 0 {
		return p, nil
	}
	sort.SliceStable(p.Hunks, func(i, j int) bool {
		a, b := p.Hunks[i], p.Hunks[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.NewStart < b.NewStart
	})

	for _, h := range p.Hunks {
		if err := checkPath(h.File); err != nil {
			return nil, err
		}
		if m := unsupportedMode(h.Patch); m != "" {
			return nil, fmt.Errorf("%w: %s has file mode %s (symlink or submodule)", ErrApplyCheck, h.File, m)
		}
	}
	if err := checkStaged(r, p.Hunks); err != nil {
		return nil, err
	}
	if err := checkDependencies(current, want, kept); err != nil {
		return nil, err
	}
	patch, err := buildPatch(r.Dir, p.Hunks)
	if err != nil {
		return nil, err
	}
	p.Patch = patch
	if _, err := r.RunEnv(nil, bytes.NewReader(patch), append(applyArgs, "--check")...); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrApplyCheck, err)
	}
	return p, nil
}

// checkPath rejects paths that could escape the work tree.
func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || (os.PathSeparator == '\\' && strings.Contains(p, "\\")) {
		return fmt.Errorf("%w: unsafe path %q", ErrApplyCheck, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return fmt.Errorf("%w: unsafe path %q", ErrApplyCheck, p)
		}
	}
	return nil
}

var modeRe = regexp.MustCompile(`(?m)^(?:new file mode|deleted file mode|old mode|new mode|index [0-9a-f]+\.\.[0-9a-f]+) (\d+)$`)

// unsupportedMode returns a symlink or gitlink mode found in the header.
func unsupportedMode(patch []byte) string {
	hdr, _, _ := splitPatch(patch)
	for _, m := range modeRe.FindAllSubmatch(hdr, -1) {
		if s := string(m[1]); s == "120000" || s == "160000" {
			return s
		}
	}
	return ""
}

func checkStaged(r *gitx.Repo, hs []model.Hunk) error {
	out, err := r.Run("diff", "--cached", "--name-only", "--no-renames", "-z")
	if err != nil {
		return err
	}
	staged := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		staged[p] = true
	}
	for _, h := range hs {
		if staged[h.File] {
			return fmt.Errorf("%w (%s)", ErrStagedOverlap, h.File)
		}
	}
	return nil
}

// checkDependencies refuses to drop H when a hunk that stays and is invited
// or kept references a symbol H adds or changes, or when a hunk that stays
// was invited as "required by <H.ID>".
func checkDependencies(current []model.Scored, drop, kept map[string]bool) error {
	for _, d := range current {
		if !drop[d.Hunk.Hash] {
			continue
		}
		h := d.Hunk
		syms := append(append([]string(nil), h.Symbols.Added...), h.Symbols.Changed...)
		for _, o := range current {
			if drop[o.Hunk.Hash] {
				continue
			}
			if o.Reason == "required by "+h.ID {
				return fmt.Errorf("%w: %s is required by %s", ErrDependency, h.ID, o.Hunk.ID)
			}
			protected := o.Verdict == model.VerdictInvited || kept[o.Hunk.Hash] || strings.HasPrefix(o.Reason, "kept")
			if !protected {
				continue
			}
			for _, sym := range syms {
				if references(o.Hunk, sym) {
					return fmt.Errorf("%w: %s uses %s from %s", ErrDependency, o.Hunk.ID, sym, h.ID)
				}
			}
		}
	}
	return nil
}

// references reports whether any added or removed line of h mentions the
// symbol's bare name as a run of word tokens (a whole word when the name has
// no tokens of its own).
func references(h model.Hunk, sym string) bool {
	name := sym[strings.LastIndex(sym, ".")+1:]
	if name == "" {
		return false
	}
	want := invite.Tokens(name)
	var word *regexp.Regexp
	if len(want) == 0 {
		word = regexp.MustCompile(`(^|[^\pL\pN_])` + regexp.QuoteMeta(name) + `($|[^\pL\pN_])`)
	}
	for _, line := range append(append([]string(nil), h.Added...), h.Removed...) {
		if word != nil {
			if word.MatchString(line) {
				return true
			}
			continue
		}
		toks := invite.Tokens(line)
		for i := 0; i+len(want) <= len(toks); i++ {
			if slices.Equal(toks[i:i+len(want)], want) {
				return true
			}
		}
	}
	return false
}

// splitPatch splits a single-hunk patch into the file header, the @@ line and
// the hunk body.
func splitPatch(patch []byte) (hdr, at, body []byte) {
	i := 0
	for i < len(patch) {
		if bytes.HasPrefix(patch[i:], []byte("@@ ")) {
			break
		}
		j := bytes.IndexByte(patch[i:], '\n')
		if j < 0 {
			return patch, nil, nil
		}
		i += j + 1
	}
	hdr, rest := patch[:i], patch[i:]
	j := bytes.IndexByte(rest, '\n')
	if j < 0 {
		return hdr, rest, nil
	}
	return hdr, rest[:j+1], rest[j+1:]
}

// keepHeaderLine lists the header lines kept in the reverse patch. Index,
// mode-change and rename lines are dropped so a content drop never touches
// file modes or renames.
func keepHeaderLine(line []byte) bool {
	for _, p := range []string{"diff --git ", "new file mode ", "deleted file mode ", "--- ", "+++ "} {
		if bytes.HasPrefix(line, []byte(p)) {
			return true
		}
	}
	return false
}

// buildPatch writes one file section per path with its chosen hunks in order.
// Each @@ line is renumbered: the new side stays the working-tree position;
// the old side is that position shifted by the chosen hunks above it, so a
// subset applies at the right lines even when unchosen hunks sit above it.
//
// One shape is rewritten: a pure insertion of a last line without a newline
// ("@@ -a,0 +b @@ +x / \ No newline"). Reverted with --unidiff-zero, git
// also strips the newline from the line before it. The unchanged line above
// is added on both sides, which tells git that line keeps its newline.
func buildPatch(work string, hs []model.Hunk) ([]byte, error) {
	var out bytes.Buffer
	file, offset := "", 0
	var lines [][]byte
	for _, h := range hs {
		hdr, at, body := splitPatch(h.Patch)
		if len(at) == 0 {
			return nil, fmt.Errorf("%w: hunk %s has no @@ line", ErrApplyCheck, h.ID)
		}
		if h.File != file {
			file, offset, lines = h.File, 0, nil
			for _, line := range bytes.SplitAfter(hdr, []byte("\n")) {
				if keepHeaderLine(line) {
					out.Write(line)
				}
			}
		}
		startW := h.NewStart
		if h.NewLines == 0 {
			startW++
		}
		oldStart := startW - offset
		if h.OldLines == 0 {
			oldStart--
		}
		if h.OldLines == 0 && h.NoNewlineNew && h.NewStart > 1 {
			if lines == nil {
				b, err := os.ReadFile(workPath(work, h.File))
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrApplyCheck, err)
				}
				lines = splitLines(b)
			}
			if h.NewStart-1 > len(lines) {
				return nil, fmt.Errorf("%w: hunk %s is outside %s", ErrApplyCheck, h.ID, h.File)
			}
			prev := lines[h.NewStart-2]
			fmt.Fprintf(&out, "@@ -%d,1 +%d,%d @@\n-%s+%s", oldStart, h.NewStart-1, h.NewLines+1, prev, prev)
		} else {
			fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, h.OldLines, h.NewStart, h.NewLines)
		}
		out.Write(body)
		offset += h.NewLines - h.OldLines
	}
	return out.Bytes(), nil
}

// Drop applies the plan (§9.1.5, §9.2) and returns the snapshot id. Any
// failure after the snapshot restores it; if that restore also fails the
// error names the snapshot so the user can run `unasked undo <id>`.
func Drop(r *gitx.Repo, p *Plan, rescore func() ([]model.Scored, error)) (string, error) {
	if p == nil || len(p.Hunks) == 0 {
		return "", nil
	}
	unlock, err := lock(r)
	if err != nil {
		return "", err
	}
	defer unlock()
	paths := touchedPaths(p.Hunks)
	if err := checkPatch(r, p.Patch, paths); err != nil {
		return "", err
	}
	snap, err := takeSnapshot(r, paths, p.Hunks)
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		if rerr := snap.restore(r.Dir); rerr != nil {
			// Record what the tree holds now, so a later plain undo can
			// still tell whether the user changed anything since.
			if aerr := snap.recordAfter(r.Dir, true); aerr != nil {
				return "", fmt.Errorf("%w; restoring the snapshot also failed: %v; run `unasked undo --force %s`", cause, errors.Join(rerr, aerr), snap.id)
			}
			return "", fmt.Errorf("%w; restoring the snapshot also failed: %v; run `unasked undo %s`", cause, rerr, snap.id)
		}
		if rerr := os.RemoveAll(snap.dir); rerr != nil {
			return "", fmt.Errorf("%w (working tree restored; could not remove snapshot %s: %v)", cause, snap.id, rerr)
		}
		return "", cause
	}

	if _, err := r.RunEnv(nil, bytes.NewReader(p.Patch), applyArgs...); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrApplyCheck, err))
	}
	// git apply rewrites files with umask permissions; put back each
	// surviving file's own mode before verifying.
	if err := snap.fixModes(r.Dir); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrVerify, err))
	}
	if err := snap.verifyRegions(r.Dir, p.Hunks); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrVerify, err))
	}
	after, err := rescore()
	if err != nil {
		return fail(fmt.Errorf("%w: re-running the report: %v", ErrVerify, err))
	}
	if err := verifyHashes(p, after); err != nil {
		return fail(fmt.Errorf("%w: %w: %v", ErrVerify, errRealigned, err))
	}
	if err := snap.recordAfter(r.Dir, false); err != nil {
		return fail(err)
	}
	// The drop is done and verified; failing to prune old snapshots only
	// leaves extra directories behind, so it must not report the drop failed.
	_ = prune(r, keepSnapshots, snap.id)
	return snap.id, nil
}

// checkPatch re-runs the apply check and refuses a patch that would write a
// path outside the plan's hunks, which the snapshot would not cover.
func checkPatch(r *gitx.Repo, patch []byte, paths []string) error {
	if _, err := r.RunEnv(nil, bytes.NewReader(patch), append(applyArgs, "--check")...); err != nil {
		return fmt.Errorf("%w: %v", ErrApplyCheck, err)
	}
	out, err := r.RunEnv(nil, bytes.NewReader(patch), append(applyArgs, "--numstat", "-z")...)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrApplyCheck, err)
	}
	// Records are "added\tdeleted\tpath\0", or "added\tdeleted\t\0old\0new\0" for a rename.
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return fmt.Errorf("%w: unexpected numstat record %q", ErrApplyCheck, rec)
		}
		names := []string{parts[2]}
		if parts[2] == "" && i+2 < len(fields) {
			names = []string{fields[i+1], fields[i+2]}
			i += 2
		}
		for _, n := range names {
			if !slices.Contains(paths, n) {
				return fmt.Errorf("%w: the patch touches %s, which no chosen hunk names", ErrApplyCheck, n)
			}
		}
	}
	return nil
}

func touchedPaths(hs []model.Hunk) []string {
	var out []string
	for _, h := range hs {
		if !slices.Contains(out, h.File) {
			out = append(out, h.File)
		}
	}
	sort.Strings(out)
	return out
}

// errRealigned marks a hash mismatch found after the byte-level region check
// passed: the bytes are exactly right, but git's zero-context re-diff split
// or aligned the remaining changes differently (typically around repeated
// lines), so their hashes moved. §9.2 still requires the restore.
var errRealigned = errors.New("the re-diff no longer matches the report's hunks")

// verifyHashes checks the rescored diff: its hash multiset must equal the
// one the plan was made from minus the dropped hashes, exactly.
func verifyHashes(p *Plan, after []model.Scored) error {
	dropped := map[string]bool{}
	for _, h := range p.Hunks {
		dropped[h.Hash] = true
	}
	want, got := map[string]int{}, map[string]int{}
	for _, h := range p.present {
		if !dropped[h] {
			want[h]++
		}
	}
	for _, s := range after {
		got[s.Hunk.Hash]++
	}
	for h := range dropped {
		if got[h] > 0 {
			return fmt.Errorf("dropped hunk %s is still present", h)
		}
	}
	for h, n := range want {
		if got[h] != n {
			return fmt.Errorf("hunk %s changed or disappeared", h)
		}
	}
	for h := range got {
		if want[h] == 0 {
			return fmt.Errorf("hunk %s appeared after the drop", h)
		}
	}
	return nil
}

// splitLines splits b into lines that keep their terminators.
func splitLines(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	return bytes.SplitAfter(b, []byte("\n"))[:countLines(b)]
}

func countLines(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// regionCheck compares one file before and after the drop: every line outside
// the dropped hunks must be byte-identical and in place, and each dropped
// region must now hold the hunk's removed lines byte-for-byte, terminators
// included (allowing only a CR before the LF that the repo's eol conversion
// adds).
func regionCheck(pre, post []byte, hs []model.Hunk) error {
	w, t := splitLines(pre), splitLines(post)
	wi, ti := 0, 0
	for _, h := range hs {
		start := h.NewStart - 1
		if h.NewLines == 0 {
			start = h.NewStart
		}
		if start < wi || start+h.NewLines > len(w) {
			return fmt.Errorf("hunk %s is outside %s", h.ID, h.File)
		}
		n := start - wi
		if ti+n > len(t) || !equalLines(w[wi:start], t[ti:ti+n]) {
			return fmt.Errorf("%s changed outside the dropped hunks", h.File)
		}
		wi, ti = start+h.NewLines, ti+n
		if ti+h.OldLines > len(t) {
			return fmt.Errorf("%s is shorter than expected", h.File)
		}
		for i, want := range h.Removed {
			term := "\n"
			if h.NoNewlineOld && i == len(h.Removed)-1 {
				term = ""
			}
			got := string(t[ti+i])
			if got != want+term && (term == "" || got != want+"\r"+term) {
				return fmt.Errorf("%s line %d is not what hunk %s removed", h.File, ti+i+1, h.ID)
			}
		}
		ti += h.OldLines
	}
	if len(w)-wi != len(t)-ti || !equalLines(w[wi:], t[ti:]) {
		return fmt.Errorf("%s changed outside the dropped hunks", hs[0].File)
	}
	return nil
}

func equalLines(a, b [][]byte) bool {
	return slices.EqualFunc(a, b, bytes.Equal)
}
