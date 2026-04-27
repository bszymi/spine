---
id: ADR-010
type: ADR
title: SecretClient abstraction for workspace credential resolution
status: Proposed
date: 2026-04-27
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md
---

# ADR-010 — SecretClient abstraction for workspace credential resolution

---

## Context

`INIT-009-workspace-runtime` introduced the `WorkspaceResolver`
with a single source of truth — a config file or a registry
table holding connection strings. The platform side (`smp`)
has now committed to storing only **secret references** in its
bindings, with values living in AWS Secrets Manager (production)
or a file-mounted JSON store (dev / test) — see
`smp:/architecture/adrs/ADR-014-aws-secrets-manager-production-secret-store.md`.

For Spine to consume those bindings, it needs a single,
provider-neutral interface for fetching secret values. Without
one, the resolver would either be hard-wired to AWS (breaking
dev), or carry two divergent code paths (breaking the
single-implementation rule).

The single-workspace deployment path must continue to work
without requiring users to set up a JSON file mount; existing
`SPINE_DATABASE_URL=…` workflows must keep starting Spine. This
is delivered by routing the single-workspace `WorkspaceResolver`
through `SecretClient` (TASK-008) with a narrow bootstrap shim
that falls back to the env var only for
`secret-store://workspaces/default/runtime_db`. There is no
provider-level carve-out: every per-workspace credential read
in Spine goes through `SecretClient`.

---

## Decision

Introduce a `SecretClient` interface in `internal/secrets`:

```go
type SecretClient interface {
    Get(ctx context.Context, ref SecretRef) (SecretValue, VersionID, error)
    Invalidate(ctx context.Context, ref SecretRef) error
}
```

The interface is **read-only**. Spine consumes secrets; rotation
and seeding are platform-side concerns (`smp:INIT-007`,
ADR-011 § Decision: "Spine never writes to the binding"). If a
Spine-side write path is ever needed (e.g. a one-shot bootstrap
CLI), it will live behind a separate `SecretWriter` interface or
a dedicated tool, not on the consumption path.

Two providers ship in this initiative:

- **AWS Secrets Manager** — production. Uses AWS SDK v2 with
  IAM role credentials.
- **File-mounted JSON** — dev and test. Reads from a directory
  mounted into the container, with a deterministic layout
  (`{root}/workspaces/{ws}/{purpose}.json`).

Both providers pass an identical contract test suite under
`internal/secrets/contract/`. This is the mechanism that
prevents prod-only behaviour from hiding behind the dev path.

`SecretValue` is a value type that:

- Renders as `<redacted>` from `String()` and `MarshalJSON`.
- Has a single `Reveal()` accessor for the in-process boundary
  with a database driver or git client.
- Is registered with the structured logger's redaction list.

Provider selection is configured by environment variable:

- `SECRET_STORE_PROVIDER=aws|file`
- AWS: `SECRET_STORE_REGION`, `SECRET_STORE_ACCOUNT`,
  `SECRET_STORE_ENV` (used to build ARNs from refs).
- File: `SECRET_STORE_PATH`.

After this ADR, **all** Spine code that needs a per-workspace
credential goes through `SecretClient`. Direct env-var or file
reads of workspace credentials are forbidden. A CI lint
enforces this.

---

## Consequences

### Positive

- Single, testable interface for credential access.
- Dev and prod exercise the same code paths, separated only by
  provider selection.
- Logger and tracing redaction is enforced by the value type,
  not by discipline.
- Future providers (Vault, GCP Secret Manager) are additive.

### Negative

- One extra interface to learn. Mitigated by the small surface
  (three methods).
- Contract tests need maintenance as the interface evolves.
- A regression in the file provider could mask a bug visible
  only in AWS — mitigated by running the AWS provider in
  staging integration tests.

### Neutral

- All per-workspace credential reads — including the
  single-workspace path — go through `SecretClient`. The file
  `SecretClient` provider itself is filesystem-only (TASK-003).
  Backward compatibility for `SPINE_DATABASE_URL=…` dev
  workflows is delivered by a narrow single-workspace bootstrap
  shim wired in when `WORKSPACE_RESOLVER=file`; the shim falls
  back to the env var only for
  `secret-store://workspaces/default/runtime_db` and for no
  other ref. Migration is tracked in TASK-008.
- One extra `SecretClient.Get` per cold pool open in
  single-workspace mode. Negligible compared with the
  `pgxpool` connect.

---

## Alternatives Considered

### Use the existing config loader for everything

Stretch the current config loader to handle Secrets Manager.
**Rejected** because credentials and config have different
lifecycles (rotation, audit) and different security
requirements (redaction). Conflating them is the bug class
this ADR is designed to prevent.

### Inline AWS SDK calls at each call site

No interface, just call AWS where you need a secret.
**Rejected** because it makes dev impossible without LocalStack
or AWS credentials, and it makes auditing what reads what
secrets impractical.

### Env-var-only for dev

Pass dev secrets through environment variables.
**Rejected** because it skips the reference indirection that
the platform side relies on. Dev would diverge from prod in
a structural way; reference-based scoping (per-workspace IAM,
per-workspace dir) would not exercise.

---

## Links

- Spine companion ADRs: ADR-011, ADR-012
- Platform side: `smp:/architecture/adrs/ADR-012-workspace-runtime-binding-model.md`,
  `smp:/architecture/adrs/ADR-014-aws-secrets-manager-production-secret-store.md`
- Cross-repo overview: `smp:/architecture/runtime-binding-overview.md`
