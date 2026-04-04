---
id: TASK-004
type: Task
title: "Extract store row scanning helpers to eliminate repeated scan boilerplate"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: implementation
created: 2026-04-04
last_updated: 2026-04-04
completed: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-004 — Extract Store Row Scanning Helpers

---

## Purpose

Row scanning boilerplate (with nullable pointer handling) is repeated across multiple store methods in `/internal/store/postgres.go`:

- **Run scanning** (4 sites): `GetRun`, `ListRunsByTask`, `ListRunsByStatus`, `ListStaleActiveRuns` — each repeats ~15 lines of scan + nullable dereference for `currentStepID`, `branchName`
- **StepExecution scanning** (3 sites): `GetStepExecution`, `ListStepExecutionsByRun`, `ListActiveStepExecutions` — each repeats ~20 lines
- **Assignment scanning** (3 sites): `ListAssignmentsByActor`, `ListExpiredAssignments`, `GetAssignment`

Additionally, `ListRunsByTask` omits `timeout_at` and `mode` columns unlike other list methods, causing zero-value fields that could produce subtle bugs.

---

## Deliverable

1. Extract `scanRun(row)` and `scanRuns(rows)` helpers
2. Extract `scanStepExecution(row)` and `scanStepExecutions(rows)` helpers
3. Extract `scanAssignment(row)` helper
4. Ensure all list methods select the same columns (fix `ListRunsByTask` to include `timeout_at` and `mode`)
5. Fix silently swallowed `json.Unmarshal` errors for actor capabilities (log warning instead of discarding)

---

## Acceptance Criteria

- All run/step/assignment scan sites use shared helpers
- `ListRunsByTask` returns complete `Run` structs (including `TimeoutAt` and `Mode`)
- Actor capabilities unmarshal errors are logged (not silently swallowed)
- All existing store integration tests pass
- All scenario tests pass
