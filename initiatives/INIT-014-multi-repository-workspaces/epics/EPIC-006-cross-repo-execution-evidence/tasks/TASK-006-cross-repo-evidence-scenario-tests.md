---
id: TASK-006
type: Task
title: Cross-repo evidence scenario tests
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-005-evidence-query-and-reporting.md
---

# TASK-006 - Cross-Repo Evidence Scenario Tests

---

## Purpose

Prove that governed intent can require and validate code-repo evidence end to end.

## Deliverable

Add scenario tests covering ADR-linked policy, check execution, evidence recording, validation, and publish blocking.

Scenarios:

- Multi-repo task with all checks passing.
- Missing evidence blocks publish.
- Failed blocking check blocks publish.
- Warning-only policy allows publish with warnings.
- Evidence tied to stale commit blocks publish.
- Evidence is visible in run inspection output.

## Acceptance Criteria

- Scenario tests use temporary primary and code repositories.
- Required policy checks are linked from governed artifacts.
- Publish proceeds only when required evidence is valid.
- Evidence remains auditable after the run completes.
