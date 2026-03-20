---
id: EPIC-003
type: Epic
title: Projection Service
status: Pending
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# EPIC-003 — Projection Service

---

## Purpose

Build the Projection Service — the component that synchronizes Git artifact state into PostgreSQL for fast querying. After this epic, Spine has a queryable artifact database that stays in sync with Git.

---

## Validates

- [Data Model](/architecture/data-model.md) §2.2, §4 — Projection layer and rebuild
- [System Components](/architecture/components.md) §4.4-4.5 — Projection Service and Store
- [Runtime Schema](/architecture/runtime-schema.md) §3 — Projection tables

---

## Acceptance Criteria

- Full projection rebuild from Git produces a correct database
- Incremental sync updates only changed artifacts
- Artifact link graph is denormalized and queryable
- Projection freshness is tracked (source_commit, synced_at)
- Polling-based change detection works
- Rebuild from empty database produces identical state to incremental sync
- Integration tests verify projection accuracy against Git content
