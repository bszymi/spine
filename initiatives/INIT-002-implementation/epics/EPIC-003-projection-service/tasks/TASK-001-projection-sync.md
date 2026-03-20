---
id: TASK-001
type: Task
title: Projection Sync Engine
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
---

# TASK-001 — Projection Sync Engine

## Purpose

Implement full and incremental projection sync from Git to PostgreSQL.

## Deliverable

- Full rebuild: scan entire repository, parse all artifacts, populate projection tables
- Incremental sync: diff commits, update only changed projections
- Artifact link denormalization into `projection.artifact_links`
- Workflow definition projection into `projection.workflows`
- Sync state tracking (`projection.sync_state`)
- Polling loop for change detection
- Event-triggered sync (react to artifact_created/artifact_updated events)

## Acceptance Criteria

- Full rebuild from empty DB produces correct projections for all artifacts
- Incremental sync correctly handles create, update, and delete
- Link denormalization produces correct bidirectional graph
- Workflow projections include parsed definition and applies_to
- Sync state records last synced commit
- Full rebuild and incremental sync produce identical end state
- Integration tests with real Git repos and PostgreSQL
