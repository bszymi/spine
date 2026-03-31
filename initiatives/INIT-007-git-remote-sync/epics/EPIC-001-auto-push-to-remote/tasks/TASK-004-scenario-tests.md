---
id: TASK-004
type: Task
title: Scenario tests for auto-push
status: Done
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
  - type: blocked_by
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/tasks/TASK-003-push-after-branch-operations.md
---

# TASK-004 — Scenario Tests for Auto-Push

---

## Purpose

Validate that all Git operations are automatically pushed to a remote.

---

## Deliverable

`internal/scenariotest/scenarios/git_remote_sync_test.go`

Test setup: create a bare Git repo as a fake "origin", clone it as the Spine repo, configure origin.

Scenarios:
1. Artifact create on main → commit appears on origin main
2. Planning run start → branch appears on origin
3. Artifact create via write_context → commit pushed to branch on origin
4. Run approval + merge → main pushed, branch deleted on origin
5. Run cancellation → branch deleted on origin
6. Auto-push disabled → nothing pushed

---

## Acceptance Criteria

- Tests use a local bare repo as fake origin (no real GitHub/Bitbucket needed)
- All 6 scenarios pass
- Tests clean up after themselves
