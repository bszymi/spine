---
id: TASK-030
type: Task
title: "Normalise worktree paths in branchwrite_test on macOS"
status: Pending
acceptance: Pending
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
