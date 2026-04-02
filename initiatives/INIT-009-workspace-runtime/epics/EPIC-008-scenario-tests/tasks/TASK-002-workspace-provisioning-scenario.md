---
id: TASK-002
type: Task
title: Workspace provisioning scenario test
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
---

# TASK-002 — Workspace provisioning scenario test

---

## Purpose

Verify the full workspace provisioning flow: create via API, provision database, provision Git repo, activate, and use.

## Deliverable

New scenario test file `internal/scenariotest/scenarios/workspace_provisioning_test.go`.

Test flow:
1. Call POST /api/v1/workspaces to create a workspace
2. Verify workspace is created with inactive status
3. Run database provisioning (create DB, apply migrations)
4. Run Git repo provisioning (fresh mode)
5. Activate the workspace
6. Create an artifact in the newly provisioned workspace
7. Run projection sync and verify the artifact appears in queries
8. Test clone mode: create workspace from an existing Spine repo, verify projection sync populates database

## Acceptance Criteria

- Fresh provisioning produces a usable workspace
- Clone provisioning detects Spine repo and syncs projections
- Provisioning failure cleans up partial resources
