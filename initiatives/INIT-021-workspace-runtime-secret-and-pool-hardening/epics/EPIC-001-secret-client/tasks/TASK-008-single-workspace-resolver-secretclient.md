---
id: TASK-008
type: Task
title: Migrate single-workspace resolver to SecretClient
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/tasks/TASK-001-secret-client-interface.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/tasks/TASK-003-file-env-provider.md
  - type: related_to
    target: /architecture/adr/ADR-010-secret-client-abstraction.md
---

# TASK-008 — Migrate single-workspace resolver to SecretClient

---

## Purpose

Route the single-workspace `WorkspaceResolver` (the file/env
provider from INIT-009) through `SecretClient` so there is one
credential read path across all deployment modes.

Today `internal/workspace/file_provider.go` reads
`SPINE_DATABASE_URL` directly via `os.Getenv`. ADR-010 forbids
direct env-var reads of workspace credentials but exempts this
provider in its Consequences section. EPIC-001's acceptance
criterion ("zero direct env-var reads of workspace credentials
in CI grep") fails against that exemption. This task closes
the carve-out.

The change is structural, not operational: dev must keep working
with `SPINE_DATABASE_URL=postgres://…`. Backward compatibility is
delivered by a small **single-workspace bootstrap shim** owned by
this task — *not* by adding env-var awareness to the file
`SecretClient` provider, which stays filesystem-only per TASK-003.

## Deliverable

- `internal/workspace/Config.DatabaseURL` is a `secrets.SecretValue`.
  `String()` and `MarshalJSON` redact automatically;
  `redactDatabaseURL` in `internal/gateway/handlers_workspaces.go`
  is deleted.
- `internal/workspace/file_provider.go` calls
  `SecretClient.Get(secrets.WorkspaceRef(cfg.ID, "runtime_db"))`
  instead of `os.Getenv("SPINE_DATABASE_URL")`. Resolver still
  returns a single workspace; behaviour on miss is unchanged.
- **Single-workspace bootstrap shim**: a small
  `secrets.SecretClient` decorator that wraps the file
  provider and, for the specific ref
  `secret-store://workspaces/default/runtime_db`, falls back to
  `SPINE_DATABASE_URL` when the underlying provider returns
  `ErrSecretNotFound`. All other refs pass through untouched.
  Lives in `internal/workspace/` (not `internal/secrets/`) so
  the secrets package stays free of env-var coupling. Wired in
  only when `WORKSPACE_RESOLVER=file`.
  Precedence: file mount wins; env var is fallback.
- `internal/workspace/db_provider.go` keeps its `DatabaseURL`
  column as a stored ref string today; resolution dereferences
  via `SecretClient` at `Resolve` time. (Schema change for
  ref-only storage is out of scope; tracked in EPIC-002 for
  the platform-binding path.)
- `internal/workspace/pool.go` calls
  `cfg.DatabaseURL.Reveal()` only at the
  `store.NewPostgresStore` boundary.
- `cmd/spine/cmd_serve.go` and `cmd/spine/cmd_migrate.go` reads
  of `SPINE_DATABASE_URL` for **Spine's own DB** (control-plane,
  not a workspace credential) are out of scope and remain.
- ADR-010 is updated: the Consequences "Neutral" carve-out for
  the single-workspace resolver is removed; the rule "all Spine
  code that needs a per-workspace credential goes through
  `SecretClient`" applies without exception.
- EPIC-001's CI grep / lint runs across all of Spine (no
  whitelist for `internal/workspace/file_provider.go`).

## Acceptance Criteria

- A grep of the Spine codebase for direct env-var reads of
  per-workspace credentials returns zero hits, with no
  whitelisted files.
- Existing dev workflows (`SPINE_DATABASE_URL=postgres://…`)
  start Spine in single-workspace mode unchanged.
- `internal/workspace/file_provider_test.go` is rewritten to
  exercise the SecretClient path through the bootstrap shim;
  covers file-mount hit, env-var fallback, and "neither set"
  error paths.
- The bootstrap shim has its own table-driven test asserting
  that only `default/runtime_db` is decorated; all other refs
  pass through and any provider error other than
  `ErrSecretNotFound` is *not* swallowed.
- `internal/workspace/pool_test.go` and gateway handler tests
  pass against `SecretValue`-typed `DatabaseURL`.
- Logger redaction is verified for `Config.DatabaseURL` (no
  raw connection string appears in any structured log).
- `redactDatabaseURL` is removed; gateway handler relies on
  `SecretValue.String()` for display.
- ADR-010 amended; cross-link to this task added.
