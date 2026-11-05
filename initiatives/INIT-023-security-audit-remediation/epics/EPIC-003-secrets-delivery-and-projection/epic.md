---
id: EPIC-003
type: Epic
title: Secrets, Delivery and Projection
status: Completed
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
owner: bszymi
created: 2026-10-06
last_updated: 2026-11-05
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/initiative.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: /architecture/data-model.md
  - type: related_to
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
  - type: related_to
    target: smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md
---

# EPIC-003 — Secrets, Delivery and Projection

---

## 1. Purpose

This epic does three things:

- **Secrets.** Add the `enc:v2` secret envelope (a key id, associated data and a key ring), and
  a sweep that re-encrypts existing rows (P3-10, P3-1).
- **Delivery.** Add an opt-in v2 webhook signature that matches SMP's receiver contract. Fix the
  delivery queue, the circuit breaker, the SSRF oracle and URL redaction (P3-11, P3-12, P3-5,
  P3-25, P3-26).
- **Projection.** Close the projection's parser differential and its silent sync gaps:
  - P3-13 is narrowed to strict `Parse` plus round-trip equality, and `Validate` stays
    warn-only;
  - P3-14, P3-22 and P3-23 are fixed.

Nothing in this epic adds runtime schema.

---

## 2. Scope

### In scope

- **`core/crypto`:** the `enc:v2` wire format, `NewSecretCipherRing`, `Seal`/`Open` with
  associated data, and the `migrate`, `strict` and `downgrade` envelope modes.
- **`adapters/store`:** subscription secrets bound to their workspace and row, and an exported
  re-encryption sweep that standalone and SMP TASK-006 both call.
- **`adapters/delivery`:**
  - v2 signatures (`signature.go`): timestamp and nonce covered by the MAC, opted in per
    subscription with `WithSignatureVersion`;
  - every claimed delivery reaching a terminal or future-scheduled state, with a fair claim;
  - a breaker probe that always resolves;
  - a target validator that reports no addresses and classifies NAT64, 6to4 and
    IPv4-compatible IPv6;
  - URL redaction in error columns and logs.
- **`core/workflow` and `adapters/projection`:**
  - one encoder (`workflow.ProjectionDefinition`) shared by the projection and the write path,
    with strict `Parse` and round-trip equality;
  - a rejected file is never projected, and its last verified row is quarantined;
  - one counted `Diff`, whose errors are never dropped;
  - `WithAuthoritativeBranch`;
  - one path scope for `artifacts_dir` on both sync entry points.
- **`service/`:** wiring for the envelope modes and key ring
  (`SPINE_SECRET_ENVELOPE_MODE`, `SPINE_SECRET_PREVIOUS_KEYS`), and the shared-mode
  `spine secrets reencrypt` command.

### Out of scope

- Making `workflow.Validate` blocking by default (initiative §7d, owner question 7).
  `WithStrictWorkflowValidation` exists and is off in both repositories.
- SMP's step-event receiver, its secret loader and its credential floor. Those are SMP
  EPIC-004 TASK-006.
- A projection event router for rejected workflows and refused files in SMP. That is an SMP
  follow-up; the Warn log is the record.
- Making v2 the default for subscriptions created through the standalone API. That is a later
  release (TASK-010 owner question 3).

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | Audit PoCs → regression tests | SMP adopter |
|---|---|---|---|---|---|---|
| TASK-009 | `enc:v2` secret envelope (key id + AAD + key ring) and subscription-secret re-encryption sweep | P3-10, P3-1 | 2 | — | `crypto-secrets-auth`, `gap1-crypto-secrets-reaud`, `verify-r2-1` | SMP TASK-006 |
| TASK-010 | Opt-in v2 webhook signatures matching SMP's receiver, plus delivery-queue, breaker, SSRF and redaction fixes | P3-11, P3-12, P3-5, P3-25, P3-26 | 2 | — | `delivery-scheduler`, `gap1-crypto-secrets-reaud`, `verify-r1-10` | SMP TASK-006 |
| TASK-011 | Projection sync integrity: close the workflow parser differential, stop dropped `Diff` errors, follow the authoritative ref, scope `artifacts_dir` | P3-13 (narrowed), P3-14, P3-22, P3-23 | 2 | — | `store-projection` | SMP TASK-012, plus the first SMP bump that contains it |

All three tasks are wave 2 and independent of each other. EPIC-001 TASK-003 rebases on
TASK-011, and EPIC-002 TASK-006 is blocked by it.

