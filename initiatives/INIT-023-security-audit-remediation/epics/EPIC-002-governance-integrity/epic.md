---
id: EPIC-002
type: Epic
title: Governance Integrity
status: In Progress
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
owner: bszymi
created: 2026-10-06
last_updated: 2026-11-05
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: related_to
    target: /architecture/engine-state-machine.md
  - type: related_to
    target: smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md
---

# EPIC-002 — Governance Integrity

---

## 1. Purpose

Make "approved" mean "what was approved got merged":

- **Bind the governed merge to the commit that passed the gates** (F2 merge half, P3-37).
- **Guard governance files** so they cannot ride a governed merge into live policy (F3). This
  needs an ADR that changes ADR-009 §1.
- **Add a step-entry policy for assign and claim** (P3-3, P3-4, P3-15, P3-16).
- **Add submit ownership with evidence, and compare-and-swap writes** (P3-17, the evidence half
  of P3-19, P3-24).
- **Add an artifact-status lifecycle with a run-owned cascade** (P3-21).

All new enforcement is opt-in through orchestrator options. Each option's zero value is
today's behaviour, so SMP adopts it deliberately. `spine serve` turns them on for standalone,
as initiative owner question 10 sets.

---

## 2. Scope

### In scope

- `core/engine` merge, submit, assign, claim and cancel paths (`merge.go`, `result.go`,
  `step.go`, `assign.go`, `claim.go`, `run.go`, `branch.go`).
- `core/branchprotect`, for the governance-path decision, and `adapters/projection`, for the
  protection-removal guard.
- `core/validation` (preconditions, status-conflict rules), `core/domain` (the declared status
  lifecycle), and `core/workflow` (commit-status checks at create and update).
- `adapters/git`: `CLIClient` implements the optional `CommitBinder` capability.
- `adapters/store`: compare-and-swap run-status writes and an optional step compare-and-swap
  capability. There is no migration.
- The ADR (ADR-016, number assigned at merge), and amendment notes in ADR-008 §2 and ADR-009 §1,
  §5 and §6.
- `spine serve` wiring for every option (owner question 10).

### Out of scope

- Who may push a run branch. That is EPIC-001 TASK-016 and SMP TASK-007: this epic decides what
  merges, not who writes.
- Enforcing `required_outputs` (initiative §7d). TASK-008 records evidence only.
- Update-capable workflow planning runs and `write_context` scoping. Those are EPIC-004
  TASK-014.
- Matching skills by id (initiative §7d). TASK-013 makes skill grants admin-only instead.

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | Audit PoCs → regression tests | SMP adopter |
|---|---|---|---|---|---|---|
| TASK-007 | Opt-in step-entry policy for assign and claim: run state, eligibility, mode, preconditions, separation of duties | P3-3, P3-4, P3-15, P3-16 | 2 | — | `skeptic-selfreview`, `skeptic-precond`, `engine-authz`, `verify-r1-6`, `verify-r1-7` | SMP TASK-011 |
| TASK-005 | Bind the governed merge to the commit that passed validation and review | F2 (merge), P3-37 | 3 | EPIC-001 TASK-002 | `governance`, `verify-r1-1`, `gap3-multi-repo-code-repo` | SMP TASK-009 |
| TASK-006 | Governance-path guard on governed merges and protection-removal guard in projection | F3 | 4 | TASK-005, EPIC-003 TASK-011 (soft: EPIC-001 TASK-003) | `store-projection`, `verify-r1-2` | SMP TASK-010 |
| TASK-008 | Submit ownership, step-output evidence and compare-and-swap state writes | P3-17, P3-19 (evidence), P3-24 | 4 | TASK-005, TASK-007 | none cited from the audit's `poc/`; the task writes its own tests | SMP TASK-011 |
| TASK-017 | Declared artifact-status lifecycle and run-owned merge cascade (opt-in) | P3-21 | 4 | TASK-005 (EPIC-003 TASK-011 for `artifacts_dir`) | `governance`, `gap2-git-state-concurrenc`, `parsing` | SMP TASK-011 |

The PoC directories are under the audit's `poc/`, which is held out of tree. Each task names
the exact PoC files it scales down into committed tests. The self-review chain behind F3
(`skeptic-selfreview`) is TASK-007's separation-of-duties test. TASK-006 holds the governance
paths regardless.

**Status (2026-11-05):** TASK-005, TASK-007 and TASK-017 are Completed. TASK-008 closed on the
owner's ruling of 2026-11-05 (the wording amendments to AC 10, 11, 14 and 21). TASK-006 is In
Progress: ADR-016's sign-off (AC 1) and the AC 11 and AC 18 amendments wait for the owner. The epic
stays In Progress.

---

## 4. Key Work Areas

- **TASK-005 — merge binding** (`Orchestrator.EnableMergeBinding`).
  - When a gate step submits, the engine captures the run-branch head server-side. It records
    the head in reserved `runs.commit_meta` keys (`spine.bound_sha`, `spine.bound_by`), with an
    optional `expected_head_sha` assertion from the client.
  - An advancing approval of a branch that moved since the last binding is refused (owner
    question 4).
  - The merge uses exactly the bound commit (`MergeBound`, `CommitFilesOnto`, `PushCommit`) and
    verifies the result. The status cascade commits onto the bound commit.
  - Code-repository merges are bound the same way or fail closed (P3-37). Under binding, a
    loop-back re-merges a code repository whose bound commit is not on its default branch.
  - `CommitBinder` is an optional capability, so SMP compiles unchanged. `spine serve` enables
    binding with no off switch.
