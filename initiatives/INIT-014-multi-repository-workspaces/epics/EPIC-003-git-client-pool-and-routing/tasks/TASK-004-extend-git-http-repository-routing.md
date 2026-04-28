---
id: TASK-004
type: Task
title: Extend git HTTP repository routing
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-002-lazy-clone-and-client-initialization.md
---

# TASK-004 - Extend Git HTTP Repository Routing

---

## Purpose

Allow runners and external clients to clone any active repository in the workspace through Spine's git HTTP endpoint.

## Deliverable

Extend gateway parsing and git HTTP resolution for:

- `/git/{workspace_id}/{repository_id}/info/refs`
- `/git/{workspace_id}/{repository_id}/git-upload-pack`
- `/git/{workspace_id}/{repository_id}/git-receive-pack`

Fallbacks:

- `/git/{workspace_id}/...` resolves to `spine`.
- `/git/...` keeps existing single-workspace behavior.

## Acceptance Criteria

- Route parser returns workspace ID, repository ID, and git protocol path.
- Repository ID is resolved through the registry, never used as a filesystem path.
- Inactive repositories return not found or forbidden after auth.
- Trusted-CIDR and push-auth behavior remains unchanged.
- Scenario tests clone primary and code repos through git HTTP.