**Status (2026-11-05):** All three tasks are Completed. TASK-010 closed first; TASK-009 (AC19
waived) and TASK-011 (AC 18 and AC 26 scenario clauses waived, AC 24 met through SMP's GitHub CI)
closed on the owner's rulings of 2026-11-05. The acceptance criteria in §6 are met by the tasks.
The epic is Completed.

---

## 4. Key Work Areas

- **TASK-009 — `enc:v2`.**
  - **Format:** `enc:v2:<kid>:<payload>`. The key id is derived with HKDF, so operators manage
    no labels. The AEAD key is HKDF-separated from v1.
  - **Bound data:** GCM authenticates the version, the key id and caller-supplied associated
    data, here the workspace and subscription identity. A value transplanted from another
    workspace or row fails to open.
  - **The ring:** one primary key plus decrypt-only previous keys.
  - **The sweep:** it re-encrypts existing v1 and plaintext rows. `migrate` is the upgrade
    default; `strict` is the documented closing step. P3-10 is closed only in `strict` mode,
    and the CHANGELOG says so.
  - **Rollback:** a `downgrade` mode.
- **TASK-010 — delivery.**
  - **v2 signature.** `v2=HMAC(secret, timestamp "." nonce "." body)` with
    `X-Webhook-Timestamp` and `X-Webhook-Nonce`. It is byte-compatible with SMP's existing v2
    receiver, so SMP's replay guard engages. It is opt-in per subscription; v1 stays the default.
  - **Queue.** A 4xx, an orphaned row or a malformed row reaches `dead` instead of hot-looping.
    A row held by an open circuit keeps its retry budget for a bounded hold.
  - **Breaker.** The half-open probe is always resolved.
  - **Target validator and URLs.** The validator's errors carry no resolved address. Stored and
    logged URLs are redacted.
- **TASK-011 — projection.**
  - **One decoder.** The projection and the write path use one strict decoder with a
    round-trip check, which closes the case-folding differential.
  - **Rejection.** A rejected workflow is not projected and its last verified row is
    quarantined. It never becomes a sync error that wedges the diff window.
  - **Errors.** The second `Diff` call goes away, and every error is counted (P3-14).
  - **The authoritative ref.** The projection follows `refs/heads/<authoritative>`, not the
    checked-out `HEAD` (P3-22).
  - **One path scope.** `artifacts_dir` scopes `FullRebuild` and `IncrementalSync` alike (P3-23).
  - **The sweep.** Active rows that cannot be verified are swept once per service instance.

---

## 5. Primary Outputs

- **`core/crypto`:** the `enc:v2` envelope and key ring, and the `KeyID` and `AssociatedData`
  helpers.
- **`adapters/store`:** bound subscription secrets and `ReencryptSubscriptionSecrets`.
- **`adapters/delivery`:** `signature.go` (`SignV2`, `WithSignatureVersion`, the header
  constants), `redact.go`, and fixes to the queue, the breaker and the target validator.
- **`core/workflow`:** `ProjectionDefinition`. **`adapters/projection`:**
  `WithAuthoritativeBranch`, `WithStrictWorkflowValidation` (off by default) and the quarantine
  state.
- **`service/cmd/spine`:** envelope-mode and key-ring configuration, and `spine secrets
  reencrypt`.
- **Documentation:**
  - `architecture/security-model.md`: envelope, rotation runbook, strict mode;
  - `docs/integration-guide.md`: v2 signature verification;
  - `architecture/event-schemas.md`: new operational events;
  - `CHANGELOG.md`: P3-10 closed only in strict mode, and the B1 delivery change.

---

## 6. Acceptance Criteria

- **P3-10, P3-1.**
  - An `enc:v2` value moved to another workspace's row, or to another row, fails to open.
  - Relabelling the key id fails authentication.
  - Rotation with a previous key keeps every row readable, and the sweep re-encrypts v1 and
    plaintext rows to the primary key.
  - `strict` refuses anything else.
- **P3-11.** With v2 on, each delivery carries a fresh timestamp and nonce under the MAC. A
  receiver that follows the contract therefore refuses a captured delivery replayed outside its
  window or with a reused nonce.
  - Golden vectors in `signature_test.go` pin the format, recomputed independently.
  - SMP TASK-006 commits the contract test that verifies Spine's v2 signer against SMP's
    receiver at the pinned revision.
- **P3-5, P3-12.**
  - Every claimed delivery reaches a terminal or future-scheduled state.
  - Newer rows are not starved behind non-retryable or circuit-held rows.
  - A probe on any early-return path resolves the breaker.
- **P3-25, P3-26.** Validator errors carry no resolved address. NAT64, 6to4 and IPv4-compatible
  IPv6 forms of private addresses are refused. No full target URL appears in `last_error`,
  `event_delivery_log.error` or a log line.
- **P3-13 (narrowed).** A workflow with a hidden case-variant key, or any file that fails strict
  `Parse` or the round trip, is never projected. `Validate` failures log only, unless
  `WithStrictWorkflowValidation(true)` is set.
- **P3-14.** A `Diff` failure during `IncrementalSync` is never reported as `idle` with
  governance changes skipped.
- **P3-22, P3-23.** The projection does not follow a checked-out run branch, and an out-of-scope
  file cannot overwrite a governed artifact's projection row.
- **Compatibility.** No runtime schema change. Each task's SMP compatibility gate passed.
  TASK-010's shared-behaviour change (B1, initiative §7b) is in `CHANGELOG.md` with the SMP
  requirement it creates.

---

## 7. Cross-Repo Coordination

- **SMP TASK-006 adopts TASK-009 and TASK-010.** It encrypts step-event secrets by default,
  binds them with `enc:v2`, opts every step-event subscription into v2 and then requires v2.
  Its final commit bumps `.spine-revision` to a Spine `main` commit that contains both tasks.
- **The B1 hazard.** After TASK-010, a 4xx from SMP's receiver dead-letters on the first attempt.
  Today SMP relies on the hot loop through the credential-rotation window and on transient
  401s. So **SMP TASK-006's R1 (503 for transient receiver failures) and R2 (the rotation-window
  requeue) must be on SMP `main` before any SMP bump that includes TASK-010.** That holds
  whichever SMP task bumps first. R1 and R2 are safe on the old Spine too.
- **TASK-011 and SMP.**
  - **The backfill probe.** TASK-011 reaches SMP's two projection sites at the first bump that
    contains it. That bump must also carry SMP's backfill-probe change (SMP TASK-012 §4.6).
    Otherwise a workspace whose canonical workflow is now rejected gets a `FullRebuild` on every
    leader acquisition, which strands its in-flight runs.
  - **The validate preview.** SMP TASK-012 keeps SMP's validate preview equal to the projection
    check, and adds an SMP corpus test.
  - **Options SMP leaves off.** SMP leaves `WithAuthoritativeBranch` and
    `WithStrictWorkflowValidation` off. Its projection reads a bare repository whose `HEAD` is
    pinned, so P3-22 does not apply.
