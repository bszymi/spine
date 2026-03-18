---
id: TASK-004
type: Task
title: Event Schema Specification
status: In Progress
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
---

# TASK-004 — Event Schema Specification

---

## Problem

ADR-002 establishes the event model — domain events are derived signals reconstructible from Git, operational events are ephemeral runtime signals. The data model defines a conceptual event envelope (event_id, event_type, timestamp, source_component, actor_id, run_id, artifact_path, source_commit, payload). ADR-002 explicitly lists event schema standards as future work.

However, no document defines the concrete schemas for individual event types. Without defined schemas, components cannot produce or consume events reliably, and the Event Router cannot validate event payloads.

## Objective

Define concrete schemas for all domain and operational event types referenced in the architecture.

## Deliverable

`/architecture/event-schemas.md`

Content should define:

- Schema for each domain event type:
  - `artifact_created`, `artifact_updated`, `artifact_superseded`
  - `run_started`, `run_completed`, `run_failed`, `run_cancelled`
  - `workflow_definition_changed`
- Schema for key operational event types:
  - `step_started`, `step_completed`, `step_failed`, `step_assigned`
  - `retry_attempted`
- Payload structure for each event type (required fields, optional fields, types)
- Event versioning strategy — how schemas evolve without breaking consumers
- Delivery guarantees per event category (at-least-once for domain events, best-effort for operational)
- How domain events are derived from Git commits (the reconstruction path)

## Acceptance Criteria

- All domain event types referenced in the architecture have concrete schemas
- Key operational event types have concrete schemas
- Payload fields are typed and documented
- Event versioning strategy is defined
- Schemas are consistent with the data model event envelope and ADR-002
