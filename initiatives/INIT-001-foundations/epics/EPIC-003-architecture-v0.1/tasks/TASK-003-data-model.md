# TASK-003 — Data Model

**Epic:** EPIC-003 — Architecture v0.1
**Initiative:** INIT-001 — Foundations
**Status:** Complete

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
