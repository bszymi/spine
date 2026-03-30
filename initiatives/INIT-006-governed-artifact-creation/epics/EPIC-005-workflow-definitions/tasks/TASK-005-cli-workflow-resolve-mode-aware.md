---
id: TASK-005
type: Task
title: Make CLI workflow resolve mode-aware
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
---

# TASK-005 — Make CLI Workflow Resolve Mode-Aware

---

## Purpose

The `spine workflow resolve` CLI command filters workflows by status and applies_to but not by mode. With `artifact-creation.yaml` now Active, the command shows creation workflows alongside execution workflows, which is misleading.

---

## Deliverable

Update `internal/cli/workflow.go` to include the `mode` field in resolve output and optionally filter by mode when a `--mode` flag is provided.

---

## Acceptance Criteria

- `spine workflow resolve` shows the mode field in output
- Optional `--mode` flag filters results by execution/creation
- Default behavior shows all matching workflows with mode displayed
