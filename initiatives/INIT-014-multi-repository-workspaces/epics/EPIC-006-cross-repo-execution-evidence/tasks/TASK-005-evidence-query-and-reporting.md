---
id: TASK-005
type: Task
title: Add evidence query and reporting
status: Pending
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

