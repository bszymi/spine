---
id: TASK-007
type: Task
title: "Backfill existing capabilities into skills during migration"
status: Completed
completed: 2026-04-05
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-006-remove-legacy-capabilities.md
---

# TASK-007 — Backfill Existing Capabilities into Skills During Migration

---

## Purpose

Migration 011 drops the `capabilities` column from `auth.actors` without first migrating existing capability data into `auth.skills` and `auth.actor_skills`. On an upgraded database with non-empty capabilities, all pre-existing actors lose their capability data silently, causing workflows that previously matched to stop matching.

Found during Codex review (P1).

---

## Deliverable

1. Add a data migration step before dropping the column in `011_drop_actor_capabilities.sql`:
   - Extract distinct capability names from all actors' `capabilities` jsonb arrays
   - Insert them into `auth.skills` with generated IDs and status `active`
   - Insert actor-skill associations into `auth.actor_skills`
   - Then drop the column

2. Handle idempotency: if skills already exist with the same name, skip insertion

---

## Acceptance Criteria

- Existing capability strings are preserved as skill entities after migration
- Actor-skill associations are created for all pre-existing capabilities
- No data loss during upgrade
- Migration is idempotent (safe to re-run)
