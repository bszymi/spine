---
id: TASK-004
type: Task
title: "Planning-Run Integration for workflow.create and Merge-on-Approval"
status: Pending
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
  - type: blocked_by
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/tasks/TASK-002-seed-workflow-lifecycle.md
  - type: blocked_by
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/tasks/TASK-003-write-context-on-workflow-ops.md
---

# TASK-004 — Planning-Run Integration for workflow.create and Merge-on-Approval

---

## Context

With the seed workflow and `write_context` support in place, `workflow.create` called without a `run_id` must start a planning-mode Run bound to `workflow-lifecycle.yaml`, open a branch, and return the run + branch to the caller. The Run's approval outcome must trigger a merge back to the authoritative branch.

## Deliverable

- Introduce an artifact type / binding target `Workflow` so `workflow-lifecycle.yaml`'s `applies_to: [Workflow]` resolves correctly. Audit the binding resolver in `internal/workflow/binding.go` and the planning-run orchestrator in `internal/engine/` for places that enumerate artifact types.
- In `handleWorkflowCreate`:
  - When no `write_context` is supplied and caller role is reviewer (not operator), start a planning-mode Run under `workflow-lifecycle` via the planning-run orchestrator (reuse `PlanningRunStarter`).
  - Write the initial body on the run's branch using the TASK-003 write-context path.
  - Return `{ run_id, branch_name, workflow_id, workflow_path, trace_id }`.
- Hook the `approved` outcome in `workflow-lifecycle.yaml` so that submission of that outcome merges the run's branch into the authoritative branch. Reuse existing planning-run merge machinery where possible (the artifact planning runs already merge on completion — extend the same path to workflow runs).
- Ensure the `needs_rework` outcome loops back to `draft` and keeps the branch alive for further edits.
- End-to-end integration test:
  - `spine init-repo /tmp/fresh`
  - `workflow.create` → run + branch opened
  - `workflow.update` (with the returned `run_id`) → second commit on branch
  - `step.submit` with outcome `approved` → branch merged, workflow-lifecycle run `completed`, workflow is Active
- Existing Runs remain pinned to their prior workflow commit SHA; regression-test this.

## Acceptance Criteria

- `workflow.create` without `write_context` returns a new run_id and branch; authoritative branch is unchanged until approval.
- Approval outcome produces exactly one merge commit and flips the new workflow to Active.
- `needs_rework` keeps the branch and leaves the run in `draft`.
- Existing Runs do not rebase — assertion in integration test.
- Package coverage stays ≥80%.
