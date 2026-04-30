---
id: TASK-002
type: Task
title: "Workspace-scoped step lifecycle handlers in platform-binding mode"
status: Pending
epic: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
work_type: bugfix
created: 2026-04-30
last_updated: 2026-04-30
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/tasks/TASK-005-wire-result-handler-in-gateway.md
  - type: related_to
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
---

# TASK-002 — Workspace-scoped step lifecycle handlers in platform-binding mode

---

## Purpose

When `WORKSPACE_RESOLVER=platform-binding`, the top-level `gateway.ServerConfig` orchestrator is `nil` (no global `deps.Store`), so every gateway handler that reads a top-level orch-derived field falls through to the unavailable / nil-deref path. Per-workspace orch adapters are constructed in `workspaceOrchestratorBuilder` and stored on `workspace.ServiceSet`, but most lifecycle handlers don't look them up via the existing `*From(ctx)` resolver pattern.

Symptoms observed dogfooding against SMP on 2026-04-30 (run `run-088bfa3e` against
`task-default.yaml`):

- `POST /api/v1/steps/{id}/submit` → `503 result handler not configured` (already covered by INIT-003 EPIC-002 TASK-005, but only the top-level adapter was wired; per-workspace path was never added).
- `POST /api/v1/steps/{id}/acknowledge` → `500 internal_error` from a panic in `recoveryMiddleware`. Stack: `engine.(*Orchestrator).AcknowledgeStep(0x0, …)` — receiver is nil because `s.stepAcknowledger` was never set on the per-workspace `ServiceSet`.
- Same risk for any other top-level orchestrator field referenced by lifecycle handlers — `s.stepAssigner` (already on `ServiceSet`, may not be looked up consistently), `s.runMergeResolver`, `s.candidateFinder`, `s.stepClaimer`, etc.

The pattern is: top-level `s.XHandler` is fine in single-workspace mode, but in platform-binding mode every handler must call `s.xHandlerFrom(ctx)` and resolve through `workspace.ServiceSet`.

A partial fix for `ResultHandler` landed as commit `8af7b0d` ("wire result handler through per-workspace service set") on 2026-04-30, touching:

- `internal/workspace/pool.go`: `ResultHandler any` on `ServiceSet`.
- `cmd/spine/cmd_serve.go`: `ss.ResultHandler = &resultAdapter{orch: orch}` in `workspaceOrchestratorBuilder`.
- `internal/gateway/server.go`: `resultHandlerFrom(ctx)` resolver method.
- `internal/gateway/handlers_workflow.go`: `handleStepSubmit` uses the resolver.

This task extends the same pattern to every other lifecycle handler that suffers the same nil-receiver bug.

## Deliverable

For each top-level orchestrator-derived field on `gateway.Server` that lifecycle handlers read directly:

1. Add a corresponding `any`-typed slot on `workspace.ServiceSet` (matching the existing `RunStarter`, `RunCanceller`, `StepAssigner` precedent).
2. Populate it inside `workspaceOrchestratorBuilder` in `cmd/spine/cmd_serve.go`.
3. Add a `*From(ctx)` resolver on `gateway.Server`, paralleling `runStarterFrom` and `resultHandlerFrom`.
4. Update the handler to call the resolver and return `503 unavailable` (not panic) when the resolver returns nil.

At minimum, audit and fix:

- `handleStepAcknowledge` (`stepAcknowledger`) — already known to panic.
- `handleStepSubmit` (`resultHandler`) — already partially wired; finish if not landed yet.
- `handleStepAssign` (`stepAssigner`) — verify per-workspace path is consistent.
- `handleStepClaim` / `candidateFinder` — verify.
- `handleRunMergeResolve` / `runMergeResolver` — verify.

## Acceptance Criteria

- `POST /api/v1/steps/{id}/acknowledge` no longer panics under `WORKSPACE_RESOLVER=platform-binding`; returns the expected 200 (or 4xx with a typed error) when the workspace context resolves.
- A run started against an SMP-style workspace (platform-binding mode, per-workspace runtime DB) can complete the full execute → validate → verify → review → publish lifecycle without any handler returning `503 X handler not configured` or 500-from-panic, given the runner has Spine credentials and a webhook subscription is wired.
- `recoveryMiddleware` log shows zero `panic recovered` entries during the lifecycle smoke run.
- Audit comment in `cmd/spine/cmd_serve.go` documents the contract: every gateway field the orchestrator owns must also live on `ServiceSet` and have a resolver.

## Out of Scope

- Webhook event delivery (TASK-003).
- Step auto-assignment with empty actor_id (TASK-004).
- Single-workspace mode regressions — the existing top-level field assignment must keep working as a fallback when `ServiceSet` lookup returns nil (test coverage to assert that).
