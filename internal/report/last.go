package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sadayamuthu/unasked/internal/gitx"
)

const lastFile = "last.json"

var hashRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

func lastPath(g *gitx.Repo) string { return filepath.Join(g.GitDir, "unasked", lastFile) }

// SaveLast writes the report to <git-dir>/unasked/last.json atomically, mode 0600.
func SaveLast(g *gitx.Repo, r *Report) error {
	dir, err := g.UnaskedDir()
	if err != nil {
		return err
	}
	path := lastPath(g)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, lastFile+".tmp*")
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

// LoadLast reads the report saved by SaveLast.
func LoadLast(g *gitx.Repo) (*Report, error) {
	b, err := os.ReadFile(lastPath(g))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", lastFile, err)
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
