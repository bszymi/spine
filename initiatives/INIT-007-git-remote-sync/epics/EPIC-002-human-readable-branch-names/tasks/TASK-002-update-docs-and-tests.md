---
id: TASK-002
type: Task
title: Update documentation and tests for branch naming
status: Done
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
  - type: blocked_by
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/tasks/TASK-001-branch-name-generator.md
---

# TASK-002 — Update Documentation and Tests for Branch Naming

---

## Purpose

Update architecture docs and existing tests to reflect the new branch naming convention.

---

## Deliverable

### 1. Architecture documentation

`architecture/git-integration.md` — update the branch naming section:
- Document the convention: `spine/<mode>/<artifact-id>-<slug>`
- Document collision handling
- Document slug sanitization rules

### 2. Existing test updates

Update any tests that assert branch names matching the old `spine/run/<run-id>` pattern:
- `internal/engine/run_test.go`
- `internal/engine/branch_test.go`
- `internal/scenariotest/` scenario tests

### 3. New scenario test

Add a scenario that validates branch naming end-to-end:
- Start a planning run → verify branch name matches `spine/plan/<id>-<slug>`
- Start a standard run → verify branch name matches `spine/run/<id>-<slug>`

---

## Acceptance Criteria

- `git-integration.md` documents the new convention
- All existing tests pass with new branch names
- Scenario test validates naming end-to-end
