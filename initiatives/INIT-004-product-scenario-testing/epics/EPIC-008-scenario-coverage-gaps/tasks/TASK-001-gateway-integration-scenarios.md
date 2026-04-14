---
id: TASK-001
type: Task
title: "Gateway integration scenario tests"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-001 — Gateway integration scenario tests

---

## Purpose

The gateway package has unit tests for individual handlers but nothing validates the full HTTP stack end-to-end: request → auth middleware → handler → service → JSON response. Cross-cutting concerns like error marshaling, content-type negotiation, and auth token propagation are not exercised in scenarios.

## Deliverable

Scenario tests that spin up a real `gateway.Server` (via `httptest.NewServer`) wired to the full service stack from `TestRuntime`, then drive it via HTTP client calls.

Scenarios to cover:

- **Golden path artifact create**: POST `/api/v1/artifacts/entry` → planning run starts → run ID returned
- **Auth rejection**: request without Bearer token → 401 before handler runs
- **Role enforcement at HTTP boundary**: Contributor token on Operator-only endpoint → 403 with correct error body
- **Error body shape**: service error is marshaled to `{"status":"error","errors":[{"code":"...","message":"..."}]}`
- **Run start → step query**: POST `/api/v1/runs` starts run, GET `/api/v1/execution/steps` returns the first waiting step
- **Result submission → status**: POST `/api/v1/execution/result` advances workflow, subsequent GET reflects updated status

Harness extension: add an HTTP client helper to `engine/` that wraps `http.Client` with a base URL and auth token, and provides typed `Do`, `Get`, `Post` helpers returning `(statusCode int, body []byte, err error)`.

## Acceptance Criteria

- All listed scenarios pass with real HTTP round-trips (not direct handler calls)
- Auth and role enforcement validated at the HTTP layer, not just in unit tests
- Error response shape asserted to match the documented API contract
- Helper is usable by future gateway scenarios without duplication
