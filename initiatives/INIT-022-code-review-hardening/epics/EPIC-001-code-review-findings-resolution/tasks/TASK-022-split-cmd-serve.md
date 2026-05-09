---
id: TASK-022
type: Task
title: "Split cmd/spine/cmd_serve.go (1712 LOC)"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-09
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

## Resolution (2026-05-09)

`cmd/spine/cmd_serve.go` had grown to **1827 LOC** by review time;
split into six focused files in the same `package main`:

| File | LOC | Contents |
| --- | --- | --- |
| `serve_cmd.go` | 195 | `serveCmd()` cobra surface (renamed from cmd_serve.go via `git mv`). |
| `serve_lifecycle.go` | 69 | `runServer()` — scheduler start, listen, signal handling, graceful shutdown. |
| `serve_config.go` | 365 | env/secret parsers (`loadSecretCipher`, `loadCodeRepoBase`, `parseGitHTTPTrustedCIDRs`, …), `workspaceDeliveryConfig`, `dbPolicyFromEnv`, `poolIdleTimeoutFromEnv`, `buildSecretClient` family. |
| `serve_wiring.go` | 425 | adapters (`runAdapter`/`resultAdapter`/`planningRunAdapter`/`workflowPlanningRunAdapter` + `planningRunResult`), `serveDeps`, `serveRuntime`, `buildServerConfig`. |
| `serve_builders.go` | 426 | `buildArtifactService`, `buildBranchProtectPolicy`, `buildEvidenceQuerier`, `buildWorkspaceCloneURLBuilder`, `buildGitPushResolver`, `buildWorkflowResolver`, `buildOrchestrator`, `buildScheduler`, `buildGitHTTPHandler`, `startEventDelivery`, `buildStore`. |
| `serve_resolver.go` | 452 | `resolverWiring`, `buildWorkspaceResolver` (file/db/platform-binding), `workspaceOrchestratorBuilder`, `newPooledWorkspaceBuilder`, `bootstrapInternalAdmin`, `wireWorkspaceDelivery`. |

**Six files, not five.** The task spec enumerated five files. A
straight five-way split would have put `serveDeps` + `serveRuntime` +
`buildServerConfig` + every `buildXxx` helper into a single
`serve_wiring.go` of ~795 LOC — roughly 2× the AC's ~400-line cap.
Splitting the small concrete `buildXxx` helpers into
`serve_builders.go` keeps every file within budget without otherwise
changing the structure described in the spec. `buildServerConfig`
stays in `serve_wiring.go` rather than `serve_config.go` (which the
spec suggested) for the same reason — folding 245 LOC of wiring
orchestration on top of the 365 LOC of env-parsing helpers would
push `serve_config.go` over 600 LOC.

**Mechanical only.** No symbol was added, removed, exported, or
unexported. No call site changed. The lifecycle helper `runServer`
preserves the original scheduler-recovery → projection-sync start →
listen → signal → delivery-cancel → scheduler-stop → shutdown order
verbatim.

**Rename history.** `cmd_serve.go` was `git mv`d to `serve_cmd.go`
so `git log --follow cmd/spine/serve_cmd.go` continues to surface
the file's history through the rename. The other five files are new;
because the old file shrank to ~10% of its size, default rename
detection (50% similarity) does not link them to the original.
Maintainers tracing moved hunks across the split should use
`git blame -C -C` or `git log --follow -M5%`.

**Test gates**

- `go build ./cmd/spine/...` — clean.
- `go build ./...` — clean.
- `go vet ./cmd/spine/...` — clean.
- `gofmt -l cmd/spine/serve_*.go` — clean.
- `go test ./cmd/spine/... -count=1 -race` — green
  (`TestServerStartupSmoke`, `serve_delivery_test`, etc. all pass).
- `go test -count=1 -race ./...` — green except the pre-existing
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake (TASK-026 territory, not a refactor regression).
- `go test -tags=scenario -count=1 ./internal/scenariotest/scenarios/...`
  — green.
- `make docker-lint` — 206 baseline unchanged. The single new-file
  finding (`gocritic emptyStringTest` on `serve_builders.go:114`'s
  `len(base) > 0`) is the same finding from the original
  `cmd_serve.go:1006` that travelled with the moved hunk; no net
  new lint findings.
- `codex review` — clean: "The split appears mechanical: the moved
  top-level declarations match the original implementation, and the
  extracted runServer preserves the prior scheduler, projection sync,
  listen, signal, delivery cancel, scheduler stop, and shutdown
  sequence. I did not find a regression introduced by the refactor."
