// Package keep stores hunks a user or agent has accepted, by content hash and branch.
package keep

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
)

// By says who kept a hunk.
type By string

const (
	ByUser  By = "user"
	ByAgent By = "agent"
)

// Entry is one kept hunk.
type Entry struct {
	Hash   string    `json:"hash"`
	Branch string    `json:"branch"`
	Reason string    `json:"reason"`
	By     By        `json:"by"`
	At     time.Time `json:"at"`
}

var ErrAgentHighRisk = errors.New("agents cannot keep deletions, test/CI or dependency changes")

const fileName = "keep.json"

// agentEnv lists environment markers set by agent tools.
var agentEnv = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CODEX_SANDBOX",
	"CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_MANAGED_BY_NPM", "UNASKED_AGENT",
}

// DetectBy returns agent when an agent marker is set or stdin is not a terminal.
func DetectBy(env func(string) string, stdinIsTTY bool) By {
	if !stdinIsTTY {
		return ByAgent
	}
	for _, k := range agentEnv {
		if env(k) != "" {
			return ByAgent
		}
	}
	return ByUser
}

// CheckAgentKeep refuses an agent keep of a high-risk hunk.
func CheckAgentKeep(s model.Scored) error {
	if s.HighRisk {
		return ErrAgentHighRisk
	}
	return nil
}

// Load reads <git-dir>/unasked/keep.json; a missing file is empty.
func Load(r *gitx.Repo) ([]Entry, error) {
	dir, err := r.UnaskedDir()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, fmt.Errorf("read %s: %w", fileName, err)
	}
	return es, nil
}

// Add stores e, replacing any entry with the same Hash and Branch.
func Add(r *gitx.Repo, e Entry) error {
	es, err := Load(r)
	if err != nil {
		return err
	}
	if e.By != ByUser && e.By != ByAgent {
		return fmt.Errorf("invalid keep author %q", e.By)
	}
	out := make([]Entry, 0, len(es)+1)
	for _, x := range es {
		if x.Hash != e.Hash || x.Branch != e.Branch {
			out = append(out, x)
		}
	}
	out = append(out, e)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	dir, err := r.UnaskedDir()
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, fileName), b)
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), fileName+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Merge applies entries for branch to s. User keeps drop the hunk from out;
// agent keeps leave it listed with the verdict raised to at least unclear.
// kept holds the entries that matched a hunk.
func Merge(s []model.Scored, entries []Entry, branch string) (kept []Entry, out []model.Scored) {
	by := map[string]Entry{}
	for _, e := range entries {
		if e.Branch != branch {
			continue
		}
		// Anything that is not a user keep counts as an agent keep, and an
		// agent keep wins over a user keep for the same hunk.
		if old, ok := by[e.Hash]; ok && old.By != ByUser {
			continue
		}
		by[e.Hash] = e
	}
	for _, h := range s {
		e, ok := by[h.Hunk.Hash]
		if !ok {
			out = append(out, h)
			continue
		}
		kept = append(kept, e)
		if e.By == ByUser {
			continue
		}
		if h.Verdict != model.VerdictInvited && h.Verdict != model.VerdictUnclear {
			h.Verdict = model.VerdictUnclear
		}
		h.Reason = "kept by agent: " + e.Reason
		out = append(out, h)
	}
	return kept, out
}
