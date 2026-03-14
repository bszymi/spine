---
id: TASK-004
type: Task
title: API Surface
status: Cancelled
epic: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
  - type: superseded_by
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/tasks/TASK-008-access-surface.md
---

# TASK-004 — API Surface

---

## Purpose

Define the minimal API surface for Spine v0.x.

## Deliverable

`/architecture/api/v0.x.md`

Content should define:

- core API endpoints or operations
- resource model exposed by the API
- authentication and authorization model at v0.x
- API boundaries (what is exposed vs internal)

## Acceptance Criteria

- API surface is minimal and justified
- resource model aligns with the domain model
- security model for actors is defined at v0.x level

---

## Cancellation Note

Closed because the architecture no longer defines a single API surface. Spine exposes multiple access patterns (CLI, API, GUI). A new task will define the full external access surface instead of only an HTTP API.

**Successor:** [TASK-008 — Access Surface v0.x](/initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/tasks/TASK-008-access-surface.md)
