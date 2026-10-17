# Fixtures

Labelled agent sessions for the release gates (spec §10). `internal/fixtures`
replays each case through the real report pipeline and checks:

- **Recall gate:** no hunk labelled `unasked` is reported `invited`
  (folded collateral included). This never relaxes.
- **False-alarm ceiling:** at most 20% of listed hunks (`unasked` + `unclear`)
  are labelled `invited`. If the two cannot both hold, recall wins: run with
  `UNASKED_RELAX_CEILING=1` and publish the measured rate (printed by every
  `TestGates` run) in the release notes.

```
go test ./internal/fixtures -run TestGates -v                    # the gates
go test ./internal/fixtures -run TestGates -update               # rewrite expected.json
go test ./internal/fixtures -run TestCalibrate -calibrate -v     # re-pick invite.Default
go test ./internal/fixtures -run TestPerformanceBudget -v        # 5,000 files, 40 hunks, 300 turns < 1 s
```

## Case layout

`testdata/fixtures/<agent>/<case>/`, where `<agent>` is the adapter name
(`claude-code`, `codex`):

| Path | What it is |
|---|---|
| `before/` | files committed before the session |
| `pre/` | optional: the developer's uncommitted edits made before the session started; they are part of the captured baseline and must never be reported |
| `after/` | every file the agent changed or created, whole; `<path>.deleted` (empty) marks a deletion |
| `transcript.jsonl` | the agent's transcript; `@CWD@` stands for the repository directory |
| `labels.json` | `{"<file>:<key line>": "invited" \| "unasked"}`, one entry per reported hunk |
| `expected.json` | the report JSON; regenerate with `-update` and review the diff |

The key line is the hunk's first non-blank added line, or its first non-blank
removed line when it adds none, trimmed. Every reported hunk needs a label and
every label must match a hunk, so a stale label or a pre-existing edit that
leaks into the report fails the test.

A file name ending in `.nofmt` is written without the suffix. Use it for Go
sources that are deliberately not gofmt-clean (formatter sweeps), so
`gofmt -l .` on this repository stays quiet.

The harness commits `before/`, writes `pre/`, records the session start
(the captured baseline), writes `after/`, then captures the transcript as the
stop hook would, so the baseline logic is exercised for real.

## Labelling

Labels are human truth: decide each hunk from what the prompts asked for,
before looking at what unasked reports, and never change a label to make a
gate pass. If a gate fails, fix the scoring or labelling rule the case
exposes (with a unit test in that package).

- `invited`: the task needs it — the change itself, code it cannot work
  without (helpers it calls, imports, config it reads, call sites a rename
  forces), and formatting of a file the task legitimately changes (spec §6.4
  folds that collateral).
- `unasked`: everything else — refactors, deletions, loosened tests,
  dependency bumps, formatter sweeps over files the task never touched,
  drive-by comment fixes.

## Recording a real session

1. Install the hooks (`unasked hook install`) and run the agent on a real task
   in a scratch clone.
2. Copy the capture directory `.git/unasked/sessions/<agent>-<session>/`
   (`transcript.jsonl`, plus `subagents/` for Claude Code).
3. Scrub secrets and private paths: replace the repository path with `@CWD@`
   and remove tokens, keys, hostnames and personal data from prompts and tool
   output.
4. Build `before/` from the commit the session started on, `pre/` from any
   uncommitted work present at the start, and `after/` from the working tree
   when the session stopped.
5. Run `go test ./internal/fixtures -run TestGates -v`; it names every hunk
   missing a label. Write `labels.json` by hand from the prompts, then
   `-update` to create `expected.json`.

A tool ships above `prompts-only` only with at least 3 real (recorded, not
synthetic) labelled cases. The seven cases here are synthetic, built from each
adapter's documented record shapes.
