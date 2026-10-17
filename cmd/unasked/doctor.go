package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sadayamuthu/unasked/internal/adapter"
	"github.com/sadayamuthu/unasked/internal/gitx"
	"github.com/sadayamuthu/unasked/internal/model"
	"github.com/sadayamuthu/unasked/internal/report"
)

// hookStatus reports whether unasked's hook is installed for an agent.
// Hook installers arrive with `unasked hook`; until then it is unknown.
func hookStatus(name string) string { return "-" }

// runDoctor prints one row per registered adapter. Outside a repository the
// capture columns are empty.
func runDoctor(args []string, dir string, stdout io.Writer) (int, error) {
	if len(args) > 0 {
		return 0, fmt.Errorf("doctor: unexpected %q", args[0])
	}
	var captured []adapter.Session
	if r, err := gitx.Open(dir); err == nil {
		if captured, err = adapter.ListCaptured(r); err != nil {
			return 0, err
		}
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tDETECTED\tVERSION\tLEVEL\tHOOK\tLAST CAPTURE")
	for _, a := range adapter.All() {
		detected := "no"
		if a.Detect() {
			detected = "yes"
		}
		ver, last := "-", "-"
		for _, s := range captured { // newest first
			if s.Agent != a.Name() {
				continue
			}
			if s.Meta.AgentVersion != "" {
				ver = s.Meta.AgentVersion
			}
			last = s.Meta.Updated.Local().Format("2006-01-02 15:04")
			break
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name(), detected, ver, a.MaxLevel(), hookStatus(a.Name()), last)
	}
	if err := tw.Flush(); err != nil {
		return 0, err
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stdout, "note: captured transcripts use default file permissions on Windows")
	}
	return model.ExitClean, nil
}

// runStats summarises the report.json written into each captured session.
func runStats(args []string, dir string, stdout io.Writer) (int, error) {
	var since time.Duration
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if name != "--since" {
			return 0, fmt.Errorf("stats: unexpected %q", args[i])
		}
		if !hasVal {
			if i+1 >= len(args) {
				return 0, fmt.Errorf("stats: --since needs a duration such as 30d")
			}
			i++
			val = args[i]
		}
		d, err := parseAge(val)
		if err != nil {
			return 0, err
		}
		since = d
	}
	r, err := gitx.Open(dir)
	if err != nil {
		return 0, err
	}
	sessions, err := adapter.ListCaptured(r)
	if err != nil {
		return 0, err
	}
	var reps []*report.Report
	for _, s := range sessions {
		if since > 0 && time.Since(s.Meta.Updated) > since {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Path, "report.json"))
		if err != nil {
			continue
		}
		var rep report.Report
		if json.Unmarshal(b, &rep) != nil {
			continue
		}
		reps = append(reps, &rep)
	}
	if len(reps) == 0 {
		fmt.Fprintln(stdout, "no captured sessions yet")
		return model.ExitClean, nil
	}
	var withUnasked int
	var st report.Stats
	labels := map[string]int{}
	for _, rep := range reps {
		if rep.Stats.Unasked > 0 {
			withUnasked++
		}
		st.Hunks += rep.Stats.Hunks
		st.Invited += rep.Stats.Invited
		st.Unasked += rep.Stats.Unasked
		st.Unclear += rep.Stats.Unclear
		for _, h := range rep.Hunks {
			if h.Verdict == string(model.VerdictUnasked) {
				labels[h.Label]++
			}
		}
	}
	rate := 0
	if st.Hunks > 0 {
		rate = st.Unasked * 100 / st.Hunks
	}
	fmt.Fprintf(stdout, "sessions: %d (%d with unasked hunks)\n", len(reps), withUnasked)
	fmt.Fprintf(stdout, "hunks: %d · unasked %d (%d%%) · unclear %d · invited %d\n", st.Hunks, st.Unasked, rate, st.Unclear, st.Invited)
	if len(labels) > 0 {
		names := make([]string, 0, len(labels))
		for l := range labels {
			names = append(names, l)
		}
		sort.Slice(names, func(i, j int) bool {
			if labels[names[i]] != labels[names[j]] {
				return labels[names[i]] > labels[names[j]]
			}
			return names[i] < names[j]
		})
		parts := make([]string, len(names))
		for i, l := range names {
			parts[i] = fmt.Sprintf("%s %d", l, labels[l])
		}
		fmt.Fprintf(stdout, "unasked labels: %s\n", strings.Join(parts, " · "))
	}
	return model.ExitClean, nil
}

// parseAge reads "30d" (days) or a Go duration such as "72h".
func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if d, err := strconv.Atoi(n); err == nil && d > 0 {
			return time.Duration(d) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("stats: bad --since %q: use a duration such as 30d", s)
}
