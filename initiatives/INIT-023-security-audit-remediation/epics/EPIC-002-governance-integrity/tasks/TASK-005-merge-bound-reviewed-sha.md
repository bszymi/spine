---
id: TASK-005
type: Task
title: "Bind the governed merge to the commit that passed validation and review"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
# Depends on spine TASK-002 (CLIClient ref grammar and Merge argv). Add the
# blocked_by link once TASK-002's epic path is fixed in this initiative.
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /architecture/multi-repository-integration.md
---

# TASK-005 — Bind the governed merge to the commit that passed validation and review

---

## Purpose

Findings: **F2 (merge half), P2, both surfaces** and **P3-37 (dormant, standalone)** from the
2026-10-06 security audit.

Spine line numbers refer to `61994fe`. SMP line numbers refer to `80995559`. Sibling tasks are
named where their contracts are used.

### F2: the merge publishes the branch tip, not the approved commit

1. **No SHA is recorded or compared.**
   - `SubmitRequest` (`core/engine/result.go:12-16`) and `StepResult` (`core/engine/step.go:17-20`)
     carry only an outcome ID and artifact paths.
   - `routeStepOutcome` stores only the workflow's `outcome.Commit` map (`step.go:396-401`).
   - `SetCommitMeta` replaces the whole `commit_meta` column
     (`adapters/store/postgres_runs.go:153-160`).
2. **The merge resolves the branch name at merge time.**
   - `mergePrimary` calls `Merge{Source: run.BranchName, Target: "main"}`
     (`core/engine/merge.go:168-174`).
   - `CLIClient.Merge` passes the bare name to `git merge` (`adapters/git/cli.go:365-404`).
   - The status cascade reads every file at `run.BranchName` (`merge.go:606`, `:690`, `:696`). It
     then checks out the branch tip and commits on top of it (`:643-667`).
   - The push sends the local `main` ref, not a verified commit (`merge.go:229`,
     `cli.go:608-611`).
   - SMP's operator fast-forwards the local run ref from origin just before merging
     (`internal/customer/spinecore/gitoperator.go:420`). It then merges `opts.Source` (`:446`).
3. **Three windows let unapproved commits through.**
   - (a) After validate passes and before the reviewer accepts.
   - (b) After the reviewer reads the diff and before they submit.
   - (c) The committing window. After a transient merge or push failure the run stays in
     `committing`. Every scheduler retry (`service/cmd/spine/serve_builders.go:313-315`) resolves
     the tip again.

**Attackers.**
- In embedded SMP: A6 (a compromised runner bearer) and A2 (a member). Member sessions and runner
  bearers have an empty ref scope for `spine/*`.
- In standalone Spine: A1 and A2. The pre-receive gate skips `spine/*` refs.
- Who may push is out of scope here (TASK-016, SMP TASK-005 and TASK-007). This task makes the
  last push irrelevant to what merges.

**Impact.**
- Unvalidated and unreviewed commits reach `main` under the trusted "Merge run …" message.
- These can include `.spine/branch-protection.yaml` with `rules: []` and `workflows/**`. The
  projection then installs them as live policy (F3).
- ADR-009 names the review step as *the* control for the run-branch path
  (`architecture/adr/ADR-009-branch-protection.md:194`). Today that control does not decide what
  merges.

**Reproduced on `61994fe`.** Both audit PoCs were re-run with `go test -overlay` for this design:
- `poc/governance/unreviewed_merge_test.go`: PASS. "commit added after the reviewer saw 47d230ff
  was merged to main".
- `poc/verify-r1-1/toctou_merge_test.go`: PASS. Commit `480ad010` "(never validated, never
  reviewed) is an ancestor of main". `main` carries `src/backdoor.go`, `rules: []` and the
  weakened `workflows/task-default.yaml`.

### P3-37: code-repo merges have the same defect, and they land first

- `attemptCodeRepositoryMerge` merges `Source: run.BranchName` into the repository's default branch
  (`core/engine/multi_repo_merge.go:204-210`). It pushes the result (`:236-249`) before the
  primary merge runs (`merge.go:42-47`).
- After a `merge_failed` loop-back, a code repo whose outcome is already `merged` is skipped
  (`multi_repo_merge.go:120-130`). Code-repo commits reviewed in the next round therefore never
  merge.
- **Unreachable today.** No code-repo catalog is wired. In `poc/gap3-multi-repo-code-repo` both
  the embedded and the standalone shapes fail closed.
- **Confirmed when wired.** A scratch overlay test on the gap3 harness, with a real `CLIClient`
  code repo, showed the tip merge: a commit made after the reviewed SHA landed on the code repo's
  origin `main`.

## Design

### 0. Invariants

- **Opt-in. The zero value keeps today's behaviour.**
  - Without `EnableMergeBinding`, every code path and every git argv is the same as before this
    task, meaning `61994fe` plus TASK-002.
  - There are two exceptions:
    - Reserved-key stripping (§1). No shipped workflow in either repository declares a `spine.*`
      commit key (checked).
    - A client that sends the new `expected_head_sha` gets it checked (§4). Old clients never send
      it.
- **When binding is on, the merge uses the bound commit and nothing else.**
  - Every governed merge, primary or code repo, merges exactly the bound commit. That is the
    run-branch head the engine recorded at the last step completion before publish, or a child of
    it that the engine made itself (the status cascade or a recovery ledger commit).
  - No commit pushed after that point reaches `main` through this run.
- **Fail closed.**
  - Enabling binding without the SHA capability returns an error.
  - A missing or invalid bound SHA means no merge.
  - A merge result that lacks the bound commit, or that contains excluded commits, is never pushed.
- **No runtime schema change.** State lives in reserved `runs.commit_meta` keys. SMP's
  `RunStore.GetRun` already selects that column (`internal/customer/spinecore/runstore.go:139`).
- **Shared maps are never mutated.**
  - `outcome.Commit` belongs to the cached workflow definition.
  - `run.CommitMeta` can alias store state; the test `memStore` returns its own pointer
    (`core/engine/integration_test.go:37-43`).
  - Every write builds a new map.

### 1. Reserved `commit_meta` keys (`core/engine/commitmeta.go`, new)

These are exported constants. SMP may read them for display.

| Key | Value | Written |
|---|---|---|
| `spine.bound_sha` | Full lowercase hex object ID (40 characters, or 64 for SHA-256) | At each recorded capture (§4), at convergence (§5), and by a recovery ledger commit (§8) |
| `spine.bound_by` | `step:<step_id>:<execution_id>`, `convergence:<divergence_id>` or `merge_recovery:<operation>` | Together with every `bound_sha` write |
| `spine.head.<step_id>` | The head at that step's latest completion | Audit and tests only; never read for decisions |
| `spine.repo.<repo_id>.bound_sha` | The bound head in that code repository | P3-37 (§8) |

Helpers (pure, table-tested):

- **`stripReserved(m)`** returns a copy without any key whose lowercased form starts with `spine.`.
  - It is applied to every `outcome.Commit` before storage, whether binding is on or off
    (Decision 8).
  - A stripped key is logged at Warn with the workflow and step.
- **`mergeCommitMeta(current, outcomeCommit, hasCommit, hb)`** returns `(map, write bool)`.
  - Non-reserved part: `stripReserved(outcomeCommit)` when the outcome has a commit. This keeps
    today's replace semantics: earlier non-reserved keys are dropped. Without a commit, the
    non-reserved part of `current` is kept.
  - Reserved part: the reserved keys of `current`, overwritten by `hb`'s keys.
  - Binding off: returns `stripReserved(outcomeCommit)` and `write = hasCommit`. That is today's
    single write, minus reserved keys.

### 2. SHA capability and additive git fields

New interface in `core/engine/interfaces.go`, after `GitOperator` (`:160-175`). The engine is the
consumer, so the interface lives here. Both `GitOperator` and `git.GitClient` implementations can
satisfy it structurally (`adapters/git` is on the core→adapters allowlist,
`dev/lint-boundaries.sh`).

```go
// CommitBinder is the optional capability SHA-bound governed merges need
// (INIT-023 TASK-005). EnableMergeBinding type-asserts it on the GitOperator.
type CommitBinder interface {
	// ResolveBranchHead returns the full object ID that refs/heads/<branch>
	// points at in the repository merges read from (origin, for remote-backed
	// operators). It never applies DWIM: a refs/tags/<branch> must not shadow it.
	ResolveBranchHead(ctx context.Context, branch string) (string, error)
	// VerifyCommit returns nil only for a full lowercase hex object ID that
	// names a commit object available to MergeBound and CommitFilesOnto.
	VerifyCommit(ctx context.Context, sha string) error
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
	// CommitFilesOnto creates a commit whose only parent is base and whose tree
	// is base's tree with files replaced. It moves no ref and touches neither
	// HEAD, the index nor a working tree.
	CommitFilesOnto(ctx context.Context, base string, files map[string]string, opts git.CommitOpts) (git.CommitResult, error)
	// MergeBound merges exactly opts.SourceSHA (required) into opts.Target and
	// reports MergeResult.BaseSHA, the target tip the merge was computed on.
	MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error)
	// PushCommitCAS sets <remote> refs/heads/<branch> to sha only if it is
	// currently expectedOld (force-with-lease). It pushes the object, not a
	// local ref.
	PushCommitCAS(ctx context.Context, remote, branch, sha, expectedOld string) error
}
```

Additive fields in `adapters/git/client.go`. Every literal of these types in both repos is keyed
(checked).
- `MergeOpts.SourceSHA string`, after `Source` (`:56`): "when set, merge exactly this commit;
  Source is used only for messages".
- `MergeResult.BaseSHA string`, after `SHA` (`:65`).

> **Design-check note (2026-10-06): two interface changes (DC-1, DC-3).**
> - `PushCommitCAS(ctx, remote, branch, sha, expectedOld)` is replaced by
>   `PushCommit(ctx, remote, branch, sha string) error`. It is a non-forced push of exactly `sha` to
>   `refs/heads/<branch>`, so git accepts it only as a fast-forward. The reason is in the §6 Push
>   note. Wherever this document says `PushCommitCAS(…, M, BaseSHA)`, read `PushCommit(…, M)`.
> - The interface also gains `CommitParents(ctx context.Context, sha string) ([]string, error)`,
>   which returns the full parent IDs of a commit, in order. `verifyBoundMerge` needs it (§6 note).
> - `MergeResult.BaseSHA` stays. `verifyBoundMerge` uses it.

### 3. Enabling (`core/engine/orchestrator.go`)

