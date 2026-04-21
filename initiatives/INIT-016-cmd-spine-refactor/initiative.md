---
id: INIT-016
type: Initiative
title: cmd/spine Refactor and Startup Smoke Test
status: Completed
owner: bszymi
created: 2026-04-17
---

# INIT-016 — cmd/spine Refactor and Startup Smoke Test

---

## Purpose

`cmd/spine/main.go` has grown to carry subcommand definitions (`workflowCmd`, `validateCmd`) and a ~400-line `serve` block in the same file as the root-command wiring. Other subcommands already live in dedicated files (`cmd_artifact.go`, `cmd_discussion.go`, `cmd_run.go`, `cmd_task.go`, `cmd_workspace.go`), so `main.go` is the outlier.

Separately, the INIT-015 follow-up PR (#415) showed that a missing `workflow.NewService` wire in the `serve` command produced 503 responses from every `/workflows/*` endpoint, and no test caught it. A minimal startup smoke test that asserts every advertised endpoint resolves past 503 would have.

## Motivation

- **Readability**: `main.go` mixes root setup, command definitions, and a long wiring block. Extracting by subcommand matches the existing `cmd_<feature>.go` pattern and shrinks the top-level file to what it implies.
- **Regression safety**: the workflow-service-wiring regression (fixed in PR #415) was a class of bug — any new service added to `ServerConfig` will silently 503 if the wiring is forgotten. A startup smoke test closes that gap at low cost.
- **Extraction itself**: the big `serve` function is hard to review and hard to reuse for testing; splitting into named helpers (`buildServerConfig`, `buildOrchestrator`, etc.) makes the smoke test straightforward to write.

## Scope

### In Scope

- **EPIC-001 — Extract cmd/spine and Add Startup Smoke Test**:
  - TASK-001: extract subcommand definitions and the `serve` wiring into dedicated files.
  - TASK-002: add a serve-startup smoke test that asserts every advertised endpoint responds with anything other than 503 for an authorized request.

### Out of Scope

- Changing CLI command surface (flag names, args, outputs).
- Changing HTTP endpoint behavior.
- Refactoring `internal/cli` (already tested separately).
- Adding unit tests for the thin subcommand wrappers (they delegate to `internal/cli` which is already covered).

## Success Criteria

1. `cmd/spine/main.go` contains only root-command wiring and shared helpers; all subcommands live in dedicated files.
2. The `serve` command's `ServerConfig` assembly is split into named helpers that can be invoked from a test.
3. A smoke test boots the server in dev mode and asserts every advertised endpoint returns something other than 503 `service not configured`.
4. No behavior changes — full test suite still passes.

## Primary Artifacts Produced

- New `cmd/spine/cmd_workflow.go`, `cmd/spine/cmd_validate.go`, `cmd/spine/cmd_serve.go` (and possibly `cmd_serve_config.go` for helpers).
- Trimmed `cmd/spine/main.go`.
- New startup smoke test under `cmd/spine/`.

## Exit Criteria

INIT-016 may be marked complete when all EPIC-001 tasks are Completed and CI is green.
