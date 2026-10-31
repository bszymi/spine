---
id: TASK-011
type: Task
title: "Aggregate checkout bound: one writer per branch, bounded in-flight checkout bytes"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-31
last_updated: 2026-10-31
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/tasks/TASK-003-checkout-merge-amplification-budget.md
---

# TASK-011 — Aggregate checkout bound: one writer per branch, bounded in-flight checkout bytes

---

## Purpose

The TASK-010 cross-task review (2026-10-31) found a P3 next to N-1. TASK-003 bounds each checkout, merge and push
by the tree budget, but nothing bounds the total bytes that concurrent branch-scoped writes put on disk at once.
`KNOWN-LIMITATIONS.md` accepted "concurrent worktrees of different branches each get the full budget"; the review
showed the premise is incomplete: concurrent writes to the *same* run branch each write the full within-budget tree
into the shared temporary directory before git refuses the second checkout of that branch, so even refused writes
materialise the tree. This task closes the residual.

## Finding

| ID | Sev | Reach | Defect |
|---|---|---|---|
| X-1 (TASK-010 cross-task review) | P3 | Standalone and embedders that set the tree budget | `EnterBranchWithLimits` (`adapters/git/branchwrite.go`) measures, creates a temporary parent, adds a linked worktree and resets it to the branch before git's own "already checked out" refusal at `switch`. There is no per-branch serialisation and no ceiling on in-flight checkout bytes, so N concurrent branch-scoped writes (for example artifact writes with a run write context) put up to N full trees on the shared disk, refused writes included. git's in-use guard also races under concurrent entries (several linked worktrees attach to one branch; the ref compare-and-swap turns that into failed writes, never lost updates). |

## Design

1. **One writer per branch, claimed before any write.** `EnterBranchWithLimits` claims the key (git common
   directory, branch) in-process before the measure and before the temporary directory is created, and holds it
   until the returned scope's Cleanup. A second entry for the same key is refused immediately (or waits under the
   caller's context — pick one, record why) without writing anything. The refusal keeps the error classification
   embedders see today for a branch that is already checked out (same sentinel or a wrapped one; prove SMP's
   classification is unchanged).
2. **Bounded in-flight checkout bytes.** A process-wide weighted semaphore, weighted by the measured tree's
   `TotalBytes`, admits a checkout only while the sum of in-flight measured trees stays under a ceiling. Acquisition
   waits under the caller's context; a context that ends first refuses with a distinct, retryable error. A tree larger
   than the ceiling on its own is refused (it could never be admitted). The ceiling is part of the checkout limits:
   zero (the library default) means no aggregate bound, so embedders see no change; `spine serve` sets it from a new
   `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES` (default: the per-tree byte maximum, so at most one maximal tree is on disk
   at a time and small trees proceed concurrently).
3. Every caller that writes a tree through these paths (branch writes, and any other checkout path that shares the
   temporary directory and the measure) uses the same claim and semaphore where it applies; list each path and its
   decision in the Implementation Notes.

## Acceptance Criteria

1. **Regression (red at 854a1e08):** N concurrent `EnterBranch` calls on one branch with a within-budget tree → one
   writes; the others are refused (or wait) before creating any `spine-wt-parent-*` directory; the peak bytes under
   TMPDIR never exceed one tree.
2. **Regression through the API path (red at 854a1e08):** concurrent `artifact.Service.Create` calls with a write
   context on the same run branch → peak bytes ≤ one tree; every successful write is on the branch.
3. **Aggregate bound:** with the ceiling set, concurrent entries on different branches never hold more than the ceiling
   in measured bytes; a context that ends while waiting gets the retryable error; an over-ceiling tree is refused.
4. **Library default unchanged:** with zero limits, behaviour is identical for every input an embedder produces except
   that a concurrent same-branch entry is refused before writing instead of after; SMP compatibility gate passes.
5. Existing suites, the scenario-git-http job and git-floor (2.39.5) pass; mutations of the claim and the semaphore are
   caught by named tests.
6. Docs: `KNOWN-LIMITATIONS.md` (the aggregate residual becomes enforced, with what remains), `docs/integration-guide.md`
   (the new variable), `CHANGELOG.md` ("Changed (embedders)" for the earlier refusal and the new limit field).

## Out of scope

- Per-tenant disk quotas; multi-replica coordination (the bound is in-process, like the other limits).
- `SPINE_GIT_HTTP_DEV_NO_AUTH` in shared development mode (a TASK-006 follow-up recorded by TASK-010).
