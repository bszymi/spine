---
id: TASK-004
type: Task
title: CLI workspace management commands
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/tasks/TASK-001-workspace-management-api.md
---

# TASK-004 — CLI workspace management commands

---

## Purpose

Add CLI commands for workspace lifecycle management, calling the API endpoints from TASK-001.

## Deliverable

New file `cmd/spine/cmd_workspace.go` and updates to `cmd/spine/main.go`.

Commands:

- `spine workspace create <workspace_id> [--name "Display Name"]` — create and provision a new workspace
- `spine workspace list` — list all workspaces with ID, name, status
- `spine workspace get <workspace_id>` — show workspace details
- `spine workspace deactivate <workspace_id>` — deactivate a workspace

## Acceptance Criteria

- All four commands work and produce clear output (table and JSON formats)
- `workspace create` reports provisioning progress (database created, migrations applied, repo initialized)
- `workspace deactivate` prompts for confirmation or accepts `--yes` flag
- Commands use the global `--workspace` flag for targeting the management API (or connect directly if on the same instance)
