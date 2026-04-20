---
id: TASK-003
type: Task
title: "Add write_context.override with operator gate, governance event, and commit trailer"
status: Completed
work_type: implementation
created: 2026-04-18
last_updated: 2026-04-20
completed: 2026-04-20
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-001-artifact-service-integration.md
---

# TASK-003 — Add write_context.override with operator gate, governance event, and commit trailer

---

## Purpose

Expose the per-operation override on the Spine API surface. An operator calling `artifact.create` / `workflow.create` / orchestrator endpoints with `write_context.override: true` can bypass a `no-delete` or `no-direct-write` rule for that single call — and produces the mandatory audit trail.

---

## Context

[ADR-009 §4](/architecture/adr/ADR-009-branch-protection.md) decides:

- Override is per-operation; no repo-wide mode.
- API surface extends `write_context` with `override: true`, mirroring the ADR-008 `run_id` extension.
- Role gate: only `operator+` may set `Override: true` effectively; lower roles get a `Deny` with a distinguishable reason.
- Audit: governance event is authoritative on every path; commit trailer `Branch-Protection-Override: true` is an API-path-only convenience hint.

---

## Deliverable

1. **Field.** Add `override: bool` to the `write_context` wire type across the Spine API; update whatever schema/type definitions the API already uses. Default `false`; omitted ≡ false.

2. **Propagation.** Thread `Override` through to `branchprotect.Request.Override` at every call site wired in TASK-001 and TASK-002. No silent drops.

3. **Role gate.** Already implemented in `branchprotect.Policy` (per EPIC-002 TASK-002). Nothing additional here except verifying the gate fires on the right actor identity.

4. **Governance event.** On every honored override (i.e. `Decision == Allow` where the only reason the policy did not `Deny` was the override), emit `branch_protection.override` with the payload fixed by ADR-009 §4: `{ actor_id, branch, rule_kinds, operation, trace_id, run_id, commit_sha }`. Use the existing event bus. `commit_sha` is `null` for deletions (consistent with the Git-path event shape).

5. **Commit trailer.** On the API write path only: when the enforcement module constructs the commit in-process, add `Branch-Protection-Override: true`. Do this only when the override was actually honored — not when `Override: true` was set but the policy allowed anyway (e.g. no matching rule).

6. **Tests.**
   - Operator override on `main` direct write → allowed, event emitted, trailer present on commit.
   - Contributor with `override: true` on `main` → rejected with the "no override authority" reason.
   - `override: true` on a branch with no matching rule → allowed, no event emitted, no trailer (override was not needed).
   - Deletion override → allowed, event emitted with `commit_sha: null`.

---

## Acceptance Criteria

- `write_context.override` is parsed and validated on every Spine API endpoint that writes.
- Only `operator+` actors can effectively use it; others receive a `Deny` with a reason distinguishing "rule denies" from "override not authorized."
- Every honored override emits exactly one `branch_protection.override` governance event with the full payload.
- API-path overrides carry the `Branch-Protection-Override: true` commit trailer; Git-path overrides (TASK-003 of EPIC-004) do not — and this asymmetry is documented.
- Unnecessary overrides (flag set on an unmatched branch) are allowed silently, do not emit events, and do not add the trailer.

---

## Scope decision — workflow endpoints

ADR-009 §4 names both artifact and workflow endpoints as carrying `write_context.override`, but enforcement is wired only on the **Artifact Service** in this task (matching TASK-001 of this epic, which wired branchprotect into `internal/artifact` only). `workflow.Service` does not consult `branchprotect.Policy` today, so an accepted `override` there would silently succeed without the mandatory audit event or commit trailer — worse than rejecting.

This PR's gateway therefore **rejects `write_context.override: true` on workflow endpoints with 400**, naming the ADR-008 operator-bypass path as the working alternative (omit `write_context` entirely as operator+ role → adds a `Workflow-Bypass` trailer). Wiring branchprotect into `workflow.Service` and unifying the two bypass mechanisms is tracked as follow-up work; ADR-009 §4 can be considered fully delivered once that wiring lands.
