---
id: TASK-027
type: Task
title: Validate workspace IDs before persistence and provisioning
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: security
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-027 — Validate workspace IDs before persistence and provisioning

---

## Purpose

`workspace_id` is currently accepted as any non-empty string by the operator API and persisted in the workspace registry. The repo provisioner later joins that ID directly with the workspace repository base directory. A traversal-shaped ID can escape the intended base path during provisioning and cleanup.

## Deliverable

- Add a shared `workspace.ValidateID` helper with a conservative allowlist, for example `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`.
- Enforce the validator in the workspace create handler before database lookup/insert.
- Enforce the same validator in `RepoProvisioner.ProvisionRepo`, `DatabaseProvisioner.ProvisionDatabase`, and registry resolver/provider entry points that accept caller-provided workspace IDs.
- Update CLI error handling so invalid workspace IDs fail before sending operator requests when possible.

## Acceptance Criteria

- `../x`, `/tmp/x`, `a/b`, IDs with whitespace, IDs starting with `-`, and empty IDs are rejected.
- Valid existing-style IDs such as `ws-1`, `ws_alpha`, and `spine42` continue to work.
- Provisioning tests assert the final repo path stays inside `SPINE_WORKSPACE_REPOS_DIR`.
- Registry and gateway tests cover invalid ID rejection before persistence.
