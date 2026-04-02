---
id: TASK-004
type: Task
title: CLI workspace support
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
---

# TASK-004 — CLI workspace support

---

## Purpose

Add workspace selection to the Spine CLI as described in [product-definition.md §5.5](/product/product-definition.md).

## Deliverable

Updates to `cmd/spine/` CLI commands.

Content should define:

- Global `--workspace` flag on all CLI commands that talk to the API
- `spine config set workspace <id>` to persist a default workspace
- Workspace ID sent as a header on every API request from the CLI
- Priority: explicit `--workspace` flag > persisted config > env var `SPINE_WORKSPACE_ID`
- Clear error when no workspace is configured and the server requires one

## Acceptance Criteria

- `--workspace` flag is available on all relevant CLI commands
- Persisted workspace config works (`spine config set workspace`)
- CLI sends workspace ID on every API request
- Missing workspace produces a helpful error message
