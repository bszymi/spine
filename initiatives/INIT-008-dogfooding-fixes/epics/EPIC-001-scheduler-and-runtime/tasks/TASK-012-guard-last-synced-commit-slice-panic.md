---
id: TASK-012
type: Task
title: "Guard LastSyncedCommit[:8] against empty string panic"
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

# TASK-012 — Guard LastSyncedCommit[:8] Against Empty String Panic

---

## Purpose

In `/internal/projection/service.go` (lines 261-273), `state.LastSyncedCommit[:8]` panics when the commit string is empty. This can occur during a failed or interrupted rebuild. The panic crashes the sync goroutine permanently until the process restarts, with no recovery middleware protecting it.

---

## Deliverable

Add a length check before slicing `LastSyncedCommit`. If empty, use a safe fallback (e.g., `"(none)"` or skip the log field).

---

## Acceptance Criteria

- Empty `LastSyncedCommit` does not panic
- Sync goroutine continues operating after a failed rebuild
- Existing projection tests pass
