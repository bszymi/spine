---
id: TASK-004
type: Task
title: Route steps to target repositories
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-002-create-run-branches-across-repositories.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-007-step-routing-decision-adr.md
---

# TASK-004 - Route Steps to Target Repositories

---

## Purpose

Tell actors and automated runners which repository a step should operate in, implementing the routing model accepted in TASK-007's ADR.

## Deliverable

Implement repository context resolution for step execution exactly as specified in the routing ADR produced by TASK-007. No new design choices live in this task — it is implementation only.

The implementation must:

- Resolve the target repository for each step using the order and rules defined in the ADR.
- Surface the resolved repository in step assignment payloads.
- Fail workflow validation when explicit step `repository` references unknown or inactive repos.
- Emit structured logs/metrics on per-step routing decisions for observability.

## Acceptance Criteria

- Behavior matches the routing model in the TASK-007 ADR (cite the ADR path in the PR).
- Step assignments include target repository ID.
- Primary-repo governance steps continue to target `spine`.
- Workflow validation catches invalid explicit step repository IDs.
- The "ambiguous" / unresolved branch (if any) behaves exactly as the ADR prescribes — not as an open implementation choice.
- Tests cover every resolution branch the ADR defines.

