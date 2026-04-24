---
id: TASK-019
type: Task
title: Extract shared Git refname validation helper
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-017-validate-git-refname-before-worktree-add.md
---

# TASK-019 — Extract shared Git refname validation helper

---

## Purpose

`internal/artifact` and `internal/workflow` each carry a duplicate `validateGitRefName` implementation before calling `git.EnterBranch`. This is security-sensitive validation; keeping two copies increases the chance of future drift.

## Deliverable

- Move the shared branch/ref validation into an appropriate git-owned package, such as `internal/git/refname`.
- Replace the artifact and workflow local helpers with calls to the shared validator.
- Consolidate the duplicated test cases into one shared test suite.
- Keep typed domain errors at service boundaries so gateway behavior does not regress.

## Acceptance Criteria

- There is one canonical implementation of the branch/ref safety rules.
- Artifact and workflow branch-scoped writes still reject leading `-`, `..`, `@{`, whitespace/control characters, `.lock`, trailing `/`, and forbidden Git ref characters.
- Existing refname tests are migrated or replaced without losing coverage.
- `go test ./internal/artifact ./internal/workflow ./internal/git` passes.
