---
id: TASK-003
type: Task
title: "Add /workflows Endpoints to OpenAPI Spec"
status: Completed
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
---

# TASK-003 — Add /workflows Endpoints to OpenAPI Spec

---

## Context

The OpenAPI specification (`api/spec.yaml`) must define the new workflow resource before handlers can be implemented and the CLI updated.

## Deliverable

Update `/api/spec.yaml`:

- Add paths:
  - `POST /workflows` → `workflow.create`
  - `GET /workflows` → `workflow.list`
  - `GET /workflows/{id}` → `workflow.read`
  - `PUT /workflows/{id}` → `workflow.update`
  - `POST /workflows/{id}/validate` → `workflow.validate`
- Add matching request/response schemas (workflow body, summary, validation report, error responses).
- Update `POST /artifacts`, `PATCH /artifacts/{path}`, and `POST /artifacts/add` to document `400 invalid_params` when the target is a workflow path, with an error payload pointing to the corresponding `workflow.*` operation.
- Update `GET /artifacts/{path}` to document that workflow paths return summary metadata only.
- Add a new `Workflows` tag.

## Acceptance Criteria

- Spec lints cleanly (existing lint / validation checks pass).
- All five new operations are present with complete schemas.
- Rejection responses for workflow paths are documented on every affected generic artifact operation.
