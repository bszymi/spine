---
id: TASK-004
type: Task
title: "Implement workflow.* Gateway Handlers"
status: Pending
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: blocked_by
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/tasks/TASK-003-extend-openapi-spec.md
---

# TASK-004 — Implement workflow.* Gateway Handlers

---

## Context

With the OpenAPI spec defining the new resource, the Gateway needs corresponding handlers that delegate to the workflow service and the validation service.

## Deliverable

- Add handlers for `workflow.create`, `workflow.update`, `workflow.read`, `workflow.list`, and `workflow.validate` in `internal/gateway/`.
- Wire each write operation to the full workflow validation suite per [Workflow Validation](/architecture/workflow-validation.md); reject with structured `validation_failed` errors on any failure.
- Produce a single atomic Git commit with structured trailers for create and update (per the invariant in [Git Integration](/architecture/git-integration.md) §5).
- Enforce authorization: reviewer role required for create/update.
- Provide unit tests for each handler and an integration test exercising create → read → update → validate → read.

## Acceptance Criteria

- All five handlers are registered and match their OpenAPI schemas.
- Workflow-specific validation runs before commit; failures do not produce partial state.
- Test coverage for the new handlers is ≥80% (project invariant).
