---
id: TASK-010
type: Task
title: "Wrap UpsertArtifactLinks in a transaction"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-010 — Wrap UpsertArtifactLinks in a Transaction

---

## Purpose

`UpsertArtifactLinks` in `/internal/store/postgres.go` (lines 805-819) deletes all existing links for a source, then inserts each new link in separate `Exec` calls outside any transaction. A crash mid-insert leaves the artifact with no links until the next projection rebuild.

---

## Deliverable

Wrap the delete+insert operations in `s.pool.BeginTx`/`Commit` to make the upsert atomic.

---

## Acceptance Criteria

- Link upsert is atomic: either all links are replaced or none are
- Existing projection tests pass
