---
id: EPIC-008
type: Epic
title: Divergence and Convergence
status: Completed
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-007-actor-gateway/epic.md
---

# EPIC-008 — Divergence and Convergence

---

## Purpose

Extend the Workflow Engine with divergence (parallel branch execution) and convergence (branch evaluation and merging). After this epic, Spine supports the full execution model including controlled experimentation.

---

## Validates

- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Full execution semantics
- [Engine State Machine](/architecture/engine-state-machine.md) §4-5 — DivergenceContext and Branch state machines
- [Git Integration](/architecture/git-integration.md) §6.2 — Divergence branch strategy

---

## Acceptance Criteria

- Structured divergence creates parallel branches with isolated execution
- Exploratory divergence supports dynamic branch creation
- All 5 convergence strategies work (select_one, select_subset, merge, require_all, experiment)
- All 4 entry policies work (all_branches_terminal, minimum_completed_branches, deadline_reached, manual_trigger)
- Branch isolation is enforced (no cross-branch artifact access)
- Git branches are created and managed for each divergence branch
- Non-selected branches are preserved (never deleted)
- Convergence results are committed to Git
- DivergenceContext and Branch state machines pass full transition tests
- End-to-end test: workflow with divergence → parallel execution → convergence → result