```go
type MovedHeadPolicy string

const (
	MovedHeadReject MovedHeadPolicy = "reject" // default (owner question 4)
	MovedHeadRebind MovedHeadPolicy = "rebind"
)

type MergeBindingConfig struct {
	MovedHead MovedHeadPolicy // "" means MovedHeadReject
}

// EnableMergeBinding turns on SHA-bound governed merges. It returns an error,
// and leaves binding off, if o.git is not a CommitBinder or the policy is
// unknown.
func (o *Orchestrator) EnableMergeBinding(cfg MergeBindingConfig) error
func (o *Orchestrator) MergeBindingEnabled() bool
```

- New unexported fields on `Orchestrator` (`:12-38`): `binder CommitBinder`,
  `movedHead MovedHeadPolicy`, `bindingOn bool`.
- **Standalone `serve` enables binding** with the default policy:
  - in `buildOrchestrator`, after `WithBranchProtectPolicy`
    (`service/cmd/spine/serve_builders.go:272`). An error is logged and the function returns nil,
    the same way the policy error is handled at `:267-271`.
  - in the workspace orchestrator, after `:90` in `service/cmd/spine/serve_resolver.go`. An error
    is returned.
  - Both pass `*git.CLIClient`, which implements `CommitBinder` (§9).
- **The scenario harness enables binding by default** (`service/scenariotest/harness/runtime.go`,
  after `:169`). This mirrors production. A `WithoutMergeBinding()` harness option exists for a
  scenario that must commit during a gate step; each use states its reason in a comment.
- `cmd/embed-smoketest` is unchanged (binding off).

### 4. Capture and assertion at submit

**API.**
- `StepResult.ExpectedHeadSHA string` (`step.go:17-20`).
- `SubmitRequest.ExpectedHeadSHA string` (`result.go:12-16`), passed through at `:120-123`.

**Where.** `SubmitStepResult` gets one new call between the outcome lookup (`step.go:317-321`)
and the auto-acknowledge block (`:323-328`). Nothing has been changed at that point, so a rejected
submit leaves the step execution exactly as it was.

```go
hb, err := o.captureHead(ctx, run, exec, stepDef, outcome, wfDef, result.ExpectedHeadSHA)
if err != nil {
	return err
}
```

**Classification** (`core/engine/merge_binding.go`, new):
- **Gate step:** `type: review` or `type: convergence`. A gate is not expected to move the head.
- **Producer step:** `manual` or `automated`.
- **Internal step:** never captured. The merge handler records its own outcome.
- **Merge-bound outcome** ("advancing"). Any one of these makes an outcome advancing:
  - `next_step: end` and the outcome has `commit:`.
  - The next step is a gate or an internal step.
  - The next step ID is not found in the workflow (fail closed).
- **Rework outcome:** any other outcome. Its next step is a producer, or it is `end` without
  `commit:` (that path completes the run without a merge).

**`captureHead` rules, in order.** Every error is a `*domain.SpineError` built with
`NewErrorWithDetail`. The detail carries `reason`, `head_sha`, and `expected_head_sha` or
`bound_sha` and `bound_by`.

1. **`ExpectedHeadSHA` is set.**
   - It must match `^([0-9a-f]{40}|[0-9a-f]{64})$`. Otherwise `ErrInvalidParams` with reason
     `expected_head_sha_invalid`.
   - The run must have a branch, the step must be on the main path (`exec.BranchID == ""`), and
     the step must not be internal. Otherwise `ErrInvalidParams` with reason
     `expected_head_sha_not_applicable`.
   - `o.git` must be a `CommitBinder`. Otherwise `ErrUnavailable` with reason
     `expected_head_sha_unsupported`. This holds even with binding off, so a client that asks for
     protection never gets silent acceptance.
2. **Nothing to capture.** Binding is off and no assertion was given; or the run has no branch; or
   the step is on a divergence branch; or the step is internal; or the execution is terminal. Then
   `hb = nil` and today's path runs unchanged.
3. **Resolve the head.** `H := binder.ResolveBranchHead(run.BranchName)`. Capture is *required*
   when binding is on, unless a gate step is submitting a rework outcome. Capture is also required
   whenever an assertion was given.
   - If resolution fails and capture is required: `ErrUnavailable` with reason
     `run_branch_head_unavailable`. The cause is wrapped.
   - If resolution fails and capture is not required: log at Warn and leave the binding unchanged.
     A reviewer can always send work back.
4. **Assertion.** If `ExpectedHeadSHA != H`: `ErrConflict` with reason `run_branch_moved`.
5. **Moved-head check.** It applies when binding is on, the step is a gate, the outcome is
   advancing, `prev := run.CommitMeta["spine.bound_sha"]` is set, and `H != prev`.
   - `reject` (default): `ErrConflict` with reason `run_branch_moved_since_gate`. The message names
     `prev`, `bound_by` and `H`, and says that commits after that point were not validated and a
     rework outcome is needed.
   - `rebind`: log at Warn and continue. `H` becomes the bound head.
   - If `prev` is absent (a run in flight at enablement, or a workflow whose first step is a
     gate), there is no comparison. This is logged at Info. See owner question 2.
6. **Code repositories** (§8) follow the same steps for each affected repository, through that
   repository's client.
7. Return `hb{head: H, by: "step:<step_id>:<execution_id>", stepID, repoHeads}`. When binding is
   off, return nil after the assertion; nothing is recorded.

**Recording.** `routeStepOutcome` (`step.go:383-410`) takes `hb *headBinding`.
- The `commit_meta` write moves above the divergence dispatch (`:384`). It still covers only
  main-path completions (`exec.BranchID == ""`).
- One `SetCommitMeta` call stores `mergeCommitMeta(run.CommitMeta, outcome.Commit, hasCommit, hb)`
  whenever `write` is true. With `hb` set, that write lands `spine.bound_sha`, `spine.bound_by`
  and `spine.head.<step_id>`.
- `outcome.Commit` is still not stored for a step that starts a divergence (`:384-386`). This
  preserves today's behaviour; only reserved keys are written there.
- `hasCommit` for `CompleteRun` (`:402-407`) is still `len(outcome.Commit) > 0`.
- `advancePublishStepIfAny`'s call (`core/engine/handlers.go:235`) passes nil.

**Out of the capture path, on purpose.**
- `IngestResult`'s idempotent path for a terminal execution (`result.go:39-54`) ignores the
  assertion, as before.
- Scheduler timeout completions (`handleStepTimeout`, `adapters/scheduler/timeout.go:63`) do not
  capture. The next gate's moved-head check covers them, or the merge uses the previous binding,
  which is older and safe.
- The in-process queue consumer (`core/engine/consumer.go:124`) captures on the server side; it
  sends no assertion.

> **Design-check note (2026-10-06): reject a malformed assertion before anything can fail the
> step (DC-8).**
> - **Problem.** `IngestResult` runs `validateRequiredOutputs` (`result.go:108`) before it calls
>   `SubmitStepResult`, and a missing output *fails* the step (`:115`). A request with a
>   malformed `expected_head_sha` and a missing output would therefore fail the step instead of
>   returning 400.
> - **Change.** Rule 1's pattern check also runs in `IngestResult`, beside the
>   `execution_id`/`outcome_id` checks (`result.go:24-29`). `captureHead` repeats it for callers
>   that use `SubmitStepResult` directly.
> - **Confirmed: rule 2's "terminal" case is load-bearing.**
>   - The standalone scheduler's step recovery re-submits *completed* executions through
>     `SubmitStepResult` (`service/cmd/spine/serve_builders.go:306`, `serve_resolver.go:175`).
>   - `EvaluateStepTransition` lets that through as an idempotent no-op
>     (`core/workflow/step_machine.go:43-50`), so `routeStepOutcome` runs again.
>   - With no capture on that path, the reserved keys are carried over from `current`, and the merge
>     uses the last recorded binding. Under `reject`, that binding equals the head the gate checked.
>   - AC 27 pins this.

### 5. Rebinding after convergence (`step.go:604-607`)

After `EvaluateAndCommit` succeeds and binding is on:
- Resolve `H` for the run branch.
- Write `{spine.bound_sha: H, spine.bound_by: "convergence:<divergence_id>"}` through
  `mergeCommitMeta`, using a freshly read `run.CommitMeta`.
- On error, log at Warn. The next gate's moved-head check then fails closed.

Convergence moves the head on the engine's behalf. Without this rebind, the evaluation gate would
always trip.

> **Design-check note (2026-10-06): rebind after convergence only from the bound head (DC-6).**
> - **Problem.** As written, §5 binds whatever the run branch holds after `EvaluateAndCommit`.
>   That includes commits anyone pushed to the main run branch while the divergence was open. The
>   evaluation gate's moved-head check then passes, because `prev` is the new head.
> - **Change.**
>   - Resolve `H0` before `EvaluateAndCommit` (`step.go:604`).
>   - Rebind to the post-convergence head `H` only if `prev` is absent or `H0 == prev`, and
>     `IsAncestor(H0, H)` holds.
>   - Otherwise log at Warn and leave the binding unchanged. The evaluation gate then fails closed
>     with `run_branch_moved_since_gate`.
> - **`bound_by` format.** It becomes `convergence:<run_id>:<divergence_id>` (DC-9).
> - **Residual.** A push between reading `H0` and the convergence merge still rides along. This is
>   dormant, since no workflow in either repository diverges, and it is added to
>   KNOWN-LIMITATIONS.
> - **Tests.** AC 16 and AC 28.

### 6. Merging (`core/engine/merge.go`)

**`MergeRunBranch` (`:37-49`).**

```go
run, done, err := o.preflightCheck(ctx, runID)
if err != nil || done { return err }
src, done, err := o.resolveMergeSource(ctx, run) // zero value when binding is off
if err != nil || done { return err }
if retried, err := o.mergeCodeRepos(ctx, run); err != nil || retried { return err }
if done, err := o.mergePrimary(ctx, run, src); err != nil || done { return err }
return o.verifyAndComplete(ctx, run, src)
```

- `preflightCheck` keeps the branch-protection check. Its status-cascade block (`:100-107`) runs
  only when binding is off.

**`resolveMergeSource`, binding on.**
1. Read `bound := run.CommitMeta["spine.bound_sha"]`. If it is missing or does not match the hex
   pattern, call `bindingFailure`.
2. Call `VerifyCommit(bound)`.
   - A retryable `*git.GitError` calls `retryMerge` (done).
   - Any other error calls `bindingFailure`.
3. Cascade (§7): if `run.CommitMeta["status"]` is set, `C := applyCommitStatusAt(run, status,
   bound)`. On error, log at Warn and use `bound`. This is today's "proceed with merge" semantics
   (`:103-106`).
4. Return `mergeSource{bound, sha: C}`.

