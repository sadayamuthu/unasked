# Contributing to unasked

Thanks for helping. unasked is only useful if two things hold: it doesn't
miss changes you didn't ask for (zero misses on the labelled fixture set, and
we treat any miss as the most serious kind of bug), and `--drop` and `undo`
never leave your working tree in a state other than the one promised. Most of
this guide is about keeping them: every change in scoring comes with a
labelled session that proves it, and every change to `--drop` or `undo` comes
with a test that round-trips the bytes.

- [Ways to help](#ways-to-help)
- [Reporting a wrong verdict](#reporting-a-wrong-verdict)
- [Development setup](#development-setup)
- [Fixtures](#fixtures)
- [Code layout](#code-layout)
- [Adding an agent adapter](#adding-an-agent-adapter)
- [Adding a tell](#adding-a-tell)
- [Pull requests](#pull-requests)
- [Security](#security)
- [License](#license)

## Ways to help

- **Report a missed or wrong verdict.** This is the most useful
  contribution. A hunk you didn't ask for that came out `invited` is a miss
  and the most serious kind of bug; see the next section.
- **Record real sessions.** All fixtures so far are synthetic. Real, scrubbed
  sessions from Claude Code and Codex are what the scoring needs most (see
  [Recording a real session](testdata/fixtures/README.md#recording-a-real-session)).
- **Run it after your agent sessions** and tell us what it found, in an issue
  or a discussion. Real-world results guide what to fix next.
- **Pick up an issue** labelled
  [`good first issue`](https://github.com/sadayamuthu/unasked/labels/good%20first%20issue)
  or [`help wanted`](https://github.com/sadayamuthu/unasked/labels/help%20wanted).
- **Add an agent.** Eleven are planned; see
  [Supported agents](USAGE.md#supported-agents) and
  [Adding an agent adapter](#adding-an-agent-adapter).

For anything larger than a small fix, open an issue first so we can agree on
the approach before you write the code.

## Reporting a wrong verdict

Please include:

1. `unasked version` and `unasked doctor` output.
2. The command you ran and its `--format json` output. **Redact it first:**
   the `prompts` and `statement` fields quote your session, so replace
   anything private (tokens, hostnames, customer data) with placeholders.
3. Which hunk got the wrong verdict, what you expected, and why: what did
   you ask for, and does this hunk belong to it?
4. If you can, the hunk itself (`git diff`) and the agent's lines around the
   edit, reduced to a made-up example if the code is private.

Never attach a raw transcript or the contents of `.git/unasked/`: transcripts
can contain secrets. A reduced example becomes a [fixture](#fixtures), which
is the fastest way to get a fix, because it becomes the test.

## Development setup

You need:

- **Go 1.25** or newer. No C compiler: unasked is pure Go (`CGO_ENABLED=0`).
- **git 2.30** or newer.

```sh
git clone https://github.com/sadayamuthu/unasked.git
cd unasked
go build -o unasked ./cmd/unasked
./unasked version
```

Before pushing, run what CI runs:

```sh
gofmt -l .                # must print nothing
go vet ./...
go mod tidy && git diff --exit-code go.mod go.sum
UNASKED_RELAX_CEILING=1 go test ./...      # full suite, incl. the 5,000-file perf test
UNASKED_RELAX_CEILING=1 go test -race -short ./...
for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go build -o /dev/null ./cmd/unasked
done
```

`go test -short ./...` skips the performance tests for a faster loop. CI runs
the suite on Linux, macOS and Windows; the `--drop`/`undo` tests include
CRLF, rename, unicode-path and fuzz cases, so run them on Windows if you touch
`internal/revert` or `internal/hunk`.

To try the hooks without touching your real agent config, point
`UNASKED_HOME` at a scratch directory: unasked then reads and writes
`$UNASKED_HOME/.claude` and `$UNASKED_HOME/.codex` instead of your home
directory. [`demo/setup.sh`](demo/setup.sh) shows a whole session driven
through the real hooks this way.

## Fixtures

Scoring is pinned by labelled agent sessions in
[`testdata/fixtures/<agent>/<case>/`](testdata/fixtures). Each case is a tiny
repository before and after the session, the agent's transcript, and a
human's verdict for every hunk:

```
testdata/fixtures/claude-code/rate-limit/
  before/             files committed before the session
  pre/                optional: your uncommitted work from before the session
  after/              every file the agent changed or created, whole
  transcript.jsonl    the transcript; @CWD@ stands for the repository path
  labels.json         {"<file>:<key line>": "invited" | "unasked"}
  expected.json       the report, regenerated with -update
```

[`testdata/fixtures/README.md`](testdata/fixtures/README.md) has the full
format, the labelling rules and how to record and scrub a real session.

**Labels are human truth.** Decide each hunk from what the prompts asked for,
before looking at what unasked reports, and never change a label to make a
gate pass. If a gate fails, fix the scoring or labelling rule the case
exposes, with a unit test in that package.

**The gates:** `TestGates` replays every case through the real pipeline,
starting from a captured baseline as the hooks would, and enforces:

- **Recall: zero misses.** No hunk labelled `unasked` may be reported
  `invited`. This never relaxes.
- **False alarms: at most 20%** of listed hunks (`unasked` + `unclear`)
  labelled `invited`.

Honest status: on the current seven synthetic cases (four Claude Code, three
Codex) the recall gate holds with zero misses, and the measured false-alarm
rate is **11.8%** (2 of 17 listed hunks), under the target. Both false alarms
are router hunks in `claude-code/rate-limit-realistic`, whose prompt shares
few words with them. High-risk hunks are never reported `invited`, so one
labelled `invited` (a deletion the prompt asked for) is at best `unclear`
and counts as a false alarm; no case has one yet. When the two gates
conflict, recall wins, so CI still runs with `UNASKED_RELAX_CEILING=1`, which fails on any miss and prints the
measured rate. Seven synthetic cases are not much evidence, which is why real
sessions are the most useful contribution.

```sh
go test ./internal/fixtures -run TestGates -v                    # the gates
go test ./internal/fixtures -run TestGates -update               # rewrite expected.json
go test ./internal/fixtures -run TestCalibrate -calibrate -v     # re-pick the thresholds
go test ./internal/fixtures -run TestPerformanceBudget -v        # 5,000 files, 40 hunks, 300 turns < 1 s
```

**Every scoring change needs a fixture or a unit test.** For a miss, add a
case (or a hunk to one) that is reported `invited` before your change and
not after it. Review every `expected.json` diff that `-update` produces.

## Code layout

| Path | What it does |
|------|--------------|
| `cmd/unasked` | CLI entry point: the report, `--drop`, `--patch`, `--keep`, `undo`, `hook`, `doctor`, `stats`, `version` |
| `internal/gitx` | Runs git. The only package that executes `git`; every call sets `GIT_TERMINAL_PROMPT=0`, `LC_ALL=C` and `core.quotepath=off` |
| `internal/gittest` | Throwaway git repositories for tests |
| `internal/baseline` | Snapshots the working tree (tracked and untracked) as a git tree without touching the real index |
| `internal/hunk` | Parses zero-context diffs into content-addressed hunks; symbol extraction |
| `internal/adapter` | Adapter interface, levels, registry, version guard, capture store, session and range selection |
| `internal/adapter/claudecode`, `internal/adapter/codex` | Transcript readers for each agent |
| `internal/invite` | Tokenizer, IDF weights, Test 1 and Test 2 scores, tells, edit alignment, verdict rules, thresholds |
| `internal/label` | Labels, high-risk, severity, summaries, cross-references for deletions, collateral folding |
| `internal/keep` | `keep.json`: keeps by content hash and branch, agent-keep rules |
| `internal/pipeline` | Runs the whole report: select, diff, score, label, keep |
| `internal/report` | The JSON v1 report and the text and markdown views; `last.json` |
| `internal/revert` | `--drop` checks, snapshot, apply, verify and `undo` |
| `internal/hook` | Per-agent hook installers and `hook run` |
| `internal/model` | Types shared across packages, exit codes |
| `internal/fixtures` | Fixture loader, release gates, calibration, performance budget |
| `schema/v1.json` | JSON schema of the report; fields may be added, never renamed or removed |

## Adding an agent adapter

An adapter implements `adapter.Adapter` in
[`internal/adapter/adapter.go`](internal/adapter/adapter.go):

```go
type Adapter interface {
	Name() string                                         // "claude-code", "codex", …
	Detect() bool                                         // installed or has data on this machine
	Find(repoDir string, since time.Time) ([]Session, error) // sessions whose cwd is repoDir or below it
	Read(s Session) (*model.Transcript, Capabilities, error)  // from the agent's own store, read-only
	ParseCaptured(dir string) (*model.Transcript, Capabilities, error) // from a capture dir
	MaxLevel() Level
}
```

Use the Claude Code and Codex adapters as models:

1. Put it in `internal/adapter/<name>/`, call `adapter.Register` from
   `init`, and add a blank import in `cmd/unasked/main.go` and
   `internal/fixtures`.
2. Fill `model.Transcript`: prompts in order (skip tool results, injected
   system text and any message starting with `[unasked report]`), and one
   `EditEvent` per edit with the file, the new text and the agent's text just
   before it. Subagent edits join the parent's trail; their prompts don't.
3. **Version-guard.** Skip unknown record types. If the format isn't one you
   know, return a lower level with the reason in `Capabilities.Degraded`, or
   an error: `adapter.Guarded` turns errors and panics into a diff-only
   report that says why. Never guess and report `clean`. A transcript still
   being written (last line cut off) must parse every complete line.
4. Honor `UNASKED_HOME` for every path under the user's home, so tests and
   the demo never touch real config.
5. If the agent has hooks, add a `hook.Spec` in `internal/hook/` (config path
   for user and project scope, install/uninstall, payload parsing, how to
   feed the report back) with golden-file tests like the existing ones, and add it to
   `hook.Specs()`.
6. **Fixtures.** At least 3 labelled cases, plus a garbled-input test that
   shows the adapter degrades instead of panicking. The rule for a new
   adapter is that it ships above `prompts-only` only with **at least 3
   real** (recorded and scrubbed, not synthetic) labelled sessions; until
   then its `MaxLevel` stays at `prompts-only` or below, whatever the parser
   can do. (Claude Code and Codex are at `full` on synthetic fixtures for
   now; real sessions for them are the next thing the fixture set needs.)
7. Document it in [Supported agents](USAGE.md#supported-agents).

Open an issue before starting, so two people don't build the same adapter.

## Adding a tell

Tells are phrases with which an agent admits an edit was incidental ("while
I'm here", "took the liberty"). A statement containing one can't invite its
hunk. They live in
[`internal/invite/tells.txt`](internal/invite/tells.txt), one Go regexp per
line, matched case-insensitively at word boundaries.

1. Add the pattern, and a sample sentence for it in `tellSamples` in
   `internal/invite/tells_test.go`.
2. Run the fixture gates: a tell that is too broad stops real work from being
   invited by the agent's statement and raises the false-alarm rate.

## Pull requests

- Keep each PR to one change; small PRs get reviewed faster.
- Use the commit message style in the history:
  `type(scope): summary`, for example
  `fix(revert): keep file mode when dropping a new file`. Types in use:
  `feat`, `fix`, `test`, `docs`, `build`, `chore`.
- CI must pass on Linux, macOS and Windows.
- Don't change the meaning of a JSON v1 field or the hunk hash format; add a
  field instead.
- Update README.md or USAGE.md when you change behaviour a user can see.

The repository's social preview image is
[`.github/social-preview.png`](.github/social-preview.png), rendered from
`.github/social-preview.svg` with `rsvg-convert -w 1280 -h 640`. GitHub has no
API for it: upload the PNG by hand in **Settings → General → Social preview**
after changing it.

## Security

Please don't report security problems in a public issue. Use
[**Report a vulnerability**](https://github.com/sadayamuthu/unasked/security/advisories/new)
on the repository's Security tab instead. That includes any way a transcript
could leave `.git/unasked/` or be committed, and any way `--drop` or `undo`
could leave the working tree in a state other than the one promised.

## License

unasked is licensed under the [MIT License](LICENSE). By contributing, you
agree that your contributions are licensed under the same license.
