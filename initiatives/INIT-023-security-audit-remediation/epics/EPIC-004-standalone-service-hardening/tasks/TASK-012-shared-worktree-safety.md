---
id: TASK-012
type: Task
title: "Standalone git-state safety: explicit convergence target, serialized working tree, bounded merge retries"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-15
# Also blocked by spine TASK-002 (CLIClient ref grammar and Merge/Checkout argv), transitively
# through TASK-005. Add the blocked_by link once TASK-002's epic path is fixed in this initiative.
# Design check (2026-10-06): TASK-002's path is fixed, so the link is added below. TASK-011 is
# related (§2.9 and P3-22).
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
  - type: blocked_by
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-002-ref-grammar-and-argv-hardening.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/tasks/TASK-011-projection-sync-fail-closed.md
  - type: blocked_by
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-004-worktree-write-containment.md
  - type: blocked_by
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-005-merge-bound-reviewed-sha.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /architecture/divergence-and-convergence.md
  - type: related_to
    target: /architecture/engine-state-machine.md
---

# TASK-012 — Standalone git-state safety: explicit convergence target, serialized working tree, bounded merge retries

---

## Purpose

Findings: **F8 (P2, standalone)**, **F9 (P2, standalone)** and **P3-27 (P3, standalone)** from the
2026-10-06 security audit (`tmp/init027-orchestration/spine-audit/REPORT.md` in SMP).

Spine line numbers refer to `61994fe`. SMP line numbers refer to `80995559`. Every PoC named
below was re-run for this design with `go test -overlay` against `61994fe` and reproduces.

### F8: convergence merges divergence-branch content straight into `main`

- `CommitConvergence` merges each selected divergence branch with **no `Target`**
  (`core/divergence/convergence.go:187-193`).
- `CLIClient.Merge` treats an empty `Target` as "merge into whatever is checked out"
  (`adapters/git/cli.go:365-370`, `MergeOpts.Target` doc at `adapters/git/client.go:57`).
- In standalone the engine keeps the shared working tree on `main` between operations
  (`core/engine/merge.go:643-668`, `merge_resolution.go:519-529`), so the merge lands on `main`.
- Convergence runs when the last divergence branch step completes
  (`core/engine/step.go:542-558` → `tryConvergence` `:573-640` → `EvaluateAndCommit` `:604`).
  That is **before** the evaluation (review) step, without `MergeRunBranch`, without the
  `committing` state, and without any check of the actual target.
  - The only branch-protection call evaluates the *source* `spine/*` ref
    (`convergence.go:184`), which no rule matches.
- Divergence refs are deterministically named `spine/<run>/<div>/<branch>`
  (`core/divergence/service.go:221`), and the standalone pre-receive gate skips every `spine/*`
  ref. Who may push them is TASK-016's subject.

**Attacker.** A1, the author of a divergence branch (a branch worker, or any authenticated actor
who can push the deterministic ref). An honest worker completing the branch step then carries
that content to `main`.

**Impact.** Unreviewed content on the governed branch while the run is still `active` and its
review step is still `waiting` (ADR-009 / Constitution §2). It can include
`.spine/branch-protection.yaml` and `workflows/**`.

