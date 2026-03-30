---
id: TASK-003
type: Task
title: Update store layer for mode column
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
---

# TASK-003 — Update Store Layer for Mode Column

---

## Purpose

Update the PostgreSQL store to persist and retrieve the `mode` field on runs.

---

## Deliverable

Updates to:
- `internal/store/postgres.go` — `CreateRun` INSERT and `GetRun` SELECT/Scan include `mode`
- `internal/store/tx.go` — `CreateRun` in transaction includes `mode`

---

## Acceptance Criteria

- `CreateRun` persists the `mode` value
- `GetRun` reads and populates `run.Mode`
- Empty/zero `mode` defaults to `"standard"` at the database level
- Existing integration tests pass (existing runs have `mode = 'standard'`)
