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

## Architecture

The credential helper uses `SMP_WORKSPACE_ID` to identify which workspace's credentials to fetch. How this ID reaches the helper differs by deployment mode:

**Dedicated mode** (one Spine instance per workspace):
- SMP sets `SMP_WORKSPACE_ID` as a static container environment variable at provisioning time
- The credential helper reads it from the process environment
- One container = one workspace = one credential

**Shared mode** (multiple workspaces per Spine instance):
- SMP passes `smp_workspace_id` when calling `POST /workspaces` on Spine
- Spine stores it in the workspace registry
- When pushing, Spine sets `SMP_WORKSPACE_ID` dynamically in the git push environment
- Each push uses the correct workspace's credential

---

## Key Work Areas

- Configure Git to use an external credential helper
- Store SMP workspace ID in workspace config (shared mode)
- Set `SMP_WORKSPACE_ID` in git push environment (both modes)
- Graceful handling when no credentials are configured (skip push, don't retry loop)

---

## Acceptance Criteria

- Spine uses configured credential helper for `git push` authentication
- Credential helper receives `SMP_WORKSPACE_ID` in all push scenarios
- When no credentials configured, push is skipped (not retried indefinitely)
- Works in both dedicated (static env) and shared (dynamic env) modes
- Existing behavior unchanged when credential helper is not configured
