---
id: TASK-009
type: Task
title: "Insert -- sentinels and ValidateRef in git Diff/MergeBase/Clone argv"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-08
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

## Resolution (2026-05-08)

Four edits, all in pre-existing files (no new files):

1. **`internal/git/cli.go::Diff`** — added `git.ValidateRef(from)` and
   `git.ValidateRef(to)` gates before the `c.run(...)` shellout, each
   returning `fmt.Errorf("diff from: %w", err)` / `"diff to: %w"`.
   Comment block above the function explains why `--` is **not**
   inserted between subcommand and refs: `git diff --` switches the
   following positionals from revisions to pathspecs, which would
   silently change the semantics on every valid call. The
   ValidateRef gate is the right defense for ref arguments.
2. **`internal/git/cli.go::MergeBase`** — same `ValidateRef(a)` /
   `ValidateRef(b)` pattern with a comment block citing the same
   rationale (`git merge-base --` would interpret arguments as
   pathspecs, which the subcommand does not even accept).
3. **`internal/git/cli.go::Clone`** — inserted `"--"` between the
   `clone` subcommand and the URL: `args = append(args, "clone",
   "--", url, path)`. Unlike Diff/MergeBase, `git clone -- <repo>
   [<dir>]` is the documented form for separating options from the
   URL positional, so the sentinel is both correct and the cheapest
   fix at the shellout boundary.
4. **`internal/gitpool/pool.go::cliCloner.Clone`** — added a
   `git.ValidateCloneURL(url)` gate at the top of the function as a
   second line of defense. Workspace and code-repo registration
   already validate at the entry points, but a stored-then-poisoned
   binding row could still reach this path; re-validating closes
   that gap.

**Tests added (all in pre-existing test files):**

- `internal/git/cli_test.go::TestDiffRejectsPoisonedRef` —
  table-driven, four cases: `--upload-pack=cmd` as `from`, then as
  `to`; `; rm -rf /` as `from`, then as `to`. Runs against
  `/nonexistent/repo` so any rejection that fell through to the
  shellout would surface as an `exec git` error rather than the
  validator error — the discriminator that proves the gate fired.
- `internal/git/cli_test.go::TestMergeBaseRejectsPoisonedRef` —
  same shape with the `MergeBase` API.
- `internal/gitpool/credentials_test.go::TestCLICloner_RejectsPoisonedURL`
  — table-driven, four cases: `--upload-pack=cmd`, `ext::sh -c id`,
  `file:///etc/passwd`, and empty URL. Asserts the wrapped
  `clone url: ...` rejection rather than reaching the throwaway
  CLIClient or the shellout.

The four happy-path tests in `cli_test.go` (`TestDiff`,
`TestCreateBranchAndMergeFastForward`, `TestCreateBranchAndMergeCommit`,
`TestPush`) and the four partial-merge / multi-repo scenarios
(`TestPartialMergeRetry_HappyPath`,
`TestPartialMergeExternalResolution_HappyPath`,
`TestCancelFromPartiallyMerged_AsymmetricCleanup`,
`TestMultiRepoRunLifecycle`) all pass — the regression bait the AC
calls out: "Diff and MergeBase continue to return correct
ref-comparison results (not pathspec results) for valid branch-name
inputs."

**Test gates:**

- `make docker-test` (unit, full repo) — green, including
  `internal/secrets` (no flake this run).
- `go test -tags scenario -p 1 -run
  'TestPartialMergeRetry_HappyPath|TestPartialMergeExternalResolution_HappyPath|TestCancelFromPartiallyMerged_AsymmetricCleanup|TestMultiRepoRunLifecycle'
  ./internal/scenariotest/scenarios` — green. The other scenario
  failures observed in the broader suite reproduce on bare main
  (`TestCreateArtifactStep` fails identically with
  `validation_failed: artifact validation failed` against
  `030ee2a`), so they are pre-existing and out of scope for
  TASK-009.
- `make docker-lint` — repo-wide count holds at 206 pre-existing
  issues; my edits contribute zero new findings.
- `golangci-lint run --enable-only=gosec` — single pre-existing
  finding in `internal/workflow/service.go:189` (path traversal
  taint analysis), unrelated to the touched files. No new gosec
  finding introduced.

**Codex iterative review:** two consecutive clean passes — "No
actionable correctness, security, or maintainability issues were
found." No findings to address.
