---
id: TASK-002
type: Task
title: Define ADR-linked validation policy format
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-001-execution-evidence-schema.md
---

# TASK-002 - Define ADR-Linked Validation Policy Format

---

## Purpose

Make architectural decisions enforceable through explicit deterministic policies, rather than by interpreting ADR prose at runtime.

## Deliverable

Define a validation policy artifact format that ADRs can link to.

Policy fields should include:

- Policy ID and version
- Related ADR paths
- Applicable repository IDs or repository roles
- Required commands or external check names
- Required path patterns
- Required result shape
- Blocking severity

## Acceptance Criteria

- ADRs can reference policies through typed links.
- Policies are versioned in the primary repo.
- Policy execution is deterministic.
- AI-assisted interpretation is explicitly non-blocking unless converted into a deterministic policy.
- Documentation includes examples for API contract, migration, and lint checks.
- Format design is consistent with the governance update delivered in TASK-007 (artifact-schema registration). TASK-007 ships the schema; this task ships the format definition that schema describes.

