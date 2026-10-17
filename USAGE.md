# Using unasked

- [Commands](#commands)
- [Reading the output](#reading-the-output)
- [Hooks](#hooks)
- [Drop and undo](#drop-and-undo)
- [In CI](#in-ci)
- [Supported agents](#supported-agents)
- [Capture store and privacy](#capture-store-and-privacy)
- [Limits](#limits)

## Commands

```
unasked [--since <ref> | --staged] [--session <id>] [--agent <name>] [--all-sessions]
        [--all] [--format text|md|json] [--strict | --lenient] [--no-color]
unasked --drop [ids…]
unasked --patch [ids…]
unasked --keep <id> [--reason "…"]
unasked undo [--list] [--force] [snapshot]
unasked hook install|uninstall [--agent name] [--global|--project]
unasked hook status [--agent name]
unasked doctor
unasked stats [--since 30d]
unasked version
```

Run every command from anywhere inside the repository (a linked worktree
works too). `unasked --help` prints the same summary.

### `unasked` (the report)

Finds the latest agent session for this repository, diffs what the agent
changed, and scores every hunk against what you asked for and what the agent
said before the edit.

**Which session:** the newest captured session (see [Hooks](#hooks)) whose
edits overlap the diff; otherwise the newest session in the agent's own
store, from the last 7 days, whose working directory is this repository or a
subdirectory of it.

**Which diff:** your working tree and index (including untracked files that
aren't ignored) against the state of the working tree **when the session
started**, as recorded by the start hook. Uncommitted work you had before the
session is therefore never blamed on the agent. Without a start capture,
unasked falls back to the newest commit before the session's first message,
then to `HEAD`, and warns `pre-existing changes may be included`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--since <ref>` | | Diff from the merge base of `<ref>` and `HEAD` to the working tree instead (for example `--since origin/main`) |
| `--staged` | | Diff the index against `HEAD` only |
| `--session <id>` | newest | Use this session |
| `--agent <name>` | any | Only consider sessions of this agent (`claude-code`, `codex`) |
| `--all-sessions` | off | Union every session whose edits overlap the range, diffed from the oldest one's start |
| `--all` | off | Also list invited hunks, with the reason each was invited |
| `--format` | `text` | `text` for people, `md` for PR comments and job summaries, `json` for machines |
| `--strict` / `--lenient` | | Raise or lower both scoring thresholds by one fixed step |
| `--no-color` | | No ANSI underline in text output (also off when `NO_COLOR` is set or stdout isn't a terminal) |

`--since` and `--staged` can't be combined, nor can `--strict` and
`--lenient`. Flag values can't start with `--`. Each run saves the report as
`last.json` in the [capture store](#capture-store-and-privacy); the hunk ids
(`h01`, `h02`…) that `--drop` and `--keep` take refer to that report. The
agent's hook, and runs by an agent (an agent marker in the environment, or
stdin not a terminal), use `last-hook.json` instead, so a hook firing in the
background never renumbers the report you are reading.

### `--drop [ids…]`

Reverts hunks in the working tree. With no ids it drops every `unasked` hunk
of the last report, never an `unclear` one; `unclear` hunks are dropped only
when you name them. See [Drop and undo](#drop-and-undo) for the checks and
the cases it refuses. Prints the dropped ids and the snapshot to undo with,
and exits 0.

```
$ unasked --drop h01
dropped h01 (snapshot 20261017T044658.826220972Z; undo with: unasked undo)
```

`--drop` works on the working tree, not the index, so it can't be combined
with `--staged`. With `--since`, committed hunks are reverted as uncommitted
changes; unasked never creates commits or rewrites history.

### `--patch [ids…]`

Prints, instead of applying, the patch that would revert the same hunks.
Apply it yourself with `git apply --unidiff-zero`.

### `--keep <id> [--reason "…"]`

Accepts a hunk on the current branch so later reports stop counting it. A
keep is stored by the hunk's content hash, so it lasts across sessions until
that hunk's content changes, and it shows in the report's KEPT section.

A keep made by an agent (unasked detects an agent's environment, or stdin that
isn't a terminal) is recorded `by: agent`. Agent keeps are always listed, the
result is never better than `unclear` while one exists, and an agent can't
keep a `deletion`, `test` or `dependency` hunk at all:

```
unasked: agents cannot keep deletions, test/CI or dependency changes: drop it (unasked --drop h01) or leave it for the user
```

This is how a deletion, test or dependency change you did ask for leaves the
report. unasked never marks one `invited`; if your prompt named it, it is
listed `unclear` with `you may have asked for this (turn N) — confirm with
unasked --keep`, and you confirm it yourself:

```sh
unasked --keep h01 --reason "asked for in turn 2"
```

An `unclear` result doesn't block the agent through the hook; only `unasked`
does.

### `undo [--list] [--force] [snapshot]`

Restores the newest drop that hasn't been undone (or the named snapshot):
the files' exact bytes and modes, and the index. It refuses if a file the drop
touched, or the index, changed after the drop; `--force` overwrites those
changes. `--list` shows the snapshots:

```
SNAPSHOT                    CREATED           HUNKS  STATE  FILES
20261017T044528.297030000Z  2026-10-17 00:45  1      ready  internal/api/headers.go
```

The last 20 snapshots are kept.

### `hook install | uninstall | status`

See [Hooks](#hooks). `--agent` picks one agent (`claude-code` or `codex`);
without it, `install` covers every supported agent found on this machine and
`uninstall` and `status` cover all of them.

### `doctor`

One row per supported agent: whether it is installed here, its version from
the newest capture, the adapter level, where unasked's hook is installed
(`user`, `project` or `no`), and when this repository last captured a session.

```
AGENT        DETECTED  VERSION  LEVEL  HOOK  LAST CAPTURE
claude-code  yes       -        full   user  2026-10-17 00:45
codex        no        -        full   no    -
```

### `stats [--since 30d]`

Sums the reports saved with each captured session in this repository:

```
sessions: 1 (1 with unasked hunks)
hunks: 4 · unasked 1 (25%) · unclear 0 · invited 3
unasked labels: deletion 1
```

`--since` takes days (`30d`) or a Go duration (`72h`).

### `version`

```
unasked v0.1.0 (git 2.50.1)
```

## Reading the output

The text report, from the [demo](README.md):

```
Session demo-session (claude-code, 1 turn, 1 min)
transcript: present · adapter level: full
You asked: "Add rate limiting to the public API: 60 requests per minute per client,
           answer 429 with a Retry-After header"

4 hunks changed · 3 invited · 1 unasked · 0 unclear
result: unasked

UNASKED
  h01  internal/api/headers.go:7-7   deletion  ⚠ deleted header X-Request-Id still referenced in 2 files
        agent said: "I'm removing the deprecated X-Request-Id header from responses; it is not needed anymore."

unasked --drop h01   revert the unasked hunks
unasked --keep h01       accept it on this branch
```

- The header names the session, whether a transcript was found (`present`,
  `degraded` with the reason, or `none`), the adapter level, and your first
  prompt.
- Sections come in this order: **UNASKED**, **UNCLEAR**, **INVITED** (only
  with `--all`), **COLLATERAL (not counted)**, **KEPT**.
- Each hunk shows its id, `file:lines`, label and a one-line summary. `⚠`
  marks high-risk hunks (`deletion`, `test`, `dependency`). A deletion says
  how many other files still reference what it removed.
- `agent said:` quotes what the agent wrote just before the edit. If it
  contains a *tell*, a phrase like "while I'm here", "noticed", "clean up" or
  "for consistency", the phrase is underlined (wrapped in `_` without color)
  and the statement can't invite the hunk. Add your own tells, one Go regexp
  per line, in `tells.txt` under your config directory
  (`~/.config/unasked/` on Linux, `~/Library/Application Support/unasked/`
  on macOS, `%AppData%\unasked\` on Windows).
- An unclear hunk also shows why: `weak match`, `no agent edit found` (no
  edit tool call of the agent made it and the session ran no shell command,
  so you or another tool did), `no usable prompt`, or for a deletion, test
  or dependency change one of your prompts names,
  `you may have asked for this (turn N) — confirm with unasked --keep`.
- When the session did run shell commands, a hunk no edit tool call explains
  is judged on your prompts alone and, unless they ask for it, is unasked
  with the reason `made by a shell command or by you (no edit tool call)`.
  Such a hunk is never reverted by `--drop` without ids, never left in the
  suggested `--drop` command and never fed back to the agent on its own;
  name it to drop it.
- A rename, a mode change, or an empty file added or deleted has no lines;
  it is listed as a file-level hunk (`file_change` in JSON): a rename or an
  empty file deleted is a `deletion` (`test` on a test path), a mode change
  a `refactor`, an empty file added a `feature`. `--drop` reverses it.

**How a hunk is judged.** Test 1 compares the hunk with your prompts; Test 2
compares what the agent said before the edit with your prompts. A hunk is
`invited` when either test scores at least the invite threshold, `unclear`
when the better score is at least the low threshold, and `unasked`
otherwise. High-risk hunks are stricter: they are never `invited`, by either
test. Wording can't prove that you asked for a deletion ("don't remove the
X-Request-Id header" names it too), so the best your words can do is make
the hunk `unclear` with reason `you may have asked for this (turn N) —
confirm with unasked --keep`; only your own `--keep` accepts it. That takes
a Test 1 score of at least the invite threshold from a prompt that names the
change specifically; shared words alone leave the hunk `unasked`. For a
deletion, "specifically" means a removal verb in its plain form (`remove`,
`delete`, `drop`, `deprecate`, `strip`, `eliminate`, `get rid of`, `clean
out`, `rip out`) followed within four words by the whole removed name
(`remove the deprecated X-Request-Id header`, `delete the legacyPing
handler`); a one-word name needs at least 4 letters. "the client drops the
request id" doesn't count. A deletion with no removed name or string to
quote can't be lifted from `unasked` by your words. For a test file, a word
like `test`, `spec`, `assert` or `coverage`; for a CI file, `CI`,
`workflow`, `pipeline` or `GitHub Actions`; for a dependency change, a word
like `bump`, `upgrade` or `dependency`, or the dependency's full name
(`golang.org/x/time`, `@types/node`; a one-word name like `requests` only
next to a word like `add`, `install` or `library`). Without such a prompt
the hunk is `unasked`, with reason `asked: generic overlap only` when the
raw score reached the invite threshold, while `asked` still shows the raw
score. When there is no transcript or no usable prompt, high-risk hunks are
`unasked` and everything else is `unclear`.

`--format md` prints the same report sized for a pull-request comment or a
job summary (UNCLEAR and COLLATERAL fold into `<details>`).

`--format json` prints the report as JSON. Its shape is version 1, published
as [`schema/v1.json`](schema/v1.json); fields may be added, never renamed or
removed.

```json
{
  "version": 1,
  "agent": "claude-code",
  "session": "demo-session",
  "transcript": "present",
  "adapter": { "name": "claude-code", "version": "2.1.289", "level": "full", "degraded_reason": "" },
  "range": { "base": "466c4ce4…", "head": "ddd8a87c…", "base_source": "captured" },
  "thresholds": { "invite": 0.2, "low": 0.15 },
  "prompts": [
    { "turn": 1, "text": "Add rate limiting to the public API: 60 requests per minute per client, answer 429 with a Retry-After header" }
  ],
  "result": "unasked",
  "hunks": [
    {
      "id": "h01",
      "file": "internal/api/headers.go",
      "lines": "7-7",
      "verdict": "unasked",
      "label": "deletion",
      "severity": "error",
      "asked": 0.44,
      "said": 0.149,
      "tell": null,
      "statement": "I'm removing the deprecated X-Request-Id header from responses; it is not needed anymore.",
      "turn": 1,
      "removed_symbols": ["X-Request-Id"],
      "references_elsewhere": 2,
      "summary": "deleted header X-Request-Id still referenced in 2 files",
      "reason": "asked: generic overlap only",
      "hash": "3f2bbfe91eb9",
      "high_risk": true
    }
  ],
  "kept": [],
  "stats": { "hunks": 4, "invited": 3, "unasked": 1, "unclear": 0, "collateral": 0, "binary": 0 },
  "warnings": [],
  "turns": 1,
  "duration": "1 min"
}
```

Key fields:

- **`result`**: `clean`, `unclear` or `unasked`. Matches the exit code (0, 1, 2).
  A report with hunks but no transcript is never `clean`, and neither is one
  with changed binary files (they aren't judged).
- **`hunks[].verdict`**: `invited`, `unclear` or `unasked`.
- **`hunks[].label`**: `deletion`, `test`, `dependency`, `feature`,
  `refactor`, `collateral` or `unknown`. **`high_risk`** is true for the first three.
- **`hunks[].severity`**: `error` (unasked and high-risk), `warning` (any
  other unasked or unclear hunk), `info` (folded collateral); absent when
  invited.
- **`hunks[].asked`** and **`said`**: the Test 1 and Test 2 scores, 0 to 1.
  **`tell`** is the tell phrase found in `statement`, or `null`.
- **`hunks[].reason`**: why it got its verdict, for example `asked: turn 1`,
  `agent said: turn 2`, `required by h03`, `collateral`, `weak match`,
  `no match`, `asked: generic overlap only`, `no agent edit found`,
  `made by a shell command or by you (no edit tool call)` or
  `no usable prompt`.
- **`hunks[].file_change`** and **`old_file`**: set on a file-level hunk:
  `rename` (from `old_file`), `mode`, `add` or `delete` (an empty file).
  Its `lines` is `0-0`.
- **`hunks[].hash`**: the hunk's identity: the first 12 hex digits of
  SHA-256 over its path, removed lines and added lines (for a file-level
  hunk, over its kind, paths and modes). Ids like `h01` are
  per-run display labels; `--drop` and `--keep` check hashes.
- **`range.base_source`**: where the diff's base came from: `captured`
  (session start), `reflog` (the last commit before the session), `head`,
  `since` or `staged`.
- **`adapter.level`** and **`degraded_reason`**: see
  [Supported agents](#supported-agents).

On an error, unasked prints one line to stderr (`unasked: <reason>`, with the
fix when there is one) and exits 3.

## Hooks

```sh
unasked hook install                        # every supported agent found here, user scope
unasked hook install --agent codex          # one agent
unasked hook install --project              # this repository's config instead
unasked hook status
unasked hook uninstall
```

The hooks run `unasked hook run --agent <name>` with an absolute path to the
binary, at session start, on each prompt, and when the agent stops.

| Agent | User scope (default) | `--project` |
|-------|----------------------|-------------|
| Claude Code | `~/.claude/settings.json` | `.claude/settings.json` |
| Codex | `~/.codex/hooks.json` (`$CODEX_HOME/hooks.json` if set) | `.codex/hooks.json` |

Installing merges into the existing file: other settings, their order and
the file's mode are kept, the original is backed up once as
`<file>.unasked-bak`, and installing twice changes nothing. `uninstall`
removes only unasked's entries. Codex runs a new or changed hook only after
you trust it: open `/hooks` in Codex once after installing.

**At session start** (and on each new prompt) the hook records a snapshot of
the working tree, the base the report diffs against. **When the agent
stops**, it copies the transcript into the [capture store](#capture-store-and-privacy)
and runs the report. If the result is `unasked`, it blocks the stop and hands
the report back to the agent as its next instruction:

```
[unasked report]
Session demo-session (claude-code, 1 turn, 1 min)
...
UNASKED
  h01  internal/api/headers.go:7-7   deletion  ⚠ deleted header X-Request-Id still referenced in 2 files
...
Drop with: unasked --drop <ids>  — or keep one with a reason: unasked --keep <id> --reason "<why the task needed it>". Deletions, test/CI and dependency changes can only be kept by the user.
```

The agent then drops the hunks or keeps them with a reason, which you see in
the KEPT section. The `[unasked report]` marker keeps this message out of
the prompts unasked scores.

Loop safety: the hook doesn't block a stop that is already a continuation of
a block, and it blocks at most 3 times in a row per task. After that it lets
the agent stop and adds `3 consecutive reports ignored; not blocking again
this session` to the report. A clean result or a new prompt resets the count.

The hook never fails the agent: any error is appended to
`<git-dir>/unasked/hook.log` and the hook exits 0. Outside a git repository it
does nothing.

## Drop and undo

Before changing anything, `--drop`:

1. Re-diffs and checks that every chosen hunk's content hash still matches
   the report it was chosen from. If not: `hunk no longer matches the working
   tree: re-run unasked`.
2. Refuses if the index has staged changes in a file it would touch
   (unrelated staged files are fine and left alone).
3. Refuses to drop a hunk that an invited or kept hunk depends on, naming the
   dependent (`h02 is required by h05`).
4. Refuses symlinks, submodules and any path outside the repository, and
   checks that the reverse patch applies cleanly (`git apply --check`).
5. Snapshots the exact bytes and mode of every file it will touch, plus the
   index, under `<git-dir>/unasked/snapshots/`.

Then it applies the reverse patch in one step and re-runs the report: every
dropped hunk must be gone and every other hunk byte-identical. If any of that
fails, it restores the snapshot and exits 3, so you get all the hunks dropped
or none. Each refusal exits 3 and changes nothing.

Dropping a new file deletes it (after it is in the snapshot). With no ids,
`--drop` takes only `unasked` hunks; when nothing was scored (no transcript),
it refuses and asks for explicit ids.

`unasked undo` restores the snapshot exactly, file bytes and index. Only one
drop or undo runs at a time per repository (`<git-dir>/unasked/revert.lock`).

## In CI

Report on every pull request with the GitHub Action:

```yaml
# .github/workflows/unasked.yml
name: unasked
on: pull_request

jobs:
  report:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0          # unasked needs the merge base
      - uses: sadayamuthu/unasked@v0
```

It downloads the release binary (checksum-verified), runs
`unasked --since origin/<base> --all-sessions --format md`, and writes the
report to the job summary. Linux and macOS runners, x64 and arm64.

**CI has no transcript.** Agent sessions live on the machine where the agent
ran, so in CI unasked judges the diff alone: every `deletion`, `test` and
`dependency` hunk is `unasked`, and every other hunk is `unclear`. With the
default `fail-on: unasked`, a pull request that deletes code or touches tests,
CI files or dependencies fails the check until someone looks at it. Use
`fail-on: never` to only report.

| Input | Default | |
|-------|---------|---|
| `fail-on` | `unasked` | `unasked` (exit 2), `unclear` (exit 1 or 2) or `never` |
| `comment` | `false` | `true` also posts the report as a pull-request comment, updated in place on later pushes (needs `pull-requests: write`) |
| `base` | the PR's base branch, else the default branch | Branch to diff from |
| `version` | `latest` | Release to use, such as `0.1.0` |
| `working-directory` | `.` | Repository to check |
| `github-token` | `github.token` | Token used to post the comment |

Outputs: `result` (`clean`, `unclear` or `unasked`), `json` (path to the full
JSON report) and `version`. For example:

```yaml
permissions:
  contents: read
  pull-requests: write
steps:
  - uses: actions/checkout@v4
    with:
      fetch-depth: 0
  - uses: sadayamuthu/unasked@v0
    id: unasked
    with:
      fail-on: never
      comment: true
  - if: steps.unasked.outputs.result == 'unasked'
    run: jq '.hunks[] | select(.verdict == "unasked")' "${{ steps.unasked.outputs.json }}"
```

`@v0` follows the latest v0.x release of the action, and `version: latest`
downloads the newest binary. For fully reproducible runs, pin both:
`uses: sadayamuthu/unasked@v0.1.0` with `version: 0.1.0`.

**Without the action** (other CI systems, or to build from source):

```yaml
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - run: go install github.com/sadayamuthu/unasked/cmd/unasked@latest
      - name: Report hunks since the base branch
        run: unasked --since "origin/$BASE" --all-sessions --format md >> "$GITHUB_STEP_SUMMARY" || [ $? -eq 1 ]
        env:
          BASE: ${{ github.base_ref }}
```

`|| [ $? -eq 1 ]` lets `unclear` (exit 1) pass and fails on `unasked`
(exit 2) and errors (exit 3).

## Supported agents

| Agent | Level in this release | Sessions read from | Stop hook |
|-------|-----------------------|--------------------|-----------|
| Claude Code | **full** (preview) | `~/.claude/projects/<dir>/<session>.jsonl`, plus subagent transcripts | `Stop`, blocks with the report |
| Codex | **full** (preview) | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` | `Stop`, blocks with the report |

Both are built for full support and marked preview until real recorded
sessions validate it: so far they are calibrated on synthetic fixtures only
(see the [release checklist](CONTRIBUTING.md#release-checklist)).

Levels, from most to least: **full** (your prompts, the agent's words before
each edit, and its edits), **report-only** (as full, but the hook can only
observe, not feed back), **partial**, **prompts-only** (only your prompts:
Test 1 still runs) and **diff-only** (no transcript: high-risk hunks are
unasked, the rest unclear).

Each adapter guards against format changes. If it meets a transcript it
can't read, it drops to a lower level, says why in the report header
(`transcript: degraded: …`) and `adapter.degraded_reason`, and never crashes
or reports `clean` on a guess.

**Planned** (not in this release), with the level each is designed to reach:

| Agent | Target level |
|-------|--------------|
| Gemini CLI | full |
| GitHub Copilot (VS Code agent mode) | full |
| Cursor | full |
| Google Antigravity | full (prompts-only if its transcript can't be parsed) |
| Amp | full (CLI) |
| Windsurf | report-only |
| Cline | report-only |
| opencode | partial |
| Aider | partial |
| Kilo Code | prompts-only |
| Kiro | prompts-only / diff-only |

Roo Code is archived and won't be supported; Kilo Code replaces it. Until an
agent is supported, `unasked --since <ref>` still gives a diff-only report on
its changes.

## Capture store and privacy

Everything unasked writes lives in `<git-dir>/unasked/` (`.git/unasked/` in a
normal checkout; each linked worktree has its own), so none of it can be
committed:

| Path | What |
|------|------|
| `sessions/<agent>-<session>/` | captured transcript, `meta.json` (start snapshot, agent version) and that session's `report.json` |
| `last.json` | your last report, for your `--drop` and `--keep` ids |
| `last-hook.json` | the last report the hook or an agent run saved, for the agent's ids |
| `keep.json` | keeps, by content hash and branch |
| `snapshots/` | drop snapshots for `undo` (last 20) |
| `hook.log` | hook errors |

Transcripts can contain secrets, so captured files are mode `0600` and
directories `0700`. On Windows they get default file permissions for now, and
`unasked doctor` says so. Captures are pruned to the newest 50 sessions and
nothing older than 30 days, whichever keeps fewer.

unasked has no network code: transcripts never leave the machine. It reads
the agents' own session stores read-only. Outside `<git-dir>/unasked/` it
writes only the working tree and index (`--drop`, `undo`) and the agents'
hook config files (`hook install`, `hook uninstall`).

## Limits

- Scoring is lexical: it compares words (stemmed, English) in your prompts,
  the agent's statements and the hunk. A prompt that happens to share words
  with an unrelated hunk can invite it (never a deletion, test or dependency
  change), and a terse prompt can leave real work `unclear`. `--strict` and
  `--lenient` shift the thresholds.
- On the labelled fixture set, 2 of 17 listed hunks (11.8%) are ones a
  human would call invited (see [CONTRIBUTING](CONTRIBUTING.md#fixtures)).
  The set is small and synthetic so far.
- Prompts are English-only for stemming and tells.
- Symbol extraction uses Go's parser for Go and patterns for other languages;
  struct fields aren't tracked as symbols yet.
- A Claude Code turn with several edits in a row gives the statement only to
  the first, so the later ones rely on Test 1.
- On Windows repositories with `core.autocrlf=true` and no start capture, a
  file can show as one whole-file hunk; dropping it is still verified and
  undoable.
- Only Claude Code and Codex are read in this release.
