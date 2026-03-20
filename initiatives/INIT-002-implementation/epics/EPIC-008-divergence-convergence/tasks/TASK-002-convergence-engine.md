---
id: TASK-002
type: Task
title: Convergence Engine
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-008-divergence-convergence/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-008-divergence-convergence/epic.md
---

# TASK-002 — Convergence Engine

## Purpose

Implement convergence execution: entry policy evaluation, convergence strategies, and result commitment.

## Deliverable

- Entry policy evaluation (all_branches_terminal, minimum_completed_branches, deadline_reached, manual_trigger)
- Convergence strategies: select_one, select_subset, merge, require_all, experiment
- Evaluation step execution (receives branch outcomes, produces convergence decision)
- Partial branch completion handling (per Divergence and Convergence §4.3)
- Convergence result commitment to Git (selected/merged artifacts)
- Non-selected branch preservation
- Convergence result recording in runtime store

## Acceptance Criteria

- Each entry policy correctly determines when convergence may begin
- Each strategy produces the correct outcome type
- Evaluation step receives all branch outcomes as input
- Partial completion is handled per strategy-specific rules
- Selected artifacts are merged to the task branch
- Non-selected branches are preserved in Git (never deleted)
- Unit tests for every policy × strategy combination
- Integration test: full divergence → convergence → Git merge flow
