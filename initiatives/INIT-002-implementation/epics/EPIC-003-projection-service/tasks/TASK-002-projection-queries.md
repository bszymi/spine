---
id: TASK-002
type: Task
title: Projection Query Layer
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
---

# TASK-002 — Projection Query Layer

## Purpose

Implement query operations against the Projection Store for artifact search, graph traversal, and run history.

## Deliverable

- Query by artifact type, status, parent path (with cursor pagination)
- Full-text search across title and content
- Link graph traversal with configurable depth
- Artifact history from Git log
- Run queries by task path and status
- Projection freshness check (source_commit vs HEAD)

## Acceptance Criteria

- All query operations from Access Surface §3.4 are implemented
- Pagination works correctly (cursor-based, stable ordering)
- Graph traversal respects depth limits
- Queries read from projection tables (not Git directly, except history)
- Performance is acceptable for test-scale data (< 100ms for typical queries)
- Unit tests for query building, integration tests for query execution
