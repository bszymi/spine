---
id: EPIC-008
type: Epic
title: Workspace Scenario Tests
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
---

# EPIC-008 — Workspace Scenario Tests

---

## Purpose

Validate workspace-aware runtime behavior through end-to-end scenario tests. The implementation epics (EPIC-003 through EPIC-007) added workspace routing, service pooling, provisioning, and multi-workspace background services. These need scenario-level tests that exercise the full stack with multiple workspaces.

---

## Key Work Areas

- Multi-workspace isolation tests: two workspaces operating concurrently cannot see each other's data
- Workspace provisioning tests: create workspace, provision DB and repo, verify usability
- Workspace lifecycle tests: create, use, deactivate, verify isolation after deactivation
- Batch migration tests: add migration, run --all-workspaces, verify all databases updated
- Single-mode backward compatibility: verify existing scenarios pass unchanged with FileProvider

---

## Primary Outputs

- New scenario test files in `internal/scenariotest/scenarios/`
- Test harness extensions for multi-workspace setup

---

## Acceptance Criteria

- Workspace isolation is verified: artifacts created in workspace A are not visible in workspace B
- Workspace provisioning creates a functional workspace (DB + repo + projection sync)
- Deactivated workspaces stop serving requests
- Batch migration applies to all workspace databases
- All existing scenario tests continue to pass (backward compatibility)
