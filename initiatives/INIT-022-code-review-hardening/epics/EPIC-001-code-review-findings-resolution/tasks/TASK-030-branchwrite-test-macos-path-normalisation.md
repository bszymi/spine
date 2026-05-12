---
id: TASK-030
type: Task
title: "Normalise worktree paths in branchwrite_test on macOS"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-004-branchwrite-tests.md
---

# TASK-030 — Normalise worktree paths in branchwrite_test on macOS

---

## Purpose

`internal/git/branchwrite_test.go:112` compares the raw path returned
by `os.MkdirTemp("", ...)` against the path reported by `git worktree
list --porcelain`. On macOS the former is typically `/var/folders/...`
while git canonicalises to `/private/var/folders/...`, so the
assertion fails on the dominant local-dev and CI environment for this
repo even though the worktree was added correctly.

This is a P2 test-quality finding from the 2026-05-12 codex review of
the INIT-022 batch (commit `6b2a15047a`).

## Deliverable

- Normalise both paths through `filepath.EvalSymlinks` (or use git's
  canonical form when parsing the `--porcelain` output) before the
  equality check.
- Add a short comment noting the macOS `/var` → `/private/var` symlink
  rationale so the next reader doesn't reintroduce the raw compare.

## Acceptance Criteria

- The assertion holds on macOS without any test-time `os.Symlink` or
  `darwin`-build-tag gymnastics.
- Linux behaviour is unchanged (no extra `EvalSymlinks` failure mode
  on missing tmp dirs).
- Manual probe: temporarily revert this fix and confirm the test
  fails on `darwin/arm64` with a path-mismatch diff at the line cited
  above.

## Out of Scope

- Broader audit of other tests that compare tmp paths against git
  output — only the one assertion called out by the codex review is in
  scope. If a sibling case shows up while editing, batch it in the same
  PR and note it in the resolution.
- Replacing `os.MkdirTemp` with a different helper.

## Resolution (2026-05-12)

Added a `canonicalPath(t, path)` helper in
`internal/git/branchwrite_test.go` that wraps `filepath.EvalSymlinks`
with a `t.Fatalf` on error. The helper's doc comment explains the
macOS `/var → /private/var` symlink quirk so the next reader doesn't
revert to a raw `strings.Contains`.

Two call sites updated:

1. `TestBranchWrite_EnterCreatesWorktreeAtFreshPath` (line 112, the
   finding called out by codex) — canonicalises `scope.RepoDir` before
   the porcelain `Contains` check.
2. `TestBranchWrite_EnterCleanupSurvivesCancelledContext` (line 165,
   the sibling case discovered while editing — same bug shape,
   in-scope per the "batch if it shows up" out-of-scope clause).
   Canonical path is captured *before* `scope.Cleanup` runs because
   `EvalSymlinks` needs the path to exist.

**Acceptance criteria satisfied:**

- *Assertion holds on macOS without darwin gymnastics.* ✓ — The fix
  is symmetric across platforms: on Linux `EvalSymlinks` is a no-op
  (paths are already canonical); on macOS it resolves
  `/var/folders/...` to `/private/var/folders/...` matching git's
  porcelain output. No build tags or runtime-OS branching.
- *Linux behaviour unchanged.* ✓ — Both tests still pass on the
  Docker Linux container after the change (full `go test ./... -race`
  green).
- *Manual probe.* The probe in the AC ("revert this fix and confirm
  the test fails on `darwin/arm64`") can't be exercised in the Docker
  test path — the symlink only exists on macOS hosts. Verified by
  inspection instead: `os.MkdirTemp` on macOS returns the
  `/var/folders/...` form per Go runtime convention, and `git worktree
  list --porcelain` canonicalises through the `/private` symlink per
  long-standing git behaviour, so the `strings.Contains` substring
  miss is mechanical. The Linux test path is unaffected by either
  change since both sides of the comparison resolve to identical
  bytes.

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l internal/git/branchwrite_test.go` — clean.
- `go test ./... -count=1 -race` — green across 38 packages.
- `make docker-lint` — 207 issues, identical to baseline at commit
  `c36d06a` (no new findings).
- `codex review` — clean: *"The test-only changes consistently
  canonicalize worktree paths before comparing them with Git's
  porcelain output and do not introduce an evident behavioral
  regression."*
