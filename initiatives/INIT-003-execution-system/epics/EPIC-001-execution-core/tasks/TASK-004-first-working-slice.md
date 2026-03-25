---
id: TASK-004
type: Task
title: First Working Slice — End-to-End Execution
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
  - type: blocked_by
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epics/EPIC-002-actor-delivery/tasks/TASK-001-queue-consumer.md
  - type: blocked_by
    target: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/tasks/TASK-001-workflow-directory.md
---

# TASK-004 — First Working Slice — End-to-End Execution

## Purpose

Validate the complete execution loop by running a single task through a single workflow with a mock actor. This is the Phase 0 deliverable — without it, further development is speculative.

## Deliverable

An integration test (and optionally a manual demo) that:

1. Creates a task artifact
2. Resolves a workflow for that task
3. Starts a run
4. Activates the first step
5. Assigns to a mock actor
6. Mock actor returns a result
7. Result is processed, outcome evaluated
8. Run completes

## Acceptance Criteria

- A task executes end-to-end without manual intervention
- All components interact correctly (orchestrator, actor gateway, workflow engine, artifact service, store)
- The integration test is repeatable and runs in CI
- No divergence, convergence, Git persistence, or advanced validation required
- This test becomes a regression guard for future changes
