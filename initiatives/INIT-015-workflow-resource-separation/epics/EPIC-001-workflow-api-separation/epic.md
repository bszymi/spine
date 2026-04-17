---
id: EPIC-001
type: Epic
title: "Workflow API Separation"
status: Pending
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
---

# EPIC-001 — Workflow API Separation

---

## Purpose

Implement [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md): promote workflow definitions to a first-class API resource and remove them from the generic artifact endpoints.

---

## Key Work Areas

- Architecture and governance documentation updates (api-operations, access-surface, validation-service, artifact-schema)
- OpenAPI spec: new `/workflows` endpoints, rejection semantics on `/artifacts*`
- Gateway handlers: implement `workflow.{create, update, read, list, validate}`
- Generic artifact handlers reject workflow-path targets
- Workflow validation suite wired into the new operations
- CLI surface updated to use the new endpoints

---

## Acceptance Criteria

- `workflow.create`, `workflow.update`, `workflow.read`, `workflow.list`, and `workflow.validate` operations exist, documented and conformant with the OpenAPI spec.
- Requests to `artifact.create`, `artifact.update`, and `artifact.add` targeting workflow paths return `400 invalid_params` with an error pointing to the corresponding `workflow.*` operation.
- `artifact.read` against a workflow path returns summary metadata only.
- All supporting documentation (architecture, governance, OpenAPI) is updated.
- CLI commands that previously wrote workflows via generic artifact operations now use the new operations.
