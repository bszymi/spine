---
id: EPIC-004
type: Epic
title: "Git Credential Helper Support"
status: Pending
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/initiative.md
  - type: depends_on
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
---

# EPIC-004 — Git Credential Helper Support

---

## Purpose

Enable Spine to authenticate with remote Git repositories for push operations. Supports three usage patterns: standalone (env var token), managed platform (credential helper), and user-configured (native Git). Also provides documentation for developers building their own management platforms.

---

## Credential Resolution Chain

Spine resolves push credentials in priority order:

1. **External credential helper** (`SPINE_GIT_CREDENTIAL_HELPER`) — Git calls an external script to get credentials. Used by SMP and custom management platforms.
2. **Built-in token** (`SPINE_GIT_PUSH_TOKEN`) — Spine injects the token into the push URL. Simplest option for standalone/Docker deployments.
3. **Git native** — User's own credential configuration (SSH keys, credential store, etc.). Spine does nothing extra.
4. **None** — Push skipped gracefully. Run completes without pushing.

---

## Deployment Mode Handling

**Dedicated mode** (one Spine instance per workspace):
- `SMP_WORKSPACE_ID` set as container env var by the management platform
- Or `SPINE_GIT_PUSH_TOKEN` set directly (standalone, no platform)

**Shared mode** (multiple workspaces per Spine instance):
- Platform passes `smp_workspace_id` via `POST /workspaces`
- Spine sets `SMP_WORKSPACE_ID` dynamically per-push from workspace config

---

## Tasks

1. **TASK-001**: Configure Git to use external credential helper
2. **TASK-002**: Store `smp_workspace_id`, pass to credential helper per-push
3. **TASK-003**: Graceful push handling without credentials
4. **TASK-004**: Built-in `SPINE_GIT_PUSH_TOKEN` for standalone deployments
5. **TASK-005**: Integration guide for management platform developers

---

## Acceptance Criteria

- Standalone users can push with a single env var (`SPINE_GIT_PUSH_TOKEN`)
- Management platforms can integrate via credential helper protocol
- Push skipped gracefully when no credentials configured
- Works in both dedicated and shared deployment modes
- Integration guide enables third-party platform development
