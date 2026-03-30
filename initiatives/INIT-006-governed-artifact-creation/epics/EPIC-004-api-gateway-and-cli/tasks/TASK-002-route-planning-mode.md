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

Wire an engine-facing interface into the gateway server and update `handleRunStart()` to route planning mode requests through it.

Currently `handleRunStart()` creates runs directly via `store.WithTx(...)` — there is no orchestrator or engine dependency on the gateway server. Planning runs require calling `StartPlanningRun()` on the engine, so the gateway must first gain an engine interface before routing can work.

---

## Deliverable

### 1. Add RunStarter interface to gateway

`internal/gateway/server.go`

Define a `RunStarter` interface that the gateway can call:

```go
type RunStarter interface {
    StartRun(ctx context.Context, artifactPath string) (*engine.StartRunResult, error)
    StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*engine.StartRunResult, error)
}
```

Add `runStarter RunStarter` field to `ServerConfig` and thread it into the `Server` struct.

### 2. Route planning mode through RunStarter

`internal/gateway/handlers_workflow.go`

In `handleRunStart()`:
- If `mode == "planning"`: call `s.runStarter.StartPlanningRun(ctx, taskPath, artifactContent)`
- If `mode == ""` or `mode == "standard"`: call `s.runStarter.StartRun(ctx, taskPath)` — this replaces the inline `store.WithTx` transaction logic with the engine interface. The observable behavior (run created, step activated) must remain identical; only the internal code path changes.
- Include `mode` in the response JSON

### 3. Wire orchestrator as RunStarter in server setup

`cmd/spine/main.go`

Pass the orchestrator (which satisfies `RunStarter`) to the gateway's `ServerConfig`.

---

## Acceptance Criteria

- Gateway server has a `RunStarter` interface field
- Planning mode requests reach `StartPlanningRun()` via the interface
- Standard mode requests are routed through `StartRun()` via the same interface (replacing inline transaction logic)
- Response includes `mode` field
- Handler does not parse the artifact content — delegates to engine
- Existing standard run behavior is preserved (same result, different code path)
