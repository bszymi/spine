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

---

## Implementation Notes

*(Implementation, 2026-10-31. Branch `init/INIT-024-TASK-011-aggregate-checkout-bound`, cut from Spine local
main `854a1e08` (INIT-024 TASK-001…009 + integration A). Run on macOS, git 2.54.0, go 1.26.5, and on git 2.39.5
in the local `golang:1.26-bookworm` image for the `git-floor` job. Scratch logs: SMP repo
`tmp/init027-orchestration/i24/t011/`. Status stays In Progress until review.)*

### What was built

| Commit | Change |
|---|---|
| `0bc6244a` | This artifact (status In Progress) and its EPIC-002 inventory row. |
| `9d6cf380` | `adapters/git/checkout_admission.go` (new): the per-branch claim (`claimBranch`, key `repoClaimKey` = the canonical git common directory, derived in-process), `ErrBranchInUse`, the process-wide FIFO weighted gate (`inflightGate`, `admitCheckout`), `ErrCheckoutsInFlight` + `*InflightWaitError` in `*GitError{Kind: ErrKindTransient, Message: CheckoutsInFlightMessage}`. `treebudget.go`: `CheckoutLimits.InflightMaxBytes` (zero = no aggregate bound; negative = zero; merged by `over` like the other limits; a ceiling without a budget brings `DefaultTreeBudget`), `TreeLimitInflightBytes`. `branchwrite.go` `EnterBranchWithLimits`: claim after the branch grammar check and before the lookup; the measure keeps its `TotalBytes` as the weight; admission after the measure and before `MkdirTemp`; every error path returns weight and claim after its removal (deferred), the scope's Cleanup (now once-only) after `worktree remove` + `RemoveAll`. `core/artifact` and `core/workflow` `enterBranch`: `ErrCheckoutsInFlight` → `domain.ErrUnavailable` (503); everything else unchanged (`git_error`). `service/cmd/spine/serve_checkout_limits.go`: `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES` (default the effective `MaxTotalBytes`; positive integer; anything else refuses to start), logged at start, a Warn below the per-tree limit. Tests below. |
| `1de494b6` | Docs: `KNOWN-LIMITATIONS.md` (a new, separate "Checkout Budget" entry), `docs/integration-guide.md` (variable row), `CHANGELOG.md` (new TASK-011 section), README variable table, `.env.example`, `architecture/git-integration.md`. |
| `dc82f875` | Tests: every refusal a mutation could turn into a wait runs under a context that ends (fail, never hang). |
| `f4f05352` | The claim key reads only regular git-administrative files (Lstat first; a `.git` link is not followed; a non-regular `commondir` is never opened); `core/worktreefs` scan allowlist entries (found by `go test ./...`: `TestNoUnconfinedWorktreeIO`). |
| (this commit) | Implementation Notes. |

### Decisions

1. **Refuse, not wait, for a held branch** (design point 1 "pick one"). Before this task git refused a second
   checkout of the branch (after writing it); refusing earlier keeps that outcome and its classification for every
   caller, where waiting would silently turn concurrent writes into a serialisation nobody asked for, and would hang
   (until the context ends) a caller that re-enters a branch it already holds. The refusal is an untyped error
   wrapping `ErrBranchInUse`, **not** a `*GitError` — exactly the class of the error it replaces (git's
   "already checked out" was returned as a plain `fmt.Errorf`), so `errors.As(err, *GitError)`, `IsRetryable` and the
   services' `*domain.SpineError` check all decide as before (`git_error`). Proven by
   `TestEnterBranch_BranchInUseKeepsTheRefusalClass` and both services' `TestCreate_CheckoutRefusalsKeepTheirClassification`;
   no SMP code matches the message (grep), and SMP's gate is green.
