# Security Policy

## Reporting a vulnerability

Please don't report security problems in a public issue, pull request or
discussion. Use
[**Report a vulnerability**](https://github.com/sadayamuthu/unasked/security/advisories/new)
on the repository's Security tab instead. Only the maintainer can see the
report.

Include what you can of:

- the output of `unasked version` (it includes the git version) and your OS
- the agent and how unasked was run: by hand, from a hook, or in the GitHub
  Action
- steps to reproduce, ideally a small repository or a script that builds one
- what happened and what you expected

You'll get a reply when the report has been read, and again when a fix is
ready. A fix ships as a patch release; the advisory names you unless you'd
rather it didn't.

## Supported versions

unasked is pre-1.0. Fixes land in the latest release only; please upgrade
before reporting.

## What counts

unasked reads agent transcripts, which can hold prompts, code and secrets,
and it rewrites files in your working tree. Anything that breaks one of these
promises is a security issue:

- **Transcripts stay local.** Captured sessions, keeps, reports and revert
  snapshots live under `.git/unasked/` (directories 0700, files 0600). A way
  for them to leave that directory, be committed, be readable by other users,
  or be sent over the network is in scope. unasked makes no network calls.
- **The working tree is never broken.** `--drop` and `undo` must either
  succeed exactly or leave every file and the index as they were. Any input
  (a crafted path, patch, transcript or repository) that makes them write
  outside the repository, follow a symlink out of it, or leave a half-applied
  state is in scope.
- **Untrusted input stays data.** A transcript, prompt, file name or branch
  name that makes unasked or its hooks run a command, or makes the hook feed
  the agent instructions other than the report, is in scope.
- **The GitHub Action.** Script injection through pull-request titles, branch
  names or file names, or a way to make it overwrite a comment it didn't
  write, is in scope.

A wrong verdict, such as a hunk marked clean that nobody asked for, is a bug,
not a vulnerability. Report it in a normal issue; see
[CONTRIBUTING.md](CONTRIBUTING.md).
