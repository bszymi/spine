---
id: TASK-008
type: Task
title: "Update User-Facing Documentation for Workflow API Split"
status: Pending
work_type: documentation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: blocked_by
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/tasks/TASK-004-implement-workflow-handlers.md
  - type: blocked_by
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/tasks/TASK-005-reject-workflows-on-artifact-endpoints.md
  - type: blocked_by
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/tasks/TASK-007-update-cli-surface.md
---

# TASK-008 — Update User-Facing Documentation for Workflow API Split

---

## Context

TASK-001, TASK-002, and TASK-006 cover architecture and governance documents. The project also ships user-facing documentation — `README.md`, `CONTRIBUTING.md`, `docs/integration-guide.md`, `KNOWN-LIMITATIONS.md`, and any CLI help / templates — that may reference workflow authoring against the generic artifact endpoints or describe the old CLI surface. These need to be brought in line with the new operations.

## Deliverable

Sweep and update:

- `README.md` — any quick-start or API usage examples that mention workflow creation/editing.
- `CONTRIBUTING.md` — workflow authoring section, if present; add guidance that workflow definitions are written through the dedicated operations.
- `docs/integration-guide.md` — platform-facing documentation of the workflow surface; update endpoint references, example payloads, and any guidance about programmatic workflow management.
- `KNOWN-LIMITATIONS.md` — remove any entries the split resolves; add new ones if discovered.
- Any `templates/` content or CLI-generated help text that references workflow authoring via artifact endpoints.

Every change must reference [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) as the governing decision in prose where a rationale is appropriate.

## Acceptance Criteria

- No remaining example or prose in user-facing documentation shows workflow writes through the generic artifact endpoints or the old CLI surface.
- Integration guide is consistent with the updated OpenAPI spec and the new CLI commands.
- A grep of the repo for workflow authoring examples returns only the updated paths.
