---
id: TASK-005
type: Task
title: Scenario tests for configurable artifacts directory
status: Completed
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
  - type: blocked_by
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/tasks/TASK-002-path-resolution.md
---

# TASK-005 — Scenario Tests for Configurable Artifacts Directory

---

## Purpose

Validate that Spine works correctly with artifacts in a subdirectory and at the repo root.

---

## Deliverable

`internal/scenariotest/scenarios/artifacts_dir_test.go`

Scenarios:

1. **Root directory (backward compat)**: `artifacts_dir: /` — init-repo, create artifacts, sync projections, start run — all work as before
2. **Subdirectory**: `artifacts_dir: spine/` — init-repo creates `spine/` with all seed artifacts, artifact CRUD works, projection discovers artifacts in subdirectory, workflow loader finds workflows in subdirectory
3. **Planning run in subdirectory**: Start planning run with `artifacts_dir: spine/`, create artifacts via write_context, approve, merge — artifacts land in `<repo>/spine/` on main
4. **Link resolution**: Artifact link `/governance/charter.md` resolves to `<repo>/spine/governance/charter.md` when `artifacts_dir: spine/`
5. **Missing .spine.yaml**: No config file — defaults to `artifacts_dir: /`, everything works

---

## Acceptance Criteria

- All 5 scenarios pass
- Tests create temporary repos with `.spine.yaml` configured appropriately
- Backward compatibility confirmed (no .spine.yaml = root behavior)
