---
id: EPIC-002
type: Epic
title: Domain Model & Storage
status: In Progress
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-001-architecture-and-adr/epic.md
---

# EPIC-002 — Domain Model & Storage

---

## 1. Purpose

Extend the domain model and database schema to support planning runs.

This is a purely additive change — new type, new column, updated queries. No existing behavior is modified.

---

## 2. Scope

### In Scope

- `RunMode` type and `Mode` field on `domain.Run`
- Database migration `008_add_run_mode.sql`
- Store layer updates (CreateRun, GetRun) for `mode` column
- Unit tests for store persistence

### Out of Scope

- Engine logic (EPIC-003)
- API changes (EPIC-004)

---

## 3. Success Criteria

1. `domain.Run` has a `Mode` field with `standard` and `planning` values
2. Migration creates the `mode` column with `standard` default
3. Store layer persists and retrieves `mode` correctly
4. All existing store tests continue to pass

---

## 4. Key Files

- `internal/domain/run.go`
- `migrations/008_add_run_mode.sql`
- `internal/store/postgres.go`
- `internal/store/tx.go`
