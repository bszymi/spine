---
id: TASK-004
type: Task
title: Add evidence rules to validation service
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-003-check-runner-integration-boundary.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-007-validation-policy-governance-update.md
---

# TASK-004 - Add Evidence Rules to Validation Service

---

## Purpose

Block publication when required multi-repo evidence is missing or failed.

## Deliverable

Extend validation with evidence-aware rules.

Rules:

- Every affected code repository must produce evidence.
- Evidence must match the run branch and head commit.
- Required policy checks must be present.
- Blocking checks must pass.
- Stale evidence must fail validation.

## Acceptance Criteria

- Missing evidence blocks publish.
- Evidence from the wrong branch or commit blocks publish.
- Failed blocking policy checks block publish.
- Warning-only policy checks do not block publish.
- Validation output names repo ID, policy ID, and failing check.

