---
id: TASK-001
type: Task
title: "Scenario Definition Format"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-003-scenario-engine/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-003-scenario-engine/epic.md
---

# TASK-001 — Scenario Definition Format

---

## Purpose

Define the structure and format for product scenarios. Scenarios must be composable, readable, and executable — capturing initial state, actions, and expected outcomes.

## Deliverable

Scenario definition types providing:

- Scenario struct with name, description, steps, and expected outcomes
- Step struct with action, inputs, and expected result
- Support for both inline Go test functions and data-driven table tests
- Composable step definitions for reuse across scenarios

## Acceptance Criteria

- Scenario format captures initial state, ordered steps, and expected outcomes
- Steps are individually identifiable for reporting purposes
- Format supports both positive and negative expected outcomes
- Scenarios can share and compose common step sequences
