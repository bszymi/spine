---
id: TASK-005
type: Task
title: "Nested and compound divergence scenarios"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-005 — Nested and compound divergence scenarios

---

## Purpose

Existing divergence scenarios exercise a single divergence window in isolation. No scenario tests a workflow where convergence produces output that feeds into a subsequent divergence, nor where one divergence window contains steps that themselves trigger another window. These compositions are expressible in workflow definitions but untested.

## Deliverable

Scenario tests covering:

- **Sequential divergence**: workflow diverges → branches complete → convergence selects winner → second divergence opens on the selected output → second convergence closes; verify final artifact reflects the full chain
- **Partial completion triggers minimum_completed_branches**: 3 branches open, entry policy is `minimum_completed_branches: 2`; after 2 complete the window closes without waiting for the third; the third branch's artifacts are not included
- **require_all with one failed branch**: one branch fails, others complete; verify `require_all` strategy does not converge and the run is blocked (or fails, per policy)
- **select_one with tied completion time**: two branches complete simultaneously; verify select_one picks exactly one deterministically and does not include artifacts from both

## Acceptance Criteria

- Sequential divergence: final state after two convergence cycles is correct
- minimum_completed_branches: window closes at threshold, third branch artifacts absent
- require_all + failure: run is not advanced; failure is surfaced
- select_one tie: exactly one branch selected; result is deterministic
