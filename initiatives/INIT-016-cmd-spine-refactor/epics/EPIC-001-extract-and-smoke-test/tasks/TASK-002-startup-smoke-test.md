---
id: TASK-002
type: Task
title: "Serve-Startup Smoke Test for Advertised Endpoints"
status: Completed
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-016-cmd-spine-refactor/epics/EPIC-001-extract-and-smoke-test/epic.md
initiative: /initiatives/INIT-016-cmd-spine-refactor/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-016-cmd-spine-refactor/epics/EPIC-001-extract-and-smoke-test/epic.md
  - type: blocked_by
    target: /initiatives/INIT-016-cmd-spine-refactor/epics/EPIC-001-extract-and-smoke-test/tasks/TASK-001-extract-subcommands-and-serve.md
---

# TASK-002 — Serve-Startup Smoke Test for Advertised Endpoints

---

## Context

PR #415 was a class of bug where a new service (`workflow.Service`) was added to `gateway.ServerConfig` but never constructed in `serve`, so every `/workflows/*` endpoint returned 503 `service not configured`. No test caught it. This task closes that gap with a minimal smoke test that boots the server in dev mode and probes every advertised endpoint for a non-503 response.

## Deliverable

- Add a test under `cmd/spine/` (or a new `internal/gateway/smoke_test.go`) that:
  - Calls the `buildServerConfig` helper from TASK-001 with a minimal in-memory store and a temp Git repo.
  - Starts the resulting server via `httptest.NewServer`.
  - For every endpoint enumerated in `/api/v1/spec.yaml` (or the route table in `routes.go`), issues a minimal authenticated request.
  - Asserts the response is **not** `503 service unavailable` with body containing `service not configured`.
  - 4xx/404/405 responses are fine — we're only checking wiring, not business logic.
- Consider running the server in `DevMode` so the smoke test doesn't need to fabricate tokens.
- Add a short comment pointing to this test from `handlers_workflows.go` near the service-nil branch, so the next person adding a service knows where to update coverage.

## Acceptance Criteria

- Smoke test enumerates every advertised endpoint — no hand-maintained allowlist that goes stale.
- Running the test against a `ServerConfig` with a deliberately nil `Workflows` field fails (proving it catches the PR #415 regression).
- Test adds < 2 s to the CI test suite.
- Full `go test ./...` passes.
