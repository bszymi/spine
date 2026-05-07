---
id: TASK-009
type: Task
title: "Insert -- sentinels and ValidateRef in git Diff/MergeBase/Clone argv"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-009 — Insert -- sentinels and ValidateRef in git Diff/MergeBase/Clone argv

---

## Purpose

Three git shellouts in `internal/git/cli.go` interpolate caller-supplied
strings directly into argv with no `--` sentinel between flags and
positional arguments:

- `Diff(from, to)` at `:415-417` — refs go in raw.
- `MergeBase(a, b)` at `:549-554` — refs go in raw.
- `Clone(url, path)` at `:272-294` — URL goes in raw.

Also, `gitpool.cliCloner.Clone` at `internal/gitpool/pool.go:608-621`
does not re-call `git.ValidateCloneURL` before shelling out. Today
callers feed server-built strings, but a stored-then-poisoned branch
name (e.g. `--upload-pack=cmd`) reaches `git diff` as an option, not
an argument.

This is a P2 security finding from the 2026-05-07 code review.

## Deliverable

**Note on `--` placement:** the sentinel works for `Clone` (where
`--` separates options from the URL positional argument) but **NOT
for `Diff`/`MergeBase`**. In those subcommands, `--` ends option
parsing and treats following positional arguments as **pathspecs**,
not revisions — so `git diff -- <branchA> <branchB>` would compare
files literally named `branchA` and `branchB`, not the two refs.
The right defense for ref arguments is mandatory `ValidateRef` on
the inputs.

- In `internal/git/cli.go::Diff` and `MergeBase`: call
  `git.ValidateRef(ref)` (or the existing equivalent) on each ref
  argument and return an error before shelling out if any ref is
  rejected. Do NOT add `--` between subcommand and refs.
- In `internal/git/cli.go::Clone`: insert `"--"` immediately before
  the URL so flags can never be interpreted as such.
- In `internal/gitpool/pool.go::cliCloner.Clone`: call
  `git.ValidateCloneURL(url)` before shelling out, returning an error
  if it rejects (second-line-of-defense pattern, complementing
  TASK-002).

## Acceptance Criteria

- Existing unit tests in `internal/git/cli_test.go` continue to pass.
- A new unit test passes `--upload-pack=cmd` as a ref to Diff and
  MergeBase and asserts the function returns `ErrInvalidRef` (or
  equivalent) **before** shelling out — not after git fails.
- A new unit test passes a refname containing shell metacharacters
  (e.g. `; rm -rf /`) to Diff and MergeBase and asserts the same
  rejection path.
- A new unit test passes `--upload-pack=cmd` as a clone URL and
  asserts the cloner rejects with `ErrInvalidCloneURL` (or equivalent)
  before shelling out.
- `Diff` and `MergeBase` continue to return correct ref-comparison
  results (not pathspec results) for valid branch-name inputs — the
  existing happy-path tests are the regression bait.

## Out of Scope

- A wholesale audit of every git shellout in the repo. This task fixes
  the three flagged surfaces; an audit pass can follow as a P3 if
  warranted.
- Hardening Push/Fetch/Reset arg surfaces — not flagged.
