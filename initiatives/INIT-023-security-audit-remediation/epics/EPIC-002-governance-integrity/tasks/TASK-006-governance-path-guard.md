---
id: TASK-006
type: Task
title: "Governance-path guard on governed merges and protection-removal guard in projection"
status: In Progress
work_type: security
created: 2026-10-06
last_updated: 2026-11-04
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

Implemented on branch `init/INIT-023-TASK-006-governance-path-guard`, from Spine local `main`
`ae513dca` (origin `61994fe` + the INIT-023 scaffold + waves 1–3 + the codex 6.1 catch-up). Diff
base: `git diff main...HEAD`. Status stays In Progress; the orchestrator closes it after merge.
No runtime schema change (`git diff --name-only main...HEAD -- migrations go.mod go.sum` is empty).

### Orchestrator rulings recorded (owner questions take their defaults, pending the owner)

- **Scope:** audit F3 (governance files ride governed merges) and its projection half.
- **OQ1** (default **(a)**): operator-or-above approval of the exact bound commit; the merge is held
  until approve or reject. Recorded in a **new ADR**: `ADR-016` was the next free number in
  `architecture/adr/` (ADR-001…015 exist; no sibling wave-4 worktree had taken it). The ADR is
  `Proposed` until the owner signs off OQ1.
- **OQ2:** guarded paths `.spine/`, `.spine.yaml`, `workflows/` and `<artifacts_dir>/workflows/`;
  AI-agent configuration not guarded.
- **OQ3:** the ADR-008 lifecycle exemption is on in `spine serve`, off in the embedded zero value.
- **OQ4:** both guards on in `spine serve`; `SPINE_GOVERNANCE_GUARD=off` restores v1; the embedded
  zero value is today's behaviour.
- **OQ5:** the projection guard covers the authoritative branch only.
- **OQ6:** a post-merge mismatch is a permanent refusal.
- **OQ7:** a refused protection removal is not a sync error (TASK-011 D2: the sync goes idle,
  the rules are kept, event + Warn).
