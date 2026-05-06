---
id: TASK-005
type: Task
title: Add evidence query and reporting
status: Completed
acceptance: Approved
acceptance_rationale: |
  internal/evidence ships the production query surface for
  EPIC-006: loader (Git-tree YAML at canonical path
  /.spine/runs/{run_id}/evidence/{repository_id}.yaml),
  per-repo summary aggregator with deterministic ordering, and a
  Querier wrapper used by the gateway. /api/v1/runs/{run_id}
  response now carries an `evidence` block grouped by repository
  (AC #1, #2); raw logs are referenced via evidence_uri and never
  embedded (AC #3); missing evidence shows as present=false /
  reason="..." with run-level rollup status surfacing the gap
  (AC #4); 25+ unit and handler tests cover serialization,
  response shape, multi-ref fallback, planning-run skip, and
  every AC anchor (AC #5). Read-side branch is hardcoded `main`
  to match the engine merge target (engine/merge.go::authoritativeBranch);
  per-workspace customization waits on a future epic that updates
  both paths in lockstep. Codex 2 passes clean back-to-back after
  a 13-pass cascade typical for boundary surfaces with
  consistency-with-other-system-parts concerns.
last_updated: 2026-05-06
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-004-validation-service-evidence-rules.md
---

# TASK-005 - Add Evidence Query and Reporting

---

## Purpose

Make multi-repo evidence visible to humans, agents, and external interfaces.

## Deliverable

Add query/API support for evidence attached to a run or task.

Views should show:

- Repository-level evidence status.
- Required and optional policy checks.
- Commit SHAs.
- Failure summaries.
- Links to raw logs or external CI runs when available.

## Acceptance Criteria

- `run inspect` or equivalent API includes evidence summary.
- Query output is grouped by repository.
- Raw logs are linked or referenced, not embedded in large responses.
- Missing evidence is visible before publish.
- Tests cover evidence serialization and response shape.

