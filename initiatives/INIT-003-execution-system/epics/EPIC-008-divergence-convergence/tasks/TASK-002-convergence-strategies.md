---
id: TASK-002
type: Task
title: Convergence Strategy Execution
status: In Progress
epic: /initiatives/INIT-003-execution-system/epics/EPIC-008-divergence-convergence/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-008-divergence-convergence/epic.md
---

# TASK-002 — Convergence Strategy Execution

## Purpose

Wire convergence evaluation into the engine orchestrator, including entry policy evaluation and actor-driven convergence evaluation steps.

## Deliverable

- Entry policy evaluation: detect when convergence can begin (all_branches_terminal, minimum_completed, deadline, manual_trigger)
- Convergence step activation: create an evaluation step for the convergence actor
- Strategy execution: apply the configured strategy (select_one, select_subset, merge, require_all)
- Result commitment: selected branch outcomes are committed, rejected outcomes are preserved
- Actor evaluation step replaces current auto-selection

## Acceptance Criteria

- Convergence triggers when entry policy is satisfied
- An actor evaluation step is created for convergence decisions
- All convergence strategies produce correct results
- Selected and rejected branch outcomes are both preserved
- Convergence results are committed to the run record
- The auto-selection behavior is replaced by actor-driven evaluation
