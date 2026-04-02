---
id: TASK-002
type: Task
title: File/env workspace provider
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/tasks/TASK-001-config-and-resolver-interface.md
---

# TASK-002 — File/env workspace provider

---

## Purpose

Implement the `WorkspaceResolver` backed by environment variables. This wraps Spine's current single-workspace configuration behind the resolver interface, providing full backward compatibility.

## Deliverable

`internal/workspace/file_provider.go`

Content should define:

- A resolver that reads `SPINE_DATABASE_URL`, `SPINE_REPO_PATH`, and other current env vars
- Returns a single `WorkspaceConfig` for the configured workspace
- `List()` returns a slice of one workspace
- Optionally supports reading from a YAML/JSON config file as an alternative to env vars

## Acceptance Criteria

- Implements `WorkspaceResolver` interface
- When configured, Spine runtime behavior is identical to current single-workspace mode
- Unit tests cover: resolve success, resolve with wrong ID, list returns one entry
