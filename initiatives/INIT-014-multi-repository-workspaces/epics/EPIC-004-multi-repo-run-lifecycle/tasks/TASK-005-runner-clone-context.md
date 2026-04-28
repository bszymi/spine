---
id: TASK-005
type: Task
title: Include clone context in actor assignments
status: Pending
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

