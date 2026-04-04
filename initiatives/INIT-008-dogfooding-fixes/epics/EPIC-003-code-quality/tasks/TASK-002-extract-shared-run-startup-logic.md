---
id: TASK-002
type: Task
title: "Extract shared run startup logic from StartRun and StartPlanningRun"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: implementation
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-002 — Extract Shared Run Startup Logic from StartRun and StartPlanningRun

---

## Purpose

`StartRun` (lines 23-156) and `StartPlanningRun` (lines 160-316) in `/internal/engine/run.go` share ~70% identical structure: trace ID generation, run record creation, branch creation with auto-push, entry step creation, run activation, event emission, and step activation. This duplication means changes to the shared sequence must be made in two places, increasing the risk of divergence.

---

## Deliverable

Extract a `startRunCommon` helper that handles the shared sequence:

1. Generate trace ID and run ID
2. Parse workflow timeout
3. Persist run to store
4. Create Git branch + optional auto-push
5. Create entry step execution
6. Activate run (pending -> active)
7. Emit `run_started` event
8. Activate entry step

Both `StartRun` and `StartPlanningRun` prepare their parameters (workflow resolution, branch name, run struct) and then call the shared path. `StartPlanningRun` additionally validates/writes the artifact and handles cleanup on failure.

---

## Acceptance Criteria

- `StartRun` and `StartPlanningRun` share a common helper for the 8 shared steps
- Planning run's additional logic (artifact validation, write, cleanup) remains separate
- All existing engine unit tests pass
- All scenario tests pass
- No behavioral change
