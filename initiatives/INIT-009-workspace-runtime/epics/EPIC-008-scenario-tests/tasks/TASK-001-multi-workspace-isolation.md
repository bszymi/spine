---
id: TASK-001
type: Task
title: Multi-workspace isolation scenario test
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
---

# TASK-001 — Multi-workspace isolation scenario test

---

## Purpose

Verify that two workspaces running in the same Spine instance are fully isolated: artifacts, runs, actors, and projections in one workspace are invisible to the other.

## Deliverable

New scenario test file `internal/scenariotest/scenarios/workspace_isolation_test.go`.

Test flow:
1. Set up two workspaces (ws-alpha, ws-beta) with separate databases and repos
2. Create an artifact in ws-alpha
3. Verify the artifact is visible via ws-alpha's projection queries
4. Verify the artifact is NOT visible via ws-beta's projection queries
5. Start a run in ws-alpha
6. Verify the run is visible in ws-alpha but not ws-beta
7. Create an actor in ws-alpha
8. Verify the actor cannot authenticate against ws-beta

## Acceptance Criteria

- Test demonstrates complete isolation across artifacts, runs, and actors
- Both workspaces operate concurrently in the same process
- Test uses the scenario test harness with the `scenario` build tag
