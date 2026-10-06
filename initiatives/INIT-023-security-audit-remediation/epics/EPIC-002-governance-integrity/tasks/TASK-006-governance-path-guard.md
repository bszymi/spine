---
id: TASK-006
type: Task
title: "Governance-path guard on governed merges and protection-removal guard in projection"
status: Pending
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
  - type: related_to
    target: /architecture/security-model.md
---

# TASK-006 — Governance-path guard on governed merges and protection-removal guard in projection

---

## Purpose

This task closes audit finding **F3** (P2, both surfaces) from the 2026-10-06 Spine security
audit: "run-branch slippage". A run branch can carry governance files into `main`. The
projection then installs them as live policy and never checks how they arrived.

Line numbers refer to Spine `main` at `61994fe`. SMP references are to SMP `main` at
`80995559`, which pins Spine `61994fe` in `.spine-revision`.

1. **A governed merge is allowed whatever it changes.**
   - `Policy.Evaluate` returns Allow for `OpGovernedMerge` before it reads any rule or any
     fact about the change (`core/branchprotect/policy.go:64-72`).
   - `branchprotect.Request` has no field that could describe the change
     (`core/branchprotect/types.go:95-118`).
   - The engine calls the policy from `preflightCheck` (`core/engine/merge.go:96`, which calls
     `checkBranchProtectMerge` at `:526-567`). The comment at `:83-95` already names this gap
     ("reject merges … on a branch touching .spine/branch-protection.yaml — see ADR-009 §1
     run-branch-slippage") and calls the hook "a single site to plug into".
   - `mergePrimary` merges the whole run branch with no path scoping (`:168-174`).
2. **The projection installs whatever reaches `main`.**
   - `projectBranchProtection` replaces every rule with the parsed file
     (`adapters/projection/service.go:569-620`). With `rules: []` it upserts zero rows at
     `:619`.
   - The rule source reads an empty table as "explicitly unprotected"
     (`core/branchprotect/projection/rule_source.go:59-84`).
   - Workflows are upserted as they arrive (`service.go:284-311`, `:500-553`).
   - Neither path checks who authorised the change.
3. **ADR-009 accepted this gap in v1.**
   - The ADR says it explicitly (`architecture/adr/ADR-009-branch-protection.md:58`, `:61`,
     `:138`, `:140`, `:156-158`, `:194`).
   - The ADR defers the guard to "a future ADR … once there is evidence it is warranted"
     (`:61`). The audit is that evidence.
4. **Attacker and impact.**
   - **Attackers.**
     - A1: anyone who authors run-branch content (an AI agent, a runner job, a contributor).
     - A2: a member who passes or skips review. In SMP today one member can do this alone: they
       self-assign and self-accept the review step. That is the amplifier, which
       TASK-007/SMP TASK-011 close separately.
   - **Steps.**
     1. The attacker commits `.spine/branch-protection.yaml: rules: []` and/or a
        `workflows/task-default.yaml` with the review step removed to their run branch.
     2. Review accepts.
     3. The publish step merges.
     4. The projection installs zero rules.
   - **Result.**
     - Any contributor may direct-write or delete `main`.
     - Every later run uses the review-less workflow.
     - No `branch_protection.override` event is emitted.
   - This is a durable governance takeover without operator authority.
5. **Re-verified for this design (2026-10-06).** The three audit PoCs were compiled into
   Spine `61994fe` with `go test -overlay` (overlay at
   `tmp/init027-orchestration/spine-init023/_work/task006/overlay_poc.json` in SMP). All
   three PASS, which proves the hole:
   - `TestPOC_R12_SelfReviewedRunSlipsGovernanceFilesIntoMain` (`poc/verify-r1-2`): `main`
     ends with `rules: []` and a task-default that has no `review` step.
   - `TestPOC_R12_ProjectionInstallsSlippedPolicy` (`poc/verify-r1-2`): 0 projected rows,
     `sync_state=idle`, and a Contributor is allowed both `direct_write` and `delete` on
     `refs/heads/main`.
   - `TestPOC_ProjectedEmptyRulesetUnprotectsMain` (`poc/store-projection/poc_bp_relax`).

   > **Design-check note (2026-10-06): the planned changed-path computation can be bypassed.**
   > The three PoCs were re-run at `61994fe` and still PASS. A new probe,
   > `tmp/init027-orchestration/spine-init023/_work/task006/designcheck/zz_dc_task006_crisscross_test.go`
   > (overlay `designcheck/overlay.json`, `go test -overlay … -run TestDesignCheck_TASK006 ./core/engine/`),
   > PASSES and shows the bypass with Spine's real `CLIClient`:
   > - the attacker's run branch merges another run's branch (R2, which tightened
   >   `.spine/branch-protection.yaml` and lands on `main` later) and resolves that file back to
   >   the old content;
   > - the run branch now has **two merge bases** with `main`. `git merge-base` returns one of them,
   >   and `Diff(merge-base(main, S), S)` (§3.2 as first designed) lists only `feat.txt`;
   > - the real `Merge` (as `mergePrimary` calls it) uses a virtual base and **reverts the governed
   >   file on `main`**. `Diff(pre-merge main, M)` lists it.
   >
   > The same works for any `workflows/**` file, where no projection backstop exists. §3.2 and
   > §3.4a below close it: union over all merge bases before the merge, plus a post-merge assertion.
6. **Both surfaces are affected.**
   - SMP builds the real orchestrator with the projection-backed policy
     (`internal/customer/spinecore/factory.go:109`, `:209`).
   - SMP runs Spine's `adapters/projection` in-process (`internal/customer/projectionwriter/worker.go:311-331`,
     `internal/customer/workspace/provision_steps_runtime.go:320-327`).
   - SMP already lists run-branch changes under `workflows/**` and `.spine/**` on the run page.
     It says "Surfacing only: enforcement (a human approval when these paths change) belongs
     in Spine" (`internal/customer/spineembedded/artifacts.go:184-202`;
     `internal/executionmeta/profile.go:26`).

**Dependencies and coordination** (INIT-023 plan, wave 4, risk high):

- **spine TASK-005 (hard).** This task needs the governed merge to be bound to one SHA. The
  guard evaluates exactly the commit `mergePrimary` will merge, never a branch name. Without
  TASK-005, anything pushed between the guard and the merge would bypass the guard.
- **spine TASK-011 (hard).** The projection guard only works if `IncrementalSync` stops
  dropping `Diff` errors (`service.go:254`, P3-14). It also needs `head` to be the
  authoritative ref (P3-22), because its reachability and newness checks are relative to it.
- **spine TASK-003 (soft).** That task makes `Diff` NUL-delimited. Until it lands, the guard
  fails closed on any C-quoted path (§2).
- **spine TASK-012 (coordination).**
  - Its bounded merge-retry counter must not count governance holds
    (`errors.Is(err, engine.ErrGovernanceApprovalRequired)`).
  - Its explicit convergence target closes the standalone F8 path. That path merges divergence
    content into `main` outside `MergeRunBranch`, so this guard cannot see it.
- **spine TASK-014 (coordination).** It routes reviewer `workflow.update` into ADR-008
  lifecycle runs. Those runs depend on the lifecycle exemption in §3.3.
- **spine TASK-017 (coordination).** If it changes which paths the merge-time status cascade
  writes, the guard's changed-path set must follow (§3.2).
- **spine TASK-007 (independent).** Separation of duties is the amplifier fix. The guard does
  not depend on it, because the gate is operator approval, not review.
- **SMP TASK-010 (consumer).** It turns both guards on in SMP and adds the approval UI and API
  (§9). The order is: this task, then SMP TASK-009 (the merge-binding bump), then SMP TASK-010
  with its `.spine-revision` bump.

> **Design-check note (2026-10-06): dependency corrections.**
> - **TASK-005 — placement.** TASK-005 §6 reorders `MergeRunBranch` to `preflightCheck →
>   resolveMergeSource → mergeCodeRepos → mergePrimary`, and its §15 says this guard plugs in
>   *after* `resolveMergeSource`, not in `preflightCheck`. This design originally put the guard in
>   `checkBranchProtectMerge` (preflight). In that position a committing run with no
>   `spine.bound_sha` (any run in flight when binding is turned on) got a governance hold that
>   neither approve nor reject could clear: both need a bound SHA (§3.6 checks 6 and 8), and cancel
>   is not allowed from `committing`. The run was stuck for good. §3.4 now places the guard after
>   TASK-005's bound-SHA validation, so a missing binding takes TASK-005's `bindingFailure` route.
> - **TASK-005 — rollback (cross-task requirement).** TASK-005's `verifyBoundMerge` refuses
>   *after* `MergeBound` has moved the local target ref ("nothing is pushed"). In standalone
>   `CLIClient` the local `refs/heads/main` is then at the refused merge commit. With
>   `SPINE_GIT_PUSH_ENABLED=false` that local ref *is* `main`. The projection reads it (today
>   `Head()`; after TASK-011, `refs/heads/main`), and every later merge is built on it. The
>   post-merge assertion (§3.4a) reuses that refusal path. So every post-merge refusal must first
>   restore the local target to `BaseSHA` (CLIClient: `update-ref refs/heads/<target> <BaseSHA>
>   <M>` and a hard reset of the working tree; SMP: the existing `deleteLocalRef`). TASK-005 owns
>   that; if it has not landed there, this task adds it.
> - **TASK-011 — workflow directories.** TASK-011 §5/§11 exports `WorkflowDirs(artifactsDir)` and
>   states this task "must also include `<artifacts_dir>/workflows/`". The original §3.1/§7 did
>   not. `spine init-repo` defaults to `artifacts_dir: spine`
>   (`service/internal/cli/initrepo.go:27`) and seeds `spine/workflows/workflow-lifecycle.yaml`
>   (`:60-92`). `FullRebuild` projects `<dir>/workflows/*.yaml` as live workflows
>   (`core/artifact/discovery.go:59-75`), so a run editing `spine/workflows/task-default.yaml` was
>   not guarded. Fixed in §3.1/§7.
> - **TASK-011 — D2.** TASK-011 D2 rejects making a content rejection a sync error: the diff
>   window would never advance, and SMP's document index stops for the whole workspace
>   (`internal/customer/docindex/indexer.go:205-238`, `internal/docmemory/reconciler.go:45-60` gate
>   on `idle`). The original §5 made a provenance refusal exactly such an error. §5 now aligns
>   with D2.
> - **TASK-007 — not independent for the ADR-008 exemption.** The operator gate does not need
>   separation of duties. The lifecycle exemption (§3.3) does: its only gate *is* the lifecycle
>   review. In standalone a reviewer can self-grant `workflow_review` (F6, until TASK-013) and,
>   without TASK-007's opt-in `SeparationOfDuties`, review their own draft. §3.3 now builds a local
>   SoD check into the exemption predicate.
> - **TASK-008 (coordination).** Reject on a workflow without a publish step goes through
>   `failRunOnMergeError`, which does a blind `UpdateRunStatus` today (`merge.go:457`). A
>   concurrent approve can complete the merge, and the run is then marked `failed`. TASK-008
>   converts that write to `TransitionRunStatus`. Until it lands, §3.6 adds the compare-and-swap.

---

## Design

### 0. Invariants

- **I1 — opt-in.** Both guards are off at their zero value, and the library then behaves exactly
  as v1 does. Spine's own `spine serve` turns them on (owner question 4). SMP turns them on in
  SMP TASK-010.
- **I2 — evaluate what gets merged.** The guard computes the changed paths from
  `merge-base(main, S)..S`, where S is the SHA that TASK-005 binds and `mergePrimary` merges.
- **I3 — fail closed.** If the changed-path set cannot be computed (a git error, an
  unparseable path, no bound SHA), the merge is refused. It is never allowed.
- **I4 — approval is server-side and SHA-bound.**
  - The authorization is an approval by an actor with role operator or higher.
  - The approval is recorded through an engine API, for the bound SHA.
  - It is never read from commit text, branch content or workflow-authored data.
- **I5 — no unproven protection removal.** The projection never installs a config that removes
  protection from the authoritative branch unless a database-backed operator record matches
  the content being installed. When no record matches, the previous rules stay in place.
- **I6 — no schema change.** There is no migration. New state goes into existing JSONB columns
  (`runtime.runs.commit_meta`, `step_executions.error_detail`, `runtime.event_log.payload`).

