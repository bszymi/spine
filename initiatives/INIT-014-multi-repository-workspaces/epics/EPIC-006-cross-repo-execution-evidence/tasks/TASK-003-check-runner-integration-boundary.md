---
id: TASK-003
type: Task
title: Implement check runner integration boundary
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-002-adr-linked-validation-policy-format.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-005-runner-clone-context.md
---

# TASK-003 - Implement Check Runner Integration Boundary

---

## Purpose

Provide a narrow interface for executing or collecting validation checks from code repositories.

## Deliverable

Add a check runner boundary that can support local commands first and external CI integrations later.

Interface responsibilities:

- Receive repository ID, branch, commit, and policy requirements.
- Execute or request checks.
- Return structured results.
- Preserve raw logs as references, not inline evidence.
- Classify check failures.

## Acceptance Criteria

- Local command checks can run against a cloned repository branch.
- Check results are normalized into the evidence schema.
- Timeouts and execution failures are classified.
- The interface does not assume a specific CI provider.
- Unit tests cover pass, fail, timeout, and unavailable states.

