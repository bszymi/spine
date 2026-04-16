---
id: TASK-017
type: Task
title: "Validate git refname before passing to worktree add"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-017 — Validate Git Refname Before Passing To worktree add

---

## Purpose

`internal/artifact/service.go:375` passes `wc.Branch` directly to `git worktree add`. Today the branch is generated via `generateBranchNameWithSuffix` and is safe, but there is no defensive check at the call site. A future API path that lets the caller supply a branch name could allow names like `--force` that git parses as flags, or unusual ref characters that cause unexpected behavior.

---

## Deliverable

- Add a `validateGitRefName` helper (rules: no leading `-`, no `..`, no control chars, passes `git check-ref-format --branch`).
- Call it in `enterBranch` before `git worktree add`.
- Reject with a clear domain error.

---

## Acceptance Criteria

- Branch name `--force` is rejected.
- Branch name containing `..` is rejected.
- Happy-path branch names continue to work.
- Unit tests cover the validator.
