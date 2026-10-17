// Package codex reads OpenAI Codex CLI rollouts
// (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl).
package codex

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/model"
)

const name = "codex"

func init() {
	home := os.Getenv("UNASKED_HOME")
	if home != "" {
		adapter.Register(New(home))
		return
	}
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		adapter.Register(codexAdapter{dir: ch})
		return
	}
	home, _ = os.UserHomeDir()
	adapter.Register(New(home))
}

type codexAdapter struct{ dir string } // dir is the Codex home (holds sessions/)

// New returns the Codex adapter reading rollouts under <home>/.codex.
// An empty home yields an adapter that detects nothing.
func New(home string) adapter.Adapter {
	if home == "" {
		return codexAdapter{}
	}
	return codexAdapter{dir: filepath.Join(home, ".codex")}
}

func (c codexAdapter) Name() string            { return name }
func (c codexAdapter) MaxLevel() adapter.Level { return adapter.Full }

func (c codexAdapter) sessions() string { return filepath.Join(c.dir, "sessions") }

func (c codexAdapter) Detect() bool {
	if c.dir == "" {
		return false
	}
	fi, err := os.Stat(c.sessions())
	return err == nil && fi.IsDir()
}

// headScanLines bounds how far into a file Find looks for session_meta.
const headScanLines = 20

// Find lists rollouts modified at or after since whose session_meta cwd is
// repoDir or inside it. Only the leading lines of each candidate are read.
func (c codexAdapter) Find(repoDir string, since time.Time) ([]adapter.Session, error) {
	if c.dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(c.sessions()); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []adapter.Session
	_ = filepath.WalkDir(c.sessions(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // unreadable entries are skipped
		}
		if fn := d.Name(); !strings.HasPrefix(fn, "rollout-") || filepath.Ext(fn) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		m, ok := head(path)
		if !ok || m.Cwd == "" || !within(repoDir, m.Cwd) {
			return nil
		}
		start, _ := time.Parse(time.RFC3339Nano, m.Timestamp)
		id := m.ID
		if id == "" {
			id = strings.TrimSuffix(d.Name(), ".jsonl")
		}
		out = append(out, adapter.Session{Agent: name, ID: id, Path: path, Cwd: m.Cwd, Start: start, End: info.ModTime()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if !out[i].End.Equal(out[j].End) {
			return out[i].End.Before(out[j].End)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

type meta struct {
	ID, Cwd, Timestamp, Version, Commit string
}

// head returns the first session_meta record in the leading lines of path.
func head(path string) (meta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return meta{}, false
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < headScanLines; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Type == "session_meta" {
				return parseMeta(rec.Payload), true
			}
		}
		if err != nil {
			break
		}
	}
	return meta{}, false
}

func parseMeta(raw json.RawMessage) meta {
	var p struct {
		ID         string `json:"id"`
		Cwd        string `json:"cwd"`
		Timestamp  string `json:"timestamp"`
		CLIVersion string `json:"cli_version"`
		Git        struct {
			CommitHash string `json:"commit_hash"`
		} `json:"git"`
	}
	_ = json.Unmarshal(raw, &p)
	return meta{ID: p.ID, Cwd: p.Cwd, Timestamp: p.Timestamp, Version: p.CLIVersion, Commit: p.Git.CommitHash}
}

// within reports whether dir is root or lies below it, also comparing with
// symlinks resolved.
func within(root, dir string) bool {
	if inside(filepath.Clean(root), filepath.Clean(dir)) {
		return true
	}
	r, err1 := filepath.EvalSymlinks(root)
	d, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && inside(r, d)
}

func inside(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Read parses the rollout file of s.
func (c codexAdapter) Read(s adapter.Session) (*model.Transcript, adapter.Capabilities, error) {
	return parseFile(s.Path)
}

// ParseCaptured parses dir/transcript.jsonl.
func (c codexAdapter) ParseCaptured(dir string) (*model.Transcript, adapter.Capabilities, error) {
	return parseFile(filepath.Join(dir, "transcript.jsonl"))
}