**Reproduced.** `poc/skeptic_conv/engine_conv_test.go` (PASS = vulnerable: "content landed on
main while the review step is still pending"), `poc/verify-r1-11/engine_convergence_main_test.go`
(PASS, `main` advanced, run `active`, review `waiting`, `no-direct-write` never consulted) and
`poc/governance/convergence_main_test.go` (PASS, `main` advanced by `EvaluateAndCommit`).

### F9: concurrent runs share one working tree with no lock

- Standalone builds **one** `git.CLIClient` per workspace and shares it between the engine, the
  divergence service, the artifact and workflow services and the projection
  (`service/cmd/spine/serve_cmd.go:125`, `service/internal/workspace/pool.go:839`; the same instance
  is passed to `divergence.NewService` at `serve_builders.go:274` and `pool.go:979`).
- The only mutex in `adapters/git` is `authMu` (`cli.go:169`), which guards push-auth caching.
- Multi-step sequences on that one HEAD/index interleave:
  - `applyCommitStatus`: Checkout(run) → WriteAndStageFile → Commit → Checkout(main)
    (`core/engine/merge.go:643-668`);
  - `mergePrimary`: Merge{Target: main} → abort → Push (`merge.go:168-246`);
  - `createRunBranches`: `Head()` then `CreateBranch(name, "HEAD")`
    (`core/engine/multi_repo_branch.go:80-92`);
  - the recovery ledger: Checkout(run) → Commit → PushBranch → Checkout(main)
    (`merge_resolution.go:519-581`);
  - in-place artifact/workflow writes (`git.EnterBranch` with an empty branch returns the main
    tree, `adapters/git/branchwrite.go:54-56`).
- `MergeRunBranch` is entered concurrently by the HTTP submit path (`core/engine/run.go:442`,
  `handlers.go:76`) and the scheduler commit retry (`serve_builders.go:313-315`).
- Nothing ever cleans the tree, so a tree left dirty or off `main` **permanently fails every later
  governed merge** in the workspace. A run branch cut while HEAD sits on a foreign run branch
  silently carries that run's unmerged work.

**Attacker.** A2 (concurrent approvals; no special privilege) plus A1.

**Impact.** Workspace-wide merge denial of service until an operator repairs the tree by hand;
cross-run contamination (one run's content committed onto another run's branch, a status commit
on `main` outside the governed merge, a run branch cut from another run's branch). No unapproved
content reached `main` in the audit runs (the skeptic lowered F9 from P1 for that reason).

**Reproduced.** `poc/gap2-git-state-concurrenc/spine_worktree_race_poc_test.go` and
`spine_poison_poc_test.go` (these use "FAIL = vulnerable"): all four tests FAIL — 25/25 pairs had a
spurious merge failure, 10/25 left the index dirty, 10/25 left HEAD off `main`; the status commit
landed on `main`; run C was cut from run A's branch; every later serial run failed.
`poc/verify-r2-2/zz_verify_r22_test.go`: 8/30 pairs poisoned the tree; in every poisoned case the
next unrelated serial run `failed`, and four strictly serial runs in a row all failed.

> **Design-check note (2026-10-06): three more paths to the shared-tree sink.** Each one was
> reproduced against the designer's prototype. The probes are in `_work/task012-check/`; see the
> Design check section.
> - **Request cancellation inside a held sequence (A2 alone).** `exec.CommandContext` SIGKILLs git
>   when the request ctx ends. That happens when the client disconnects during the submit that
>   triggers `MergeRunBranch` (`run.go:442`, `handlers.go:76`), or during an in-place artifact
>   write.
>   - 4 of 8 cancelled governed merges left `.git/index.lock`.
>   - The next unrelated merge then failed `remote_unavailable` ("repository locked").
>   - The prototype's `RestoreWorkingTree` reported **success**.
>
>   This is F9's "permanently fails every later governed merge", with no concurrency needed.
> - **The same run entered twice.** This is the HTTP submit plus scheduler retry pair that the
>   finding names. Suppose the second entry passes `preflightCheck` before the first one completes.
>   It then:
>   - merges a branch the first entry already deleted;
>   - overwrites the `merged` outcome with `failed`;
>   - flips the `completed` run to `failed` and emits `run_failed`.
>
>   This reproduces identically on `61994fe` and on the prototype. The tree lock does not reach it.
> - **HEAD-relative reads by other requests while a sequence holds the tree.** A
>   `ReadFile(ctx, "HEAD", …)` issued during run A's cascade returned run A's unreviewed version of
>   a shared task file. These are the sinks:
>   - `StartRun` reads the task at `HEAD` and binds the workflow by its `type`
>     (`core/engine/run.go:132`);
>   - precondition reads (`step.go:808-813`);
>   - the workflow pin fallback `gitClient.Head` (`core/workflow/binding.go:83`);
>   - ID allocation and workflow listing (§2.9).

### P3-27: unbounded merge retries; multi-workspace scheduler without budget or concurrency bound

- `scheduler.WithCommitRetry(fn, maxRetries, threshold)` stores `commitMaxRetries`
  (`adapters/scheduler/options.go:37-43`, `scheduler.go:21`) but nothing reads it.
  `retryRunsByStatus` (`recovery.go:349-409`) and `recoverCommittingRuns` (`:266-293`) call the
  retry function on every tick, forever.
- A transient failure keeps the run in `committing` (`merge.go:192-196`, `:242-244`), so a run
  whose merge or push keeps failing transiently (network, a stale `index.lock` → "repository
  locked", `adapters/git/errors.go:45-46`) is retried every 60 s indefinitely.
  `architecture/engine-state-machine.md:61-62` already documents "retries remain" and "after
  retries" — the code does not implement it.
- Standalone passes `maxRetries = 3` (`serve_builders.go:315`); the dormant `MultiScheduler`
  passes `0` (`multi.go:173`).
- `MultiScheduler.forEachWorkspace` (`multi.go:144-188`) scans every tenant serially in the one
  ticker goroutine with no per-workspace budget, so one slow tenant delays every other tenant's
  timeouts, orphan scans and merge retries. `MultiScheduler` is **not constructed** by
  `spine serve` today (`NewMultiScheduler` has no production caller), so this half is dormant.

**Verified for this design.** With a transient push failure injected, each `MergeRunBranch` call
leaves the run `committing` and increments the primary outcome's `attempts` (1, 2, 3, …) with
class `network` (`core/engine/multi_repo_merge.go:367-400`).

## Design

### 0. Invariants

- **Standalone-only behaviour change.** SMP's engine operator (`RealGitOperator`) is not a shared
  working tree. Every new behaviour switches on a capability SMP's types do not have, or on
  configuration SMP never sets. SMP compiles and behaves unchanged (§7).
- **A shared working tree is only touched by one sequence at a time,** in-process. Sequences
  never interleave on HEAD, the index or the files.
- **Between sequences the shared tree rests on a clean checkout of its home branch** (`main`).
  Whoever takes the tree first repairs it if it is not, so a poisoned tree self-heals at the next
  governed operation. The repair never destroys data: tracked changes are quarantined into an
  unreferenced commit, untracked files are never touched.
- **No lock is held while emitting events or routing steps.** `MemoryQueue.Publish` blocks while
  the queue is full (`core/queue/memory.go:97-100`, reached from `QueueRouter.Emit`
  `core/event/router_impl.go:44`). A queue consumer that needs the tree would otherwise deadlock
  with a holder that emits. Locked sections are git-only.
- **Convergence never names an implicit target.** `CLIClient.Merge` refuses an empty `Target`.
- **No runtime schema change.** The retry bound uses `repository_merge_outcomes.attempts`
  (migration 022).

> **Design-check note (2026-10-06): invariants added or corrected.**
> - **A taken tree is never abandoned half-way.** Only the lock *wait* honours the caller's ctx.
>   Once the tree is taken, the entry restore, `fn` and the exit restore run on
>   `context.WithoutCancel(ctx)`, bounded by `treeSequenceTimeout`. Every git child is stopped
>   with SIGTERM, never SIGKILL (§2.5 note).
> - **Locked sections are git-only, plus one run-status read** (§2.7 note). There are no events,
>   no routing and no store writes inside them.
> - **Outside a sequence, `HEAD` on a home-branch tree means the home branch** (new §2.9).
> - **"The repair never destroys data" is narrowed.** The `reset --hard` fallback after a failed
>   `git merge --abort` (§2.5 step 2) discards the conflicted state of Spine's own failed merge
>   without quarantine. That is the only unquarantined discard.
> - **In-process only.** The lock and the restore assume one `spine serve` process per repository
>   path. A second process, or an operator's git, on the same tree is not serialized, and a restore
>   can quarantine its in-flight state (§4 note).
> - **Two deliberate exceptions to "SMP behaves unchanged":** SIGTERM-on-cancel for git children,
>   and the run-status re-check in the merge blocks. Both are listed in the §6 compatibility
>   note. Neither changes a successful result.

### 1. F8 — explicit convergence target

**1.1 Pass the run to convergence.** The engine already holds the run in `tryConvergence`.

- `core/engine/interfaces.go:126-129`:
  `EvaluateAndCommit(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) error`.
- `core/engine/step.go:604`: `o.convergence.EvaluateAndCommit(ctx, run, divCtx, *convDef)`.
- `core/divergence/convergence_adapter.go:13` and `:49`: thread `run` through.
- `core/divergence/convergence.go:172`:
  `CommitConvergence(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, output *ConvergenceOutput) error`.

**1.2 `CommitConvergence`** (`convergence.go:172-230`) becomes three steps:

1. **Guard.** Return `domain.NewError(domain.ErrPrecondition, …)` before any git or store call
   unless `run != nil`, `run.RunID == divCtx.RunID`, `run.BranchName != ""` and
   `strings.HasPrefix(run.BranchName, "spine/")`. Every run branch is `spine/run/…` or
   `spine/plan/…` (`core/engine/branchname.go:22-24`). This makes "converge into `main`" or
   "converge into another run's branch" unrepresentable.
2. **Merge loop (`mergeSelected`, unexported).** For each selected branch: the existing
   `evaluateBranchOp` call (`:184`) and `s.git.Merge(MergeOpts{Source: gitBranch, Target:
   run.BranchName, Strategy: "merge-commit", Message: …})`.
   - The loop runs inside `git.WorkingTreeGuard.WithWorkingTree` when `s.git` implements it (§2.3),
     otherwise directly.
   - On a merge error, **and only for a shared-tree client** (`s.git` implements
     `git.WorkingTreeGuard`), call `s.git.Merge(MergeOpts{Source: "--abort", Strategy: "abort"})`
     before returning. Today a convergence conflict leaves the shared tree mid-merge.
   - The abort must not reach other clients: SMP's `divergenceGitClient.Merge` resolves an empty
     `Target` from `Source` (`internal/customer/spinecore/divergencegitclient.go:294-300`); for
     `"--abort"` that resolves to `"HEAD"`, `RealGitOperator.Merge` short-circuits the abort
     (`gitoperator.go:387-389`), and the adapter then calls `Push(ctx, "origin", "HEAD")`
     (`divergencegitclient.go:305`).
3. **Finish** (unchanged code from `:196-229`): the divergence transition,
   `UpdateDivergenceContext`, then the `convergence_completed` event — after the tree is released.

> **Design-check note (2026-10-06): the guard must run before the `converging` write.**
> - The engine reaches `CommitConvergence` through `EvaluateAndCommit`. That function first
>   persists `active → converging` (`convergence_adapter.go:15-29`) and lists the branches. With
>   the guard only in `CommitConvergence`, a refused convergence leaves the context `converging`.
>   The probe showed `active -> converging` on a refusal, which contradicts "before any git or
>   store call".
> - **Change.** Run the same guard as the first statement of `EvaluateAndCommit`. Keep it in
>   `CommitConvergence` too, because that method is exported and tests call it directly.
> - **Also.** Run `evaluateBranchOp` for every selected branch *before* taking the tree, since
>   `policy.Evaluate` is not git. Evaluate the actual target `run.BranchName`, not the source. The
>   real evaluator allows `OpGovernedMerge` unconditionally, so the decision does not change, and
>   the audit record then names the branch that actually moves.

**1.3 `CLIClient.Merge` refuses an empty `Target`** (`adapters/git/cli.go:365-370`).

- `Strategy == "abort"` is the only shape without a `Target`. It requires `Source` to be empty or
  `"--abort"` and runs `git merge --abort`. Any other abort shape is refused.
  - TASK-002 owns `Merge`'s argv (qualified refs, `--end-of-options`). If TASK-002 already split
    the abort out, keep its form; this rule then applies to the non-abort path only.
- Otherwise an empty `Target` returns `&GitError{Kind: ErrKindPermanent, Op: "merge", Message:
  "merge target required"}` before any git command runs.
- Update the `MergeOpts.Target` doc (`client.go:57`) from "empty = current branch" to "required
  except for Strategy abort".
- Production callers already pass a `Target`: `merge.go:168-174` (`main`),
  `multi_repo_merge.go:204-210` (`repo.DefaultBranch`); the aborts are `merge.go:327-330` and
  `multi_repo_merge.go:219`. Checked: no other non-test caller in Spine; SMP never calls
  `CLIClient.Merge` (§7).

> **Design-check note (2026-10-06): the abort shape must match TASK-002.**
> - TASK-002 keeps the in-band sentinel as `Strategy == "abort"` **or** `Source == "--abort"`.
>   Each one produces exactly `git merge --abort` (TASK-002 AC: "`Merge{Strategy:"abort"}` and
>   `Merge{Source:"--abort"}` each produce exactly `merge --abort`"). SMP's operator also keys on
>   `Source == "--abort"` (`gitoperator.go:387`).
> - The prototype refused `Merge{Source: "--abort"}` with "merge target required".
> - **Rule for this task.**
>   - A call is an abort when either field says so.
>   - An abort is refused before git runs when `Target != ""`, or when `Strategy == "abort"` and
>     `Source` is neither empty nor `"--abort"`.
>   - Every other call needs a non-empty `Target`.
>
>   AC 4 gains the `{Source: "--abort"}` case.

**1.4 Divergence-branch base is unchanged.** `createBranchRecord` still calls
`CreateBranch(name, "HEAD")` (`core/divergence/service.go:227`). With §2.5 that resolves to the
home branch (`main`) in standalone, exactly what happens today when the tree is healthy. Forking
from the run branch (SMP's behaviour) is a follow-up, not a security fix (out of scope).

> **Design-check note (2026-10-06): fork divergence branches from the run branch. This supersedes
> §1.4 and Decision 12.**
> - **Why.** Once the target is fixed, a `HEAD` (= `main`) base makes the run's own
>   pre-divergence work and the branch work siblings, so any file both touch now conflicts *at
>   convergence*. The probe (`zz_t012chk_divbase_test.go`) showed this:
>   - a run step edits `README.md` on the run branch, and the divergence branch edits it too;
>   - `EvaluateAndCommit` fails and the context stays `converging`;
>   - `tryConvergence` only logs at Warn (`step.go:555-557`), so the run stalls with no route.
>
>   At `61994fe` the same run converged into `main` (F8) and conflicted later, at the governed
>   merge, which routes through `merge_failed`.
> - SMP already treats a `main` fork as a bug. Its adapter says Spine's model is "HEAD == run
>   branch" and resolves `HEAD` to `run.BranchName` (codex T-K+13b R2 P1;
>   `divergencegitclient.go:75-80`, `:95-106`; `factory.go:238-243`).
> - **Change.**
>   - `createBranchRecord` (`service.go:227`) passes `run.BranchName` as the base when it is
>     non-empty and has the `spine/` prefix the §1.2 guard requires. Otherwise it keeps `"HEAD"`.
>   - `CreateExploratoryBranch` builds a synthetic run with only `RunID` (`service.go:172`), so it
>     gains a `run *domain.Run` parameter, guarded like §1.2. The same change goes on the engine's
>     `DivergenceHandler` (`interfaces.go:121`) and the gateway's `BranchCreator`
>     (`service/internal/gateway/server.go:161`). The gateway handler
>     (`handlers_divergence.go:40-46`) already has the store and loads the run with
>     `st.GetRun(divCtx.RunID)`.
> - **SMP.** `divergenceGitClient.CreateBranch` passes any base other than `""` or `"HEAD"` through
>   unchanged (`divergencegitclient.go:154-156`, documented at `:135-137`). SMP therefore forks from
>   the same ref it resolves today. SMP neither calls `CreateExploratoryBranch` nor implements
>   either interface, so it compiles unchanged (checked by grep).

### 2. F9 — serialized working tree

**2.1 Tree lock** (`adapters/git/treelock.go`, new; unexported except where stated).

- **Registry.** Process-wide `map[canonicalKey]*treeLock` behind one mutex. The key is the
  cleaned absolute path, resolved through `filepath.EvalSymlinks` when it exists. Two clients or
  scopes that name the same directory differently (`/var/…` vs `/private/var/…`, `repo/.`) share
  one lock.
- **Lock.** A capacity-1 channel, so waiting honours `ctx.Done()` (a cancelled request stops
  waiting and gets `ctx.Err()`).
- **Reentrancy by ticket.** Acquisition stores a fresh `*treeTicket` as the lock's current holder
  and returns a child ctx carrying it. A stateful call whose ctx carries the **current holder's**
  ticket proceeds without re-locking. A ticket is honoured only while it is the live holder, so a
  ctx that escapes after release (an async event handler, a stored ctx) cannot bypass the lock.
- **Refcounting.** An entry counts holders plus waiters and is deleted at zero. This matters for
  SMP, which locks a fresh per-request worktree path on every artifact or workflow write; without
  it the registry would grow by one entry per request.

> **Design-check note (2026-10-06): acquisition and wait errors.**
> - **A done ctx never acquires.** Check `ctx.Err()` before the `select`. Go picks at random among
>   ready cases, so with an already-cancelled ctx on a free lock the prototype ran `fn` 91 times
>   out of 200 (`TestT012Check_CancelledCtxAcquisitionIsNondeterministic`).
> - **The wait error is typed:** `*git.TreeWaitError{Err: ctx.Err()}`, exported, with `Unwrap`.
>   §2.7's lock-wait rule keys on `errors.As`, not on "a ctx error". An `fn` error can wrap
>   `context.DeadlineExceeded` too, through the §2.5 note's sequence bound.

**2.2 Stateful `CLIClient` operations take the lock per call.**

| Operation | Today | Change |
|---|---|---|
| `Checkout` (`cli.go:334-337`) | unlocked | lock |
| `Commit` (`:341-362`) | unlocked | lock |
| `Merge`, including abort (`:365-403`) | unlocked | lock (+ §1.3) |
| `CreateBranch` (`:406-409`) | unlocked | lock (+ §2.5 when base is `HEAD`/empty) |
| `DeleteBranch` (`:412-415`) | unlocked | lock |
| `WriteAndStageFile` (`:508-520`, TASK-004's body) | unlocked | lock, before TASK-004's confined write |
| TASK-005 `MergeBound` | — | lock |

Not locked, because they do not read or write HEAD, the index or the files: `Clone`, `Diff`,
`Log`, `ReadFile`, `ListFiles`, `Head`, `RefSHA`, `MergeBase`, `HasCommitWithTrailer`, `Push`,
`PushBranch`, `DeleteRemoteBranch`, and TASK-005's `ResolveBranchHead`, `VerifyCommit`,
`IsAncestor`, `CommitFilesOnto` (temporary index) and `PushCommitCAS`. HEAD-relative *reads* stay
unserialized (§9, TASK-011).

A per-call lock makes each operation atomic. It does not make a multi-call sequence atomic; that
is §2.3.

**2.3 Sequences: `git.WorkingTreeGuard`** (`adapters/git`, exported).

```go
// WorkingTreeGuard is implemented by clients whose stateful operations share one
// on-disk working tree (*CLIClient). A caller that runs a multi-step sequence on
// that tree (checkout → write → commit → checkout, merge → abort → push) runs it
// inside WithWorkingTree. Never emit events or route steps inside fn.
type WorkingTreeGuard interface {
	WithWorkingTree(ctx context.Context, fn func(ctx context.Context) error) error
}
```

`(*CLIClient).WithWorkingTree`: take the lock (or reenter); when this call **took** the tree,
apply §2.5 before `fn` and after `fn`; call `fn` with the ticket-carrying ctx. The interface lives
in `adapters/git` so both `core/engine` and `core/divergence` can use it (`adapters/git` is on the
core→adapters allowlist, `dev/lint-boundaries.sh`). SMP's `RealGitOperator` and every engine test
mock do not implement it, so their sequences run exactly as today.

**2.4 Home branch** (`adapters/git`, exported option).

```go
// WithHomeBranch declares that the client's working tree is a shared tree that
// must rest on a clean checkout of branch between sequences.
func WithHomeBranch(branch string) CLIOption
```

- The home branch is a property of the **tree**. The client registers it in a process-wide map
  keyed like the lock (under the absolute path and, when it resolves, the symlink-free path;
  lookups try both). Every client and in-place scope on that tree then uses it, including
  `git.EnterBranch`, which receives only a directory.
- Only standalone's shared workspace clients register one (§2.8). Trees that no client registered
  are never restored: code-repo clones, SMP's per-request worktrees, and the trees of plain
  `NewCLIClient(dir)` users.
- `(*CLIClient).RestoreWorkingTree(ctx) (TreeRestoreReport, error)` is exported for operators and
  tests. It returns a permanent `*GitError` when the tree has no home branch.

**2.5 Restore** (`restoreTree`, unexported). It runs while holding the lock, with a detached ctx
bounded at 30 s (`context.WithTimeout(context.WithoutCancel(ctx), …)`), so a request cancellation
never kills git half-way and leaves a stale `index.lock`.

1. Record `git symbolic-ref -q HEAD` (empty when detached).
2. If `git rev-parse -q --verify MERGE_HEAD` succeeds: `git merge --abort`; if that fails,
   `git reset --hard -q`. (`git stash create` refuses an unmerged index, so the abort comes first.
   `reset --hard` clears `MERGE_HEAD` — both checked on git 2.54 for this design.)
3. If `git status --porcelain=v1 -z --untracked-files=no` is non-empty:
   `sha := git stash create "spine: quarantined working-tree state"`, then `git reset --hard -q`.
   - `stash create` writes a commit but **no ref**: nothing is advertised by the git HTTP
     endpoint, and the content stays recoverable with `git stash apply <sha>` until `gc` prunes
     it (two weeks by default).
   - If `stash create` fails, return the error and change nothing.
4. If HEAD is not `refs/heads/<home>`: check out `<home>` using TASK-002's branch-checkout argv
   (never DWIM to a tag).
5. Verify: HEAD is `refs/heads/<home>` and the tracked status is clean. Otherwise fail.
- **Untracked files are never removed.** If one blocks step 4 the restore fails, and an operator
  resolves it.
- **Failure** returns `*git.TreeRestoreError{Branch, Err}` (exported, `Unwrap` to the `*GitError`,
  so `classifyGitError`'s transient/permanent class survives).
- **Report.** `TreeRestoreReport{PreviousRef, AbortedMerge, QuarantineSHA, SwitchedHEAD}` with
  `Repaired()`. A repair logs at Warn and writes `observe.AuditLog(ctx, "working_tree_restored",
  …)` with the home branch, the previous ref, `aborted_merge` and `quarantine_sha`
  (`adapters/git` → `core/observe` is allowed by the boundary lint).

**When it runs** — only when a caller *takes* the tree (not on a reentrant call), and only for a
tree with a home branch:

- `WithWorkingTree`: before `fn` (a failure returns the `*TreeRestoreError` and `fn` does not run)
  and after `fn` (a failure is logged at Error; `fn`'s result is returned).
- `EnterBranch(ctx, dir, "", …)` (§2.6): before handing out the in-place scope.
- `CreateBranch(name, base)` with `base` `""` or `"HEAD"`: before resolving `HEAD`, so a run or
  divergence branch is never cut from a foreign branch even when called outside a sequence.
- Per-call `Checkout`, `Commit`, `Merge`, `WriteAndStageFile` and `DeleteBranch` never restore:
  they are parts of sequences, and the sequence owns HEAD.

> **Design-check note (2026-10-06): cancellation must not stop git half-way (F9 reached by A2).**
> - **Evidence** (Design check, probes 1 and 2). The prototype passes the request ctx into the
>   sequence.
>   - 4 of 8 cancelled governed merges left `.git/index.lock`. Every later operation failed
>     "repository locked" (transient).
>   - `RestoreWorkingTree` returned **no error**, because `symbolic-ref`, `status` and
>     `rev-parse` all work with a stale lock present.
>   - Sending SIGTERM instead removed the lock in all 8 runs. Git's lockfile handlers run on
>     SIGTERM, not on SIGKILL. But an interrupted cascade checkout still left up to 575 untracked
>     files, which no restore removes.
>   - A git command stopped inside `fn` returns empty or partial stderr. `classifyGitError` then
>     takes its default (permanent) case, and `mergePrimary` routes a client disconnect through
>     `handlePermanentMergeFailure`. Part of the `failed` runs in probe 2 came from two prototype
>     deviations from §2.5/§2.7: an entry restore on the request ctx, and a recorded lock-wait
>     error.
>   - Running the taken sequence on a detached ctx left no lock and no residue in any run.
> - **Change 1: SIGTERM.** Every git child that Spine starts sets
>   `cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }` and
>   `cmd.WaitDelay = 10 * time.Second`. That covers:
>   - `CLIClient.run` (`cli.go:645`);
>   - `Clone` (`cli.go:297`);
>   - the `branchwrite.go` helpers (`:75`, `:91`, `:130`, `:142`, `:152`, `:168`, `:175`).
>
>   It applies to every client, SMP's included.
> - **Change 2: a detached sequence.**
>   - When `WithWorkingTree` *takes* the tree, the entry restore, `fn` and the exit restore run
>     on `context.WithoutCancel(ctx)`, bounded by `treeSequenceTimeout` (default 5 min, owner
>     question 6).
>   - Only the lock wait honours the caller's ctx. A reentrant call keeps the caller's ctx.
>   - In-place scopes get the same treatment only on home-branch trees (§2.6 note), so SMP's
>     per-request writes stay cancellable exactly as today.
>   - A sequence that hits the bound is stopped with SIGTERM and healed by the next restore.
> - **Change 3: the restore checks for the lock file.**
>   - Before step 1, if `.git/index.lock` exists, return a `*TreeRestoreError` whose message
>     names the file. Its `Err` is the transient `*GitError` "repository locked".
>   - With changes 1 and 2, the in-process holder never leaves such a file behind. A lock found
>     here is crash residue or belongs to another process.
>   - The restore does not delete it (owner question 5).

**2.6 In-place write scopes** (`adapters/git/branchwrite.go:53-56`). With an empty branch,
`EnterBranch` takes the tree lock (reentrant via ctx), applies §2.5 when it took the tree, and
returns a scope whose `Cleanup` releases the lock (idempotent; the branched case is unchanged).
This serializes standalone's direct artifact and workflow writes with engine sequences. Today such
a write can commit onto a run branch the engine has checked out, or have its staged file swept
into the engine's cascade commit.

- `core/artifact/service.go`: call `scope.Cleanup()` right after `s.autoPush` (`:264`) and before
  `s.emitEvent` (`:272`) and `emitBranchProtectionOverride` (`:275`). The deferred `Cleanup` at
  `:200` stays for the error paths. This keeps events out of the locked section (§0).
  `workflow.Service` emits no events (`core/workflow/service.go:138-200`).
- Checked: inside an in-place scope neither service calls a locked `CLIClient` method (they call
  only `ReadFile`, `ListFiles`, `Head` and `Push` on `s.git`), so the lock cannot self-deadlock.

> **Design-check note (2026-10-06): the in-place scope must hand its ticket to the caller.**
> - **The trap.** The prototype's in-place `EnterBranch` holds the lock but returns no
>   ticket-carrying ctx. If the same request then makes a locked `CLIClient` call inside its own
>   scope, that call waits for itself until the ctx ends. In the probe, `Checkout` inside the
>   scope returned `context deadline exceeded` after 1 s; with a background ctx it would never
>   return.
> - Neither service makes such a call today. But TASK-004, TASK-014 and TASK-017 all edit these
>   write paths, so the "checked" claim above is a fragile invariant.
> - **Change.** Add `(*WriteScope).Context(ctx context.Context) context.Context` (additive). It
>   returns ctx carrying the scope's ticket:
>   - on a home-branch tree, also detached and bounded (§2.5 note);
>   - otherwise keeping ctx's cancellation, so SMP is unchanged;
>   - for a branched (worktree) scope, ctx itself.
> - `artifact.Service.writeAndCommit` and `workflow.Service` run every in-scope call
>   (`StageAndCommit`, `UnstageFile`, `autoPush`) with `scope.Context(ctx)`. Events keep the
>   original ctx and still run after `Cleanup`.

**2.7 Engine sequences** (`core/engine`). A small helper
`func (o *Orchestrator) withTree(ctx, fn) error` uses `o.git.(git.WorkingTreeGuard)` or runs `fn`
directly. Every block is git-only; outcome recording, state transitions, events and step routing
happen after it returns.

| Sequence | Block (git-only) | After the block |
|---|---|---|
| Status cascade, binding off (`preflightCheck` `merge.go:100-107`) | `applyCommitStatus` (`:599-675`) | today's log-and-proceed |
| Primary merge (`mergePrimary` `:168-246`) | `Merge{Target: main}` → `abortMerge` on error → `Push("origin","main")` when auto-push | record outcome, classify, `retryMerge` / `handlePermanentMergeFailure` (unchanged logic) |
| Run start (`createRunBranches` `multi_repo_branch.go:80-92`) | `Head()` + `CreateBranch(name, "HEAD")` | `PushBranch` and code repositories (each has its own tree and per-call locks) |
| Recovery ledger (`writeMergeRecoveryLedgerCommit` `merge_resolution.go:519-581`) | Checkout(run) → Commit → PushBranch → Checkout(main) | the existing callers' outcome writes and events |
| Convergence | inside `CommitConvergence` (§1.2) | divergence transition and event |

- **"Restore a clean tree on `main` before each governed merge"** is the primary-merge block's
  entry restore (the block takes the tree fresh). The cascade block's entry restore protects the
  cascade too.
- **Entry-restore failure in the primary-merge block** is that attempt's merge error: it is
  recorded as a failed primary outcome (`recordPrimaryMergeOutcome`) and classified as today.
  A transient class (for example a stale `index.lock`) stays `committing` and is bounded by §3.
  A permanent class routes through `handlePermanentMergeFailure`.
- **Lock-wait cancellation.** A ctx error returned by `WithWorkingTree` itself can only come from
  the lock wait (the restore runs on a detached ctx, §2.5), so `fn` has not run.
  `MergeRunBranch` returns it **without** recording an outcome or routing, and the run stays
  `committing` for the scheduler. Without this rule, a client disconnect while waiting would be
  classified `unknown` (permanent) and loop the run back.
- **After TASK-005.** Under binding, the cascade is plumbing (`CommitFilesOnto`, no tree), and the
  primary block holds `MergeBound` → `verifyBoundMerge` reads → `PushCommitCAS`. A
  `verifyBoundMerge` violation is returned from the block as a verdict, and the existing
  record-and-route runs after release. The binding-off path keeps TASK-005 AC 7's exact call
  sequence (a recording operator is not a guard, so `withTree` runs `fn` directly).
- **Collision renumber** (`merge.go:200-207`) runs after the block, as today. It is wired in
  neither product. Its doc gains "an implementation must run its git sequence through
  `git.WorkingTreeGuard`".
- The ticket in ctx makes the publish handler → `MergeRunBranch` and similar nested paths
  reentrant. No engine path starts a goroutine (checked: no `go` statement in `core/engine`,
  `core/divergence`, `core/artifact` or `core/workflow`).

> **Design-check note (2026-10-06): four additions to the engine blocks.**
> 1. **Same-run re-check (F9, the named concurrent entry).**
>    - The cascade block and the primary-merge block each start with `GetRun`. If the run is no
>      longer `committing`, the block returns `errRunSuperseded` without doing any git work.
>      `MergeRunBranch` then returns nil without recording an outcome or routing.
>    - This closes the double-entry race. `completeAfterMerge` moves the status *before*
>      `CleanupRunBranch` deletes the branch (`merge.go:350-381`), and the delete is a locked
>      call. A second entry therefore either sees `completed`, or merges a still-existing branch
>      as "already up to date".
>    - TASK-008's compare-and-swap writes remain the store-level backstop.
>    - Probe 3 (`completed → failed`) must pass after this change (AC 6b).
> 2. **A refused bound merge rolls back inside the block.**
>    - Three refusals happen *after* `MergeBound` has moved local `refs/heads/main` to `M`:
>      TASK-005's `verifyBoundMerge`, TASK-006's §3.4a post-merge assertion, and TASK-017's
>      `merge_result_mismatch`. All three require restoring `main` to `BaseSHA` (TASK-006
>      dependency note: `update-ref refs/heads/main <BaseSHA> <M>` plus a hard reset).
>    - That restore is git, so it **must run inside the primary-merge block, before release**.
>      Only the outcome record and the routing run after the block.
>    - Otherwise the next holder builds on, or publishes, the refused `M`:
>      - an in-place artifact write commits on top of `M`, and its `autoPush` runs
>        `git push origin main` (`artifact/service.go:584-598`);
>      - a run start cuts its branch from `M`;
>      - another run's `MergeBound` computes on `M`.
>
>      AC 26.
> 3. **TASK-005's API names (its DC-1).**
>    - Wherever this document says `PushCommitCAS`, read `PushCommit`, a non-forced push of the
>      verified SHA.
>    - Add `CommitParents` to §2.2's unlocked list.
>    - §9's "lease mismatch stays permanent" bullet is superseded, because there is no lease.
>      What remains is re-syncing local `main` after a *non-fast-forward* refusal, which is out of
>      scope.
> 4. **Code repositories (dormant, cheap guard).**
>    - `attemptCodeRepositoryMerge` (`multi_repo_merge.go:204-219`) runs merge and then abort as
>      two separate per-call locks.
>    - Wrap the sequence in the code-repo client's `WorkingTreeGuard` when the client implements
>      it. This takes the lock only; code-repo clients register no home branch, so no restore runs.
>    - The path is unreachable until a code-repo catalog is wired (P3-37).

**2.8 Wiring.**

- `service/cmd/spine/serve_cmd.go:125`:
  `git.NewCLIClient(repoPath, append(gitOpts, git.WithHomeBranch("main"))...)`.
- `service/internal/workspace/pool.go:839`: the same, for the workspace's primary client only.
  `gitOpts` also feeds `gitpool.NewCLIClientFactory` and `NewCLICloner` (code repositories, own
  default branches), so build a copy rather than appending to the shared slice.
- `"main"` matches the engine's `authoritativeBranch` (`core/engine/merge.go:23`). The call sites
  carry a comment pointing at it.
- `service/scenariotest/harness/repo.go:29` uses `WithHomeBranch("main")` so scenarios mirror
  production. A harness option `WithoutHomeBranch()` exists for a scenario that must observe a
  non-restored tree; each use states its reason. `harness/multirepo.go:152` (code repos) is
  unchanged.

**2.9 `HEAD` on a home-branch tree means the home branch** *(added by the design check,
2026-10-06)*.

- **Why.** Probe 4 (`zz_t012chk_headread_test.go`): during run A's cascade, a concurrent
  `ReadFile(ctx, "HEAD", "tasks/shared.md")` returned run A's unreviewed `type: Spike`.
  `StartRun` reads the task at `HEAD` (`core/engine/run.go:132`) and resolves the workflow by
  `art.Type`. A run started in that window therefore binds the workflow that an unreviewed branch
  chose. The other sinks are:
  - preconditions (`resolveReadRef`, `step.go:808-813`);
  - the workflow pin fallback (`core/workflow/binding.go:83`);
  - `NextID` (`id_allocator.go:61`, `handlers_artifact_entry.go:97`);
  - `handlers_tasks.go:131`;
  - artifact read and list (`artifact/service.go:284`, `:321`; `handlers_artifact_entry.go:274`,
    `:317`);
  - workflow read and list (`workflow/service.go:206`, `:240`, `:250`);
  - the projection (`projection/service.go:61`, `:174`; P3-22).
- **Change.**
  - Applies to a `CLIClient` whose tree has a registered home branch.
  - Its read methods `ReadFile`, `ListFiles`, `Head`, `Diff`, `Log`, `MergeBase` and `RefSHA`
    resolve a `HEAD` argument to `refs/heads/<home>`, with no DWIM.
  - `Log` also has an implicit `HEAD`: the `<since>..HEAD` range at `cli.go:484`, and the default
    revision when `Since` is empty. Both name `refs/heads/<home>`. This covers the projection's
    history query (`adapters/projection/query.go:155`).
  - The rewrite applies whether or not a sequence is held. Inside a sequence, the only `HEAD`
    reader is `createRunBranches`' `Head()`, called right after the entry restore, when the two
    refs are equal.
  - Real `HEAD` is kept by everything that is not a read method:
    - the internal `rev-parse HEAD` of `Commit` and `Merge` (`cli.go:356`, `:396`);
    - the package-level `CurrentBranch`.
- **Scope.**
  - Trees without a home branch are unchanged: SMP, code repositories and plain `NewCLIClient`.
  - TASK-011 still pins the projection explicitly (`WithAuthoritativeBranch`), which also covers
    bare repositories. The two agree.
  - TASK-002's `resolveRev` passes `HEAD` through unchanged, so this rewrite runs before it.
- This moves "retargeting HEAD-relative reads" from Out of scope into scope for standalone.
  Serializing reads stays out (Decision 14).

### 3. P3-27 — bounded merge retries and a bounded multi-workspace scheduler

**3.1 Domain** (`core/domain`, additive).

- `repository_merge_outcome.go`:
  `func (o RepositoryMergeOutcome) RetriesExhausted(maxRetries int) bool` returns true iff
  `maxRetries > 0`, `o.Status == failed`, `o.FailureClass.IsTransient()` (`:129-135`) and
  `o.Attempts > maxRetries`. `Attempts` counts every recorded attempt for (run, repository),
  the first one included. Within one committing episode, `> maxRetries` therefore means the first
  attempt and `maxRetries` automatic retries have run and the latest failed transiently. Across
  `merge_failed` loop-backs the count carries over (Decision 10).
  `func FirstExhaustedOutcome(outcomes []RepositoryMergeOutcome, maxRetries int) *RepositoryMergeOutcome`
  returns the first in slice order.
- `step.go:39-48`: `FailureMergeRetriesExhausted FailureClassification = "merge_retries_exhausted"`.
  `runtime.step_executions.error_detail` is `jsonb` with no CHECK on the classification
  (`migrations/001_initial_schema.sql:123`).

**3.2 Engine** (`core/engine/merge_retry.go`, new).

```go
// MergeRetriesExhaustedError is the cause recorded when a committing run's
// automatic merge retries are spent (INIT-023 TASK-012, P3-27).
type MergeRetriesExhaustedError struct {
	RepositoryID string
	Attempts     int
	MaxRetries   int
	LastClass    domain.MergeFailureClass
	LastDetail   string
}

// AbandonExhaustedMerge ends a committing run whose recorded merge outcomes show
// the retry budget spent. It routes exactly like a permanent merge failure: a
// workflow with a publish step takes merge_failed (back to execute, per
// task-default), a legacy terminal-commit workflow fails the run (branch kept).
// It returns false, and changes nothing, when the run is not committing or no
// outcome is exhausted.
func (o *Orchestrator) AbandonExhaustedMerge(ctx context.Context, runID string, maxRetries int) (bool, error)
```

- Steps: `GetRun`; status not `committing` → `(false, nil)`. `ListRepositoryMergeOutcomes`;
  `FirstExhaustedOutcome` nil → `(false, nil)`.
- Re-upsert that outcome with `Attempts` and `LastAttemptedAt` unchanged and `FailureDetail`
  prefixed once with `retries exhausted (attempts=N, max_retries=M): `. The class stays the
  transient class (the CHECK at `migrations/022_repository_merge_outcomes.sql:45-50` admits no
  new value, and the last failure *was* transient).
- `observe.AuditLog(ctx, "merge_retries_exhausted", …)`, then
  `handlePermanentMergeFailure(ctx, run, &MergeRetriesExhaustedError{…})` (`merge.go:309-321`).
- `recordGitConflictOnStep` (`merge.go:490-496`): an error that `errors.As` to
  `*MergeRetriesExhaustedError` gets `FailureMergeRetriesExhausted`. The step's `ErrorDetail.Message`
  names the repository, the attempts and the last failure.
- No tree lock: this method does no git work and routes steps (§0).

**3.3 Scheduler gate** (`adapters/scheduler`, additive).

- `options.go`:
  `type CommitExhaustedFunc func(ctx context.Context, runID string, maxRetries int) (abandoned bool, err error)`
  and `func WithCommitExhausted(fn CommitExhaustedFunc) Option`; new field on `Scheduler`.
- `recovery.go`, new `func (s *Scheduler) commitBudgetSpent(ctx, runID string) bool`, called
  before `commitRetryFn` in the committing pass of `retryRunsByStatus` (`:404-407`, only when
  `resumeTrigger == ""`) and in `recoverCommittingRuns` (`:287-290`):
  - `commitMaxRetries <= 0` → false (today's behaviour; every existing test passes `0`).
  - Handler wired → `abandoned, err := s.commitExhaustedFn(ctx, runID, s.commitMaxRetries)`. An
    error is logged and the run is skipped this tick (the next tick re-checks). `abandoned`
    → skip.
  - No handler wired → read `ListRepositoryMergeOutcomes`; if `FirstExhaustedOutcome` is set,
    log at Error ("merge retries exhausted; no exhaustion handler wired; run left in committing")
    and skip. A misconfiguration therefore stops the loop instead of retrying forever.
- The partially-merged resume pass is not gated. An operator resolution resets that path, and its
  own gate (`codeRepoOutcomesAllowResume`, `:422-436`) already prevents flapping.

**3.4 `MultiScheduler`** (`adapters/scheduler/multi.go`).

- `MultiSchedulerConfig` (`:32-36`) gains:
  - `CommitMaxRetries int` (default 3, the standalone value);
  - `WorkspaceBudget time.Duration` (default `TimeoutInterval`, 30 s);
  - `MaxConcurrentWorkspaces int` (default 4).
- `ServiceSet` (`:62-68`) gains `CommitExhaustedFn CommitExhaustedFunc`.
- `forEachWorkspace` (`:144-188`):
  - runs each workspace in its own goroutine behind a semaphore of size `MaxConcurrentWorkspaces`
    and waits for all before returning, so ticks never overlap;
  - releases the pool entry with `defer`;
  - passes `WithCommitRetry(ss.CommitRetryFn, cfg.CommitMaxRetries, 0)` (was `0, 0` at `:173`)
    and `WithCommitExhausted(ss.CommitExhaustedFn)`.
- **Soft budget.** The per-workspace ctx carries a deadline value (unexported key). The
  per-item loops check it between items and stop early with a Warn log (`scan`, `workspace_id`,
  `processed`, `remaining`): `ScanTimeouts` (`timeout.go:26`), `ScanRunTimeouts`
  (`run_timeout.go:36`), `ScanOrphans` (`orphan.go:23`) and `retryRunsByStatus`
  (`recovery.go:358`).
  - It is **not** a ctx deadline. Cancelling ctx would kill git mid-command through
    `exec.CommandContext` (merges in the retry pass; `FailRun` → branch cleanup in the orphan pass
    at `orphan.go:35-36`). That can leave a stale lock file which `classifyGitError` reports as
    "repository locked" (transient) forever.
  - Single-workspace mode sets no budget, so it is unchanged.

**3.5 Wiring.**

- `serve_builders.go:313-315`: keep `WithCommitRetry(…, 3, 2*time.Minute)` and add
  `scheduler.WithCommitExhausted(orch.AbandonExhaustedMerge)`. Name the constant
  `defaultCommitMaxRetries = 3`.
- `serve_resolver.go:166-168`: also set `ss.CommitExhaustedFn = orch.AbandonExhaustedMerge`.
  `service/internal/workspace/pool.go:71` gains the matching field. These exist so a future wiring
  of `MultiScheduler` gets the bound; wiring `MultiScheduler` into `spine serve` is out of scope
  (owner question 3).

> **Design-check note (2026-10-06): what the bound counts, and what it does not.**
> - **Counted.** Only attempts that reach `recordPrimaryMergeOutcome`, or a code-repo outcome,
>   with a transient class.
> - **Holds are not counted.** A governance hold (TASK-006) returns from `governanceGate` before
>   `mergePrimary` and records no outcome. That is what TASK-006 asks of this task; AC 16 pins it.
>   TASK-017's lifecycle refusals are permanent, so they are not counted either.
> - **Uncounted transient exits stay unbounded.**
>   - The lock-wait rule (§2.7), by design.
>   - Outcome-persistence errors (`merge.go:187-189`, `:230-232`, `:254-256`), which mean a store
>     outage.
>   - Two exits that sibling tasks add: TASK-005's transient `VerifyCommit` → `retryMerge` with no
>     outcome, and TASK-017's retryable read failures that "keep the run in `committing`".
>
>   **Cross-task requirement:** those two exits record a failed primary outcome with the transient
>   class before `retryMerge`, so the bound applies to them.
> - **Decision 10, stated precisely.** After a run's first exhaustion, `attempts` is already above
>   the bound. Every later committing episode of that run is therefore abandoned at its first
>   transient failure: zero automatic retries, not merely "fewer" (owner question 4).

### 4. Documentation

- `architecture/git-integration.md` §6.2 item 3 (`:338`): "After convergence, the selected branch
  is merged into the **run (task) branch**, never into the authoritative branch. It reaches the
  authoritative branch only through the governed merge after the run's evaluation step."
- `architecture/git-integration.md`, new §6.6 "Shared working tree (standalone)": the lock, the
  home branch, the restore and quarantine, the git-only rule, and that a governed merge holds the
  tree across its push.
- `architecture/divergence-and-convergence.md` §4.5 (`:403-416`): convergence merges the
  selected branches into the run branch *before* the evaluation step, so the evaluator reviews
  the converged content.
- `architecture/engine-state-machine.md:61-62`: the retry budget (`commitMaxRetries`, counted
  from `repository_merge_outcomes.attempts`), the `merge_retries_exhausted` classification, and
  that exhaustion routes like `git.commit_failed_permanent` / `merge_failed`.
- `KNOWN-LIMITATIONS.md` "Architecture Deviations": standalone serializes stateful operations on a
  workspace's tree; a restore quarantines tracked changes in `SPINE_REPO_PATH` (relevant when an
  operator points standalone at a checkout they also edit); attempts are counted per (run,
  repository), not per committing episode; HEAD-relative reads are not serialized (TASK-011).
- `CHANGELOG.md` staging section: a "Security" entry.

> **Design-check note (2026-10-06): more for the documentation.**
> - `KNOWN-LIMITATIONS.md` also states:
>   - one `spine serve` process per repository path. The lock is in-process, so a second process
>     or an operator's git is not serialized, and a restore may quarantine its in-flight work;
>   - git's own detached auto-gc can still contend for ref locks. That failure is transient and
>     bounded by §3;
>   - a `.git/index.lock` left by a crash is reported, not removed (owner question 5).
> - `architecture/git-integration.md` §6.6 also covers SIGTERM on cancel, the detached and bounded
>   sequence, `HEAD` = home (§2.9), and divergence branches forking from the run branch (§1.4
>   note). §6.2 item 1 says so explicitly.

### 5. Data, format and migration

- No migration and no new table or column.
- New values: `FailureMergeRetriesExhausted` in step `error_detail` (`jsonb`), the
  `retries exhausted (…)` prefix in `repository_merge_outcomes.failure_detail`, and the
  `working_tree_restored` / `merge_retries_exhausted` audit log entries.
- Quarantine commits are unreferenced objects; nothing is pushed or advertised.

### 6. Backward compatibility (Spine)

- **API changes:**
  - `engine.ConvergenceHandler.EvaluateAndCommit` and
    `divergence.Service.EvaluateAndCommit` / `CommitConvergence` gain `run *domain.Run`;
  - `CLIClient.Merge` refuses an empty `Target`.
- **Everything else is additive:** `git.WorkingTreeGuard`, `WithHomeBranch`,
  `RestoreWorkingTree`, `TreeRestoreReport`, `TreeRestoreError`, `WithCommitExhausted`,
  `CommitExhaustedFunc`, the `MultiSchedulerConfig` and `ServiceSet` fields,
  `AbandonExhaustedMerge`, `MergeRetriesExhaustedError`, and the domain helpers.
- **Existing test edits** (exactly these; verified with the prototype):
  - `adapters/git/cli_test.go:97` and `:144` gain `Target: "main"`;
  - `core/divergence/convergence_test.go:340`, `:388` and `:428` pass a run with a `spine/` branch;
  - `core/engine/divergence_test.go:161`, the mock's signature.
- **Behaviour in standalone:**
  - one governed merge, run start, convergence or in-place write at a time per workspace tree;
  - an HTTP submit that triggers a merge may wait for another merge's push;
  - transiently failing merges end after 3 automatic retries.

> **Design-check note (2026-10-06): compatibility of the design-check changes.**
> - **More Spine API changes.**
>   - `divergence.Service.CreateExploratoryBranch`, `engine.DivergenceHandler.CreateExploratoryBranch`
>     and the gateway's `BranchCreator` gain `run *domain.Run` (§1.4 note).
>   - Additive: `(*WriteScope).Context` and `TreeWaitError`.
> - **More existing-test edits.** The exploratory-branch tests and the fakes that implement those
>   interfaces gain the parameter (`core/divergence/*_test.go`, `service/internal/gateway` fakes).
>   The Implementation Notes list them; AC 20 is amended accordingly. A scenario that checks out a
>   non-`main` branch in the harness tree and then reads `HEAD` needs `WithoutHomeBranch()` with a
>   stated reason.
> - **More standalone behaviour changes.**
>   - A client disconnect no longer cancels a governed merge that has already taken the tree; the
>     merge completes (bounded).
>   - Divergence branches fork from the run branch.
>   - API reads of `HEAD` see `main`.
> - **SMP** compiles unchanged: SMP implements neither interface and calls neither method (checked
>   by grep).
>   - SIGTERM-on-cancel reaches SMP's `CLIClient` users (projection reads, the artifact and
>     workflow writers). A cancelled git now exits through its signal handler instead of being
>     killed. No result changes.
>   - SMP registers no home branch, so it gets no detachment, no `HEAD` rewrite and no restore.
>   - The §2.7 run-status re-check is SMP-visible. In SMP, `withTree` runs `fn` directly, so the
>     re-check still happens. That costs one extra `GetRun` per cascade and per primary merge. A
>     run that is no longer `committing` is skipped instead of being merged against a deleted
>     branch. SMP TASK-012's gate covers it.
>   - On a platform where `Process.Signal(SIGTERM)` is unsupported, `cmd.Cancel` falls back to
>     `Process.Kill` (today's behaviour).
>   - Re-checked for this review against the prototype overlay:
>     `go build ./cmd/... ./internal/... ./scenariotest/... ./dev/...` and
>     `go test ./internal/customer/spinecore/...` pass.

### 7. SMP compatibility and the dependent SMP task

**Compatibility gate** (all verified against a prototype of this design, §8):

1. **SMP never calls `CLIClient.Merge`.** SMP's `CLIClient` users
   (`projectionwriter/worker.go:312`, `workspace/provision_steps_runtime.go:320`,
   `spinecore/runbranchdiffreader.go:176`, `spinecore/artifactservice.go:306` and `:447`,
   `spinecore/workflowwriter.go:335`) call only reads, `MergeBase` and `Push`, plus the services'
   in-place writes. Governed merges go through `RealGitOperator.Merge`
   (`spinecore/gitoperator.go:376`), which is not touched.
2. **Convergence with an explicit `Target`.** SMP wires `divergence.Service` with
   `divergenceGitClient` (`spinecore/factory.go:245-247`). A non-empty `Target` passes through
   unchanged (`divergencegitclient.go:294`), then `op.Merge` and `Push("origin", Target)` run.
   That is the same run branch the adapter resolved itself before (`:153-183`).
   - The new guard accepts SMP's run branches (`spine/run/…` and `spine/plan/…`, generated by
     Spine's `StartRun`).
   - The convergence abort is never sent to SMP's adapter (§1.2).
3. **Signature change.** SMP passes `*divergence.Service` to `WithConvergence` and implements
   neither handler interface, so it compiles unchanged.
4. **The engine guard is inert in SMP.** `RealGitOperator` does not implement
   `git.WorkingTreeGuard`, so every engine sequence runs exactly as today, still serialized by
   SMP's own per-workspace `writeGateMu` and per-request worktrees.
5. **Lock scope in SMP.** SMP never sets `WithHomeBranch`, so no restore ever runs in SMP. Its
   in-place artifact and workflow scopes take an uncontended lock on a per-request worktree path,
   and the registry entry is removed at release (no growth). SMP does not call `git.EnterBranch`
   itself.
6. **The scheduler change does not reach SMP.** SMP does not import `adapters/scheduler`
   (verified: no `WithCommitRetry` or scheduler import).
7. **`go build`, `go vet ./internal/customer/...` and the `spinecore`, `spineembedded`,
   `projectionwriter` and `workspace` tests pass** with SMP `80995559` against the prototype,
   with no SMP edits.

**What SMP must change: nothing.** SMP TASK-012 (the `.spine-revision` bump to INIT-023's closing
commit) picks this task up and runs SMP's full gates. Optional, recommended for SMP TASK-012:
`divergenceGitClient.Merge` may now refuse an empty `Target` instead of resolving it, because
Spine always sends one. That turns a future Spine regression into an error rather than a
silent fallback.

### 8. Design validation

A prototype of §1, §2 and the engine part of §2.7 was built as `-overlay` copies (no repository
edit) under SMP `tmp/init027-orchestration/spine-init023/_work/task012/` (see `README.txt` there;
§3 was not prototyped). Results on this machine (git 2.54, Go 1.26.5):

- The engine-level inverted PoCs (AC 1, 6, 7, 8, 9) **fail on unmodified `61994fe`** (a copy
  without the new `WithHomeBranch` option) and **pass** with the prototype. AC 2–4, 10 and 11
  need the new API, so their "before" evidence is the original PoCs passing on `61994fe` plus the
  mutation runs.
- `go test ./...` in Spine passes (38 packages, 0 failures). The scenario suite passes, but only
  11 scenarios ran; 253 skip without the test Postgres.
  `go test -race` on `adapters/git`, `core/engine` and `core/divergence` is clean.
- The SMP gate (§7) passes.
- Mutants M1, M2, M3, M5, M6 and M7 of AC 18 were run; each fails exactly the listed tests.
- The prototype found three issues, now fixed in this design:
  - whole-`MergeRunBranch` locking would hold the tree across blocking event emission (§0);
  - the convergence abort would push `HEAD` through SMP's adapter (§1.2);
  - the "cut from a foreign branch" check must use `main`'s first-parent history, not a tip
    comparison (AC 8).

### 9. Coordination with sibling tasks

- **TASK-002 (transitive blocker).** Owns the argv of `Merge`, `Checkout` and `CreateBranch`,
  and the abort form. This task adds the lock, the `Target` rule and the restore. The restore's
  checkout uses TASK-002's branch-checkout form.
- **TASK-004 (blocker).** Owns `WriteAndStageFile`'s confined write. This task adds the lock in
  front of it, and the restore cleans the partially staged cascade TASK-004 handed over (its §4).
- **TASK-005 (blocker).** Owns `MergeBound`, `CommitFilesOnto` and `PushCommitCAS`; §2.7 maps
  them into the primary block.
  - **Lease mismatch stays permanent**, answering TASK-005's handoff. In standalone the local
    `main` already holds the unpublished merge, so a retry recomputes "already up to date" against
    the same lease and is rejected again. Reclassifying it as transient would only add futile
    retries, now bounded by §3. Re-syncing local `main` from origin before a retry is a
    follow-up.
    *(Design-check note (2026-10-06): superseded. TASK-005's DC-1 removed the lease; see §2.7
    note 3.)*
  - TASK-005 §5 rebinds after convergence. With F8 fixed, convergence now moves the run branch
    in standalone too, which is what that rebind expects.
- **TASK-006.** Its governance-path guard reads git only; it runs outside the blocks.
- **TASK-008.** Its compare-and-swap run-status writes also close the residual race between
  `AbandonExhaustedMerge`'s routing (blind `UpdateRunStatus` in `failRunOnMergeError`,
  `merge.go:457`) and a concurrent completion.
- **TASK-011.** Owns P3-22: the projection follows `refs/heads/main` instead of `HEAD`. This task
  shrinks the windows in which HEAD is off `main` to the git-only blocks, but does not serialize
  HEAD-relative reads.
- **TASK-016.** Decides who may push `spine/<run>/<div>/<branch>`. After this task such content
  reaches only the run branch and its review.
- **SMP TASK-012.** Revision bump and gates (§7).

> **Design-check note (2026-10-06): coordination updates.**
> - **TASK-004** asks this task's acceptance to cover a multi-path cascade in which `worktreefs`
>   refuses a later path after earlier paths were already staged (its §4 note). AC 9b.
> - **TASK-005.**
>   - DC-1 renamed `PushCommitCAS` to `PushCommit` and added `CommitParents` (§2.7 note 3).
>   - Its refusal-path rollback runs inside the primary block (§2.7 note 2).
>   - Its transient `VerifyCommit` exit should record an outcome (§3 note).
> - **TASK-006** asked that holds not be counted. That is satisfied structurally (§3 note,
>   AC 16). Its §3.4a rollback is the same in-block requirement as TASK-005's.
> - **TASK-011** pins the projection. §2.9 makes `HEAD` mean `main` for every other standalone
>   reader. Together they close P3-22 and the read half of F9. Until TASK-011 lands, §2.9 already
>   covers the projection's `Head()` in standalone.
> - **TASK-014.** Its update pre-check read and the run-branch base (`multi_repo_branch.go:90`)
>   both resolve to `refs/heads/main` in standalone (§2.9 and the run-start restore).
> - **TASK-017.** Its `merge_result_mismatch` uses the same in-block rollback. Its retryable read
>   failures should record an outcome (§3 note).

## Decisions

1. **The lock is keyed by the canonical tree path, process-wide.** A per-instance mutex does not
   cover two clients on one tree: standalone hands the same path to several constructors
   (`serve_cmd.go:125`, `pool.go:839`), and an evicted `ServiceSet` can overlap its replacement.
   The refcounted registry keeps SMP's per-request paths from accumulating.
2. **Git-only blocks, not a lock around the whole `MergeRunBranch`.** The whole-method form was
   prototyped first and works for the PoCs. It was rejected because it holds the tree across
   `emitEvent` and step routing, and `Publish` blocks on a full queue (§0). The blocks keep every
   invariant the PoCs test (AC 6–9).
3. **Not per-run worktrees** (SMP's model). That would remove the shared tree, but it changes
   where standalone's runners, git HTTP endpoint and projection read from, and every engine path.
   A serialized tree with self-heal fixes the finding with a contained change.
4. **Quarantine, not hard reset or refusal.** `git reset --hard` destroys whatever is in the tree,
   which matters because `SPINE_REPO_PATH` defaults to `.` (`serve_cmd.go:103-106`). Refusing
   keeps today's DoS (every later merge fails until an operator acts). A ref-backed quarantine
   (`refs/stash`, `refs/spine/…`) would be advertised to every fetcher; `stash create` is not.
   Owner question 2 offers refusal.
5. **The home branch is client configuration, not an engine constant.** Restoring is only right
   for a tree that is meant to rest on one branch. Making it opt-in keeps SMP, code-repo clients
   and ad-hoc `NewCLIClient` users free of any restore.
6. **The run is passed to convergence, not looked up.** The engine already holds it.
   - *Rejected:* a `RunStore` dependency on `divergence.Service`, which needs new wiring and is
     silently skipped when unwired.
   - *Rejected:* a `parent_branch` column on divergence contexts, which is a schema change.
7. **The convergence abort is limited to shared-tree clients.** SMP's adapter would turn it into
   `Push("origin","HEAD")` (§1.2). SMP's per-call scopes cannot be left mid-merge.
8. **The retry bound is enforced where the knob lives (the scheduler) and decided by the engine
   under the existing ledger.**
   - *Rejected:* changing `CommitRetryFunc`'s signature. It touches about 16 references across
     five test files for no behavioural gain.
   - *Rejected:* an engine-only `WithMergeRetryLimit`. It leaves `commitMaxRetries` dead and caps
     operator-triggered resumes too.
9. **A soft per-workspace budget, not a ctx deadline.** Cancelling ctx kills git mid-command and
   can leave a lock file that turns every later attempt transient (§3.4).
10. **Attempts are counted per (run, repository) lifetime.** A per-episode count needs a new
    column or a reserved `commit_meta` key that TASK-005 has not defined for this. The failure
    direction is safe: a later episode gets fewer automatic retries, never more (owner
    question 4).
11. **Tickets are validated against the live holder.** Plain "ctx says held" reentrancy would let
    a ctx that escaped after release bypass the lock (AC 10e).
12. **Divergence branches keep `"HEAD"` as base** (self-healed to `main`). Forking from the run
    branch would change standalone semantics beyond the finding; it is listed as a follow-up.
    *(Design-check note (2026-10-06): superseded by the §1.4 note. With the convergence target
    fixed, a `main` base turns overlapping run and branch work into a convergence conflict that
    stalls the run. Divergence branches now fork from the run branch, which is also SMP's
    behaviour.)*
13. *(Design check.)* **Detach a taken sequence from the request** instead of making every step
    cancellation-safe. A cancelled checkout cannot be made atomic, and git's own lock cleanup
    needs SIGTERM. Finishing a bounded sequence the server has already started costs at most
    `treeSequenceTimeout`.
14. *(Design check.)* **Rewrite `HEAD` to the home branch for reads; do not serialize reads.**
    Serializing reads would make every API read wait behind merges and pushes. The rewrite costs
    nothing, and SMP is unaffected.
15. *(Design check.)* **Re-check the run status inside the block** rather than adding a per-run
    in-process singleflight. A singleflight would be a second registry and would not cover a
    store-driven second entry. The re-check uses an ordering `completeAfterMerge` already has:
    status first, branch delete second.

## Owner questions

The design works without answers to these, using the stated defaults.

1. **Retry budget.** With 60 s ticks, 3 automatic retries tolerate about 4 minutes of transient
   failure before a publish-step run loops back to `execute`, or a legacy run fails with its branch
   kept.
   - *Default:* 3, the value standalone already configures (`serve_builders.go:315`).
   - *Alternative:* a larger number, or a time budget.
2. **Restore policy for a poisoned shared tree.**
   - *Default:* quarantine and continue. The quarantine SHA is logged and recoverable until `gc`
     prunes it.
   - *Alternative:* refuse every governed operation until an operator cleans the tree. That is
     safer for a hand-edited checkout but keeps F9's denial of service.
3. **Shared-mode merge retries.** In `db` and `platform-binding` modes no scheduler retries
   committing runs at all: `MultiScheduler` has no production caller. This task bounds it but does
   not wire it.
   - *Default:* a follow-up task to wire it (a functional change, not part of this finding).
   - *Alternative:* wire it here.
4. **Retry counting across loop-backs.**
   - *Default:* accept per-run-lifetime counting (Decision 10).
   - *Alternative:* a per-episode counter, which needs a migration.
   - *(Design check, 2026-10-06.)* Precisely: after a run's first exhaustion, each later committing
     episode of that run gets **zero** automatic retries. Its first transient failure is
     abandoned on the next tick and loops back again. One network blip in a reworked run
     therefore costs a full rework cycle.
5. **(Design check, 2026-10-06) A stale `.git/index.lock` after a crash.** With SIGTERM and the
   detached sequence, Spine itself no longer leaves one behind. A process or host crash still can.
   - *Default:* the restore fails with a `TreeRestoreError` that names the file, every governed
     merge in the workspace fails transiently (bounded by §3), and an operator deletes the file.
   - *Alternative:* the restore deletes the file automatically, but only while it holds the
     in-process lock and only when the file's mtime predates the process start. This is safe
     under the single-process assumption and unsafe if an operator runs git in the tree at the
     same time.
6. **(Design check, 2026-10-06) The sequence bound.**
   - *Default:* `treeSequenceTimeout` = 5 minutes, a constant. It also bounds a hung push, which
     is today's "network timeout for `git push`" gap.
   - *Alternative:* a configuration value, for very large pushes.

## Acceptance Criteria

Unless stated otherwise, tests use the real `git.CLIClient` on temporary repositories. Shared-tree
clients are built with `git.WithHomeBranch("main")`, as production builds them. Test names are
binding (the mutation table refers to them).

1. **F8 regression, engine path** (from `poc/verify-r1-11/engine_convergence_main_test.go` and
   `poc/skeptic_conv`).
   - Test: `core/engine/shared_worktree_regression_test.go`,
     `TestF8_ConvergenceMergesIntoRunBranchNotMain`.
   - Setup: the PoC's wiring with `orch.git` and the divergence service sharing one client, as
     production shares it. A worker pushes `UNREVIEWED.md` to the divergence ref and submits its
     branch step.
   - Result:
     - `main` is unchanged and lacks `UNREVIEWED.md`;
     - the run branch contains it;
     - the divergence context is `resolved`;
     - the tree is on `refs/heads/main` and clean.
2. **F8 regression, divergence service** (from `poc/governance/convergence_main_test.go`).
   - Test: `core/divergence/convergence_target_test.go`,
     `TestF8_StandaloneConvergenceTargetsRunBranch`.
   - `EvaluateAndCommit(ctx, run, divCtx, convDef)` leaves `main` unchanged, and
     `branch-output.md` is on the run branch only.
3. **The F8 guard.** `TestF8_ConvergenceRefusesMissingOrForeignTarget` runs these cases in this
   order, each in its own subtest:
   - another run's existing `spine/` branch;
   - `BranchName: "main"`;
   - an empty branch;
   - a nil run (last, so a nil dereference cannot hide the other cases).

   Each returns an error, and `main`, the run branch and the foreign branch are unchanged.
   *(Design check, 2026-10-06.)* In each case the divergence context status is also unchanged
   (`active`, not `converging`), and `UpdateDivergenceContext` is never called. The guard runs
   first in `EvaluateAndCommit` (§1.2 note).
4. **`CLIClient.Merge` target rule** (`adapters/git`).
   - `TestF8_MergeRefusesEmptyTarget`: the error is a non-retryable `*GitError`, and `main` is
     unchanged.
   - `TestF8_MergeAbortStillWorksWithoutTarget`: a conflicted merge is aborted with
     `{Source: "--abort", Strategy: "abort"}`, and `MERGE_HEAD` is gone.
   - `{Strategy: "abort", Target: "x"}` is refused before git runs.
   - *(Design check, 2026-10-06.)* `{Source: "--abort"}` with an empty `Strategy` runs exactly
     `git merge --abort` and is not refused (TASK-002 parity). `{Source: "--abort", Target: "x"}`
     is refused before git runs.
5. **Convergence abort scope.**
   - **Shared tree.** A divergence branch that conflicts with the run branch leaves no
     `MERGE_HEAD`, and HEAD is on `main`. `CommitConvergence` returns the merge error and the
     divergence context is not `resolved`.
   - **Non-guard client.** A recording fake (not a `git.WorkingTreeGuard`) sees exactly one
     `Merge` with `Target == run.BranchName` and no abort call, even when that merge fails.
6. **F9 regression, concurrent merges** (from `spine_worktree_race_poc_test.go`
   `TestPOC_ConcurrentGovernedMergesRace` and `verify-r2-2` `TestVerifyR22_ConcurrentMerges`).
   - Test: `TestF9_ConcurrentGovernedMergesSerialize`, 25 iterations of two concurrent
     `MergeRunBranch` calls on one shared client.
   - Every iteration:
     - both runs end `completed`, with their content on `main`;
     - no "Update artifact status" commit is on `main`'s first-parent line;
     - the tree is on `refs/heads/main` and clean.

   **6b.** *(Design check, 2026-10-06.)* **F9 regression, the same run entered twice.** This is
   inverted from `_work/task012-check/zz_t012chk_sameRun_test.go`.
   - Test: `TestF9_SameRunDoubleEntryNeverFailsACompletedRun`.
   - The store's `GetRun` returns a copy, as Postgres does; the engine mock's shared pointer would
     hide the race.
   - Entry B is paused after `preflightCheck` (a pausing branch-protection policy). Entry A runs
     `MergeRunBranch` to completion, so the run is `completed` and its branch is deleted. Then B
     resumes.
   - Result: the run stays `completed`, the primary outcome stays `merged`, B emits no
     `run_failed`, and B returns nil.
   - It fails on `61994fe` and on the prototype (`completed → failed`).
7. **F9 regression, atomic cascade** (from `TestPOC_StatusCascadeCommitLandsOnMain`).
   - Test: `TestF9_StatusCascadeIsAtomicAgainstConcurrentMerge`.
   - While run A is paused after `Checkout(run-a)` inside its cascade, run B's `MergeRunBranch`
     (in another goroutine) does not return within 500 ms. After A resumes, both complete, and no
     status commit is on `main`'s first-parent line.
8. **F9 regression, run-branch base** (from `TestPOC_NewRunBranchCutFromForeignRunBranch`).
   - Test: `TestF9_RunBranchNeverCutFromForeignRunBranch`.
   - A `CreateBranch("spine/run/run-c", "HEAD")` started in another goroutine while A holds the
     tree does not return within 500 ms.
   - Afterwards `spine/run/run-c` is not run A's pre-merge tip and is on
     `git rev-list --first-parent main`.
9. **F9 regression, self-heal** (from `spine_poison_poc_test.go` and `verify-r2-2`
   `TestVerifyR22_PoisonPersists`).
   - Test: `TestF9_PoisonedTreeSelfHealsAtNextMerge`.
   - Setup: the audit's poisoned state — HEAD on `spine/run/run-a` with a staged edit.
   - Four serial governed merges (runs c, d, e, f) each:
     - end `completed`, with their content on `main`;
     - leave the tree on `main` and clean.
   - No run-a content reaches `main`, and `spine/run/run-a` is unchanged.
   - A Warn record with a non-empty `quarantine_sha` is emitted once.

   **9b.** *(Design check, 2026-10-06; TASK-004 handoff.)* **A multi-path cascade refused part-way.**
   - Test: `TestF9_PartiallyStagedCascadeIsQuarantined`.
   - The run branch adds two artifacts. The second sits under a committed symlinked directory,
     which `worktreefs` refuses after the first path is staged.
   - Result:
     - `MergeRunBranch` proceeds (today's log-and-proceed);
     - no cascade edit is on `main`'s first-parent line;
     - the tree is on `refs/heads/main` and clean;
     - the next unrelated governed merge completes.
10. **Lock unit tests** (`adapters/git/treelock_internal_test.go`; also run under `-race`).
    - (a) Nested `Checkout` inside `WithWorkingTree` does not deadlock.
    - (b) A second client on the same directory, spelled differently (`repo/.`), blocks while the
      first holds the tree.
    - (c) Waiting returns the ctx error when ctx ends.
    - (d) The registry is empty after all holders release.
    - (e) A ticket-carrying ctx captured inside a released `WithWorkingTree` does not bypass a
      later holder.
    - (f) `Checkout` blocks while an in-place `EnterBranch(dir, "")` scope is open and proceeds
      after `Cleanup`.
    - (g) *(Design check.)* Inside an in-place scope, a locked call made with `scope.Context(ctx)`
      completes without waiting. The same call made with the plain ctx still blocks until its
      deadline, which documents the trap.
    - (h) *(Design check.)* With an already-cancelled ctx and a free lock, 200 `WithWorkingTree`
      calls run `fn` 0 times and each returns a `*TreeWaitError`. The prototype ran `fn` 91 of 200
      times.
    - (i) *(Design check.)* On a home-branch tree, `fn` receives a ctx that is not cancelled when the
      caller's ctx is (`fn` observes `ctx.Err() == nil` after the caller cancels) and that carries
      a deadline no later than `treeSequenceTimeout`.
11. **Restore semantics** (`TestF9_RestoreQuarantinesAndReturnsToMain`,
    `TestF9_HomeBranchSelfHealsOnlyWhenConfigured`).
    - **Quarantine.** From a foreign branch with a staged edit and an untracked file:
      - `RestoreWorkingTree` reports `PreviousRef == refs/heads/feature` and a `QuarantineSHA`
        whose tree holds the edit;
      - HEAD is `refs/heads/main`;
      - the untracked file still exists;
      - `git for-each-ref` lists no new ref.
    - **No-op.** A second restore reports `Repaired() == false`.
    - **Merge in progress.** A tree with `MERGE_HEAD` is aborted (`AbortedMerge`).
    - **Home branch configured.** `CreateBranch(x, "HEAD")` and `EnterBranch(dir, "")` from a
      poisoned shared tree start from `main`.
    - **No home branch.** On a tree that no client registered a home branch for,
      `WithWorkingTree` never moves HEAD.
    - **Restore failure.** An untracked file that blocks the checkout makes
      `WithWorkingTree` return a `*TreeRestoreError` without running `fn`.
    - **Stale lock.** *(Design check.)* With `.git/index.lock` present (created by the test),
      `RestoreWorkingTree` returns a `*TreeRestoreError` whose message names `index.lock`, and
      whose unwrapped `*GitError` is retryable. `WithWorkingTree` does not run `fn`. The file is
      not deleted. The prototype returned success here.
12. **No events or routing under the tree lock.**
    - The probe: an `EventEmitter` fake (engine) and an `event.EventRouter` fake (divergence).
      On each `Emit`, it starts a goroutine that calls
      `WithWorkingTree(context.WithTimeout(bg, 1s), noop)` through a second client on the same
      tree, waits for it, and records a violation if it timed out.
    - It is run over:
      - `MergeRunBranch` success, transient, and permanent (conflict) paths;
      - `StartRun`;
      - convergence;
      - an in-place `artifact.Service.Create`.
    - No violation is recorded.
13. **Lock-wait cancellation is not a merge failure.** `MergeRunBranch` with a ctx that is
    cancelled while another holder keeps the tree:
    - returns the ctx error *(design check: a `*TreeWaitError` that unwraps to it)*;
    - records no outcome row (none existed before);
    - leaves the run `committing`.
14. **Entry-restore failure is a recorded attempt.** With the restore made to fail (an untracked
    file blocking `checkout main`), `MergeRunBranch`:
    - records a failed primary outcome (`attempts` = 1);
    - routes by its class: a permanent class goes to the publish step's `merge_failed` or fails a
      legacy run.
15. **P3-27, engine** (`core/engine/merge_retry_test.go`).
    - Setup: a wrapper client that fails `Push` with a transient `*GitError`, and auto-push on.
      *(Design check: run it twice, once with binding off failing `Push`, and once with binding on
      failing TASK-005's `PushCommit`.)*
    - Repeated `MergeRunBranch` calls yield `attempts` 1, 2, 3, 4 with class `network`.
    - `AbandonExhaustedMerge(ctx, id, 3)` returns:
      - `false` (and changes nothing) at `attempts` 3;
      - `true` at 4.
    - After `true`:
      - **legacy workflow:** the run is `failed`, and the last completed step's `ErrorDetail`
        has `Classification == "merge_retries_exhausted"`;
      - **publish-step workflow** (an internal merge step with `merge_failed → execute`): the run
        is `active` at `execute`;
      - the outcome row keeps `attempts` 4 and the transient class, and its `failure_detail`
        starts with `retries exhausted (attempts=4, max_retries=3): ` exactly once.
    - A second call returns `false`.
    - `maxRetries` 0 always returns `false`.
16. **P3-27, scheduler** (`adapters/scheduler`, fake store).
    - With `WithCommitRetry(fn, 3, 0)` and `WithCommitExhausted(ex)`:
      - an exhausted run calls `ex(ctx, id, 3)` and **not** `fn`;
      - a non-exhausted run calls `fn`;
      - an `ex` error calls neither and logs.
    - With `maxRetries` 0, `ex` is never called. The existing tests in `recovery_partial_test.go`
      and `scenarios/partial_merge_retry_test.go` pass unedited.
    - With no `ex` wired and an exhausted outcome, `fn` is not called and an Error is logged.
    - `RecoverOnStartup`'s committing pass applies the same gate.
    - *(Design check; TASK-006.)* A committing run with no outcome row, whose `fn` keeps returning
      a hold error, is never abandoned over 10 ticks: `ex` returns `false` every time and `fn` is
      called every tick.
    - **Domain table** for `RetriesExhausted`: status × class (transient and permanent) ×
      attempts around the bound × `maxRetries` 0.
17. **P3-27, `MultiScheduler`.**
    - Concurrency: with 10 workspaces whose scan blocks on a barrier, at most
      `MaxConcurrentWorkspaces` run at once (observed maximum), and `forEachWorkspace` returns only
      after all finish.
    - Budget: a workspace with 100 committing runs and `WorkspaceBudget` 50 ms, whose retry `fn`
      sleeps 10 ms, stops after fewer than 100 calls and logs `remaining > 0`. Its ctx is never
      cancelled (`fn` observes `ctx.Err() == nil`).
    - Each per-workspace `Scheduler` gets `commitMaxRetries == 3` by default and the
      `ServiceSet`'s exhaustion function.
18. **Fails before the fix, mutation-checked.** The Implementation Notes record the PoCs passing
    (vulnerable) on `61994fe` and each mutation below failing at least the listed tests.

    | Mutation | Fails |
    |---|---|
    | M1: lock acquisition always treated as held (no-op lock) | AC 1, 6, 7, 8, 9, 10, 11 |
    | M2: restore is a no-op | AC 1, 9, 11 |
    | M3: `CommitConvergence` sends `Target: ""` | AC 1, 2 |
    | M4: `CLIClient.Merge` with empty `Target` merges into HEAD again (with M3) | AC 1, 4 |
    | M5: guard reduced to `run != nil` | AC 3 (other-run, `main` cases) |
    | M6: `EnterBranch(dir, "")` takes no lock | AC 10f, 11 |
    | M7: any ticket for the key is honoured | AC 10e |
    | M8: call `handlePermanentMergeFailure` inside the primary-merge block instead of after it | AC 12 |
    | M9: `commitBudgetSpent` always false | AC 16 |
    | M10: `RetriesExhausted` uses `>=` | AC 15, 16 |
    | M11: `MultiScheduler` uses a ctx deadline instead of the soft budget | AC 17 |
    | M12 *(design check)*: git children keep the default SIGKILL cancel | AC 24a |
    | M13 *(design check)*: `WithWorkingTree` passes the caller's ctx to `fn` | AC 10i, 24b |
    | M14 *(design check)*: no run-status re-check in the blocks | AC 6b |
    | M15 *(design check)*: guard only in `CommitConvergence` | AC 3 (status unchanged) |
    | M16 *(design check)*: no `HEAD` → home rewrite | AC 25 |
    | M17 *(design check)*: refusal rollback after the block is released | AC 26 |
    | M18 *(design check)*: divergence base stays `"HEAD"` | AC 27 |
    | M19 *(design check)*: `{Source: "--abort"}` without `Strategy` is refused | AC 4 |
    | M20 *(design check)*: the restore ignores `.git/index.lock` | AC 11 (stale lock) |

    M1, M2, M3, M5, M6 and M7 were already run against the design prototype (§8).
    *(Design check: the M12–M20 "fails" columns are predictions. Probes 1–6 in
    `_work/task012-check/` show that each listed AC's scenario fails on the prototype, which
    behaves like these mutants.)*
19. **Wiring.**
    - `serve_cmd.go` and `pool.go` build the workspace primary client with
      `WithHomeBranch("main")`, and gitpool code-repo clients without it. A test asserts this
      through the `ServiceSet`.
    - `buildScheduler` and `workspaceOrchestratorBuilder` wire the exhaustion handler.
    - The scenario harness repo client sets the home branch. Every `WithoutHomeBranch()` use
      carries a reason and is listed in the Implementation Notes.
20. **Existing tests.** The only edits to existing test files are those listed in §6. All other
    tests under `./...` pass unchanged.
    *(Design check: plus the `CreateExploratoryBranch` signature edits listed in the §6 note.
    Every scenario that uses `WithoutHomeBranch()` is listed with its reason.)*
21. **Documentation.** The §4 changes are present.
22. **The SMP compatibility gate** passes with SMP `main` at `80995559` (or the then-current SMP
    `main`) and `../spine` on this task's branch, with no SMP edits:
    - `go build ./cmd/... ./internal/... ./scenariotest/... ./dev/...`;
    - `go vet ./internal/customer/...`;
    - `go test ./internal/customer/spinecore/... ./internal/customer/spineembedded/... ./internal/customer/projectionwriter/... ./internal/customer/workspace/...`.

    The results are recorded in the Implementation Notes.
23. **Gates.** All of these pass:
    - `go build ./...`, `go vet ./...` and `go test ./...`;
    - `go test -race ./adapters/git/... ./core/engine/... ./core/divergence/... ./adapters/scheduler/...`;
    - `make test-scenario` **with the test database up** (`SPINE_TEST_DATABASE_URL`). Without it,
      253 of 264 scenarios skip;
    - `make lint`, including `lint-boundaries`, and `golangci-lint run --new-from-rev main`;
    - `make lint-security`, with no new gosec finding;
    - `gofmt -l` lists no file this task adds or changes, beyond the import-order differences
      that already exist at `61994fe` in `core/divergence/convergence.go`,
      `core/engine/merge.go`, `core/engine/interfaces.go` and `core/engine/multi_repo_branch.go`;
      *(Design check: `gofmt -l` at `61994fe` also lists five more files this task edits, all
      for import order: `core/engine/merge_resolution.go`, `core/artifact/service.go`,
      `adapters/scheduler/recovery.go`, `service/cmd/spine/serve_builders.go` and
      `service/internal/workspace/pool.go`. Either add them to this exemption or reformat their
      imports in a separate commit.)*
    - `codex review --base main` has no P1 or P2.
24. **(Design check, 2026-10-06) Cancellation never poisons the tree.**
    - (a) `adapters/git`, `TestF9_CancelledGitLeavesNoLockFile`. The repo config sets a smudge
      filter `sleep 2; cat` on `*.slow`, and a branch adds `b.slow`. A `CLIClient.Checkout` of that
      branch with a ctx cancelled after 300 ms returns an error, and `.git/index.lock` does not
      exist afterwards. This is deterministic: verified on git 2.54, where SIGKILL leaves the lock
      and SIGTERM does not.
    - (b) `core/engine`, `TestF9_CancelledRequestNeverPoisonsTree`. Setup: run A's branch carries
      the same slow file, on a shared client with a home branch. Cancel `MergeRunBranch(run-a)`
      after 300 ms. Result:
      - no `.git/index.lock`;
      - the tree is on `refs/heads/main` with no tracked changes and no untracked files;
      - run A is `completed` or `committing`, never `failed`, and no outcome has class `unknown`;
      - a following scheduler-style `MergeRunBranch(run-a)` (if still `committing`) and
        `MergeRunBranch(run-b)` both complete.
    - It inverts `_work/task012-check/zz_t012chk_cancel_test.go` and `zz_t012chk_residue_test.go`.
25. **(Design check, 2026-10-06) `HEAD` reads see the home branch** (`TestF9_HeadReadDuringSequenceSeesMain`).
    - Inverted from `zz_t012chk_headread_test.go`. While run A's cascade holds the tree (hooked
      after `Checkout(run-a)`):
      - `ReadFile(bg, "HEAD", "tasks/shared.md")` returns `main`'s `type: Task`;
      - `Head(bg)` equals `rev-parse refs/heads/main`.
    - `StartRun` of `tasks/shared.md` in that window binds the `Task` workflow.
    - On a tree with no home branch, `ReadFile(…, "HEAD", …)` still follows the real `HEAD`.
26. **(Design check, 2026-10-06) A refused bound merge is rolled back before the tree is released**
    (`TestF9_RefusedBoundMergeNeverVisibleToNextHolder`). Requires TASK-005.
    - Setup: binding on, auto-push on to a bare `origin`, and a `verifyBoundMerge` refusal injected
      after `MergeBound`. While the primary block holds the tree, an in-place
      `artifact.Service.Create` waits for it.
    - Result:
      - the artifact commit's parent is `BaseSHA`, never `M`;
      - origin `main` never contains `M`;
      - local `main` equals `BaseSHA` when the outcome is recorded.
27. **(Design check, 2026-10-06) Divergence branches fork from the run branch**
    (`TestF8_DivergenceForksFromRunBranch`). Inverted from `zz_t012chk_divbase_test.go`.
    - A run step edits `README.md` on the run branch, then the divergence starts, and the branch
      edits the same file. Result:
      - `merge-base(divergence branch, run branch)` is the run branch tip at divergence start;
      - `EvaluateAndCommit` succeeds and the context is `resolved`;
      - `main` is unchanged.
    - The same holds for a branch added through `CreateExploratoryBranch(ctx, run, …)`.
    - SMP gate: `divergenceGitClient.CreateBranch` receives `run.BranchName` and forwards it
      unchanged (an SMP unit test at the bump, SMP TASK-012).

## Out of scope / follow-ups

- **Per-run worktrees for standalone** (Decision 3). This would remove the shared tree entirely.
- ~~**Divergence branches forked from the run branch** in standalone, matching SMP (Decision 12).~~
  *(Design check, 2026-10-06: moved into scope. See the §1.4 note and AC 27.)*
- **Wiring `MultiScheduler` into `spine serve` shared modes** (owner question 3).
- **A per-episode retry counter** (owner question 4).
- **Serializing or retargeting HEAD-relative reads.** TASK-011 removes `HEAD` from the projection.
  `artifact.Service.Read` with an empty ref (`core/artifact/service.go:283-286`) and
  `workflow.Service` (`core/workflow/service.go:225`) still read `HEAD`; with this task that is
  `main` outside the git-only blocks.
  *(Design check, 2026-10-06: retargeting is now in scope for standalone (§2.9). Inside a block
  `HEAD` is not `main`, and probe 4 showed a concurrent read seeing a foreign run branch.
  Serializing reads stays out of scope.)*
- **A network timeout for `git push`** (a hung push holds the tree). This is an existing gap; the
  soft budget keeps other tenants moving.
  *(Design check: the detached sequence's `treeSequenceTimeout` now bounds it at 5 minutes, owner
  question 6. A network-level timeout remains a follow-up.)*
- **The `classifyGitError` gap.** curl's "Couldn't connect to server" is classified permanent,
  not transient (observed while designing AC 15).
  *(Design check, 2026-10-06: there is also the bare `"lock"` substring at `errors.go:45`. It
  matches paths in stderr. For example, "The following untracked working tree files would be
  overwritten by merge: blocked-notes.md" was classified "repository locked", which is
  transient, although the refusal is permanent (verified on git 2.54). The §3 bound now caps the
  loop. The follow-up should match `index.lock': File exists` and `.lock': File exists`
  precisely. Content conflicts print to stdout with empty stderr and fall to the default
  permanent case, so they are unaffected.)*
- **Re-syncing local `main` from origin** before a lease-mismatch retry (TASK-005 handoff, §9).
- **Pruning quarantine objects** beyond git's default `gc`.

## Design check (2026-10-06)

The design was checked adversarially against several sources:
- the audit report (F8, F9 and P3-27);
- the PoCs `poc/skeptic_conv`, `poc/verify-r1-11`, `poc/governance`, `poc/gap2-git-state-concurrenc`
  and `poc/verify-r2-2`;
- Spine `61994fe` and SMP `80995559`;
- the sibling designs TASK-002, TASK-004, TASK-005, TASK-006, TASK-011, TASK-014 and TASK-017;
- the designer's prototype in `_work/task012/`.

The probes are in `_work/task012-check/`, run with `go test -overlay` (git 2.54, Go 1.26.8):
- `ov_proto_chk.json` is the prototype plus the probes;
- `ov_proto_chk_sigterm.json` and `ov_proto_chk_detached.json` are the prototype with the
  SIGTERM change, and with SIGTERM plus the detached sequence;
- `ov_base_sameRun.json` is unmodified `61994fe` plus probe 3.

Neither repository was modified.

**Probes (each fails on the prototype unless stated):**

| # | Probe | Result |
|---|---|---|
| 1 | `zz_t012chk_cancel_test.go`: cancel `MergeRunBranch` 15–150 ms into a 12k-file cascade | 4/8 left `.git/index.lock`; the next merge failed `remote_unavailable`; `RestoreWorkingTree` returned nil. With SIGTERM: 0/8. |
| 2 | `zz_t012chk_residue_test.go`: the same, then retry the run | SIGTERM only: up to 575 untracked files, 8/8 runs `failed`. SIGTERM plus detached: no lock and no residue in any run. The 3/8 still `failed` there came from the prototype's recorded lock-wait error, which §2.7 already forbids. |
| 3 | `zz_t012chk_sameRun_test.go`: the same run entered twice | `61994fe` **and** prototype: `completed → failed`, outcome `merged → failed`, `run_failed` emitted. |
| 4 | `zz_t012chk_headread_test.go`: a `HEAD` read during another run's cascade | Returned the foreign run branch's `type: Spike`. |
| 5 | `zz_t012chk_divguard_test.go`: a foreign run passed to `EvaluateAndCommit` | Refused, but the context had already moved `active → converging`. |
| 6 | `zz_t012chk_divbase_test.go`: run work and branch work touch the same file | Convergence conflict; the context is stuck `converging`. |
| 7 | `zz_t012chk_git_test.go` | `{Source:"--abort"}` was refused ("merge target required"). A locked call inside its own in-place scope hung until the deadline. A cancelled-ctx acquisition ran `fn` 91/200 times. |
| — | Scratch (`scratchpad/hookprobe`, `sigkill`) | A slow smudge filter makes the lock-file case deterministic: SIGKILL leaves `index.lock`, SIGTERM does not. Plain checkouts: SIGKILL 21/24 stale, SIGTERM 0/24. |

**Changed, and why:**

| # | Change | Why (evidence) |
|---|---|---|
| DC-1 | SIGTERM and `WaitDelay` for every git child; a taken sequence runs detached and bounded; the restore reports a stale `index.lock` (§0, §2.5 note) | Probes 1 and 2. A client disconnect (A2) recreated F9's permanent merge DoS and reported a healed tree. |
| DC-2 | A run-status re-check opens the cascade and primary blocks (§2.7 note 1, AC 6b) | Probe 3. The finding's own named entry (HTTP submit plus scheduler retry) was not fixed by the lock. |
| DC-3 | The guard runs first in `EvaluateAndCommit`; policy evaluation moves before the block and names the target (§1.2 note, AC 3) | Probe 5. "Before any git or store call" was false. |
| DC-4 | The abort shape matches TASK-002 (§1.3 note, AC 4) | Probe 7. The prototype refused TASK-002's `{Source:"--abort"}`. |
| DC-5 | `WriteScope.Context` hands the ticket to the caller (§2.6 note, AC 10g) | Probe 7. A latent self-deadlock in paths that TASK-004, TASK-014 and TASK-017 edit. |
| DC-6 | `HEAD` on a home-branch tree means the home branch for reads (new §2.9, AC 25) | Probe 4. `StartRun` binds the workflow by a type read at `HEAD`. |
| DC-7 | TASK-005 DC-1 names (`PushCommit`, `CommitParents`); the lease paragraph is superseded (§2.7 note 3, §9, AC 15) | TASK-005's design check asked for it. |
| DC-8 | A refused bound merge's local rollback runs inside the primary block (§2.7 note 2, AC 26) | Otherwise an in-place write's `autoPush` publishes the refused `M` to origin `main`. TASK-005, TASK-006 and TASK-017 all refuse after `MergeBound`. |
| DC-9 | Divergence branches fork from the run branch; `CreateExploratoryBranch` gains the run (§1.4 note, Decision 12, AC 27) | Probe 6. With the fixed target, a `main` base stalls runs. SMP already forks from the run branch. |
| DC-10 | Specify what the bound counts; ask TASK-005 and TASK-017 to record transient exits (§3 note, AC 16) | Two sibling exits would otherwise loop forever. TASK-006's "do not count holds" is met. |
| DC-11 | AC 23's `gofmt` exemption list is incomplete | `gofmt -l` at `61994fe` lists five more files this task edits. |
| DC-12 | Code-repo merge and abort as one guarded sequence (§2.7 note 4) | A cheap guard instead of leaving dormant code unserialized. |
| — | Narrowed "the repair never destroys data"; stated the single-process assumption; added owner questions 5 and 6; sharpened owner question 4; extended the `classifyGitError` follow-up | `reset --hard` fallback; `blocked-notes.md` was classified "repository locked" (git 2.54). |

**Verified as stated (not changed):**
- the Spine line references checked in Purpose, §1–§3 and §9 at `61994fe`, and the SMP ones at
  `80995559` (`divergencegitclient.go:294-305`, `gitoperator.go:387-389`, `factory.go:245-247`,
  the `CLIClient` user list);
- `scheduler.WithCommitRetry` stores `commitMaxRetries` and nothing reads it;
- `MultiScheduler` has no production caller;
- every existing test passes `maxRetries` 0;
- the engine, divergence, artifact and workflow packages start no goroutines;
- `MemoryQueue.Publish` blocks while the queue is full;
- standalone passes the raw `*CLIClient` (through `gitpool.PrimaryClient()`) to the engine and the
  divergence service, so the `WorkingTreeGuard` assertion holds in production;
- SMP's `divergenceGitClient.Merge` and `CreateBranch` behave as §1.2/§7 say;
- SMP compiles against the prototype, and its `spinecore` tests pass;
- the prototype's regression suite passes (`adapters/git`, `core/engine`, `core/divergence`,
  `core/artifact`, `core/workflow`, `adapters/scheduler`).

**Checked and not a bypass:**
- git's `receive.denyCurrentBranch` default (`refuse`) stops a push from rewriting the checked-out
  branch's working tree. The githttp handler never sets `updateInstead`.
- Convergence has no other merge caller.
- `CommitFilesOnto` (TASK-005) moves no ref, so it needs no lock.
- A content conflict prints to stdout and is classified permanent, so the `"lock"` substring
  quirk cannot turn it into a retry loop.

## Implementation Notes

Implemented on branch `init/INIT-023-TASK-012-shared-worktree-safety`, from Spine local `main`
`ae513dca` (origin `61994fe` + the INIT-023 scaffold + waves 1–3 + the codex 6.1 catch-up). Diff
base: `git diff main...HEAD`. Status stays In Progress; the orchestrator closes it after merge.

### Orchestrator rulings recorded (owner questions take their defaults, pending the owner)

- Scope as ruled: audit F9 (standalone shared working tree race / denial of service), with this
  artifact's F8 and P3-27 halves, built on TASK-002's argv/ref grammar, TASK-004's worktreefs and
  TASK-005's merge path (all on `main`). SIGTERM-on-cancel covers TASK-005's `CommitBinder` git
  runner (`runWith`) too: every git child in `adapters/git` is built by one constructor.
- **OQ1** (default, pending owner): retry budget 3 (`defaultCommitMaxRetries`, the value `spine
  serve` already passed; `MultiSchedulerConfig.CommitMaxRetries` defaults to 3).
- **OQ2** (default, pending owner): quarantine and continue; the quarantine SHA is logged (Warn at
  creation, the `working_tree_restored` / `working_tree_restore_failed` audit records) and carried
  in a `TreeRestoreError` when a later step fails.
- **OQ3** (default, pending owner): `MultiScheduler` is bounded (concurrency, soft budget, retry
  budget, exhaustion handler in `ServiceSet`) but **not wired** into `spine serve`; a follow-up.
- **OQ4** (default, pending owner): attempts counted per (run, repository) lifetime from
  `repository_merge_outcomes.attempts`; no migration.
- **OQ5** (default, pending owner): a stale `.git/index.lock` fails the restore with a
  `*TreeRestoreError` whose message names the file (inner `*GitError` transient, "repository
  locked"); it is never deleted.
- **OQ6** (default, pending owner): `treeSequenceTimeout` = 5 minutes, a constant.
- **Rule 4 (SMP-visible shared behaviour):** git children are stopped with SIGTERM on cancel (Kill
  after `WaitDelay` 10 s, or at once where the signal is unsupported), and the engine's cascade and
  primary-merge blocks re-read the run and stand down unless it is `committing`. Neither changes a
  successful result; the SMP gate below runs SMP's suites against this branch.
- No runtime schema change (Rule 1). New values only: step classification
  `merge_retries_exhausted`, the `retries exhausted (…)` failure-detail prefix, audit records.

### What was built

- **`adapters/git`** (commit `0a6bdeb2`, codex fixes `68d51946`, `7dfdb597`, `7bd6663b`):
  - `gitcmd.go`: `newGitCmd` builds every git child in the package (`CLIClient.run`/`runSink`,
    `Clone`, `runWith` — TASK-005's `CommitBinder` runner —, `lookupBranch`, `CheckGitVersion`, the
    `branchwrite.go` helpers): `cmd.Cancel` sends SIGTERM (Kill where unsupported), `WaitDelay` 10 s.
  - `treelock.go`: a process-wide, refcounted lock registry keyed by the canonical tree path (abs,
    symlinks resolved); acquisition hands out a ticket carried in the ctx, honoured only while it is
    the live holder; a done ctx never acquires (checked before and after the wait;
    `*TreeWaitError`). `WorkingTreeGuard` / `(*CLIClient).WithWorkingTree`. `WithHomeBranch`
    registers the tree's home branch (abs and symlink-free path); `HomeBranch()` reports it.
    `restoreHeld` (`RestoreWorkingTree` exported): stale `index.lock` → transient refusal naming the
    file; abort a merge in progress (`reset --hard` fallback); quarantine tracked changes with
    `git stash create` (fixed `Spine` identity, no ref) then `reset --hard`; switch home with
    `switch --no-guess --no-overwrite-ignore`; verify. Before either `reset --hard`,
    `untrackedObstruction` refuses (changing nothing) when an untracked or ignored file stands where
    the reset would rewrite a tracked path. The quarantine SHA is logged at Warn when made and
    carried by a later failure (`TreeRestoreError.QuarantineSHA`, Error log,
    `working_tree_restore_failed` audit). A repair logs `shared working tree restored` (Warn) and the
    `working_tree_restored` audit record. A taken sequence on a home-branch tree runs detached and
    bounded (`treeSequenceTimeout` = 5 min), with entry and exit restores; on any other tree
    `WithWorkingTree` only holds the lock (codex round 1).
  - `cli.go` / `commitbinder.go`: `Checkout`, `Commit`, `Merge` (incl. abort), `CreateBranch`,
    `DeleteBranch`, `WriteAndStageFile`, `MergeBound`/`mergeSHA`, `ResetBranch` hold the lock per call
    (re-entrant). `CreateBranch(…, "HEAD")` that takes a home-branch tree restores it first. `Merge`
    refuses an empty `Target` ("merge target required", permanent, before any git, after Source's
    grammar check); an abort is `Strategy "abort"` or `Source "--abort"` and is refused with a
    `Target`, or with `Strategy "abort"` and another `Source`. On a home-branch tree `ReadFile`,
    `ListFiles`, `TreeEntryType`, `Head`, `Diff`, `Log` (implicit tip and `<since>..`), `MergeBase`,
    `RefSHA` resolve `HEAD` to `refs/heads/<home>` (exact lookup; missing home branch is
    `ErrKindNotFound`).
  - `branchwrite.go`: in-place `EnterBranch(ctx, dir, "")` takes the tree (restoring a home-branch
    tree first) until `Cleanup`; `(*WriteScope).Context(ctx)` hands the scope's ticket to the caller
    (detached and bounded on a home-branch tree, ctx's cancellation kept otherwise, ctx itself for a
    worktree scope or a re-entered scope).
  - `client.go`: `MergeOpts.Target` doc.
- **`core/divergence`** (`45abdd23`): `EvaluateAndCommit` / `CommitConvergence` /
  `CreateExploratoryBranch` take the run; `convergenceTargetGuard` (the divergence's own run, a
  `spine/` branch) runs first in all three; branch protection names `run.BranchName` for each
  selected branch before the tree is taken; `mergeSelected` merges with `Target: run.BranchName`,
  under `WithWorkingTree` for a guard client with the abort inside; `divergenceBase` forks branches
  from `run.BranchName`. Engine `DivergenceHandler`/`ConvergenceHandler` and the gateway's
  `BranchCreator` follow; `handleCreateBranch` loads the run.
- **`core/engine`** (`a14c3736`, `47b2eaea`, `c7eb2e9f`): `worktree.go` (`withTree`,
  `withClientTree`, `requireStillCommitting` → `errRunSuperseded`, `blockExit`); git-only blocks for
  the binding-off cascade (`preflightCheck`), the primary merge (`mergePrimary`: merge → abort →
  push; `mergePrimaryBound`: MergeBound → abort / verifyBoundMerge → takeBackRefusedMerge →
  PushCommit), the run start (`createRunBranches`), the binding-off recovery ledger
  (`writeLedgerOnRunBranch`, its restore ctx keeps the ticket) and the code-repository merge (the
  whole git part under the client's guard). `merge_retry.go`: `AbandonExhaustedMerge`,
  `MergeRetriesExhaustedError`; `recordGitConflictOnStep` classifies `merge_retries_exhausted`.
  TASK-005's transient `VerifyCommit` exit records a failed transient outcome before `retryMerge`
  (the §3 note's cross-task requirement). `CollisionHandler` doc.
- **`core/artifact`, `core/workflow`**: in-scope git calls use `scope.Context(ctx)`;
  `artifact.Service` calls `scope.Cleanup()` after `autoPush`, before the events.
- **`core/domain`**: `RetriesExhausted`, `FirstExhaustedOutcome`, `FailureMergeRetriesExhausted`.
- **`adapters/scheduler`**: `CommitExhaustedFunc`, `WithCommitExhausted`, `commitBudgetSpent`
  (committing pass of the retry cycle and of `RecoverOnStartup`; partially-merged resume pass not
  gated), `CommitRetryBudget()`; `MultiScheduler`: `CommitMaxRetries` (0 → 3, < 0 unbounded),
  `WorkspaceBudget` (0 → `TimeoutInterval`), `MaxConcurrentWorkspaces` (0 → 4), semaphore
  goroutines with a wait for all, `defer Release`, `ServiceSet.CommitExhaustedFn`; the soft budget is
  a ctx value checked between items by `budgetIter` in `ScanTimeouts`, `ScanRunTimeouts`,
  `ScanOrphans` and `retryRunsByStatus`, with per-workspace resume positions (codex round 1); without
  a budget `budgetIter` is the plain in-order loop.
- **`service/`** (`c9bf30d4`): `workspace.PrimaryClientOptions` (a copy of the base options plus
  `WithHomeBranch(engine.AuthoritativeBranch)`) builds the primary client in `serve_cmd.go` and
  `buildServiceSet`; gitpool code-repository clients and the cloner keep the base options.
  `buildScheduler`: `WithCommitRetry(…, defaultCommitMaxRetries = 3, 2m)` +
  `WithCommitExhausted(orch.AbandonExhaustedMerge)`; `workspaceOrchestratorBuilder` sets
  `ss.CommitExhaustedFn`. Harness: `NewTestRepo` builds its client with `WithHomeBranch("main")`;
  `harness.WithoutHomeBranch()` opts out.
- **Docs** (`3443e364` and the codex commits): `architecture/git-integration.md` §6.2 and new §6.6,
  `divergence-and-convergence.md` §4.5, `engine-state-machine.md` (committing rows),
  `KNOWN-LIMITATIONS.md` ("Shared Working Tree" under Architecture Deviations; binding items 7 and 13
  updated), `CHANGELOG.md` (Security / Changed / Changed (embedders)).

### Acceptance criteria — evidence

Test files: `adapters/git/treelock_internal_test.go`, `adapters/git/shared_worktree_test.go`,
`core/divergence/convergence_target_test.go`, `core/engine/shared_worktree_regression_test.go`,
`core/engine/merge_retry_test.go`, `adapters/scheduler/commit_budget_test.go`,
`adapters/scheduler/multi_internal_test.go`, `core/domain/repository_merge_outcome_retry_test.go`,
`service/cmd/spine/serve_worktree_wiring_test.go`, `service/internal/workspace/pool_home_branch_test.go`.

| AC | Evidence |
|---|---|
| 1 | `TestF8_ConvergenceMergesIntoRunBranchNotMain`: main unchanged and without `UNREVIEWED.md`, the run branch has it, divergence `resolved`, tree on a clean `main`. |
| 2 | `TestF8_StandaloneConvergenceTargetsRunBranch`. |
| 3 | `TestF8_ConvergenceRefusesMissingOrForeignTarget`: subtests in the stated order (other run's `spine/` branch, `main`, empty, nil); `ErrPrecondition`; no ref moves; context stays `active` and `UpdateDivergenceContext` is never called; the exported `CommitConvergence` refuses the same runs. |
| 4 | `TestF8_MergeRefusesEmptyTarget` (branch and `SourceSHA` forms, permanent, no git process), `TestF8_MergeAbortStillWorksWithoutTarget` (`{Source:"--abort", Strategy:"abort"}`, `{Source:"--abort"}`, `{Strategy:"abort"}` each run exactly `merge --abort`), `TestF8_MergeAbortShapesRefusedBeforeGit` (`{Strategy:"abort", Target:"x"}`, `{Source:"--abort", Target:"x"}`, `{Strategy:"abort", Source:"feature"}`). |
| 5 | `TestF8_ConvergenceAbortScope`: shared tree — no `MERGE_HEAD`, HEAD on `main`, error, not resolved; non-guard recording fake — exactly one `Merge` with `Target == run.BranchName`, no abort, with and without a merge failure. |
| 6 | `TestF9_ConcurrentGovernedMergesSerialize` (25 iterations). |
| 6b | `TestF9_SameRunDoubleEntryNeverFailsACompletedRun` (copy-returning store; entry B paused in branch protection; entry A completes and deletes the branch; B returns nil, run `completed`, outcome `merged`, no `run_failed`). |
| 7 | `TestF9_StatusCascadeIsAtomicAgainstConcurrentMerge`. |
| 8 | `TestF9_RunBranchNeverCutFromForeignRunBranch`. |
| 9 | `TestF9_PoisonedTreeSelfHealsAtNextMerge` (exactly one Warn with a non-empty `quarantine_sha`). |
| 9b | `TestF9_PartiallyStagedCascadeIsQuarantined`: nine artifacts plus `docs/b.md`; at the cascade's checkout the branch tip replaces `docs/` with a committed symlink, so worktreefs refuses `docs/b.md` after earlier paths were staged. The cascade write order is a map iteration, so the test repeats the scenario (up to 8 times) until a write was staged before the refusal, asserting the properties on every run. |
| 10 | (a) `TestTreeLock_NestedCallReenters`, (b) `…SameTreeDifferentSpellingShareOneLock`, (c) `…WaitHonoursContext`, (d) `…RegistryEmptyAfterRelease`, (e) `…StaleTicketDoesNotBypass`, (f) `…InPlaceScopeHoldsTree`, (g) `…InPlaceScopeContextReenters`, (h) `…DoneContextNeverAcquires` (0/200), (i) `…SequenceIsDetachedAndBounded`, plus `…NoHomeSequenceKeepsCancellation` (codex round 1); `TestF9_StatefulCallsWaitForTheTree` covers every locked call and `EnterBranch(dir, "")`. All under `-race`. |
| 11 | `TestF9_RestoreQuarantinesAndReturnsToMain` (quarantine, untracked kept, no new ref, no-op, merge in progress aborted), `TestF9_HomeBranchSelfHealsOnlyWhenConfigured` (CreateBranch/EnterBranch from a poisoned shared tree start from main; no home branch never moves HEAD; restore failure → `*TreeRestoreError`, fn not run; invalid home fails closed), `TestF9_RestoreReportsStaleIndexLock` (message names `index.lock`, inner `*GitError` retryable, fn not run, file kept). Codex rounds 2–3: `TestF9_RestoreNeverDeletesObstructingUntrackedFiles`, `TestF9_RestoreFailureAfterQuarantineCarriesTheSHA`, `TestF9_RestoreNeverOverwritesIgnoredFiles`. |
| 12 | `TestF9_NoEventsOrRoutingUnderTheTreeLock`: merge success, transient (push), permanent (conflict, routes `run_failed`), `StartRun`, convergence, in-place `artifact.Service.Create`; a second client tries `WithWorkingTree` (1 s) on every emit; no violation. |
| 13 | `TestF9_LockWaitCancellationIsNotAMergeFailure`: `*git.TreeWaitError` wrapping `DeadlineExceeded`, no outcome row, run `committing`. |
| 14 | `TestF9_EntryRestoreFailureIsARecordedAttempt`: an untracked `notes.md` blocks `switch main`; one failed, permanent outcome (`attempts` 1); legacy run `failed`; publish workflow routes `merge_failed` → `execute`. |
| 15 | `TestMergeRetry_TransientPushIsBoundedThenAbandoned` (binding off failing `Push` and binding on failing `PushCommit`, each with the legacy and the publish-step workflow): attempts 1–4 class `network`; `false` (no change) at 3, `true` at 4; legacy → `failed` with `merge_retries_exhausted` on the step; publish → `active` at `execute`; outcome keeps 4 attempts and `network`, prefix exactly once; second call `false`; `maxRetries` 0 `false`. Also `TestMergeRetry_TransientVerifyCommitIsACountedAttempt` and the codex round 4 tests. |
| 16 | `TestCommitBudget_ExhaustedRunIsHandedOverNotRetried`, `…HandlerErrorSkipsTheRun`, `…ZeroMaxRetriesNeverAsks`, `…NoHandlerLeavesExhaustedRunCommitting`, `…RecoverOnStartupAppliesTheGate`, `…HeldRunWithoutOutcomeIsNeverAbandoned` (10 ticks); `TestRetriesExhausted` and `TestFirstExhaustedOutcome` (domain table). `recovery_partial_test.go` and `scenarios/partial_merge_retry_test.go` are unedited and pass. |
| 17 | `TestMultiScheduler_BoundedConcurrency` (10 workspaces, observed maximum 4, returns after all, each released once), `…SoftBudgetStopsBetweenItemsWithoutCancelling` (fewer than 100 calls, `remaining > 0` logged, ctx never cancelled), `…PerWorkspaceSchedulerGetsTheRetryBudget` (default 3, configured, unbounded; the ServiceSet's handler), `…BudgetedScanResumesWhereItStopped` and `TestBudgetIter_NoBudgetIsThePlainLoop` (codex round 1). |
| 18 | Mutation table below; the original PoCs re-run on `main` below. |
| 19 | `TestServeWiring_PrimaryClientHomeBranchAndRetryBudget` (primary client home branch, code-repository client none, base options untouched, `buildScheduler` budget 3 with handler, `workspaceOrchestratorBuilder` sets `CommitExhaustedFn`), `TestBuildServiceSet_PrimaryClientHasHomeBranch` (through the ServiceSet and `GitPool.PrimaryClient`). Harness: `NewTestRepo` uses `WithHomeBranch("main")`; **no** scenario uses `WithoutHomeBranch()` (the scenario suite's failures are identical to `main`'s, see Gates). |
| 20 | Existing-test edits — see Deviations (more than §6 listed, because TASK-002/003 added merge tests without a `Target` after the design). |
| 21 | Docs as listed under What was built. |
| 22 | SMP gate below. |
| 23 | Gates below. |
| 24 | (a) `TestF9_CancelledGitLeavesNoLockFile` (slow smudge filter, cancelled at 300 ms, no `index.lock`); (b) `TestF9_CancelledRequestNeverPoisonsTree` (run A's branch adds 40 files written before a slow-filter file; cancelled at 300 ms: no `index.lock`, clean `main` with no untracked files, run A `completed` or `committing`, no `unknown` outcome, retry of A and run B complete). Both also pass in `golang:1.26-bookworm` (git 2.39.5). |
| 25 | `TestF9_HeadReadsResolveToHomeBranch` (every read method; a second client on the tree; a tree with no home keeps the real HEAD) and `TestF9_HeadReadDuringSequenceSeesMain` (during run A's cascade: `ReadFile(HEAD)` = main's `type: Task`, `Head()` = `refs/heads/main`, `StartRun` binds `Task`). |
| 26 | `TestF9_RefusedBoundMergeNeverVisibleToNextHolder` (binding on, auto-push to a bare origin, a refusal injected after `MergeBound` through `CommitParents`; an in-place `artifact.Service.Create` started inside the block waits; its commit's parent is the base; origin `main` never contains `M`). See Deviations for the "local main at record" assertion. |
| 27 | `TestF8_DivergenceForksFromRunBranch` (merge-base = run tip at divergence start; convergence succeeds, `resolved`, main unchanged; exploratory branch at the run tip; another run refused). The SMP-side unit test is SMP TASK-012's. |

### Deviations (with reasons)

1. **Existing-test edits beyond §6 (AC 20).** TASK-002 and TASK-003 added merge tests after the
   design that relied on an implicit target, and TASK-004/TASK-001 added a scan and an
   exploratory-branch test:
   - `adapters/git/cli_test.go` (§6), `cli_bounds_test.go` (three merges gain `Target`),
     `refgrammar_test.go` (the oid fast-forward row gains `Target: "main"` and its `switch` argv;
     the abort-by-strategy row drops its `Target`, which AC 4 now refuses; the abbreviated-id merge
     gains `Target`);
   - `core/worktreefs/scan_test.go`: two reviewed allowlist entries — `treeReal` (EvalSymlinks of
     the tree *root* to key the lock) and `restoreHeld` (an Lstat existence check of
     `<git-dir>/index.lock`);
   - `core/divergence/convergence_test.go` (§6), `service_test.go` and `service_controlbyte_test.go`
     (exploratory signature; their runs gain a `spine/` branch), `core/engine/divergence_test.go`
     (§6), `core/engine/merge_binding_test.go` (`headMovingConvergence` fake),
     `service/internal/gateway/handlers_divergence_test.go` and `server_accessors_test.go` (fakes).
2. **Detach only home-branch sequences** (codex round 1). §2.5's note "Change 2" reads as if every
   taken `WithWorkingTree` sequence were detached; AC 10i and the "trees without a home branch are
   unchanged" invariant scope it to home-branch trees, and codex flagged code-repository merges that
   kept running for 5 minutes after a cancel. A no-home tree's sequence holds the lock and keeps the
   caller's ctx.
3. **Restore hardening beyond §2.5** (codex rounds 2–3): an untracked or ignored file in the way of
   either `reset --hard` refuses the restore before anything changes; the switch home passes
   `--no-overwrite-ignore`; the quarantine SHA is logged at creation and carried by a later failure
   (`TreeRestoreError.QuarantineSHA`, additive). The verify step runs only after a repair (the
   initial reads already prove a clean home), and `stash create` runs with a fixed `Spine`
   identity so it works on a host with no `user.name`.
4. **Retry-budget coordination beyond §3** (codex round 4): `mergeAttemptGate` keeps an
   abandonment and a merge attempt of the same run from overlapping in-process, and an exhausted
   outcome counts only when attempted in the current committing episode (after the run's latest
   step activity). Decision 10's "abandoned at its first transient failure" now holds strictly; the
   cross-process residual stays with TASK-008.
5. **TASK-005's transient `VerifyCommit` exit records an outcome** (§3 note's cross-task
   requirement; TASK-005 merged without it). Binding on only. TASK-017's exit is its own.
6. **Additive API not listed in §6:** `(*CLIClient).HomeBranch`, `(*Scheduler).CommitRetryBudget`
   (wiring checks, AC 19), `workspace.PrimaryClientOptions` (service-internal, shared by both
   wiring sites), `TreeRestoreError.QuarantineSHA`; `harness.WithoutHomeBranch()` is an `EnvOption`
   that `NewTestRepo` (now variadic) also accepts.
7. **Locked set:** `ResetBranch` (added by TASK-005 after the design) is locked like `MergeBound`;
   `TreeEntryType` gets the `HEAD` rewrite (a read method §2.9 does not list); `Merge` with
   `SourceSHA` and no `Target` is refused too; Source's grammar is checked before the
   target-required check so TASK-002's refusals keep `ErrInvalidRef`.
8. **Acquisition:** a done ctx is checked again after the wait, so "a done ctx never acquires" is
   strict.
9. **Cascade block lock wait:** a `*TreeWaitError` from the cascade block ends the attempt like one
   from the primary block (nothing recorded), instead of the cascade's log-and-proceed: the primary
   block could not take the tree with that ctx either.
10. **Code-repository merge block:** the whole git part (merge → abort, or MergeBound → verify →
    take-back → push) holds the client's lock; a `*TreeWaitError` is the "could not start" error (no
    outcome row, run stays committing). Dormant.
11. **`MultiSchedulerConfig`:** `CommitMaxRetries` 0 means the default 3 (zero value = default, as
    §3.4 asks) and a negative value unbounded; the soft budget stops between items after at least one
    item per scan per tick and resumes there next tick (codex round 1).
12. **AC 26's "local main equals `BaseSHA` when the outcome is recorded"** is asserted as "local
    main at record time is `BaseSHA`, or the in-place write that waited for the tree and landed on
    `BaseSHA` — never `M`": the waiting write may take the tree between the block's release and the
    record, both of which happen after the release by design.
13. **AC 12, transient path:** it emits nothing; the assertion is "no violation".
14. **AC 9b** retries its scenario until a write was staged before the refusal (map-ordered
    cascade writes).
15. **Scenario harness:** no scenario needed `WithoutHomeBranch()`.

### Fails before the fix (AC 18)

**The original audit PoCs, re-run on an archive of `main` (`ae513dca`)** — each copied into its
package of a `git archive main` copy in the scratchpad, never into a worktree:
- `poc/skeptic_conv` `TestSkeptic_EngineConvergenceLandsOnMain`: PASS (= vulnerable).
- `poc/verify-r1-11` `TestPoC_VerifyR1_11_EngineConvergenceLandsOnMain`: "CONFIRMED: A1's
  divergence-branch content is on main while the run is still active and the human review step has
  not run" (main advanced; run `active`, review `waiting`).
- `poc/governance` `TestPoC_StandaloneConvergenceMergesIntoMain`: PASS (= vulnerable, "convergence
  merged divergence-branch content directly into main").
- `poc/gap2-git-state-concurrenc` (FAIL = vulnerable): all four FAIL —
  `TestPOC_ConcurrentGovernedMergesRace` (23/25 spurious merge failures, 5 dirty trees, 5 HEAD off
  main, cross-run commits), `TestPOC_StatusCascadeCommitLandsOnMain`,
  `TestPOC_NewRunBranchCutFromForeignRunBranch`, `TestPOC_PoisonedTreeAffectsLaterMerge`.
- `poc/verify-r2-2`: `TestVerifyR22_ConcurrentMerges` poisoned pairs each failed the next serial
  run; `TestVerifyR22_PoisonPersists`: four strictly serial runs all `failed`.

The inverted, committed ports use the new API (`WithHomeBranch`, the run argument), so they do not
compile on `main`; their before-evidence is the PoCs above plus the mutation runs below.

**Mutations** (`scratchpad/mutate.py`: each mutant applied to a fresh copy of the worktree; the
task's test files of `adapters/git`, `core/engine`, `core/divergence`, `adapters/scheduler` and
`core/domain` run; never edited in the worktree). M1–M10, M12, M14–M20 ran at `c9bf30d4`+docs; M11
and M13 re-ran after codex rounds 1–2 (M13 after AC 24b's fixture gained files written before the
slow-filter file, so that a non-detached sequence leaves the design check's untracked residue);
M21–M27 guard the codex fixes. Every mutant is killed, at least by the predicted AC's tests:

| Mutation | Predicted | Failing tests (observed) |
|---|---|---|
| M1: no-op lock | AC 1, 6, 7, 8, 9, 10, 11 | `TestTreeLock_SameTreeDifferentSpellingShareOneLock`, `TestTreeLock_WaitHonoursContext`, `TestTreeLock_RegistryEmptyAfterRelease`, `TestTreeLock_StaleTicketDoesNotBypass`, `TestTreeLock_InPlaceScopeHoldsTree`, `TestTreeLock_InPlaceScopeContextReenters`, `TestTreeLock_DoneContextNeverAcquires`, `TestTreeLock_SequenceIsDetachedAndBounded`, `TestF9_HomeBranchSelfHealsOnlyWhenConfigured`, `TestF9_RestoreReportsStaleIndexLock`, `TestF9_StatefulCallsWaitForTheTree`, `TestF8_ConvergenceMergesIntoRunBranchNotMain`, `TestF9_ConcurrentGovernedMergesSerialize`, `TestF9_StatusCascadeIsAtomicAgainstConcurrentMerge`, `TestF9_RunBranchNeverCutFromForeignRunBranch`, `TestF9_PoisonedTreeSelfHealsAtNextMerge`, `TestF9_PartiallyStagedCascadeIsQuarantined`, `TestF9_LockWaitCancellationIsNotAMergeFailure`, `TestF9_CancelledRequestNeverPoisonsTree`, `TestF9_RefusedBoundMergeNeverVisibleToNextHolder`, `TestF8_StandaloneConvergenceTargetsRunBranch`, `TestF8_ConvergenceAbortScope`, `TestF8_DivergenceForksFromRunBranch` |
| M2: restore is a no-op | AC 1, 9, 11 | `TestTreeLock_SequenceIsDetachedAndBounded`, `TestF9_RestoreQuarantinesAndReturnsToMain`, `TestF9_HomeBranchSelfHealsOnlyWhenConfigured`, `TestF9_RestoreReportsStaleIndexLock`, `TestF8_ConvergenceMergesIntoRunBranchNotMain`, `TestF9_PoisonedTreeSelfHealsAtNextMerge`, `TestF9_PartiallyStagedCascadeIsQuarantined`, `TestF8_StandaloneConvergenceTargetsRunBranch`, `TestF8_ConvergenceAbortScope`, `TestF8_DivergenceForksFromRunBranch` |
| M3: CommitConvergence sends Target "" | AC 1, 2 | `TestF8_ConvergenceMergesIntoRunBranchNotMain`, `TestF8_StandaloneConvergenceTargetsRunBranch`, `TestF8_ConvergenceAbortScope`, `TestF8_DivergenceForksFromRunBranch` |
| M4: Merge with empty Target merges into HEAD (with M3) | AC 1, 4 | `TestF8_MergeRefusesEmptyTarget`, `TestF8_ConvergenceMergesIntoRunBranchNotMain`, `TestF8_StandaloneConvergenceTargetsRunBranch`, `TestF8_ConvergenceAbortScope`, `TestF8_DivergenceForksFromRunBranch` |
| M5: guard reduced to run != nil | AC 3 | `TestF8_ConvergenceRefusesMissingOrForeignTarget`, `TestF8_DivergenceForksFromRunBranch` |
| M6: EnterBranch(dir, "") takes no lock | AC 10f, 11 | `TestTreeLock_InPlaceScopeHoldsTree`, `TestTreeLock_InPlaceScopeContextReenters`, `TestF9_HomeBranchSelfHealsOnlyWhenConfigured`, `TestF9_StatefulCallsWaitForTheTree`, `TestF9_RefusedBoundMergeNeverVisibleToNextHolder` |
| M7: any ticket for the key is honoured | AC 10e | `TestTreeLock_StaleTicketDoesNotBypass` |
| M8: handlePermanentMergeFailure inside the primary block | AC 12 | `TestF9_NoEventsOrRoutingUnderTheTreeLock`, `TestF9_HeadReadDuringSequenceSeesMain` |
| M9: commitBudgetSpent always false | AC 16 | `TestCommitBudget_ExhaustedRunIsHandedOverNotRetried`, `TestCommitBudget_HandlerErrorSkipsTheRun`, `TestCommitBudget_NoHandlerLeavesExhaustedRunCommitting`, `TestCommitBudget_RecoverOnStartupAppliesTheGate`, `TestCommitBudget_HeldRunWithoutOutcomeIsNeverAbandoned` |
| M10: RetriesExhausted uses >= | AC 15, 16 | `TestMergeRetry_TransientPushIsBoundedThenAbandoned`, `TestCommitBudget_ExhaustedRunIsHandedOverNotRetried`, `TestCommitBudget_NoHandlerLeavesExhaustedRunCommitting`, `TestCommitBudget_RecoverOnStartupAppliesTheGate`, `TestRetriesExhausted` |
| M11: MultiScheduler ctx deadline instead of soft budget | AC 17 | `TestMultiScheduler_SoftBudgetStopsBetweenItemsWithoutCancelling`, `TestMultiScheduler_BudgetedScanResumesWhereItStopped` |
| M12: git children keep the default SIGKILL cancel | AC 24a | `TestF9_CancelledGitLeavesNoLockFile` |
| M13: WithWorkingTree passes the caller's ctx to fn | AC 10i, 24b | `TestTreeLock_SequenceIsDetachedAndBounded`, `TestF9_CancelledRequestNeverPoisonsTree` |
| M14: no run-status re-check in the blocks | AC 6b | `TestF9_SameRunDoubleEntryNeverFailsACompletedRun` |
| M15: guard only in CommitConvergence | AC 3 | `TestF8_ConvergenceRefusesMissingOrForeignTarget` |
| M16: no HEAD -> home rewrite | AC 25 | `TestF9_HeadReadsResolveToHomeBranch`, `TestF9_HeadReadDuringSequenceSeesMain` |
| M17: refusal rollback after the block is released | AC 26 | `TestF9_RefusedBoundMergeNeverVisibleToNextHolder` |
| M18: divergence base stays HEAD | AC 27 | `TestF8_DivergenceForksFromRunBranch` |
| M19: {Source: "--abort"} without Strategy is refused | AC 4 | `TestF8_MergeAbortStillWorksWithoutTarget`, `TestF9_StatefulCallsWaitForTheTree` |
| M20: the restore ignores .git/index.lock | AC 11 (stale lock) | `TestF9_RestoreReportsStaleIndexLock` |
| M21: restore has no untracked-obstruction check (codex r2 P1) | codex follow-up | `TestF9_RestoreNeverDeletesObstructingUntrackedFiles` |
| M22: restore failure drops the quarantine SHA (codex r2 P2) | codex follow-up | `TestF9_RestoreFailureAfterQuarantineCarriesTheSHA` |
| M23: budgeted scans restart at the head of the list (codex r1 P2) | codex follow-up | `TestMultiScheduler_BudgetedScanResumesWhereItStopped` |
| M24: no-home sequences detached too (codex r1 P2) | codex follow-up | `TestTreeLock_NoHomeSequenceKeepsCancellation` |
| M25: restore switch overwrites ignored files (codex r3 P1) | codex follow-up | `TestF9_RestoreNeverOverwritesIgnoredFiles` |
| M26: abandonment does not wait out an in-flight attempt (codex r4 P2) | codex follow-up | `TestMergeRetry_AbandonSkipsWhileAMergeIsInFlight` |
| M27: an earlier episode's outcome counts (codex r4 P2) | codex follow-up | `TestMergeRetry_PreviousEpisodeOutcomeDoesNotAbandon` |

### Gates (final code `b95361b8`; the artifact commit after it changes only this file)

All on macOS (git 2.54, Go 1.26.5) unless stated; databases `s012_spine` (scenario-harness
migrated), `s012_reg` (`migrations/registry/*.sql`), `s012_smp_it`, `s012_spine_runtime_it` on
`i23-pg`, dropped at the end.

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` | OK |
| Build | `make build` | OK |
| Embed smoke test | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | "library surface is sound" |
| Vet | `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | clean (all three) |
| Unit | `go test -count=1 -p 4 ./...` | 40 packages ok, 0 failures |
| Race | `go test -race -count=1 -p 4` on `adapters/git/...`, `adapters/scheduler/...`, `core/engine/...`, `core/divergence/...`, `core/domain/...`, `core/artifact/...`, `core/workflow/...`, `core/worktreefs/...`, `service/cmd/spine/...`, `service/internal/workspace/...`, `service/internal/gateway/...` | 12 ok, 0 races (at `a56df4ce`; the gocritic-only change in `b95361b8` re-ran `-race -run 'TestMergeRetry_\|TestF9_' ./core/engine/`: ok) |
| Integration (touched packages) | `SPINE_TEST_DATABASE_URL=…/s012_spine go test -tags integration -count=1 -p 2 -v ./service/cmd/spine/ ./service/internal/workspace/` | 235 PASS, 0 skips (incl. `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy`) |
| CI scenario-git-http | `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (and `^TestBootstrapInternalAdmin_`, `^TestWorkflowLifecycle_`) | 20 / 2 / 4 PASS, 0 failures, 0 skips |
| `make test-scenario` | `go test -tags scenario -count=1 -p 4 ./service/scenariotest/...` with both DSNs | 34 top-level failures, **identical** to an archive of `main` run against the same databases (empty diff). The two first-run differences (`TestNewTestDB`/`TestCleanup` on the branch, `TestActorAPI_AIAgentGoldenPath` on main) were `-p 4` races on the shared database; serially they pass on the branch, and `TestSchemaMatchesProduction` fails on both. |
| golangci-lint | `golangci-lint run --new-from-rev main <pkg>` per touched package (harness with `--build-tags scenario`) | 0 new issues (fixed on the way: 4 gocritic builtinShadow, 1 gofmt, 1 gosec G118 documented with a `//nolint` reason, 1 gocritic initClause) |
| gosec | the same with `--enable-only=gosec` | 0 new issues |
| gofmt | `gofmt -l` on every changed file vs `main`'s version | only the pre-existing import-order diffs, byte-identical to `main`'s, in `adapters/scheduler/recovery.go`, `core/divergence/convergence.go`, `core/divergence/service.go`, `core/divergence/convergence_test.go`, `core/divergence/service_test.go`, `service/internal/gateway/server_accessors_test.go` (AC 23's exemption; not reformatted, to keep the diff minimal) |
| git floor | `golang:1.26-bookworm` (git 2.39.5), worktree read-only: the CI job's two steps, plus the task's tests `-run 'TestF8_\|TestF9_\|TestMergeRetry_\|TestCommitBudget_\|TestMultiScheduler_' ./core/engine/ ./core/divergence/ ./adapters/scheduler/` | all ok at `b95361b8`: step 1 (`adapters/git`, `core/engine` ref-grammar filter), step 2 (`adapters/git/...`, `core/artifact`, `core/workflow`), and the task filter (`core/engine`, `core/divergence`, `adapters/scheduler`) |

The engine's real-git tests need a git identity for the engine's own commits (as the other real-git
engine tests do); the container passes `GIT_AUTHOR_*`/`GIT_COMMITTER_*`.

### SMP compatibility gate (Rule 3)

SMP `1ae9b0e3` (detached checkout `i23/s-T012/smp`, `../spine` = this worktree), no SMP edits,
`GOFLAGS=-mod=readonly`:

| Step | Command | Result at Spine `b95361b8` |
|---|---|---|
| Build + vet | `go build ./... && go vet ./...`, plain and `-tags integration`, `-tags scenario`, `-tags faultinject` | clean (all four) |
| Race, every SMP package importing a changed Spine package | `go test -race -count=1 -p 4 -timeout 20m` on `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}` (the importers of `adapters/git`, `adapters/scheduler`, `core/engine`, `core/divergence`, `core/domain`, `core/artifact`, `core/workflow`, under every tag set), with `TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` | 11 ok, `projectionsvc` has no test files |
| Integration job, Spine-runtime step | `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` | PASS: 1914 passed, 0 failed, 2 allowed skips |
| githttp suite | `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | PASS: 301 passed, 0 failed, 1 allowed skip |
| Leak check | `ws_%` databases and roles before/after | 0 / 0 |

Platform migrations via `go run ./dev/apply-migrations -set platform -dir migrations` (116
versions), Spine runtime via `-set spine-runtime` (26). The same gate also passed at `7dfdb597`
(before codex rounds 3–4). Rule 4: SIGTERM-on-cancel and the run-status re-check reach SMP's
`CLIClient` users and engine; no successful result changed in any SMP suite. SMP never sets
`WithHomeBranch`, never calls `AbandonExhaustedMerge`, `WithWorkingTree` or `CLIClient.Merge`, and
receives an explicit convergence `Target` plus `run.BranchName` as the divergence base.

### Codex

`codex review --base main`, plain (no model override); every review sub-session
(`session_meta.source = {"subagent":"review"}`) ran `gpt-6.1-sol`:

| Round | Review session | Findings | Resolution |
|---|---|---|---|
| 1 | `01a13f91-17a2-7100-8ff4-20e8a7d2aa87` | [P2] budgeted scans restart at the list head (starvation); [P2] `WithWorkingTree` detached no-home sequences | fixed in `68d51946` (resume positions; detach only on home-branch trees) |
| 2 | `01a13f9c-2b5c-72b0-9897-4b0cf4e89877` | [P1] `reset --hard` deleted untracked files obstructing a tracked path; [P2] quarantine SHA lost when a later restore step failed | fixed in `7dfdb597` |
| 3 | `01a13fb4-b96b-7671-9abc-82d32d964f79` | [P1] the restore's switch overwrote ignored files (`--overwrite-ignore` default) | fixed in `7bd6663b` (`--no-overwrite-ignore`) |
| 4 | `01a13fc1-451e-73b3-aae4-88b114d23090` | [P2] exhaustion abandonment could race an in-flight merge attempt / use the previous episode's outcome | fixed in `a56df4ce` |
| 5 | `01a13fcc-af3a-7d23-bd2a-0544aef73864` | none ("No actionable regressions were identified") | — |

No finding was rejected.

### Not run / notes for the orchestrator

- The SMP-side unit test of AC 27 (`divergenceGitClient.CreateBranch` receives and forwards
  `run.BranchName`) belongs to SMP TASK-012 (no SMP edits here); the SMP gate's `spinecore` suite
  passes with the new base.
- `MultiScheduler` remains unwired (OQ3); its bounds are covered by unit tests only.
- Runtime schema: unchanged.
