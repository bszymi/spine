---
id: EPIC-001
type: Epic
title: Execution Core
status: Completed
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-001 — Execution Core

---

## Purpose

Implement the minimal runtime orchestrator that drives workflow execution end-to-end. This is the central component that INIT-002 did not deliver — the engine that wires run lifecycle, step progression, precondition evaluation, and outcome routing into a coherent execution loop.

After this epic, the system can create a run, activate steps, evaluate preconditions, route outcomes, and track step state — all in memory without Git persistence.

---

## Key Work Areas

- Engine orchestrator struct and interfaces
- Run lifecycle management (create, activate, progress, complete/fail)
- Step progression logic (preconditions, assignment requests, outcome evaluation, next step determination)
- Integration with existing workflow state machines
- First working slice — minimal end-to-end execution with actor and workflow

---

## Primary Outputs

- `internal/engine/orchestrator.go` — Core orchestrator
- `internal/engine/run.go` — Run lifecycle management
- `internal/engine/step.go` — Step progression engine
- Integration tests proving end-to-end execution

---

## Acceptance Criteria

- Engine orchestrator can create a Run from a Task and resolved Workflow
- Steps activate in sequence based on workflow definition
- Preconditions are evaluated before step activation
- Step outcomes determine next step routing
- Run completes when terminal step is reached
- Run fails when a step fails permanently
- First working slice: a single task executes end-to-end (with mock actor)
