---
id: TASK-002
type: Task
title: Create migration 008_add_run_mode.sql
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
---

# TASK-002 — Create Migration 008_add_run_mode.sql

---

## Purpose

Add the `mode` column to `runtime.runs` table to persist the run mode.

---

## Deliverable

`migrations/008_add_run_mode.sql`

The migration should:
- Add `mode text NOT NULL DEFAULT 'standard'` to `runtime.runs`
- Add check constraint `runs_mode_check` for allowed values (`standard`, `planning`)
- Add partial index on `mode` where `mode != 'standard'` (sparse index for planning runs)
- Record the migration version in `schema_migrations`

---

## Acceptance Criteria

- Migration applies cleanly on a fresh database (after 001-007)
- Migration applies cleanly on an existing database with existing runs
- Existing runs get `mode = 'standard'` as default
- Migration follows the pattern of `002_add_branch_name.sql`
