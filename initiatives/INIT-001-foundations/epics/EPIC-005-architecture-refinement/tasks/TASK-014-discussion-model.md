---
id: TASK-014
type: Task
title: Discussion and Comment Runtime Model
status: Completed
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
---

# TASK-014 — Discussion and Comment Runtime Model

---

## Purpose

Define the runtime storage and interaction model for discussions and comments, implementing the governance decisions from ADR-003.

## Deliverable

`/architecture/discussion-model.md`

Content should define:

- Discussion thread schema (fields, relationships to artifacts and Runs)
- Storage location (runtime database, not Git — per ADR-003)
- How discussions are linked to artifacts, steps, and Runs
- Comment structure (author, timestamp, content, thread context)
- How discussion outcomes are converted to durable artifacts
- Retention policy for discussions that don't produce durable outcomes
- Access control for discussions (who can create, read, resolve)

## Acceptance Criteria

- Discussion schema is defined with clear fields and relationships
- Storage model is consistent with ADR-003 decisions
- Conversion path from discussion to durable artifact is specified
- Retention and lifecycle rules are defined
- Model is consistent with the data model, security model, and observability
