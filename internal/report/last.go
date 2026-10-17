package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sadayamuthu/unasked/internal/gitx"
)

// LastFile holds the report the developer last ran; LastHookFile the one an
// agent's hook or an agent's own run last saved. Each caller resolves ids
// against its own, so a background hook cannot renumber the developer's
// report (I6).
const (
	LastFile     = "last.json"
	LastHookFile = "last-hook.json"
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

func lastPath(g *gitx.Repo, name string) string { return filepath.Join(g.GitDir, "unasked", name) }

// SaveLast writes the report to <git-dir>/unasked/last.json.
func SaveLast(g *gitx.Repo, r *Report) error { return SaveLastFile(g, LastFile, r) }

// LoadLast reads the report saved by SaveLast.
func LoadLast(g *gitx.Repo) (*Report, error) { return LoadLastFile(g, LastFile) }

// SaveLastFile writes the report to <git-dir>/unasked/<name> atomically,
// mode 0600.
func SaveLastFile(g *gitx.Repo, name string, r *Report) error {
	dir, err := g.UnaskedDir()
	if err != nil {
		return err
	}
	path := lastPath(g, name)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && os.PathSeparator == '/' {
		return fail(err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// LoadLastFile reads the report saved by SaveLastFile under name.
func LoadLastFile(g *gitx.Repo, name string) (*Report, error) {
	b, err := os.ReadFile(lastPath(g, name))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &r, nil
}

// Resolve maps display ids ("h12") and 12-hex content hashes to hashes.
func (r *Report) Resolve(ids []string) ([]string, error) {
	byID := map[string]string{}
	for _, h := range r.Hunks {
		byID[h.ID] = h.Hash
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		} else if hashRe.MatchString(id) {
			out = append(out, id)
		} else {
			return nil, fmt.Errorf("unknown hunk id %q: re-run unasked to refresh the report", id)
		}
	}
	return out, nil
}

// Target names one hunk: its content hash and, for a display id, the file
// and first new line it had in the report, which tell apart hunks with the
// same hash (the same change made twice in one file).
type Target struct {
	Hash, File string
	Start      int
}

// ResolveTargets maps display ids ("h12") to the hunk they name in r, and
// 12-hex content hashes to a Target with the hash only.
func (r *Report) ResolveTargets(ids []string) ([]Target, error) {
	byID := map[string]HunkJSON{}
	for _, h := range r.Hunks {
		byID[h.ID] = h
	}
	out := make([]Target, 0, len(ids))
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			start, _, _ := strings.Cut(h.Lines, "-")
			n, _ := strconv.Atoi(start)
			out = append(out, Target{Hash: h.Hash, File: h.File, Start: n})
		} else if hashRe.MatchString(id) {
			out = append(out, Target{Hash: id})
		} else {
			return nil, fmt.Errorf("unknown hunk id %q: re-run unasked to refresh the report", id)
		}
	}
	return out, nil
}
