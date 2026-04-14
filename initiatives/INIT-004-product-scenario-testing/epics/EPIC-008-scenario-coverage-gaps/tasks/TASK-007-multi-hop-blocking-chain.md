---
id: TASK-007
type: Task
title: "Multi-hop blocking chain scenarios"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-007 — Multi-hop blocking chain scenarios

---

## Purpose

Existing blocking scenarios only test a single `blocked_by` link (A blocks B). No scenario tests a chain (A blocks B blocks C), nor verifies that completing A transitively unblocks both B and C, nor tests the case where a blocker is resolved via automated step completion rather than manual action.

## Deliverable

Scenario tests covering:

- **Two-hop chain**: Task C is `blocked_by` Task B, Task B is `blocked_by` Task A; attempt to start C fails; complete A; attempt to start C still fails (B still incomplete); complete B; start C succeeds
- **Automated blocker resolution**: Task A is `blocked_by` Task B; Task B is completed via an automated step result submission (no human claim); verify A becomes startable after B completes
- **Multiple blockers**: Task C is `blocked_by` both Task A and Task B (two separate links); C cannot start until both A and B are complete; completing only one is insufficient
- **Blocker cancelled instead of completed**: Task A is `blocked_by` Task B; Task B is cancelled; verify A is still blocked (cancelled ≠ completed) and start is rejected

## Acceptance Criteria

- Two-hop chain: C only becomes startable after both A and B are individually completed
- Automated resolution: blocker completed by automated actor unblocks downstream task
- Multiple blockers: all blockers must be complete before downstream task can start
- Cancelled blocker: A remains blocked when B is cancelled, not completed
