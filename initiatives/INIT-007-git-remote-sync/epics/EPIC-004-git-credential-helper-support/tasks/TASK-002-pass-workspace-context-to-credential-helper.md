---
id: TASK-002
type: Task
title: "Pass workspace context to credential helper"
status: Completed
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-002 — Pass workspace context to credential helper

---

## Purpose

The credential helper needs the SMP workspace ID to fetch the correct credentials. In shared mode, Spine serves multiple workspaces, each with different Git remotes and credentials. Spine must pass the right workspace identity to the credential helper for each push operation.

## Deliverable

`internal/workspace/config.go`, `internal/git/cli.go`, `internal/gateway/handlers_workspaces.go` updates

Content should define:

### Store SMP workspace ID

- Add `smp_workspace_id` field to workspace `Config` struct
- Accept `smp_workspace_id` in `POST /workspaces` request body
- Store in workspace registry alongside existing config

### Set environment per-push

- In shared mode: read `smp_workspace_id` from workspace config, set `SMP_WORKSPACE_ID` in git push environment
- In dedicated mode: read `SMP_WORKSPACE_ID` from container environment (set by SMP provisioner at container creation)
- Both modes: credential helper reads `$SMP_WORKSPACE_ID` from environment

### Git push environment setup

- `Push()` method receives workspace context (or workspace ID)
- Adds `SMP_WORKSPACE_ID` to the command environment before executing `git push`
- Works for all push scenarios: run merge, artifact commit, branch push

## Acceptance Criteria

- `POST /workspaces` accepts and stores `smp_workspace_id`
- Shared mode: `SMP_WORKSPACE_ID` set dynamically per-push from workspace config
- Dedicated mode: `SMP_WORKSPACE_ID` read from container environment
- Credential helper can read the workspace ID for all push operations
- Existing push behavior unchanged when `smp_workspace_id` is not set
