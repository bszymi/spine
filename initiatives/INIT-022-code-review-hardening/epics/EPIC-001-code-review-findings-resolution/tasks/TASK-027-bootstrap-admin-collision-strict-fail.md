---
id: TASK-027
type: Task
title: "Strict-startup error for bootstrap-admin hash collision"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-11
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-027 — Strict-startup error for bootstrap-admin hash collision

---

## Purpose

`internal/auth/bootstrap.go:97-114`: when a bearer hash collides with
an actor that is **not** `smp-admin`, bootstrap silently logs and
returns nil. The platform's bearer is then unable to authenticate, but
the warning is the only signal — under sampled logging, operators
won't notice.

This is a P3 hardening finding from the 2026-05-07 code review.

## Deliverable

- In `BootstrapInternalAdmin`, return an error on the
  non-`smp-admin` hash-collision branch.
- In `cmd/spine` (the workspace-load wireup), surface the error per
  the existing `SPINE_ENV=production` strict-startup philosophy:
  - In `production`: fail workspace load loudly.
  - Outside production: keep the warn-and-continue behavior for dev
    convenience, OR also fail — pick whichever matches the existing
    `BootstrapInternalSubscription` shape.

## Acceptance Criteria

- A new unit test seeds a non-`smp-admin` actor with a token whose
  hash matches `SMP_ADMIN_TOKEN`, calls `BootstrapInternalAdmin`,
  asserts the documented error class is returned.
- An end-to-end check (or a focused workspace-load test) confirms the
  workspace fails to come up under `SPINE_ENV=production`.

## Out of Scope

- Re-architecting bootstrap to support per-workspace bearers
  (separate epic-out-of-scope item per INIT-020/EPIC-002).

## Resolution (2026-05-11)

Three changes:

1. **`internal/auth/bootstrap.go`** — added exported sentinel
   `ErrAdminTokenHashCollision`. The non-`smp-admin` branch in
   `upsertInternalAdminToken` now returns
   `fmt.Errorf("bootstrap admin token hash already bound to actor %q; manual cleanup of colliding auth.tokens row required: %w", boundActor, ErrAdminTokenHashCollision)`
   instead of warn-and-return-nil. The error carries the colliding
   actor ID as operator-actionable context so on-call dashboards
   surface the row that needs manual cleanup without parsing
   structured fields.

2. **`cmd/spine/serve_config.go`** — added
   `workspaceDeliveryConfig.ProductionStrict bool`, populated from
   `resolveRuntimeEnv() == "production"` in
   `loadWorkspaceDeliveryConfig`. Mirrors the strict-startup policy
   already used for `SPINE_DEV_MODE`,
   `SPINE_CODE_REPO_BASE`, and `SPINE_SECRET_ENCRYPTION_KEY`.

3. **`cmd/spine/serve_resolver.go`** — `bootstrapInternalAdmin` now
   returns `error`. Policy:
   - Always logs the underlying error (preserves the existing
     structured-log breadcrumb).
   - In production strict mode + `errors.Is(err, auth.ErrAdminTokenHashCollision)`,
     returns the error wrapped with the workspace ID so the pool
     builder fails workspace load.
   - All other errors (and the collision off-production) are
     swallowed — matching `BootstrapInternalSubscription`'s shape so
     transient store blips don't knock a workspace out of rotation.
   `newPooledWorkspaceBuilder` propagates the error from
   `bootstrapInternalAdmin`, so workspace load surfaces the failure
   to the workspace.ServicePool log.

**Tests added**

- `internal/auth/bootstrap_test.go::TestBootstrapInternalAdmin_TokenHashCollision`
  — asserts the sentinel match, operator-actionable error message,
  and that the colliding token row is NOT silently rebound.
- `cmd/spine/serve_delivery_test.go::TestBootstrapInternalAdmin_StrictProductionFailsOnCollision`
  — production-mode wireup returns the sentinel error and names the
  workspace ID in the wrapper.
- `cmd/spine/serve_delivery_test.go::TestBootstrapInternalAdmin_NonProductionLogsCollision`
  — dev-mode wireup swallows the collision (logs only), confirming
  dev convenience is preserved.
- `cmd/spine/serve_delivery_test.go::TestLoadWorkspaceDeliveryConfig_ProductionStrictFromEnv`
  — table-driven over SPINE_ENV values (`production`, `PRODUCTION`,
  `staging`, `development`, ``) anchoring the env wiring contract.

**Regression-bait verification** (manual, pre-submission):

| Mutation | Result |
| --- | --- |
| Revert collision branch to `log.Warn(...); return nil` | FAIL — both `TestBootstrapInternalAdmin_TokenHashCollision` (`expected collision error, got nil`) and `TestBootstrapInternalAdmin_StrictProductionFailsOnCollision` (`expected collision to surface as error in production, got nil`) fail. |

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l internal/auth/ cmd/spine/` — clean on edited files (pre-existing baseline gofmt drift in `cmd/spine/cmd_artifact.go` unrelated).
- `go test ./... -count=1 -race` — green.
- `go test -tags=scenario -count=1 ./internal/scenariotest/scenarios/...` — green.
- `make docker-lint` — 206 baseline unchanged.
- `codex review` — clean: *"I did not find any P1/P2/P3 correctness issues in the TASK-027 changes."*
