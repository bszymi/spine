---
id: EPIC-002
type: Epic
title: Platform-Binding Bootstrap Completeness
status: Completed
acceptance: Approved
acceptance_rationale: |
  TASK-001 (BootstrapInternalAdmin: seed smp-admin actor + token on
  workspace load) shipped and is Completed/Approved. The epic's
  scope was that single bootstrap-completeness task; with it landed,
  workspace loads in platform-binding mode now seed the smp-admin
  actor + token deterministically, removing the prior bootstrap
  gap. The acceptance field was missing from frontmatter when the
  task closed; backfilled here as part of the post-INIT-014 status
  coherence sweep so the epic's frontmatter conforms to the
  artifact-schema rule that Completed epics carry an explicit
  acceptance field.
last_updated: 2026-05-07
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
owner: bszymi
created: 2026-05-04
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
  - type: related_to
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md
---

# EPIC-002 — Platform-Binding Bootstrap Completeness

---

## 1. Purpose

`WORKSPACE_RESOLVER=platform-binding` (Spine `INIT-021`) introduces
per-workspace runtime databases that Spine connects to lazily on first
workspace touch. Spine handles the *runtime* of that connection well
— pool, cache, invalidation, migrations — but the *bootstrap* of
out-of-band rows that have to exist in the runtime DB before downstream
flows can succeed is **only half-built**.

There are two such bootstrap rows. One pair is for the platform-side
subscriber that consumes step events; the other is for the platform-side
caller that authenticates as admin to forward customer requests:

| Bootstrap row | Mechanism today | Driven by env |
|---|---|---|
| `event_subscriptions` row named `smp-internal` | `internal/delivery/bootstrap.go::BootstrapInternalSubscription`, runs on every workspace load from `wireWorkspaceDelivery` | `SMP_EVENT_URL`, `SMP_INTERNAL_TOKEN` |
| `auth.actors` + `auth.tokens` row for the platform's bootstrap admin | **missing** — no Spine mechanism creates it; the platform either reaches into Spine's runtime DB directly (violating ADR-011 / SMP ADR-012) or operators hand-roll INSERTs | (no env var yet) |

The missing half was discovered during SMP dogfooding on 2026-04-30:
SMP forwards a request to Spine using the workspace's
`service_token_ref` as bearer; Spine's auth middleware looks the bearer
up against the workspace runtime DB; finds no matching row; returns
401. The workaround — manual `INSERT INTO auth.actors / auth.tokens`
in the workspace runtime DB after every fresh provision — is not viable
beyond a single-workspace dev stack.

The *symmetric* fix is a `BootstrapInternalAdmin` mechanism that mirrors
`BootstrapInternalSubscription` shape-for-shape: runs from
`wireWorkspaceDelivery` on every workspace load, idempotently writes
the admin actor + token rows from an env-provided token, no-ops cleanly
when the env var is unset (preserving single-workspace and
pre-platform-binding deployments).

## 2. Scope

### In Scope

- `BootstrapInternalAdmin` function and integration into the existing
  workspace-load bootstrap path (alongside `BootstrapInternalSubscription`).
- Schema-level idempotency for the two new rows (`actor_id='smp-admin'`,
  deterministic `token_id`).
- Observability: log the create / update / no-op paths so operators
  can audit a fresh stack's bootstrap from logs alone.
- Test coverage at the same depth as `BootstrapInternalSubscription`
  (unit + scenariotest if one exists for the subscription path).

### Out of Scope

- Per-workspace admin bootstrap tokens. The current `SMP_ADMIN_TOKEN`
  env-var model uses a single shared bearer per Spine instance
  (matching how `SMP_INTERNAL_TOKEN` works for the subscription).
  Per-workspace tokens via secret-store refs are a future
  initiative item that lands before any prod multi-workspace
  deployment.
- Changes to the bearer-resolution middleware itself. It already
  matches the bearer hash against `auth.tokens` correctly; the gap is
  that nothing seeds the row.
- Replacing `SPINE_AUTH_TOKEN` ergonomics in the dev stack — out of
  band of this epic.

## 3. Success Criteria

This epic is successful when:

1. A freshly-loaded workspace under `WORKSPACE_RESOLVER=platform-binding`
   has the `smp-admin` actor + token rows present in its runtime DB
   without any platform-side direct DB writes.
2. SMP can call any workspace-scoped API (e.g.
   `POST /api/v1/workspaces/{id}/runs`) using the workspace's
   `service_token_ref` as bearer and get 200, not 401, on the first
   call after provisioning.
3. SMP `INIT-008/EPIC-001/TASK-003` (the SMP-side companion) closes
   without any Spine-table-writing code in the SMP `Provisioner`.
4. Re-running workspace bootstrap (idle eviction → re-load) does not
   duplicate or invalidate the rows; the sequence is fully idempotent.

## 4. Cross-Repo Coordination

The SMP companion task is
`smp:initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-platform-binding-followups/tasks/TASK-003`
(rewritten 2026-05-04 to align with this epic). SMP's job is purely to
pass the env var; the seeding lives entirely in Spine.

The `architecture/runtime-binding-overview.md` ownership map (cross-repo
synthesis with SMP) names "WorkspaceResolver behaviour" and "per-workspace
connection pool & cache" as Spine-owned. The bootstrap admin row sits
on the same side of that line. SMP never touches the runtime DB.
