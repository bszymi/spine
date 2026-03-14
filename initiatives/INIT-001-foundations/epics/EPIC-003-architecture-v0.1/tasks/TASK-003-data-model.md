---
id: TASK-003
type: Task
title: Data Model
status: Completed
epic: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
---

# TASK-003 — Data Model

---

## Purpose

Define the data model for Spine, including the relationship between Git truth and database projections.

## Deliverable

`/architecture/data-model.md`

Content should define:

- what lives in Git (source of truth)
- what lives in the database (projections, runtime state)
- projection and rebuild mechanisms
- reconciliation strategy
- queue and event model

## Acceptance Criteria

- Git truth vs DB projection boundary is clearly defined
- disposable database principle is operationally concrete
- rebuild and reconciliation strategies are documented
