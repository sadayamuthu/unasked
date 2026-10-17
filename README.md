# unasked

[![CI](https://github.com/sadayamuthu/unasked/actions/workflows/ci.yml/badge.svg)](https://github.com/sadayamuthu/unasked/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sadayamuthu/unasked?sort=semver)](https://github.com/sadayamuthu/unasked/releases/latest)
[![Homebrew](https://img.shields.io/badge/brew-sadayamuthu%2Ftap%2Funasked-FBB040?logo=homebrew&logoColor=white)](https://github.com/sadayamuthu/homebrew-tap)
[![Docker](https://img.shields.io/badge/docker-ghcr.io%2Fsadayamuthu%2Funasked-2496ED?logo=docker&logoColor=white)](https://github.com/sadayamuthu/unasked/pkgs/container/unasked)
[![deb/rpm](https://img.shields.io/badge/packages-.deb%20%7C%20.rpm-A81D33?logo=debian&logoColor=white)](https://github.com/sadayamuthu/unasked/releases/latest)
[![License](https://img.shields.io/github/license/sadayamuthu/unasked)](LICENSE)

See what your coding agent changed that you never asked for. When an AI
coding agent stops, `unasked` reads what you asked for and what the agent
said before each edit, scores every hunk of the diff, and lists the ones
nobody asked for: the "unused" function it deleted, the test it loosened,
the dependency it bumped, the lint sweep over a file the task never
touched. Then `unasked --drop` reverts exactly those hunks and nothing else.

![Claude Code was asked to add rate limiting; unasked reports that it also deleted the X-Request-Id header, still referenced in 2 files, then drops that hunk and undoes the drop](demo/demo.gif)

It is local and deterministic: no model, no network, nothing leaves your
machine. A report never touches your working tree. `--drop` checks every hunk against the
report, snapshots each file it touches, and verifies the result, so the
working tree ends up either with exactly those hunks reverted or unchanged;
`unasked undo` restores the snapshot byte for byte.

## What it flags

Every hunk gets a verdict (`invited`, `unclear` or `unasked`) and a label:

| Label | Hunk | Severity when unasked |
|-------|------|-----------------------|
| `deletion` | Removes a function, type, file, header, route, flag or config key; names other files that still reference it | error |
| `test` | Changes a test, test data or a CI/workflow file | error |
| `dependency` | Changes `go.mod`, `package.json`, a lockfile, `requirements*.txt` and the like | error |
| `feature` | Adds an exported symbol, route, flag or config key | warning |
| `refactor` | Changes the body or signature of an existing symbol | warning |
| `collateral` | Whitespace-only or import-only changes, or an edit made by a formatter or generator | warning |
| `unknown` | Anything else | warning |

`deletion`, `test` and `dependency` are high-risk and are never `invited`
automatically. When one of your prompts names the change specifically
("remove the X-Request-Id header"), the hunk is `unclear` with "you may have
asked for this": confirm it with `unasked --keep`. Otherwise it is `unasked`.
The agent's explanation never helps, and an agent cannot `--keep` them.
`unclear` hunks are warnings. Formatting in a file the task legitimately
changed is folded into one "collateral" line and not counted.

## Install

unasked needs **git 2.30 or newer** on your `PATH`.

**Homebrew** (macOS and Linux):

```sh
brew install sadayamuthu/tap/unasked
```

**Debian / Ubuntu** (`.deb`, amd64 and arm64):

```sh
VERSION=0.1.0   # latest release, without the v
curl -fLO "https://github.com/sadayamuthu/unasked/releases/download/v${VERSION}/unasked_${VERSION}_$(dpkg --print-architecture).deb"
sudo apt install "./unasked_${VERSION}_$(dpkg --print-architecture).deb"
```

**Fedora / RHEL** (`.rpm`, x86_64 and aarch64):

```sh
VERSION=0.1.0
sudo dnf install "https://github.com/sadayamuthu/unasked/releases/download/v${VERSION}/unasked-${VERSION}-1.$(uname -m).rpm"
```

**Docker** (linux/amd64 and linux/arm64; mount the repository at `/repo`).
The container can't see your agent's transcripts, so this is mostly for
CI-style `--since` checks:

```sh
docker run --rm -v "$PWD":/repo ghcr.io/sadayamuthu/unasked --since origin/main
```

**GitHub Actions** (checks each pull request against its base branch; see
[In CI](USAGE.md#in-ci)):

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0
- uses: sadayamuthu/unasked@v0
```

**Prebuilt binary** (macOS, Linux and Windows, amd64 and arm64): download the
archive for your platform from the
[latest release](https://github.com/sadayamuthu/unasked/releases/latest),
then put `unasked` on your `PATH`:

```sh
tar -xzf unasked_*_$(uname -s | tr A-Z a-z)_*.tar.gz
sudo mv unasked /usr/local/bin/
```

On Windows, unzip `unasked_v<version>_windows_amd64.zip` (or `_arm64`) and put
`unasked.exe` in a folder on your `PATH`.

On macOS, a binary from an archive downloaded in a browser is blocked by
Gatekeeper because it isn't signed yet (Homebrew installs aren't affected).
Clear the quarantine flag once:

```sh
xattr -d com.apple.quarantine /usr/local/bin/unasked
```

**With Go** (needs Go 1.25 or newer; no C compiler, unasked is pure Go):

```sh
go install github.com/sadayamuthu/unasked/cmd/unasked@latest
```

Or build from source:

```sh
git clone https://github.com/sadayamuthu/unasked.git
cd unasked
go build -ldflags "-X main.version=$(git describe --tags --always)" -o unasked ./cmd/unasked
```

Check it works:

```sh
unasked version
unasked doctor     # which agents it found, and whether their hooks are installed
```

## Quick start

Install the hooks once. They cover every repository you open with that agent:

```sh
unasked hook install          # every supported agent found on this machine
```

From then on, when Claude Code or Codex stops, unasked captures the session,
and if it finds unasked hunks it hands the report back to the agent, which
drops them or explains itself. You can also run it yourself, inside the
repository, after any session:

```sh
# What did the last agent session change that I didn't ask for?
unasked

# Revert every unasked hunk, or just the ones you name
unasked --drop
unasked --drop h03 h07

# Changed your mind? Restore the files exactly as they were
unasked undo

# Accept one on this branch so it stops being reported
unasked --keep h05 --reason "needed for the migration"
```

Exit codes make it easy to script:

| Code | Result |
|------|--------|
| 0 | `clean`: every hunk was invited (also: a `--drop`, `--keep` or `undo` that succeeded) |
| 1 | `unclear`: some hunks could not be matched to what you asked, none are clearly unasked |
| 2 | `unasked`: at least one hunk nobody asked for |
| 3 | error or refused action (not a git repo, stale hunk id, a drop that wouldn't apply cleanly…) |

Supported agents in this release: **Claude Code** and **Codex**, both built
for full support (prompts, the agent's words before each edit, and a stop
hook that feeds the report back) and marked **preview** until real recorded
sessions validate it (see [CONTRIBUTING.md](CONTRIBUTING.md#release-checklist)).
Eleven more are planned; see [Supported agents](USAGE.md#supported-agents).

See **[USAGE.md](USAGE.md)** for every command and flag, the JSON output,
hooks, the safety rules of `--drop` and `undo`, CI recipes and the capture
store. To report a wrong verdict or add an agent, see
**[CONTRIBUTING.md](CONTRIBUTING.md)**.

## Development

```sh
UNASKED_RELAX_CEILING=1 go test ./...   # full suite, incl. fixture gates and the 5,000-file perf test
UNASKED_RELAX_CEILING=1 go test -short ./...   # skips the perf test
go vet ./...
gofmt -l .
```

`UNASKED_RELAX_CEILING=1` is what CI sets so that recall, not the 20%
false-alarm target, decides a failing run; see [Fixtures](CONTRIBUTING.md#fixtures).

Scoring is pinned by labelled agent sessions in `testdata/fixtures/`; each one
is a tiny repository, a transcript, and a human's verdict for every hunk.

## License

[MIT](LICENSE)
