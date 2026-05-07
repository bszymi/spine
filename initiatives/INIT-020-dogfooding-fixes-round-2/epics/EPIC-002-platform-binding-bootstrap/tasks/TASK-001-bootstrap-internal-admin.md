---
id: TASK-001
type: Task
title: "BootstrapInternalAdmin: seed smp-admin actor + token on workspace load"
status: Completed
acceptance: Approved
acceptance_rationale: |
  Task shipped on main: workspace loads in platform-binding mode
  now seed the smp-admin actor row and bootstrap token
  deterministically on each load, closing the prior bootstrap gap
  where workspaces lacked the admin identity until manual
  provisioning. The acceptance field was omitted when the task
  closed; backfilled here as part of the post-INIT-014 status
  coherence sweep so the frontmatter conforms to the schema rule
  that Completed tasks carry an explicit acceptance field.
last_updated: 2026-05-07
epic: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-002-platform-binding-bootstrap/epic.md
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
work_type: feature
created: 2026-05-04
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-002-platform-binding-bootstrap/epic.md
---

# TASK-001 — BootstrapInternalAdmin: seed smp-admin actor + token on workspace load

---

## Purpose

In `WORKSPACE_RESOLVER=platform-binding` mode, Spine looks up inbound
bearer tokens against the workspace's *own* runtime DB
(`auth.tokens.token_hash`). A freshly-resolved workspace has no rows
in `auth.actors` / `auth.tokens`, so the platform's admin bearer
(SMP's `service_token_ref`) hits Spine's auth middleware, doesn't
match, and returns:

```
{"errors":[{"code":"unauthorized","message":"authentication required"}]}
```

Observed against an SMP-driven workspace on 2026-04-30. The only
unblocking workaround was to manually:

```sql
INSERT INTO auth.actors  (actor_id, actor_type, name, role, status)
VALUES ('smp-admin', 'automated_system', 'SMP Bootstrap Admin', 'admin', 'active');

INSERT INTO auth.tokens  (token_id, actor_id, token_hash, name)
VALUES ('bootstrap-…', 'smp-admin', '<hash of bearer>', 'smp-admin-bootstrap');
```

inside the workspace's runtime DB. That is exactly what
`BootstrapInternalSubscription` was built to avoid for the
subscription-row case — and the symmetric fix is a
`BootstrapInternalAdmin` that lives in the same place and runs on the
same trigger.

## Deliverable

- New `internal/auth/bootstrap.go` (or sibling location next to
  `internal/delivery/bootstrap.go`):

  ```go
  // BootstrapAdminConfig holds the env-derived config for the
  // platform's bootstrap admin actor.
  type BootstrapAdminConfig struct {
      Token string // SMP_ADMIN_TOKEN — bearer the platform presents.
                   // Empty disables the bootstrap (single-workspace
                   // / pre-platform-binding deployments).
  }

  // BootstrapInternalAdmin idempotently inserts the platform's
  // bootstrap admin actor + token row into the runtime DB so the
  // platform's service_token_ref bearer authenticates on the first
  // workspace request. Mirror of BootstrapInternalSubscription.
  func BootstrapInternalAdmin(ctx context.Context, st store.Store, cfg BootstrapAdminConfig) error
  ```

- Invocation from `cmd/spine/cmd_serve.go::wireWorkspaceDelivery`,
  alongside the existing `BootstrapInternalSubscription` block:

  ```go
  if cfg.SMPAdminToken != "" {
      if err := auth.BootstrapInternalAdmin(deliveryCtx, ss.Store, auth.BootstrapAdminConfig{
          Token: cfg.SMPAdminToken,
      }); err != nil {
          log.Error("workspace bootstrap internal admin failed", "workspace", ss.Config.ID, "error", err)
      }
  }
  ```

- Workspace delivery config (`workspaceDeliveryConfig` and the
  `loadWorkspaceDeliveryConfig` reader) extended with `SMPAdminToken
  string` sourced from `os.Getenv("SMP_ADMIN_TOKEN")`. Empty value is
  the no-op path; non-empty triggers the bootstrap.

- Idempotent INSERT semantics:
  - `auth.actors` row keyed on the literal `actor_id='smp-admin'`,
    `ON CONFLICT (actor_id) DO UPDATE SET actor_type, name, role,
    status, updated_at = now()` so a stale row (e.g. from manual
    seeding) is healed to the canonical shape.
  - `auth.tokens` row keyed on `token_hash`, deterministic `token_id`
    of the form `bootstrap-${HashToken(SMPAdminToken)[:12]}` so two
    different bearer rotations get distinct row IDs but the same
    bearer always lands on the same row. `ON CONFLICT (token_hash)
    DO UPDATE SET actor_id, name`. No `expires_at`.
  - On a token rotation (env var changed): on next workspace load the
    new hash inserts a new row; the old row remains until rotation
    cleanup is built (out of scope here — the live bearer is the only
    one anyone uses, the stale row is harmless until then).

- Schema use-only: the new function uses the existing
  `auth.HashToken` and the existing store interface; no new schema
  migrations.

- Logging at `INFO` for the create / update paths, `WARN` for
  unexpected store errors, `DEBUG` for the no-op (already-correct)
  path. Mirrors the verbosity of `BootstrapInternalSubscription`.

## Acceptance Criteria

- A workspace freshly resolved under `WORKSPACE_RESOLVER=platform-binding`
  with `SMP_ADMIN_TOKEN` set has both rows present in its runtime DB
  after the first request — no SMP-side DB write.
- A request to any workspace-scoped endpoint with bearer
  `SMP_ADMIN_TOKEN` succeeds (200, not 401) immediately after first
  resolve.
- Re-resolving the workspace (idle eviction → re-load) does not
  duplicate either row and emits the `INFO` "already configured" log
  line on the no-op path.
- A token rotation (env var changed, Spine restarted) causes the next
  workspace load to insert the new row; the old request bearer
  immediately starts failing with 401 (no in-process auth caching of
  the old hash beyond the existing pool TTL).
- With `SMP_ADMIN_TOKEN` unset, the bootstrap is a no-op with no
  warnings (single-workspace / pre-platform-binding deployments stay
  green).
- Unit test coverage matches `internal/delivery/bootstrap_test.go`'s
  shape: create path, update path (mismatched hash), no-op path,
  store-error path.
- Scenariotest path: `scenariotest/scenarios/bootstrap_internal_admin_test.go`
  drives a platform-binding workspace through resolve → first request
  → assert `auth.tokens` has the expected hash; re-resolve → assert
  no duplication.

## Out of Scope

- Per-workspace admin tokens via secret-store refs (epic-level out
  of scope; deferred until prod multi-workspace ships).
- Changes to the bearer-resolution middleware. It already matches
  `auth.tokens.token_hash` correctly; the gap is purely the absence
  of the row.
- Token rotation cleanup (stale `auth.tokens` rows post-rotation).
- Replacing the dev compose's `SPINE_AUTH_TOKEN` shared-bearer
  ergonomics. The new env var slots in next to it; SMP's compose
  pins both to the same value.

## Notes

- The SMP companion task is
  `smp:initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-platform-binding-followups/tasks/TASK-003`
  (rewritten 2026-05-04). It is purely the env-var pass-through plus
  docs; no code logic on the SMP side.
- The cross-repo ownership rule (SMP `architecture/runtime-binding-overview.md §3`
  / Spine `ADR-011`) names runtime-DB writes as Spine-owned. This
  task is the implementation of that ownership for the admin row,
  symmetric to what `BootstrapInternalSubscription` already does for
  the subscription row.
