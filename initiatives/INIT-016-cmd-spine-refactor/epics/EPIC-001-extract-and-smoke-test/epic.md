---
id: EPIC-001
type: Epic
title: "Extract cmd/spine and Add Startup Smoke Test"
status: Completed
initiative: /initiatives/INIT-016-cmd-spine-refactor/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-016-cmd-spine-refactor/initiative.md
---

# EPIC-001 — Extract cmd/spine and Add Startup Smoke Test

---

## Purpose

Shrink `cmd/spine/main.go` to root-command wiring only and add one smoke test that catches missing service wiring at startup — the class of bug that caused PR #415.

---

## Key Work Areas

- Extract `workflowCmd` and `validateCmd` from `main.go` into dedicated subcommand files.
- Extract the `serve` command block into its own file, splitting the `ServerConfig` assembly into named helpers callable from tests.
- Add a single smoke test that boots the server in dev mode and asserts every advertised endpoint returns anything other than 503 `service not configured`.

---

## Acceptance Criteria

- `main.go` contains only root-command wiring and shared helpers.
- Every subcommand lives in a dedicated `cmd_<feature>.go` file.
- `serve` wiring is split into helpers that can be invoked from tests.
- Startup smoke test exists and passes.
- No behavioral changes — `spine --help`, `spine serve`, and every existing subcommand work identically.
