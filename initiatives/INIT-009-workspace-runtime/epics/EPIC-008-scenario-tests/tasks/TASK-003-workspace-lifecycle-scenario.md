---
id: TASK-003
type: Task
title: Workspace lifecycle and deactivation scenario test
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
---

# TASK-003 — Workspace lifecycle and deactivation scenario test

---

## Purpose

Verify workspace lifecycle: creation, active usage, deactivation, and post-deactivation behavior.

## Deliverable

New scenario test file `internal/scenariotest/scenarios/workspace_lifecycle_test.go`.

Test flow:
1. Create and provision a workspace
2. Create artifacts and run workflows in it
3. Deactivate the workspace
4. Verify requests to the deactivated workspace are rejected (403 or similar)
5. Verify the deactivated workspace's service set is evicted from the pool
6. Verify other workspaces continue operating normally

## Acceptance Criteria

- Active workspace serves requests normally
- Deactivated workspace rejects new requests immediately (not after cache TTL)
- Pool eviction occurs on deactivation
- Other workspaces are unaffected
