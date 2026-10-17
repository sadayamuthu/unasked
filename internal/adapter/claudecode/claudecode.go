// Package claudecode reads Claude Code session transcripts
// (~/.claude/projects/<encoded cwd>/<session>.jsonl).
package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/model"
)

const name = "claude-code"

func init() {
	home := os.Getenv("UNASKED_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	adapter.Register(New(home))
}

type claude struct{ home string }

// New returns the Claude Code adapter reading transcripts under home.
func New(home string) adapter.Adapter { return claude{home: home} }

func (c claude) Name() string            { return name }
func (c claude) MaxLevel() adapter.Level { return adapter.Full }

func (c claude) projects() string { return filepath.Join(c.home, ".claude", "projects") }

func (c claude) Detect() bool {
	if c.home == "" {
		return false
	}
	fi, err := os.Stat(c.projects())
	return err == nil && fi.IsDir()
}

// headScanLines bounds how far into a file Find looks for cwd and timestamp.
const headScanLines = 200

// Find lists sessions whose cwd is repoDir or inside it and whose file was
// modified at or after since. It opens candidate files rather than trusting
// the encoded directory name, which is lossy.
func (c claude) Find(repoDir string, since time.Time) ([]adapter.Session, error) {
	if c.home == "" {
		return nil, nil
	}
	dirs, err := os.ReadDir(c.projects())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []adapter.Session
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(c.projects(), d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().Before(since) {
				continue
			}
			path := filepath.Join(c.projects(), d.Name(), f.Name())
			cwd, start := head(path)
			if cwd == "" || !within(repoDir, cwd) {
				continue
			}
			out = append(out, adapter.Session{
				Agent: name, ID: strings.TrimSuffix(f.Name(), ".jsonl"),
				Path: path, Cwd: cwd, Start: start, End: info.ModTime(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].End.Equal(out[j].End) {
			return out[i].End.Before(out[j].End)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// head returns the first cwd and timestamp found in the leading lines of path.
func head(path string) (cwd string, start time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < headScanLines; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct{ Cwd, Timestamp string }
			if json.Unmarshal(line, &rec) == nil && rec.Cwd != "" {
				t, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
				return rec.Cwd, t
			}
		}
		if err != nil {
			break
		}
	}
	return "", time.Time{}
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

// Read parses the session file plus any <session>/subagents/*.jsonl beside it.
func (c claude) Read(s adapter.Session) (*model.Transcript, adapter.Capabilities, error) {
	base := strings.TrimSuffix(s.Path, filepath.Ext(s.Path))
	subs, _ := filepath.Glob(filepath.Join(base, "subagents", "*.jsonl"))
	return parseSession(s.Path, subs)
}

// ParseCaptured parses a captured copy: dir/transcript.jsonl and dir/subagents.
func (c claude) ParseCaptured(dir string) (*model.Transcript, adapter.Capabilities, error) {
	subs, _ := filepath.Glob(filepath.Join(dir, "subagents", "*.jsonl"))
	return parseSession(filepath.Join(dir, "transcript.jsonl"), subs)
}
