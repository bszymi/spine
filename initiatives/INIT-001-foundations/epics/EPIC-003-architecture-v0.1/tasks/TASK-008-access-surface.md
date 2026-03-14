---
id: TASK-008
type: Task
title: Access Surface v0.x
status: Pending
epic: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
  - type: supersedes
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/tasks/TASK-004-api-surface.md
---

# TASK-008 — Access Surface v0.x

---

## Purpose

Define the external access surface for Spine v0.x, including all supported interaction modes (CLI, API, GUI).

This task supersedes [TASK-004 — API Surface](/initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/tasks/TASK-004-api-surface.md), which was scoped to a single HTTP API. The architecture now recognizes multiple access patterns through the Access Gateway.

## Deliverable

`/architecture/access/v0.x.md`

Content should define:

- Supported access modes (CLI, API, GUI)
- External operations exposed by Spine
- Authentication and authorization model for actors
- Boundary between Spine core and external interfaces

## Acceptance Criteria

- Access surface is minimal and clearly justified
- Access modes (CLI/API/GUI) are defined
- Actor authentication and authorization model is specified
- Boundaries between external access and internal engine are documented
