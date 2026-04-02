---
id: TASK-001
type: Task
title: Update components.md deployment considerations
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
  - type: related_to
    target: /architecture/components.md
---

# TASK-001 — Update components.md deployment considerations

---

## Purpose

Update the deployment considerations section of `architecture/components.md` to describe the shared runtime model alongside the existing single-instance model.

## Deliverable

Updates to `architecture/components.md` §6.

Content should describe:

- The workspace routing layer as a new runtime concept: `WorkspaceResolver` resolves workspace config, service pool manages per-workspace service sets
- Single mode: one workspace, one repo, one database per process (current v0.x model, retained)
- Shared mode: multiple workspaces per process, each with its own repo and database, resolved at the request boundary
- The service pool pattern: lazy initialization of per-workspace Git client, store, artifact service, projection service, engine
- Global system routes (health, metrics) remain workspace-exempt

## Acceptance Criteria

- v0.x single-instance description is retained as the baseline/default
- Shared runtime model is described as an additional deployment option
- The workspace resolver and service pool are introduced as new architectural components
- The distinction between logical workspace isolation and deployment topology is clear
