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

Enable Spine to authenticate with remote Git repositories using credentials managed by the hosting platform (Spine Management Platform). Uses the standard Git credential helper mechanism: Spine configures Git to call an external script that retrieves credentials from the platform's encrypted secret store at push time.

This was explicitly out-of-scope for EPIC-001 (auto-push), which handles the push mechanics. This epic handles the authentication mechanics.

---

## Key Work Areas

- Configure Git to use an external credential helper
- Pass workspace identity to the credential helper via environment
- Graceful handling when no credentials are configured (skip push, don't retry loop)
- Support for both single-workspace and shared (multi-workspace) deployment modes

---

## Primary Outputs

- Git credential helper configuration in Spine's git setup
- Workspace ID passed to credential helper via environment variable
- `GIT_PUSH_ENABLED` config flag (false when no credentials, prevents retry loop)
- Documentation for credential helper protocol integration

---

## Acceptance Criteria

- Spine uses configured credential helper for `git push` authentication
- Credential helper receives workspace_id to request workspace-specific credentials
- When no credentials configured, push is skipped (not retried indefinitely)
- Works in both dedicated (single workspace) and shared (multi-workspace) modes
