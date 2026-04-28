---
id: TASK-005
type: Task
title: Repository registry integration scenarios
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-004-repository-management-api-and-cli.md
---

# TASK-005 - Repository Registry Integration Scenarios

---

## Purpose

Validate repository lifecycle behavior at the integration boundary, complementing (not duplicating) unit-level coverage already required by TASK-002, TASK-003, and TASK-004.

## Deliverable

Add cross-component scenario tests that exercise the catalog + binding + API surface end to end.

Scenarios should cover:

- Register two code repos through the API/CLI, list them, then resolve each through the registry service.
- Catalog-only entry without a runtime binding produces an "unbound" status through every read surface.
- Deactivating a repository hides it from active resolution but preserves history through the API.
- A workspace with no `repositories.yaml` resolves the primary repo only (single-repo backward compatibility).
- Catalog and binding written by independent processes converge correctly through the service.

## Acceptance Criteria

- Scenario tests exercise the API/CLI/service/store stack together — pure unit tests stay in TASK-002/003/004.
- Single-repo backward-compatibility scenario exists and passes.
- Two-code-repo registration scenario passes against a real (sqlite-backed) store.
- Existing single-repo tests remain unchanged.
- This task does not introduce coverage already required by 002/003/004 — overlap should be removed, not added.
