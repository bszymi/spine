---
id: EPIC-003
type: Epic
title: Standalone Server Hardening
status: Pending
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
owner: bszymi
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/initiative.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
---

# EPIC-003 — Standalone Server Hardening

---

## 1. Purpose

This epic closes the findings that reach only standalone `spine serve`.

- **N-2 (P2).** In shared mode (`db` or `platform-binding` resolver), the trusted-CIDR token-less
  clone is not scoped to a workspace. A runner host trusted for one tenant reads every tenant.
- **N-14 to N-17 (P3). Session and auth hygiene:**
  - an SSE stream outlives a revocation (N-14);
  - an unauthenticated request builds a workspace's services (N-15);
  - an unset `SPINE_ENV` runs non-production silently (N-16);
  - `run.status` shows author-writable evidence as fact (N-17).
- **P3-27 (open half).** No scheduler runs in the shared modes. This is a functional gap with a
  governance edge: step and run timeouts never fire, orphans are never recovered, and committing
  runs are never retried.

SMP embeds none of `service/internal/gateway`, `service/cmd/spine` or `core/evidence`, and calls
no `MultiScheduler`, so every task here is inert for SMP (gates only).

---

## 2. Scope

### In scope

- `service/internal/gateway`: `handlers_git.go`, `handlers_events_stream.go` and
  `middleware.go`.
- `service/internal/githttp` (the trusted-CIDR lookup), `service/cmd/spine` (config, wiring,
  lifecycle), `core/evidence` and `adapters/scheduler` (wiring only).
- The shipped compose files (`SPINE_ENV`), `docs/integration-guide.md`,
  `docker-compose.override.yaml.example`, `KNOWN-LIMITATIONS.md` and `CHANGELOG.md`.

### Out of scope

- Job-scoped runner clone tokens. TASK-006 offers workspace-bound CIDRs instead (TASK-006
  Decision 2).
- Multi-replica scheduler leadership (TASK-008 Out of scope).
- Proactive SSE close on revocation through an event bus (TASK-007 owner question 1).

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | PoCs → regression tests | SMP impact |
|---|---|---|---|---|---|---|
| TASK-006 | Workspace-scoped trusted-CIDR clone in shared mode | N-2 | 2 | — | `scratch/standalone/sa2/zz_sa2_trusted_cidr_crosstenant_test.go` | no |
| TASK-007 | Session and auth hygiene | N-14, N-15, N-16, N-17 | 2 | — | `scratch/standalone/sa3/zz_sa3_sse_revocation_test.go` | no |
| TASK-008 | Wire the multi-workspace scheduler into shared-mode `spine serve` | P3-27 (open half) | 2 | — | none; the task writes its own wiring tests | no |

---

## 4. Acceptance Criteria

- **N-2.**
  - In shared mode, a token-less read from a trusted address is admitted only for the workspaces
    that address is bound to. Every other workspace answers the uniform 401.
  - `spine serve` refuses to start in shared mode with an unbound trusted CIDR.
- **N-14.** A stream whose bearer was revoked, or whose actor was suspended, closes at the next
  revalidation tick.
- **N-15.** In shared mode, a request without a well-formed bearer never builds a ServiceSet.
  A ServiceSet build failure is a uniform 401 for any caller the build left unauthenticated.
- **N-16.** `spine serve` and `spine secrets reencrypt` refuse an unknown `SPINE_ENV`, and refuse
  an unset one (owner question in TASK-007).
- **N-17.** Evidence read from a run branch is labelled unverified in `run.status`.
- **P3-27.** In `db` and `platform-binding` modes, one scheduler tick scans every active
  workspace with the bounded `MultiScheduler`.
- Single-mode behaviour is unchanged except where a task says otherwise. Every task's SMP gate
  passes.
