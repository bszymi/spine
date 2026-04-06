---
id: TASK-003
type: Task
title: "Wire blocking detection into run lifecycle and bootstrap"
status: Completed
completed: 2026-04-06
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/tasks/TASK-001-dependency-blocking-detection.md
---

# TASK-003 — Wire Blocking Detection into Run Lifecycle and Bootstrap

---

## Purpose

TASK-001 implemented `IsBlocked()` and `CheckAndEmitBlockingTransition()` as isolated helpers, but nothing calls them. Without wiring:
- Blocked tasks can still have runs started on them
- Completing a blocker task never re-evaluates dependents
- The `task_unblocked` event is never emitted

Found during Codex review (P1/P2).

---

## Deliverable

1. **Bootstrap wiring**:
   - Call `orch.WithBlockingStore(store)` in `cmd/spine/main.go` after orchestrator creation
   - Call `orch.WithBlockingStore(db.Store)` in the scenario test harness (`internal/scenariotest/harness/runtime.go`)

2. **StartRun enforcement**:
   - In `Orchestrator.StartRun()`, call `IsBlocked(taskPath)` before creating the run
   - If blocked, return an error: `"task is blocked by: [list of blockers]"`
   - Do NOT create the run — the task cannot proceed until blockers are resolved

3. **Completion re-evaluation**:
   - After a run completes successfully (in the completion path), call `CheckAndEmitBlockingTransition(taskPath)` to re-evaluate tasks that were blocked by this one
   - This emits `task_unblocked` for any dependents that are now ready

4. **Tests**:
   - Test that `StartRun` fails for a blocked task
   - Test that `StartRun` succeeds when all blockers are terminal
   - Test that completing a run triggers re-evaluation of dependents

5. **Documentation**:
   - Update `/architecture/engine-state-machine.md` to note that StartRun checks blocking status

---

## Acceptance Criteria

- `StartRun` returns an error for tasks with unresolved `blocked_by` links
- `StartRun` succeeds when all blockers are in terminal status
- Run completion triggers `CheckAndEmitBlockingTransition` for dependents
- `task_unblocked` event is emitted when all blockers resolve
- Blocking store is wired in both production bootstrap and scenario harness
- Tests cover enforcement and re-evaluation paths
