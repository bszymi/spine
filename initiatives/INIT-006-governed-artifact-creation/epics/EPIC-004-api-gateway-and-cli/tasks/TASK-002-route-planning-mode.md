---
id: TASK-002
type: Task
title: Route planning mode to StartPlanningRun
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-002 — Route Planning Mode to StartPlanningRun

---

## Purpose

Update `handleRunStart()` to route planning mode requests to the orchestrator's `StartPlanningRun()` method.

---

## Deliverable

`internal/gateway/handlers_workflow.go`

In `handleRunStart()`:
- If `mode == "planning"`: call `orchestrator.StartPlanningRun(ctx, taskPath, artifactContent)`
- If `mode == ""` or `mode == "standard"`: use existing `StartRun()` path (no changes)
- Include `mode` in the response JSON

---

## Acceptance Criteria

- Planning mode requests reach `StartPlanningRun()`
- Standard mode requests follow existing path unchanged
- Response includes `mode` field
- Handler does not parse the artifact content — delegates to engine
