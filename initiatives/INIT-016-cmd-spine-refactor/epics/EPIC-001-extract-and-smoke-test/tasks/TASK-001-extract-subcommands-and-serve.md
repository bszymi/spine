---
id: TASK-001
type: Task
title: "Extract Subcommands and Serve Wiring from main.go"
status: Pending
work_type: refactor
created: 2026-04-17
epic: /initiatives/INIT-016-cmd-spine-refactor/epics/EPIC-001-extract-and-smoke-test/epic.md
initiative: /initiatives/INIT-016-cmd-spine-refactor/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-016-cmd-spine-refactor/epics/EPIC-001-extract-and-smoke-test/epic.md
---

# TASK-001 — Extract Subcommands and Serve Wiring from main.go

---

## Context

`cmd/spine/main.go` contains root-command wiring, the entire `serve` command (~400 lines of service assembly), and the `workflowCmd` / `validateCmd` definitions. Other subcommands already live in dedicated `cmd_<feature>.go` files. This task brings the outliers into line and splits `serve` so its wiring can be called from tests.

## Deliverable

- Move `workflowCmd()` into new `cmd/spine/cmd_workflow.go`.
- Move `validateCmd()` into new `cmd/spine/cmd_validate.go`.
- Move the `serve` command definition into new `cmd/spine/cmd_serve.go`. Split the assembly into small, named helpers — e.g. `buildServerConfig`, `buildOrchestrator`, `buildGitHTTPHandler` — so the smoke test in TASK-002 can invoke them without reimplementing wiring.
- Trim `main.go` so it holds only root-command setup, shared helpers (`newAPIClient`, `printResponse`, `normalizePath`), and command registration.
- Keep all exported behavior identical. `spine --help`, `spine serve`, and every existing subcommand must behave as before.

## Acceptance Criteria

- `main.go` ≤ 200 lines.
- Every subcommand lives in a `cmd_<feature>.go` file.
- `serve` command body is broken into named helpers callable from tests.
- Full `go test ./...` passes; `go vet ./...` clean.
- `spine --help` output identical to before.
