---
id: TASK-004
type: Task
title: Add repository management API and CLI
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
last_updated: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-003-repository-registry-service.md
  - type: related_to
    target: /architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md
---

# TASK-004 - Add Repository Management API and CLI

---

## Purpose

Expose repository registration and management through supported Spine interfaces.

## Deliverable

Add API and CLI operations for repository management.

API operations:

- `POST /repositories`
- `GET /repositories`
- `GET /repositories/{repository_id}`
- `PUT /repositories/{repository_id}`
- `POST /repositories/{repository_id}/deactivate`

CLI commands:

- `spine repository register`
- `spine repository list`
- `spine repository inspect`
- `spine repository deactivate`

## Acceptance Criteria

- API spec documents request and response schemas.
- CLI commands send workspace-scoped API requests.
- Registration validates repository ID and clone URL.
- Deactivation refuses when active runs reference the repository.
- Responses redact credential and secret material.

