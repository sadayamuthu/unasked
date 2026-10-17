package hook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/invite"
	"github.com/sadayamuthu/unasked/internal/pipeline"
	"github.com/sadayamuthu/unasked/internal/report"
)

const (
	// maxFeedbacks caps consecutive feedbacks per session (§8 loop safety).
	maxFeedbacks = 3
	capWarning   = "unasked: 3 consecutive reports ignored; not blocking again this session"

	lockName  = ".hook.lock"
	lockWait  = time.Second
	lockStale = 2 * time.Minute

	logName    = "hook.log"
	logMaxSize = 1 << 20
	maxPayload = 16 << 20
)

var errLockBusy = errors.New("capture dir lock busy for 1s; skipped this hook call")

// Run handles one hook call from agent: the payload is on stdin, the agent
// reads the answer from stdout. It never fails the agent: every error or
// panic is appended to <git-dir>/unasked/hook.log and Run returns 0 (or the
// exit code the agent's protocol asks for).
func Run(agent string, stdin io.Reader, stdout, stderr io.Writer, now time.Time) (code int) {
	c := &call{agent: agent, now: now}
	defer func() {
		if p := recover(); p != nil {
			c.log(fmt.Errorf("panic: %v", p))
			code = 0
		}
	}()
	code, err := c.handle(stdin, stdout, stderr)
	if err != nil {
		c.log(err)
		return 0
	}
	return code
}

// call is one hook invocation; r is set once the repo is known.
type call struct {
	agent, event, cwd string
	now               time.Time
	r                 *gitx.Repo
}

func (c *call) handle(stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return 0, fmt.Errorf("read payload: %w", err)
	}
	s, known := Get(c.agent)
	parse := parseCommon
	if known {
		parse = s.ParsePayload
	}
	p, perr := parse(data)
	c.event, c.cwd = p.Event, p.Cwd
	if !known {
		return 0, fmt.Errorf("unknown agent %q", c.agent)
	}
	if perr != nil {
		return 0, perr
	}
	if p.Cwd == "" {
		return 0, errors.New("hook payload: no cwd")
	}
	r, err := gitx.Open(p.Cwd)
	if errors.Is(err, gitx.ErrNotRepo) {
		return 0, nil // not our business outside a repository
	}
	if err != nil {
		return 0, err
	}
	c.r = r
	if p.Session == "" {
		return 0, errors.New("hook payload: no session_id")
	}
	dir, err := adapter.CaptureDir(r, c.agent, p.Session)
	if err != nil {
		return 0, err
	}
	unlock, err := lock(dir)
	if err != nil {
		return 0, err
	}
	defer unlock()
	switch p.Event {
	case "SessionStart", "UserPromptSubmit":
		_, err := adapter.RecordStart(r, c.agent, p.Session, p.Cwd)
		return 0, err
	case "Stop":
		return c.stop(s, p, dir, stdout, stderr)
	}
	return 0, nil
}

// stop captures the session, reports, and feeds an unasked result back to
// the agent unless loop safety says not to.
func (c *call) stop(s Spec, p Payload, dir string, stdout, stderr io.Writer) (int, error) {
	r := c.r
	p.TranscriptPath = expandHome(p.TranscriptPath)
	m, err := adapter.RecordStop(r, c.agent, p.Session, p.TranscriptPath, s.Extra(p))
	if err != nil {
		return 0, err
	}
	rep, _, _, err := pipeline.Run(r, adapter.Request{SessionID: p.Session, Agent: c.agent}, invite.Default)
	if err != nil {
		return 0, err
	}

	feedback, saveMeta := false, false
	switch {
	case rep.Result == report.ResultUnasked && !p.StopHookActive && m.Feedbacks < maxFeedbacks:
		m.Feedbacks++
		feedback, saveMeta = true, true
	case rep.Result == report.ResultUnasked && !p.StopHookActive:
		fmt.Fprintln(stderr, capWarning)
		rep.Warnings = append(rep.Warnings, strings.TrimPrefix(capWarning, "unasked: "))
	case rep.Result == report.ResultClean && m.Feedbacks != 0:
		m.Feedbacks = 0
		saveMeta = true
	}
	var errs []error
	if saveMeta {
		if err := adapter.SaveMeta(dir, m); err != nil {
			// Without a persisted counter the loop cap cannot hold: let the agent stop.
			feedback = false
			errs = append(errs, err)
		}
	}
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, rep); err != nil {
		errs = append(errs, err)
	} else if err := atomicWrite(filepath.Join(dir, "report.json"), buf.Bytes(), 0o600); err != nil {
		errs = append(errs, err)
	}
	// The agent's `unasked --drop <ids>` resolves ids against last.json.
	if err := report.SaveLast(r, rep); err != nil {
		errs = append(errs, err)
	}
	code := s.Respond(stdout, rep, feedback)
	if err := adapter.Prune(r, 0, 0); err != nil {
		errs = append(errs, err)
	}
	return code, errors.Join(errs...)
}

// lock takes dir's hook lock (O_EXCL), waiting up to lockWait. A lock older
// than lockStale is left from a crashed hook and is taken over.
func lock(dir string) (func(), error) {
	p := filepath.Join(dir, lockName)
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errLockBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// log appends err to <git-dir>/unasked/hook.log. Without a repository from
// the payload it tries the process directory; outside any repository the
// error is dropped, since unasked writes nowhere else.
func (c *call) log(err error) {
	defer func() { recover() }()
	r := c.r
	if r == nil {
		dir := c.cwd
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if dir == "" {
			return
		}
		var e error
		if r, e = gitx.Open(dir); e != nil {
			return
		}
	}
	base, e := r.UnaskedDir()
	if e != nil {
		return
	}
	p := filepath.Join(base, logName)
	if fi, e := os.Stat(p); e == nil && fi.Size() > logMaxSize {
		os.Rename(p, p+".1")
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if e != nil {
		return
	}
	defer f.Close()
	event := c.event
	if event == "" {
		event = "-"
	}
	fmt.Fprintf(f, "%s %s %s: %s\n", c.now.UTC().Format(time.RFC3339), c.agent, event, strings.Join(strings.Fields(err.Error()), " "))
}

// expandHome expands a leading "~/" (hook docs show such paths).
func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
