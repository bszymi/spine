---
id: TASK-002
type: Task
title: "Wire Policy.Evaluate into Orchestrator merge and divergence paths"
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
  - type: related_to
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-001-artifact-service-integration.md
---

# TASK-002 — Wire Policy.Evaluate into Orchestrator merge and divergence paths

---

## Purpose

`Orchestrator.MergeRunBranch` and the divergence-branch lifecycle are the two remaining code paths where Spine advances authoritative refs. Both must consult `branchprotect.Policy` with the correct classification so that governed merges go through while anything that looks like a bypass is rejected.

---

## Context

[ADR-009 §3](/architecture/adr/ADR-009-branch-protection.md):

> Every code path that advances a ref goes through a small number of well-defined functions (the Artifact Service's commit helpers, `Orchestrator.MergeRunBranch`, divergence branch management). Those functions call `Policy.Evaluate` with `Kind: GovernedMerge` (for merge paths carrying a `RunID`) or `Kind: DirectWrite` (for direct commit paths).

[ADR-009 §3](/architecture/adr/ADR-009-branch-protection.md) also clarifies that Spine-internal `spine/*` branches are out-of-scope by construction — rules do not name them. The Orchestrator must pass the correct branch to `Evaluate` (the *target* branch of a merge, not the run branch being merged from).

---

## Deliverable

1. **Audit `internal/engine` merge + divergence surfaces.** Enumerate the ref-advancing entry points: `MergeRunBranch`, divergence-branch creation/merge/delete, scheduler recovery writes. Classify each.

2. **Classify and wire.**
   - `MergeRunBranch` on an authorizing Run outcome: `Kind = GovernedMerge`, `RunID` populated. The policy allows this regardless of rules.
   - Divergence-branch operations on `spine/*`: the branch pattern does not match user rules by design (§3), so evaluation is a no-op in practice — but the call still goes through the policy so the audit trail is consistent.
   - Scheduler recovery writes: if any exist that advance authoritative refs outside a Run, classify as `DirectWrite` and let the policy reject them. If that reveals a real bug (the scheduler would be doing a silent direct write), file a follow-up; do not paper over it with an exemption.

3. **Pass correct branch + `TraceID`.** The policy needs the *destination* ref for every call, and a `TraceID` so audit events correlate with the Run's trace. Use whatever trace context the Orchestrator already threads.

4. **Tests.**
   - Authorizing Run → `MergeRunBranch` succeeds on `main`.
   - Merge request whose Run has not reached an authorizing outcome → rejected (even though it looks like a merge at the Git level, it is a direct write from the policy's perspective).
   - Divergence operation on `spine/run/<id>` → allowed, no audit event (rule set empty for the pattern).

---

## Acceptance Criteria

- Every authoritative-ref-advancing call in `internal/engine` routes through `Policy.Evaluate`.
- Governed merges to a `no-direct-write` branch succeed; non-governed merges to the same branch are rejected.
- `spine/*` branches are unaffected (no user-authored rules target them, by construction).
- No silent direct writes remain in the Orchestrator — any that existed are either reclassified as governed merges or surfaced as a bug.
- Tests demonstrate the happy path and every reject path listed above.
