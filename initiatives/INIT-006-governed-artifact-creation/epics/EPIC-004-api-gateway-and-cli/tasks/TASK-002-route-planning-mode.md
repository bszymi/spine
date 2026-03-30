---
id: TASK-002
type: Task
title: Route planning mode to StartPlanningRun
status: Completed
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

Wire an engine-facing interface into the gateway server and update `handleRunStart()` to route planning mode requests through it.

Currently `handleRunStart()` creates runs directly via `store.WithTx(...)` — there is no orchestrator or engine dependency on the gateway server. Planning runs require calling `StartPlanningRun()` on the engine, so the gateway must first gain an engine interface before routing can work.

---

## Deliverable

### 1. Add RunStarter interface to gateway

`internal/gateway/server.go`

Define a `PlanningRunStarter` interface that the gateway can call for planning runs only:

```go
type PlanningRunStarter interface {
    StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*engine.StartRunResult, error)
}
```

Add `planningRunStarter PlanningRunStarter` field to `ServerConfig` and thread it into the `Server` struct.

### 2. Route planning mode only — leave standard path unchanged

`internal/gateway/handlers_workflow.go`

In `handleRunStart()`:
- If `mode == "planning"`: call `s.planningRunStarter.StartPlanningRun(ctx, taskPath, artifactContent)`
- If `mode == ""` or `mode == "standard"`: use the existing inline `store.WithTx(...)` logic unchanged. Do NOT reroute standard runs through the engine — today's handler only persists a run and waiting step, while `engine.StartRun()` also creates branches, emits events, and activates steps. Changing the standard path would alter observable API behavior.
- Include `mode` in the response JSON

### 3. Wire orchestrator as PlanningRunStarter in server setup

`cmd/spine/main.go`

Pass the orchestrator (which satisfies `PlanningRunStarter`) to the gateway's `ServerConfig`.

---

## Acceptance Criteria

- Gateway server has a `PlanningRunStarter` interface field
- Planning mode requests reach `StartPlanningRun()` via the interface
- Standard mode requests use the existing inline `store.WithTx(...)` path — completely unchanged
- Response includes `mode` field
- Handler does not parse the artifact content — delegates to engine