> **Design-check note (2026-10-06): invariants amended.**
> - **I1.** "Opt-in" still holds. In addition, `WithGovernanceGuard(Enabled: true)` returns an
>   error unless merge binding is on (`o.MergeBindingEnabled()`, TASK-005) and `o.git` implements
>   the `MergeBaseLister` capability (§3.1). Without binding, I2 cannot hold, and the original
>   step-2 deny produced holds nobody could decide.
> - **I2 is now two checks.**
>   - **(a) Before the merge**, the changed paths are the union of `Diff(B, src.sha)` over
>     **every** merge base `B` of (`main`, `src.sha`). If a path's content at `src.sha` equals its
>     content at every merge base, the virtual merge base agrees too, so a standard merge leaves
>     `main`'s version (proof sketch in §3.2).
>   - **(b) After the merge**, before the push: the governed paths that the merge actually changed
>     (`Diff(BaseSHA, M)`) must be a subset of the evaluated `Touched()` set plus the exempt paths
>     (§3.4a). This covers config-dependent merge behaviour (`merge.directoryRenames=true` and
>     merge drivers in the operator's git config). `CLIClient` inherits the global and system git
>     config (`adapters/git/cli.go:626-660` sets no isolation).
> - **I5.**
>   - A provenance record counts only if the authorising act *changed* the config:
>     - an override push whose old→new range changed `.spine/branch-protection.yaml`;
>     - an approved merge whose `BaseSHA`→`M` changed it.
>   - Otherwise an unrelated operator push, or an unrelated approved merge, would vouch for a
>     relaxation that was already sitting unauthorised on `main` (laundering).
>   - A refusal keeps the previous rules but is **not a sync error**, matching TASK-011 D2.

### 1. ADR (first deliverable)

Create `architecture/adr/ADR-016-governance-path-guard.md` from `templates/adr-template.md`.
016 is the next free number at `61994fe`; take whatever number is free at merge time. The ADR
has status `Proposed` and becomes `Accepted` when the owner signs off on owner question 1. It
records:

1. **What it supersedes.** It supersedes the ADR-009 decision not to add a merge-time path guard
   in v1 (§1 bullet 2 at `:58`, `:61`, §5 at `:138-140`, `:156-158`, and Consequences
   negative bullet 3 at `:194`). It does not add a branch-protection rule kind. The ruleset in
   `.spine/branch-protection.yaml` stays flat (ADR-009 §2/§6). The guard is a
   merge-authorization rule configured by the deployment, not a per-branch rule.
2. **Guarded paths.** By default `.spine/`, `.spine.yaml` and `workflows/` (owner question 2).
   The set is configurable per deployment.
3. **Authorization.** This is the owner question 1 choice. The default is an operator+ approval
   of the bound SHA, recorded on the run under reserved `commit_meta` keys and audited by a
   governance event.
4. **Hold semantics.**
   - The run stays in `committing` until an operator approves (the merge proceeds) or rejects
     (the run takes the workflow's `merge_failed` route, or fails on workflows without a publish
     step).
   - Cancel is not allowed from `committing` (`core/workflow/run_machine.go:165-199`), so
     reject is the way out.
5. **ADR-008 interaction.** A workflow-lifecycle planning run may merge its own workflow file
   (`run.TaskPath`) without operator approval when the deployment enables the exemption (owner
   question 3). This does not apply when the file is the governing lifecycle workflow itself.
   Every other governance path in that run still needs approval. ADR-008 §2 gets an "Amended by
   ADR-016" note.
6. **Projection protection-removal guard.**
   - Provenance comes from database records only:
     - an honoured `branch_protection.override` event;
     - an approval that a governed merge consumed.
   - Commit trailers are never used, because any run-branch author can forge them.
   - When no record matches, the previous rules stay, `sync_state` reports `error` and a
     `branch_protection.removal_refused` event is emitted.
7. **Accepted gaps.**
   - When the installed rules no longer carry `no-direct-write` on the authoritative branch, the
     projection guard is inactive. This keeps ADR-009 §1 bullet 3 consistent: self-protection is
     already off.
   - A fresh database restored from git refuses a relaxed config until an operator re-affirms it
     with an override push.

Also add "Amended by ADR-016" notes in ADR-009 (§1, §5, §6, Consequences) and ADR-008 (§2), and
update `architecture/security-model.md` (branch-protection section around `:304-307`),
`architecture/event-schemas.md`, `architecture/engine-state-machine.md` (the held `committing`
note) and `docs/git-push-guide.md` (the re-affirm-by-override recovery).

> **Design-check note (2026-10-06): ADR content follows the amended sections.**
> - **Item 6.** It records:
>   - provenance means an authorising act that *changed* the config;
>   - the newness anchor;
>   - a refusal keeps the rules but is not a sync error (owner questions 7 and 8).
> - **The re-affirm recovery.** In `docs/git-push-guide.md` it becomes: an override push that
>   edits `.spine/branch-protection.yaml`.
> - **Guarded paths (item 2).** Item 2 adds `WorkflowDirs(artifacts_dir)` and the own-run
>   evidence carve-out.
> - **The two-part evaluation.** The ADR also records I2(a)/(b): the union over merge bases, and
>   the post-merge assertion.
> - **Execution evidence.** `architecture/execution-evidence.md` §6.1 gets a note about the
>   carve-out.

### 2. `core/branchprotect`: governance facts and one pure decision

**New file `core/branchprotect/governance.go`:**

```go
// DefaultGovernancePaths is the guarded set used when the engine option leaves Paths empty.
var DefaultGovernancePaths = []string{".spine/", ".spine.yaml", "workflows/"}

// GovernancePaths is a validated, case-folded set of repo-relative prefixes.
type GovernancePaths struct{ entries []string }

// NewGovernancePaths validates entries: trims spaces, strips one leading "/",
// maps "X/**" to "X/", lower-cases, and rejects "", any ".." segment, glob
// metacharacters (* ? [ \) and control bytes.
func NewGovernancePaths(entries []string) (GovernancePaths, error)

// Match reports whether a git path is governed. It is fail-closed:
//   - a path beginning with `"` is treated as governed. It is a C-quoted path that a
//     newline-parsed `git diff --name-status` did not decode (adapters/git/cli.go:433;
//     until TASK-003 lands);
//   - any byte < 0x20 or 0x7f also counts as governed;
//   - otherwise q := lower(strings.TrimLeft(p, "/")). For an entry "X/", the path matches
//     when q == "X" (a file or symlink named like the directory) or q has prefix "X/".
//     Any other entry matches only when q equals it exactly.
func (g GovernancePaths) Match(p string) bool

type GovernanceApproval struct {
	SHA        string       // the bound SHA the approver approved
	Actor      domain.Actor // ActorID + Role at approval time
	ApprovedAt time.Time
}

// GovernanceCheck carries the facts for a governed merge. nil keeps v1 behaviour.
type GovernanceCheck struct {
	Paths        GovernancePaths
	BoundSHA     string   // the exact commit mergePrimary will merge (TASK-005)
	ChangedPaths []string // Path and OldPath of every FileDiff in merge-base(main, BoundSHA)..BoundSHA,
	                      // plus every path the engine itself writes onto the merge source (§3.2)
	ExemptPaths  []string // exact paths excused by the ADR-008 lifecycle rule (engine-computed)
	Approval     *GovernanceApproval
}

// Touched returns the sorted, de-duplicated governed paths that are not exempt.
func (c *GovernanceCheck) Touched() []string

// EvaluateGovernance is the single decision. The real policy and the engine both call it.
func EvaluateGovernance(c *GovernanceCheck) (Decision, []Reason)
```

**The decision, in order:**

1. `c == nil` → Allow with `governed_merge`, as today.
2. `BoundSHA == ""` → Deny with `governance_approval_required` ("no bound merge commit").
3. `Touched()` is empty → Allow with `governed_merge`.
4. Allow with the new `governance_approved` reason when all of these hold:
   - `Approval != nil`;
   - `Approval.Actor.ActorID != ""`;
   - `Approval.Actor.Role.HasAtLeast(domain.RoleOperator)`;
   - `strings.EqualFold(Approval.SHA, BoundSHA)`.
5. Otherwise Deny with the new `governance_approval_required` reason. The message names the
   count, the first 5 paths and the bound SHA. If an approval exists for another SHA, the
   message says so ("approval recorded for X, merge is bound to Y").

> **Design-check note (2026-10-06): `Match` and decision step 2.**
> - **Every entry also covers what lies beneath it.** For any entry `E`, whether or not it ends
>   in `/`, `Match` is true when `q == E'` or `q` has prefix `E' + "/"`, where `E'` is `E`
>   without a trailing `/`.
>   - This closes the type-confusion class: `.spine.yaml/x`, or `workflows` replaced by a file.
>   - It costs nothing for the defaults.
> - **Folding applies to matching only.** Case folding is used for *matching* only. Exemptions
>   (§3.3) and the evidence carve-out (§3.2) compare byte-exactly.
> - **Decision step 2 must never create an undecidable hold.** `BoundSHA == ""` now returns
>   Deny with a third reason, `governance_unevaluable`. The engine maps it to
>   `ErrUnavailable`: no hold record, no event.
>   - It can only arise from a programming error, because §3.4 runs after TASK-005's
>     bound-SHA validation.
>   - A deny that carries `governance_approval_required` is the only kind that creates a hold.
> - **C-quoted paths.** Treating them as governed is safe. But until TASK-003 lands, *every* run
>   that adds a non-ASCII file name anywhere (for example `docs/café.md`) is held. Land TASK-003
>   first, or decode git's C-quoting here (`\a \b \t \n \v \f \r \" \\` and `\ooo`) and fall back
>   to "governed" only when decoding fails.

**`core/branchprotect/types.go`:** two additive changes.

- `Request` gains `Governance *GovernanceCheck`. It is consulted only for `OpGovernedMerge`.
- Two `ReasonCode` constants are added after `:70` and `:79`: `ReasonGovernanceApproved`
  (`"governance_approved"`, an allow) and `ReasonGovernanceApprovalRequired`
  (`"governance_approval_required"`, a deny).

**`core/branchprotect/policy.go:67-72`:** when `req.Kind == OpGovernedMerge` and
`req.Governance != nil`, return `EvaluateGovernance(req.Governance)`. The nil branch and every
`OpDelete`/`OpDirectWrite` path stay byte-for-byte the same. Governance evaluation still loads no
rules, so the note at `merge.go:94-95` stays true.

### 3. Engine (`core/engine`)

#### 3.1 Option

**New file `core/engine/governance_guard.go`:**

```go
type GovernanceGuard struct {
	Enabled                         bool
	Paths                           []string // empty → branchprotect.DefaultGovernancePaths
	ExemptWorkflowLifecycleTaskPath bool     // owner question 3; zero value = no exemption
	MaxReportedPaths                int      // caps path lists in events/status; 0 → 100
}

// WithGovernanceGuard validates Paths with branchprotect.NewGovernancePaths. On error it
// leaves the guard unchanged and returns the error.
func (o *Orchestrator) WithGovernanceGuard(g GovernanceGuard) error

var ErrGovernanceApprovalRequired = errors.New("engine: governed merge requires governance approval")
```

The setter follows the existing `With*` methods (`orchestrator.go:100-198`). `Orchestrator`
gains one unexported field next to `policy` (`:32`).

> **Design-check note (2026-10-06): option and capability additions.**
>
> ```go
> type GovernanceGuard struct {
> 	// …fields above…
> 	// WorkflowDirs are added to the guarded set. Callers pass
> 	// projection.WorkflowDirs(artifactsDir) (TASK-011), e.g. ["workflows/", "spine/workflows/"].
> 	// core/engine cannot import adapters/projection, so the wiring computes this.
> 	WorkflowDirs []string
> 	// ExemptOwnRunEvidence excuses .spine/runs/<run.RunID>/evidence/ (byte-exact run ID).
> 	// It follows architecture/execution-evidence.md §6.1. Default true; see owner question 9.
> 	ExemptOwnRunEvidence bool
> }
>
> // MergeBaseLister is an optional capability, type-asserted like TASK-005's CommitBinder.
> // CLIClient implements it with `git merge-base --all <a> <b>` (both full object IDs or
> // validated refs, after TASK-002's terminator).
> type MergeBaseLister interface {
> 	MergeBases(ctx context.Context, a, b string) ([]string, error)
> }
> ```
>
> - `WithGovernanceGuard(Enabled: true)` returns an error, and leaves the guard off, when any of
>   these holds:
>   - `!o.MergeBindingEnabled()`;
>   - `o.git` is not a `MergeBaseLister`;
>   - path validation fails.
> - Call order in every wiring: `WithBranchProtectPolicy`, then `EnableMergeBinding`, then
>   `WithGovernanceGuard`.
> - `GitClient`, `GitOperator` and `RunStore` do not change, so SMP compiles unchanged. SMP's
>   `RealGitOperator` implements `MergeBases` in SMP TASK-010 (§9 item 12).

#### 3.2 Changed-path set: `buildGovernanceCheck(ctx, run, boundSHA)`

1. **The diff.**
   - `base, err := o.git.MergeBase(ctx, <main>, boundSHA)`, where `<main>` is the same
     authoritative ref form TASK-002/TASK-005 use.
   - Then `diffs, err := o.git.Diff(ctx, base, boundSHA)`.
   - Any error, an empty `base`, or an empty `boundSHA` returns an error (I3).
2. **ChangedPaths.**
   - For every entry, append `d.Path` and, when it is set, `d.OldPath`. Renames out of a
     governed tree count, and so do deletions (`FileDiff`, `adapters/git/client.go:86-90`).
   - Append `run.TaskPath` when `run.CommitMeta["status"] != ""`. The status cascade
     (`merge.go:100-107`, `:599-717`) writes only `run.TaskPath` and `.md` files that the
     diff already lists.
   - If TASK-017 changes the cascade, this set must include every path the engine writes onto
     the merge source after the guard runs.
3. **Approval.** Parse it from the reserved `commit_meta` keys (§3.7). A missing or malformed
   value means there is no approval.
4. **ExemptPaths.** Computed per §3.3.

The preflight order stays as it is. The guard runs before `applyCommitStatus` (`merge.go:83-107`)
and before `mergeCodeRepos` (`:42`). A held run therefore leaves no status commit on the branch
and merges no code repository.

> **Design-check note (2026-10-06): `buildGovernanceCheck` replaced by
> `buildGovernanceCheck(ctx, run, src)`.**
>
> `src` is TASK-005's `mergeSource{bound, sha}`. `bound` is the validated `spine.bound_sha`; `sha`
> is the bound commit, or the cascade commit TASK-005 §7 built on top of it with
> `CommitFilesOnto`.
>
> 1. **Merge bases.** `bases := MergeBases(<main>, src.sha)`.
>    - An error or an empty list returns an error (I3).
> 2. **ChangedPaths.** The union over every `B` in `bases` of `Path` and `OldPath` from
>    `Diff(B, src.sha)`.
>    - The cascade footprint is now *inside* the diff, because `src.sha` already contains the
>      cascade commit. The "append `run.TaskPath`" heuristic is dropped, and TASK-017 needs no
>      follow-up here.
>    - Proof sketch for "union over all bases": if `src.sha` agrees with every merge base on a path
>      `p`, then git's virtual base agrees too, since recursively merging identical sides is a
>      no-op. The three-way merge then keeps `main(p)`.
> 3. **BoundSHA.** `src.bound`. The approval binds to it, never to `src.sha`: the cascade commit
>    is rebuilt with a new timestamp on every attempt.
> 4. **Evidence carve-out.**
>    - With `ExemptOwnRunEvidence`, every path with the byte-exact prefix
>      `.spine/runs/<run.RunID>/evidence/` is added to `ExemptPaths`.
>    - Today no producer writes there, in Spine or SMP. It is the documented location
>      (`architecture/execution-evidence.md` §6.1; `core/evidence/loader.go:63-65`).
>    - Without the carve-out, the first evidence producer would turn every run into a governance
>      hold.
>    - Another run's evidence directory stays governed, because writing it would forge that run's
>      evidence.
>
> **Ordering.** The gate runs after TASK-005's bound-SHA validation and before `mergeCodeRepos`.
> Under binding the cascade never touches the run branch (`CommitFilesOnto` moves no ref). So "a
> held run leaves no status commit on the branch" still holds; a held evaluation leaves at most an
> unreferenced cascade object, which `gc` collects. An implementer may split `resolveMergeSource`
> so the cascade commit is built only after the gate allows; both orders are acceptable.

#### 3.3 ADR-008 lifecycle exemption

`ExemptPaths = [run.TaskPath]` only when all of these hold:

- `ExemptWorkflowLifecycleTaskPath` is set;
- `run.Mode == domain.RunModePlanning`;
- `artifact.IsWorkflowPath(run.TaskPath)` (`core/artifact/discovery.go:256`);
- `run.TaskPath != run.WorkflowPath`, so the governor cannot approve its own edit;
- the pinned governing workflow (`o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath,
  run.WorkflowVersion)`) has `AppliesTo` containing `"Workflow"`.

If the workflow load fails, there is no exemption (fail toward approval).

`StartWorkflowPlanningRun` sets exactly this shape (`core/engine/run.go:291-375`: path at `:302`,
`Mode` at `:349`). An artifact planning run whose `.md` declares `type: Workflow` resolves the
same governing workflow (`run.go:223`) but has a `.md` `TaskPath`. It therefore never gets the
exemption.

> **Design-check note (2026-10-06): the exemption predicate is tightened.**
>
> **Caller-supplied paths.** `StartPlanningRun` takes a caller-supplied `artifactPath` with no
> `.md` check (`run.go:203-251`). A front-matter-wrapped `workflows/x.yaml` can therefore also
> produce this run shape.
>
> **Conditions added.** All must hold. Any failure means no exemption, and the run falls back to
> operator approval.
>
> 1. **Exact path shape.** `run.TaskPath == "workflows/" + id + ".yaml"`, where `id` is the `id`
>    parsed with `workflow.Parse` from `ReadFile(src.bound, run.TaskPath)`.
>    - The comparison is byte-exact, never case-folded.
>    - `ExemptPaths` is compared byte-exactly in `Touched()`.
> 2. **The edit never governs workflows.** Neither the bound content nor the merge-base content
>    (if present) of `run.TaskPath` declares `applies_to` containing `Workflow`.
>    - Otherwise an exempt edit could add a second workflow-governing binding. `ResolveBinding`
>      then returns `ErrConflict` for every lifecycle run (`core/workflow/binding.go:65-72`), a
>      denial of service.
>    - Or, in the `artifacts_dir` layout, it could shadow `spine/workflows/workflow-lifecycle.yaml`
>      through a root `workflows/workflow-lifecycle.yaml`. That path passes the
>      `TaskPath != WorkflowPath` check.
> 3. **Local separation of duties.**
>    - The actor of the last completed main-path `type: review` step execution is non-empty.
>    - It differs from the actor of every completed non-review step execution in the run
>      (`ListStepExecutionsByRun`).
>    - This holds whatever TASK-007's opt-in flag says. It closes the standalone single-reviewer
>      path: self-grant `workflow_review` (F6), draft, self-review, then a review-less
>      `task-default` merged with no operator.
>
> These checks cost one `ReadFile`, one `Parse` and one list call. They run only for planning runs
> that touch their own workflow path.

#### 3.4 Preflight: `checkBranchProtectMerge` (`merge.go:526-567`)

- Keep the nil-policy check (`:527-530`).
- When the guard is enabled:
  - Build the check. On error, log and return
    `domain.NewErrorWithCause(domain.ErrUnavailable, "governance guard: cannot compute the merge's changed paths; merge refused", err)`.
    The run stays in `committing` and nothing is merged.
  - Set `req.Governance`.
- Call `o.policy.Evaluate` as today.
- **Engine-side enforcement.** Also call `branchprotect.EvaluateGovernance(gc)` and treat a Deny
  from either as a Deny. A custom or test `Policy` can therefore not turn the guard off. This
  matters because the scenario harness installs `branchprotect.NewPermissive()`
  (`service/scenariotest/harness/runtime.go:169`).
- **On Deny with `governance_approval_required`:**
  - Call `recordGovernanceHold` (§3.5).
  - Return `domain.NewErrorWithCause(domain.ErrPrecondition, msg, ErrGovernanceApprovalRequired)`.
- Any other Deny keeps today's `ErrForbidden` path (`:556-564`).
- **On Allow with `governance_approved`:** mark the merge pass as consuming an approval. This uses
  the pass state TASK-005 adds between `preflightCheck` and `mergePrimary` for the bound SHA.

These errors already leave the reviewer's submit successful and the run in `committing`:

- In the publish-step path, `advanceToNextStep` logs activation errors and does not return them
  (`step.go:446-448`). `activateInternalStep` returns the handler error with the step still
  `in_progress` (`handlers.go:135-141`).
- In the terminal-commit path, `CompleteRun` logs the immediate merge failure (`run.go:437-446`).

> **Design-check note (2026-10-06): placement moves out of `preflightCheck`.**
>
> ```go
> run, done, err := o.preflightCheck(ctx, runID)    // unchanged: nil-policy check, Governance == nil
> src, done, err := o.resolveMergeSource(ctx, run)  // TASK-005: a missing or invalid bound SHA
>                                                   // takes bindingFailure, never a hold
> gc, err := o.governanceGate(ctx, run, src)        // this task; nil gc when the guard is off
> if err != nil { return err }                      // hold, or a refused or unevaluable gate
> if retried, err := o.mergeCodeRepos(ctx, run); … // unchanged
> if done, err := o.mergePrimary(ctx, run, src, gc); …
> ```
>
> - `governanceGate` does what the bullets above describe:
>   - build the check (§3.2 note);
>   - `o.policy.Evaluate` with `Request{Kind: OpGovernedMerge, Governance: gc, …}`;
>   - `EvaluateGovernance(gc)`;
>   - hold or allow.
>   - D4 (one decision point, enforced in the engine as well) is unchanged.
> - **Other `OpGovernedMerge` callers are unaffected.** `checkBranchProtectMerge` in preflight
>   keeps today's governance-free call. So do the divergence convergence call
>   (`core/divergence/convergence.go:184`, `Governance == nil` → Allow, as today) and SMP's githttp
>   `Request` literal.
> - **Scheduler noise.** In standalone the commit-retry sweep re-enters `MergeRunBranch` on every
>   tick (`service/cmd/spine/serve_resolver.go:166-168`; `adapters/scheduler/recovery.go:288`),
>   and a hold error is logged at Error each tick. The `CommitRetryFn` wrappers in
>   `serve_resolver.go`/`serve_builders.go` return nil for
>   `errors.Is(err, ErrGovernanceApprovalRequired)` and log at Debug. The hold stays visible
>   through the step's `ErrorDetail` and the event.
>
> **§3.4a Post-merge assertion (new; defence in depth for I2(b)).** In `mergePrimary`, after
> `MergeBound` and TASK-005's `verifyBoundMerge`, and before `PushCommitCAS`, when `gc != nil`:
>
> 1. `actual := governed(Diff(mergeResult.BaseSHA, M))`. Both `Path` and `OldPath` count, and the
>    carve-out and exemptions apply.
> 2. A violation exists when `actual ⊄ gc.Touched()`. With no approval, `Touched()` must
>    already be empty, so any governed path in `actual` is a violation.
>    - "Already up to date" (`M == BaseSHA`) gives an empty diff and passes.
> 3. **On a violation or a `Diff` error,** take TASK-005's refusal path:
>    - restore the local target to `BaseSHA` (Dependencies note);
>    - record the primary outcome as failed, with `*git.GitError{Kind: ErrKindPermanent,
>      Op: "governance_guard", Message: "merge changed governed paths not covered by the
>      evaluation: …"}`;
>    - call `handlePermanentMergeFailure`;
>    - push nothing;
>    - emit `governance.merge_held` with `post_merge: true` and the extra paths.
>    - Owner question 6 offers a hold instead.
> 4. With `MergeBases` in place this should never fire under default git config. It exists for
>    merge semantics the pre-merge evaluation cannot see.

#### 3.5 Hold record: `recordGovernanceHold(ctx, run, gc)`

1. **Target step.**
   - Use `findActiveMergeStep` (`handlers.go:247`) for workflows with a publish step.
   - Otherwise use the last completed step, as `recordGitConflictOnStep` does (`merge.go:471-506`).
2. **Error detail.**
   - Set `ErrorDetail{Classification: domain.FailureGovernanceApprovalRequired, Message,
     StepID, RuleID: "governance-path-guard"}`.
   - Write it only when it differs from the stored value, so a re-evaluation on every scheduler
     tick causes no writes.
3. **Event.**
   - Emit `governance.merge_held` with the deterministic event ID
     `evt-<trace12>-governance-held-<boundSHA12>`. The event-log and delivery-queue inserts
     already deduplicate on event ID: `ON CONFLICT DO NOTHING` at
     `adapters/store/postgres_delivery.go:223-230` and `:11-21`, and in SMP's emitter.
   - Payload:
     - `run_id`, `task_path`, `bound_sha`;
     - `paths` (at most `MaxReportedPaths`), `path_count`;
     - `exempt_paths`;
     - `stale_approval_sha`, when an approval for another SHA exists.

> **Design-check note (2026-10-06): episode discriminator.**
>
> **The problem.** After a reject, the author may resubmit the same SHA (§3.6 note). The second
> hold then has the same event ID, `ON CONFLICT DO NOTHING` swallows it, and admins are not
> notified.
>
> **The fix.** The ID becomes `evt-<trace12>-governance-held-<boundSHA12>-<episode>`, where
> `<episode>` is the target step's `ExecutionID`:
>
> - the publish step execution, which is new on every pass through publish;
> - otherwise the last completed step execution.
>
> Re-evaluation on every tick stays idempotent, because the ID is stable within one episode.

#### 3.6 Decision API: approve or reject a held merge

```go
type GovernanceDecision string // "approve" | "reject"

type GovernanceDecisionRequest struct {
	RunID     string
	CommitSHA string             // must equal the bound SHA
	Decision  GovernanceDecision
	Actor     domain.Actor       // authenticated by the caller; checked again here
	Reason    string             // ≤ 2000 bytes; required for reject
}

type GovernanceDecisionResult struct {
	RunID, CommitSHA string
	Decision         GovernanceDecision
	Paths            []string
	PathCount        int
	MergeAttempted   bool
	MergeError       string // approve only: the merge failed after the approval was recorded
	RunStatus        domain.RunStatus
}

func (o *Orchestrator) DecideGovernanceMerge(ctx context.Context, req GovernanceDecisionRequest) (*GovernanceDecisionResult, error)
```

**Checks, in order.** Each failure returns the error shown and makes no write and no event.

1. The guard is enabled. Otherwise `ErrUnavailable`.
2. `Actor.ActorID != ""` and `Actor.Role.HasAtLeast(RoleOperator)`. Otherwise `ErrForbidden`.
3. `Decision` is valid, `Reason` is within the length limit, and a reject has a reason.
   Otherwise `ErrInvalidParams`.
4. `CommitSHA` matches `^([0-9a-f]{40}|[0-9a-f]{64})$` after lower-casing. Otherwise
   `ErrInvalidParams`.
5. The run exists and `run.Status == committing`. Otherwise `ErrNotFound` or `ErrConflict`.
6. `buildGovernanceCheck` succeeds. Otherwise `ErrUnavailable`.
7. `Touched()` is not empty. Otherwise `ErrPrecondition` ("run does not require governance
   approval").
8. `CommitSHA` equals the bound SHA. Otherwise `ErrConflict` ("merge is bound to <sha>").

**Approve.**

1. Write the reserved keys (§3.7) through `mergeCommitMeta`:
   - `sha`, `actor_id`, `actor_role`;
   - `approved_at` (RFC 3339, UTC).
2. Emit `governance.merge_decided` with event ID
   `evt-<trace12>-governance-approve-<sha12>-<unixmilli>` and this payload:
   - `run_id`, `bound_sha`, `decision`, `actor_id`, `actor_role`, `reason`;
   - `paths`, `path_count`.
3. Clear a governance hold `ErrorDetail` on the target step.
4. Call `o.MergeRunBranch(ctx, run.RunID)`. A merge error goes into `MergeError` and the call
   still succeeds, because the approval stands.
5. Approving the same SHA again is idempotent and acts as a manual merge retry. SMP has no
   commit-retry sweep; §9 point 10 explains why that matters.

**Reject.**

1. Emit `governance.merge_decided` with `decision: rejected`.
2. Call `o.handlePermanentMergeFailure(ctx, run, fmt.Errorf("governance change rejected by %s: %s", …))`
   (`merge.go:309-321`). Publish-step workflows take `merge_failed` (back to `execute` in
   task-default). Workflows without a publish step fail, and the branch is preserved
   (`:443-467`).

There is no separation-of-duties rule on the approver. An operator can already land any change
directly with an override push (ADR-009 §4/§5), so a self-approval grants nothing new. See
Decisions D11.

> **Design-check note (2026-10-06): reject must revoke, and decisions must not race.**
>
> 1. **Reject clears the approval.**
>    - **Correction.** TASK-005's `mergeCommitMeta` *preserves* reserved keys across later outcome
>      writes when binding is on (TASK-005 §1, "Reserved part"). So §3.7's claim that "a new
>      committing episode starts with no approval" is false. Binding is a prerequisite of this
>      guard (I1), so that case always applies.
>    - **The attack.** Approve X, the merge fails transiently and the run stays `committing`, then
>      the operator rejects. The run loops back. The author resubmits X unchanged, review accepts,
>      and the old approval for X merges it. The operator's reject is silently overridden.
>    - **The fix.** Reject writes `spine.governance_approval.sha = ""` (with `actor_id`,
>      `actor_role` and `approved_at` also emptied) through `MergeCommitMeta` *before*
>      `handlePermanentMergeFailure`. JSONB `||` cannot delete a key, and an empty SHA parses as
>      "no approval". Approvals stay SHA-bound otherwise: a loop-back that produces a new SHA is
>      already stale.
> 2. **No race between approve and reject.**
>    - Reject's route-back already uses compare-and-swap (`advancePublishStepIfAny`,
>      `handlers.go:215-230`).
>    - The no-publish-step path (`failRunOnMergeError`, `merge.go:457`) does not. Until TASK-008
>      converts it, `DecideGovernanceMerge(reject)` on that path uses
>      `TransitionRunStatus(committing → failed)`. A miss (the run already completed) returns
>      `ErrConflict` with no event.
>    - Approve re-reads the run after `MergeCommitMeta`. If the run is no longer `committing`, it
>      returns `ErrConflict` without merging.

#### 3.7 Reserved `commit_meta` keys and how they are written

**Constants in `core/domain/run.go`, so the store and the engine share them:**

- `CommitMetaReservedPrefix = "spine."`;
- `CommitMetaGovernanceApprovalSHA = "spine.governance_approval.sha"`;
- `…ActorID = "spine.governance_approval.actor_id"`;
- `…ActorRole = "spine.governance_approval.actor_role"`;
- `…ApprovedAt = "spine.governance_approval.approved_at"`;
- `…MergeSHA = "spine.governance_approval.merge_sha"`;
- `StripReservedCommitMeta(map[string]string) (kept map[string]string, dropped []string)`.

**Forgery defence.**

- **Engine.** In `routeStepOutcome` (`core/engine/step.go:396-400`), pass
  `StripReservedCommitMeta(outcome.Commit)` to `SetCommitMeta` and log a warning that lists the
  dropped keys.
  - `hasCommit` keeps using `len(outcome.Commit) > 0`, so routing is unchanged.
  - Because `SetCommitMeta` replaces the whole map (`adapters/store/postgres_runs.go:153-160`),
    a new committing episode starts with no approval.
- **Validator.** `workflow.ValidateSchema` rejects outcome `commit:` keys with the reserved prefix
  in its outcome loop (`core/workflow/parser.go:190-211`), compared case-insensitively.
  - No shipped workflow uses such a key (checked: `workflows/*.yaml` in Spine and SMP).
  - If TASK-005 stores its binding in `commit_meta`, it uses this prefix and helper. Whichever
    of the two tasks lands first adds the helper.

**Atomic writes.**

- Add `(*store.PostgresStore).MergeCommitMeta(ctx, runID string, patch map[string]string)`:
  `UPDATE runtime.runs SET commit_meta = COALESCE(commit_meta,'{}'::jsonb) || $1::jsonb WHERE run_id = $2`,
  plus `mustAffect`.
- The engine uses it through an optional interface:

  ```go
  type commitMetaMerger interface {
  	MergeCommitMeta(ctx context.Context, runID string, patch map[string]string) error
  }
  ```

  The engine type-asserts `o.store`. If the store does not implement the interface, it falls
  back to read-modify-write (`GetRun` then `SetCommitMeta`). That fallback is safe here because
  only `committing` runs are written and `routeStepOutcome` does not run in `committing`.
- `RunStore` (`core/engine/interfaces.go:21-55`) does not change, so SMP's `RunStore` keeps
  compiling.

> **Design-check note (2026-10-06): overlap with TASK-005 §1 and §10.** TASK-005 (a hard
> dependency, so it lands first) already does three things:
> - strips every `spine.`-prefixed key, case-insensitively, from every `outcome.Commit` inside
>   `mergeCommitMeta`;
> - adds the `ValidateSchema` rule;
> - keeps reserved keys across writes.
>
> This task therefore adds **no** second strip in `routeStepOutcome` and no second validator rule.
> It adds only the `spine.governance_approval.*` key constants. They live in `core/domain`, so
> that `adapters/store` (which cannot import `core/engine`) shares them. Two more keys are added
> (§3.8 note): `base_sha`, and the reject tombstone semantics above. AC 9 still holds, and it now
> exercises TASK-005's strip.

#### 3.8 Consumption marker (provenance for the projection)

- **When it is written.** In `mergePrimary`'s success path (`merge.go:248-257`), when the pass
  consumed an approval, call `mergeCommitMeta(run, {MergeSHA: mergeResult.SHA})` before
  `recordPrimaryMergeOutcome`. A write error returns `(false, err)`, the same "fatal, stays in
  committing" handling the outcome row gets (`:250-256`).
- **Who reads it.** The projection's provenance query (§5) only counts an approval whose
  `merge_sha` equals the primary outcome's `merge_commit_sha`.
- **Why.** A stale or unconsumed approval can never vouch for a commit.

> **Design-check note (2026-10-06): write the marker before the push, and record the base.**
>
> **The race.** As first designed, the marker and the outcome row are written *after* the push.
> A projection tick in that window finds no record. With refusal no longer a sync error (§5
> note), that refusal would not be retried.
>
> **The fix.** Write `{MergeSHA: M, BaseSHA: mergeResult.BaseSHA}` (the new key
> `spine.governance_approval.base_sha`):
>
> - after `MergeBound`, `verifyBoundMerge` and the §3.4a assertion;
> - before `PushCommitCAS`.
>
> **Error handling.** A write error takes the refusal path: restore the local target and stay
> `committing`.
>
> **Harmless failures.** A marker for a merge whose push then fails names a commit that never
> reaches `main`. The projection's reachability check ignores it, and a retry overwrites it.

#### 3.9 Read API

```go
type GovernanceMergeStatus struct {
	RunID       string
	RunStatus   domain.RunStatus
	Evaluated   bool     // false when no bound SHA exists yet
	BoundSHA    string
	Required    bool     // Touched() non-empty
	Held        bool     // committing && Required && no matching approval
	Paths       []string // capped
	PathCount   int
	Truncated   bool
	ExemptPaths []string
	Approval    *struct{ SHA, ActorID, ActorRole, ApprovedAt, MergeSHA string }
}

func (o *Orchestrator) GovernanceMergeStatus(ctx context.Context, runID string) (*GovernanceMergeStatus, error)
```

This method is read-only. SMP TASK-010 and the standalone `GET` route (§7) use it.

### 4. Store (`adapters/store`): no migration

- `MergeCommitMeta` (§3.7) in `postgres_runs.go`.
- **New file `postgres_governance.go`:**

```go
type GovernanceAuthorization struct {
	Source     string // "override_event" | "governance_approval"
	CommitSHA  string // override: the honoured pushed/created commit; approval: the merge commit
	ActorID    string
	RunID      string
	EventID    string
	RecordedAt time.Time
}

// ListGovernanceAuthorizations returns at most `limit` records per source, newest first.
func (s *PostgresStore) ListGovernanceAuthorizations(ctx context.Context, branch string, limit int) ([]GovernanceAuthorization, error)
```

**Source 1: override events.**

```sql
SELECT event_id, payload->>'actor_id', payload->'payload'->>'commit_sha', created_at
FROM runtime.event_log
WHERE event_type = 'branch_protection.override'
  AND payload->'payload'->>'operation' = 'direct_write'
  AND payload->'payload'->>'branch' IN ($1, 'refs/heads/' || $1)
  AND payload->'payload'->>'commit_sha' IS NOT NULL
ORDER BY created_at DESC
LIMIT $2
```

- `event_log.payload` is the whole `domain.Event` in both writers: Spine's
  `adapters/delivery/subscriber.go:70-84` and SMP's `spinecore/eventemitter.go:106-110`.
- The git path stores `branch` as `refs/heads/main` (`service/internal/githttp/handler.go:493-532`;
  SMP `internal/customer/githttp/handler.go:658-692`).
- The API path stores the short name (`core/artifact/service.go:640-668`).
- The `(event_type, created_at)` index covers the query (`migrations/017_event_log.sql`).

**Source 2: consumed approvals.**

```sql
SELECT r.run_id, r.commit_meta->>'spine.governance_approval.actor_id', o.merge_commit_sha, o.merged_at
FROM runtime.runs r
JOIN runtime.repository_merge_outcomes o
  ON o.run_id = r.run_id AND o.repository_id = 'spine'
WHERE o.status = 'merged'
  AND o.target_branch = $1
  AND o.merge_commit_sha IS NOT NULL
  AND r.commit_meta->>'spine.governance_approval.merge_sha' = o.merge_commit_sha
ORDER BY o.merged_at DESC
LIMIT $2
```

Neither method joins the `store.Store` interface. The projection takes a narrow interface (§5),
so no implementer of `store.Store` (SMP fakes included) has to change.

> **Design-check note (2026-10-06): record shape.**
> - **`GovernanceAuthorization` gains `BaseSHA string`.**
>   - For an override, it is the push's `old_sha`.
>   - For an approval, it is `spine.governance_approval.base_sha`.
>   - Empty, or an all-zero SHA, means "config absent before".
> - **Source 1** also selects `payload->'payload'->'pre_receive_ref'->>'old_sha'` and requires it
>   to be non-null.
>   - The git-path event has carried it since ADR-009 §4 (`service/internal/githttp/handler.go:493-532`;
>     SMP `internal/customer/githttp/handler.go:658-692`).
>   - API-path override events have no `old_sha`, so they are not counted. The artifact and
>     workflow API writes cannot produce a parseable `.spine/branch-protection.yaml`
>     (`artifact.Parse` requires front matter). If that ever changes, the API path must record
>     `old_sha` too.
> - **Source 2** drops the `repository_merge_outcomes` join. It selects runs whose
>   `commit_meta ? 'spine.governance_approval.merge_sha'` holds and whose `merge_sha` and
>   `base_sha` are non-empty, newest `approved_at` first.
>   - Landing is proved by the projection's reachability check (`merge_sha` is an ancestor of
>     `head`), not by the outcome row.
>   - The outcome row is written after the push, which the §3.8 note moved the marker ahead of.
>   - This is a sequential scan of `runtime.runs`, but it only runs when a removal is evaluated.