> **Design-check note (2026-10-06): trust a stored binding only if this run wrote it (DC-9).**
> - **Problem.** Stripping (§1) stops new plants. But a `spine.bound_sha` that a pre-TASK-005
>   engine stored from a workflow outcome's `commit:` map survives the upgrade, and projection
>   loading skips `Validate` (P3-13). A run sitting in `committing` at enablement would then merge
>   the planted commit, because no capture happens after enablement to overwrite it.
> - **Change: every `spine.bound_by` value names the run.**
>   - `step:<step_id>:<execution_id>`. Execution IDs already start with `<run_id>-`
>     (`run.go:78`, `step.go:425`, `step.go:623`, `retry.go:84`).
>   - `convergence:<run_id>:<divergence_id>`.
>   - `merge_recovery:<run_id>:<operation>`.
> - **Check.** In step 1, `resolveMergeSource` calls `bindingFailure` unless `spine.bound_by` is
>   present, well-formed and names `run.RunID`.
> - **Why it works.** A workflow file cannot name a run ID in advance: run IDs come from a random
>   trace ID (`run.go:179`). The check is a pure string check with no store read.
> - **Test.** AC 29.

**`bindingFailure`.**
- It records the primary outcome as failed with an error wrapped as
  `*git.GitError{Kind: ErrKindNotFound, Op: "merge_binding"}`. `classifyMergeFailure` therefore
  stores class `precondition` (`multi_repo_merge.go:319-321`).
- It then calls `handlePermanentMergeFailure` (`merge.go:309-321`). A workflow with a publish step
  routes `merge_failed` back to `execute`. A legacy terminal-commit workflow fails the run.
- It returns done.

**`mergePrimary`, binding on.**
- `MergeBound` with `{Source: run.BranchName, SourceSHA: src.sha, Target, Strategy, Message,
  Trailers}` replaces `:168-174`.
- Merge-error handling (`:179-212`) is unchanged, except that the collision-renumber branch
  (`:200-207`) is skipped. `WithCollisionHandler` is unwired in both products, and a renumber
  commits on the branch tip (Decision 11).
- **`verifyBoundMerge`** runs after a successful merge and before any push.
  1. `IsAncestor(src.sha, M)` must be true.
  2. `tip := ResolveBranchHead(run.BranchName)`. On error, log and skip step 3.
  3. If `tip != src.bound` and `tip` is not an ancestor of `src.bound`, set
     `src.excludedTip = tip`. If `IsAncestor(tip, M)` is true, that is a violation.
  - A violation, or an `IsAncestor` error, records the outcome as failed (permanent,
    `Op: "merge_binding"`). It then calls `handlePermanentMergeFailure`. Nothing is pushed.

> **Design-check note (2026-10-06): verify the merge's structure, not the run-branch tip (DC-3).
> This replaces steps 1–3 above.**
> - **Problem 1: step 3 is a lever for the attacker this task assumes.**
>   - The attacker force-pushes `refs/heads/<run>` to `main`'s current tip, or to any `main`
>     commit that is not in the bound commit's history. That holds whenever `main` has advanced
>     since the run started.
>   - Then `tip ≠ bound`, `tip` is not an ancestor of `bound`, and `IsAncestor(tip, M)` is true,
>     because `tip` is `M`'s first parent.
>   - The approved merge is refused as a violation, and the run loops back to `execute`.
> - **Problem 2: steps 1–3 do not prove that `M` holds nothing else.** An implementation that
>   merged `src.sha` and also the tip would pass step 1.
> - **Change.** `verifyBoundMerge` accepts exactly one of these shapes:
>   - `M == BaseSHA` and `IsAncestor(src.sha, BaseSHA)` (already up to date);
>   - `M == src.sha` and `IsAncestor(BaseSHA, src.sha)` (a fast-forward; `merge-commit` never
>     produces it, but it is accepted for completeness);
>   - `CommitParents(M) == [BaseSHA, src.sha]`.
> - **Anything else fails.** Any other shape, an empty `BaseSHA` or an error is the permanent
>   `merge_binding` failure, and nothing is pushed. Every accepted shape has `BaseSHA` as an
>   ancestor of `M`, so the push (DC-1) is always a fast-forward of the verified base.
> - **The tip is still read (step 2), but only to report.** Set `src.excludedTip = tip` if and
>   only if the tip resolved and `!IsAncestor(tip, M)`. The tip never makes the merge fail.
> - **Tests.** AC 11, AC 12 and AC 25.

- **Push** (`:228-246`): `PushCommitCAS(ctx, "origin", authoritativeBranch, M,
  mergeResult.BaseSHA)`.
  - An empty `BaseSHA` is a permanent failure; nothing is pushed.
  - Error classification does not change. On a lease mismatch, git writes
    `! [rejected] … (stale info)` and `error: failed to push some refs` to stderr.
    `classifyGitError` already maps both to permanent (`adapters/git/errors.go`), the same as
    today's non-fast-forward rejection (Decision 6). This was checked with git 2.54 for this
    design.
- If `src.excludedTip != ""`, log at Warn: "merge_binding: commits after the bound head were not
  merged", with `run_id`, `bound_sha` and `tip_sha`.

