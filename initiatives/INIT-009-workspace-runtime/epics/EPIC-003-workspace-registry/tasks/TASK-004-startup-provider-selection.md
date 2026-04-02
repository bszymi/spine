---
id: TASK-004
type: Task
title: Startup provider selection
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/tasks/TASK-002-file-env-provider.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/tasks/TASK-003-database-provider.md
---

# TASK-004 — Startup provider selection

---

## Purpose

Wire provider selection into Spine's startup so the runtime knows which workspace resolver to use.

## Deliverable

Updates to `cmd/spine/main.go` (serve command).

Content should define:

- `SPINE_WORKSPACE_MODE` env var: `single` (default, file/env provider) or `shared` (database provider)
- In shared mode, `SPINE_REGISTRY_DATABASE_URL` points to the registry database
- Startup validates the selected provider can initialize successfully
- The resolved provider is available for injection into gateway and background services

## Acceptance Criteria

- Spine starts in single-workspace mode by default (no config change required)
- Setting `SPINE_WORKSPACE_MODE=shared` switches to the database provider
- Missing or invalid provider configuration produces a clear startup error
