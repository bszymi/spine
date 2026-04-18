---
id: TASK-001
type: Task
title: "Wire Policy.Evaluate into Artifact Service commit helpers"
status: Pending
work_type: implementation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
---

# TASK-001 — Wire Policy.Evaluate into Artifact Service commit helpers

---

## Purpose

Every path in `internal/artifact` that advances a ref (commit, merge, delete) must consult `branchprotect.Policy.Evaluate` before mutating the repository. This catches direct commits that bypass the workflow machinery — the exact class of write ADR-009 §2 calls out.

---

## Context

[ADR-009 §3](/architecture/adr/ADR-009-branch-protection.md) locates the check in "the Artifact Service's commit helpers" and "`Orchestrator.MergeRunBranch`". This task covers the Artifact Service half; the Orchestrator half is TASK-002.

The classifier rule is simple: if the caller provides a `write_context.run_id` that resolves to a Run whose workflow authorized the merge, `Kind = GovernedMerge`; otherwise `Kind = DirectWrite`. Deletions are `Kind = Delete` regardless of `run_id`.

---

## Deliverable

1. **Audit the commit surface.** Enumerate every `internal/artifact` helper that advances or deletes a ref. Each must either:
   - Accept a `branchprotect.Policy` dependency and call `Evaluate` before writing, or
   - Be explicitly out-of-scope (e.g. a read-only or in-memory helper) with a code comment justifying the exclusion.

2. **Classify.** For each call site, construct a `Request` with the correct `Kind`:
   - Commit helpers with a `write_context.run_id` that resolves to an authorizing Run: `GovernedMerge`.
   - Commit helpers without that linkage: `DirectWrite`.
   - Deletion helpers: `Delete`.

3. **Plumb `ActorIdentity`.** The policy evaluator needs actor ID and role. Reuse whatever the existing request context exposes (the same identity that ADR-008's override already consumes).

4. **Handle `Deny`.** A `Deny` decision translates to a structured error that the API layer surfaces with the rule that failed and the branch. Do not leak bootstrap-vs-configured-rules distinctions to external callers.

5. **Tests.** Per-helper unit tests mocking the `Policy`. Plus at least one integration test showing: a commit to `main` without `run_id` → rejected; a commit to a non-protected branch → allowed.

---

## Acceptance Criteria

- Every ref-advancing helper in `internal/artifact` either consults `Policy.Evaluate` or has a justified in-code exclusion.
- Direct commit to a `no-direct-write` branch via the Artifact Service is rejected with a clear error.
- Governed merges (with `write_context.run_id`) still succeed.
- No behavior change on branches with no matching rule.
- Package-level tests cover the three `Kind` values and the two terminal decisions.