> **Design-check note (2026-10-06): the push is a non-forced, fast-forward-only push of the verified
> SHA, not `--force-with-lease` (DC-1).** Checked with git 2.54 in a scratch repository.
> - **The lease turns a transient push failure into a permanent one.**
>   - After a transient push failure, local `main` already holds the merge `M`, while origin is
>     still at the old base `B`.
>   - The retry's `MergeBound` reports `BaseSHA = M`, the local tip. `--force-with-lease=
>     refs/heads/main:M` is then refused with `! [rejected] … (stale info)`, which
>     `classifyGitError` maps to permanent (verified).
>   - One network blip on a push therefore becomes `merge_failed` (publish workflows) or a failed
>     run (legacy workflows).
>   - The same refusal follows when the first push actually landed but the client saw a network
>     error.
>   - In standalone, the unpushed `M` also stays in local `main`. Every later governed merge in the
>     workspace computes its `BaseSHA` on top of it and is refused the same way, until an operator
>     re-syncs `main`.
>   - Today, the retry's plain `git push origin main` fast-forwards origin and succeeds.
>   - SMP has the same shape. Its `Push` deliberately keeps the local merge across a transient
>     failure (`internal/customer/spinecore/gitoperator.go:536-549`). §14's SMP `MergeBound`
>     fast-forwards the target non-forcibly (`:412-419`), so `BaseSHA` is the unpushed local tip
>     there too.
> - **`--force-with-lease` is a force.**
>   - With a matching lease, a SHA that does not descend from origin's tip *rewrites* `main`
>     (verified: `+ e5bc4c4...7e1cabc (forced update)`).
>   - §6 as written never checks `IsAncestor(BaseSHA, M)`. A buggy `MergeBound` could therefore
>     rewind `main`; SMP's will be new code.
> - **A plain push of the SHA gives the intended property.** `git push <remote>
>   <sha>:refs/heads/<branch>`, with no `--force`, no `+` and no lease:
>   - publishes exactly `sha`, wherever local `main` points;
>   - succeeds when origin is behind, which is the retry case (verified);
>   - is "Everything up-to-date" when origin already has `sha`;
>   - is refused with `! [rejected] … (fetch first)` or `(non-fast-forward)` when origin moved to
>     something `sha` does not contain. That is classified permanent, as today (verified).
>
>   This is the safety the lease was meant to give, without the false refusals.
> - **Change.** Call `PushCommit(ctx, "origin", authoritativeBranch, M)`. Keep the
>   classification and the empty-`BaseSHA` rule.
> - **Tests.** AC 13 and AC 24.

**`verifyAndComplete` (`:278-301`).** `completeAfterMerge(ctx, run, src.excludedTip == "")` at
`:297`. A branch whose excluded commits were refused is kept for inspection (owner question 3).

**`abortMerge` (`:324-333`)** is unchanged.

**The committing window (window c).** From the capture at the approving completion onward, the
bound value is frozen. Every attempt merges it: the immediate attempt (`run.go:442` or the publish
handler), every scheduler commit retry, and every resume from `partially-merged`. Only the engine
moves it again, through §5 or §8.

> **Design-check note (2026-10-06): attempts must be idempotent (DC-2).**
> - **Problem.** Every attempt above recomputes the cascade (§7). So that a retry after a landed
>   local merge is "already up to date", the cascade must return the *same* commit each time. See
>   the §7 note.
> - **With DC-1 and DC-2:**
>   - A transient push failure recovers on the next attempt.
>   - A replay after a landed merge adds no commit to `main`.
>   - This holds for a `partially-merged` resume and for the concurrent immediate-plus-scheduler
>     attempt.
> - **Test.** AC 24.

### 7. Status cascade on the bound commit (`merge.go`)

`applyCommitStatusAt(ctx, run, status, bound) (string, error)` uses the same filters as
`applyCommitStatus` (`:599-641`), with these differences:
- Paths come from `cascadePathsAt(bound)`: `MergeBase(main, bound)` and then `Diff(base, bound)`.
  A file added after the bound commit is never considered.
- Files are read with `ReadFile(bound, path)`.
- Rewrites are written with `CommitFilesOnto(bound, updates, CommitOpts{Message, Trailers})`. The
  message text is the same as at `:652-655`; the trailers are `Run-ID` and `Trace-ID`.
- There is no `Checkout`, no `WriteAndStageFile`, and the branch ref does not move.
- It returns `bound` unchanged when nothing needs rewriting.

The old `applyCommitStatus` and `cascadePaths` stay exactly as they are for binding off.

Consequence under binding: the cascade commit is no longer on the run branch. It reaches `main`
only through the merge. This also stops a `merge_failed` loop-back from leaving `status: Completed`
on the branch that the executor reworks.

> **Design-check note (2026-10-06): `CommitFilesOnto` must be deterministic (DC-2).**
> - **Problem.** `git commit-tree` stamps the current time. Two calls with the same base, files and
>   message return different SHAs (verified: `7a7ee26…` and `a2d1a77…`, one second apart). With
>   `GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` pinned, both calls returned `270cd79…`.
> - **Why it matters.** Today's cascade is naturally idempotent: a retry finds the status already
>   rewritten on the branch and makes no commit. Under binding as written, every attempt makes a
>   new `C'`. `MergeBound(C')` onto a `main` that already holds `C` is then not "already up to
>   date". It lands a second, empty merge commit and a second cascade commit on `main` for each
>   retry and each `partially-merged` resume. §9's "already up to date" claim does not hold.
> - **Change.** `CommitFilesOnto` pins the author and committer dates to `base`'s committer date
>   (`git log -1 --format=%cI <base>`). It uses the identity `CLIClient` commits with today. The
>   same inputs then always give the same commit, and a retry merges an object `main` already
>   holds.
> - **Considered.** Recording the cascade SHA in a reserved key also works, but the pinned date
>   needs no state.
> - **Tests.** AC 10 and AC 24.

### 8. Code repositories (P3-37)

- **Start.** In `createRunBranches`, after the wiring check (`core/engine/multi_repo_branch.go:68-71`),
  when binding is on: resolve every code repository's client and require a `CommitBinder`.
  Otherwise return `ErrPrecondition` ("merge binding requires SHA-capable clients for code
  repositories") before any branch exists.
- **Capture.** `captureHead` resolves `run.RepositoryBranches[id]`, or `run.BranchName`, in each
  code repository. The required and best-effort rules of §4 apply. The moved-head check compares
  against `spine.repo.<id>.bound_sha`.
- **Merge** (`core/engine/multi_repo_merge.go`).
  - **Skip rule** (`:124-130`) under binding:
    - A terminal `merged` outcome is skipped only when
      `IsAncestor(repoBound, ResolveBranchHead(DefaultBranch))` is true.
    - Otherwise the repository is merged again. This fixes the loop-back drop.
    - Other terminal statuses are skipped as today.
  - **`attemptCodeRepositoryMerge`** (`:204-210`):
    - A missing or invalid `repoBound`, or a client that is not a `CommitBinder`, gives a failed
      outcome with class `precondition` (permanent).
    - Otherwise it calls `MergeBound(SourceSHA: repoBound)` and checks `IsAncestor(repoBound, M)`.
    - It calls `PushCommitCAS(origin, DefaultBranch, M, BaseSHA)` instead of `Push` (`:237`).
- **Recovery** (`core/engine/merge_resolution.go`).
  - **`RetryRepositoryMerge`** (`:212`) under binding rebinds `spine.repo.<id>.bound_sha` to the
    code repository's current run-branch head. An operator fixing a conflict on the branch is
    exactly this case. The new SHA is recorded as a `Rebound-SHA` trailer on the ledger commit and
    in the event payload. Operators can already resolve a repository outside Spine, so this adds
    no authority.
  - **`writeMergeRecoveryLedgerCommit`** (`:499-591`) under binding:
    - `L := CommitFilesOnto(primary bound, nil, CommitOpts{…, AllowEmpty: true})`.
    - Write `{spine.bound_sha: L, spine.bound_by: "merge_recovery:<operation>"}`.
    - Skip the `Checkout` (`:519`), the branch `Commit` (`:567`) and the `PushBranch`
      (`:577-581`).
    - `L` reaches `main` through the resumed merge, as the ledger does today.

> **Design-check note (2026-10-06): an operator retry is not a content approval (DC-7).**
> - **Problem.** `run.merge.retry` (`core/auth/permissions.go:33`, operator) means "try again",
>   typically after a transient failure. Rebinding to the code repository's current head turns it
>   into approval of whatever anyone pushed to that branch since the gate, which the operator never
>   reviewed.
> - **Change (default; owner question 7).**
>   - `RetryRepositoryMerge` rebinds only when the request carries `head_sha`, a new optional field
>     on the retry endpoint (`service/internal/gateway/handlers_workflow.go:334`), checked against
>     the hex pattern.
>   - `head_sha` must equal the code repository's current run-branch head. If it does not, the
>     result is `ErrConflict` (`run_branch_moved`).
>   - Without `head_sha`, the binding is kept, and the retry merges the bound commit again.
> - **Audit.** The `Rebound-SHA` trailer and the event payload record the rebind.
> - **Push.** Code repositories also push with `PushCommit` (DC-1), and their merges get
>   `verifyBoundMerge`'s structural check (DC-3).
> - **No deployed behaviour changes.** No code-repo catalog is wired.
> - **Ledger `bound_by`.** It becomes `merge_recovery:<run_id>:<operation>` (DC-9).

- **KNOWN-LIMITATIONS.** With binding off, code-repo merges still merge the tip and the loop-back
  skip remains. Both paths are dormant until a code-repo catalog is wired.

### 9. `CLIClient` implements `CommitBinder` (`adapters/git/cli.go`)

TASK-002 lands first and owns `Merge`'s argv shape: qualified branch arguments, validated refs and
`--end-of-options`. This task adds the following on top.

- **`Merge` (`:365-404`).**
  - When `opts.SourceSHA != ""`: run `VerifyCommit(SourceSHA)` first. The merge's positional
    argument is `SourceSHA`, placed after TASK-002's terminator, and `Source` is not used.
  - When it is empty: TASK-002's behaviour, unchanged.
- **`MergeBound`.**
  - `SourceSHA` and `Target` are required.
  - Check out the target using TASK-002's qualified form, then set
    `BaseSHA := rev-parse --verify refs/heads/<target>^{commit}`.
  - Merge as above and return `BaseSHA`.
  - "Already up to date" (a retry after a merge that landed) returns `SHA == BaseSHA`. The CAS push
    of that SHA is then a no-op.
- **`ResolveBranchHead`.**
  - Validate the name with TASK-002's grammar.
  - Run `git rev-parse --verify --quiet refs/heads/<branch>^{commit}`. The ref is always fully
    qualified, so a pushed `refs/tags/<branch>` cannot shadow it (F10), and the argument can never
    start with `-`.
  - An unknown ref returns `*GitError{Kind: ErrKindNotFound}`.
- **`VerifyCommit`.** The hex pattern, then `git cat-file -t <sha>` must print `commit`.
- **`IsAncestor`.** Both arguments are checked against the hex pattern. Run
  `git merge-base --is-ancestor`: exit 0 means true, exit 1 means false, anything else is an error.
- **`CommitFilesOnto`** (plumbing only).
  1. Use a temporary `GIT_INDEX_FILE` outside the repository and run `read-tree <base>`.
  2. For each path, in sorted order:
     - The path must be relative and clean, with no `..`, no `.git` component (case-insensitive)
       and no NUL or control bytes.
     - `ls-tree -z <base> -- <path>` must show the entry as absent, `100644` or `100755`. The mode
       is preserved. Symlinks and gitlinks are refused.
     - Then `hash-object -w --stdin` and `update-index --add --cacheinfo`.
  3. Run `write-tree`, then `commit-tree <tree> -p <base>` with `buildCommitMessage(opts)` on
     stdin.
  - It never touches `HEAD`, refs, the real index or the working tree. This removes the F5 and F9
    working-tree surfaces from the cascade path.
  - It needs a small run helper with extra env and stdin, because `run` (`:626`) supports neither.
- **`PushCommitCAS`.**
  - Run `git push --force-with-lease=refs/heads/<branch>:<expectedOld> <remote>
    <sha>:refs/heads/<branch>` through `run(op "push")`, so credentials apply (`:626-660`).
  - Do not use `--porcelain`: it moves the `[rejected]` line to stdout, and `run` classifies on
    stderr only.
  - Checked for this design: the push sends the object even when the local `refs/heads/main`
    points elsewhere, and a moved remote is refused with `(stale info)`.
  - `remote` must pass `ValidateRef`. `sha` and `expectedOld` must match the hex pattern.

> **Design-check notes (2026-10-06) on §9.**
> - **`PushCommit` replaces `PushCommitCAS` (DC-1).**
>   - Argv: `git push --end-of-options <remote> <sha>:refs/heads/<branch>`, through `run(op
>     "push")`.
>   - No `--force`, no `+` prefix and no `--force-with-lease`.
>   - Validate the remote with TASK-002's `ValidateRemoteName` and the branch with
>     `QualifyBranch`, not `ValidateRef`. TASK-002 §1 already names these two helpers for this task
>     (DC-5).
>   - `sha` must pass `VerifyCommit`'s pattern.
> - **`MergeBound` "already up to date" (above).** This holds only with DC-2's deterministic
>   cascade. The push of that SHA is then a no-op when origin has it, and a fast-forward when an
>   earlier push failed.
> - **`VerifyCommit` must not let a ref name pass as an object ID (DC-4).**
>   - git treats a hex string as an object name only when its length matches the repository's
>     object format. In a SHA-1 repository, a 64-hex string is resolved as a ref name.
>   - With `refs/tags/<64 hex>` present, `git cat-file -t <64 hex>` prints `commit`, and
>     `git merge-base --is-ancestor` accepts it (verified, git 2.54). A 40-hex ref name, by
>     contrast, is ignored by git (verified).
>   - **Change.** Run `git rev-parse --verify --quiet --end-of-options <sha>^{commit}` and require
>     the output to equal `<sha>` exactly. This one call also refuses tree, blob and annotated-tag
>     IDs, because the peeled output differs from the input.
>   - The inputs are engine-produced today, so this is defence in depth.
> - **`CommitParents` (DC-3).** It runs `git rev-list --parents -n 1 --end-of-options <sha>`,
>   checks the hex pattern first, and returns the parents in order.

### 10. Workflow parser (`core/workflow/parser.go`)

- The outcome loop (`:190-211`) adds `schemaError(oPrefix+".commit", …)` for any commit key whose
  lowercased form starts with `spine.`.
- This is for user experience only. The engine strip (§1) is the enforcement, because projection
  loading skips `Validate` (P3-13, which TASK-011 fixes).

### 11. Gateway and API (standalone)

- `stepSubmitRequest` (`service/internal/gateway/handlers_workflow.go:31-36`) gains
  `ExpectedHeadSHA string \`json:"expected_head_sha,omitempty"\``. It is passed into
  `ResultSubmission` at `:443-447`.
- `ResultSubmission` (`service/internal/gateway/server.go:261-265`) gains the field.
  `resultAdapter.IngestResult` (`service/cmd/spine/serve_wiring.go:60-75`) passes it on.
- `api/spec.yaml`:
  - `StepSubmitRequest` (`:1319-1346`) gets `expected_head_sha`: a string with pattern
    `^([0-9a-f]{40}|[0-9a-f]{64})$`.
  - The 409 for `step.submit` documents `run_branch_moved` and `run_branch_moved_since_gate`.
  - A 400 is added for an invalid or inapplicable assertion.

### 12. Documentation

- **`architecture/git-integration.md` §6.3 "Merge conditions"** (`:342-379`): the merged content
  is the bound commit, the committing window rule, and the moved-head policy.
- **`architecture/api-operations.md`** (`:144`, `:160`): `step.submit`, the assertion and its
  errors.
- **`KNOWN-LIMITATIONS.md`**, new section "Governed merge binding":
  1. With binding off, the merge uses the tip. This is SMP's state until SMP TASK-009.
  2. Steps of type `automated` are not gates. Window (a) for a validate step relies on the runner's
     assertion (owner question 5).
  3. A workflow whose first step is a gate has no earlier binding to compare with.
  4. The code-repo limitations listed in §8.
