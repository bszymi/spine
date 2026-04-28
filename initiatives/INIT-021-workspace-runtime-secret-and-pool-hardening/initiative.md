---
id: INIT-021
type: Initiative
title: Workspace Runtime — Secret Resolution and Connection-Pool Hardening
status: Completed
owner: bszymi
created: 2026-04-27
last_updated: 2026-04-28
links:
  - type: related_to
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: related_to
    target: /architecture/components.md
  - type: related_to
    target: /architecture/data-model.md
---

# INIT-021 — Workspace Runtime — Secret Resolution and Connection-Pool Hardening

---

## 1. Intent

`INIT-009-workspace-runtime` (Completed) shipped Spine's workspace
routing layer: `WorkspaceResolver` interface, file/env and database
providers, registry table, per-workspace isolation. It explicitly
deferred two follow-ups that are now blocking the platform's
multi-workspace deployment work:

- **Secret resolution.** The current resolver loads connection
  strings from configuration. The platform's INIT-007 commits to
  storing only secret references in its bindings; raw credentials
  live in AWS Secrets Manager (production) or a file-mounted store
  (dev). Spine needs a `SecretClient` abstraction and a resolver
  extension that dereferences secret references at the boundary.
- **Connection pool and cache policy.** INIT-009 §6 listed
  connection-pool pressure and pool-sizing as open risks but did
  not commit to concrete policy. Per-workspace pool sizing,
  invalidation triggers, and idle eviction now need to be
  decided.

This initiative addresses both, paired with the platform's
`INIT-007-multi-workspace-runtime-resolution`. Both must land
together for end-to-end multi-workspace operation in AWS.

---

## 2. Scope

### In scope

- **`SecretClient` interface** in Spine, with two providers:
  - AWS Secrets Manager (production)
  - File-mounted JSON (dev / test)
- **WorkspaceResolver extension** to fetch the binding from the
  platform (HTTP), dereference secret references via
  `SecretClient`, and assemble the `WorkspaceConfig` /
  workspace context.
- **Per-workspace connection pool policy:** sizing defaults,
  per-workspace overrides, idle eviction, and invalidation
  triggers (binding `Invalidate` API call from the platform,
  cache TTL, repeated connection error).
- **Contract tests** ensuring the file and AWS providers behave
  identically.
- **Three ADRs** covering the above three decisions.
- Updates to relevant architecture documents
  (`components.md`, `data-model.md`, `git-integration.md` if
  Git credentials change resolution path).

### Out of scope

- Platform-side work — bindings, secret-store provisioning, IAM,
  RDS topology. All in `smp:/initiatives/INIT-007-multi-workspace-runtime-resolution`.
- Automated rotation (Secrets Manager scheduled rotation with
  a Lambda). Manual-but-scripted rotation is supported via the
  binding `Invalidate` call; automation is a follow-up.
- Dedicated-mode-specific behaviour beyond what already exists.

---

## 3. Success Criteria

This initiative is successful when:

1. Spine can fetch a workspace's binding from the platform's
   internal API and dereference its secret references via
   `SecretClient` to produce a usable `WorkspaceConfig`.
2. Both `SecretClient` providers (AWS, file) pass the same
   contract tests.
3. A per-workspace connection pool exists with documented
   sizing defaults, configurable overrides, and idle eviction.
4. A binding `Invalidate` notification from the platform causes
   Spine to drop and re-fetch the affected workspace's binding
   and connection pool, without restart and without affecting
   other workspaces.
5. Three ADRs are accepted: `SecretClient` abstraction, resolver
   secret-ref dereference, per-workspace pool policy.
6. Single-workspace deployments continue to start with
   `SPINE_DATABASE_URL=…` (no JSON file mount required), but
   via the new `SecretClient` path: the `WORKSPACE_RESOLVER=file`
   resolver delegates to the file `SecretClient` provider, with
   a narrow bootstrap shim falling back to the env var only for
   `secret-store://workspaces/default/runtime_db`. The legacy
   `SPINE_WORKSPACE_MODE` knob is replaced by `WORKSPACE_RESOLVER`
   — this is a deliberate breaking config change with no
   automatic migration.

