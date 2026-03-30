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

### 1. Workflow definition struct

`internal/workflow/` — add `Mode string` field to the workflow definition struct (yaml tag: `mode`)

### 2. Parser update

`internal/workflow/parser.go` — read the `mode` field during parsing. Default to `"execution"` if absent.

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