- ADR-009 is not edited here. TASK-006 owns its governance-guard amendment.

> **Design-check note (2026-10-06): two more KNOWN-LIMITATIONS entries (DC-10).**
> 5. **A gate binds only when its advancing outcome reaches the merge through gates or internal
>    steps.** In a shape such as `review → finalize (manual) → end + commit`, `finalize` is a
>    producer and rebinds to the tip, so the review does not decide what merges. No shipped workflow
>    in either repository has this shape (checked). The per-step `binds_head` follow-up (owner
>    question 5) covers it.
> 6. **The DC-6 residual for convergence:** a push between reading `H0` and the convergence merge.

> **Design-check note (2026-10-06): a standalone behaviour change to document (DC-12).** Under
> `reject`, a commit made on the run branch *during* a gate step makes that gate's approval fail
> with 409. This includes a reviewer's own fix-up through `write_context` / `/artifacts/add`
> (`service/internal/gateway/handlers_artifacts.go:87`, `:263`). The reviewer must send the work
> back so that it is validated again. `api-operations.md` and the 409 description must say so
> (owner question 4).

### 13. Data, format and migration

- No migration in either repository. The only format change is the reserved `commit_meta` keys
  (§1).
- **Runs in flight at enablement.**
  - A run with no `spine.bound_sha` fails closed at merge (owner question 2).
  - A run that completes any step after enablement records a binding.
- **Turning binding off again.** Reserved keys stop being read. They are dropped at the next
  commit-carrying outcome, since replace semantics apply with binding off.

### 14. Backward compatibility and SMP

