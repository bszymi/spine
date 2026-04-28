---
id: TASK-006
type: Task
title: Single-repo backward-compatibility regression scenario
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-005-repository-registry-tests.md
---

# TASK-006 - Single-Repo Backward-Compatibility Regression Scenario

---

## Purpose

Lock in initiative success criterion #6 ("existing single-repo workspaces are fully backward compatible — no migration required") with an end-to-end scenario test that fails loudly if any future change reintroduces a multi-repo assumption.

## Deliverable

Add a top-level scenario test that:

- Boots a workspace from an INIT-008-era fixture with no `/.spine/repositories.yaml` and no runtime bindings table populated.
- Creates a task, runs it, merges it, and verifies the run completes through the same code path that single-repo workspaces use today.
- Exercises the git HTTP endpoint without a `repo_id` in the URL.
- Asserts no implicit catalog or binding rows are written.

The scenario should be structured so it is run as part of regular CI for every epic in INIT-014, not just EPIC-001.

## Acceptance Criteria

- Scenario passes against the current implementation before EPIC-001 lands and stays green through EPIC-002 through EPIC-007.
- Failing this scenario in any future PR signals a backward-compatibility regression and blocks merge.
- Scenario uses a fixture that mirrors a pre-INIT-014 workspace exactly (no schema migrations applied beyond what existed at INIT-008 close).
- No existing single-repo tests need behavior changes.
