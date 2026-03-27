---
id: TASK-002
type: Task
title: "Projection Rebuild Scenarios"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-007-resilience-testing/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-007-resilience-testing/epic.md
---

# TASK-002 — Projection Rebuild Scenarios

---

## Purpose

Validate that database projections can be fully rebuilt from Git history. This proves that the database is a derived view, not the source of truth, and can be reconstructed at any time.

## Deliverable

Scenario test suite covering:

- Build state through normal operations (create artifacts, run workflows)
- Drop all projections (clear database)
- Rebuild projections from Git history
- Compare rebuilt projections against original state
- Validate that queries against rebuilt projections return correct results

## Acceptance Criteria

- Rebuilt projections match original state exactly
- All artifact data, workflow state, and relationships are reconstructed
- Queries against rebuilt projections return identical results
- Rebuild is idempotent — running it twice produces the same result
