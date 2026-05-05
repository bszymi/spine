---
id: TASK-005
type: Task
title: Include clone context in actor assignments
status: Completed
acceptance: Approved
acceptance_rationale: |
  AssignmentContext extended with workspace_id and commit_baseline (both omitempty) on top of the ADR-015 triple. Per-repo baselines captured at run start by createRunBranches — primary via o.git.Head() (CreateBranch base is "HEAD"), code repos via client.RefSHA(base) so the recorded SHA matches what CreateBranch resolves base to regardless of the cached repo's working-tree HEAD. Persisted on runtime.runs.repository_baselines (migration 025, JSONB '{}' default). Scenario test drives `git clone --branch` against the workspace's git HTTP server using only fields from a constructed AssignmentContext. Codex 3 passes (1 P2 in pass 1: code-repo baseline must come from RefSHA(base) not Head; passes 2-3 clean).
last_updated: 2026-05-05
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-004-step-repository-routing.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-004-extend-git-http-repository-routing.md
---

# TASK-005 - Include Clone Context in Actor Assignments

---

## Purpose

Give runner containers enough structured data to clone the correct repository and branch.

## Deliverable

Extend assignment payloads with:

- Repository ID
- Git HTTP clone URL
- Branch name
- Workspace ID
- Commit baseline if available

## Acceptance Criteria

- Actor assignment payloads include clone context for execution steps.
- Clone URLs use Spine git HTTP routes, not external forge URLs.
- Existing actor clients tolerate missing clone context for non-execution steps.
- Scenario test verifies a runner can clone a code repo branch from an assignment.

