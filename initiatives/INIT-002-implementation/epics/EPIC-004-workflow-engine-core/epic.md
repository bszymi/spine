---
id: EPIC-004
type: Epic
title: Workflow Engine Core
status: Completed
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# EPIC-004 — Workflow Engine Core

---

## Purpose

Build the core Workflow Engine — the component that interprets workflow definitions and governs execution through state machines. After this epic, Spine can execute simple linear workflows (no divergence/convergence yet).

---

## Validates

- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Step-graph parsing and execution
- [Engine State Machine](/architecture/engine-state-machine.md) — Run and StepExecution state machines
- [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) — Workflow resolution
- [Error Handling](/architecture/error-handling-and-recovery.md) — Retry, timeout, failure handling

---

## Acceptance Criteria

- Workflow YAML files are parsed and validated
- Workflow binding resolves `(type, work_type)` to an active workflow
- Run state machine transitions are correct and fully tested
- Step execution state machine handles assign, submit, retry, timeout
- Scheduler detects timeouts and orphaned Runs
- Crash recovery resumes Runs from persisted state
- Git commit as durable boundary (committing state) works correctly
- Linear workflow (assign → execute → review → accept/reject) executes end-to-end
