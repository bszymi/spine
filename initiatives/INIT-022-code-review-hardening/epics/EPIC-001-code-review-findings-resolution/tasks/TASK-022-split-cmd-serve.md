---
id: TASK-022
type: Task
title: "Split cmd/spine/cmd_serve.go (1712 LOC)"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-016-cmd-spine-refactor/initiative.md
---

# TASK-022 — Split cmd/spine/cmd_serve.go (1712 LOC)

---

## Purpose

`cmd/spine/cmd_serve.go` is 1712 lines: `serveCmd()` is 211 lines,
`buildServerConfig()` is 209, `workspaceOrchestratorBuilder()` is 178.
The file is the canonical example of mixed concerns (CLI parsing,
config assembly, resolver wiring, lifecycle, orchestrator
construction).

This is a P3 maintainability finding from the 2026-05-07 code review.

## Deliverable

Split into focused files in the same package, no API change:

- `serve_cmd.go` — the `serveCmd()` cobra surface only.
- `serve_config.go` — `buildServerConfig` and config-derived helpers.
- `serve_wiring.go` — gateway / pool / scheduler / engine wiring.
- `serve_resolver.go` — resolver selection and platform-binding wiring.
- `serve_lifecycle.go` — graceful start/stop, signal handling.

The split is mechanical; preserve symbol visibility (exported vs
unexported) and import paths.

## Acceptance Criteria

- `go build ./cmd/spine/...` and the existing CLI tests pass.
- No new exported symbols.
- Each new file under ~400 lines.
- `git log --follow` still surfaces history for the moved hunks (use
  `git mv` semantics or rely on rename detection).

## Out of Scope

- Changing what wiring happens. This task is structural only.
- Touching INIT-016's deferred work.