### 5. Projection protection-removal guard (`adapters/projection`)

**New file `adapters/projection/protection_guard.go`:**

```go
type GovernanceProvenanceReader interface {
	ListGovernanceAuthorizations(ctx context.Context, branch string, limit int) ([]store.GovernanceAuthorization, error)
}

type ProtectionRemovalGuard struct {
	Enabled             bool
	AuthoritativeBranch string                     // "" → "main" (or TASK-011's configured ref)
	Provenance          GovernanceProvenanceReader // nil while Enabled → every removal is refused
	Events              event.Emitter              // nil → s.events; both nil → log only
	Lookback            int                        // records per source; 0 → 200
}

var ErrProtectionRemovalUnauthorized = errors.New(
	"projection: branch-protection config removes protection from the authoritative branch without an operator record")

func (s *Service) WithProtectionRemovalGuard(g ProtectionRemovalGuard)
```

**Hook.** In `projectBranchProtection` (`service.go:569-620`), call
`s.guardProtectionRemoval(ctx, commitSHA, candidateRules, candidateContent)` before both
`UpsertBranchProtectionRules` calls:

- the bootstrap path at `:582` (candidate `BootstrapDefaults()`, content nil);
- the parsed path at `:619`.

On error, return it. The rules are untouched, and the callers already count the error into
`sync_state=error`:

