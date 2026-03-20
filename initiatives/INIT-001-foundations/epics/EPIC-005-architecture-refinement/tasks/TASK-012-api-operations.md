---
id: TASK-012
type: Task
title: API Operation Schemas
status: Completed
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
---

# TASK-012 — API Operation Schemas

---

## Purpose

Expand the Access Surface operation categories into detailed request/response schemas for API and CLI implementation.

## Deliverable

`/architecture/api-operations.md`

Content should define:

- JSON request/response schema for each operation (artifact.create, artifact.update, run.start, step.submit, etc.)
- Error response format and error codes
- Authentication/authorization requirements per operation
- Pagination and filtering for query operations
- Request validation rules
- Examples for key operations

## Acceptance Criteria

- All operations from Access Surface §3 have detailed schemas
- Error codes and conditions are enumerated
- Authorization requirements per operation are specified
- Schemas are consistent with the access surface, domain model, and internal operation model
