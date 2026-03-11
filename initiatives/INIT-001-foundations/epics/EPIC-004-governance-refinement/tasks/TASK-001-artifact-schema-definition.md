# TASK-001 — Artifact Schema Definition

**Epic:** EPIC-004 — Governance Refinement
**Initiative:** INIT-001 — Foundations
**Status:** Pending

---

## Purpose

Define the YAML front matter schema for each artifact type in Spine so that metadata is machine-readable, consistent, and self-describing.

The domain model requires that artifact metadata is stored in Markdown front matter and that link targets use globally unambiguous references. This task specifies the concrete fields, formats, and conventions that make that possible.

## Deliverable

`/governance/artifact-schema.md`

Content should define:

- Required and optional front matter fields per artifact type (Initiative, Epic, Task, ADR, Governance)
- Field types and allowed values (e.g., status enums, date formats)
- Link format for artifact references (globally unambiguous)
- Bidirectional link conventions and which link types have inverse semantics
- Example front matter blocks for each artifact type

## Acceptance Criteria

- Every artifact type has a clearly documented schema
- Required vs optional fields are distinguished
- Link target format is specified and globally resolvable
- Examples are provided for each artifact type
- Schema is consistent with the domain model and ADR-004
