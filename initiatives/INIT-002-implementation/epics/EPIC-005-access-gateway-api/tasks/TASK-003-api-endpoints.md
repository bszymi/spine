---
id: TASK-003
type: Task
title: API Endpoint Handlers
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
---

# TASK-003 — API Endpoint Handlers

## Purpose

Implement all API endpoint handlers, connecting HTTP requests to internal service calls.

## Deliverable

- Artifact operation handlers (create, read, update, validate, list, links)
- Workflow operation handlers (run.start, run.status, run.cancel, step.submit, step.assign)
- Task governance handlers (accept, reject, cancel, abandon, supersede)
- Query handlers (artifacts, graph, history, runs)
- System handlers (health, rebuild, rebuild status, validate_all)
- WriteContext support for run-scoped artifact writes
- Idempotency-Key enforcement on write operations
- Cursor-based pagination

## Acceptance Criteria

- All 25 endpoints from api-spec.yaml have working handlers
- Request validation rejects malformed input with structured errors
- WriteContext correctly routes to authoritative vs task branch
- Pagination returns correct cursors and has_more flag
- End-to-end tests: HTTP request → service call → Git commit → response