- `IncrementalSync` at `:275-281` keeps `LastSyncedCommit` and forces `bpDirty` on the next tick
  (`:265`);
- `FullRebuild` at `:121-127`.

Both entry points pass through this one function, so the provisioning `FullRebuild` and the sync
loop are both covered.

**`guardProtectionRemoval`:**

1. **Guard off.** Return nil.
2. **Installed rules.**
   - `installed := bpproj.New(s.store).Rules(ctx)`, which reuses the decoding in
     `core/branchprotect/projection/rule_source.go:61-84`.
   - A read error returns that error (fail closed).
   - `installedSource` is `rows[0].SourceCommit` from `ListBranchProtectionRules`. It is the same
     on every row (`postgres_projections.go:309-329`).
3. **ADR-009 §1 bullet 3.**
   - `oldKinds` is the union of the `Protections` of `config.Config{Rules: installed}.MatchRules(branch)`.
   - If `oldKinds` lacks `no-direct-write`, return nil.
   - An empty table counts as explicitly unprotected, so it also returns nil here.
4. **Not a removal.**
   - `newKinds` is the same union over the candidate.
   - If `newKinds ⊇ oldKinds`, return nil. Tightening, reordering and unrelated branches never
     read provenance.
5. **Read the records.** `Provenance.ListGovernanceAuthorizations(ctx, branch, lookback)`. A nil
   reader or an error means refused.
