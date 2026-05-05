---
id: TASK-006
type: Task
title: Multi-repo run lifecycle tests
status: Completed
acceptance: Approved
acceptance_rationale: |
  Coverage map (TASK-006 deliverable list) is satisfied across the
  unit + scenario layers. Unit: multi_repo_branch_test.go covers
  primary-only / single-code / multi-code branch creation and 6
  partial-failure rollback variants; step_routing_test.go covers
  ADR-015 explicit / default-spine / opt-in / unknown / inactive /
  bad-format / multi-code-no-decl resolution; assignment_payload_test.go
  covers workspace_id + commit_baseline omitempty + non-execution-step
  tolerance. Scenario: runner_clone_context_test.go drives a real
  git clone from an AssignmentContext; new
  multi_repo_run_lifecycle_test.go consolidates the engine -> store
  -> real git CLI -> on-disk refs path against two real on-disk code
  repos and asserts AffectedRepositories + RepositoryBaselines
  Postgres roundtrip + entry step persisted RepositoryID + cleanup
  symmetry. Codex 5 passes; passes 4 + 5 clean back-to-back. Two P3
  test-coverage-accuracy findings (in-memory vs persisted, git error
  discrimination) fixed in passes 2 / 4. Closes EPIC-004.
last_updated: 2026-05-05
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-005-runner-clone-context.md
---

# TASK-006 - Multi-Repo Run Lifecycle Tests

---

## Purpose

Validate run startup and assignment behavior for tasks that span multiple repositories.

## Deliverable

Add unit and scenario tests for the multi-repo run lifecycle.

Coverage:

- Primary-only task.
- Single code repo task.
- Multiple code repo task.
- Branch creation cleanup on failure.
- Explicit step routing.
- Ambiguous step routing.
- Runner clone context.

## Acceptance Criteria

- Tests prove branch creation happens in every affected repo.
- Failure scenarios leave no orphaned startup state.
- Assignment payloads are stable and documented by tests.
- Existing run lifecycle tests remain valid.
