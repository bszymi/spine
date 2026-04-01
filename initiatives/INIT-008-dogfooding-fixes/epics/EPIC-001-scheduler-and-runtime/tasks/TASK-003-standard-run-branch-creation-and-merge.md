---
id: TASK-003
type: Task
title: "Standard run branch creation fails silently and skips merge"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-01
last_updated: 2026-04-01
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-002-planning-run-auto-merge.md
---

# TASK-003 — Standard Run Branch Creation Fails Silently and Skips Merge

---

## Purpose

When a standard run is started via `StartRun()`, branch creation failure is silently ignored — the `BranchName` is cleared in memory, the run continues without a branch, no WARN log is emitted, and the run completes without ever going through the merge path.

This means standard runs operate directly on the current branch (or main) without Git isolation, and changes must be merged manually.

Found during dogfooding: TASK-001 in the Spine Management Platform was executed through the `task-default` workflow. The run started successfully (`run-7b06c0e3`) but the DB shows `branch_name` is empty. No `spine/run/*` branch was created. No WARN log was emitted despite the code at `run.go:90` supposedly logging failures. The run completed and the merge step was a no-op.

Planning runs (`StartPlanningRun`) correctly treat branch creation failure as fatal — this asymmetry should be fixed.

---

## Root Cause Analysis

In `/internal/engine/run.go` lines 88-96:

```go
if err := o.git.CreateBranch(ctx, branchName, "HEAD"); err != nil {
    log.Warn("failed to create run branch", "branch", branchName, "error", err)
    run.BranchName = ""
}
```

Issues:
1. Branch creation failure is non-fatal — the run continues without Git isolation
2. The in-memory `BranchName` is cleared but the DB may not be updated consistently
3. `MergeRunBranch` in `merge.go:32-34` skips merge when `BranchName` is empty, transitioning directly to completed
4. No WARN log was observed in Docker logs despite the code path — suggests the error may be swallowed before reaching the log statement

Compare with `StartPlanningRun` lines 238-240 which correctly fails:
```go
if err := o.git.CreateBranch(ctx, branchName, "HEAD"); err != nil {
    return nil, fmt.Errorf("create planning branch: %w", err)
}
```

---

## Deliverable

Fix `StartRun()` to treat branch creation failure as fatal, matching `StartPlanningRun` behavior.

Specifically:
1. Make `CreateBranch` failure in `StartRun` return an error (fail the run start)
2. Investigate why no WARN log was emitted for `run-7b06c0e3`
3. Ensure `MergeRunBranch` is triggered on run completion and merges the branch to main
4. Add a scenario test for the full standard run flow: start → branch created → execute → review → commit → merge to main → branch cleanup

---

## Acceptance Criteria

- `StartRun` fails if branch creation fails (no silent fallback)
- Standard run completion triggers `MergeRunBranch` which merges to main
- Branch is cleaned up after successful merge
- Scenario test validates: start run → branch exists → complete run → artifacts on main → branch deleted
- WARN logging works correctly if branch creation is ever made non-fatal again
