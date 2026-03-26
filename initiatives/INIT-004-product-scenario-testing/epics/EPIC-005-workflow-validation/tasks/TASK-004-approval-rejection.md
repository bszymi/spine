---
id: TASK-004
type: Task
title: "Approval and Rejection Scenarios"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-005-workflow-validation/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-005-workflow-validation/epic.md
---

# TASK-004 — Approval and Rejection Scenarios

---

## Purpose

Validate the approval and rejection flows within workflows, including successful approvals, rejections with follow-up, rejections closed, and the resulting state transitions and audit records.

## Deliverable

Scenario test suite covering:

- Approval flow: review step -> approved -> commit with correct final status
- Rejection with follow-up: review step -> rejected -> task returns to execution step
- Rejection closed: review step -> rejected closed -> task reaches terminal rejected state
- Audit trail records approval/rejection decisions with rationale
- Retry limits are enforced on repeated rejections

## Acceptance Criteria

- Approved tasks reach correct terminal state
- Rejected-with-follow-up tasks return to the correct re-execution step
- Rejected-closed tasks reach terminal rejected state
- Audit trail captures decision, actor, and rationale for each review outcome
- Retry limits trigger appropriate escalation or terminal state
