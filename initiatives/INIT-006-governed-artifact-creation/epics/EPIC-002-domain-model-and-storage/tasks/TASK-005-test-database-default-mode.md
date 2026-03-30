---
id: TASK-005
type: Task
title: Add integration test for database-level mode default
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

# TASK-005 — Add Integration Test for Database-Level Mode Default

---

## Purpose

The existing `TestRunModeDefaultStandard` test validates the application-side `modeOrDefault` fallback but does not verify the database DEFAULT constraint from migration 008. If the migration default is broken or removed, the test would still pass because `CreateRun` always sends an explicit value.

Add a test that inserts a run via raw SQL without the `mode` column and reads it back through `GetRun` to confirm the database DEFAULT produces `"standard"`.

---

## Deliverable

New test in `internal/store/postgres_integration_test.go`:

- Insert a run using raw SQL that omits the `mode` column entirely
- Retrieve the run via `GetRun`
- Assert that `run.Mode` is `"standard"`

---

## Acceptance Criteria

- Test verifies the database DEFAULT, not the application-side fallback
- Test runs as part of `go test -tags integration ./internal/store/...`
- All existing store tests continue to pass