**SMP with the option off** (this task's compatibility gate):
- The new fields are additive and keyed. `engine.GitOperator`, `RunStore` and every store
  interface are unchanged.
- `RealGitOperator` is not a `CommitBinder`. It keeps compiling and ignores `SourceSHA`, and SMP
  does not call `EnableMergeBinding`.
- No reserved keys are written. SMP workflows declare no `spine.*` commit keys and no divergence.
- `CLIClient` uses in SMP are unaffected: `projectionwriter`, `provision_steps_runtime`,
  `runbranchdiffreader`, `workflowwriter` and `artifactservice` never set `SourceSHA`.
- SMP's `spinecore` and `spineembedded` merge and submit suites must pass without edits against
  this branch.

**What SMP TASK-009 must change** (in this order, after SMP TASK-002 and TASK-005):

1. **Implement `engine.CommitBinder` on `*spinecore.RealGitOperator`.**
   - `ResolveBranchHead` must read origin with an exact `refs/heads/<branch>`, using `ls-remote`
     or a fetch into a private ref; never bare-name DWIM (P3-7). The object must be fetched into
     the persistent clone, so that `VerifyCommit`, `CommitFilesOnto` and `MergeBound` find it
     later.
   - `CommitFilesOnto` must use plumbing in the persistent clone, with a temporary
     `GIT_INDEX_FILE`.
   - `MergeBound` must fast-forward only the *target* from origin (not `Source`) and merge
     `SourceSHA` in a `WriteScope`. It reports `BaseSHA`.
   - `PushCommitCAS` must push `<sha>:refs/heads/main` with
     `--force-with-lease=refs/heads/main:<BaseSHA>` through `cmdWithCredentials`.
   - Hold the write gate across `MergeBound` and `PushCommitCAS`, and release it on every path
     (P3-34, SMP TASK-002).

   > **Design-check note (2026-10-06): SMP TASK-009 items 1 and 2 change (DC-1, DC-2, DC-3, DC-4).**
   > - **Push.** Replace the `PushCommitCAS` bullet with `PushCommit`: `push --end-of-options
   >   <url> <sha>:refs/heads/main` through `cmdWithCredentials`, with no force and no lease.
   >   - Keep today's rule (`gitoperator.go:536-549`): a *permanent* failure deletes the local
   >     `main` ref so that the next `MergeBound` re-creates it from origin, and a *transient*
   >     failure keeps it.
   >   - With a lease, the transient case would be refused on retry, as shown in the §6 Push note.
   > - **More methods.** `RealGitOperator` must also implement `CommitParents`.
   > - **`CommitFilesOnto`.** It must pin both dates to the base commit's committer date, so that
   >   it is deterministic.
   > - **`VerifyCommit`.** It must use the output-equality check.
   > - **Conformance.** The conformance suite (AC 14) covers all four.

2. **Run the shared conformance suite** (`core/engine/enginetest`, AC 14) against
   `RealGitOperator`.
3. **Enable binding** in `newOrchestratorWithRunStore`, after `WithBranchProtectPolicy`
   (`internal/customer/spinecore/factory.go:209`).
   - Enable only when `gitOp` is a `*RealGitOperator` (`:251`). The stub operator stays off.
   - An enable error is a factory error.
4. **Send assertions.**
   - `SubmitStepRequest` carries `expected_head_sha` into `engine.SubmitRequest`
     (`internal/customer/spineembedded/proxy.go:1096-1100`, `embedded_ready.go:193-197`).
   - The runner sends the SHA it checked out and tested (validate) or pushed (execute).
   - The review UI sends the head that the run-branch diff endpoint (`runbranchdiffreader`)
     returned with the diff it rendered.
5. **Map errors.** `wrapEmbeddedError` (`proxy.go:1589`) maps the new conflict reasons to a
   distinct 409 code, for example `RUN_BRANCH_MOVED`.
6. **Bump `.spine-revision`** to the Spine commit that contains this task.

Effect in SMP once enabled:
- The status cascade no longer commits onto the run branch. The cascade-driven trigger of P3-2
  (local run ref ahead of origin) disappears, although SMP TASK-002 still fixes the root cause.
- `verify` and `review` (both `type: review`, `workflows/task-default.yaml:82`, `:110`) become
  gates. `validate` (`automated`, `:55`) relies on the runner's assertion.

### 15. Coordination with sibling tasks

- **TASK-002 (blocker).**
  - Its ref grammar must accept a full object ID wherever this task passes one: `ReadFile`,
    `Diff`, `MergeBase`, and the `Merge` positional argument. An object ID must never be qualified
    to `refs/heads/<sha>`.
  - If TASK-002's helper lacks an object-ID case, this task adds `IsObjectID` handling to it.
- **TASK-003.** The `-z` `Diff` and bounded `ReadFile` apply to the cascade at the bound commit
  unchanged.
- **TASK-004.** Working-tree confinement still governs the binding-off cascade.
  `CommitFilesOnto` does not use the working tree.
- **TASK-006** (blocked by this task). The governance-path guard should judge exactly
  `merge-base(main, src.sha)..src.sha`. `src` is available in `MergeRunBranch` after
  `resolveMergeSource`; the guard plugs in there rather than in `preflightCheck`.
- **TASK-008** (blocked by this task).
  - Output evidence should be judged at the bound head.
  - Its compare-and-swap step writes may make the `commit_meta` write compare-and-swap too. Today
    a lost update needs two concurrent main-path completions on one run, which no flow produces.
- **TASK-012.** Owns working-tree serialization and bounded commit retries. A lease mismatch stays
  permanent here; TASK-012 may revisit that.

  > **Design-check note (2026-10-06): TASK-012's handoff must be updated (DC-1).**
  > - TASK-012 §9 accepted "lease mismatch stays permanent" on the premise that a retry
  >   "recomputes 'already up to date' against the same lease and is rejected again". That
  >   premise is the regression DC-1 removes.
  > - With `PushCommit`:
  >   - A transient push failure recovers on the next attempt.
  >   - A non-fast-forward refusal stays permanent, as today.
  >   - TASK-012's "re-sync local `main` from origin" follow-up is still needed, but only for the
  >     non-fast-forward case.
  > - TASK-012's §2.2 lock table and AC 15 wrapper should name `PushCommit` and `CommitParents`
  >   instead of `PushCommitCAS`. Neither of them takes the lock.

- **TASK-016, SMP TASK-005 and SMP TASK-007.** They decide who may push to `spine/*`. This task
  decides what merges.
- **TASK-017.** Its run-owned cascade builds on `applyCommitStatusAt`.
- **SMP TASK-009.** Adopts this task (§14).
- **SMP TASK-012.** Performs the final revision bump.

## Decisions

1. **State goes in reserved `commit_meta` keys, not a new column or table.**
   - There is no migration in Spine or in SMP's `spinemigrations`, and SMP already reads the
     column.
   - *Rejected:* a `runs.bound_sha` column. It needs two migrations and a scan change in SMP's
     hand-written `GetRun`.
2. **Server-side capture plus an optional client assertion, not one of them alone.**
   - The client alone cannot cover the committing window or clients that send nothing.
   - The server alone cannot prove what a reviewer saw.
3. **Capture at completion, not at activation.**
   - Approval happens at completion. "The previous completion" is effectively the activation
     point of the next step.
   - *Rejected:* activation capture. It adds writes in `ActivateStep`, which is on SMP's
     `StartRun` path, for no extra coverage.
4. **The capability has an explicit `MergeBound` method, not just a `SourceSHA` field.**
   - SMP's `RealGitOperator.Merge` would silently ignore an unknown field and merge the tip.
   - Requiring a method that can only mean "merge this SHA" makes that impossible to enable by
     accident.
   - `CLIClient.Merge` also honours `SourceSHA` for direct callers.
5. **The cascade uses plumbing on the bound commit, not checkout and commit on the branch.**
   - Committing on the branch would re-open the window this task closes: a push between the
     checkout and the commit.
   - Plumbing also avoids the working-tree races (F9) and symlink sinks (F5).
6. **The CAS push sends an explicit SHA, and a lease mismatch stays permanent.**
   - Pushing the object publishes exactly the verified commit, even if the local `main` moved
     concurrently.
   - *Rejected:* retrying a lease mismatch as transient. In standalone, the local `main` already
     holds the merge, so a retry would loop. Retry policy belongs to TASK-012.

   > **Design-check note (2026-10-06): Decision 6 is amended (DC-1).** The push still sends an
   > explicit SHA, but *without* force or lease.
   > - git's fast-forward rule gives the "no clobber" property.
   > - The lease refused the normal retry after a transient push failure, because the local `main`
   >   is ahead of origin.
   > - Being a force, the lease could also rewrite `main` if `MergeBound` returned a non-descendant.
   >
   > A non-fast-forward refusal stays permanent, as today.

7. **Opt-in rather than always on.**
   - SMP's operator lacks the capability, so always-on would break SMP at the revision bump.
   - The staged order is: Spine TASK-005, then SMP TASK-009, then the bump.
8. **Reserved keys are stripped even with binding off.**
   - Otherwise a workflow outcome stored before an upgrade could plant a `spine.bound_sha` that
     binding trusts after it.
   - The cost is zero, because no shipped workflow uses the prefix.
9. **Gates are classified by step type, not by a new workflow field.**
   - This needs no change to the workflow format and no edits to SMP workflows.
   - A declared `binds_head` is a follow-up (owner question 5).
10. **P3-37 is fixed under binding, not only documented.**
    - The per-repository capture, `MergeBound` and the containment-checked skip rule reuse the
      primary machinery, which costs little.
    - The loop-back drop becomes a re-merge of reviewed content rather than a silent skip.
    - The binding-off state is recorded in KNOWN-LIMITATIONS.
11. **Collision renumber is skipped under binding.**
    - `WithCollisionHandler` (`orchestrator.go:147`) is wired in neither product.
    - Renumber commits on the branch tip, which would reintroduce window (c).
    - A binding-aware renumber is a follow-up.
12. **An assertion on a divergence-branch step is rejected, not checked against the divergence
    branch.**
    - Neither repository's workflows use divergence (checked).
    - Rejecting is explicit, while silently ignoring would mislead the client.

## Owner questions

The design works without answers to these, using the stated defaults.

1. **Make the assertion mandatory?** Should `expected_head_sha` be required on advancing gate
   submits when binding is on?
   - *Default:* optional in Spine's API, for compatibility with existing standalone clients. SMP
     TASK-009 always sends it.
2. **Runs in flight at enablement.** A run reaching merge with no bound SHA:
   - *Default:* fail closed. Workflows with a publish step route `merge_failed` back to `execute`.
     Legacy terminal-commit workflows fail the run.
   - *Alternative:* bind the tip once, with an audit log entry.
3. **Excluded commits at merge.**
   - *Default:* keep the run branch (skip cleanup) and log at Warn with the bound and tip SHAs.
   - *Alternative:* delete it, as today; the excluded commits are lost.
4. **Moved-head policy.** What happens when a gate step submits an advancing outcome and the head
   has moved since the previous binding?
   - *Default:* `reject`. The approval is refused with 409, and the reviewer can still choose a
     rework outcome.
   - `rebind` accepts the current head. That still closes windows (b) with an assertion and (c),
     but not (a).
   - "Merge the previous binding", which ignores the new commits, was considered. It is not
     implemented unless chosen.
   - *Design-check note (2026-10-06), DC-12:* under `reject`, a standalone reviewer who pushes or
     `write_context`-commits a fix-up during review can no longer approve it in the same step. They
     must send it back to `execute`, so that it is validated again. That is the intended
     governance, but it is a visible change for standalone users, and it is documented (§12 note).
5. **Should `automated` steps also be gates?** For example SMP's `validate`.
   - *Default:* no. Automated steps may commit. The runner's assertion covers validate.
   - A declared per-step flag is the follow-up if the answer is yes.
6. **Escape hatch.** Should standalone have an environment variable to disable binding?
   - *Default:* no. The rollback is to deploy the previous build. A security switch-off in config
     is a footgun.
7. *(Added by the design check, 2026-10-06, DC-7.)* **May an operator's code-repository merge
   retry adopt new commits?**
   - *Default:* only when the operator names them. `RetryRepositoryMerge` rebinds to the code
     repository's current head only when the request carries a matching `head_sha`. Without it,
     the retry merges the bound commit again.
   - *Alternative:* the original design, where every retry rebinds to the current head. An
     operator's "try again" then also approves unreviewed pushes.
   - Dormant until a code-repo catalog is wired.

## Acceptance Criteria

Unless stated otherwise, every test uses the real `git.CLIClient` on temporary repositories.

1. **F2 regression, from `poc/governance/unreviewed_merge_test.go`**
   (`core/engine/merge_binding_regression_test.go`, package `engine_test`, binding on).
   - **(a) Push during review.** After `execute` is submitted, a commit adds `evil.md` and sets
     `.spine/branch-protection.yaml` to `rules: []`. The `review` submit of `accepted` then:
     - returns `ErrConflict` with detail reason `run_branch_moved_since_gate`;
     - leaves `main` without `evil.md`, with its protection file still holding the `main` rule;
     - leaves the run `active`, and the review execution not `completed` with an empty
       `OutcomeID`.
   - **(b) Push in the committing window.** Accept with an unmoved head. A wrapper fails the first
     `MergeBound` with a transient `*git.GitError`, and the run stays `committing`. Push the same
     sneak commit, then call `MergeRunBranch` again, as the scheduler does. Then:
     - the run is `completed`;
     - `main` contains the deliverable but not `evil.md`, and its protection file is unchanged;
     - the tip is not an ancestor of `main`;
     - `refs/heads/<run>` still exists.
2. **F2 regression, from `poc/verify-r1-1/toctou_merge_test.go`** (same file; the workflow mirrors
   task-default: execute, validate, review, publish).
   - **(a) Sneak after validate.** A commit with `src/backdoor.go`, `rules: []` and a rewritten
     `workflows/task-default.yaml` is pushed after `validate` passes. Accepting the review is
     rejected, and `main` contains none of the three files.
   - **(b) Assertion under `rebind`.** The policy is `rebind`, and the reviewer asserts the
     pre-sneak SHA. The result is `ErrConflict` with `run_branch_moved`, and `main` is unchanged.
   - **(c) Clean run.** With no sneak and the assertion equal to the head, the run reaches
     `published`. `main` contains `src/feature.go`, and the primary outcome's `MergeCommitSHA` has
     the bound SHA as an ancestor.
     *Design-check note (2026-10-06):* "Reaches `published`" means the run is `completed` and the
     publish step execution's `OutcomeID` is `published`. `published` is a step outcome, not a run
     status.
3. **Auto-push CAS, end to end** (same file). Auto-push is on with a bare `origin`, and the
   committing-window scenario of 1b is replayed. Then:
   - `origin`'s `main` equals the merge commit that `MergeBound` returned;
   - the sneak is not reachable from `origin`'s `main`.

   *Design-check note (2026-10-06):* with DC-1, this AC is about `PushCommit`. The CAS lease is
   gone; the expectations are unchanged.
4. **The assertion** (`core/engine/merge_binding_test.go`). Each case leaves the step execution's
   status, `ActorID` and `OutcomeID` unchanged, and makes no `SetCommitMeta` call.
   - A mismatch returns `ErrConflict` with `run_branch_moved`.
   - These values return `ErrInvalidParams` with `expected_head_sha_invalid`: a 7-character
     prefix, 39 hex characters, uppercase hex, a non-hex string, and `--upload-pack=x`.
   - Each of these returns `ErrInvalidParams` with `expected_head_sha_not_applicable`: a run with
     no branch, a divergence-branch step, and an internal step.
   - An operator that is not a `CommitBinder`, with binding off, returns `ErrUnavailable` with
     `expected_head_sha_unsupported`.
   - **Gateway** (`service/internal/gateway/gateway_test.go`, beside the existing submit tests): a JSON `expected_head_sha` reaches
     `ResultSubmission.ExpectedHeadSHA`. The `resultAdapter` passes it into
     `engine.SubmitRequest`.
5. **Reserved keys.**
   - A workflow outcome `commit: {status: Completed, spine.bound_sha: <attacker SHA>,
     Spine.Bound_By: x}` stores `status: Completed`, and `spine.bound_sha` equals the
     server-resolved head. The attacker values appear nowhere in `commit_meta`.
   - **Binding off:** `SetCommitMeta` receives exactly `{status: Completed}`.
   - **Parser:** the same workflow fails `Validate` with the error path
     `steps[i].outcomes[j].commit`.
6. **`mergeCommitMeta` table** (pure).
   - A completion without a commit keeps existing non-reserved keys, for example `status: In
     Progress`, and updates the reserved keys.
   - A completion with a commit replaces non-reserved keys: a stale extra key from an earlier
     outcome is dropped, as today. Reserved keys are kept.
   - Neither input map is modified (pointer and contents compared before and after).
7. **Binding off keeps today's behaviour** (SMP compatibility at the Spine level). A recording
   `GitOperator` on a zero-value orchestrator shows:
   - every `MergeOpts` has an empty `SourceSHA` and `Source == run.BranchName`;
   - `Push("origin", "main")` is called;
   - the binding-off cascade makes the same `Checkout`, `WriteAndStageFile` and `Commit` sequence
     as at `61994fe`;
   - no `CommitBinder` method is called.
   - *Design-check note (2026-10-06):* a step that starts a divergence, and whose outcome has
     `commit:`, makes no `SetCommitMeta` call, as at `61994fe`. §4 moves the write above the
     divergence dispatch, so this case needs its own check.

   Also, every existing test under `./core/engine/...` passes with no edits to existing test
   files.
8. **Enabling fails closed.**
   - `EnableMergeBinding` on an operator that is not a `CommitBinder` returns an error, and
     `MergeBindingEnabled()` stays false.
   - An unknown policy returns an error.
   - The standalone builders produce orchestrators with `MergeBindingEnabled() == true`. This is
     tested through `buildOrchestrator` and the workspace-orchestrator builder.
9. **A missing or invalid binding fails closed.** A run in `committing` with no `spine.bound_sha`,
   or with `main` or a 7-character value, gives:
   - no `MergeBound` call;
   - a primary outcome recorded as `failed` with class `precondition`;
   - for a publish-step workflow, a route to `execute`; for a legacy workflow, a `failed` run.
10. **The cascade uses the bound commit.** With binding on and `status: Completed`:
    - `main` has `status: Completed` in the task file and in a child artifact added at or before
      the bound commit;
    - an artifact `.md` added after the bound commit is not on `main`;
    - `refs/heads/<run>` is identical before and after the merge;
    - `CommitFilesOnto` leaves `HEAD`, the index and `git status --porcelain` unchanged.
    - *Design-check note (2026-10-06), DC-2:* two `CommitFilesOnto` calls with the same base, files
      and options, made at least one second apart, return the same SHA.
11. **Excluded commits.** When the tip differs from the bound commit at merge:
    - `IsAncestor(bound, M)` is true and `IsAncestor(tip, M)` is false;
    - the run branch is preserved;
    - the Warn record contains `run_id`, `bound_sha` and `tip_sha`.

    A tip rewound to an ancestor of the bound commit is not treated as excluded, and the branch is
    cleaned up.

    *Design-check note (2026-10-06), DC-3:* "excluded" now means the tip resolved and
    `!IsAncestor(tip, M)`. A tip that is already contained in `M` is never excluded and never fails
    the merge (AC 25).
12. **Post-merge verification.** A fake `CommitBinder` whose `MergeBound` merges the tip gives:
    - no `PushCommitCAS` call;
    - a primary outcome recorded as `failed`, then the `merge_failed` route.

    An empty `BaseSHA` likewise produces no push and a permanent failure.

    *Design-check note (2026-10-06), DC-3.* Read `PushCommitCAS` as `PushCommit`. Each of these
    `MergeBound` results also gives no push, a `failed` outcome and the `merge_failed` route:
    - `M` whose parents are `[BaseSHA, tip]`;
    - `M` whose parents are `[BaseSHA, src.sha, tip]`;
    - `M` whose first parent is not `BaseSHA`, which simulates a rewind;
    - `M == BaseSHA` while `src.sha` is not an ancestor of `BaseSHA`.
13. **The CAS push** (`adapters/git/commitbinder_test.go`, against a bare origin).
    - It pushes exactly `sha`, even when the local `refs/heads/main` points elsewhere.
    - It is rejected with a non-retryable `*git.GitError` when `origin` has moved past
      `expectedOld`.
    - Malformed `sha` or `expectedOld` values are rejected before git runs.

    *Design-check note (2026-10-06), DC-1: this AC is replaced by the following for `PushCommit`.*
    - It pushes exactly `sha`, even when the local `refs/heads/main` points elsewhere.
    - It succeeds when origin's `main` is an ancestor of `sha`. This includes origin lagging behind
      local `main` after an earlier failed push.
    - It is a success no-op when origin already has `sha`.
    - It is refused with a non-retryable `*git.GitError` when origin's `main` is not an ancestor of
      `sha`. Origin's `main` is then unchanged: no forced update.
    - A malformed `sha`, remote or branch is refused before git runs.
    - The recorded argv contains no `--force`, no `--force-with-lease` and no `+` refspec.
14. **`CLIClient` conformance through the shared suite.** The suite is exported as
    `enginetest.RunCommitBinderConformance(t, Harness)` in the new package
    `core/engine/enginetest`, so that SMP TASK-009 can run it against `RealGitOperator`.
    - **`ResolveBranchHead`** ignores a `refs/tags/<branch>` that points elsewhere. It rejects
      invalid names before shelling out, and reports `ErrKindNotFound` for an unknown branch.
    - **`VerifyCommit`** accepts a full commit SHA. It rejects a prefix, uppercase, non-hex, a tree
      SHA, a blob SHA, a missing object, and a value starting with `-`.
    - **`IsAncestor`** returns true or false, and returns an error for a missing object.
    - **`MergeBound`:**
      - errors on an empty `SourceSHA`;
      - merges `SourceSHA` and not `Source`'s tip;
      - returns the pre-merge target tip as `BaseSHA`;
      - is idempotent on "already up to date".
    - **`Merge`** with `SourceSHA` honours it. With an empty `SourceSHA`, the existing `Merge`
      tests pass unchanged.
    - **`CommitFilesOnto`:**
      - produces a commit whose parent is the base and whose tree differs only in the given paths;
      - preserves an executable bit;
      - moves no ref;
      - rejects `../x`, `/abs`, `.git/config`, `a/.GIT/b`, a symlink entry and a NUL in a path.
    - *Design-check note (2026-10-06): additions to the suite.*
      - **`VerifyCommit` (DC-4)** rejects:
        - a 64-hex value in a SHA-1 repository, even when `refs/tags/<that value>` and
          `refs/heads/<that value>` exist;
        - an annotated tag object's SHA.
      - **`CommitParents` (DC-3)** returns `[p1, p2]` for a merge commit, `[p]` for a normal
        commit and `[]` for a root commit. It refuses a non-hex argument before git runs.
      - **`CommitFilesOnto` (DC-2)** is deterministic (AC 10).
      - **`PushCommit` (DC-1)** passes the AC 13 cases.
      - **`MergeBound`'s "already up to date" case** is checked with a source that `main` already
        contains *and* whose cascade was recomputed, using the deterministic `CommitFilesOnto`.
15. **P3-37** (`core/engine/multi_repo_merge_binding_test.go`, package `engine`, gap3 harness,
    real `CLIClient` code repo).
    - **(a) Push after capture.** A commit pushed to the code repository's run branch after the
      gate capture is not on the code repository's `origin` default branch after the merge. This
      is the scratch reproduction, inverted.
    - **(b) Missing capability.** With binding on and a code-repo client that is not a
      `CommitBinder`, `StartRun` returns `ErrPrecondition`. No branch exists in any repository and
      no run row is created.
    - **(c) Loop-back.**
      - Setup: the code repository merges at bound commit R1; the primary fails permanently and
        routes back; new reviewed work R2 is captured.
      - Result: the next merge merges the code repository again, and `origin`'s default branch
        contains R2.
      - With binding off, the same setup keeps today's skip. This pins the documented limitation.
    - **(d) Missing per-repository binding.** The outcome is `failed` with class `precondition`.
    - **(e) Retry rebinds.** Under binding, `RetryRepositoryMerge` rebinds to the code repository's
      current head and writes a `Rebound-SHA` trailer.
      *Design-check note (2026-10-06), DC-7: this case is replaced by three.*
      - With `head_sha` equal to the current head, it rebinds and writes the trailer.
      - With a different `head_sha`, it returns `ErrConflict` (`run_branch_moved`) and changes
        nothing.
      - Without `head_sha`, it keeps `spine.repo.<id>.bound_sha`. A commit pushed after the gate is
        not on the code repository's origin default branch after the resumed merge.
    - **(f) Ledger commit.** Under binding, `writeMergeRecoveryLedgerCommit` creates a commit whose
      parent is the bound commit, rebinds to it, and leaves the run-branch ref unchanged. After the
      resumed merge, `git log main --grep "Operation: merge_recovery_retry"` finds it.
16. **Moved-head policy table** (pure plus engine).
    - The table spans step type (review, convergence, manual, automated, internal), outcome kind
      (advancing, rework, end without commit) and head state (moved, not moved, no previous
      binding).
    - Under `reject`, `ErrConflict` occurs only for an advancing outcome from a gate step whose
      head moved since a previous binding.
    - Under `rebind`, the submit succeeds and the binding becomes the current head.
    - A fake convergence handler that moves the head during `EvaluateAndCommit` leads to a rebind
      with `bound_by: convergence:<id>`. The next gate's advancing submit then succeeds.
      *Design-check note (2026-10-06), DC-6 and DC-9:* the value is
      `convergence:<run_id>:<divergence_id>`. The negative case is AC 28.
17. **Head-resolution failure.** `ResolveBranchHead` returns an error.
    - On a producer completion, or on a gate's advancing outcome: `ErrUnavailable` with
      `run_branch_head_unavailable`, and the step is unchanged.
    - On a gate's rework outcome: the submit succeeds and the reserved keys are unchanged.
18. **Collision renumber is skipped.** With binding on and a stub `CollisionHandler` wired, a
    permanent planning-run merge failure never calls `DetectAndRenumber`.
19. **Scenario suite.** The harness binding is on by default and
    `go test -tags scenario ./service/scenariotest/...` is green. Every `WithoutMergeBinding()` use
    carries a reason comment and is listed in the Implementation Notes.
20. **API and documentation.**
    - `api/spec.yaml` has `expected_head_sha` with its pattern, and the 409 and 400 descriptions.
    - `git-integration.md` §6.3, `api-operations.md` and `KNOWN-LIMITATIONS.md` contain the text
      from §12.
21. **The SMP compatibility gate** passes with SMP `main` at `80995559` and `../spine` on this
    task's branch, with no SMP edits:
    - `go build ./...`;
    - `go vet ./internal/customer/...`;
    - `go test ./internal/customer/spinecore/... ./internal/customer/spineembedded/...`.

    The results are recorded in the Implementation Notes.

    *Design-check note (2026-10-06):*
    - **Baseline.** If a Spine task merged before this one needed an SMP adaptation (for example,
      TASK-002), the baseline is the SMP commit that adopted it, not `80995559`. The rule is still
      "no SMP edit *for this task*".
    - **Commands.** Run SMP's `dev/lint-spine-internals.sh` as well. `core/engine/enginetest` is
      under `core/…`, so it is allowed.
22. **Fails before the fix, checked by mutation.**
    - The Implementation Notes record both PoCs passing (vulnerable) on `61994fe`.
    - They also record each mutation below making at least the listed AC fail.

    | Mutation | Fails |
    |---|---|
    | M1: drop the `EnableMergeBinding` call in the regression tests, or pass `SourceSHA: ""` in `mergePrimary` | AC 1, 2, 3 |
    | M2: remove `stripReserved` | AC 5 |
    | M3: make the moved-head check always accept | AC 1a, 2a, 16 |
    | M4: replace `PushCommitCAS` with `Push("origin", "main")` | AC 3, 12 |
    | M5: remove `verifyBoundMerge` | AC 12 |
    | M6: run the cascade at `run.BranchName` | AC 10 |
    | M7: drop `SourceSHA` in `attemptCodeRepositoryMerge` | AC 15a |
    | M8: remove the capability assertion in `EnableMergeBinding` | AC 8 |
    | M9: restore the unconditional terminal skip | AC 15c |

    *Design-check note (2026-10-06).*
    - **M4 is not caught as written.** In a serial test, the local `main` equals `M` at push time.
      `Push("origin", "main")` therefore publishes the same commit, and AC 3 and AC 12 still pass.
      M4 is now caught by AC 26.
    - **New mutations:**

    | Mutation | Fails |
    |---|---|
    | M4′: replace `PushCommit` with `Push("origin", "main")` | AC 26 |
    | M10: add `--force` or `--force-with-lease` to `PushCommit` | AC 13 (no forced update), AC 24 |
    | M11: drop the pinned dates in `CommitFilesOnto` | AC 10, AC 24 |
    | M12: restore the tip-based violation (`IsAncestor(tip, M)` fails the merge) | AC 25 |
    | M13: drop the parent-structure check in `verifyBoundMerge` | AC 12 (DC-3 cases) |
    | M14: drop `VerifyCommit`'s output-equality check | AC 14 (64-hex case) |
    | M15: drop the `bound_by` run check | AC 29 |
    | M16: rebind after convergence unconditionally | AC 28 |
    | M17: rebind on `RetryRepositoryMerge` without `head_sha` | AC 15e |
23. **Gates.** All of these pass:
    - `go build ./...`, `go vet ./...` and `go test ./...` (that is, `make test`);
    - `make test-scenario`;
    - `make lint`, including `lint-boundaries`, and `golangci-lint run --new-from-rev main` is
      clean;
    - `make lint-security`, with no new gosec finding;
    - `gofmt -l` is empty;
    - `codex review --base main` has no P1 or P2.

*ACs 24–29 were added by the design check (2026-10-06).*

24. **A transient push failure recovers, and replays add nothing** (DC-1, DC-2;
    `core/engine/merge_binding_regression_test.go`). Auto-push is on, with a bare `origin`.
    - **(a) The push never landed.** A wrapper fails the first `PushCommit` with a transient
      `*git.GitError` and pushes nothing. The run stays `committing`. The second `MergeRunBranch`
      then gives:
      - a `completed` run;
      - origin's `main` containing the bound commit;
      - exactly one merge commit on origin's `main` with the trailer `Run-ID: <run>`, and exactly
        one cascade commit (`git rev-list` count);
      - no `merge_failed` route.
    - **(b) The push landed, but the client saw an error.** The wrapper lets the first push through
      and then returns a transient error. The second attempt succeeds, with the same commit counts.
    - **(c) No workspace wedge.** After (a), an unrelated second run merges and pushes successfully.
    - **(d) Replay after a landed merge.** Calling `MergeRunBranch` again on a run moved back to
      `committing` (the `partially-merged` resume shape) adds no commit to `main`.
25. **A tip reset is not a violation** (DC-3).
    - **Setup.** `main` advances after the run starts. After the approving gate, `refs/heads/<run>`
      is force-pushed to `main`'s current tip.
    - **Expected.**
      - The merge succeeds and the run is `completed`.
      - `main` contains the bound commit.
      - No `merge_failed` route happens.
      - No excluded-commit Warn is logged, because the tip is contained in `M`.
26. **The call sequence with binding on** (catches M4′). A recording wrapper around `CLIClient`
    shows, for one governed merge:
    - exactly one `MergeBound`, with `SourceSHA` equal to the cascade commit (or the bound commit
      when nothing was rewritten);
    - exactly one `PushCommit("origin", "main", M)`, where `M` is `MergeBound`'s `SHA`;
    - no `Push`, `Merge` (other than an abort), `Checkout(<run>)`, `WriteAndStageFile` or `Commit`
      call on the primary path.
27. **Recovery re-submits and assertion ordering** (DC-8).
    - **(a) Recovery re-submit.** With binding on, `SubmitStepResult` on a *completed* gate
      execution (the scheduler step-recovery shape) makes no `ResolveBranchHead` call and keeps
      every reserved key.
    - **(b) Ordering.** Through `IngestResult`, a malformed `expected_head_sha` on a step whose
      `required_outputs` are missing returns `ErrInvalidParams` (`expected_head_sha_invalid`), and
      the step is not `failed`.
28. **The convergence rebind guard** (DC-6). A commit pushed to the main run branch while a
    (fake) divergence is open gives:
    - no rebind after `EvaluateAndCommit`; `spine.bound_by` is unchanged;
    - `ErrConflict` (`run_branch_moved_since_gate`) on the evaluation gate's advancing submit.
29. **Binding provenance** (DC-9). A run in `committing` whose `spine.bound_sha` is a valid commit
    gives no `MergeBound` call and a `precondition` failure when `spine.bound_by` is:
    - missing;
    - `x`;
    - `step:review:run-deadbeef-review-1`, which names another run;
    - `convergence:run-deadbeef:div-1`.

## Out of scope / follow-ups

- **Who may push to `spine/*`.** Covered by TASK-016, SMP TASK-005 and SMP TASK-007.
- **Which paths a governed merge may carry.** Covered by TASK-006.
- **Pinning the bound commit** with a ref such as `refs/spine/bound/<run>`, so it survives `gc`
  after a force-push. Today a `gc`-ed bound commit fails closed (AC 9 path).
- **Declaring gates in the workflow.** A per-step `binds_head` flag (owner question 5), and
  binding at run start for workflows whose first step is a gate.
- **The activation head in the assignment payload**, so runners do not need to resolve it.
- **Returning the bound head in the submit response** (`bound_head_sha`). SMP can read
  `commit_meta` directly meanwhile.
- **Assertions on divergence-branch steps**, and divergence branches in general.
- **A binding-aware collision renumber** (Decision 11).
- **Lease-mismatch retry semantics** (TASK-012).
  *Design-check note (2026-10-06):* with DC-1 there is no lease. What remains for TASK-012 is
  re-syncing local `main` after a *non-fast-forward* refusal.
- *(Added by the design check, 2026-10-06.)* **`.gitattributes` merge drivers in the bound
  content.** For example, `merge=union` can change how a conflicting governed merge resolves. The
  audit did not cover this (REPORT §5). The merged content still comes only from `BaseSHA` and
  `src.sha`, and DC-3 checks that.
- **A dedicated event type for excluded commits.** It needs the delivery subscriber and bootstrap
  lists (`adapters/delivery/subscriber.go:148`, `bootstrap.go:50`). Today this is a Warn log only.
- **Binding-off code-repo behaviour** (KNOWN-LIMITATIONS; dormant).

## Design check

An adversarial design check was run on 2026-10-06 against Spine `61994fe` and SMP `80995559`. Each
change is marked in place as a "Design-check note (2026-10-06)". The designer's text is otherwise
unchanged.

**What was verified and holds.**
- **Both PoCs reproduce.** Run with `go test -overlay` on `61994fe`, both pass, which means
  vulnerable: `governance/unreviewed_merge_test.go` and `verify-r1-1/toctou_merge_test.go`.
- **The core mechanism closes F2 on every path in the report and the PoCs.** The mechanism is a
  server-side capture at each main-path completion, a moved-head refusal at advancing gate
  outcomes, and a merge of the recorded SHA only.
  - Windows (a), (b) and (c) are closed.
  - Every entry into the merge sink reads the frozen binding. There are three such callers: the
    immediate attempt (`run.go:442`), the publish handler (`handlers.go:76`), and the scheduler's
    commit retry and `partially-merged` resume (`serve_builders.go:314`, `serve_resolver.go:167`).
  - `CompleteRun` has a single caller (`step.go:403`).
  - Only the engine writes `commit_meta`: `SetCommitMeta` is called only from `routeStepOutcome`,
    and `CreateRun` does not insert the column.
  - SMP has no other `git merge` site, and SMP restricts run-branch writes to the draft step
    (`artifacts_addtorun.go:79`).
- **File:line references are accurate.** These were checked at both revisions: `result.go`,
  `step.go`, `merge.go`, `multi_repo_merge.go`, `client.go`, `cli.go`, `errors.go`,
  `postgres_runs.go`, `serve_builders.go`, `serve_resolver.go`, `serve_wiring.go`, the gateway
  handlers and `server.go`, `runtime.go`, `ADR-009:194`, and SMP's `gitoperator.go`,
  `runstore.go`, `factory.go`, `proxy.go` and `embedded_ready.go`.
- **The struct changes are additive.** Every literal of the changed structs in both repositories
  is keyed.
- **No `spine.*` commit key exists** in any workflow or test.
- **Both standalone builders pass a concrete `*git.CLIClient`.**
- **SMP may import `core/engine/enginetest`** under its ADR-017 lint.

**What was changed, and why.**

| ID | Change | Why |
|---|---|---|
| DC-1 | `PushCommitCAS` (`--force-with-lease` on `BaseSHA`) becomes `PushCommit`, a non-forced push of the explicit SHA (§2, §6, §9, §14, §15, Decision 6, AC 13, AC 24) | Verified on git 2.54: after a transient push failure, the retry's lease names the unpushed local tip and is refused as `(stale info)`, which is permanent. A network blip became `merge_failed` and, in standalone, wedged every later merge in the workspace. The lease is also a force: it rewrote `main` with a non-descendant. A fast-forward-only push gives the intended safety without either problem. TASK-012's handoff must be updated. |
| DC-2 | `CommitFilesOnto` is deterministic, with dates pinned to the base commit (§6, §7, §14, AC 10, AC 24) | `commit-tree` is time-stamped (verified). Each retry or resume made a new cascade commit plus an empty merge commit on `main`, and §9's "already up to date" claim was false. |
| DC-3 | `verifyBoundMerge` checks `M`'s parent structure through a new `CommitParents`; the tip only reports excluded commits (§2, §6, AC 11, AC 12, AC 25) | The tip-based "violation" let anyone who can push the run branch fail every approved merge by resetting the branch to `main`'s tip. It also did not prove that `M` held nothing else. |
| DC-4 | `VerifyCommit` requires `rev-parse --verify <sha>^{commit}` to output `<sha>` exactly (§9, AC 14) | In a SHA-1 repository, git resolves a 64-hex string as a ref name (verified with a planted tag). The design's pattern accepts 64 hex characters. |
| DC-5 | The remote and branch are validated with TASK-002's `ValidateRemoteName` and `QualifyBranch` (§9) | This aligns §9 with TASK-002 §1, which already names these helpers for this task. |
| DC-6 | The convergence rebind happens only if the pre-convergence head equals the binding (§5, §12, AC 28) | Otherwise, pushes to the main run branch during a divergence became the binding without any check. |
| DC-7 | An operator's code-repository merge retry rebinds only with a matching `head_sha` (§8, AC 15e, owner question 7) | A "retry" is not a content approval. Dormant. |
| DC-8 | The assertion's syntax is checked in `IngestResult` before `validateRequiredOutputs`; the recovery re-submit path is pinned (§4, AC 27) | A malformed assertion could otherwise *fail* the step. The scheduler re-submits completed executions through `SubmitStepResult`. |
| DC-9 | `spine.bound_by` embeds the run ID, and the merge refuses a binding that this run did not write (§6, AC 29) | A pre-upgrade engine could have stored a `spine.bound_sha` from a workflow `commit:` map (P3-13), and a run sitting in `committing` would merge it. Run IDs cannot be predicted in a workflow file. |
| DC-10 | KNOWN-LIMITATIONS: a gate binds only when its advancing outcome reaches the merge (§12) | A producer step after a gate rebinds to the tip. No shipped workflow has that shape. |
| DC-11 | AC 2(c) wording; AC 7 adds the divergence-start case; AC 21 baseline; mutation table M4′ and M10–M17, plus AC 26 | `published` is a step outcome. The §4 restructure needs its binding-off check. M4 was not caught in a serial test. |
| DC-12 | The standalone UX change under `reject` is documented (§12, owner question 4) | A reviewer's fix-up during review now needs a rework round. |

**Residual risks, accepted and documented.**
- Binding stays off in SMP until SMP TASK-009. The embedded F2 remains open until then.
- Window (a) for `automated` validate steps depends on the runner sending its assertion (owner
  question 5). A compromised runner bearer (A6) can submit `validate` without testing anything at
  all, so binding cannot protect "validated" against A6.
- A bound commit that is garbage-collected after a force-push fails closed (AC 9 path); the
  pinning ref is a follow-up.
- A push between reading `H0` and the convergence merge still rides along (dormant).

## Implementation Notes

