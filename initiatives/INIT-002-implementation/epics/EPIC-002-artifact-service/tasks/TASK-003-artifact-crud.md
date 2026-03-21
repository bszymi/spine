---
id: TASK-003
type: Task
title: Artifact CRUD Operations
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# TASK-003 — Artifact CRUD Operations

## Purpose

Implement create, read, update operations on artifacts with proper Git commits and event emission.

## Deliverable

- `artifact.Create` — validate + write file + commit with trailers + emit event
- `artifact.Read` — read from Git (specific ref or HEAD)
- `artifact.Update` — validate + update file + commit with trailers + emit event
- `artifact.List` — scan repository for artifacts
- Atomic commits (per Git Integration §5.3)
- Commit author set to actor identity (per Git Integration §5.2)
- Domain event emission (artifact_created, artifact_updated)

## Acceptance Criteria

- Create produces a valid Git commit with structured trailers
- Read returns artifact from any Git ref
- Update validates before committing; rejects invalid changes
- Duplicate creation (same path) is rejected
- Integration tests against temporary Git repos
- Events emitted for every write operation