---

## 4. Primary Artifacts Produced

- `/architecture/adr/ADR-010-secret-client-abstraction.md`
- `/architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md`
- `/architecture/adr/ADR-012-per-workspace-connection-pool-policy.md`
- `internal/secrets/` — `SecretClient` interface and two providers
- Resolver code path that consumes bindings from the platform
- Per-workspace pool implementation with eviction and
  invalidation
- Contract test suite under `internal/secrets/contract/`

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- **Source of Truth** — Git remains authoritative per workspace.
  Bindings and secrets are runtime infrastructure, not artifact
  truth.
- **Disposable Database** — runtime state per workspace remains
  operational and rebuildable.
- **Actor Neutrality** — secret resolution does not create or
  modify actor identity.
- **Reproducibility** — given the same binding and secret-store
  contents, resolver output is deterministic.

Additional constraints:

- The `SecretClient` interface is the **only** path for any code
  in Spine to obtain workspace credentials. Direct env-var or
  file reads for workspace credentials are forbidden after this
  initiative.
- Secret values must never appear in structured logs, traces,
  or error messages.
- Backward compatibility with the file/env provider in
  single-workspace deployments is required.
- Spine never writes to the binding table — it consumes
  bindings via the platform's read endpoint.

---

## 6. Risks

- **Platform binding API as a single point of failure.** Spine
  depends on the platform to resolve bindings. If the platform
  is down, Spine cannot bring up new workspace contexts.
  *Mitigation:* cached bindings continue to serve. TTL is set
  long enough to ride out short platform outages.
- **Provider behaviour drift.** The dev file provider and the
  AWS provider could diverge.
  *Mitigation:* contract test suite that runs both against the
  same scenarios in CI.
- **Cache invalidation race.** A rotation that completes on the
  platform side before Spine sees the `Invalidate` could leave
  Spine briefly using stale credentials.
  *Mitigation:* rotation runbook (platform side) keeps both
  versions valid until Spine acknowledges; Spine retries on
  connection errors with cache invalidation.
- **Pool exhaustion under load.** Per-workspace pools sum to a
  global connection budget that cannot exceed PgBouncer/RDS
  capacity.
  *Mitigation:* documented default per-workspace pool size,
  observability on pool saturation, idle eviction.

---

## 7. Work Breakdown

### Epics

```
/initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/
  /epics/
    /EPIC-001-secret-client/
    /EPIC-002-resolver-secret-ref/
    /EPIC-003-pool-policy/
```

| Epic | Title | Dependencies |
|------|-------|--------------|
| EPIC-001 | `SecretClient` abstraction and providers | — |
| EPIC-002 | Resolver dereference of secret references | EPIC-001 |
| EPIC-003 | Per-workspace pool & cache policy | EPIC-002 |

---

## 8. Exit Criteria

INIT-021 may be marked complete when:

- All three epics are completed.
- ADR-010, ADR-011, ADR-012 are accepted.
- Spine end-to-end can serve a workspace whose binding lives in
  the platform and whose credentials live in Secrets Manager
  (staging) and the file-mounted store (dev).
- Contract test suite is green for both providers.
- A platform-initiated `Invalidate` is observably honored by
  Spine in staging.
- File/env provider continues to support single-workspace
  deployments unchanged.

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- Predecessor initiative: `/initiatives/INIT-009-workspace-runtime/initiative.md`
- Components: `/architecture/components.md`
- Data Model: `/architecture/data-model.md`
- Platform companion: `smp:/initiatives/INIT-007-multi-workspace-runtime-resolution/initiative.md`
- Cross-repo synthesis: `smp:/architecture/runtime-binding-overview.md`
