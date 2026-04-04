---
id: TASK-004
type: Task
title: "Gateway handler bypasses engine StartRun for standard runs"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-01
last_updated: 2026-04-04
completed: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-003-standard-run-branch-creation-and-merge.md
---

# TASK-004 — Gateway Handler Bypasses Engine StartRun for Standard Runs

---

## Purpose

The gateway handler `handleRunStart` in `/internal/gateway/handlers_workflow.go` (lines 104-185) has its own inline implementation for standard runs that bypasses `engine.StartRun()` entirely. This means:

1. No branch name is generated
2. No `CreateBranch` is called
3. No branch is stored on the run record
4. The merge path on run completion is skipped (empty `BranchName`)

Planning runs correctly delegate to `s.planningRunStarter.StartPlanningRun()` (line 77), which goes through the engine and creates a branch. Standard runs should follow the same pattern.

Found during dogfooding: TASK-003 fixed `engine.StartRun()` to make branch creation fatal, but the fix had no effect because the gateway never calls `engine.StartRun()` for standard runs.

---

## Root Cause

The gateway handler at `/internal/gateway/handlers_workflow.go:104-185` creates runs inline:
- Builds `domain.Run` struct directly (no `BranchName` field set)
- Calls `store.CreateRun` / `UpdateRunStatus` / `CreateStepExecution` in a transaction
- Returns 201 without ever touching Git

The engine's `StartRun()` at `/internal/engine/run.go:23-154` does the same DB operations PLUS:
- Generates branch name via `generateBranchNameWithSuffix`
- Creates Git branch via `o.git.CreateBranch`
- Pushes branch if auto-push enabled
- Emits `run_started` event

---

## Deliverable

Route standard runs through the engine, matching the planning run pattern:

1. Add a `RunStarter` interface to the gateway (like `PlanningRunStarter`)
2. Create an adapter from `engine.Orchestrator` to the gateway interface (like `planningRunAdapter`)
3. Wire it in `cmd/spine/main.go`
4. Replace the inline handler code with a call to the adapter
5. Ensure the response includes `branch_name` so clients know which branch to use

---

## Acceptance Criteria

- Standard runs go through `engine.StartRun()` (not inline gateway code)
- Branch is created when a standard run starts
- `branch_name` is visible in the run status response
- Run completion triggers `MergeRunBranch` which merges to main
- Planning run behavior is unchanged
- Existing tests pass, new test verifies branch creation for standard runs
