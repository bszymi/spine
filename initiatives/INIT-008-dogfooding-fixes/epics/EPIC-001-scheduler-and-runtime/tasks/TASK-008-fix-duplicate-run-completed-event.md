---
id: TASK-008
type: Task
title: "Fix duplicate run_completed event on concurrent merge"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-008 — Fix Duplicate run_completed Event on Concurrent Merge

---

## Purpose

`CompleteRun` in `/internal/engine/run.go` (lines 275-283) and `completeAfterMerge` in `/internal/engine/merge.go` (lines 128-130) can both emit `EventRunCompleted` if `MergeRunBranch` runs concurrently, because both callers read `run.Status == committing` before either updates it.

---

## Deliverable

Ensure atomic status check-and-update or deduplicate event emission to prevent double `run_completed` events.

---

## Acceptance Criteria

- Only one `run_completed` event is emitted per run regardless of concurrent merge timing
- Existing tests pass
