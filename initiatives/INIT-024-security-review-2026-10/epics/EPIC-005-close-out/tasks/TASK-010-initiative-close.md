---
id: TASK-010
type: Task
title: "Initiative close: integration, gates, documentation and the SMP hand-off"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-30
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/epic.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/tasks/TASK-001-planning-starter-separation-of-duties.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/tasks/TASK-003-checkout-merge-amplification-budget.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/tasks/TASK-006-workspace-scoped-trusted-cidr.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/tasks/TASK-009-toolchain-and-ci-pinning.md
---

# TASK-010 — Initiative close: integration, gates, documentation and the SMP hand-off

---

## Purpose

This task closes INIT-024. It integrates the final waves, runs the cross-task review and the gates
at the closing commit, reconciles the documentation, names the closing commit SMP pins, and
decides when the artifacts and fixes may be pushed to the public repository.

It is blocked by TASK-001 … TASK-009. It follows INIT-023's close pattern: tasks stay In Progress
until GitHub CI runs green on the pushed closing commit.

## Scope

### In scope

- The integration branch(es), the cross-task review, the gates, and the status lifecycle of every
  INIT-024 task and epic.
- A reconciliation pass over `KNOWN-LIMITATIONS.md`, `security-model.md`,
  `docs/integration-guide.md` and `CHANGELOG.md`, so the shipped behaviour and the docs agree
  after every wave.
- The push decision (initiative owner question 1).

### Out of scope

- New fixes. A defect found at close becomes a task in its owning epic, or a recorded follow-up.

## The close procedure

1. **Integrate.** Merge the task branches through `init/INIT-024-waveN-integration` branches in
   wave order (initiative §7a). Resolve the file overlaps each task declared (initiative §7a
   table). Whichever task landed second rebased, so the integration is a merge, not a rewrite.
2. **Cross-task review.** Two skeptics per P2 (N-1, N-2), as INIT-023 did. Re-run both P2 PoCs
   (`scratch/git/f4/zz_rv_bomb_test.go`, `scratch/standalone/sa2`) against the closing tree and
   confirm neither reproduces.
3. **Gates at the closing commit.**
   - Spine CI (all five jobs), `go vet` with every tag, race on the changed packages,
     `scenario-git-http` on PostgreSQL 18, `git-floor` on git 2.39.5, golangci (no new issues).
   - `govulncheck` clean (TASK-009).
   - codex review on the configured model, two rounds clean.
4. **SMP compatibility at the pair.** Build SMP `main` against the closing commit with
   `replace ../spine`, and run SMP's Spine-runtime and gateway suites (initiative §5). Record the
   SMP commit and the result.
5. **Documentation reconciliation.** Confirm `KNOWN-LIMITATIONS.md`, `security-model.md`,
   `docs/integration-guide.md` and `CHANGELOG.md` match the shipped behaviour across all tasks,
   with no contradiction between tasks' edits.
6. **Name the closing commit** in these Implementation Notes, for SMP's `.spine-revision` pin.
7. **Status.** Set each task and epic to Completed, and the initiative to Completed, once the exit
   criteria (initiative §8) hold. Tasks stay In Progress until GitHub CI has run green on the
   pushed closing commit.

## SMP hand-off

- The closing commit is what SMP EPIC-005 (and SMP EPIC-004's closing bump) pin. TASK-001 is
  already on `main` before the INIT-023 closing commit (initiative owner question 2), so SMP's
  INIT-023 bump carries it.
- The hand-off note records, per Spine task, what SMP must adopt or verify (the "SMP impact"
  section of each task), and which SMP companion task owns it:
  - N-1 byte budget in `CheckTreeDiff` / `boundRunBranchCheckout` / `MergeBound`;
  - N-7 corpus and projection re-run;
  - N-4 history-reader separator;
  - N-8/N-9/N-13 gateway parity;
  - the MVS-raised modules.

## Push decision (initiative owner question 1)

- Nothing containing these artifacts or the N-1/N-2 fixes is pushed to the public repository until
  TASK-003 and TASK-006 are merged on Spine `main` and SMP's N-1 half is on SMP `main`.
- When that holds, the owner is asked to approve the push. Until approval, the branch stays local,
  as INIT-023's 460 unpushed commits do.

## Acceptance Criteria

1. Every INIT-024 task is Completed (or Cancelled with a recorded reason), and every epic is
   Completed.
2. The cross-task review found no new P1 or P2, and both P2 PoCs fail to reproduce at the closing
   commit.
3. Every task's regression tests are committed and pass at the closing commit; each PoC that could
   not be committed has a recorded reason.
4. The gates in step 3 and the SMP compatibility build in step 4 passed, with the SMP commit and
   results recorded.
5. `KNOWN-LIMITATIONS.md`, `security-model.md` and `docs/integration-guide.md` match the shipped
   behaviour.
6. The closing commit is named in these Implementation Notes.
7. The owner questions (initiative §7d and per task) are answered, or their defaults are recorded
   in each affected task.
8. The push decision is recorded with the owner's approval or the reason to hold.

## Out of scope

- New fixes (a defect at close becomes a task or a follow-up).
- The owner-only repository settings (initiative §7d), which are tracked but not performed here.

## File overlaps

- This task edits the shared docs (`KNOWN-LIMITATIONS.md`, `CHANGELOG.md`, `security-model.md`) to
  reconcile what each task wrote. It makes no code change.
