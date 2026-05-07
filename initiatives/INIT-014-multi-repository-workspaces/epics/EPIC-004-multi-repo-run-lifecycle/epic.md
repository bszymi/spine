---
id: EPIC-004
type: Epic
title: "Multi-Repo Run Lifecycle"
status: Completed
acceptance: Approved
acceptance_rationale: |
  All seven tasks completed and approved on main: TASK-001
  (run.affected_repositories model + persistence migration 021),
  TASK-002 (create run branches across affected repositories with
  partial-failure rollback), TASK-003 (clean up partial branch
  creation on failed StartRun), TASK-004 (step repository routing
  per ADR-015), TASK-005 (runner clone context — workspace_id /
  repository_id / branch_name / commit_baseline in assignment
  payloads, migration 025), TASK-006 (multi-repo run lifecycle
  scenario tests via scenariotest framework), and TASK-007
  (step-routing decision ADR-015 — single repository_id per
  execution resolved at run start). The epic's primary outputs
  all shipped: AffectedRepositories field on domain.Run, branch
  fan-out across every affected repo, single-repo-per-step
  assignment routing, end-to-end scenario coverage. Epic
  frontmatter was left at Pending after PR-level task closures
  landed; corrected here as part of the post-INIT-014 status
  coherence sweep.
last_updated: 2026-05-07
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
---

# EPIC-004 - Multi-Repo Run Lifecycle

---

## Purpose

Create and route run branches across the primary Spine repository and every code repository affected by a task.

This epic makes execution multi-repo aware before merge coordination is added.

---

## Scope

### In Scope

- Run model updates for affected repositories
- Branch creation across all affected repositories
- Cleanup when branch creation fails partway through
- Step repository routing and runner clone instructions
- Actor-facing assignment payload updates

### Out of Scope

- Final merge coordination
- Cross-repo atomic transactions
- Cross-repo divergence

---

## Primary Outputs

- Multi-repo run startup path
- Repository-aware step execution context
- Assignment payloads with clone URL, repo ID, and branch name
- Tests for startup, routing, and cleanup behavior

---

## Acceptance Criteria

1. Starting a run creates the same branch name in every affected repository.
2. A branch-creation failure cleans up already-created branches.
3. Step execution payloads identify the target repository (single `repository_id` per execution, per [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md)).
4. Single-repo tasks keep the current behavior.
5. Runner containers can clone the intended repo and branch.

The step routing model is governed by [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md): every step targets exactly one repository, resolved as `step.repository` if set or `spine` otherwise. No fan-out in v0.x.

