---
id: TASK-003
type: Task
title: Add mode field to workflow definition and parser
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
---

# TASK-003 — Add Mode Field to Workflow Definition and Parser

---

## Purpose

Extend the workflow definition format with an optional `mode` field (`execution` / `creation`) and update the parser to read it.

This field allows the workflow binding resolver to distinguish between execution workflows (used by standard runs) and creation workflows (used by planning runs). Existing workflows without a `mode` field default to `execution` for backward compatibility.

---

## Deliverable

### 1. Domain struct

`internal/domain/workflow.go` — add `Mode string` field (yaml tag: `mode`, json tag: `mode`) to `WorkflowDefinition`. This is where the field must live because `workflow.Parse()` unmarshals YAML directly into this domain struct. Adding it elsewhere would cause the field to be silently discarded.

### 2. Parser default

`internal/workflow/parser.go` — after parsing, if `Mode` is empty, set it to `"execution"`. Validate that `Mode` is one of `"execution"` or `"creation"`, reject other values.

### 3. Projection update

`internal/projection/` — if workflows are projected to PostgreSQL, ensure the `mode` field is stored and queryable.

---

## Acceptance Criteria

- `mode` field is parsed from YAML workflow definitions
- Absent `mode` defaults to `"execution"`
- Valid values: `"execution"`, `"creation"`
- Invalid mode values produce a parse error
- All existing workflows continue to parse correctly (backward compatible)
- Projection stores the `mode` field if applicable
