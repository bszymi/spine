---
id: TASK-011
type: Task
title: "Improve test coverage to above 80% for gateway, artifact, and workspace packages"
status: Completed
work_type: implementation
created: 2026-04-14
last_updated: 2026-04-16
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-011 — Improve test coverage to above 80% for gateway, artifact, and workspace packages

---

## Context

Current unit test coverage (`go test -cover ./...`) for three core packages falls below the 80% threshold:

| Package | Coverage |
|---|---|
| `internal/gateway` | 51.5% |
| `internal/artifact` | 68.0% |
| `internal/workspace` | 33.3% |

All other non-infrastructure packages already exceed 78%. Bringing these three above 80% closes the gap and ensures new handler/service code is accompanied by tests.

---

## Deliverable

Add unit tests to each package until `go test -cover` reports ≥ 80% for all three:

### `internal/gateway` (51.5% → ≥ 80%)

The handler layer has the widest gap. Priority areas:

- Handlers that have no test at all: `handleRunStart`, `handleRunStatus`, `handleRunCancel`, `handleListAssignments`, `handleActorCreate`, `handleExecutionCandidates`, `handleExecutionRelease`, `handleQueryRuns`, `handleQueryArtifacts`, `handleQueryGraph`, `handleQueryDiscussions`
- Error paths in existing handlers (missing body, invalid params, service unavailable)
- Authorization rejection paths (401, 403) shared across handlers

Use the existing `gateway_test.go` + `handlers_*_test.go` pattern: spin up `httptest.NewServer`, inject fakes via `ServerConfig`, assert status codes and response bodies.

### `internal/artifact` (68.0% → ≥ 80%)

- Uncovered paths in the artifact service (Create, Update, Delete error branches)
- Validation edge cases (malformed front matter, unknown artifact type)
- Reader/writer error propagation

### `internal/workspace` (33.3% → ≥ 80%)

- Workspace resolver logic
- Service pool lifecycle (acquire, release, timeout)
- DB provider connection handling error paths

---

## Acceptance Criteria

- `go test -cover ./internal/gateway/...` reports ≥ 80%
- `go test -cover ./internal/artifact/...` reports ≥ 80%
- `go test -cover ./internal/workspace/...` reports ≥ 80%
- All existing tests continue to pass
- No new mocks or test helpers that duplicate existing fakes
