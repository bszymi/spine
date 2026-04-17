---
id: TASK-003
type: Task
title: "Support write_context { run_id } on workflow.create/update"
status: Pending
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
  - type: blocked_by
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/tasks/TASK-001-adr-workflow-lifecycle.md
---

# TASK-003 — Support write_context { run_id } on workflow.create/update

---

## Context

For branch-based edits, `workflow.Service` must be able to write to a Run's task branch instead of the authoritative branch. The `artifact.Service` already has this pattern (`WriteContext{Branch}` + worktree scoping) — mirror it.

## Deliverable

- Add `WriteContext` support to `internal/workflow/service.go`:
  - New `WriteContext{ RunID, Branch }` type (or reuse the existing artifact one if the signatures align).
  - Helper `WithWriteContext(ctx, wc)` that carries the branch through `Create` / `Update`.
  - Writes target the provided branch via worktree scoping (mirror `internal/artifact/service.go`'s `enterBranch`/`stageAndCommit` pattern; consider extracting the shared helpers to a new `internal/git/worktree` package if duplication is painful).
- Extend gateway handlers (`internal/gateway/handlers_workflows.go`):
  - `workflowCreateRequest` and `workflowUpdateRequest` gain an optional `write_context` field (re-use `writeContextRequest` from `handlers_artifacts.go`).
  - Resolve `run_id → branch` via the existing `resolveWriteContext` helper.
  - Preserve the existing direct-commit path when `write_context` is absent (used by operator bypass in TASK-005).
- Add tests:
  - Service-level test with a real temp repo asserting that a branch-scoped write lands on the branch and *not* on `main`.
  - Handler-level test covering both `write_context`-set and `write_context`-absent paths.

## Acceptance Criteria

- `workflow.Service.Create/Update` accept a branch parameter and commit to it when set.
- Gateway accepts and forwards `write_context` on both endpoints.
- `main` branch is untouched when `run_id` is provided.
- Tests cover branch-scoped and direct-commit paths.
- Package coverage stays ≥80%.