6. **Look for a matching record.** A record matches when all of these hold:
   - its SHA is a full hex SHA;
   - it is reachable: `s.git.MergeBase(ctx, rec, commitSHA) == rec`;
   - it is new: `installedSource` is not a hex SHA, or `MergeBase(rec, installedSource) != rec`.
     This blocks replaying an old authorization after it was superseded;
   - its content matches: `ReadFile(rec, BranchProtectionConfigPath)` is byte-equal to the
     candidate content.

   A git error on a record means that record does not match. On the first match, log the
   provenance (source, record SHA, actor, run or event ID) and return nil.
7. **No match.**
   - Emit `branch_protection.removal_refused` with event ID `bp-removal-refused-<head12>` and this
     payload:
     - `branch`, `installed_kinds`, `candidate_kinds`;
     - `head_commit`, `installed_source_commit`;
     - `records_checked`.
   - Return `fmt.Errorf("%w: …", ErrProtectionRemovalUnauthorized)`.

**Why content matching is sound.**

- An honoured override authorises exactly the tree it pushed.
- A consumed approval authorises exactly the merge commit it produced.
- If the config bytes at `head` equal the config bytes at such a commit, and that commit is newer
  than what is installed, then the installed relaxation is the one an operator authorised.
- Any later unauthorised edit changes the bytes and gets no match.
- Re-pushing an old authorised blob fails the newness check.

`MergeBase` and `ReadFile` are already on `git.GitClient` (`adapters/git/client.go:17-22`). No git
interface grows.

> **Design-check note (2026-10-06): three holes in the matching rule, and the sync-error choice.**
>
> 1. **Laundering. A record must have *changed* the config.**
>    - **The hole.** A relaxation R lands on `main` without authority (for example through a
>      bypass of the merge guard) and is refused. Then *any* later operator override push
>      (a hotfix), or *any* approved governed merge (say a workflow edit), produces a commit whose
>      tree still carries R. As first designed, that record matched the content, was reachable and
>      was new, so R was installed. The operator authorised the hotfix, not R.
>    - **The fix.** Add a check to step 6: `ReadFile(rec.BaseSHA, BranchProtectionConfigPath)`
>      must differ from `ReadFile(rec.CommitSHA, …)`. An absent file at the base, or an all-zero
>      base, counts as different.
>    - **Consequence for recovery.** The documented re-affirm must *edit* the file (for example
>      update a comment line) in an override push. `docs/git-push-guide.md` says so. See owner
>      question 8.
> 2. **Replay while bootstrap rules are installed.**
>    - **The hole.** The newness check only compares against `installedSource` when it is a hex
>      SHA. After the file is deleted (rows stamped `"bootstrap"`), *any* old authorisation whose
>      bytes equal the candidate is accepted again.
>    - **The fix.** The newness anchor is `installedSource` when it is hex. Otherwise it is the
>      `LastSyncedCommit` read at the start of this sync (`IncrementalSync`'s `from`;
>      `FullRebuild`'s `prevCommit`, `service.go:67-71`), when that is hex. With neither, there is
>      no anchor; this is the fresh-database accepted gap. `guardProtectionRemoval` takes the
>      anchor as a parameter.
> 3. **A refusal is not a sync error** (TASK-011 D2).
>    - **What the function returns.** `projectBranchProtection` returns the typed sentinel
>      `ErrProtectionRemovalUnauthorized`.
>    - **What `FullRebuild` and `IncrementalSync` do with it.** They log at Warn, keep the
>      previous rows, emit the event (deduplicated per head), and do **not** increment the error
>      count. The sync then ends `idle@head`.
>    - **When it is re-evaluated.** On the next diff that touches the file, which is exactly the
>      operator's config-changing re-affirm push, and on every `FullRebuild`.
>    - **Why.** Counting it would freeze `LastSyncedCommit`, so the diff window grows every tick.
>      It would also stop SMP's governed document index for the whole workspace, which reconciles
>      only at `idle`.
>    - **What is lost.** `sync_state` no longer shows the refusal. The signals are the Warn log,
>      the `branch_protection.removal_refused` event, and the installed rows' `source_commit`
>      differing from the file at `head`. SMP TASK-010 surfaces the event to admins. See owner
>      question 7.

### 6. Domain: failure classification and events

- **`core/domain/step.go:41-49`:** add `FailureGovernanceApprovalRequired FailureClassification =
  "governance_approval_required"`. `IsRetryable` stays false through its default branch.
- **`core/domain/event.go`, operational block (`:31-78`):** add
  `EventGovernanceMergeHeld = "governance.merge_held"`,
  `EventGovernanceMergeDecided = "governance.merge_decided"` and
  `EventBranchProtectionRemovalRefused = "branch_protection.removal_refused"`.
- **Registration and docs:** register the three events in `allEventTypes()`
  (`adapters/delivery/subscriber.go:137-174`) and document them in
  `architecture/event-schemas.md`.

### 7. Standalone wiring (`service/`)

- **Orchestrators.**
  - **Where:** `serve_builders.go:265-272` and `serve_resolver.go:84-90`.
  - **Call:** `orch.WithGovernanceGuard(engine.GovernanceGuard{Enabled: governanceGuardEnabled(),
    ExemptWorkflowLifecycleTaskPath: true})`.
  - **Switch:** `SPINE_GOVERNANCE_GUARD`. `off`, `false`, `0` and `no` turn the guard off.
    Unset means on (owner question 4). Any other value means on, with a warning.
  - **On failure:** a validation error fails startup or the workspace build, the same way the
    branch-protection builders already do.
- **Projection.**
  - **Where:** `serve_wiring.go:287` and `service/internal/workspace/pool.go:955`.
  - **Call:** `WithProtectionRemovalGuard(projection.ProtectionRemovalGuard{Enabled: <same
    switch>, Provenance: <*store.PostgresStore>, Events: <the router>})`.
- **Gateway.**
  - **Routes** (`service/internal/gateway/routes.go:75-80`):
    - `POST /runs/{run_id}/governance-decision`, body
      `{"decision","commit_sha","reason"}`, permission `run.governance.decide`;
    - `GET /runs/{run_id}/governance`, permission `run.status`.
  - **Responses** for the decision route: 200, 400, 403, 404, 409 (not committing, or SHA
    mismatch), 412 (no governance change) and 503 (guard off or not wired).
  - **Permission:** `core/auth/permissions.go:28-34` gains `"run.governance.decide":
    domain.RoleOperator`.
  - **Handler:** a new file `handlers_run_governance.go` takes the actor from
    `domain.ActorFromContext`. It resolves the decider per workspace, the same way
    `runMergeResolverFrom` does (`server.go:482`).
  - **Spec:** `api/spec.yaml` documents both routes.
- **Scenario harness:** add a way to turn the guard on (`service/scenariotest/harness/runtime.go:116`,
  `:162-169`). It stays off by default, so existing scenarios do not change.

> **Design-check note (2026-10-06): wiring corrections.**
> - **Guard options.** Both orchestrator builders pass:
>   - `WorkflowDirs: projection.WorkflowDirs(<artifacts_dir>)` (TASK-011), taking `cfg.ArtifactsDir`
>     in `serve_builders.go` (see `:48`) and `spineCfg.ArtifactsDir` in the workspace path
>     (`service/internal/workspace/pool.go:919-934`);
>   - `ExemptOwnRunEvidence: true`.
>   - They call `WithGovernanceGuard` after TASK-005's `EnableMergeBinding`.
> - **Commit retry.** The commit-retry closures (`serve_builders.go:314`,
>   `serve_resolver.go:167`) swallow `ErrGovernanceApprovalRequired` (§3.4 note).
> - **Projection.** The projection option also needs the newness anchor (internal) and nothing
>   else.
> - **Harness.** The opt-in option also takes the harness's `WithArtifactsDir` value.

### 8. Data, format, migration and backward compatibility

- **Schema.** No migration. New data goes into:
  - reserved keys in `runtime.runs.commit_meta` (JSONB, `migrations/013`);
  - one new value of `error_detail.classification` (JSONB);
  - three event types in `runtime.event_log`.
- **API.** Every API change is additive:
  - `Request.Governance`, new reason codes and `GovernanceCheck` in `branchprotect`;
  - new `engine` methods and types;
  - `store.PostgresStore` methods, not added to the interface;
  - the projection option.
  - SMP builds `branchprotect.Request` with named fields
    (`internal/customer/githttp/handler.go:574-580`), so it compiles unchanged.
- **Behaviour with both guards off:**
  - unchanged except for two points;
  - reserved-prefix `commit` keys from workflows are dropped;
  - `ValidateSchema` rejects them in new workflows;
  - no shipped workflow uses that prefix.
- **Event-log retention.** `runtime.event_log` rows are pruned (`postgres_delivery.go:295`).
  - Override provenance is only needed between the push and the first successful projection.
  - Once the config is installed, later syncs compare against the installed rules.
  - A database rebuilt long after a relaxation keeps the stricter bootstrap rules and reports
    `sync_state=error`. The operator fixes this by re-affirming with an override push. The ADR
    and `docs/git-push-guide.md` record this.

### 9. SMP impact, and what SMP TASK-010 must change

SMP is unaffected until it opts in. TASK-010 (after the merge-binding bump in SMP TASK-009) must:

1. **Bump.** Bump `.spine-revision` to the Spine commit that carries this task.
2. **Enable the merge guard.**
   - **Where:** `internal/customer/spinecore/factory.go`, after `:209`.
   - **Call:** `orch.WithGovernanceGuard(engine.GovernanceGuard{Enabled: true, Paths: <owner
     question 2>})`.
   - **Exemption:** `ExemptWorkflowLifecycleTaskPath` stays false. SMP has no caller of
     `StartWorkflowPlanningRun`; its workflow writes are admin direct commits with
     `workflow.WithBypass` (`internal/customer/spineembedded/embedded_workflows.go:119`).
3. **Atomic approval writes.** Add `MergeCommitMeta` to `spinecore.RunStore`, delegating to
   `s.pgs` as `SetCommitMeta` does (`runstore.go:327-329`). `GetRun` already selects
   `commit_meta` (`:127-140`).
4. **SHA reads in the git operator.** Make sure `RealGitOperator.MergeBase` (`gitoperator.go:329-370`)
   and `Diff` (`:793-830`) accept a full bound SHA as the second argument. The bound SHA may no
   longer be a branch tip. Today `fastForwardLocalRefs` and `resolveRefForRead` fetch by name.
   SMP TASK-009 makes `Merge` SHA-capable; TASK-010 adds tests for these two reads.
5. **Approve/reject endpoint.**
   - It is an admin-only permission in `internal/customer/workspace/permissions.go:17-26`.
   - It calls `DecideGovernanceMerge` with `Actor{ActorID: <caller's Spine actor>, Role:
     domain.RoleAdmin}`. That is the same admin→`RoleAdmin` mapping as
     `githttp/gateway.go:632-655`.
   - It runs inside the git-session/write-gate wrapper used by the other merge-cascading calls
     (`spineembedded/proxy.go:1340-1371`, `spinecore/gitsession.go`).
   - Members get 403.
6. **Run detail.**
   - Expose `GovernanceMergeStatus`.
   - Show the held state and the approve/reject actions for admins in the existing
     ProtectedChanges panel (`spineembedded/artifacts.go:184-202`).
   - Optionally notify admins on `governance.merge_held`.
7. **Enable the projection guard.**
   - **Where:** `projectionwriter/worker.go:314` and `provision_steps_runtime.go:322`.
   - **Call:** `svc.WithProtectionRemovalGuard(spineprojection.ProtectionRemovalGuard{Enabled:
     true, Provenance: pgs, Events: <an event_log-writing emitter>})`.
   - **Provenance:** admin override pushes already write `branch_protection.override` to
     `runtime.event_log` through `spinecore.NewEventEmitter` (`githttp/gateway.go:387-398`), so
     this needs no SMP change.
8. **Imported or re-bound repositories.**
   - When provisioning finds a repo whose HEAD relaxes `main`, it now keeps the bootstrap rules and
     reports `sync_state=error`.
   - SMP's workspace seed has no config (`internal/customer/workspace/reposeed/seed/` holds only a
     README), so fresh workspaces are unaffected.
   - Surface the error to admins and document the recovery (an admin override push).
