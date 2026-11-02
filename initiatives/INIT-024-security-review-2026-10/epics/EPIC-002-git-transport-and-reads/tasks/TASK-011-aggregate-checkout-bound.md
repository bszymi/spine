---
id: TASK-011
type: Task
title: "Aggregate checkout bound: one writer per branch, bounded in-flight checkout bytes"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-31
last_updated: 2026-11-02
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

### Round 1 review fixes (2026-10-31 … 2026-11-02)

*(Review of `854a1e08..e92b45c4`: 0 P1/P2, 2 P3, 5 P4 — the same-branch defect closed and the ceiling held across
branches. Codex round 3 ran on the round-1 code. Status stays In Progress.)* This section supersedes Decision 5
(one FIFO for the process), Decision 7 (a ceiling alone brings the default budget everywhere), Decision 8 (weight =
`TotalBytes`) and the serve default in `9d6cf380`'s row (the per-tree byte limit) above.

| Commit | Change |
|---|---|
| `0faef71e` | Round-1 code and tests (P3-1, P3-2, P4-1…P4-4 below). |
| `5cfe16dd` | Round-1 docs: KNOWN-LIMITATIONS (the per-entry weight, the default ceiling's derivation, fair admission and the half share per workspace, what one workspace can still do to another, operator levers), the integration guide's variable row, CHANGELOG, README, `.env.example`, `architecture/git-integration.md`. |
| `a5225cb3` | Codex round 3 fixes and tests (P1, P2 below). |
| `180764d7` | Codex round 3 docs: KNOWN-LIMITATIONS, CHANGELOG (`ErrBranchMoved`), the integration guide's variable row. |
| `e4eba029` | Tests only: every entry and admission that can wait in the process-wide gate runs under `boundedCtx(t, d)` (10 s; 30 s for the six-repository and entries tests), a refusal that unexpectedly succeeds is released before the test fails, holder scopes also go to Cleanup with a bounded wait (`cleanupAtEnd`), and the two service tests stop waiting for the holder's deferred Cleanup after 10 s — so a defect fails a named test instead of hanging the binary until `-timeout` (four round-1 mutants hung a test until that timeout; see the mutation table). |
| (this commit) | These notes. |

#### Review dispositions

| ID | Defect | Disposition |
|---|---|---|
| P3-1 | One process-wide FIFO and a default ceiling equal to the per-tree byte limit: one workspace's maximal tree filled the ceiling, and a waiter at the head blocked every other repository's write that fitted, until those were refused at their deadline. | **Fixed.** One FIFO queue per repository (keyed by the claim key), the queues served round-robin; an arrival waits only behind its own repository's queue; one repository holds at most half the ceiling (a single heavier tree only while its repository holds nothing). `spine serve`'s default ceiling = max(2 × `SPINE_GIT_TREE_MAX_BYTES`, one maximal tree's weight) = 4 GiB with the defaults; a Warn at start when a ceiling is below a maximal tree's weight. **Documented residual** (KNOWN-LIMITATIONS): no reservation — a large waiting tree can lose freed room to a steady stream of other repositories' smaller writes until its deadline (503). Tests: `TestInflightGate_OneRepositoryNeverBlocksAnother` and `TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories` (both red at `e92b45c4`), `TestInflightGate_HalfTheCeilingPerRepository`, `_RoundRobinAcrossRepositories`, `_FIFOWithinARepositoryAndCancellation`, `_ForgetsIdleRepositories`, `TestLoadCheckoutLimits_InflightCeiling`. |
| P3-2 | The weight was the blob bytes only: trees of directories and empty files weighed 0, so any number of them was admitted (inode exhaustion of the shared temporary directory). | **Fixed.** Weight = bytes + `git.InflightEntryWeight` (4096) × `Entries` (`git.InflightWeight(TreeStats)`, saturating; `TreeBudget.MaxInflightWeight()` = the heaviest tree a budget admits); the over-ceiling refusal uses the same weight. `Entries` also counts gitlinks (a superset of what is written). Tests: `TestEnterBranch_EntriesAreWeighed` (red at `e92b45c4`), `TestInflightWeight_CountsEntries`. |
| P4-1 | The release-after-removal order was untested (a release-first mutant survived). | **Fixed.** A test hook at the release (`SetScopeReleaseHookForTest`); `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved` asserts the private parent is gone and git no longer lists the worktree on Cleanup, a failure after the add, and a failure before anything was created; it kills the release-first mutant (F07). |
| P4-2 | The claim key depended on the path's spelling (a case variant on case-insensitive storage keyed differently; a subdirectory or a symlinked `.git` fell back to its own path). Not request-reachable. | **Fixed.** Key = the git common directory's (device, inode) (`checkout_identity_unix.go`; the canonical path elsewhere and as the fallback), the repository found upward from the given directory as git does; a `.git` symbolic link is followed for the identity, and a file is opened only when what it names is a regular file. `TestEnterBranch_BranchClaimIsPerRepository` and `TestRepoClaimKey_MatchesGitCommonDir` add the subdirectory, symlinked `.git` and case-variant (APFS) spellings. |
| P4-3 | `artifact.Service` pushed inside the branch scope, holding the claim and the weight across an unbounded network push. | **Fixed for branch scopes:** the branch is read inside the scope, the scope released, then the branch pushed (failures still only logged). In-place writes still push inside their scope, as before. `core/workflow` does not push. Test: `TestCreate_PushStartsAfterTheBranchScopeIsReleased` (red at `e92b45c4`). |
| P4-4 | A ceiling alone brought the default budget to every client in the process (`Checkout`/`Merge`/`Clone` of a repository without the neutral attributes line were then refused). | **Fixed.** The implied budget applies only inside `EnterBranchWithLimits` (to weigh its tree); the process default and every client keep exactly the limits they were given. `TestCheckoutLimits_InflightCeiling` (a ceiling-only default leaves `DefaultCheckoutLimits`/`CLIClient.CheckoutLimits` without a budget and a plain repository's `Checkout` succeeds). |
| P4-5 | The stale `KNOWN-LIMITATIONS.md` residual sentence. | **Left to the TASK-010 closer**, as instructed (the TASK-011 entry supersedes it). |

#### Codex round 3 (the last round; `codex review --base 854a1e08` at `5cfe16dd`, session `01a19022-ee81-7672-91cc-5704db3bb695`, model `gpt-6.1-sol`)

| Finding | Fix (`a5225cb3`) |
|---|---|
| **P1** A branch that moved while its entry waited for room (a push) was checked out at its new tip while the gate held the old tree's weight, so the ceiling could be exceeded. | With a ceiling, the tip is re-read after admission; when it moved, the weight is returned and the new tip measured and admitted again, at most `branchMovedAttempts` (3) times, then the retryable `git.ErrBranchMoved` (it also matches `ErrCheckoutsInFlight`, so API callers get `unavailable`, 503). After the checkout, `HEAD` must be the weighed commit; a move in between removes the worktree and refuses the same way. Tests: `TestEnterBranch_BranchMovedWhileWaitingIsReweighed` (red at `5cfe16dd`), `_BranchMovedDuringCheckoutIsRefused`, `_BranchThatKeepsMovingIsRefused`. |
| **P2** The round-robin moved every repository it visited to the back, restoring the original order after each pass, so one repository's backlog could take each freed slot before another repository waiting for the same room. | A repository whose head does not fit keeps its place; only a served one goes to the back, so freed room goes first to the repository that has waited longest since it was last served. Test: `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` (red at `5cfe16dd`). |

**The two codex-round-3 fixes (`a5225cb3`, `180764d7`) and the test-bounding commit `e4eba029` are NOT
codex-reviewed** (the codex budget for the task is spent); they go to an independent delta review.

#### Red / green

"Red" = the head tests run with `go test -overlay` mapping the production files to the earlier revision.

| Regression | Red | Green |
|---|---|---|
| `TestInflightGate_OneRepositoryNeverBlocksAnother` | at `e92b45c4` (`t011/red-r1/`, with a compile shim for the new test API): C's 10-byte admission waited 2 s behind B's waiting 60 and was refused (`ErrCheckoutsInFlight`) | PASS |
| `TestEnterBranch_EntriesAreWeighed` | at `e92b45c4`: 3 trees of 3,001 entries in flight under a 2.5-tree ceiling, peak 9,013 filesystem entries, a tree weighing twice the ceiling admitted | PASS |
| `TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories` | at `e92b45c4`: another repository's 10-byte write beside one maximal tree waited 3 s and was refused (8 MiB ceiling) | PASS |
| `TestCreate_PushStartsAfterTheBranchScopeIsReleased` | at `e92b45c4`: the push started while the worktree was on disk and the branch still claimed | PASS |
| `TestEnterBranch_BranchMovedWhileWaitingIsReweighed` | at `5cfe16dd` (`t011/red-c3/`): the gate held 4,102 for a scope that checked out the moved tip weighing 10,653,696 | PASS |
| `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` | at `5cfe16dd`: a3 took the freed slot before b1 | PASS |

The bounded tests (`e4eba029`) were run again against both red bases (`red-r1/overlay-bounded.json` — the round-1
overlay plus the codex-round-3 test file left out — and `red-c3/overlay.json`; logs `red-r1/red-bounded.log`,
`red-c3/red-bounded.log`): all six still fail at their base with the same messages, so bounding the waits did not
weaken them.

#### Mutations (round 1; `t011/mut-r1/`, overlays, re-run on `e4eba029` with `mut-r1/run2.sh`, logs `mut-r1/r2/`)

33 mutants. The first round-1 run reached 25 of them before it was stopped during S04: 21 were killed by named
tests; F03, F13, K02 and S03 each hung a test until the 5-minute binary timeout (F03 named no failing test at all;
the other three also failed named tests, then a leaked scope or a locked gate hung a later test); S04–S11 were not
run. All 33 are now killed by named tests: 0 timeouts, 0 build errors, no survivor. F13 (a cancelled last waiter left in the turn
list) makes the admission pass dereference an empty queue and panic while holding the gate's lock: it is reported
under the failing test's name in `adapters/git`, `core/artifact` and `core/workflow`. Before `e4eba029` the two
service tests deadlocked in their deferred Cleanup on that lock, so only the binary timeout reported them.

| Mutant | Killed by (FAIL by name; 0 timeouts, 0 build errors) |
|---|---|
| C01 no branch claim | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestCreate_ConcurrentRunBranchWritesPutOneTreeOnDisk`, `TestEnterBranch_BranchClaimIsPerRepository`, `TestEnterBranch_BranchInUseKeepsTheRefusalClass`, `TestEnterBranch_RacingSameBranchEntriesWriteOneTree`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| C02 claim released when EnterBranch returns (not at Cleanup) | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchClaimIsPerRepository`, `TestEnterBranch_BranchInUseKeepsTheRefusalClass`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| C03 claim key ignores the branch | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchClaimIsPerRepository` |
| C04 a non-regular administrative file opened (no regular-file check) | `TestRepoClaimKey_ReadsOnlyRegularAdministrativeFiles` |
| C05 claim taken after the temporary directory | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| F01 no per-repository share (fits ignores the half ceiling) | `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F02 per-repository share = the whole ceiling | `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F03 one global FIFO (an arrival waits whenever any repository has waiters) | `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother` |
| F04 a served repository is not moved to the back (drain one repository first) | `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| F05 weight = bytes only (no entry cost) | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightWeight_CountsEntries` |
| F06 serve default ceiling = the per-tree byte limit | `TestLoadCheckoutLimits_InflightCeiling`, `TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories` |
| F07 weight and claim released before the worktree is removed | `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved` |
| F08 claim key by path (no device/inode identity) | `TestEnterBranch_BranchClaimIsPerRepository` |
| F09 no upward discovery of the repository | `TestEnterBranch_BranchClaimIsPerRepository`, `TestRepoClaimKey_MatchesGitCommonDir` |
| F10 artifact push inside the branch scope | `TestCreate_PushStartsAfterTheBranchScopeIsReleased` |
| F11 a ceiling alone brings the default budget process-wide | `TestCheckoutLimits_InflightCeiling` |
| F12 idle repositories never forgotten | `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F13 a cancelled last waiter leaves its repository in the turn list | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_InflightWaitEndsWithRetryableError` — the panic (nil head in admitLocked, under the gate lock) is reported under the test's name in all three packages; the bounded Cleanup reports the stuck gate instead of deadlocking |
| F14 MaxInflightWeight without the entry cost | `TestInflightWeight_CountsEntries`, `TestLoadCheckoutLimits_InflightCeiling` |
| K01 no tip re-read after admission (codex P1) | `TestEnterBranch_BranchMovedWhileWaitingIsReweighed` |
| K02 no HEAD check after switch (codex P1) | `TestEnterBranch_BranchMovedDuringCheckoutIsRefused` |
| K03 a repository whose head does not fit is rotated too (codex P2) | `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S01 the gate never waits | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S02 weight released when EnterBranch returns | `TestCheckoutLimits_InflightCeiling`, `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` |
| S03 no over-ceiling refusal | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S04 a cancelled waiter left queued | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S05 weight kept on error paths (only the claim released) | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S06 wait not bounded by the mutate deadline | `TestEnterBranch_InflightWaitEndsWithRetryableError` |
| S07 a done context acquires with room | `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S08 no admission pass after a cancelled waiter | `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S09 artifact service maps the wait refusal to git_error | `TestCreate_CheckoutRefusalsKeepTheirClassification` |
| S10 workflow service maps the wait refusal to git_error | `TestCreate_CheckoutRefusalsKeepTheirClassification` |
| S11 no implied budget inside EnterBranch (a ceiling alone weighs nothing) | `TestCheckoutLimits_InflightCeiling`, `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` |

#### Gates (final code `e4eba029`; only this notes commit follows)

Logs: `t011/chain1.out`, `basic-final/`, `ci-final/`, `light-final/`, `linux-floor.log`.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | rc=0; rc=0; rc=0 |
| `go test -p 4 ./...` (heavy lock) | rc=0 (41 packages ok, no failure) |
| `go vet ./...` plain / `-tags integration` / `-tags scenario`; `GOOS=linux` and `GOOS=windows` vet of the touched packages | rc=0 / 0 / 0; 0 / 0 |
| `-race -p 4` on `adapters/git/...`, `core/artifact/...`, `core/workflow/...`, `core/worktreefs/...`, `service/cmd/spine/...` | rc=0, no data race |
| `-tags integration` suites of `adapters/git/...`, `adapters/gitpool/...`, `core/artifact/...`, `core/workflow/...`, `service/cmd/spine/...`, `service/internal/githttp/...` (own DB `s24011_ci_final`, dropped) | rc=0 (7 packages ok) |
| `scenario-git-http` job, every step (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*` / workflow-policy / step CAS + evidence / status lifecycle scenario + integration / governance scenario + store / multi-scheduler) | rc=0 for all; 90 / 26 / 154 / 1 / 12 + 24 / 37 + 1 / 19 + 1 / 1 passes; 0 skips |
| `git-floor` (`golang:1.26-bookworm`, git 2.39.5, `--pull=never`, read-only mounts, go1.26.10 toolchain copied into the tmpfs; container removed) | STEP1 rc=0, STEP2 rc=0; every TASK-011 test PASS on 2.39.5 (AC 2: 1 of 4 writers, peak 13,107,437 = one tree + the artifact) |
| golangci-lint v2.11.4 `--new-from-rev=854a1e08` on the touched packages; `--enable-only=gosec` | 0 issues; 0 issues |
| `gofmt -l` on the changed files | clean |

#### SMP compatibility gate

`smp` (SMP main `21cc2e8a`, `../spine` = this worktree at `e4eba029`, left unmodified), daytime run 2026-11-02
(`t011/smp-day.out`, `t011/smp/`): `go build ./...` rc=0; `go vet` plain / integration / `-tags scenario
./scenariotest/...` / faultinject rc=0; `go test -race -p 4` over the initiative §5 packages plus every SMP importer
of `adapters/git`, `core/artifact` or `core/workflow` rc=0 (15 packages ok); `dev/testgate` Spine-runtime leg PASS
(2,463 passed, 0 failed, 3 allowed skips) and githttp leg PASS (590 passed, 1 allowed skip); platform / spine-runtime
migrations applied to `s24011_*` databases (dropped); no `ws_*` database or role before or after. No SMP change.

**An earlier run of the same gate failed three timing tests, none caused by this task.** The first run (in the gate
chain, `t011/smp-overnight/`) ran from 2026-11-01 14:13 to 2026-11-02 09:40 while the host went through repeated
sleep / dark-wake cycles (`pmset -g log`). It failed `internal/customer/gitread`
`TestBatch_IdleAndCloseStopChildren` (an idle-timer restart order) in the `-race` leg, and
`internal/customer/spineembedded` `TestReconcileWaiting_Integration/inside_grace` (a 10-second grace window seen as
elapsed) and `internal/customer/executionreadiness` `TestReadinessService_Integration` ("no runner is online": a
runner's liveness seen as stale) in the runtime leg. None of them reaches `EnterBranch` with a branch or sets a
ceiling. Re-run standalone under the heavy lock with the gate's flags (`t011/smp-rerun.sh`, `smp-rerun.out`), all
three pass both at this head and at `854a1e08` (every Spine production file the branch changes mapped to its
`854a1e08` content by `go test -overlay`, `t011/smp-base/overlay.json`): 1,101 passed, 0 failed for the two runtime
packages, and `TestBatch_IdleAndCloseStopChildren` PASS in `gitread -race`, at each revision. The standalone testgate
exit is 1 at both revisions only because the gate's `-allow-skip` for `spinecore`'s Linux-only skips matched nothing
(`spinecore` was not in the standalone set). The daytime run above then passed in full.

#### Not run (round 1)

- Codex on `a5225cb3`, `180764d7` and `e4eba029` (the task's codex budget is spent): they go to an independent delta
  review.
- The full `make test-scenario` with a registry database, GitHub CI (nothing is pushed), and the tests on Windows
  (vet only).

### Round 2 — delta review fixes (2026-11-02)

*(Delta review of `e92b45c4..7064711a`: 0 P1/P2, 2 P3, 3 P4. Status stays In Progress. The round-1 gate logs are
archived under `t011/round1/`; this round's are at `t011/`.)* This round supersedes, above: the round-1 serve default
(max(2 × the byte limit, one maximal tree)), its "below" warning, the per-attempt wait deadline, and the attach by
`switch` under a ceiling.

| Commit | Change |
|---|---|
| `db8545c2` | Code and tests: the attach without a checkout and the other-worktree check (P3-A), the default ceiling (P3-B), one wait deadline (P4-1). |
| `43bcc31b` | Docs (P4-2): KNOWN-LIMITATIONS, CHANGELOG, the integration guide's variable row, README, `.env.example`, `architecture/git-integration.md`. |
| `71db73ca` | Test only: the other-worktree check fails closed (a round-2 mutant, N06, survived without it). |
| (this commit) | These notes. |

#### Dispositions

| ID | Defect | Disposition |
|---|---|---|
| P3-A | Under a ceiling the worktree was filled at the weighed commit and then attached with `switch`, which checks the branch's current tip out: a branch moved after the re-read was written (a tree nobody weighed: the reviewer measured 3.0× the gate's weight on disk) before the post-condition refused it. | **Fixed.** Under a ceiling the worktree is attached with `git symbolic-ref HEAD refs/heads/<n>` (writes only the worktree's HEAD file); then `git worktree list --porcelain -z` — a branch another worktree holds is refused with an untyped error matching `ErrBranchInUse` (git_error, the class of git's refusal; the message names no path), the worktree removed; then the post-condition and `HEAD` = the weighed commit, else the worktree removed and `ErrBranchMoved`. The moved tip is never written. A failed listing refuses the entry (fail-closed). Without a ceiling the attach is the `switch` it was (same argv: `TestRefGrammar_ArgvShape` unchanged). Tests: `TestEnterBranch_MoveAfterTheReReadIsNeverWritten` (the reviewer's probe, ported with a deterministic mover; red at `7064711a`), `TestEnterBranch_CeilingAttachesWithoutACheckout` (no `switch`; on the branch, clean, a commit moves the branch; budget-only and library-default modes keep `switch`), `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_UnreadableWorktreeListRefusesTheEntry`, `TestCreate_UnderACeilingTheWriteIsCommittedAndPushedOnTheBranch` (core/artifact: commit on the run branch, parent = the previous tip, pushed to origin). `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` was updated deliberately: a branch checked out in the main worktree is now refused, under its ceiling, by the other-worktree check (ErrBranchInUse) instead of git's `switch`. |
| P3-B | The serve default max(2 × bytes, one maximal tree's weight) equalled one maximal tree when the entry allowance dominated, so one entry-heavy tree within the budget filled the ceiling (its repository held nothing, so the half share did not apply); no warning. | **Fixed.** Default = 2 × `MaxInflightWeight()` (saturating; the requested max(2 × `MaxInflightWeight()`, 2 × the byte limit) is the same value, the weight being at least the bytes): 5,933,367,296 bytes with the defaults (was 4 GiB), so a maximal tree always fits within its repository's half. The startup warning fires at a ceiling **at or** below a maximal tree's weight. `TestLoadCheckoutLimits_InflightCeiling` updated deliberately (new defaults; an equal ceiling warns, one byte above does not). Test: `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories` (the reviewer's probe; red at `7064711a`). |
| P4-1 | The wait deadline was per attempt inside the three-attempt re-weigh loop: up to 3 × `SPINE_GIT_MUTATE_TIMEOUT` holding the claim. | **Fixed.** One admission context — the caller's, capped by a single mutate deadline taken before the loop — bounds every wait (`admitCheckout` no longer applies a deadline of its own). The measures and lookups run under the caller's context, as before. Test: `TestEnterBranch_ReweighWaitsShareOneDeadline` (another repository takes the room back for 1 s each time it frees; with a 1.5 s deadline the second wait ends at the deadline; red at `7064711a`: `ErrBranchMoved` after 3.05 s). |
| P4-2 | Stale KNOWN-LIMITATIONS claims ("the tree checked out is the tree weighed"; "half the ceiling (or one maximal tree) … others share the rest"). | **Fixed** with P3-A / P3-B: the tree written is the tree weighed (filled at the weighed commit, attached without a second checkout, a moved branch refused without its tree written); a maximal tree fits within a half with the default, one heavier than half only with a lower ceiling; the other-worktree check and what it does not see. |
| P4-3 | Starvation of a tree heavier than half by multi-repository streams. | **No change**: the documented "no reservation" residual stays. |

#### Decisions and deviations

1. **The attach without a checkout applies only under a ceiling.** It is where the weight must match the tree; with
   zero limits (the library default, AC 4) and with a budget alone the attach stays `switch`, byte-for-byte the
   argv embedders had. A budget alone keeps TASK-003's behaviour: a branch moved between the measure and `switch`
   is checked out unmeasured (`spine serve` always has a ceiling, so it is not exposed).
2. **The other-worktree check runs after the attach, not before** (the review said "before attaching"). Every
   holder that existed before is still seen, and so is one that attaches between the check and the attach, which a
   check before could miss (git's own guard, checked before it attaches, has that window). Attaching first writes
   only the scope's own HEAD file; a refusal removes the worktree.
3. **The refusal for another worktree is a new message** ("checked out in another worktree of the repository",
   matching `ErrBranchInUse`), where git's named the other worktree's path; it names no path. `git worktree list`
   does not show a worktree in the middle of a rebase or bisect of the branch, which git's guard does (documented).
4. **The listing is `--porcelain -z`** (git ≥ 2.36; the floor is 2.39.5), so no worktree path can forge a "branch"
   line; the scope's own worktree is told apart by `os.SameFile`. The two `os.Stat` calls are allowlisted in the
   `core/worktreefs` scan.

#### Red / green

Red = the round-2 tests run against `7064711a`'s production code (`t011/red-r2/`: `red-p3a.log` and `red-p3b.log` at
the unmodified head; `red-git.log` and `red-serve.log` with `overlay.json` mapping `branchwrite.go`,
`checkout_admission.go`, `export_test.go`, `checkout_admission_test.go`, `serve_checkout_limits.go` and its test to
`7064711a`).

| Test | Red at `7064711a` | Green |
|---|---|---|
| `TestEnterBranch_MoveAfterTheReReadIsNeverWritten` (both modes) | the worktree held 41,943,040 bytes after a git command against the 10,653,696 weighed; peak under TMPDIR 41,943,198 (3.9×) | PASS |
| `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories` | the 10-byte write waited 3 s and was refused under the 5,144,576-byte default (one maximal tree) | PASS |
| `TestEnterBranch_ReweighWaitsShareOneDeadline` | `ErrBranchMoved` after 3.05 s (three waits) | PASS |
| `TestEnterBranch_CeilingAttachesWithoutACheckout` | `switch` ran, no `symbolic-ref HEAD`, no listing (ceiling modes; the no-ceiling modes pass at both) | PASS |
| `TestEnterBranch_BranchInAnotherWorktreeIsRefused` | git's own refusal, naming the other worktree's path | PASS |

#### Mutations (`t011/mut-r2/`, `run3.sh`, logs `mut-r2/r3/`, `summary-final.txt`)

43 mutants: the 33 of round 1, 29 ported onto the round-2 code with `patch` and 4 re-made by hand where round 2 moved
their code (F06, K02, S03, S06; `make_r2.py`), plus N01–N10 for the new code (`make_r2.py`). All compile. First run:
42 killed by named tests, 0 timeouts, 0 build errors; **N06 (a failed listing read as "no holder") survived** →
`71db73ca` added `TestEnterBranch_UnreadableWorktreeListRefusesTheEntry`, and N06 was then killed. Final: 43 of 43
killed by name.

| Mutant | Killed by (FAIL by name) |
|---|---|
| C01 no branch claim | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestCreate_ConcurrentRunBranchWritesPutOneTreeOnDisk`, `TestEnterBranch_BranchClaimIsPerRepository`, `TestEnterBranch_BranchInUseKeepsTheRefusalClass`, `TestEnterBranch_RacingSameBranchEntriesWriteOneTree`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| C02 claim released when EnterBranch returns | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchClaimIsPerRepository`, `TestEnterBranch_BranchInUseKeepsTheRefusalClass`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| C03 claim key ignores the branch | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchClaimIsPerRepository` |
| C04 a non-regular administrative file opened | `TestRepoClaimKey_ReadsOnlyRegularAdministrativeFiles` |
| C05 claim taken after the temporary directory | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting` |
| F01 no per-repository share | `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F02 per-repository share = the whole ceiling | `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F03 one global FIFO | `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother` |
| F04 a served repository not moved to the back | `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| F05 weight = bytes only | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestEnterBranch_ReweighWaitsShareOneDeadline`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightWeight_CountsEntries` |
| F06 serve default = the per-tree byte limit (re-made) | `TestLoadCheckoutLimits_InflightCeiling`, `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories`, `TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories` |
| F07 released before the worktree is removed | `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved` |
| F08 claim key by path | `TestEnterBranch_BranchClaimIsPerRepository` |
| F09 no upward discovery | `TestEnterBranch_BranchClaimIsPerRepository`, `TestRepoClaimKey_MatchesGitCommonDir` |
| F10 artifact push inside the scope | `TestCreate_PushStartsAfterTheBranchScopeIsReleased` |
| F11 implied budget process-wide | `TestCheckoutLimits_InflightCeiling` |
| F12 idle repositories never forgotten | `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository` |
| F13 a cancelled last waiter left in the turn list | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_InflightWaitEndsWithRetryableError` (panics under the gate lock; reported by name in all three packages) |
| F14 MaxInflightWeight without the entry cost | `TestInflightWeight_CountsEntries`, `TestLoadCheckoutLimits_InflightCeiling`, `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories` |
| K01 no tip re-read after admission | `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_ReweighWaitsShareOneDeadline` |
| K02 no HEAD check after the attach (re-made) | `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten` |
| K03 a non-fitting repository rotated too | `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| N01 **new** `switch` again under a ceiling | `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_CeilingAttachesWithoutACheckout`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten` |
| N02 **new** another worktree's hold ignored | `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved` |
| N03 **new** the scope's own worktree counted as another | `TestCheckoutLimits_InflightCeiling`, `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestCreate_UnderACeilingTheWriteIsCommittedAndPushedOnTheBranch`, `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_CeilingAttachesWithoutACheckout`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories`, `TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories` |
| N04 **new** the other-worktree refusal does not match ErrBranchInUse | `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` |
| N05 **new** `worktree list --porcelain` without `-z` | `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved` |
| N06 **new** a failed `worktree list` read as no holder | `TestEnterBranch_UnreadableWorktreeListRefusesTheEntry` — survived the first run; killed after `71db73ca` added the test |
| N07 **new** a mutate deadline per re-weigh attempt | `TestEnterBranch_ReweighWaitsShareOneDeadline` |
| N08 **new** the round-1 default (max(2 x bytes, one maximal tree)) | `TestLoadCheckoutLimits_InflightCeiling`, `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories` |
| N09 **new** warn only below a maximal tree's weight | `TestLoadCheckoutLimits_InflightCeiling` |
| N10 **new** default = one maximal tree's weight | `TestLoadCheckoutLimits_InflightCeiling`, `TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories` |
| S01 the gate never waits | `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_ReweighWaitsShareOneDeadline`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S02 weight released when EnterBranch returns | `TestCheckoutLimits_InflightCeiling`, `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightCeilingBoundsDifferentBranches`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` |
| S03 no over-ceiling refusal (re-made) | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S04 a cancelled waiter left queued | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_CeilingAttachesWithoutACheckout`, `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestEnterBranch_ReweighWaitsShareOneDeadline`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestEnterBranch_TreeOverTheInflightCeilingIsRefused`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S05 weight kept on error paths | `TestCheckoutLimits_InflightCeiling`, `TestEnterBranch_BranchInAnotherWorktreeIsRefused`, `TestEnterBranch_BranchMovedDuringCheckoutIsRefused`, `TestEnterBranch_BranchMovedWhileWaitingIsReweighed`, `TestEnterBranch_BranchThatKeepsMovingIsRefused`, `TestEnterBranch_CeilingAttachesWithoutACheckout`, `TestEnterBranch_EntriesAreWeighed`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit`, `TestEnterBranch_MoveAfterTheReReadIsNeverWritten`, `TestEnterBranch_ReweighWaitsShareOneDeadline`, `TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved`, `TestInflightGate_FIFOWithinARepositoryAndCancellation`, `TestInflightGate_ForgetsIdleRepositories`, `TestInflightGate_HalfTheCeilingPerRepository`, `TestInflightGate_OneRepositoryNeverBlocksAnother`, `TestInflightGate_RoundRobinAcrossRepositories`, `TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne` |
| S06 waits not bounded by the mutate deadline (re-made on the one deadline) | `TestEnterBranch_InflightWaitEndsWithRetryableError`, `TestEnterBranch_ReweighWaitsShareOneDeadline` |
| S07 a done context acquires | `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S08 no admission pass after a cancel | `TestInflightGate_FIFOWithinARepositoryAndCancellation` |
| S09 artifact service maps the wait refusal to git_error | `TestCreate_CheckoutRefusalsKeepTheirClassification` |
| S10 workflow service maps the wait refusal to git_error | `TestCreate_CheckoutRefusalsKeepTheirClassification` |
| S11 no implied budget inside EnterBranch | `TestCheckoutLimits_InflightCeiling`, `TestCreate_CheckoutRefusalsKeepTheirClassification`, `TestEnterBranch_InflightWeightIsReturnedOnEveryExit` |

#### Gates (final code: `43bcc31b`; `71db73ca` adds one test, run as noted)

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | rc=0; rc=0; rc=0 |
| `go test -p 4 ./...` (heavy lock) | rc=0 (41 packages ok) |
| `go vet ./...` plain / `-tags integration` / `-tags scenario`; `GOOS=linux` / `GOOS=windows` vet of the touched packages | rc=0 / 0 / 0; 0 / 0 |
| `-race -p 4` on `adapters/git/...`, `core/artifact/...`, `core/workflow/...`, `core/worktreefs/...`, `service/cmd/spine/...` | rc=0, no data race (and the `71db73ca` test with the attach tests under `-race`: ok) |
| `-tags integration` suites of the touched packages (own DB `s24011_ci_final`, dropped) | rc=0 (7 packages ok) |
| `scenario-git-http` job, every step | rc=0 for all; 90 / 26 / 154 / 1 / 12 + 24 / 37 + 1 / 19 + 1 / 1 passes; 0 skips |
| `git-floor` (`golang:1.26-bookworm`, git 2.39.5, `--pull=never`; container removed) | STEP1 rc=0, STEP2 rc=0; every TASK-011 test PASS on 2.39.5, the round-2 attach, other-worktree (`--porcelain -z`) and deadline tests included (36 PASS, 0 FAIL) |
| golangci-lint v2.11.4 `--new-from-rev=854a1e08` on the touched packages; `--enable-only=gosec` | 0 issues; 0 issues (and 0 on `adapters/git` at `71db73ca`) |
| `gofmt -l` on the changed files | clean |

#### SMP compatibility gate

**Incomplete — the shared PostgreSQL went down during it.** `smp` (SMP main `21cc2e8a`, `../spine` = this worktree at
`43bcc31b`, left unmodified), 2026-11-02 11:47–12:12 UTC, daytime (`t011/chain1.out`, `t011/smp/`):

| Leg | Result |
|---|---|
| `go build ./...`; `go vet` plain / integration / `-tags scenario ./scenariotest/...` / faultinject | rc=0; rc=0 / 0 / 0 / 0 |
| `go test -race -p 4` over the initiative §5 packages and every SMP importer of `adapters/git`, `core/artifact`, `core/workflow` | rc=0 |
| `dev/testgate` githttp leg | PASS (590 passed, 1 allowed skip) |
| `dev/testgate` Spine-runtime leg | **not a valid result**: 2,278 passed, 120 failed — `spineembedded`'s DB-backed tests failed with `connection refused` (187 lines) once the shared `i23-pg` container stopped at 12:07 UTC: its Docker VM disk was full ("could not write to file … No space left on device" at its restart; the container is `Exited (1)`). The first failure, `TestGovernance_SelfApprovalWhenTheAdminHeldTheReview` ("internal_error: load run"), is in the same minute. |

The container was not restarted, reconfigured or pruned (rules). **To finish:** once `i23-pg` is back, re-run
`t011/smp.sh` in daytime and drop `s24011_smp_it` and `s24011_spine_runtime_it` (left behind: the gate's drop ran
against the stopped container). Round 2 changes nothing SMP reaches — SMP calls no branch-scoped `EnterBranch` and
sets no ceiling, so neither the attach nor the serve default applies — and the round-1 gate passed in full
(2026-11-02 daytime).

#### Not run (round 2)

- The SMP gate's Spine-runtime leg (above; the shared PostgreSQL is down).
- Codex (the budget is spent): `db8545c2`, `43bcc31b` and `71db73ca` go to the independent delta check.
- The full `make test-scenario` with a registry database, GitHub CI, the tests on Windows (vet only).
