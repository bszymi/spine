---
id: TASK-001
type: Task
title: Review and fix all scheduler timeout defaults
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-001 — Review and Fix All Scheduler Timeout Defaults

---

## Purpose

The orphan detection threshold (5 minutes, fails at 15 minutes) destroyed a planning run branch while a human was reviewing artifacts. All scheduler timeouts need auditing against real human-paced usage.

Spine workflows are human-driven — runs may be active for days or weeks. The scheduler must not interfere with normal work.

---

## Deliverable

### 1. Audit all timeouts

Review every timeout in `internal/scheduler/`:
- `orphanThreshold` — currently 5 minutes. **Change to 30 days or disable auto-fail entirely.**
- `orphanInterval` — scan frequency (60s). Fine as a scan interval, but the action taken must change.
- `timeoutInterval` — step timeout check frequency (30s). Review if appropriate.
- `commitThreshold` — commit retry window. Review if appropriate.
- Step-level `timeout` in workflow YAML definitions.

### 2. Fix orphan detection behavior

The orphan scanner currently **fails** runs after 3x the threshold. Options:
- **A)** Change to warn-only — never auto-fail, require manual cancellation
- **B)** Increase threshold to 30+ days so auto-fail only catches truly abandoned runs
- **C)** Exempt planning runs from orphan detection entirely

Decide which approach and implement.

### 3. Add environment variable configuration

Add `SPINE_ORPHAN_THRESHOLD` environment variable so the threshold can be configured at startup without code changes. Follow the pattern of `SPINE_PROJECTION_POLLING_INTERVAL`.

### 4. Update tests

- Update `internal/scheduler/scheduler_test.go` and `recovery_test.go` for new defaults
- Verify planning runs survive extended review periods in scenario tests

### 5. Update documentation

- `architecture/error-handling-and-recovery.md` — document the new threshold and configuration
- `architecture/engine-state-machine.md` — clarify orphan detection behavior

---

## Acceptance Criteria

- Orphan detection does not kill runs within hours or days of inactivity
- `SPINE_ORPHAN_THRESHOLD` environment variable works
- Planning runs survive multi-day review periods
- All existing tests pass with new defaults
- Behavior change is documented