9. **Release note.** Runs whose agents legitimately edit `workflows/steps/*.yaml` or
   `.spine/agents/**` now wait for an admin.
10. **No retry sweep.** SMP has no committing-run retry sweep: no SMP code calls
    `scheduler.WithCommitRetry`, `RunCommitRetry` or `RunRetryCycle`. A held run waits for an
    admin decision. Re-approving retries a merge that failed after an approval.
11. **Tests.** Reproduce the PoC through the embedded path:
    - a member run with `rules: []` is held;
    - a member's approve gets 403;
    - an admin's approve merges;
    - the projection writer installs the rules.
    - Also: an admin override push of a relaxed config is installed, and a relaxation pushed to
      the bare repo outside the gateway is refused with the rules kept.

> **Design-check note (2026-10-06): additional SMP TASK-010 items** (from grepping SMP's callers).
>
> 12. **`MergeBases` on `RealGitOperator`.**
>     - Implement `git merge-base --all` with the same pre-fetch and SHA handling as `MergeBase`
>       (`gitoperator.go:329-370`).
>     - Without it, `WithGovernanceGuard` returns an error (I1).
> 13. **Local-SHA `Diff`.** §3.4a diffs `BaseSHA..M`, where `M` is an *unpushed* merge commit in
>     the persistent clone. `RealGitOperator.Diff` (`:793-830`) must accept two local object IDs
>     without fetching them by name.
> 14. **The embedded engine's push of `main` never emits `branch_protection.override`.**
>     - If it ever did, for example by pushing through the gateway with `spine.override=true`,
>       every merge commit (unapproved ones included) would become Source 1 provenance. The
>       projection guard would then be void.
>     - Today the engine pushes to the binding URL with no override option (`gitoperator.go:477-530`),
>       and the gateway's actor paths are runner, job and session only (`githttp/gateway.go:470-600`).
>     - Add a test that pins this.
> 15. **Docindex and docmemory.** With refusal no longer a sync error (§5 note 3), a refused
>     relaxation does not stall them. Verify with `go test ./internal/customer/docindex/...
>     ./internal/docmemory/...`.
> 16. **Webhook fan-out of the three new event types.**
>     - `spinecore.NewEventEmitter` fans out to subscriptions with `event_types = '{}'`
>       (`eventemitter.go:142-148`).
>     - Confirm that SMP's step-event receiver accepts or ignores unknown types with a 2xx.
>     - A 4xx would leave non-terminal delivery rows (P3-5).
> 17. **Projection writer pool.** The writer's `_runtime` pool (`projectionwriter/worker.go:31-38`)
>     must be able to `SELECT` from `runtime.event_log` and `runtime.runs`. So must the
>     provisioning `FullRebuild` pool (`provision_steps_runtime.go:315-325`). A permission error
>     refuses every removal, which fails closed but also blocks legitimate ones. Add an integration
>     assertion.

### 10. Regression tests from the audit PoCs

The PoC bodies are committed as tests. Each PoC is inverted: it now asserts that the hole is
closed. Each test fails when the fix is mutated back to v1 behaviour (see the Acceptance
Criteria).

- **`poc/verify-r1-2/poc_r12_engine_test.go`** becomes
  `core/engine/governance_guard_test.go::TestGovernanceGuard_SelfReviewedRunCannotSlipGovernanceFiles`.
  - It uses the same fixtures (`testutil.NewTempRepo`, the real `git.CLIClient`, the real
    `branchprotect.New`, `mockRunStore`), with the guard on and TASK-005's binding fixture.
  - It asserts:
    - the run stays `committing`;
    - `main:.spine/branch-protection.yaml` still has the `main` rule;
    - `main:workflows/task-default.yaml` still has `id: review`;
    - the publish step's `ErrorDetail.Classification == governance_approval_required`;
    - one `governance.merge_held` event, listing both paths.
- **`poc/verify-r1-2/poc_r12_projection_test.go`** becomes
  `adapters/projection/protection_guard_git_test.go::TestProtectionRemovalGuard_IncrementalSyncKeepsRulesOnSlippedPolicy`
  (real git):
  - the rules still protect `main`;
  - `sync_state.Status == "error"`;
  - `LastSyncedCommit` stays at the pre-merge SHA;
  - one `branch_protection.removal_refused` event;
  - Contributor `direct_write` and `delete` on `refs/heads/main` are denied.
- **`poc/store-projection/poc_bp_relax_test.go`** becomes
  `adapters/projection/protection_guard_test.go::TestProtectionRemovalGuard_RefusesUnprovenancedEmptyRuleset`:
  - `upsertCalls == 0`;
  - the error `errors.Is(…, ErrProtectionRemovalUnauthorized)`;
  - the policy built over the installed rows still denies.
  - `fakeStore` (`branch_protection_test.go:44-61`) gains `ListBranchProtectionRules`.

> **Design-check note (2026-10-06): the probe becomes a regression test, and assertions change.**
> - **The new probe.** `_work/task006/designcheck/zz_dc_task006_crisscross_test.go` becomes
>   `core/engine/governance_guard_git_test.go::TestGovernanceGuard_CrissCrossRevertIsCaught`
>   (AC 28).
> - **Projection regression.** The assertions "`sync_state.Status == "error"`" and
>   "`LastSyncedCommit` stays at the pre-merge SHA" in the projection regression above are
>   replaced (§5 note 3):
>   - the sync ends `idle` at the merge commit;
>   - the rules are kept;
>   - one `branch_protection.removal_refused` event;
>   - a later commit that does not touch the file does not re-query provenance.
> - **The original PoCs stay green.** Opt-in (I1) means the three original PoC files, which never
>   enable a guard, still PASS against the merged tree. The *inverted* tests are the evidence.
>   AC 27 is corrected to match.

---

## Decisions

- **D1. Hold the run in `committing`.** Alternative: route back at once through `merge_failed`.
  Holding lets an approval finish the same run with no churn, and reject keeps the route-back.
  Routing back at once would force the author through execute, review and publish again for
  every approval.
- **D2. Approval only for held (`committing`) runs.** Alternative: approval at any time.
  - This binds the approval to the final bound SHA, which is exactly what the operator sees.
  - It also avoids racing `routeStepOutcome`'s whole-map `SetCommitMeta`.
  - A pre-approval during review would be overwritten or go stale.
- **D3. Store the approval in reserved `commit_meta` keys plus an event.**
  - A new table was rejected: I6 forbids schema changes.
  - Event-only storage was rejected for three reasons:
    - `event_log` is pruned (`postgres_delivery.go:295`);
    - standalone writes it best-effort (`subscriber.go:78-84`);
    - the merge gate must not depend on a delivery side effect.
- **D4. The decision is a pure function in `branchprotect`, also enforced in the engine.** This
  keeps ADR-009 §3's single decision point, and puts the facts on the `Request` for audit and for
  future policies. Calling it from the engine as well means a `NewPermissive()` policy or a
  custom `Policy` cannot drop the guard. Enforcing it only inside `Policy.Evaluate` would be
  bypassed by custom implementations.
- **D5. Case-folded prefixes, failing closed on quoted paths, instead of globs.**
  - `path.Match` cannot express `**` and does not cross `/`.
  - F1 already showed how `path.Match` can surprise a security gate.
  - Case folding covers checkouts on case-insensitive filesystems.
  - Quoted paths cover the pre-TASK-003 `Diff` (P3-9).
- **D6. Projection provenance comes from database records, checked by content, reachability and
  newness.** Rejected alternatives:
  - **Commit trailers:** forgeable by any run-branch author, which the plan forbids.
  - **Finding the introducing commit with a first-parent path log:** this needs a new
    `git.GitClient` method. That would break SMP's implementers of the interface, for example
    `spinecore/divergencegitclient.go`.
  - **Signed commits:** there is no key infrastructure, and ADR-009 §6 keeps them out of scope.
- **D7. The projection guard only acts while `no-direct-write` is installed on the authoritative
  branch.** Without it, direct pushes to `main` are already unguarded and produce no override
  event (ADR-009 §1 bullet 3), so demanding provenance would only block operators.
- **D8. Both guards are opt-in in the library and on in `spine serve`.** Existing tests, fakes
  (`fakeStore` embeds a nil `store.Store`) and SMP keep working until they opt in. Standalone
  closes F3 by default.
- **D9. A new ADR plus amendment notes, rather than rewriting ADR-009 in place.** The change
  touches both ADR-009 and ADR-008, and history stays readable. The owner may prefer an in-place
  ADR-009 amendment (owner question 1).
- **D10. Reserved keys are stripped in the engine and rejected by the validator.** Stripping is
  the control. The validator gives authors early feedback. Without stripping, a workflow on
  `main` could set `commit: {spine.governance_approval.sha: …}`.
- **D11. No separation-of-duties rule for the operator approver.** An operator can already land
  the same change with an override push. Requiring a second operator could deadlock
  single-operator workspaces (SMP pilots).
- **D12. A consumption marker (`merge_sha`) defines provenance.** Without it, an unconsumed or
  stale approval could vouch for a merge commit that merely inherited a relaxed config from an
  unauthorized earlier commit.
- **D13. `MergeCommitMeta` is an optional interface, not a new `RunStore` method.** Adding it to
  `RunStore` would break SMP's `RunStore` at the bump. The read-modify-write fallback is safe
  only because writes are limited to `committing` runs.

*Added by the design check (2026-10-06):*

- **D14. Pre-merge union over all merge bases, plus a post-merge assertion.** The single
  `merge-base` is provably bypassable (Purpose §5 note).
  - The union is exact under default git config and keeps the hold/approve UX accurate.
  - The assertion makes the guard independent of merge semantics. It costs one `Diff` per
    merge.
  - *Rejected:* `git merge-tree --write-tree` to pre-compute the result tree. It needs git 2.38+
    on every image, and a new operator method that SMP must implement anyway.
- **D15. The guard sits after `resolveMergeSource`, not in `preflightCheck`.** This follows
  TASK-005 §15. It is the only position where every hold is decidable and the merge source
  (including the cascade) is known.
- **D16. Provenance must have changed the config.** Content equality alone lets any later
  operator act launder an unauthorised relaxation that is already on `main`.
- **D17. A refusal is not a sync error.** This keeps one rule across the projection (TASK-011
  D2). It accepts lower visibility to avoid an unbounded re-sync loop and a workspace-wide
  document-index stall in SMP.

## Owner questions

1. **Authorization model, recorded in the ADR.** Default: **(a)**.
   - (a) An operator+ approval of the bound SHA. The merge is held until approve or reject.
   - (b) Refuse every governed merge that touches governance paths. Governance files would then
     change only through an override push or the ADR-008 operator bypass.
   - (c) A second reviewer (role reviewer or higher, not the executor). This depends on TASK-007
     and is weaker than ADR-009 §5's operator-only intent.
   - Also: a new ADR-016 (default), or an in-place ADR-009 amendment?
2. **Guarded paths.** Default: `.spine/`, `.spine.yaml`, `workflows/` in Spine. Should SMP TASK-010
   also guard AI-agent configuration (`.claude/`, `.codex/`, `.agents/`, `.mcp.json`,
   `CLAUDE.md`, `AGENTS.md`; `internal/executionmeta/agent_config.go`)? Default: **no**. That
   content does not change governance, and SMP TASK-017 already flags it.
3. **ADR-008 lifecycle exemption.** Default: **on in `spine serve`, off in SMP.** A lifecycle run
   may merge its own `workflows/<id>.yaml` without operator approval, but not the lifecycle
   workflow itself, and not any other governance path.
4. **Standalone default.** Default: **both guards on in `spine serve`**, with
   `SPINE_GOVERNANCE_GUARD=off` restoring v1. The alternative is off by default, which leaves F3
   open on standalone.
5. **Projection guard scope.** Default: **the authoritative branch only**, matching the plan.
   Should it also refuse removing protection from any currently protected pattern (for example
   `release/*`, `staging`)?

*Added by the design check (2026-10-06):*

6. **Post-merge mismatch (§3.4a).**
   - Default: **a permanent refusal.** Nothing is pushed, the local target is restored, and
     `merge_failed` routes back (or the run fails on workflows with no publish step).
   - Alternative: turn it into a governance hold that lists the extra paths, so an operator can
     approve. This is more convenient, but it would approve content the pre-merge evaluation
     never showed.
   - With `MergeBases` in place this fires only under non-default git merge config.
7. **Refused protection removal and `sync_state`.**
   - Default: **not a sync error** (TASK-011 D2). The sync goes `idle`. The rules are kept, and
     the event and Warn log are the signal.
   - Alternative: the original `error` state. It is more visible, but it freezes
     `LastSyncedCommit`, so the diff window grows every tick. It also stops SMP's governed
     document index for the workspace until an admin re-affirms.
8. **Provenance must have changed the config.**
   - Default: **yes.** Override records count only when the push's old→new range changed
     `.spine/branch-protection.yaml`, and approval records only when `BaseSHA`→`M` changed it.
   - Cost: the documented re-affirm must edit the file, for example a comment line, rather than
     push any commit.
9. **Evidence carve-out.**
   - Default: **on.** `.spine/runs/<own run id>/evidence/` never needs approval. Another run's
     evidence directory still does.
   - Alternative: off. Then every run that produces evidence under the documented convention is
     held.
10. **ADR-008 exemption hardening (§3.3 note).**
    - Default: the exemption also requires local reviewer≠author and that the edited workflow
      never declares `applies_to: [Workflow]`.
    - Alternative: keep the original predicate, and make `spine serve` turn the exemption on only
      once TASK-007 SoD and TASK-013 skill authority are enabled.

## Acceptance Criteria

1. **ADR.**
   - `architecture/adr/ADR-016-governance-path-guard.md` (or the number assigned at merge)
     exists, records the owner question 1 decision and is `Accepted` before the code merges.
   - ADR-009 §1, §5, §6 and Consequences, and ADR-008 §2, each carry an "Amended by ADR-016"
     note.
   - `security-model.md`, `event-schemas.md`, `engine-state-machine.md` and
     `docs/git-push-guide.md` are updated.
2. **Zero value is v1.** `TestGovernanceGuard_ZeroValueKeepsV1Behaviour` shows that a run
   touching `.spine/branch-protection.yaml` and `workflows/` merges with no approval and no new
   events when `WithGovernanceGuard` is not called. The existing `core/engine`, `core/branchprotect`
   and `adapters/projection` tests pass with no edits other than `fakeStore` gaining
   `ListBranchProtectionRules`.
3. **Engine regression (the inverted audit PoC).** `TestGovernanceGuard_SelfReviewedRunCannotSlipGovernanceFiles`
   asserts everything §10 lists. **Mutation:** if `EvaluateGovernance` returns Allow
   unconditionally, the test fails.
4. **Approve flow.** In the same fixture, `DecideGovernanceMerge(approve)` by an operator with the
   bound SHA:
   - merges, so `main` carries the run's content and the run is `completed`;
   - completes the publish step as `published`;
   - writes the five `spine.governance_approval.*` keys, with `merge_sha` equal to the primary
     outcome's `merge_commit_sha`;
   - emits one `governance.merge_decided` (`approved`) event.
5. **Reject flow.**
   - On task-default, reject leaves the run `active` at `execute` and the publish step completed
     with `merge_failed`.
   - On a workflow with no publish step (artifact-creation shape), reject leaves the run `failed`
     with the branch preserved.
   - `main` is unchanged in both cases.
6. **Who may approve.**
   - Actors with roles `contributor`, `reviewer` or empty get `ErrForbidden`. `commit_meta` does
     not change and no event is emitted.
   - `operator` and `admin` succeed. **Mutation:** if the role check is removed, the test fails.
7. **SHA binding.**
   - An approval whose SHA differs from the bound SHA gets `ErrConflict`.
   - In this scenario the stale approval is not honoured; the guard holds again and names the
     stale SHA:
     1. an approval is recorded for X;
     2. `merge_failed` routes back;
     3. a new commit makes the bound SHA Y;
     4. the run is accepted again.
   - **Mutation:** if the SHA equality check is dropped, both tests fail.
8. **State checks.**
   - A decision on a run that is not `committing` gets `ErrConflict`.
   - A decision on a `committing` run with no governed paths gets `ErrPrecondition`.
   - A malformed SHA gets `ErrInvalidParams`.
   - A reject with no reason gets `ErrInvalidParams`.
9. **Forgery.**
   - A workflow outcome with `commit: {status: Done, spine.governance_approval.sha: <bound>,
     spine.governance_approval.actor_role: admin}` leads to a held merge.
   - The stored `commit_meta` has `status` and no `spine.` keys.
   - `workflow.ValidateSchema` reports an error at `…outcomes[i].commit.spine.governance_approval.sha`.
   - **Mutation:** if the strip is removed, the merge happens and the test fails.
10. **Path matching** (table test of `GovernancePaths.Match` and `Touched`).
    - Matched:
      - `.spine/branch-protection.yaml`, `.spine` (a file or symlink), `.SPINE/agents/x.yaml`;
      - `.spine.yaml`, `workflows/a.yaml`, `workflows/steps/validate.yaml`, `Workflows/A.yaml`;
      - a rename `workflows/a.yaml` → `docs/a.yaml` (through `OldPath`);
      - a deletion of `.spine/branch-protection.yaml`;
      - `"workflows/\303\244.yaml"` (C-quoted);
      - a path with a control byte.
    - Not matched: `workflowsx/a.yaml`, `docs/workflows/a.yaml`, `spine/x`, `my.spine.yaml`.
    - Invalid `NewGovernancePaths` entries (`""`, `../x`, `work*`, `a\x00`) return errors.
    - **Mutations:** the test fails when either of these is removed:
      - the `OldPath` append;
      - the quoted-path fail-closed branch.
11. **Fail closed on git errors.** With a fake `GitOperator` whose `MergeBase` or `Diff` returns an
    error (and, separately, an empty bound SHA):
    - `MergeRunBranch` returns an error that is not `ErrGovernanceApprovalRequired`;
    - `Merge` is never called;
    - the run stays `committing`.
12. **Idempotent hold.**
    - Three consecutive `MergeRunBranch` calls on a held run emit `governance.merge_held` with one
      identical event ID.
    - `UpdateStepExecution` is called for the hold only once (counted on the mock store).
    - The step's `ErrorDetail.Classification` is `governance_approval_required`. On the
      terminal-commit path it is on the last completed step.
13. **Lifecycle (ADR-008).** With `ExemptWorkflowLifecycleTaskPath: true`, a run shaped like
    `StartWorkflowPlanningRun` (planning mode, `TaskPath: workflows/foo.yaml`, governing
    `applies_to: [Workflow]`):
    - merges without approval when the diff touches only `workflows/foo.yaml`;
    - is held when the diff also touches `.spine/x.yaml`;
    - is held when `TaskPath` is the governing workflow's own path;
    - is held with the exemption off;
    - in the held case, merges after operator approval (the authorised lifecycle edit still
      merges).
    - **Mutation:** removing the `TaskPath != WorkflowPath` condition fails the self-edit case.
14. **Cascade footprint.** A run with `commit_meta.status` set and `TaskPath` under `.spine/` is
    held even when its diff does not touch `TaskPath`.
15. **No side effects while held.** For a held run with a code repository:
    - `mergeAffectedCodeRepositories` is not reached (fake `RepositoryGitClients` call count 0);
    - the run branch has no status-cascade commit.
16. **Consumption marker.**
    - `spine.governance_approval.merge_sha` is written only on an approved, successful merge.
    - A merge with no governed paths writes no `spine.` keys.
    - If `MergeCommitMeta` fails, `MergeRunBranch` returns an error and the run stays
      `committing`.
17. **Projection regression (the inverted `poc_bp_relax`).**
    `TestProtectionRemovalGuard_RefusesUnprovenancedEmptyRuleset` asserts everything §10 lists.
    **Mutation:** if `guardProtectionRemoval` returns nil, the test fails.
18. **Projection regression with real git (the inverted `verify-r1-2` projection PoC).**
    `TestProtectionRemovalGuard_IncrementalSyncKeepsRulesOnSlippedPolicy` asserts everything §10
    lists. The same guard also refuses through `FullRebuild` (rules kept, `sync_state=error`).
19. **Projection, accepted cases** (real git, fake provenance reader):
    - an `override_event` record whose SHA is the pushed tip carrying `rules: []` → installed;
    - a consumed `governance_approval` record whose SHA is the merge commit → installed;
    - after either, an unrelated later commit on `main` → still installed, and provenance is not
      re-queried.
20. **Projection, refused cases.** Each is refused with the rules kept:
    - a record that is an ancestor of the installed source commit (replay);
    - a record not reachable from head;
    - a record whose config bytes differ;
    - an empty record list;
    - a provenance reader error;
    - a nil reader.

    **Mutations:** each case fails if its own check is removed (newness, reachability, content).
21. **Projection, no-lookup cases.** A provenance reader that fails the test when called:
    - tightening (adding `release/*`) installs;
    - reordering installs;
    - deleting the file installs the bootstrap rules;
    - installed rules without `no-direct-write` on `main` let `no-delete` be removed.
22. **Store.** `ListGovernanceAuthorizations` integration test against Postgres, following the
    existing `adapters/store` integration-test pattern:
    - it returns override events written in the Spine-subscriber shape and in the SMP-emitter
      shape (`branch` `main` and `refs/heads/main`);
    - it returns consumed approvals;
    - it excludes:
      - `delete` overrides;
      - other branches;
      - approvals without `merge_sha`;
      - approvals whose `merge_sha` differs from `merge_commit_sha`;
    - `MergeCommitMeta` merges without dropping existing keys;
    - `git diff --name-only <base>..HEAD -- migrations/` is empty.
23. **Standalone gateway.**
    - `POST /runs/{run_id}/governance-decision` returns 403 for a contributor token and 200 for an
      operator. The decision-route statuses (400, 409, 412, 503) are covered by handler tests.
    - `GET /runs/{run_id}/governance` returns the status.
    - `api/spec.yaml` documents both routes.
    - `SPINE_GOVERNANCE_GUARD` parsing is table-tested: unset, `off`, `false`, `0`, `no`, `on`,
      and junk (which means on, with a warning).
24. **Scenario.** One scenario under `service/scenariotest` with the guard on:
    1. a contributor's run touches `workflows/task-default.yaml`;
    2. the merge is held;
    3. an operator approves through the API;
    4. `main` advances;
    5. the projection installs the new workflow version.
25. **Events.** The three new event types are in `allEventTypes()` and in
    `architecture/event-schemas.md`. `TestAllEventTypes`, or its equivalent, covers them.
26. **SMP stays additive.** With SMP `main` pointed at this branch through its `go.mod` replace and
    no SMP edits, `go build ./...` and `go vet ./...` in SMP pass. The PR body records the
    command and its output.
27. **Gates.** In Spine:
    - `go build ./...`, `go vet ./...` and `go test ./...` are green;
    - `go test -race ./core/engine/... ./core/branchprotect/... ./adapters/projection/...` is
      green;
    - lint is at the existing baseline;
    - `codex review` raises no open P1 or P2.

    The original audit PoCs, compiled with the overlay against the merged tree, now fail on their
    vulnerability assertions. The PR body records this.

> **Design-check note (2026-10-06): corrections to ACs above.**
> - **AC 4:** "the five keys" becomes **six**. `spine.governance_approval.base_sha` is added, and
>   `merge_sha`/`base_sha` are written **before** the push (the §3.8 note).
> - **AC 11:** add the post-merge cases. A `Diff(BaseSHA, M)` error, or a `MergeBases` error, also
>   refuses: nothing is pushed, and local `refs/heads/main` equals `BaseSHA`.
> - **AC 12:** the event ID includes the episode (the §3.5 note).
> - **AC 13:** add three cases. Each **is held**:
>   - the review step was completed by the draft step's actor;
>   - the bound content of `TaskPath` declares `applies_to: [Workflow]`;
>   - `TaskPath`'s case differs from the parsed workflow `id`.
>
>   Mutation: removing the local SoD check fails the first case.
> - **AC 14:** the cascade footprint is now covered by diffing `src.sha` (the §3.2 note). The test
>   keeps its assertion. Mutation: diffing `src.bound` instead of `src.sha` fails it.
> - **AC 18:** replace "`sync_state.Status == "error"`; `LastSyncedCommit` stays at the pre-merge
>   SHA" with:
>   - the sync ends `idle` at `head`;
>   - the rules still protect `main`;
>   - exactly one `branch_protection.removal_refused` event across three ticks;
>   - an operator override push that **edits** the file installs it on the next tick.
> - **AC 20:** add these refused cases:
>   - **(laundering)** an override record whose `old_sha` and new SHA carry identical config bytes;
>   - **(laundering)** an approval record whose `base_sha` and `merge_sha` carry identical config
>     bytes;
>   - **(bootstrap replay)** installed rows stamped `"bootstrap"`, and a matching record that is
>     an ancestor of the sync's starting `LastSyncedCommit`.
>
>   Mutations: removing the changed-config check or the `LastSyncedCommit` anchor fails the
>   matching case.
> - **AC 22:**
>   - Source 1 rows without `pre_receive_ref.old_sha` (the API-path shape) are excluded.
>   - Source 2 needs no `repository_merge_outcomes` row, and excludes approvals with an empty or
>     tombstoned `sha`.
> - **AC 26:** add the test suites to build and vet. With the `go.mod` replace pointed at this
>   branch and no SMP edits, `go test` passes (those runnable without the Docker test stack) for:
>   - `./internal/customer/spinecore/...`
>   - `./internal/customer/spineembedded/...`
>   - `./internal/customer/projectionwriter/...`
>   - `./internal/customer/githttp/...`
>   - `./internal/customer/docindex/...`
>   - `./internal/docmemory/...`
> - **AC 27:** the PoC sentence above is **wrong under I1** and is replaced:
>   - The three original PoCs still PASS against the merged tree, because they never enable a
>     guard. This demonstrates opt-in.
>   - The inverted tests (AC 3, 17, 18, 28) FAIL when their guard is mutated back to v1.
>   - The PR body records both runs.
>
> **New acceptance criteria (design check):**
>
> 28. **Criss-cross is caught.** `TestGovernanceGuard_CrissCrossRevertIsCaught` uses the probe's
>     history: the run branch merged R2 and reverted its governance change, and R2 was then merged
>     to `main`.
>     - (a) With `MergeBases`, the run is **held**, and `Touched()` lists
>       `.spine/branch-protection.yaml`.
>     - (b) With a test `GitOperator` whose `MergeBases` returns only the single `merge-base`, the
>       §3.4a assertion refuses:
>       - nothing is pushed;
>       - `main` still has the tightened bytes;
>       - local `refs/heads/main == BaseSHA`, and the working tree is clean;
>       - with `SPINE_GIT_PUSH_ENABLED=false` the refused content is not on `main`.
>     - The same for a `workflows/task-default.yaml` revert.
>     - Mutations: (a) fails if the union is reduced to one base; (b) fails if §3.4a is removed.
> 29. **Binding prerequisite, and no undecidable holds.**
>     - `WithGovernanceGuard(Enabled: true)` returns an error, and leaves the guard off, when
>       binding is off or `o.git` lacks `MergeBases`.
>     - A `committing` run with a missing or invalid `spine.bound_sha` takes TASK-005's
>       `bindingFailure` route (`merge_failed`, or `failed`), with **no**
>       `governance_approval_required` detail and no `governance.merge_held` event.
>     - A table test over every guard outcome asserts that whenever a run is left `committing`
>       with a hold, `DecideGovernanceMerge` accepts both approve and reject for its bound SHA.
> 30. **`artifacts_dir` workflows.** With `WorkflowDirs` from `WorkflowDirs("spine")`:
>     - a run touching `spine/workflows/task-default.yaml` is held;
>     - a run touching root `workflows/x.yaml` is held;
>     - `spine/docs/a.md` is not.
>     - The standalone wiring test asserts that both builders pass `WorkflowDirs`.
> 31. **Reject revokes.**
>     1. Approve X while the merge is made to fail transiently, so the run stays `committing`.
>     2. Reject.
>     3. Assert that `spine.governance_approval.sha == ""`.
>     4. Loop back and resubmit X unchanged, and review accepts: the run is **held** again, with a
>        new `governance.merge_held` event ID.
>     - Mutation: removing the tombstone write fails the test.
> 32. **Evidence carve-out.**
>     - A run whose only governed change is `.spine/runs/<its run id>/evidence/spine.yaml` merges
>       with no approval.
>     - A run that touches `.spine/runs/<another run id>/evidence/spine.yaml`, or
>       `.spine/runs/<its id>/x.yaml`, is held.
> 33. **Entry coverage.** `.spine.yaml/x` is matched (a `Match` table row), and `workflows` as a
>     file is matched.
> 34. **No race between decisions.** On a workflow with no publish step, an approve that completes
>     the merge, followed by a reject, makes the reject return `ErrConflict`. The run stays
>     `completed`, and no `run_failed` event is emitted. With the compare-and-swap reverted to
>     `UpdateRunStatus`, the test fails.
> 35. **Scheduler noise.** In standalone, a held run under the commit-retry sweep logs no Error
>     line (the closures swallow `ErrGovernanceApprovalRequired`). This is a handler or wiring
>     unit test.

## Out of scope / follow-ups

- **Separation of duties** for assign, claim and review is the amplifier. TASK-007 and SMP
  TASK-011 cover it.
- **Binding the merge to the reviewed SHA** is TASK-005. **Projection sync integrity** is
  TASK-011. **NUL-delimited `Diff`** is TASK-003. **The convergence-to-`main` path (F8) and
  bounded retries** are TASK-012, which must exclude holds.
- **Turning both guards on in SMP**, plus SMP's approval UI and API, is SMP TASK-010.
- **Symlinked governance directories.**
  - If `main` already holds `.spine/agents` as a symlink to a directory outside the guarded
    trees, edits to the target are not governed paths.
  - Spine's projection reads by git path and does not follow such links. SMP's runner does
    follow in-repository links through `os.Root` (SMP TASK-013 notes).
  - The symlink itself is a guarded change once this task lands. Treating any change to the mode
    of a symlink (120000) as governed is a candidate follow-up for SMP TASK-010 or a later Spine
    task.
- **Projection provenance for `workflows/**`.** It is not added here. Workflows reach `main` only
  through guarded merges, the ADR-008 operator bypass, or pushes gated by branch protection. P3-13
  (the parser differential) is TASK-011.
- **Cancel from `committing`.** It is not added, because reject covers the stuck-run case. A
  state-machine change would need its own task.
- **A committing-run retry sweep for SMP.** It is an existing SMP gap and is not created here.
- **Commit trailers.** No trailer is written or read for governance merges. ADR-009 §4's
  `Branch-Protection-Override` trailer stays a convenience only.

## Design check (2026-10-06)

**Method.**
- **Repositories, read-only.** Spine `61994fe` and SMP `80995559`; both trees clean afterwards.
- **PoCs.** The three F3 PoCs were re-run with
  `_work/task006/overlay_poc.json`. All three still PASS.
- **New probe.** `_work/task006/designcheck/zz_dc_task006_crisscross_test.go` (overlay
  `designcheck/overlay.json`) PASSES. It proves the bypass in item 1 with Spine's real
  `CLIClient`.
- **Line references.** Every line reference in Purpose, §2-§7 and §9 was re-read. They are
  accurate. Minor drifts are not worth an edit: `projection.Service.events` is an
  `event.EventRouter` (it satisfies `event.Emitter`), and `bpproj.New` returns
  `(*RuleSource, error)`.

**What changed, and why.**

1. **Bypass: criss-cross merge bases.**
   - `Diff(merge-base(main,S), S)` missed a governed file that the merge reverted on `main`.
   - Reproduced, not theoretical.
   - Fix: union over `merge-base --all` (the new optional `MergeBaseLister`), plus the §3.4a
     post-merge assertion (I2, D14, AC 28).
2. **Bypass: `artifacts_dir` workflows.**
   - `spine init-repo` defaults to `spine/`, and `FullRebuild` projects `spine/workflows/*.yaml`.
   - The default guarded set missed them, and TASK-011 had already flagged this.
   - Fix: `WorkflowDirs` (§3.1/§7, AC 30).
3. **Stuck runs.**
   - Placing the guard in `preflightCheck` turned a missing binding into a hold. Neither approve
     nor reject could clear it, and cancel is not allowed from `committing`.
   - Fix: the guard moves after TASK-005's `resolveMergeSource`. It requires binding at enable
     time, and step 2 becomes a non-hold reason (§3.4 note, D15, AC 29).
4. **Reject overridden.**
   - TASK-005 preserves reserved keys, so an approval survived a reject and later merged the same
     SHA.
   - Fix: a reject tombstone (§3.6 note, AC 31). The §3.7 claim that the next episode starts
     with no approval was corrected.
5. **Projection laundering and replay.**
   - Content-equality provenance accepted unrelated operator acts.
   - With bootstrap rows installed, old records were replayable.
   - Fix: the changed-config rule and the `LastSyncedCommit` anchor (§4/§5 notes, D16, AC 20).
6. **SMP breakage: `sync_state=error`.**
   - A refusal froze `LastSyncedCommit` and stopped SMP's docindex and docmemory. This
     contradicted TASK-011 D2.
   - Fix: a refusal is no longer a sync error. The consumption marker moved before the push to
     remove the race this would otherwise open (§3.8/§5 notes, D17, AC 18).
7. **ADR-008 exemption.**
   - Its only gate is the lifecycle review, which one standalone reviewer can satisfy alone
     (F6 and no SoD).
   - Fix: a local SoD check, an `applies_to: [Workflow]` exclusion and a byte-exact path shape
     (§3.3 note, AC 13).
8. **Future breakage: execution evidence.**
   - `.spine/runs/<run>/evidence/` is the documented producer location, so `.spine/` would hold
     every run once a producer exists.
   - Fix: the own-run carve-out (§3.2 note, AC 32).
9. **Smaller items.**
   - A hold event ID per episode (§3.5).
   - Prefix coverage for every entry in `Match`.
   - Compare-and-swap for reject on the terminal-commit path (AC 34).
   - Scheduler log noise (AC 35).
   - Overlap with TASK-005's strip and validator removed (§3.7 note).
10. **ACs corrected.**
    - AC 27 claimed the original PoCs would fail. Under I1 they still pass, so the claim was
      unfalsifiable as written.
    - AC 26 now runs SMP's test suites, not only build and vet.
11. **Cross-task requirement raised against TASK-005.**
    - Every refusal after `MergeBound` must restore the local target ref.
    - Standalone `CLIClient` otherwise leaves the refused merge on local `main`. That is
      authoritative with push off, and the projection reads it.
    - §3.4a depends on this.

**Left as is (checked).**
- **Single decision point (D4)** and **engine-side enforcement against `NewPermissive()`.**
- **SHA format checks.**
- **No schema change (I6).**
- **SMP compiles unchanged.** `GitClient`, `GitOperator`, `RunStore` and `store.Store` are
  untouched, and SMP's only `branchprotect.Request` literal is keyed
  (`internal/customer/githttp/handler.go:574-580`).
- **Facts confirmed in code:**
  - SMP has no `StartWorkflowPlanningRun` caller and no commit-retry sweep;
  - cancel is impossible from `committing`;
  - publish steps have no timeout (`workflows/task-default.yaml:93-116`);
  - committing runs are skipped by the run-timeout and orphan scanners.

## Implementation Notes

