---
id: TASK-002
type: Task
title: Artifact Schema Validation
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# TASK-002 — Artifact Schema Validation

## Purpose

Validate artifact front matter against the schema rules defined in artifact-schema.md.

## Deliverable

- Required field validation per artifact type
- Status enum validation per artifact type (artifact-schema.md §6)
- Link format validation (canonical path format per §3.2)
- Link type validation (valid link types per §4.1)
- ID format validation (per naming-conventions.md §2)
- Structured validation errors with field paths

## Acceptance Criteria

- Validation covers all rules from artifact-schema.md §2-6
- Each artifact type has specific validation rules
- Invalid artifacts produce clear, structured error messages
- Validation is composable (can validate individual fields or full artifact)
- Unit tests cover valid and invalid cases for every artifact type
