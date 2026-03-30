---
id: TASK-005
type: Task
title: Update API spec
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-005 — Update API Spec

---

## Purpose

Update the OpenAPI specification to document the planning run API.

---

## Deliverable

`api/spec.yaml`

Updates:
- `RunStartRequest` schema: add `mode` (enum: standard, planning) and `artifact_content` (string) properties
- `RunResponse` and `RunStatusResponse` schemas: add `mode` field
- Description text for `POST /runs`: document planning mode behavior
- Add validation note: `artifact_content` required when `mode=planning`

---

## Acceptance Criteria

- Spec validates with an OpenAPI linter
- New fields are documented with descriptions
- Existing schema definitions are not broken
