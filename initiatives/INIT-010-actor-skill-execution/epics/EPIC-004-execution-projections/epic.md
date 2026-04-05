---
id: EPIC-004
type: Epic
title: Execution Projections and Query Infrastructure
status: Pending
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
owner: bszymi
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/initiative.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
---

# EPIC-004 — Execution Projections and Query Infrastructure

---

## 1. Purpose

Build execution-focused projections and query APIs optimized for operational task discovery. Artifact projections already exist but lack execution state views (ready, blocked, assigned, eligible).

Operational queries must be supported through projections derived from artifact state in Git and execution state in the runtime store.

---

## 2. Scope

### In Scope

- Execution projection schema (task + execution state combined view)
- Projection update mechanism driven by workflow and Git events
- Execution query API for task discovery

### Out of Scope

- Artifact projection changes (already sufficient for artifact queries)
- Analytics or aggregation queries
- Dashboard UI (management platform)

---

## 3. Tasks

| Task | Title | Dependencies |
|------|-------|-------------|
| TASK-001 | Execution Projection Schema | None |
| TASK-002 | Execution Query API | TASK-001 |
