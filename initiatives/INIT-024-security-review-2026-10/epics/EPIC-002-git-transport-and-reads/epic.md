---
id: EPIC-002
type: Epic
title: Git Transport and Reads
status: Pending
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
owner: bszymi
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/initiative.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /architecture/git-integration.md
---

# EPIC-002 — Git Transport and Reads

---

## 1. Purpose

INIT-023 bounded what Spine **reads** from git. It did not bound what git **writes** when Spine
checks out or merges a pushed tree, or what Spine **keeps** when it lists one.

- **N-1 (P2).** A push of a few KB checks out to terabytes. This is the initiative's main
  availability finding.
- **The read path wedges or trusts too much:**
  - a fixed output cap wedges the projection (N-6);
  - a deleted branch name falls back to a tag (N-10);
  - commit messages can forge history records (N-11);
  - an author-chosen diff runs without an allocation limit (N-12).
- **The standalone push gate has three races:**
  - case-folding storage (N-8);
  - the review-freeze window (N-9);
  - one shared git semaphore that a single member can hold (N-13).

---

## 2. Scope

### In scope

- `adapters/git`:
  - `branchwrite.go`, `cli.go`, `commitbinder.go`, `limits.go`, `refgrammar.go`;
  - new `treebudget.go` and `listing.go`.
- `core/artifact/discovery.go` and `adapters/projection` (the streamed-listing callers), and
  `core/engine/output_evidence.go`.
- `service/internal/githttp/handler.go` and the new `spine git-hook pre-receive` subcommand.
- Tests, `KNOWN-LIMITATIONS.md` ("Git read bounds", "Git push gate"),
  `docs/integration-guide.md` and `CHANGELOG.md`.

### Out of scope

- SMP's gateway and `gitread`. SMP's N-1 half is a byte budget in `CheckTreeDiff`. These are SMP
  companion tasks, and TASK-003 gives them `git.MeasureTree` to call.
- A wall-clock deadline on mutating git commands (TASK-003 Decision 3).
- git pack and fsck internals.

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | PoCs → regression tests | SMP impact |
|---|---|---|---|---|---|---|
| TASK-003 | Checkout and merge amplification budget, pre-receive tree limits, SMP-facing `MeasureTree` | N-1 | 2 | — | `scratch/git/f4/zz_rv_bomb_test.go` `TestRV_CheckoutAmplification` | yes: new API; SMP's own half |
| TASK-004 | Read-path robustness | N-6, N-10, N-11, N-12 | 3 (soft: TASK-003) | — | `scratch/secrets-delivery-projection/projection/zz_outputcap_wedge_test.go`; `scratch/git/f4/zz_rv_f10_test.go`; `scratch/git/f4/zz_rv_log_test.go` | yes: streamed listings are shared |
| TASK-005 | Standalone push-gate races | N-8, N-9, N-13 | 3 (soft: TASK-003) | — | `scratch/git/f1` `TestRV_F1_CaseFoldRef`, `TestRV_PushAdmission_FreezeRace` | no (a parity note for SMP's gateway) |
| TASK-011 | Aggregate checkout bound: one writer per branch, bounded in-flight checkout bytes | X-1 (TASK-010 cross-task review, P3 next to N-1) | close-out follow-up (after TASK-003) | — | the cross-task review's concurrent-entry probes → regression tests in `adapters/git` and `core/artifact` (the task's AC 1–2) | no at the bump (the ceiling is library opt-in; a concurrent same-branch entry is refused before writing instead of after); a parity note for SMP's own branch writes |

---

## 4. Acceptance Criteria

- **N-1.**
  - Spine's git client never runs `worktree add`, `switch` or `merge` on a tree that exceeds the
    budget. With receive-pack on, the standalone gateway refuses such a push at pre-receive.
  - The PoC's 8.9 KB pack is refused before any working-tree file is written.
- **N-6.** A tree whose full listing exceeds the per-command output cap, but which holds few
  artifact paths, no longer fails `FullRebuild` or `IncrementalSync`.
- **N-10.** A read of a deleted run branch by its bare name is NotFound, never a same-named
  tag's content.
- **N-11.** No commit message can add, drop or alter a `Log` record.
- **N-12.** The output-evidence diff runs under `GIT_ALLOC_LIMIT` with rename detection off.
- **N-8, N-9, N-13.**
  - The gateway refuses receive-pack on case-folding storage.
  - A push whose step left its write state while the pack was in flight does not update the ref.
  - One principal cannot hold every git slot, and no slot is held while a client stalls the
    command section.
- Every task's SMP compatibility gate passes with no SMP change.