- **TASK-006 — governance-path guard** (`WithGovernanceGuard`, `WithProtectionRemovalGuard`).
  - **The ADR comes first.** It changes ADR-009 §1: governed merges are no longer
    unconditionally allowed.
  - **The guard.** It judges exactly `merge-base(main, bound)..bound`. A change under `.spine/`,
    `.spine.yaml`, `workflows/` or `<artifacts_dir>/workflows/` holds the merge
    (`ErrGovernanceApprovalRequired`) until an operator approves or rejects that exact bound
    SHA. There is an optional ADR-008 lifecycle exemption (owner question 6).
  - **The projection guard.** The projection refuses a branch-protection config that removes
    protection from the authoritative branch, unless a server-side approval or override record
    covers the change. Commit trailers are never trusted.
- **TASK-007 — step-entry policy** (`WithStepEntryPolicy`, `StrictStepEntryPolicy()`).
  - Assign and claim check the following:
    - run status;
    - actor existence, type, skills and eligible ids;
    - execution mode, so an engine-owned `spine_only` step cannot be claimed;
    - preconditions, which fail closed under strict evaluation;
    - separation of duties on review-type steps, keyed on step type and not on skills.
  - Cancel terminates live executions.
  - Each check is a separate switch. SMP chooses its set (owner question 2), and `spine serve`
    installs the strict policy with a logged `legacy` escape.
- **TASK-008 — submit integrity.**
  - The engine verifies the submitter (`SubmitRequest.ActorID`).
  - Run-status writes are compare-and-swap (`TransitionRunStatus`), so a race can no longer
    resurrect a cancelled run.
  - Step-execution writes can be compare-and-swap on `(status, actor_id)`, through an optional
    capability.
  - Each step completion records its head SHA and changed paths as evidence, using TASK-005's
    capture. An empty step becomes visible.
- **TASK-017 — status lifecycle** (`WithStatusLifecycle`, `WithBlockingStatusConflicts`).
  - A declared transition table per artifact type (`domain.DefaultStatusLifecycle()`).
  - Commit-status checks run when a workflow is created or updated.
  - The merge cascade writes only artifacts the run owns, and only declared transitions.
  - The status-conflict rules SC-002 and SC-003 can block.
  - It requires merge binding.

---

## 5. Primary Outputs

- `architecture/adr/ADR-016-governance-path-guard.md`, Accepted, with "Amended by ADR-016" notes
  in ADR-009 and ADR-008.
- New engine options and types:
  - `MergeBindingConfig`, `CommitBinder`;
  - `GovernanceGuard`, `ErrGovernanceApprovalRequired`, a governance decision API, and the
    `run.governance.decide` permission;
  - `StepEntryPolicy`;
  - `SubmitRequest.ActorID`, an optional step compare-and-swap port;
  - `domain.DefaultStatusLifecycle()`.
- `adapters/projection.WithProtectionRemovalGuard`.
- Updated `architecture/engine-state-machine.md`, `architecture/event-schemas.md`,
  `architecture/security-model.md`, `KNOWN-LIMITATIONS.md` (binding-off limitations) and
  `CHANGELOG.md`.
- Committed regression tests from the PoCs in §3.

---

## 6. Acceptance Criteria

- **The zero value is today.** With no new option set, every existing engine, gateway and
  scenario test passes unchanged. Each task's SMP compatibility gate passed against SMP `main`
  with no SMP edit.
- **F2.** With binding on, a commit pushed to the run branch after the validated or reviewed
  head does not reach `main`, and the merged tree is the bound commit's. An approval of a moved
  head is refused. Code-repository merges are bound or fail closed.
- **F3.**
  - With the guard on, a governed merge that changes `.spine/branch-protection.yaml` or
    `workflows/**` is held until an operator approves the exact bound SHA. Reject routes the run
    back.
  - With the projection guard on, a merged `rules: []` does not remove protection from `main`
    without a matching approval or override record.
  - The audit's single-member self-review chain no longer reaches `main` (F3 PoCs inverted).
- **P3-3, P3-4, P3-15, P3-16.** With the policy on:
  - an ineligible actor cannot be assigned or claim;
  - an engine-owned `spine_only` step cannot be claimed;
  - a step whose precondition fails stays unassignable;
  - a cancelled or paused run cannot be advanced, and cancelling it terminates its live
    executions.
- **P3-17, P3-24.** A submit carrying `ActorID` from an actor who does not hold the step is
  refused. Run-status writes use compare-and-swap, so a cancel and a completion cannot both win.
- **P3-19 (evidence).** `step_completed` carries the step's head SHA and changed paths, and
  flags a step that declared outputs but changed nothing.
- **P3-21.** With the lifecycle on, the merge cascade cannot change the status of an artifact
  the run does not own, and cannot apply an undeclared transition.
- **Standalone wiring** in `spine serve` matches initiative owner question 10, and each escape
  hatch logs when it is used.

---

## 7. Cross-Repo Coordination

- **Order:** Spine TASK-005 → SMP TASK-009 (`RealGitOperator` implements `CommitBinder`; binding
  on; SMP bump).
- **Then** Spine TASK-006 → SMP TASK-010 (both guards on, with the admin approval API and UI).
  SMP TASK-010 comes after SMP TASK-009, because the guard needs a bound SHA.
- **Then** Spine TASK-007, TASK-008 and TASK-017 → SMP TASK-011 (policy set, submitter identity
  on every submit path, step compare-and-swap, lifecycle). SMP TASK-011 needs SMP TASK-009,
  because the lifecycle requires binding.
- **Constraints on all three SMP tasks:** the run-status compare-and-swap reaches SMP at the
  bump through SMP's existing `TransitionRunStatus`, and needs no opt-in. No other behaviour in
  this epic reaches SMP until SMP sets an option.
- **SMP TASK-012 records the opt-in ledger at close:** which options SMP turns on, which it
  leaves off, and why.