2. **Claim key = git common directory, derived in-process.** It is what git's own guard is scoped to, so entries
   through a symlinked path, a linked worktree of the repository or a bare clone share one claim
   (`TestEnterBranch_BranchClaimIsPerRepository`, `TestRepoClaimKey_MatchesGitCommonDir` compares with
   `git rev-parse --git-common-dir` for five layouts). Running `git rev-parse` would add a process to every entry and
   change the pinned argv (`TestRefGrammar_ArgvShape` passes unchanged). A layout the derivation does not read keys by
   the canonical repoDir — weaker (git's guard covers the rest, as before), never a false refusal.
3. **The claim is taken before the lookup.** Design point 1 asks for "before the measure and the temporary directory";
   before the lookup as well means a refused entry starts no git process at all (asserted).
4. **The wait is bounded by the mutate deadline too** (design point 2 says "waits under the caller's context").
   An HTTP request's context does not end at the server's write timeout, so a wait could otherwise outlive the
   request's useful life; `spine serve`'s 2-minute `SPINE_GIT_MUTATE_TIMEOUT` caps it, and the same retryable
   refusal results. With no deadline set (the library default) it is the caller's context alone.
5. **FIFO admission.** A waiting tree at the head blocks smaller ones behind it, so a large tree is never starved.
6. **The over-ceiling refusal is `ErrTreeTooLarge`** with the new `TreeLimitInflightBytes` (permanent, `Seen` = the
   measured bytes): it is the same class as a budget refusal (a retry cannot shrink the tree). The wait refusal is the
   distinct, transient `ErrCheckoutsInFlight`, which the artifact and workflow services report as `unavailable` (503)
   — the only service-visible change, reachable only with a ceiling set.
7. **A ceiling without a budget brings the default budget** (`CheckoutLimits` normalisation): the weight is the
   measure, so a ceiling alone would otherwise be a silent no-op.
8. **Weight = `TreeStats.TotalBytes`** (as designed). Filesystem overhead per entry is not weighed (KNOWN-LIMITATIONS).

### Paths that write a tree (design point 3)

| Path | Decision |
|---|---|
| `EnterBranch` / `EnterBranchWithLimits` with a branch (artifact and workflow branch writes; the planning-run start) | **Claim + gate.** A full new tree in a private directory under `os.TempDir()` from entry to Cleanup. |
| `EnterBranch` with an empty branch (in-place scope; its restore to the home branch) | Neither. It rewrites the workspace's own tree in place, already serialised per tree by the tree lock (INIT-023 TASK-012) and measured per tree. |
| `CLIClient.Checkout`, `Merge`, `MergeBound`, `ResetBranch` (`read-tree -u`), the shared tree's restore | Neither: the same in-place, tree-locked, per-tree-measured rewrite of a workspace tree. |
| `CLIClient.Clone` (code-repository clones, shared-mode workspace clones) | Not weighed: it creates a persistent tree under the operator's clone base, not a transient one, so holding a weight only while it writes would not bound what stays on disk; bounded per tree by the budget (KNOWN-LIMITATIONS). |
| `CLIClient.CommitFilesOnto` (private index in `os.TempDir()`) | Not weighed: `read-tree` without `-u` writes an index only, no working-tree files. |

### Acceptance criteria — evidence

"Red" = the test run with `go test -overlay` mapping `branchwrite.go`, `treebudget.go`, `export_test.go`,
`core/artifact/service.go` and `core/workflow/service.go` to their `854a1e08` content, `checkout_admission.go` and
the new-API test file removed, and a compile-only shim declaring `ErrBranchInUse` (`t011/red-overlay.json`,
`t011/shim/`, logs `red-ac1.log`, `red-ac2.log`). "Green" = the branch.

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` (a held scope, 4 concurrent entries, 10 MiB tree, library default and serve budget): **red:** each entry got git's "already used by worktree" after 16 (28 with the budget) git processes, peak under TMPDIR 52,429,609 bytes = 5.0 trees vs the held 10,485,921; **green:** all `ErrBranchInUse`, 0 git processes, peak = the held scope, one `spine-wt-parent-*`, the branch enterable again after Cleanup. `TestEnterBranch_RacingSameBranchEntriesWriteOneTree` (4 entries started together, each holding until all returned): **red:** "4 entries hold the branch at once" (git's guard raced — the P4), 4 worktree adds, peak 4.0 trees; **green:** exactly 1 writer, 1 add, peak ≤ one tree. Both also pass on git 2.39.5. |
| 2 | done | `TestCreate_ConcurrentRunBranchWritesPutOneTreeOnDisk` (core/artifact; 4 concurrent `Create` with a write context on one run branch, 12.5 MiB tree, serve's TASK-003 defaults): **red:** "4 worktree adds for 1 successful writes", peak 52,429,955 = 4.0 trees vs one 13,107,349; **green:** adds = successes (1), peak 13,107,489 (one tree + the artifact written), every success on the branch, every refusal `git_error`. |
| 3 | done | `TestEnterBranch_InflightCeilingBoundsDifferentBranches` (6 branches, ceiling 2.5 trees: at most — and at some point exactly — 2 scopes, gate never above the ceiling, peak under TMPDIR ≤ ceiling, all 6 admitted, gate empty after); `TestEnterBranch_InflightWaitEndsWithRetryableError` (context end and mutate deadline → transient `ErrCheckoutsInFlight` + `context.DeadlineExceeded`, `*InflightWaitError` weight/ceiling, nothing written, a waiter admitted at the holder's Cleanup); `TestEnterBranch_TreeOverTheInflightCeilingIsRefused` (immediate permanent `ErrTreeTooLarge`/`TreeLimitInflightBytes`, `Seen` = measured bytes); `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` (held exactly while open, Cleanup twice, git's refusal of a branch checked out in the main worktree and a failed temporary directory return weight and claim, zero limits never touch the gate); `TestInflightGate_FIFOAndCancellation`; `TestCheckoutLimits_InflightCeiling`; both services' `TestCreate_CheckoutRefusalsKeepTheirClassification` (wait → `unavailable`); serve `TestLoadCheckoutLimits_InflightCeiling`. New API: no red. |
| 4 | done | Zero limits: no gate use (`InflightWeightIsReturnedOnEveryExit`), no extra git process and the same argv (`TestRefGrammar_ArgvShape`, `TestTreeBudget_OffStartsNoProcess` unchanged and green); the only change is the earlier same-branch refusal, same class (Decision 1). SMP gate below. |
| 5 | done | Gates below; mutations below. |
| 6 | done | `KNOWN-LIMITATIONS.md` (new "Concurrent branch-scoped writes are bounded in aggregate" entry — enforced bound, what remains, operator levers; the old residual line is left to the TASK-010 closer), `docs/integration-guide.md`, `CHANGELOG.md` (Security / Changed (operators) / Changed (embedders)), README, `.env.example`, `architecture/git-integration.md`. |

### Mutations (each caught by a named test; `t011/mut/`, overlays)

| Mutant | Caught by |
|---|---|
| MC1 no claim | `SameBranchEntriesAreRefusedBeforeWriting`, `RacingSameBranchEntriesWriteOneTree`, `BranchClaimIsPerRepository`, `BranchInUseKeepsTheRefusalClass`, `ConcurrentRunBranchWritesPutOneTreeOnDisk`, `CheckoutRefusalsKeepTheirClassification` |
| MC2 claim released when EnterBranch returns (not at Cleanup) | `SameBranchEntriesAreRefusedBeforeWriting`, `BranchClaimIsPerRepository`, `BranchInUseKeepsTheRefusalClass`, `CheckoutRefusalsKeepTheirClassification` |
| MC3 claim taken after the temporary directory | `SameBranchEntriesAreRefusedBeforeWriting` (refused entries ran git), `CheckoutRefusalsKeepTheirClassification` |
| MC4 key = repoDir (not the common directory) | `BranchClaimIsPerRepository` (linked worktree), `RepoClaimKey_MatchesGitCommonDir` |
| MC5 key ignores the branch | `BranchClaimIsPerRepository`, `InflightCeilingBoundsDifferentBranches`, `InflightWaitEndsWithRetryableError`, `CheckoutRefusalsKeepTheirClassification` |
| MH1 a non-regular `commondir` opened | `RepoClaimKey_ReadsOnlyRegularAdministrativeFiles` (blocked on a FIFO) |
| MH2 a `.git` link followed | `RepoClaimKey_ReadsOnlyRegularAdministrativeFiles` |
| MS1 the gate never waits | `InflightCeilingBoundsDifferentBranches`, `InflightWaitEndsWithRetryableError`, `InflightGate_FIFOAndCancellation`, `InflightWeightIsReturnedOnEveryExit`, `TreeOverTheInflightCeilingIsRefused`, `CheckoutRefusalsKeepTheirClassification` |
| MS2 weight released when EnterBranch returns | `InflightCeilingBoundsDifferentBranches`, `InflightWaitEndsWithRetryableError`, `InflightWeightIsReturnedOnEveryExit`, `CheckoutRefusalsKeepTheirClassification` |
| MS3 no over-ceiling refusal | `TreeOverTheInflightCeilingIsRefused`, `InflightGate_FIFOAndCancellation` |
| MS4 a cancelled waiter left queued | `InflightWaitEndsWithRetryableError` (first run did not compile — `elem` unused — fixed and rerun) |
| MS5 no FIFO (a fitting later waiter passes the head) | `InflightGate_FIFOAndCancellation` |
| MS6 weight kept on error paths | `InflightWeightIsReturnedOnEveryExit` |
| MS7 no wake-up after a cancelled head | `InflightGate_FIFOAndCancellation` |
| MS8 wait not bounded by the mutate deadline | `InflightWaitEndsWithRetryableError` |
| MS9 / MS9b the services map the wait refusal to `git_error` | `TestCreate_CheckoutRefusalsKeepTheirClassification` (core/artifact / core/workflow) |
| MS10 serve has no default ceiling | `TestLoadCheckoutLimits_InflightCeiling` |
| MS11 a done context acquires with room | `InflightGate_FIFOAndCancellation` |
| MS12 a ceiling without a budget ignored | `TestCheckoutLimits_InflightCeiling`, `InflightWeightIsReturnedOnEveryExit`, `CheckoutRefusalsKeepTheirClassification` |

### Gates (all at `f4f05352`, the final code commit; only this notes commit follows)

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | rc=0 |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | rc=0; rc=0 |
| `go test -p 4 ./...` (heavy lock) | rc=0. An earlier run at `dc82f875` failed only `core/worktreefs TestNoUnconfinedWorktreeIO` (the claim key's reads were not allowlisted) — fixed in `f4f05352` |
| `go vet ./...` plain / `-tags integration` / `-tags scenario`; `GOOS=linux` and `GOOS=windows` vet of the touched packages | rc=0 / 0 / 0; 0 / 0 |
| `-race` on the touched packages (`adapters/git/...`, `core/artifact/...`, `core/workflow/...`, `core/worktreefs/...`, `service/cmd/spine/...`) | rc=0, no data race |
| `-tags integration` suites of `adapters/git/...`, `adapters/gitpool/...`, `core/artifact/...`, `core/workflow/...`, `service/cmd/spine/...`, `service/internal/githttp/...` (own DB `s24011_ci_final`, dropped) | rc=0 |
| `scenario-git-http` job, every step: `TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*` / workflow-policy / step CAS + evidence / status lifecycle (scenario + integration) / governance (scenario + store) / multi-scheduler | rc=0 for all; 90 / 26 / 154 / 1 / 12 + 24 / 37 + 1 / 19 + 1 / 1 passes; 0 skips |
| `git-floor` (`golang:1.26-bookworm`, git 2.39.5, `--pull=never`, read-only mounts, the module-cache go1.26.10 linux toolchain copied into the tmpfs): both CI steps + verbose TASK-011 tests | STEP1 rc=0, STEP2 rc=0; every TASK-011 test and the existing `TestEnterBranch_*` PASS on 2.39.5 (AC 2 there: 1 of 4 writers, peak 13,107,437 = one tree + the artifact) |
| golangci-lint v2.11.4 `--new-from-rev=854a1e08` on the touched packages | 0 issues (the full run's remaining hits are all pre-existing lines; one errcheck fewer than the base: Cleanup's unchecked `RemoveAll` is gone) |
| `--enable-only=gosec` | 0 issues |
| `gofmt -l` on changed files | clean |

### Codex (`codex review --base 854a1e08`, plain; model `gpt-6.1-sol` confirmed in each review sub-session)

| Round | At | Review session | Findings | Disposition |
|---|---|---|---|---|
| 1 | `dc82f875` | `01a18fd0-786c-7003-9ecc-4f26398487c1` | none ("No actionable regressions … branch claims, admission queue, cancellation handling, and service error mappings appear consistent") | — |
| 2 | `f4f05352` | `01a18fec-6b49-7803-97a2-3854de8da2ac` | none ("No actionable regressions … branch claims, checkout admission gate, configuration, or error mapping") | — clean on the final code |

### SMP compatibility gate

`smp` (SMP main `21cc2e8a`, `../spine` = this worktree at `f4f05352`, left unmodified): `go build ./...` rc=0;
`go vet` plain / integration / `-tags scenario ./scenariotest/...` / faultinject rc=0; `go test -race -p 4` over the
initiative §5 packages plus every SMP importer of `adapters/git`, `core/artifact` or `core/workflow` (`.`,
`cmd/customer-app`, `internal/customer/{githttp,gitread,executionreadiness,proposals,projectionwriter,spinecore,
spineembedded,stepevents,workspace/...,run}`, `internal/engine/...`) rc=0, no failure; `dev/testgate` Spine-runtime leg
PASS (2,463 passed, 0 failed, 3 allowed skips) and githttp leg PASS (590 passed, 1 allowed skip); platform / spine-runtime
migrations applied to `s24011_*` databases (dropped); no `ws_*` database or role before or after. No SMP change.

**Why nothing changes for SMP:** SMP never reaches `EnterBranch` with a branch in production — every embedded writer
(`BuildArtifactService`, the planning writer, `WorkflowWriter`) roots `artifact.Service` / `workflow.Service` at its
own scope worktree and strips the write-context branch, so writes are in place — and it sets no ceiling, so the gate
is never used.

**Parity note for SMP TASK-018 (SMP's own branch writes and gateway).** SMP's `gitclone.Pool.EnterWriteScope` already
holds one write scope per (workspace, repo) clone (a per-clone mutex that waits — stronger than Spine's per-branch
claim) and measures the branch's tree before `git worktree add` (INIT-027 EPIC-005 TASK-011). What it lacks is the
aggregate: scopes of different clones (workspaces x repositories) each put a within-budget tree under
`SMP_GIT_CLONE_ROOT/.../.worktrees` at once. The parity fix is a process-wide FIFO weighted gate on the measured
`TotalBytes` SMP already computes, taken after the measure and before the add and released after
`WriteScope.Cleanup` removed the worktree, with a ceiling knob mirroring `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`
(suggested `SMP_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`, default the per-tree byte limit, a wait that ends returns 503). The
same applies to any other SMP path that materialises a run branch on a shared disk (the `/git/{workspace}` gateway's
receive side does not check out). Spine's gate is internal to `adapters/git` (reachable only through
`EnterBranchWithLimits`); exporting it for SMP would be a separate, additive follow-up.

### Deviations

1. **Claim before the lookup** (the design said before the measure and the temporary directory): a refused entry runs
   no git process at all. Decision 3.
2. **The wait is also bounded by the mutate deadline** (design: the caller's context). Decision 4.
3. **The wait refusal is `unavailable` (503) through the services**, the over-ceiling refusal `ErrTreeTooLarge`; the
   design named only "a distinct, retryable error" and "refused". Decision 6.
4. **A ceiling without a budget brings the default budget.** Decision 7.
5. **Beyond the design:** `WriteScope.Cleanup` of a branch scope runs once; the claim key's reads were hardened
   (Lstat, regular files only) after `go test ./...` flagged them, with the `core/worktreefs` scan allowlist updated
   (as TASK-003 did); README, `.env.example` and `architecture/git-integration.md` updated alongside the three docs AC 6
   names.
6. **KNOWN-LIMITATIONS:** the new entry is a separate bullet at the end of "Checkout Budget"; the old residual sentence
   ("concurrent worktrees of different branches each get the full budget") is left for the TASK-010 closer to
   reconcile, as instructed, and is now superseded by the new entry.

### Not run

- The full `make test-scenario` with a registry database (only the CI job's scenario groups; known pre-existing
  failures).
- GitHub CI (nothing is pushed).
- Trees larger than ~60 MiB on the host (disk safety); the fixtures measure the bound in multiples of a 10-13 MiB tree.