- **OQ8:** provenance must have changed the config.
- **OQ9:** the own-run evidence carve-out (`.spine/runs/<own run id>/evidence/`) is on (in
  `spine serve`; the engine option's zero value is off, see Deviations 3).
- **OQ10:** exemption hardening — local reviewer≠author, and the edited workflow (bound and
  merge-base versions) never declares `applies_to: [Workflow]`.
- **Placement:** the guard plugs in after TASK-005's `resolveMergeSource` and binds to TASK-005's
  bound SHA; it reads through TASK-003's bounded `Diff`/`ReadFile` and feeds TASK-011's
  projection. SMP TASK-010 adopts it; the embedded API is in `docs/integration-guide.md` §10.

### What was built

| Commit | Change |
|---|---|
| `09ea2afe` | `architecture/adr/ADR-016-governance-path-guard.md` (Proposed); "Amended by ADR-016" notes in ADR-009 §1, §5, §6, Consequences and ADR-008 §2 |
| `346994a9` | `core/branchprotect/governance.go`: `GovernancePaths` (validated, case-folded prefixes — Unicode simple folding, so U+212A and U+017F fold too; every entry covers what lies beneath it; unsafe, control-byte and undecodable C-quoted paths are governed), `GovernanceApproval`, `GovernanceCheck.Touched()` (byte-exact exemptions), `EvaluateGovernance`. `Request.Governance` (consulted only for `OpGovernedMerge`), reason codes `governance_approved`, `governance_approval_required`, `governance_unevaluable`; `Policy.Evaluate` defers to it and still loads no rules |
| `34d1314e` | `core/domain/governance.go` (the six `spine.governance_approval.*` keys), `FailureGovernanceApprovalRequired`, events `governance.merge_held`, `governance.merge_decided`, `branch_protection.removal_refused` registered in `allEventTypes()` |
| `3b313528` | `(*git.CLIClient).MergeBases` (`git merge-base --all`, TASK-002 argument handling, fail closed) — the optional `engine.MergeBaseLister` |
| `b4c06b07` | `core/engine/governance_guard.go` (`GovernanceGuard`, `WithGovernanceGuard`, `MergeBaseLister`, `ErrGovernanceApprovalRequired`, `governanceGate`, `buildGovernanceCheck` — union over every merge base of the merged commit — the lifecycle exemption, the evidence carve-out, the hold record, the post-merge assertion `assertGovernedMerge`, the consumption marker, the governance take-back), `core/engine/governance_decision.go` (`DecideGovernanceMerge`, `GovernanceMergeStatus`); `MergeRunBranch` calls the gate after `resolveMergeSource`; `mergeSource.gov`; the assertion runs in `mergePrimaryBound` after `verifyBoundMerge`, before the push |
| `f913f952` | `(*store.PostgresStore).MergeCommitMeta` (`commit_meta \|\| patch`), `ListGovernanceAuthorizations` (override events with `pre_receive_ref.old_sha`; consumed approvals with `merge_sha` and `base_sha`, tombstoned sha excluded); neither joins `store.Store` |
| `d5d577c7` | `adapters/projection/protection_guard.go`: `ProtectionRemovalGuard`, `WithProtectionRemovalGuard`, `ErrProtectionRemovalUnauthorized`, `GovernanceProvenanceReader`, `Service.WorkflowDirs()`; both upserts in `projectBranchProtectionAt` pass the guard; `FullRebuild` and `IncrementalSync` treat a refusal as a Warn, not an error |
| `0a8a06a6` | `service/cmd/spine/serve_governance_guard.go`: the switch, both orchestrator builders, both projection wirings (process level; per workspace in the pooled builder), the commit-retry wrappers (`governanceAwareCommitRetry`), permission `run.governance.decide` |
| `338f9869` | `POST /runs/{run_id}/governance-decision`, `GET /runs/{run_id}/governance` (`handlers_run_governance.go`), `api/spec.yaml` |
| `2ab5a0cb` | `harness.WithGovernanceGuard` / `WithRuntimeGovernanceGuard`; scenario `TestGovernancePathGuard_HeldUntilOperatorApproves` |
| `b5980615` | Docs: security-model §7.3, event-schemas §4.16–§4.18 + emission map, engine-state-machine (held `committing`), git-push-guide §4 (re-affirm by an editing override push), execution-evidence §6.1, api-operations, integration-guide §10, CHANGELOG, KNOWN-LIMITATIONS "Governance-Path Guard" |
| `48c99c6d` | Mutation-driven test fix (binding-off prerequisite) and lint hygiene |
| `be372095`, `0972f1b7`, `54f51f28`, `c3b9cdd3` | Codex rounds 1–4 fixes (below) |

### Per-AC evidence

| AC | Status | Evidence |
|---|---|---|
| 1 | partial | ADR-016 exists and records OQ1 (default (a)); amendment notes in ADR-009 §1/§5/§6/Consequences and ADR-008 §2; security-model, event-schemas, engine-state-machine, git-push-guide (and execution-evidence, api-operations, integration-guide) updated. The ADR is **`Proposed`**: "Accepted before the code merges" needs the owner's sign-off on OQ1 |
| 2 | done | `TestGovernanceGuard_ZeroValueKeepsV1Behaviour` (binding off and on, no `WithGovernanceGuard`: merges, no governance event, no governance keys). No existing `core/engine`, `core/branchprotect` or `adapters/projection` test was edited (`fakeStore` is wrapped by the new `guardFakeStore`, not edited) |
| 3 | done | `TestGovernanceGuard_SelfReviewedRunCannotSlipGovernanceFiles` (real `CLIClient`, real `branchprotect.New`, `mockRunStore`, binding on): committing, `main` keeps the main rule and `id: review`, publish `ErrorDetail` `governance_approval_required` (rule `governance-path-guard`), one `governance.merge_held` naming both paths, no outcome row. Mutation M1 caught |
| 4 | done | `TestGovernanceGuard_ApproveMerges`: merged, completed, publish `published` with the hold cleared, the **six** keys (`merge_sha` = the primary outcome's `merge_commit_sha`, `base_sha` = pre-merge main), one `governance.merge_decided` (`approved`) — through the read-modify-write fallback store; the atomic path in `TestGovernanceGuard_ConsumptionMarker` |
| 5 | done | `TestGovernanceGuard_RejectRoutesBack` (task-default: active at execute, publish `merge_failed`; no publish step: failed, branch kept, one `run_failed`; main unchanged) |
| 6 | done | `TestGovernanceDecision_WhoMayApprove` (contributor, reviewer, empty role, empty id → `ErrForbidden`, no write, no event; operator and admin merge). Mutation M2 caught |
| 7 | done | `TestGovernanceDecision_StateChecks/another_sha` (`ErrConflict`) and `TestGovernanceGuard_StaleApprovalIsNotHonoured` (approval X, conflict → `merge_failed`, new commit Y, accepted again → held, `stale_approval_sha` X, detail names it, a decision on X is `ErrConflict`). Mutations M3, M4 caught |
| 8 | done | `TestGovernanceDecision_StateChecks` (not committing `ErrConflict`; no governed paths `ErrPrecondition`; malformed SHA and a ref `ErrInvalidParams`; reject without reason, reason too long, unknown decision `ErrInvalidParams`; guard off `ErrUnavailable`; unknown run `ErrNotFound`; unevaluable `ErrUnavailable`) — none writes or emits |
| 9 | done (Deviation 9) | `TestGovernanceGuard_ForgedApprovalIsStripped` (outcome commit with `status: Done` and planted `spine.governance_approval.*` for the bound commit, role admin → held; stored `commit_meta` has `status`, no governance keys). `workflow.Validate` reports it at `steps[1].outcomes[0].commit` naming the key (TASK-005's rule; Deviations 9). Mutation M5 (TASK-005 strip removed) caught |
| 10 | done | `core/branchprotect/governance_test.go` `TestGovernancePaths_Match` (every listed match/non-match, C-quoted, control byte, Kelvin sign, long s), `TestNewGovernancePaths_RejectsInvalidEntries` (`""`, `../x`, `work*`, `a\x00`, …), `TestGovernanceCheck_Touched` (rename via `OldPath`, deletion); engine side `TestGovernanceGuard_RenameAndDeletion`. Mutations M6 (`OldPath`), M7 (quoted branch) caught |
| 11 | done (Deviation 22, owner ruling pending) | `TestGovernanceGuard_FailsClosed`: `MergeBases` error, malformed base, pre-merge `Diff` error → `ErrUnavailable` (not a hold), no `MergeBound`, committing, no hold record. Since review round 1 the two errors retrying cannot change — no merge base, and a `Diff` over the output limit (`git.ErrOutputTooLarge`) — take the permanent route instead (`TestGovernanceGuard_UnevaluableMergeIsRefusedNotStuck`), which the AC's literal "`Diff` returns an error → the run stays `committing`" does not allow; post-merge `Diff` error (permanent and transient) → no push, `ResetBranch main <Base> <M>`, no marker. Missing/invalid `bound_sha` → TASK-005's route: `TestGovernanceGuard_MissingBindingIsNotAHold`. Real-git local `main == BaseSHA`: AC 28(b) |
| 12 | done | `TestGovernanceGuard_IdempotentHold` (three attempts, one event id `evt-<trace12>-governance-held-<bound12>-<publish execution>`, the hold written once; terminal-commit path on the last completed step). Mutation M23 caught |
| 13 | done | `TestGovernanceGuard_LifecycleExemption` (8 cases incl. the three design-check ones; held cases merge after approval). Mutations M8 (`TaskPath != WorkflowPath`), M9 (local SoD) caught. Starter recording (round 1 finding 2): `TestStartWorkflowPlanningRun_RecordsTheStarter` on both the create and (since review round 2) the update path |
| 14 | done | `TestGovernanceGuard_CascadeFootprintIsEvaluated` (`.spine/tasks/TASK-9.md` with a status outcome, diff touches only `src/x.go` → held on the cascaded file; run branch unmoved; merges after approval). Mutation M10 (diff `src.bound`) caught |
| 15 | done | `TestGovernanceGuard_NoSideEffectsWhileHeld` (code repository: `RepositoryGitClients.Client` called 0 times; no `Commit`/`Checkout`/`WriteAndStageFile`/`MergeBound`/`PushCommit`); real git: AC 14's unmoved run branch |
| 16 | done | `TestGovernanceGuard_ConsumptionMarker` (marker written before the push; an ungoverned merge writes no governance key; a failed `MergeCommitMeta` → error, committing, no push, taken back). Mutation M24 caught. ("no `spine.` keys" read as no `spine.governance_approval.*` keys: binding keys exist by design) |
| 17 | done | `TestProtectionRemovalGuard_RefusesUnprovenancedEmptyRuleset` (no upsert, `ErrProtectionRemovalUnauthorized`, the policy over the installed rows still denies, one event; nil reader refuses; guard off installs). Mutation M11 caught |
| 18 | done | `TestProtectionRemovalGuard_IncrementalSyncKeepsRulesOnSlippedPolicy` (real git: idle at the merge commit, rules kept, contributor `direct_write`/`delete` denied, one event over three ticks, no re-query on an unrelated commit, `FullRebuild` refuses too and ends **idle** — Deviations 12: the AC's FullRebuild sentence predates OQ7/D17, which the design-check correction applied to IncrementalSync only — and an editing override push installs on the next tick). Mutations M11, M25 caught |
| 19 | done | `TestProtectionRemovalGuard_AcceptedCases` (override record; consumed approval; both still installed after an unrelated commit with no re-query; since review round 2 two authorised relaxations in one sync window — approved+approved, override+approved — and the FullRebuild that installs them once the earlier record is readable). `TestProtectionRemovalGuard_ApprovedMergeInFlightHoldsTheSync` covers a second approved merge landing during the in-flight hold |
| 20 | done | `TestProtectionRemovalGuard_RefusedCases` (replay, unreachable, bytes differ, empty list, nil reader, both laundering cases, bootstrap replay; a reader error holds the sync — Deviations 11; review round 1: an approved merge whose own diff only adds a rule; review round 2: an unauthorised relaxation between authorised acts, and authorised bytes from a line the approved base does not descend from). Mutations M12–M16 caught, and the round 2 mutants R2-M1…R2-M8 |
| 21 | done | `TestProtectionRemovalGuard_NoLookupCases` (tightening, reordering, deleting the file, `no-delete` removable without `no-direct-write`, empty table) with a reader that fails the test when called |
| 22 | done | `adapters/store/governance_integration_test.go` `TestGovernanceStore_Integration` (both branch spellings, the whole-event shape; excludes delete, other branch, API shape, other type, unconsumed, tombstoned, no base; no outcome row needed; per-source limit; `MergeCommitMeta` keeps keys, NULL column, not-found); `git diff --name-only main...HEAD -- migrations/` empty |
| 23 | done | `TestRunGovernanceDecision_Authorization` (403 contributor/reviewer, 200 operator/admin with the actor forwarded), `TestRunGovernanceDecision_StatusMapping` (400, 404, 409 ×2, 412, 503 ×3), `TestRunGovernanceStatus`; spec documents both routes; `TestParseGovernanceGuard` (unset, off, false, 0, no, on, true, 1, yes, junk → on + Warn) |
| 24 | done | `service/scenariotest/scenarios/governance_path_guard_test.go` `TestGovernancePathGuard_HeldUntilOperatorApproves` (held; contributor 403; operator approves through the API; main advances; projection installs version 1.1) |
| 25 | done | `adapters/delivery/event_types_test.go` `TestAllEventTypes_IncludesGovernanceGuardEvents`; event-schemas §4.16–§4.18 + emission map |
| 26 | done | SMP gate below (no SMP edit) |
| 27 | done | Gates below. The original PoCs, compiled into this tree with `go test -overlay`: `TestPOC_R12_SelfReviewedRunSlipsGovernanceFilesIntoMain` and `TestPOC_ProjectedEmptyRulesetUnprotectsMain` PASS (they enable no guard — I1), the design-check probe `TestDesignCheck_TASK006_CrissCrossHidesGovernanceRevert` PASSes (raw git behaviour); `TestPOC_R12_ProjectionInstallsSlippedPolicy` panics in TASK-011's sweep (its fake store has no `ListActiveWorkflowProjections`) **identically on a `git archive main` export** — pre-existing fixture rot, not this task. The inverted tests (AC 3, 17, 18, 28) fail with their guard mutated back (M1, M11, M17, M18) |
| 28 | done | `TestGovernanceGuard_CrissCrossRevertIsCaught` for `.spine/branch-protection.yaml` and `workflows/task-default.yaml`: (a) every merge base → held, the path named; (b) single-base lister → post-merge refusal, `post_merge: true` event, local main = base, tightened bytes on main, clean tree, routed back, outcome failed (push off). Mutations M17 (one base), M18 (assertion removed) caught |
| 29 | done | `TestWithGovernanceGuard_Prerequisites` (binding off — also with a `MergeBaseLister` — no `MergeBases`, invalid path → error, guard off); `TestGovernanceGuard_MissingBindingIsNotAHold`; `TestGovernanceGuard_EveryHoldIsDecidable` (6 hold shapes × approve/reject). Mutation M21 caught |
| 30 | done | `TestGovernanceGuard_ArtifactsDirWorkflows`; `TestServeWiring_EnablesGovernanceGuards` (both builders pass `workflows/,spine/workflows/` for `artifacts_dir: spine`); `TestService_WorkflowDirsFollowsArtifactsDir` |
| 31 | done | `TestGovernanceGuard_RejectRevokesTheApproval` (approve X with a transient merge failure → committing; reject; `sha == ""`; resubmit X → held under a new event id). Mutation M19 caught |
| 32 | done | `TestGovernanceGuard_OwnRunEvidenceCarveOut` (own evidence merges; another run's evidence, own run outside `evidence/`, carve-out off → held). Mutation M22 caught |
| 33 | done | `Match` rows `.spine.yaml/x` and `workflows` (a file) |
| 34 | done | `TestGovernanceDecision_RejectLosesTheRace` (no publish step; an approve completes the merge inside the reject's revocation write; reject → `ErrConflict`, run completed, no `run_failed`, no rejected decision event). Mutation M20 (`UpdateRunStatus`) caught |
| 35 | done | `TestCommitRetry_HeldMergeLogsNoError` (the scheduler's real sweep with `governanceAwareCommitRetry`: no Error line, a Debug line; a real failure still logs Error). Mutation M27 caught |

**Mutations** (`go test -overlay`, worktree untouched; scripts in this session's scratchpad): M1–M36, every one caught by the test named above. M17/M18 first showed "not caught" because the criss-cross subtests' names carried `/` from file paths (go test splits `-run` on `/`); renamed, they are caught. M21 found a real gap (the binding-off check was only exercised with an operator that also lacked `MergeBases`), fixed in `48c99c6d`. M28–M36 cover the codex-round fixes.

### Deviations and design choices (with reasons)

1. **C-quoted paths are decoded, not always governed** (the design-check's alternative). TASK-003
   made Spine's `Diff` NUL-delimited, but an embedder's operator may still C-quote; a decodable
   path is matched decoded, an undecodable one is governed. No guarded entry can start with `"`,
   so decoding only adds governance. `domain.UnsafeRepositoryPath` paths (invalid UTF-8, control,
   U+2028/9, bidi) are governed too.
2. **Case folding uses Unicode simple folding** (smallest rune of each `SimpleFold` orbit, lower-cased),
   not `strings.ToLower`, which misses U+017F LONG S.
3. **`ExemptOwnRunEvidence` zero value is false**: the library's zero value must be today's
   behaviour, and a true zero value cannot be expressed on a bool; `spine serve` and the harness set
   it (OQ9 default "on").
4. **Only the guard's own decision creates a hold.** A custom `Policy` that denies with
   `governance_approval_required` while the guard sees nothing to approve gets a plain `403`
   refusal — otherwise it would create a hold `DecideGovernanceMerge` must refuse (`412`), breaking
   AC 29. `EvaluateGovernance` also treats a malformed (not only empty) bound SHA as unevaluable.
5. **The governance take-back restores exactly `BaseSHA`** (`takeBackGovernedMerge`), not
   TASK-005's `takeBackRefusedMerge`: the latter moves to a newer first parent of a refused merge,
   which for a fast-forward result is run-branch content.
6. **A post-merge `Diff` failure** is retried when it is a retryable git error and the take-back
   was clean (TASK-005's own rule for its checks); otherwise it is the permanent refusal.
7. **The marker is not written on an "already up to date" replay**: the attempt that made the
   merge wrote it before its push; a replay keeps it. Approve does not reset `merge_sha`/`base_sha`
   (a concurrent attempt's marker must not be lost); a stale marker for an unlanded merge is ignored
   by the projection's reachability check.
8. **The lifecycle exemption reads the edited file at `src.sha`** (what merges), not `src.bound`;
   they are equal for lifecycle runs (no status cascade on a `.yaml` task path).
9. **AC 9's validator path:** TASK-005's rule reports reserved keys at `steps[i].outcomes[j].commit`
   with the key in the message; the design-check note says this task adds no second rule.
10. **Store Source 2** has no `repository_merge_outcomes` join and excludes an empty/tombstoned
    `sha` (design-check note and AC 22 correction). Source 2 is not branch-scoped
    (KNOWN-LIMITATIONS 11).
11. **A provenance read failure is an infrastructure error, not a refusal**: the sync is held and
    retried with the rules kept (AC 20 lists it among the "refused with the rules kept" cases; this
    keeps the rules too and does not lose an authorised relaxation to a database blip, consistent
    with TASK-011 D2). Codex round 1 extended this to retryable git failures while checking a record.
12. **`FullRebuild` refusals end `idle`** (§5 note 3, D17), not `error` as AC 18's original sentence
    says; the design-check correction replaced only the IncrementalSync assertions.
13. **Projection wiring per workspace** is done in `cmd/spine`'s pooled builder
    (`workspaceOrchestratorBuilder`, which runs inside `buildServiceSet` before the ServiceSet is
    returned and before any sync), not in `service/internal/workspace/pool.go:955`: the switch then
    lives in one package. The engine guard's workflow directories come from the workspace
    projection (`ProjSync.WorkflowDirs()`); `buildOrchestrator` gained a `workflowDirs` parameter.
14. **The gateway finds the decider on the `RunMergeResolver`** (the orchestrator) by type
    assertion: no new `ServiceSet` or `ServerConfig` field.
15. **Override provenance is written synchronously in `spine serve`'s push path** (codex round 1):
    the design assumed the delivery subscriber's asynchronous, delivery-gated `event_log` write.
16. **Reject decides its route before writing**, and fails a no-publish run with its own
    compare-and-swap (TASK-008 converts `failRunOnMergeError` later); a publish-path reject that
    finds the run completed (or still committing) answers 409 without an event.
17. **A refused merge that cannot be taken back blocks every governed merge** (codex round 2, P1;
    also a failed marker write, round 3): in process for all runs and in reserved key
    `spine.governance.blocked_target` on the run, which stays committing. Restart residual in
    KNOWN-LIMITATIONS 6.
18. **With the guard on, a `FullRebuild` whose branch-protection step failed on infrastructure keeps
    the previous `last_synced_commit`** (codex round 3), preserving the newness anchor.
19. **Re-approving a commit whose approval is recorded retries the merge even when nothing is left
    to evaluate** (codex round 4): after an approved merge landed locally and its push failed.
20. **`GovernanceMergeStatus`** answers `ErrUnavailable` with the guard off and reports an
    `EvaluationError` (no git output) instead of failing when the facts cannot be computed.

### Codex review (`codex review --base main`, codex-cli 0.162.1, no `-c model=` override)

Every round's review sub-session (`session_meta.source = {"subagent":"review"}`) has
`"model":"gpt-6.1-sol"`.

| Round | Session | Findings | Disposition |
|---|---|---|---|
| 1 (`48c99c6d`) | `01a13bf1-f617-77e2-9692-53631b7d15da` | P2 override provenance arrives asynchronously, a refusal advances past it; P2 transient git errors in record verification become a refusal | Both real; fixed in `be372095` with tests |
| 2 (`be372095`) | `01a13bfa-1d71-7e82-b3e6-0a84ef2f620b` | P1 a failed take-back leaves the refused merge for the next merge to publish; P2 reject falls to the no-publish route on lookup failure; P2 decision event ids collide within a second | All real; fixed in `0972f1b7` with tests |
| 3 (`0972f1b7`) | `01a13c04-05ea-7611-a002-9be95a78c252` | P2 a failed marker write with a failed take-back leaves no block; P2 a `FullRebuild` infra failure moves the bootstrap anchor to head (reproduced) | Both real; fixed in `54f51f28` with tests |
| 4 (`54f51f28`) | `01a13ec2-27d6-7190-95bd-86f0b3c20ad3` | P2 re-approval after a locally landed merge whose push failed answers 412 | Real; fixed in `c3b9cdd3` with a test |
| 5 (`c3b9cdd3`, the final code) | `01a13ecb-d024-7d43-b0ca-e5502baa2719` | "No actionable defects were identified" | — |

No finding was rejected.

### Gates (final code `c3b9cdd3`; only this notes commit follows)

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 FAIL |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 11 touched non-scenario packages (`adapters/{delivery,git,projection,store}`, `core/{auth,branchprotect,domain,engine}`, `service/cmd/spine`, `service/internal/gateway`, `service/scenariotest/harness`) | all ok |
| `-tags integration -race -p 1 -count=1 -v` on `adapters/{delivery,projection,store}` and `service/cmd/spine` (fresh `s006_it2`; registry `s006_reg3` migrated from `migrations/registry`) | 898 PASS (subtests counted), 0 SKIP, 1 FAIL: `adapters/store` `TestRunModeDatabaseDefault` — the known pre-existing failure (identical on `main`, per the wave-3 notes) |
| CI `scenario-git-http` job, its `run:` steps verbatim (`w3final/ci_job.py`, fresh `s006_ci2`) | job rc 0: step 2 (`TestGitHTTP_*`) 87 PASS, step 3 (`TestBootstrapInternalAdmin_*`) 26, step 4 (`TestWorkflowLifecycle_*`) 57, step 5 (pooled workflow-service policy) 1; 0 FAIL, 0 SKIP |
| Full scenario suite `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (fresh `s006_scen2`, registry `s006_reg4` migrated) | 31 top-level FAIL — the **identical** sorted set of the known pre-existing failures (wave-3 final run's `scen-b.topfails`: the 30 + `TestWorkspace_ScopedServices`); 199 top-level PASS (wave-3 baseline 198 + the new `TestGovernancePathGuard_HeldUntilOperatorApproves`); 0 SKIP. An earlier run on `0972f1b7` gave the same result |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5; `--pull=never --rm --init`, non-root, worktree and module cache read-only at `/work/spine`, `GOPROXY=off`, `GOTOOLCHAIN=local`) plus this task's git-heavy tests | step 2: 73 PASS; step 3: 891 PASS (both `TestMergeBases` tests); `Governance\|WithGovernanceGuard` in `core/engine`: 98 PASS (criss-cross included); `ProtectionRemovalGuard` in `adapters/projection`: 27 PASS; 0 FAIL. (A first run with the worktree mounted at its host path is **not** counted: that Docker Desktop mount came up empty on this VM, which a `-v` re-check exposed.) Container removed |
| `golangci-lint` v2.11.4 `run --new-from-rev main` on every touched package, plain, `--build-tags=integration`, `--build-tags=scenario` (with `service/scenariotest/...`) | 0 issues each |
| `golangci-lint --enable-only=gosec`: `./...` plain; `./...` with `integration,scenario`; `--new-from-rev main` on `./core/... ./adapters/... ./service/...` | 0; 1 (G602 at `adapters/store/postgres_integration_test.go:1563`, an untouched file — pre-existing, same as the wave-3 notes); 0 |
| `gofmt -l` on every changed `.go` file | empty |
| Mutation runs M1–M36 (`go test -overlay`) | all caught (see Per-AC evidence) |
| Original PoCs by overlay (AC 27) | see AC 27 |

### SMP compatibility gate

`i23/s-T006/smp` = SMP `main` `1ae9b0e3` (detached, `.spine-revision` d23b897), `../spine` = this
worktree, `GOFLAGS=-mod=readonly`, no `vendor/`; never modified (`git status` clean after every
step).

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `bash dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 14 SMP packages importing a changed Spine package, test imports included (`go list`): `cmd/customer-app`, `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`, `internal/recovery` (on `54f51f28`) | 13 ok, `projectionsvc` has no tests. The wave-1/2 `gitread`/`projectionwriter` failures of earlier gates are gone on this SMP main |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s006_smp_it` with `vector`, `s006_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`), on `c3b9cdd3` | 1914 passed, 0 failed, 2 allowed skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/`, on `c3b9cdd3` | 301 passed, 0 failed, 1 allowed skip; `testgate: PASS` |
| The three Linux-only skips, in `golang:1.26-bookworm` (git 2.39.5, read-only mounts at `/work/s-T006/{smp,spine}` so `../spine` resolves) | `TestDiffChildMemory_MillionPaths` PASS, `TestReceiveTimeMemory_RealGitReaderFetch` PASS (maxrss 11352 KiB vs 265952 without the threshold), `TestIntegration_ReceiveTimeMemoryBounded` PASS (4 subtests) |

The round-4 change (`c3b9cdd3`) touched only `core/engine/governance_decision.go`; the SMP packages
that import `core/engine` (`spinecore`, `spineembedded`) ran on `c3b9cdd3` through the testgate
row; the other packages of the race row do not import `core/engine`. `ws_*` databases and roles:
0 before and 0 after.

### Environment

Shared PostgreSQL 18.6 (`i23-pg`, `localhost:55630`): only `s006_*` databases were created, and
all were dropped (checked: none left). Docker: only `golang:1.26-bookworm` (git-floor, SMP
Linux-only tests) and `alpine:3` (disk/mount checks) from present images, `--pull=never --rm`; no
container left; the VM kept about 5 GB free. Logs and mutation/overlay scripts are in this
session's scratchpad.

### Not run

- GitHub CI (needs a push).
- Image-based SMP gates (images, RDS emulation, `make dev-up` smoke) and the other SMP integration
  steps (platform-DB shared suites, fenced migrate, workspace-schema, faultinject, verify-children)
  — not in this task's gate list.
- A side-by-side scenario run on a `main` export: the branch's failure set was compared with the
  wave-3 final run's `main`-identical set (same 31 names) instead.

### Handoffs

- **SMP TASK-010** (adopts this task; `docs/integration-guide.md` §10): implement
  `engine.MergeBaseLister` on `RealGitOperator` (`git merge-base --all`, same pre-fetch as
  `MergeBase`) and make `Diff` accept two local object ids; call `WithGovernanceGuard` after
  `EnableMergeBinding` (exemption off, carve-out per SMP's evidence producer); add
  `MergeCommitMeta` to `spinecore.RunStore`; call `DecideGovernanceMerge` with the caller's Spine
  actor and `RoleAdmin` inside the git-session/write-gate wrapper; expose
  `GovernanceMergeStatus`; enable `WithProtectionRemovalGuard` in the projection writer and the
  provisioning rebuild with the runtime store as provenance (the pools need `SELECT` on
  `runtime.event_log` and `runtime.runs`); pin that the engine's own pushes never emit
  `branch_protection.override`. SMP has no commit-retry sweep: re-approval is its merge retry (now
  also after a push failure, codex round 4).
- **TASK-012:** its bounded merge-retry counter must not count `ErrGovernanceApprovalRequired`
  (nor `errGovernanceTargetBlocked`, which is an unavailable refusal); its worktree lock makes the
  governance take-back reliable (KNOWN-LIMITATIONS "Governance-Path Guard" 6).
- **TASK-008:** `rejectGovernanceMerge` already fails a no-publish run with a compare-and-swap; when
  `failRunOnMergeError` becomes one, the two can share it.
- **TASK-017:** the guard diffs the merged commit (`src.sha`), so any change to the bound cascade
  is evaluated automatically.
- **Owner:** sign off ADR-016 (OQ1) to move it to `Accepted`.

### Review round 1 (2026-10-16)

Three reviewers looked at `569ec83e`: correctness and compatibility, adversarial security, and
completeness and tests. Each finding was checked against the code before anything changed: the
races by reading the decision and merge paths and then reproducing them with fake stores and
hooks, rename following with real git at the default configuration, and the rest by reading the
code. Commits: `1148a742` (engine, store, git), `9e808929` (projection), `283e7f9f` (wiring
test), `0c842103` (docs, CI), `556fca14` (the codex round below), `b038cb3b` (lint). Still no
runtime schema change: `git diff --name-only main...HEAD -- migrations go.mod go.sum` is empty.

#### Findings and what was done

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P2 | An approve whose write lands after a concurrent reject keeps the approval it wrote | **FIXED.** `DecideGovernanceMerge` now holds a per-run governance stripe (see 5). The approval is written only while the run is still `committing`: `(*PostgresStore).MergeCommitMetaIfStatus`, an `UPDATE … WHERE status = $3` behind the new optional `commitMetaStatusMerger`. The fallback writes, re-reads, and writes the tombstone back if the run left `committing`. A reject now revokes a second time after its route (`revokeAgainAfterRoute`). The `governance.merge_decided` event is emitted as soon as the approval is recorded. Tests: `TestGovernanceDecision_LateApproveCannotOutliveAReject` (conditional write and fallback: 409, no approval, no approved event, the resubmit is held), `TestGovernanceDecision_RejectRevokesAgainAfterItsRoute` (an approval written between the revocation and the route does not survive; publish and no-publish), and the store integration case for `MergeCommitMetaIfStatus` |
| 2 | P2 | The lifecycle exemption's SoD check misses the run's starter, who wrote the body | **FIXED.** `startWorkflowPlanningRun` now records the context's actor in the reserved key `spine.governance.started_by` (`engine.CommitMetaGovernanceStartedBy`), only while the guard and the exemption are on. `reviewerIsNotAuthor` refuses in three cases: the reviewer started the run; no starter is recorded (runs from before the upgrade); or the reviewer is a run author by `runAuthor`, TASK-007's definition (any author execution in any status, plus assignment-ledger rows, which also covers the assignee-only `write_context` writes). Tests: `TestGovernanceGuard_LifecycleExemption` gains three cases (the reviewer started the run while another actor drafted; no starter; the reviewer held an earlier failed draft attempt), and `TestStartWorkflowPlanningRun_RecordsTheStarter` |
| 3 | P3 | Docs say the post-merge assertion fires only under non-default merge configuration, but rename following triggers it at the default configuration | **FIXED.** Corrected in ADR-016 §3 and the Alternatives, KNOWN-LIMITATIONS 7, the `governance_guard.go` comments, the CHANGELOG and engine-state-machine. The refusal message now names the remedy: update the run branch from `main` and resubmit. Test `TestGovernanceGuard_RenameFollowingIsRefusedWithARemedy` uses real git at the default configuration: the run is refused, `main` is restored, the outcome names the remedy, and after `git merge main` on the run branch the same change is **held** and an approval merges it |
| 4 | P3 | The consumption marker is written after `MergeBound` moves the local `main`, so a projection tick in that window refuses the approved relaxation | **FIXED** (done properly once codex round R1-1 showed that with bootstrap rows even a rebuild cannot recover). `ListGovernanceAuthorizations` also returns runs still `committing` with an approval (`GovernanceSourcePendingApproval`, never an authorisation). When a removal has no matching record, the approved commit is on the head and no reachable marker exists, the sync is **held** for a retry instead of refused (`approvedMergeInFlight`). Test: `TestProtectionRemovalGuard_ApprovedMergeInFlightHoldsTheSync` (protected and bootstrap rows; a stale marker keeps the hold; a pending entry vouches for nothing; an unrelated pending approval does not hold). The store integration test covers the pending rows. Residual: KNOWN-LIMITATIONS 13 |
| 5 | P2 | A reject that arrives during an approved attempt in flight returns success, and the change merges anyway | **FIXED.** `governanceRunStripes` is a package-level, run-striped, context-honouring semaphore, re-entrant through the context. `MergeRunBranch` holds it for the whole attempt while the guard is on (`lockGovernanceRunIfGuarded`; nothing is taken with the guard off), and so does `DecideGovernanceMerge`. A reject therefore waits for the attempt and then answers 409. A reject after the approved merge reached the local `main` is now 409 ("the merge won"), not 412. Tests: `TestGovernanceDecision_RejectWaitsForAnInFlightApprovedMerge` (the approve's own merge, and a scheduler retry carrying the approval) and `TestLockGovernanceRun` (re-entrant; exclusive; the waiter gives up with its context; idempotent release; a guard-off `MergeRunBranch` never waits). KNOWN-LIMITATIONS 9 is rewritten, including the cross-process residual |
| 6 | P2 | Same as 1 (`approveGovernanceMerge` at `:181`) | **FIXED** with 1 |
| 7 | P2 | A merge that can never be evaluated (no merge base, or a diff over the output limit) stays `committing` for good | **FIXED.** `unevaluableForever` covers `errGovernanceNoMergeBase` and `git.ErrOutputTooLarge`. `refuseUnevaluableMerge` takes the permanent route: the outcome is failed, then `merge_failed` or a failed run. A decision on such a run is 412 and the status reads "cannot be evaluated: …". `(*git.CLIClient).MergeBases` maps `git merge-base`'s exit 1 with no output to an empty list, and the `MergeBaseLister` doc gives the contract. Errors that may be transient still keep the run `committing`. Tests: `TestGovernanceGuard_UnevaluableMergeIsRefusedNotStuck` (both causes × publish and no publish) and `TestMergeBases_RefusesAndFailsClosed` (unrelated histories give an empty list) |
| 8 | P3 | Laundering: a record whose own diff only tightens still vouches for a relaxation already on `main` | **FIXED for approved merges**, recorded as a residual for override pushes. An approved-merge record now counts only if the config at its `BaseSHA` applied every kind being removed (`protectsAll`). Test: `TestProtectionRemovalGuard_RefusedCases/laundering:_an_approved_merge_whose_own_diff_only_adds_a_rule` (IncrementalSync and FullRebuild). Override pushes keep OQ8's changed-the-file rule, for two reasons. The operator authored the bytes being installed. And the documented re-affirmation, a comment-line edit in an override push (OQ8's stated cost), depends on that rule; the stricter rule would turn every re-affirmation into a two-push restore-then-relax. The residual is KNOWN-LIMITATIONS 14 and a git-push-guide warning |
| 9 | P3 | Same as 3 | **FIXED** with 3 |
| 10 | P2 | No test covers the process-level wiring of `spine serve` | **FIXED.** `TestBuildServerConfig_EnablesGovernanceGuards` drives `buildServerConfig` (`artifacts_dir: spine`, env unset and `off`). Overlay mutations were caught: dropping `enableProtectionRemovalGuard(projSync, …)` fails it, and so does passing `nil` for `workflowDirsFor(…)`. A compile-time assertion pins the two `PostgresStore` capabilities the engine type-asserts |
| 11 | P3 | event-schemas §4.17 decision id is stale | **FIXED.** The id is now `evt-<trace12>-governance-<approve\|reject>-<sha12>-<unixmilli>-<rand12>`, with "do not parse; correlate on run_id/bound_sha/decision" |
| 12 | P3 | The two DB-backed tests never run in CI | **FIXED.** `scenario-git-http` gains a must-pass, never-skip step for `^TestGovernancePathGuard_` (scenario) and `^TestGovernanceStore_Integration$` (integration). It was run verbatim (gates below) |
| 13 | P3 | AC 26 evidence omits `internal/customer/docindex/...` and `internal/docmemory/...` | **FIXED.** Run this round, both with and without the platform DB (SMP gate below) |

**Acceptance-criteria gaps.**

- **AC 1** stays partial: ADR-016 is `Proposed` until the owner signs off OQ1. This is owner-gated and cannot be fixed in code.
- **AC 9**, **AC 18** and **AC 16**: the deviations already recorded (9, 12, and the AC 16 reading) still stand.
- **AC 13, 29, 30 and 31**: done (findings 2, 7, 10 and 1/5).
- **AC 26**: evidence completed (finding 13).
- **ADR number**: still free, no gap.
- **AC 11**: the AC's literal "no merge base → ErrUnavailable" is now the permanent route (Deviation 22).

#### Codex (`codex review --base main`, codex-cli 0.162.1, no model override)

| Round | Code | Review sub-session (`source: {"subagent":"review"}`, `model: gpt-6.1-sol`) | Findings | Disposition |
|---|---|---|---|---|
| R1-1 | `0c842103` | `01a14403-3f1b-77a2-b4d3-abd94dc16498` | P2: the lifecycle exemption ignores the target's version, so a clean merge with a `main` that has since made the file govern workflows lands unapproved. P2: with bootstrap rows, a refusal inside the marker window moves the newness anchor past the approved merge, so even a rebuild cannot install it | Both real, fixed in `556fca14`. 1: the gate also reads the target tip, and the post-merge assertion re-checks the merged result (`lifecycleResultStillExempt`); test `TestGovernanceGuard_LifecycleExemptionFollowsTheTarget` covers main moving before the gate (held) and between the gate and the merge (refused). 2: the in-flight hold (finding 4) |
| R1-2 | `556fca14` | `01a1440e-8196-7c30-a84f-02c57bac30c7` | "No actionable regressions were identified" | — |
| R1-3 | `b038cb3b` (the final code) | `01a14432-7e7d-74e2-9523-e5504b24e4b6` | "No actionable regressions were identified" | — |

No codex finding was rejected.

Every new behaviour was mutation-checked by `go test -overlay` (worktree untouched), and every mutant was caught. The mutants:

- **Stripe and approval write:** no stripe in `MergeRunBranch`; no stripe in `DecideGovernanceMerge`; an unconditional approval write; no second revocation.
- **Unevaluable merges:** no unevaluable branch; no `MergeBases` exit-1 mapping.
- **Lifecycle exemption:** no starter check; no `runAuthor`; no starter recording; no target-tip read; no post-merge lifecycle re-check.
- **Decisions:** reject-after-landed back to 412.
- **Projection:** no `protectsAll`; no in-flight hold; pending entries counted as provenance.
- **Wiring:** both call sites in `serve_wiring.go`.

#### Deviations added this round

21. **Decisions are serialised with merges in process only**, following the engine's existing stripe pattern (`sodBindStripes`, `recoveryBindingStripes`). Across processes, the conditional approval write plus the second revocation still stop a late approval. But a reject served by another process while an approved merge is in flight is not ordered with it; that would need a database lock per run. This is KNOWN-LIMITATIONS 9, and the integration guide tells embedders with replicas to serialise the two themselves.
22. **No merge base and an oversized diff are permanent refusals**, not `ErrUnavailable` as AC 11 lists. Retrying cannot change them, and a retry left the run undecidable, which breaks AC 29. The `MergeBaseLister` contract now says unrelated histories are an empty list.
23. **A reject after the approved merge reached the local `main` is 409, not 412.** Nothing is left to evaluate there because the merge won, which is what KNOWN-LIMITATIONS 9 says.
24. **Only approved-merge records must have removed the protection themselves** (finding 8). Override pushes keep OQ8's rule because of the documented re-affirmation.
25. **An unmatched removal while an approved merge is in flight holds the sync** (infrastructure retry) instead of being refused (finding 4, refining OQ7). Otherwise the approved change would be lost for good, which matters most with bootstrap rows. Residual: KNOWN-LIMITATIONS 13.
26. **The starter is recorded only while the guard and the exemption are on**, so the zero value writes nothing. A lifecycle run started before the upgrade, or with no actor on the context, gets no exemption and needs an operator approval.
27. **When the lifecycle exemption fails its post-merge re-check, the merge is refused permanently.** This happens only if the target moved between the gate and the merge. It is not a hold, because the post-merge path never holds (OQ6). The resubmit is then held, since the gate reads the moved target.

#### Gates (final code `b038cb3b`; only this notes commit follows)

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." (on `b038cb3b`) |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 FAIL (on `b038cb3b`) |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean (on `b038cb3b`) |
| `go test -race -p 4 -count=1` on the 11 touched non-scenario packages | all ok (on `556fca14`); `adapters/projection` re-run with `-race` on `b038cb3b`: ok |
| `-tags integration -race -p 1 -count=1 -v` on `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/gateway` (fresh `s006_r1it2`, registry `s006_r1reg2` migrated from `migrations/registry`), on `556fca14` | 1944 PASS, 0 SKIP, 1 FAIL: `TestRunModeDatabaseDefault`, the known pre-existing failure. An earlier run that also included `adapters/git` had one more failure, `adapters/delivery` `TestQueuePG_CircuitOpenDoesNotStarve` (TASK-010's breaker test, timing under load); standalone it passed `-count=3` |
| CI `scenario-git-http`, its `run:` steps verbatim (`w3final/ci_job.py` on this worktree's `ci.yml`, fresh `s006_r1ci`), on `b038cb3b` | job rc 0. Step 2 (`TestGitHTTP_*`) 87 PASS, step 3 26, step 4 57, step 5 1, **new step 6** 19 (scenario) + 1 (store integration); 0 FAIL, 0 SKIP |
| Full scenario suite `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (fresh `s006_r1scen`, registry `s006_r1sreg`), on `556fca14` | 31 top-level FAIL, `diff`-**identical** to the wave-3 baseline `scen-b.topfails`; 199 top-level PASS, including `TestGovernancePathGuard_HeldUntilOperatorApproves`; 0 SKIP |
| CI `git-floor` in `golang:1.26-bookworm` (git 2.39.5; `--pull=never --rm --init`, non-root, worktree read-only at `/work/spine`, `GOPROXY=off`) plus this task's git tests, on `b038cb3b` | step 2: 73 PASS; step 3: 891 PASS (both `TestMergeBases` tests); `Governance\|WithGovernanceGuard\|LockGovernanceRun\|RecordsTheStarter` in `core/engine`: 124 PASS (a `-v` re-check lists `RenameFollowing…`, `LifecycleExemptionFollowsTheTarget`, `CrissCross…` and `LifecycleExemption` as PASS); `ProtectionRemovalGuard` in `adapters/projection`: 33 PASS; 0 FAIL; containers removed |
| `golangci-lint` v2.11.4 `run --new-from-rev main` on the 11 touched packages, plain and `--build-tags=integration`, and with `service/scenariotest/...` under `--build-tags=scenario` | 0 issues each, after `b038cb3b` (one `gocritic rangeValCopy` the new `MergeSHA` field caused) |
| `golangci-lint --enable-only=gosec`: `./...` plain; `./...` with `integration,scenario`; `--new-from-rev main` on `./core/... ./adapters/... ./service/...` | 0; 1, the pre-existing G602 at `adapters/store/postgres_integration_test.go:1563` (untouched); 0 |
| `gofmt -l` (toolchain gofmt) on every changed `.go` file | empty |

#### SMP compatibility gate (this round, on `b038cb3b`)

`i23/s-T006/smp` = SMP `main` `1ae9b0e3` (detached, `.spine-revision` d23b897), `../spine` = this
worktree, `GOFLAGS=-mod=readonly`. It was never modified: `git status` was clean afterwards.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `bash dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 14 SMP packages whose imports or test imports reach a changed Spine package (computed again with `go list`; the same 14) | 13 ok; `projectionsvc` has no tests |
| `go test -race -count=1 -v ./internal/customer/docindex/... ./internal/docmemory/...` (finding 13), without a database, then with `TEST_PLATFORM_DB` | ok, ok: 81 PASS, 14 SKIP (the docmemory repository tests skip without a database); with the database, 95 PASS, 0 SKIP |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s006_r1smp_it` with `vector`, `s006_r1spine_rt`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites", `go run ./dev/testgate … ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` | 1914 passed, 0 failed, 2 allowed skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests", `go run ./dev/testgate … -tags integration ./internal/customer/githttp/` | 301 passed, 0 failed, 1 allowed skip; `testgate: PASS` |
| The three Linux-only skips in `golang:1.26-bookworm` (read-only mounts at `/work/s-T006/{smp,spine}`) | `TestDiffChildMemory_MillionPaths` PASS (peak 4408 KiB), `TestReceiveTimeMemory_RealGitReaderFetch` PASS (11352 KiB vs 265956 without the threshold), `TestIntegration_ReceiveTimeMemoryBounded` PASS (4 subtests) |

`ws_*` databases and roles: 0 before and 0 after. The SMP zero value is unchanged:

- The stripe is taken only with the guard on.
- `recordLifecycleStarter` writes only with the guard and the exemption on.
- `MergeCommitMetaIfStatus` is an optional store capability.
- The `MergeBases` change is reachable only through the guard.
- The projection changes run only inside `guardProtectionRemoval`, which is nil at the zero value.

#### Environment

The shared PostgreSQL holds only the `s006_r1*` databases listed above, and all of them were
dropped (`s006%` count: 0). Docker ran only `golang:1.26-bookworm` from the present image
(`--pull=never --rm`), and no container is left. Logs, overlay mutants and job scripts are in
this session's scratchpad (`t006r1/`).

#### Handoff changes

- **SMP TASK-010.** Four additions to the earlier handoff:
  - `RealGitOperator.MergeBases` must return an empty list, not an error, for unrelated histories.
  - Add `MergeCommitMetaIfStatus` to `spinecore.RunStore` next to `MergeCommitMeta`.
  - If customer-app runs replicas against one workspace database, serialise `DecideGovernanceMerge` with `MergeRunBranch` per run across them (for example with an advisory lock). The engine's stripe is in-process only.
  - The provenance reader now also reads committing runs (no new grant: the same `SELECT` on `runtime.runs`).
- **TASK-012.** Its worktree lock and bounded retry counter must also treat the governance stripe's `ErrUnavailable` (a waiter that gave up) as "try again".

### Review round 2 (2026-10-16)

A second-round reviewer looked at `5086ade5`. It raised one finding (P3) and six gaps. Each was
checked against the code before anything changed:

- **The finding.** I ported the reviewer's PoC into the real-git `pgFix` fixture on `5086ade5`.
  It reproduced: all three new accepted cases failed there, and they pass on the round-2 code.
- **The gaps.** I read the tests and the AC text.

Commits:

- `1edaf6fa`: projection.
- `71a8d92b`: engine test.
- `f49156d1`: docs.
- `469a4ef1`: the in-flight composition test.
- This notes commit.

Still no runtime schema change: `git diff --name-only main...HEAD -- migrations go.mod go.sum` is
empty.

#### Finding and what was done

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P3 | Round 1's finding-8 fix (`9e808929`) counted an approved-merge record only when its base config applied every kind in `removed`. But `removed` is installed minus candidate, not the state just before that record. Two authorised relaxations can land before one projection tick: two approvals inside one poll, a second merge during the in-flight hold, or a sync held for infrastructure. Then the newest record's base no longer applies the kind the earlier act removed. The earlier record's bytes differ from the candidate, so it cannot match either. Both were refused, and for good: the installed rows keep their old source commit, so even a `FullRebuild` could not recover | **FIXED, with a stricter rule than suggested** (Deviation 28). The removal is now accounted for kind by kind along a **chain of custody** (`provenanceCheck.custody` in `adapters/projection/protection_guard.go`). An override record accounts for every kind its bytes lack (OQ8, unchanged). An approved merge accounts for the kinds its own base applied. A kind already missing at its base is accounted for only when that base config is, **byte for byte**, the result of another valid record whose commit the base **descends from**: the record is reachable, newer than the installed rows, and changed the config, and it is judged the same way in turn. The newest-record content match is kept. Each record's own checks run once, and a record's base is read only when its content is wanted, as before. The memo of failed walks is exact under consistent history, and can only refuse more otherwise. Tests: see below |

**Why not the suggested per-kind union.** The suggestion was to accept when every kind in `removed`
was dropped by *some* valid record's own diff in the window. Consider one window where:

1. approved r1 drops `no-delete`;
2. approved r2 restores it;
3. an unauthorised write drops it again, with other bytes;
4. approved r3 adds only a `release/*` rule on top.

The union accepts this, because r1 dropped `no-delete`, and so it launders the unauthorised write
in step 3. Matching intermediate states by kinds instead of bytes accepts it too. The chain refuses
it: r3's base is not the result of any record. Ancestry matters as well. Without it, bytes an
operator authorised on a side line, joined later with `merge -s ours`, would vouch for the same
bytes written to `main` without an operator.

**New tests** (in `adapters/projection/protection_guard_git_test.go`):

- `TestProtectionRemovalGuard_AcceptedCases`:
  - `two approved relaxations in one sync window`;
  - `an override push then an approved merge in one sync window`;
  - `the earlier record arriving late is installed by a FullRebuild`.
- `TestProtectionRemovalGuard_RefusedCases`:
  - `laundering: an unauthorised relaxation between authorised acts`;
  - `laundering: authorised bytes from a line the approved base does not descend from`.

  The existing round-1 laundering case is kept.
- `TestProtectionRemovalGuard_RecordVerificationErrors/transient on an earlier link of the chain`:
  the sync is held and retried, not refused.
- `TestProtectionRemovalGuard_ApprovedMergeInFlightHoldsTheSync/a second approved merge lands
  while the first is in flight`: the first tick is held, and the next tick installs through r1's
  record.
- `TestProtectionRemovalGuard_ApprovedMergeWithoutABase`: the defensive created-base branch, which
  had no test since round 1.

The five accepted-path tests fail on the round-1 guard (overlay of `5086ade5`'s
`protection_guard.go`).

**Mutants** (`go test -overlay`, worktree untouched; all caught):

| Mutant | Change | Caught by |
|---|---|---|
| R2-M1 | no chain step | the three accepted cases and the in-flight composition |
| R2-M2 | no ancestry check on a link | the side-line laundering case |
| R2-M3 | links matched by kinds, not bytes | the between-acts laundering case |
| R2-M4 | the reviewer's per-kind union | both new laundering cases |
| R2-M5 | the round-1 rule dropped (an approved diff accounts for everything) | the round-1 laundering case, both new laundering cases, the late-record rebuild case and the chain-link transient case |
| R2-M6 | no `noteErr` on a link | both transient cases |
| R2-M7 | a created base accepted outright | `ApprovedMergeWithoutABase` |
| R2-M8 | `recordLifecycleStarter` skipped on the update path | the new update rows (it survived the old test) |

**Docs.** The rule is now described in these places:

- ADR-016 §6 and a new §8 gap;
- security-model §7.3;
- the CHANGELOG;
- integration-guide §10, including a note to keep `Lookback` above the number of operator acts one
  window can see;
- git-push-guide §4;
- KNOWN-LIMITATIONS 14 and a new **15**.

KNOWN-LIMITATIONS 15 records two residuals:

- any unauthorised write to the file between two authorised acts, even a comment-only edit, breaks
  the chain and is refused (an editing override push recovers);
- newness is judged against the installed rules, not against later acts in the same window. So a
  byte-exact restore of an earlier act's result, after a tightening in the same window, is
  installed on that act's record.

The second residual predates this round: it exists for the single-record match too. Closing it
needs the file's commit log, which means a new `git.GitClient` method, the alternative ADR-016
already rejects.

#### Gaps

| Gap | Disposition |
|---|---|
| AC 1 (ADR-016 `Proposed`) | **Owner-gated, unchanged.** "Accepted before the code merges" needs the owner's sign-off on OQ1. ADR-016 is still the next free number: Spine `main` ends at ADR-015, and no sibling worktree (`s-T008`, `s-T012`, `s-T017`) has ADR-016…019 |
| AC 11 (literal "no merge base → `ErrUnavailable`") | **Explained; owner ruling or AC amendment needed** (open question). Deviation 22 stands. The AC's own wording still holds for every error that retrying can change: `TestGovernanceGuard_FailsClosed` covers a `MergeBases` error, a malformed base and a pre-merge `Diff` error. Only no merge base and `git.ErrOutputTooLarge` take the permanent route, because a retry left the run undecidable, which breaks AC 29. The Per-AC row still listed "no merge base" under `FailsClosed` after round 1 moved it; that row is corrected |
| AC 18 (FullRebuild "`sync_state=error`") | **Explained; AC text amendment for the owner.** Deviation 12 stands. The sentence predates OQ7 and D17 ("a refusal is not a sync error"), which the orchestrator's binding OQ7 ruling restates. The design-check correction replaced only the IncrementalSync half. A `FullRebuild` that ended `error` on a refusal would contradict OQ7 |
| AC 9 (validator path) | **Explained, unchanged** (Deviation 9). The design-check note in §3.7 (overlap with TASK-005) takes precedence: this task adds no second validator rule. TASK-005's rule reports reserved keys at `steps[i].outcomes[j].commit` and names the key in the message. Reporting at the AC's literal path would mean changing TASK-005's merged rule, whose paths SMP surfaces |
| AC 13 / finding 2 (update path untested) | **FIXED** (`71a8d92b`). The `update` rows now run `StartWorkflowUpdatePlanningRun` against an existing definition and assert that the writer's update path ran. Mutant R2-M8 survived the old test and is caught now. The update and create paths still share the `onBranch` closure |
| AC 19/20 (two relaxations in one window) | **FIXED** (finding 1's tests above). Per-AC rows 19 and 20 are updated |

#### Deviations added this round

28. **A protection removal is judged along a chain of byte-identical, history-ordered authorised
    states**, not by the reviewer's per-kind union, which launders an unauthorised write between
    authorised acts (finding 1). The cost is that any unauthorised edit of the file between two
    authorised acts in one window, even a comment-only edit, is refused until an editing override
    push re-affirms it (KNOWN-LIMITATIONS 15).

#### Codex (`codex review --base main`, codex-cli 0.162.1, no model override)

| Round | Code | Review sub-session (`source: {"subagent":"review"}`, `model: gpt-6.1-sol`, `cwd` this worktree) | Findings | Disposition |
|---|---|---|---|---|
| R2-1 | `f49156d1` | `01a1445d-33ba-7531-8b08-bef2b27d52bb` | "No actionable regressions were identified" | — |
| R2-2 | `469a4ef1` (the final code) | `01a14470-3889-7d32-8f61-feff0cc824bf` | "No actionable regressions were identified" | — |

No codex finding was rejected.

#### Gates (final code `469a4ef1`; only this notes commit follows)

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." (on `f49156d1`; `469a4ef1` adds one test only) |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 FAIL (on `f49156d1`); `adapters/projection` again on `469a4ef1` (below) |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean (on `f49156d1`); `go vet` plain, `-tags integration` and `-tags scenario` on `adapters/projection` and `core/engine` before each commit |
| `go test -race -p 4 -count=1` on the 11 touched non-scenario packages (`adapters/{delivery,git,projection,store}`, `core/{auth,branchprotect,domain,engine}`, `service/cmd/spine`, `service/internal/gateway`, `service/scenariotest/harness`), on `469a4ef1` | all ok |
| `-tags integration -race -p 1 -count=1 -v` on `adapters/{delivery,projection,store,git}`, `service/cmd/spine`, `service/internal/gateway` (fresh `s006_r2it`, registry `s006_r2reg` migrated from `migrations/registry`), on `469a4ef1` | 2236 PASS, 1 SKIP (`TestT003_GitMemChild`, a child-process helper that skips when not spawned; also skipped in round 1's run with `adapters/git`), 1 FAIL: `adapters/store` `TestRunModeDatabaseDefault`, the known pre-existing failure |
| CI `scenario-git-http`, its `run:` steps verbatim (`w3final/ci_job.py` on this worktree's `ci.yml`, fresh `s006_r2ci`), on `469a4ef1` | job rc 0. Step 2 (`TestGitHTTP_*`) 87 PASS, step 3 26, step 4 57, step 5 1, step 6 20 (19 scenario + 1 store integration); 0 FAIL, 0 SKIP |
| Full scenario suite `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (fresh `s006_r2scen`, registry `s006_r2sreg`), on `469a4ef1` | 31 top-level FAIL, `diff`-**identical** to the wave-3 baseline `scen-b.topfails`; 199 top-level PASS, including `TestGovernancePathGuard_HeldUntilOperatorApproves`; 0 SKIP |
| CI `git-floor` in `golang:1.26-bookworm` (git 2.39.5; `--pull=never --rm --init`, non-root, worktree read-only at `/work/spine`, `GOPROXY=off`) plus this task's git tests, on `469a4ef1` | step 2: 73 PASS; step 3: 891 PASS (both `TestMergeBases` tests); `Governance\|WithGovernanceGuard\|LockGovernanceRun\|RecordsTheStarter` in `core/engine`: 128 PASS (124 in round 1, plus the four update rows); `ProtectionRemovalGuard` in `adapters/projection`: 41 PASS (33 in round 1, plus the eight new cases, `merge -s ours` included); 0 FAIL; container removed |
| `golangci-lint` v2.11.4 `run --new-from-rev main` on the 11 touched packages, plain and `--build-tags=integration`, and with `service/scenariotest/...` under `--build-tags=scenario` | 0 issues each (on `469a4ef1`) |
| `golangci-lint --enable-only=gosec`: `./...` plain; `./...` with `integration,scenario`; `--new-from-rev main` on `./core/... ./adapters/... ./service/...` | 0; 1, the pre-existing G602 at `adapters/store/postgres_integration_test.go:1563` (untouched); 0 |
| `gofmt -l` (toolchain gofmt) on every changed `.go` file of `main...HEAD` | empty |
| Mutants R2-M1…R2-M8 (above) | all caught |

#### SMP compatibility gate (this round, on `469a4ef1`)

`i23/s-T006/smp` = SMP `main` `1ae9b0e3` (detached, `.spine-revision` d23b897), `../spine` = this
worktree, `GOFLAGS=-mod=readonly`. It was never modified: `git status` was clean afterwards.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `bash dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 14 SMP packages whose imports or test imports reach a changed Spine package (computed again with `go list`; the same 14) | 13 ok; `projectionsvc` has no tests |
| `go test -race -count=1 -v ./internal/customer/docindex/... ./internal/docmemory/...` without a database, then with `TEST_PLATFORM_DB` | ok, ok: 81 PASS, 14 SKIP; with the database 95 PASS, 0 SKIP |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s006_r2smp_it` with `vector`, `s006_r2spine_rt`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites", `go run ./dev/testgate … ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` | 1914 passed, 0 failed, 2 allowed skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests", `go run ./dev/testgate … -tags integration ./internal/customer/githttp/` | 301 passed, 0 failed, 1 allowed skip; `testgate: PASS` |
| The three Linux-only skips in `golang:1.26-bookworm` (git 2.39.5; read-only mounts at `/work/s-T006/{smp,spine}`) | `TestDiffChildMemory_MillionPaths` PASS (peak 4388 KiB), `TestReceiveTimeMemory_RealGitReaderFetch` PASS (11356 KiB vs 266020 without the threshold), `TestIntegration_ReceiveTimeMemoryBounded` PASS (index-pack and unpack-objects, plain and delta) |

`ws_*` databases and roles: 0 before and 0 after. The SMP zero value is unchanged: every change
this round is inside `guardProtectionRemoval`, which is nil at the zero value, or in tests and
docs.

#### Environment

The shared PostgreSQL held only the `s006_r2*` databases listed above. All of them were dropped
(`s006%` count: 0). Docker ran only `golang:1.26-bookworm` from the present image (`--pull=never
--rm`), and no container is left. Logs, overlay mutants and job scripts are in this session's
scratchpad (`t006r2/`).

#### Handoff changes

None to the API. SMP TASK-010 adopts the same `ProtectionRemovalGuard`. The chain walks only the
records its `GovernanceProvenanceReader` returns. With the store's reader nothing changes, but
`Lookback` should stay above the number of operator acts one sync window can see
(integration-guide §10).

### Integration (wave 4 merge) (2026-10-19)

The four wave 4 branches were merged with `git merge --no-ff` onto `init/INIT-023-wave4-integration`
(worktree `i23/s-int4/spine`, from Spine local `main` `ae513dca`), in this order: TASK-012
(`3872f4d6`), TASK-008 (`1fef1444`), TASK-017 (`2cbfe037`), TASK-006 (`0b9a4f22`). The semantic
conflicts between them were then fixed in coordination commits, each code fix with a test that
fails without it: `3ea7d88f` (engine), `4dbf0486` (serve wiring test and docs), `96cf8e40` (docs),
`044c2103` (lint, test code) and `a6acc720` (codex round 2). The final code is `a6acc720`; the
commit that adds this note changes only the four task artifacts. Nothing was pushed. Spine
`main`, the task branches and the SMP checkout are unchanged. The owner questions of all four
tasks stay recorded as defaults, pending the owner; none was re-decided.

**This task.** It merged last, with conflicts in `ci.yml`, `CHANGELOG.md`,
`core/domain/step.go`, `core/engine/{merge,merge_binding,orchestrator}.go`,
`docs/integration-guide.md` (now §12), `service/cmd/spine/{serve_builders,serve_resolver}.go`
and the scenario harness (`environment.go`, `runtime.go`). Both sides were kept:

- the wiring sets binding, then the lifecycle, then this guard;
- the commit retry is `governanceAwareCommitRetry` with TASK-012's
  `defaultCommitMaxRetries` and exhaustion handler;
- the harness has both options.

What changed for this task:

- **The post-merge assertion and the consumption marker run inside TASK-012's merge block,
  before the push.** git's auto-merge had placed the assertion after the block, that is after the
  push. Four of this task's tests failed on that state:
  `TestGovernanceGuard_FailsClosed` (post-merge diff errors), `…ConsumptionMarker`,
  `…FailedTakeBackBlocksMerges` and `…MarkerFailureWithFailedTakeBackBlocks`. The merge
  `0b9a4f22` split `assertGovernedMerge` into two halves:
  - `checkGovernedMerge`, in the block: the git reads, the marker write and the take-back;
  - `settleGovernedRefusal`, after it: the block record, the outcome, the event and the route.
- **The take-back.** `takeBackGovernedMerge` is now `takeBackToBase`, shared with TASK-017's
  V1. It goes through `git.CLIClient`'s armed, tree-locked `ResetBranch` on the shared tree
  (`TestWave4_LateRefusalsAreTakenBackUnderTheTreeLock`).
- **One story for a failed take-back** (KNOWN-LIMITATIONS "Governance-Path Guard" 6, rewritten).
  - With the guard on, a refusal of TASK-005's structural check or of V1 whose take-back failed
    now records this task's block as well (`blockGovernedMergesOn`). Every governed merge is then
    refused as unavailable before it merges.
  - This sits on top of TASK-012's pending take-back on the shared tree. Whichever completes the
    take-back clears both records.
  - See `TestWave4_FailedTakeBackBlocksGovernedMergesWhicheverCheckRefused`. With the guard off
    nothing changes.
- **Retry budget.** A held run and a blocked run are never abandoned by TASK-012's budget, and
  this task's "unavailable" refusals are not counted, as the handoff asked.
- **Compare-and-swap.** The hold record and its clearing go through TASK-008's step
  compare-and-swap (see TASK-008's note).
- **The decision API mirrors the status lifecycle.** With the lifecycle on, `governanceCheckForRun`
  computes the facts from the lifecycle's merge source, as `resolveMergeSource` does.
  - Before, a held standard-run merge that added a Draft task answered 412, "nothing to
    approve". TASK-005's cascade commit differed from the lifecycle's (the lifecycle does not
    stamp a standard run's additions).
  - A merge the lifecycle now refuses is `ErrPrecondition` (`errLifecycleRefusesMerge`), and
    `GovernanceMergeStatus` reports it as an `EvaluationError`.
  - See `TestWave4_GovernanceDecisionMirrorsTheLifecycle`; the mutant is killed.
- **Codex round 2.** `git.CLIClient.MergeBases` ignored TASK-012's §2.9 `HEAD` rewrite, so on a
  home-branch tree its answer depended on the checkout. It now resolves `HEAD` like `MergeBase`
  (`a6acc720`, `TestWave4_MergeBasesResolvesHeadToTheHomeBranch`).
- **Convergence is no longer a path around the guard** (F8 closed by TASK-012). ADR-016's
  consequence and KNOWN-LIMITATIONS 5 now say so. ADR-016 stays `Proposed`.
- **Integration guide.** The section is now §12; the references to "integration-guide §10" above
  mean that section.

**Coordination commits.**

| Commit | What it fixes | Tests |
|---|---|---|
| `3ea7d88f` | V1 is taken back to exactly `BaseSHA`; a failed take-back of a TASK-005 or V1 refusal records the governance block (guard on); the retry budget spares held and blocked runs; the lifecycle's transient exit is a counted attempt; the governance hold and its clearing are compare-and-swap; the decision API mirrors the lifecycle | `core/engine/wave4_integration_test.go` (9 tests), `wave4_integration_git_test.go` (4 real-git cases). Every fix has a mutant killed under `go test -overlay`: M1–M7, M3b, and M8/M9 (governance check, V1 moved out of the block). The overlays and logs are in `w4/agents/int-spine/mut/` |
| `4dbf0486` | Serve wiring composition; docs: the order of checks (`engine-state-machine.md`), the merge block (`git-integration.md` §6.6), KNOWN-LIMITATIONS, integration guide §11/§12, CHANGELOG "wave 4 integration" | `service/cmd/spine/serve_wave4_wiring_test.go`: both builders have binding, lifecycle, guard, home-branch primary client (a `MergeBaseLister`), retry budget and handler, governance-aware retry, projection guard |
| `96cf8e40` | ADR-016 and KNOWN-LIMITATIONS: convergence is no longer a path around the guard | docs only |
| `044c2103` | gocritic and staticcheck on the new test | test code only |
| `a6acc720` | Codex round 2 P2: `MergeBases` resolves `HEAD` to the home branch | `adapters/git/wave4_mergebases_test.go` (mutant M10 killed) |

**Gates.** The code gates ran on `a6acc720` unless the row names `96cf8e40`. The only code
change after `96cf8e40` is `a6acc720`'s `HEAD` rewrite in `MergeBases`, which no engine or
scenario caller reaches (they pass `main` or object ids). Logs are in
`spine-management-platform/tmp/init027-orchestration/w4/agents/int-spine/`.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean, all three |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 failures |
| `go test -race -p 4 -count=1` on the 19 packages changed since `ae513dca` (`service/scenariotest/scenarios` builds only with `-tags scenario`; the scenario rows cover it) | 19 ok, 0 data races (`adapters/{delivery,git,projection,scheduler,store}`, `core/{actor,artifact,auth,branchprotect,divergence,domain,engine,validation,workflow,worktreefs}`, `service/cmd/spine`, `service/internal/{gateway,workspace}`, `service/scenariotest/harness`) |
| `-tags integration -p 1 -count=1 -v` on the changed packages with integration suites: `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace`. `SPINE_TEST_DATABASE_URL` = fresh `s4i_it`, `SPINE_REGISTRY_TEST_DATABASE_URL` = `s4i_reg`, migrated from `migrations/registry` | 584 top-level PASS (1161 with subtests), 0 SKIP, 1 FAIL. The failure is `adapters/store` `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, column `workflow_version_label`), the known pre-existing failure: a `git archive` export of `main` `ae513dca` fails it the same way. Run at `96cf8e40` and again at `a6acc720`, with the same result |
| Full scenario suite (`-tags scenario -p 1 -count=1 -v ./service/scenariotest/...`), branch at `96cf8e40` against a `git archive` export of `main` `ae513dca`; fresh databases each, registry migrated | 31 top-level `--- FAIL` on both, and the sorted lists are identical, at every level. They are the known pre-existing set: the 30 plus `TestWorkspace_ScopedServices`, the same list as wave 3. Top-level PASS is 204 on the branch and 198 on `main`. The extra 6 are the wave's scenarios: `TestCompletedStep_IsNotReRouted`, `TestStepOutputEvidence_RecordedAndReadBack`, `TestGovernancePathGuard_HeldUntilOperatorApproves` and the three `StatusLifecycle` scenarios. 0 SKIP |
| CI `scenario-git-http`: its 7 `run:` steps, read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, `SPINE_TEST_DATABASE_URL` = fresh `s4i_ci`) | All 7 steps exit 0. `TestGitHTTP_*`: 20 PASS. `TestBootstrapInternalAdmin_*`: 2. `TestWorkflowLifecycle_*`: 4. Pooled workflow-service branch protection: 1. TASK-008's store compare-and-swap: 8, and its scenarios: 2. TASK-017's `StatusLifecycle` scenarios: 3, and its pooled validator: 1. TASK-006's `TestGovernancePathGuard_HeldUntilOperatorApproves`: 1, and `TestGovernanceStore_Integration`: 1. 0 SKIP and 0 FAIL in every step |
| CI `git-floor`: its 3 `run:` steps in `golang:1.26-bookworm` (`--pull=never --rm --init`, non-root, read-only mounts, `GOPROXY=off`, no git identity); plus an extra run of `core/engine` `-run 'TestWave4_\|TestStatusLifecycle_\|TestGovernanceGuard_\|TestF9_\|TestMergeRetry_'` | git 2.39.5. All 3 steps ok: `adapters/git/...` and `core/engine` (ref-grammar filter), then `adapters/git/...`, `core/artifact` and `core/workflow`. The extra `core/engine` run is ok on git 2.39.5 too, both at `a6acc720` |
| `golangci-lint` v2.11.4 `run --new-from-rev ae513dca` on the changed packages, plain, `--build-tags integration` and `--build-tags scenario` | 0 issues each. The two issues in the new test at `96cf8e40` were fixed in `044c2103` |
| `golangci-lint --enable-only=gosec`: `--new-from-rev ae513dca ./...`, full `./...`, and full `--build-tags integration,scenario` | 0; 0; 1. The one is G602 at `adapters/store/postgres_integration_test.go:1563`, a file unchanged since `main` (wave 3 recorded the same finding) |
| `gofmt -l` on the 167 `.go` files changed since `ae513dca` | only `core/divergence/convergence.go`: the import-order problem it has on `main` `ae513dca`, covered by TASK-012's AC 23 exemption |
| `ci.yml` | one step per task: TASK-008's, TASK-017's and TASK-006's in `scenario-git-http`; none duplicated; the YAML parses |
| `codex review --base main` (codex-cli 0.162.1, the configured default, no `-c model=`; sessions read from `~/.codex/sessions/2026/10/19/`, `source: {"subagent":"review"}`, `cwd` = this worktree, `model` = `gpt-6.1-sol`) | Round 1 at `96cf8e40`: `01a153e5-817e-7f33-9a6b-680376b6f857`, no findings. Round 2 at `044c2103`: `01a153fc-abf2-7a72-9d81-03a7e51ef284`, one P2 (the `MergeBases` `HEAD` rewrite): real, reproduced, fixed in `a6acc720`. Round 3 at `a6acc720`: `01a15409-966a-7da1-a64f-248cfbd7f2b4`, no findings |

**SMP compatibility.** `i23/s-int4/smp` is SMP `main` `1ae9b0e3`, detached, with `../spine` =
this worktree and `GOFLAGS=-mod=readonly`. The checkout was never modified, and `git status` is
clean.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK`; `git status` clean |
| `go test -race -p 4 -count=1`, no DSNs, on the 14 SMP packages importing a changed Spine package (test imports included): `cmd/customer-app`, `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`, `internal/recovery` | 13 ok; `projectionsvc` has no tests. No failures: the wave 3 `gitread` / `projectionwriter` contract failures are gone on SMP `main` `1ae9b0e3` |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s4i_smp_it` with `vector`, `s4i_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`, as in CI) | `4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)`; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `1 packages, 301 tests passed, 0 failed, 1 skipped (1 allowed)`; `testgate: PASS` |
| The three Linux-only tests those allowances skip on macOS, in `golang:1.26-bookworm` (git 2.39.5): `TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch` (`spinecore`) and `TestIntegration_ReceiveTimeMemoryBounded` (`-tags integration`, `githttp`) | all 3 PASS on git 2.39.5; the containers were removed |

No SMP failure, so no compatibility break.

**Not run.**
- GitHub CI: it cannot run this branch until Spine is pushed.
- The other steps of SMP's integration job (platform-DB shared suites, fenced migrate,
  workspace-schema, faultinject, verify-children) and the image-based gates. They are not in this
  gate list.
- The full scenario suite was not repeated after `a6acc720`, for the reason in the Gates
  preamble.

**Environment.**
- PostgreSQL: the shared `i23-pg` on `localhost:55630`. Only `s4i_*` databases were created, and
  all were dropped. There were no `ws_*` databases or roles before the SMP suites and none
  after, and the shared roles were untouched.
- Docker: the only containers were the `git-floor` runs, the Linux-only SMP runs and the
  `alpine:3 df` checks, all `--rm`. Nothing was pulled or pruned.

### Cross-task review (wave 4) (2026-10-19)

The wave 4 integration (`db0b727b`) was reviewed as a whole: three lenses (merge path, state
writes, completeness and compatibility), each finding checked by two skeptics. The fixes are on
`init/INIT-023-wave4-integration` in the worktree `i23/s-int4/spine`: `0ab10a57`, `4ae5032b`,
`6c835669`, `757d90b8`, `09986e8a`, `e0c85d2a`, `19de65e5`, `8d5edbfe` and `f47131f4`. The final
code is `f47131f4`; the commit that adds these notes changes only the task artifacts. Every code fix has a test that fails
without it (the mutation check below). Nothing was pushed. Spine `main`, the task branches and the
SMP checkout are unchanged. The owner questions of all four tasks stay recorded as defaults,
pending the owner; none was re-decided. This note carries the full table and the gates; TASK-012,
TASK-017 and TASK-008 have short notes for their findings.

**Dispositions.** Severity is the reviewer's; the two skeptics' votes are in brackets.

| # | Finding | Disposition |
|---|---|---|
| P2-1 [P2, P3] | `merge_binding.go:792`: V1 (TASK-017) and this task's post-merge assertion could be skipped for good. A `MergeBound` error after git had moved the local `main` (the trailing `rev-parse` fails, the git child is killed after its ref update), or the process dying inside the merge block (on `SIGTERM` neither the scheduler nor the HTTP server waits for one), left the merge on `main`; the block only ran `git merge --abort`. The next attempt saw "already up to date", both checks returned nil for the replay, and the merge was pushed: a review-less workflow carried into `workflows/` by a rename on `main`, or a criss-cross resolution that reopens a Completed task. An approved merge killed before its consumption marker completed without one. | **Real, fixed** (`0ab10a57`, `merge_inflight.go`). With V1 or the guard on, an attempt that can move `main` records the reserved `spine.merge.unverified` (`"<base> <source>"`) on the run in the merge block before `MergeBound`. The write that records the accepted merge clears it before the push, together with the consumption marker (`checkGovernedMerge` now returns the marker; `acceptedMergePatch`). A `MergeBound` error after `main` moved is taken back in the block (`takeBackFailedMerge`, compare-and-swap through `ResetBranch`); if that fails, the run stays `committing` (transient, never the permanent route) and governed merges are blocked on the merge. Every attempt of a run that still carries the record settles it first, before anything reads `main` (`settleUnverifiedMerge`): `main` at the base is cleared; `main` at this run's merge is taken back and the attempt merges and checks again; a merge buried under later commits refuses the attempt as unavailable, keeps the record and blocks governed merges (guard on) until an operator resets `main`. The retry budget never abandons a run that carries the record. A replay without a record (bound already on `main`) is unchanged. Both checks off: nothing is written. Follow-up `f47131f4`, from the second skeptic's note that a killed git child is classed permanent (empty stderr): when `main` cannot be checked after the error (unreadable, or moved to something other than this attempt's merge), the attempt is transient whatever the error's class, so the run stays `committing` and its next attempt settles the record instead of being routed away with an unverified merge possibly on `main`. Residual: KNOWN-LIMITATIONS "Governance-Path Guard" 16. Tests: `wave4_review_merge_test.go`, real git: a late error, a crash then a restart, a buried merge, a failed late take-back, V1 on the criss-cross, an approved merge crashing before its marker, a legitimate replay, the budget's exemption, and a permanent late error with `main` unreadable (`TestWave4Review_UncheckedLateMergeStaysCommitting`) |
| P2-2 [P3, P2] | `governance_guard.go:899`: `governanceHoldTarget` took a failed `LoadWorkflow` (or a failed step listing, swallowed by `findActiveMergeStep`) for "no publish step" and answered with the last completed step. The hold record, its clearing on approval and the retry budget's held test could each pick a different step: one transient `git show` abandoned a held, undecided run (or failed it, with a second failure), or left an approved run's hold on the publish step so its failures were retried forever. | **Real, fixed** (`4ae5032b`). `governanceHoldTarget` returns `(target, error)`; it falls back to the last completed step only when the loaded workflow has no live publish step. A hold whose target cannot be read records nothing that attempt (no step write, no event; the merge is still held). `clearGovernanceHold` clears every execution of the run that carries the hold, reading no workflow, and returns its error. `governanceKeepsCommitting` returns the error, so `AbandonExhaustedMerge` returns `(false, err)` and the scheduler skips the tick. Tests: `TestWave4Review_HoldReadFailureNeverAbandons` (one and two failures), `…ApprovalClearsTheHoldWhateverTheWorkflowRead`, `…HoldNotRecordedOnAGuessedStep` |
| P3 | `governance_guard.go:1282`: a stale hold (the run's commits reached `main` another way, with no decision) kept the retry budget off the run forever, and the step kept showing a hold. | **Fixed** (`4ae5032b`): an attempt the gate allows clears any hold left on the run (`TestWave4Review_StaleHoldIsClearedWhenTheGateAllows`, the reviewer's PoC). Residual while every attempt fails before the gate: KNOWN-LIMITATIONS "Governance-Path Guard" 17 |
| P3 | `governance_decision.go:189`: after an approved merge was refused post-merge and its take-back failed, `GET /governance` said `required=false, held=false`, the approve returned an empty `merge_error`, and a reject answered 409 "the approved merge already reached the local main". | **Fixed** (`6c835669`), differently from the suggestion for the reject: while the run's `blocked_target` merge is still in the local `main`'s history, `GovernanceMergeStatus` sets `Blocked` / `BlockedMergeSHA` (never `Held`; served as `blocked`, `blocked_merge_sha`), the approve reports it in `merge_error`, and a reject is a 409 that says the merge could not be taken back and that the run's next attempt takes it back first. The reject is not let through: KNOWN-LIMITATIONS 6 keeps a blocked run `committing` so its own retries restore `main` after a restart, and its post-merge refusal routes it back on its own once the take-back works. Test: `TestWave4Review_BlockedRefusedMergeIsReportedTruthfully` (the reviewer's PoC, inverted) |
| P3 | `merge_retry.go:215` (TASK-012): `AbandonExhaustedMerge` routed through `handlePermanentMergeFailure`, which turns a failed workflow load (or step listing) into "no publish step" and fails a publish-workflow run. | **Fixed** (`4ae5032b`, test strengthened in `09986e8a`): the route (workflow, live publish step) is decided before anything is marked or routed, and a read error returns `(false, err)`; `routePermanentMergeFailure` takes the decision. A read failing after the decision leaves the run `committing` for the next tick (`TestWave4Review_AbandonmentDecidesItsRouteFirst`) |
| P3 | `governance_decision.go:235` (TASK-012): an abandonment could interleave with an approve, route the approved run back, and leave the approval's `MergeRunBranch` standing down while the response said a merge was attempted; the hold-period count also stayed in place after the approval. | **Fixed in part** (`4ae5032b`). `DecideGovernanceMerge` registers with `mergeAttemptGate` before it records anything: during an abandonment it is `ErrUnavailable` with nothing written (try again), and no abandonment starts until the decision and its merge finish, so the approval's attempt can no longer stand down because of one (`TestWave4Review_DecisionIsAnAttemptInFlight`). **Not done**: restarting the count at the approval. That is retry counting (TASK-012 owner question 4 / Decision 10, recorded as a default); a follow-up for the owner |
| P3 | `serve_builders.go:416` (TASK-008): no test pinned that `spine serve`'s startup step recovery calls `RecoverCompletedStep`. | **Fixed** (`757d90b8`): the callbacks move into `engineSchedulerOptions` / `stepRecoveryFor` over a `schedulerEngine` interface, shared by both builders; `TestServeScheduler_StepRecoveryUsesRecoverCompletedStep` drives startup recovery through them. The `SubmitStepResult` mutant no longer compiles (not in the interface); a `RetryStep` mutant fails |
| P3 | `serve_builders.go:436`: no test pinned that the production scheduler retries through `governanceAwareCommitRetry` (AC 35). | **Fixed** (`757d90b8`): `commitRetryFor`, used by both builders; `TestServeScheduler_HeldMergeLogsNoError` drives the built commit-retry sweep. The bare-`MergeRunBranch` mutant fails |
| P3 | `serve_builders.go:217`: no test of the shared-mode push resolver's `withOverrideProvenance`. | **Fixed** (`757d90b8`): `TestGitPushResolver_RecordsOverrideProvenanceInSharedMode` asserts the emitter writes an honoured override to the workspace's own event log. The mutant fails |
| P3 | `governance_guard.go:1273`: a well-formed `blocked_target` kept the run off the budget even after another run's gate had taken the merge back. | **Fixed** (`4ae5032b`): `governanceKeepsCommitting` checks the merge against the local `main` (`mergeOnLocalTarget`; a missing commit is not on it), clears a stale key and lets the budget apply; a read error skips the tick (`TestWave4Review_StaleBlockedTargetDoesNotStopTheBudget`) |
| P3 | `engine-state-machine.md:147`: §3.3.1 lacked `merge_retries_exhausted` and `governance_approval_required`. | **Fixed** (`e0c85d2a`) |
| P3 | `integration-guide.md:819`: `SPINE_GOVERNANCE_GUARD` missing from the §8 environment reference. | **Fixed** (`e0c85d2a`): default on, the off spellings, the Warn on an unrecognised value, ADR-016 and §12 |
| P3 | The tenth P3 (of the completeness lens, by its position): its text was cut off in the task handed to this fix pass (only `"severity": "P3"` and `"repo": "spine` survived), and no copy of the finding is in the reviewers' scratch directories. | **Not addressed: unknown.** Recorded as an open question for the orchestrator. That lens's scratch also has a scenario run with `spine serve`'s defaults (the guard on) in which `TestWorkflowLifecycle_*` waits in `committing` ("the run's starter is not recorded"); if that was the finding, it is TASK-006's lifecycle exemption, an owner question kept at its default, not re-decided here |

Also fixed: `gofmt -l` over the changed files listed `core/divergence/convergence.go` (the
`adapters/git` import out of order, as on `main` `ae513dca`); reformatted in its own commit
(`19de65e5`), as TASK-012's AC 23 allows. `golangci-lint --new-from-rev ae513dca` found four
issues in the review's own code (gocritic `builtinShadow` in `settleUnverifiedMerge`, staticcheck
QF1008 in a test helper), fixed in `8d5edbfe` with no behaviour change.

**Mutation check.** `w4/agents/fix-spine/mut/mut.py` applies the engine mutants one at a time, each
undoing one fix, and runs the test that pins it. First round (13 mutants): 11 killed; E5 (the
abandonment back on `handlePermanentMergeFailure`) did not build and E13 (the budget ignoring the
merge-in-flight record) survived. Their tests were strengthened in `09986e8a`. Final round, in a
throwaway detached worktree at `f47131f4` (`mut2.py`, `mut3.py`; logs `mut2.log`, `mut3.log`):
all 14 engine mutants are killed (E1–E13, and E14: the new unchecked-`main` reclassification
removed, killed by `TestWave4Review_UncheckedLateMergeStaysCommitting`), and so are the four serve
mutants: the bare `MergeRunBranch` as the commit retry, `RetryStep` for a completed execution, and
the shared-mode and single-mode push resolvers without the provenance wrapper. The worktree was
removed.

**Follow-ups.**
- KNOWN-LIMITATIONS "Governance-Path Guard" 16: the merge-in-flight record is on its run only, so
  after a restart another writer can build on, and publish, an unverified merge before that run's
  next attempt. Closing it needs a record every writer checks (for example in the repository,
  like TASK-012's pending take-back but durable).
- "Governance-Path Guard" 17: a stale hold on a run whose attempts all fail before the gate.
- Restarting the retry count at an approval (above): for the owner, with TASK-012's Decision 10.

**Codex.** `codex review --base main` (codex-cli 0.162.1, the configured default, no `-c model=`;
findings read from the review sub-session JSONL under `~/.codex/sessions/2026/10/19/` with
`source: {"subagent":"review"}`, `cwd` = this worktree, `model` = `gpt-6.1-sol`). Round 1 at
`e0c85d2a`: session `01a1547b-0773-71d1-9911-72cbaa105d4b`, no findings. Round 2 at `f47131f4`:
session `01a154ce-eb74-7e82-90e2-2575bf420173`, no findings. Two clean rounds; no third was run.

**Gates** (final code `f47131f4`; logs in
`spine-management-platform/tmp/init027-orchestration/w4/agents/fix-spine/`, `chainD.out`,
`chainS.out` and the directories they name).

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean, all three |
| `gofmt -l` on the 172 `.go` files changed since `ae513dca` | none |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 failures |
| `go test -race -p 4 -count=1` on the 19 packages changed since `ae513dca` (`service/scenariotest/scenarios` builds only with `-tags scenario`) | 19 ok, 0 data races |
| `-tags integration -p 1 -count=1 -v` on `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace` (fresh `s4f_it`; `s4f_reg` migrated from `migrations/registry`) | 587 top-level PASS (1164 with subtests), 0 SKIP, 1 FAIL: `adapters/store` `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, `workflow_version_label`), the known pre-existing failure |
| Full scenario suite (`-tags scenario -p 1 -count=1 -v ./service/scenariotest/...`, fresh databases, registry migrated) | 31 top-level `--- FAIL`, the sorted list identical to the `main` `ae513dca` baseline of the integration pass (`int-spine/scen-m.topfails`): the known 30 plus `TestWorkspace_ScopedServices`. 204 PASS, 0 SKIP |
| CI `scenario-git-http`: every `run:` step read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, fresh `s4f_ci`), including the steps TASK-008, TASK-017 and TASK-006 added | all 7 steps exit 0: `TestGitHTTP_*` 20, `TestBootstrapInternalAdmin_*` 2, `TestWorkflowLifecycle_*` 4, pooled branch protection 1, TASK-008's store compare-and-swap and evidence 10, TASK-017's scenarios and pooled validator 4, TASK-006's scenario and store 2 (top-level PASS); 0 FAIL, 0 SKIP |
| CI `git-floor`: its 3 `run:` steps in `golang:1.26-bookworm` (`--pull=never --rm --init`, non-root, read-only mounts, `GOPROXY=off`) | git 2.39.5; all 3 steps ok |
| `golangci-lint` v2.11.4 `run --new-from-rev ae513dca` on the changed packages, plain, `--build-tags integration`, `--build-tags scenario` | 0 issues each (the four the review's code had were fixed in `8d5edbfe`) |
| `golangci-lint --enable-only=gosec`: `--new-from-rev ae513dca ./...`; full `./...`; full `--build-tags integration,scenario` | 0; 0; 1: G602 at `adapters/store/postgres_integration_test.go:1563`, a file unchanged since `main` (recorded by wave 3 and the integration pass) |

**SMP compatibility.** `i23/s-int4/smp` is SMP `main` `1ae9b0e3`, detached, `../spine` = this
worktree at `f47131f4`, `GOFLAGS=-mod=readonly`; never modified (`git status` clean before and
after).

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | all OK |
| `go test -race -p 4 -count=1`, no DSNs, on the 14 SMP packages importing a changed Spine package (test imports included; recomputed with `go list`, the same list as the integration pass): `cmd/customer-app`, `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`, `internal/recovery` | 13 ok (`projectionsvc` has no tests), 0 failures |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s4f_smp_it` with `vector`, `s4f_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites" via `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`) | `4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)`; `testgate: PASS` |
| SMP CI "Git gateway integration tests" via `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `1 packages, 301 tests passed, 0 failed, 1 skipped (1 allowed)`; `testgate: PASS` |
| The three Linux-only tests those allowances skip on macOS, in `golang:1.26-bookworm` (git 2.39.5): `TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch`, `TestIntegration_ReceiveTimeMemoryBounded` | all 3 PASS |

No SMP failure, so no compatibility break.

**Not run.** GitHub CI (nothing is pushed). SMP's integration-job steps outside this gate list
(platform-DB shared suites, fenced migrate, workspace-schema, faultinject, verify-children) and the
image-based gates.

**Environment.** PostgreSQL: the shared `i23-pg` on `localhost:55630`; only `s4f_*` databases were
created and all were dropped; no `ws_*` databases or roles before the SMP suites or after; the shared
roles were untouched. Docker: only the `git-floor` run, the two Linux-only SMP runs and `alpine:3 df`
checks, all `--rm` and `--pull=never`; nothing pulled or pruned. The mutation check used a
throwaway detached worktree under the scratch directory, removed afterwards.

### Acceptance (wave 4) (2026-10-19)

The final gate on `init/INIT-023-wave4-integration` (worktree `i23/s-int4/spine`). Every gate ran at
`02d65f4c`. Its last code commit is `f47131f4`, the last fix of the cross-task review; `02d65f4c`
and the commit that adds this note change only the four task artifacts. Nothing was pushed. Spine
`main` (`ae513dca`), the task branches and the SMP checkout are unchanged. The owner questions of
all four tasks stay recorded as defaults, pending the owner; none was re-decided. No gate found a
defect, so this gate added no code commit.

**This task.**
- Its CI step, "Governance-path guard scenario and store", passes in the `scenario-git-http` run
  below: `TestGovernancePathGuard_HeldUntilOperatorApproves` and
  `TestGovernanceStore_Integration`, with 0 SKIP and 0 FAIL. The scenario passes in the full
  scenario run as well.
- `core/branchprotect` passes under `-race`. The SMP packages that import it, `githttp` and
  `spinecore`, pass under `-race` and in their CI suites.
- ADR-016 stays Proposed, with the owner's sign-off pending.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean, all three |
| `gofmt -l` (Go 1.26.5) on the 172 `.go` files changed since `ae513dca` (88 added, 84 modified) | empty |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 failures (3 more packages have no tests) |
| `go test -race -p 4 -count=1` on the 19 packages changed since `ae513dca`: `adapters/{delivery,git,projection,scheduler,store}`, `core/{actor,artifact,auth,branchprotect,divergence,domain,engine,validation,workflow,worktreefs}`, `service/cmd/spine`, `service/internal/{gateway,workspace}`, `service/scenariotest/harness`. `service/scenariotest/scenarios` builds only with `-tags scenario`; the scenario rows cover it | 19 ok, 0 data races |
| `-tags integration -p 1 -count=1 -v` on the changed packages that have integration suites: `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace`. `SPINE_TEST_DATABASE_URL` = fresh `s4g_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s4g_reg`, migrated from `migrations/registry` | 587 top-level PASS (1164 with subtests), 0 SKIP, 1 FAIL. The failure is `adapters/store` `TestRunModeDatabaseDefault` (`postgres_integration_test.go:702: GetRun: can't scan into dest[5] (col: workflow_version_label): cannot scan NULL into *string`), the known pre-existing failure. This run checked it again: a `git archive` export of `main` `ae513dca` fails it with the same message |
| CI `scenario-git-http`: its 7 `run:` steps, read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, repo root as cwd, `SPINE_TEST_DATABASE_URL` = fresh `s4g_ci`) | All 7 steps exit 0. `TestGitHTTP_*`: 20 PASS. `TestBootstrapInternalAdmin_*`: 2. `TestWorkflowLifecycle_*`: 4. Pooled workflow-service branch protection: 1. TASK-008's store compare-and-swap: 8, and its scenarios: 2. TASK-017's `StatusLifecycle` scenarios: 3, and its pooled validator: 1. TASK-006's governance scenario: 1, and its store test: 1. 0 SKIP and 0 FAIL in every log |
| Full scenario suite (what `make test-scenario` runs, with `-p 1 -count=1 -v -timeout 60m`), on this branch and on a fresh `git archive` export of `main` `ae513dca`; fresh `s4g_scen_{b,m}` each, registry `s4g_sreg_{b,m}` migrated | 31 top-level `--- FAIL` on each, and the sorted lists are identical at every level (59 lines with subtests on each). They are the known pre-existing set: the 30 plus `TestWorkspace_ScopedServices`. Top-level PASS is 204 on the branch and 198 on `main`. The extra 6 are `TestCompletedStep_IsNotReRouted`, `TestStepOutputEvidence_RecordedAndReadBack`, `TestGovernancePathGuard_HeldUntilOperatorApproves`, `TestPlanningRun_StatusCascadesToChildren_StatusLifecycle`, `TestStandardRun_StatusLifecycleArtifactsDir` and `TestStandardRun_StatusLifecycleRefusesNonOwnedStatusEdit`. 0 SKIP |
| CI `git-floor`: its 3 `run:` steps, verbatim (`bash -e`), in `golang:1.26-bookworm` (`--pull=never --rm --init`; non-root `501:20`, where GitHub runs root; the worktree, the main repository's `.git`, the host module cache and the step scripts mounted read-only; `GOPROXY=off`, `GOTOOLCHAIN=local`) | git 2.39.5. All 3 steps ok: the version check; `adapters/git`, `adapters/git/refname` and `core/engine` with the ref-grammar filter; then `adapters/git/...`, `core/artifact` and `core/workflow`. The container was removed |
| `golangci-lint` v2.11.4 `run --new-from-rev ae513dca` on the changed packages, plain, `--build-tags integration` and `--build-tags scenario` (with `./service/scenariotest/...`) | 0 issues each |
| `golangci-lint --enable-only=gosec` on the changed packages (plain; `integration,scenario`) and on `./...` (plain; `integration,scenario`) | 0; 1; 0; 1. The one finding is G602 at `adapters/store/postgres_integration_test.go:1563`. It was there before: the file is unchanged since `ae513dca`, and the `main` export reports the same finding in this run |
| `codex review --base main` (codex-cli 0.162.1, the configured default, no `-c model=`). The session was read from `~/.codex/sessions/2026/10/19/` (`source: {"subagent":"review"}`, `cwd` = this worktree, `model` = `gpt-6.1-sol`) | This round ran because `02d65f4c` (notes only) was added after the cross-task review's last clean round, which was at `f47131f4` (`01a154ce-eb74-7e82-90e2-2575bf420173`, no findings). Round at `02d65f4c`: `01a154e6-f52f-70d3-a575-8d753013f8b2`, `"findings": []`, "patch is correct". Codex's own `go test ./core/engine -timeout 90s` side run timed out inside its sandbox; `core/engine` needs about 360 s on this machine, and it passes in the gate rows above. Nothing to fix, so no second round |

**SMP compatibility.** `i23/s-int4/smp` is SMP `main` `1ae9b0e3`, detached, with `../spine` =
this worktree (`02d65f4c`) and `GOFLAGS=-mod=readonly`. The checkout was never modified, and
`git status` is clean before and after.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1`, no DSNs, on the 14 SMP packages that import a changed Spine package under any of the four tag sets (test imports included): `cmd/customer-app`, `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`, `internal/recovery` | 13 ok; `projectionsvc` has no tests. 0 failures |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s4g_smp_it` with `vector`, `s4g_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, and `TEST_PROVISIONING_DSN` = `smp_prov`, as in CI; CI's timeout is 10m, raised for the loaded machine) | `4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)`; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `1 packages, 301 tests passed, 0 failed, 1 skipped (1 allowed)`; `testgate: PASS` |
| The three Linux-only tests those allowances skip on macOS, in `golang:1.26-bookworm` (git 2.39.5, non-root, `--init`, read-only mounts): `TestDiffChildMemory_MillionPaths` and `TestReceiveTimeMemory_RealGitReaderFetch` (`spinecore`, `-race`), and `TestIntegration_ReceiveTimeMemoryBounded` (`githttp`, `-race -tags integration`) | all 3 PASS. The containers were removed |

No SMP command failed, so there is no compatibility break, and no run against `../spine` at
`ae513dca` was needed.

**Not run.**
- GitHub CI. It cannot run this branch until Spine is pushed.
- The other steps of SMP's integration job: the platform-DB shared suites, fenced migrate,
  workspace-schema, faultinject and verify-children. They are not in this gate list. The image-based
  gates were not run either.
- `-race` on `service/scenariotest/scenarios`, which builds only with `-tags scenario`. The CI job
  and the full scenario run cover it without `-race`.

**Environment.**
- PostgreSQL: the shared `i23-pg` on `localhost:55630`. Only `s4g_*` databases were created, and
  all of them were dropped. There were no `ws_*` databases or roles before the SMP suites and none
  after. The shared roles `platform`, `platform_audit_retention` and `smp_prov` were already there
  and were untouched.
- Docker: the only containers were the `git-floor` run, the two Linux-only SMP runs and the
  `alpine:3 df` checks, all `--rm --pull=never`. Nothing was pulled, built or pruned. The Docker
  VM had 5.8 GB free at the lowest check.
- Logs: `spine-management-platform/tmp/init027-orchestration/w4/agents/gate-spine/`.

### Integration follow-up (wave 4) (2026-10-19)

A reviewer ran the scenario suite with `spine serve`-like settings: the guard and the lifecycle
exemption on. Every ADR-008 workflow-lifecycle run was then held for an operator with "the run's
starter is not recorded". `recordLifecycleStarter` (`core/engine/governance_guard.go`) reads
`domain.ActorFromContext` and records nothing when no actor is bound. This follow-up checked
where a lifecycle run starts and whether an actor is on the context there. The fix is
`958e063a` on `init/INIT-023-wave4-integration`. Nothing was pushed, and owner question 3 (the
exemption is on in `spine serve`) stays at its default.

**Start paths.**

| Path | Actor on ctx at the start? | Starter recorded? |
|---|---|---|
| `spine serve` (single-workspace and pooled): POST `/workflows` or PUT `/workflows/{id}` by a caller below operator, routed to `startWorkflowLifecycleRun` → `workflowPlanningRunAdapter` → `Orchestrator.StartWorkflow(Update)PlanningRun` | yes. The auth middleware binds `domain.WithActor` (`middleware.go`), and the adapter passes the request context unchanged | yes |
| `spine serve` dev mode with no token (`devMode`, no actor) | no | no. The run is held for an operator, which is the intended fail-closed for a genuinely actor-less start |
| POST `/runs` with `mode=planning` for a `workflows/` path | never starts one: `isWorkflowTarget` refuses it before any starter runs (TASK-014), and the engine's `StartPlanningRun` refuses it too | n/a |
| `workflow.Service` | starts no run. Its writes on the lifecycle branch run inside `onBranch` with the starter's context | n/a |
| Scenario harness `engine.StartWorkflowPlanningRun` / `StartWorkflowUpdatePlanningRun` | **no**. `sc.Ctx` carried only the log actor id (`observe.WithActorID`) | **no**, the defect |

So `spine serve` was not affected. Only the harness lacked the actor, and the harness is what was
fixed. The engine is unchanged.

**Fix (`958e063a`).** The harness's two start steps now start the run with `lifecycleStartCtx`.
When the scenario's context binds no actor, it binds the reviewer the log context already names
(`engine.ScenarioLifecycleStarter`, `scenario-actor`), as the gateway binds the caller. The
binding is local to the start, and later steps keep `sc.Ctx`.

**Tests** (`service/scenariotest/scenarios/workflow_lifecycle_starter_test.go`, Postgres-backed,
all `TestWorkflowLifecycle_*`, so they run in the `scenario-git-http` CI step that must never
skip):
- `TestWorkflowLifecycle_GatewayStartedRunReviewedByAnotherIsExempt` and
  `TestWorkflowLifecycle_GatewayStartedRunReviewedByItsStarterIsHeld` drive the real path, with
  the guard and the exemption on:
  - the real gateway router, auth middleware and `handleWorkflowCreate`, over the scenario's
    orchestrator, with a starter adapter that is `serve_wiring.go`'s passthrough;
  - reviewer `wl-starter`'s POST `/workflows` records `wl-starter` as the starter, and `rev-a`
    drafts under `spine serve`'s strict step-entry policy;
  - `rev-b` reviews → the merge is exempt and the workflow is on `main`;
  - `wl-starter` reviews (allowed to claim: they ran no step) → held for an operator, reason
    "the reviewer started the run", and `main` does not have the file.
- `TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview_GuardOn` is TASK-014 AC 19 under
  the guard, through the harness start. On `07825efb` it was held with "the run's starter is not
  recorded".

**Mutations** (throwaway worktree, removed; all killed):

| Mutant | Killed by |
|---|---|
| harness binds no actor | `..._DraftAuthorCannotReview_GuardOn` (starter not recorded) |
| `startWorkflowPlanningRun` does not call `recordLifecycleStarter` | `..._GatewayStartedRunReviewedByAnotherIsExempt` |
| `reviewerIsNotAuthor` does not compare the reviewer with the starter | `..._GatewayStartedRunReviewedByItsStarterIsHeld` |

The golden-path lifecycle scenarios, run under the guard, are still held after the fix, and that
is correct. Their synthetic actor `scenario-test-actor` drafts and reviews, so the exemption
refuses them as "the reviewer is an author of the run". They run with the guard off, as before.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: stays In Progress.** Spine `main` was pushed on 2026-11-04 with the owner's
  approval. GitHub CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802`
  plus INIT-024 TASK-010's git-identity fix) passed all 8 jobs, the governance-path guard scenario
  and store step included. That closes the GitHub CI item under "Not run". Remaining (owner):
  - **AC 1:** ADR-016 is `Proposed`; it becomes `Accepted` with the owner's sign-off on OQ1.
  - **AC 11:** a ruling or an AC amendment for the permanent route of an unevaluable merge
    (Deviation 22).
  - **AC 18:** the amendment of the `FullRebuild` sentence (Deviation 12).
