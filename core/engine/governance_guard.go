package engine

// Governance-path guard on governed merges (ADR-016; INIT-023 TASK-006,
// audit F3 "run-branch slippage", P2, both surfaces).
//
// The defect. A governed merge was allowed whatever it changed
// (branchprotect allowed OpGovernedMerge unconditionally, ADR-009 §3), and
// mergePrimary merged the whole run branch. A run branch could therefore
// carry `.spine/branch-protection.yaml: rules: []` or a review-less
// `workflows/task-default.yaml` into main, and the projection installed it
// as live policy: any contributor could then direct-write or delete main,
// every later run used the review-less workflow, and no override event
// was ever emitted. In SMP one member could self-assign and self-accept
// the review step, so a single actor sufficed.
//
// The fix, opt-in through WithGovernanceGuard (zero value: today's
// behaviour; `spine serve` turns it on):
//
//  1. Gate (governanceGate). After TASK-005's resolveMergeSource has
//     validated the run's frozen binding and built the merge source, the
//     changed paths are the union of Diff(B, S) over EVERY merge base B of
//     main and S, the commit the merge will merge (the bound commit or the
//     status-cascade commit built on it). One merge base is not enough: in
//     a criss-cross history `git merge-base` prints one of two bases, the
//     diff from it omits a governance file the run reverted, and git's
//     recursive merge (a virtual base) reverts it on main. If S agrees
//     with every base on a path, the virtual base agrees too (recursively
//     merging identical sides is a no-op), so main keeps its version.
//  2. Decision. branchprotect.EvaluateGovernance decides, consulted both
//     through Policy.Evaluate (Request.Governance) and here directly, so a
//     permissive or custom Policy cannot turn the guard off (D4). A
//     governance-changing merge is HELD unless an operator approved
//     exactly the bound commit: the run stays in committing, nothing is
//     merged (no code repository either), and the hold is recorded on a
//     step and announced once per episode (recordGovernanceHold).
//  3. Post-merge assertion (checkGovernedMerge, inside the primary merge
//     block; settleGovernedRefusal after it). Before the push, the
//     governed paths the merge actually changed (Diff(BaseSHA, M)) must be
//     covered by the evaluation; anything else is a permanent refusal,
//     taken back to BaseSHA and never pushed (owner question 6). It fires
//     under git's DEFAULT configuration too (review round 1): rename
//     following carries a run's edit of a path that main has since renamed
//     into a governed tree onto the governed name, which the pre-merge
//     diff from the merge base cannot see. Non-default configuration
//     (merge.directoryRenames, merge drivers) is the other source. The
//     author's remedy is to update the run branch from main: the merge
//     base moves, the governed path is evaluated and the merge is held
//     for an operator decision.
//  4. Consumption marker. A merge that consumed an approval records the
//     merge commit and its base under reserved commit_meta keys BEFORE the
//     push, so the projection's protection-removal guard can tell an
//     approved relaxation from one that slipped in.
//
// The approval itself is written only by DecideGovernanceMerge
// (governance_decision.go), an engine API that checks the operator role;
// workflow outcomes can never write reserved keys (TASK-005 strips them).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// GovernanceGuard configures the governance-path guard (ADR-016). The
// zero value is the guard off.
type GovernanceGuard struct {
	// Enabled turns the guard on. It requires merge binding
	// (EnableMergeBinding, called first) and a GitOperator that implements
	// MergeBaseLister.
	Enabled bool
	// Paths replaces branchprotect.DefaultGovernancePaths (`.spine/`,
	// `.spine.yaml`, `workflows/`) when non-empty. Entries are path
	// prefixes validated by branchprotect.NewGovernancePaths.
	Paths []string
	// WorkflowDirs are added to the guarded set: every directory the
	// projection installs workflow definitions from. Callers pass
	// projection.WorkflowDirs(artifactsDir) (core/engine cannot import
	// adapters/projection), e.g. ["workflows/", "spine/workflows/"].
	//
	// Since INIT-024 TASK-002 (review finding N-5) leaving it out no longer
	// leaves those directories open: every decision also guards
	// <ArtifactsDir()>/workflows/ when the orchestrator has an artifacts
	// directory (WithArtifactsDir, in either call order), and the
	// structural backstop branchprotect.WorkflowDefinitionDirs holds a
	// *.yaml / *.yml file directly inside any directory named "workflows"
	// (see governedPathsFor). Passing the projection's directories is still
	// the documented wiring: it is the only source that also guards nested
	// files of <artifacts_dir>/workflows/ when the orchestrator was never
	// told the artifacts directory.
	WorkflowDirs []string
	// ExemptWorkflowLifecycleTaskPath lets an ADR-008 workflow-lifecycle
	// planning run merge its own workflow file without an operator
	// approval, under the conditions of lifecycleExemption (owner
	// questions 3 and 10). The zero value grants no exemption.
	ExemptWorkflowLifecycleTaskPath bool
	// ExemptOwnRunEvidence excuses paths under
	// `.spine/runs/<run id>/evidence/` (byte-exact run id), the documented
	// execution-evidence location (architecture/execution-evidence.md
	// §6.1; owner question 9). The zero value excuses nothing.
	ExemptOwnRunEvidence bool
	// MaxReportedPaths caps the path lists in events and status reads;
	// 0 means 100.
	MaxReportedPaths int
}

// MergeBaseLister is the optional capability the guard needs: every merge
// base of two commits (`git merge-base --all`), each a full lowercase hex
// object id. Histories with no common ancestor are an empty list and a nil
// error — the guard refuses such a merge permanently — while an error is
// treated as possibly transient (the run stays committing and is retried).
// It is not part of GitOperator, so embedders whose operator lacks it keep
// compiling; WithGovernanceGuard type-asserts it and refuses to turn the
// guard on without it. *git.CLIClient implements it.
type MergeBaseLister interface {
	MergeBases(ctx context.Context, a, b string) ([]string, error)
}

// ErrGovernanceApprovalRequired is the cause of the error MergeRunBranch
// returns for a held merge (errors.Is works through the
// *domain.SpineError, code precondition_failed). The run is left in
// committing. Retry sweeps should treat it as "waiting for an operator",
// not as a failed attempt (it is not a merge failure and must not count
// against a retry budget).
var ErrGovernanceApprovalRequired = errors.New("engine: governed merge requires governance approval")

// errGovernanceUnevaluable is the cause of the error for a merge whose
// changed paths could not be computed: refused, never held.
var errGovernanceUnevaluable = errors.New("engine: governance guard could not evaluate the merge")

// errGovernanceNoMergeBase: the merged commit shares no history with the
// target, so there is no base to diff from.
var errGovernanceNoMergeBase = errors.New("the run branch shares no history with " + authoritativeBranch)

// unevaluableForever reports whether an evaluation failure is a property
// of the merge itself, the same on every attempt (review round 1): no
// merge base (an orphan history pushed to the run branch), or a diff
// larger than the git output limit (git.ErrOutputTooLarge — the same trees
// and limit always give the same size). Retrying those would leave the run
// in committing for good, with neither approve nor reject able to act and
// cancel not allowed from committing; the gate refuses them permanently
// instead (refuseUnevaluableMerge). Every other failure — a git process,
// lock or store error, a malformed MergeBaseLister answer — may be
// transient and keeps the run committing for a retry (fail closed).
func unevaluableForever(err error) bool {
	return errors.Is(err, errGovernanceNoMergeBase) || errors.Is(err, git.ErrOutputTooLarge)
}

// unevaluableReason names an unevaluableForever failure for messages,
// without git output.
func unevaluableReason(err error) string {
	switch {
	case errors.Is(err, errGovernanceNoMergeBase):
		return "the run branch shares no history with " + authoritativeBranch
	case errors.Is(err, git.ErrOutputTooLarge):
		return "its diff exceeds the git output limit"
	default:
		return "its changed paths cannot be computed"
	}
}

// GovernanceHoldRuleID is the ErrorDetail.RuleID of a held merge.
const GovernanceHoldRuleID = "governance-path-guard"

// defaultMaxReportedPaths caps reported path lists.
const defaultMaxReportedPaths = 100

// governanceGuard is the validated configuration, plus the in-process
// record of refused merges that could not be taken back (blocks).
type governanceGuard struct {
	cfg         GovernanceGuard
	paths       branchprotect.GovernancePaths
	bases       MergeBaseLister
	maxReported int

	mu sync.Mutex
	// blocks maps a refused merge commit still on the local target to the
	// base it must be taken back to (recordGovernanceBlock).
	blocks map[string]string
}

// CommitMetaGovernanceBlockedTarget is the reserved commit_meta key a run
// carries while a merge the guard refused for it could not be taken back:
// "<merge sha> <base sha>". It survives a restart (the in-process block
// does not), so the run's own retries restore the target before anything
// else (codex review round 2).
const CommitMetaGovernanceBlockedTarget = "spine.governance.blocked_target"

// CommitMetaGovernanceStartedBy is the reserved commit_meta key naming the
// actor who started a workflow-lifecycle planning run (review round 1).
// startWorkflowPlanningRun writes the body the caller sent onto the run
// branch, so that actor authored the run's content, yet no step execution
// or assignment records them; the lifecycle exemption's separation-of-
// duties check needs them (reviewerIsNotAuthor). Written once, at start,
// only while the guard and the exemption are on; reserved, so workflow
// outcomes can never set it and TASK-005 keeps it across bindings.
const CommitMetaGovernanceStartedBy = "spine.governance.started_by"

// errGovernanceTargetBlocked is the cause of the error for a merge refused
// because a refused merge is still on the local target.
var errGovernanceTargetBlocked = errors.New("engine: a merge the governance guard refused is still on the local target")

// governanceRunStripes serialises, within one process, every merge
// attempt of a run while the guard is on (MergeRunBranch) with every
// operator decision on that run (DecideGovernanceMerge) — review round 1.
// Without it:
//
//   - a reject that arrived while an approved attempt was between the
//     gate and the push routed the run back and answered success, and the
//     attempt then merged and published the rejected change anyway (and
//     could not complete the run it found active);
//   - an approve whose write landed after a concurrent reject recreated
//     the approval the reject had revoked, so a resubmit of the same
//     commit after the loop-back merged with no hold.
//
// The stripe is held for the whole attempt or decision. An approve
// records the approval and runs its merge under one hold, so no scheduler
// tick consumes the approval in between and the decision event is always
// emitted for an approval that was recorded. A reject waits for an
// in-flight attempt, then reads the run again: a completed run (the merge
// won) or one whose approved merge already reached the local target is
// answered 409 before anything is written.
//
// Re-entrant through the context (governanceStripesHeld): a call chain
// that holds the stripe and reaches MergeRunBranch again — approve's own
// merge; a workflow whose merge_failed route activates another internal
// merge step — does not wait for itself. Package level and striped by run
// ID like sodBindStripes and recoveryBindingStripes, for the same reasons
// (`spine serve` builds an orchestrator per workspace and the pool
// rebuilds them); two runs on one stripe only serialise needlessly. Each
// stripe is a one-slot semaphore so a waiter honours its context. With
// the guard off nothing is taken (the embedded zero value is unchanged).
//
// Residual (KNOWN-LIMITATIONS "Governance-Path Guard" 9): processes
// sharing one database are not serialised. The approval write and the
// reject's revocation are safe across processes on their own (the
// conditional approval write, the reject's second revocation after its
// route); an approved attempt in flight in another process is not.
var governanceRunStripes = func() (s [64]chan struct{}) {
	for i := range s {
		s[i] = make(chan struct{}, 1)
	}
	return s
}()

// governanceStripesHeld is the context key under which the bitmask of the
// governance stripes the call chain holds is carried (64 stripes, one bit
// each).
type governanceStripesHeld struct{}

func governanceStripe(runID string) uint {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return uint(h.Sum32() % uint32(len(governanceRunStripes)))
}

// lockGovernanceRun takes runID's governance stripe, unless ctx already
// holds it, and returns the context that records it held plus an
// idempotent release (never nil). A free stripe is taken even when ctx is
// already done; a contended one is waited for only until ctx ends, and
// then the error is ErrUnavailable wrapping ctx.Err(), with nothing held.
func lockGovernanceRun(ctx context.Context, runID string) (context.Context, func(), error) {
	idx := governanceStripe(runID)
	held, _ := ctx.Value(governanceStripesHeld{}).(uint64)
	if held&(1<<idx) != 0 {
		return ctx, func() {}, nil
	}
	sem := governanceRunStripes[idx]
	select {
	case sem <- struct{}{}:
	default:
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx, func() {}, domain.NewErrorWithCause(domain.ErrUnavailable,
				fmt.Sprintf("governance guard: gave up waiting for another merge attempt or decision on run %s", runID), ctx.Err())
		}
	}
	var once sync.Once
	return context.WithValue(ctx, governanceStripesHeld{}, held|1<<idx), func() { once.Do(func() { <-sem }) }, nil
}

// lockGovernanceRunIfGuarded is MergeRunBranch's entry: lockGovernanceRun
// while the guard is on, nothing otherwise.
func (o *Orchestrator) lockGovernanceRunIfGuarded(ctx context.Context, runID string) (context.Context, func(), error) {
	if o.governance == nil || !o.bindingOn {
		return ctx, func() {}, nil
	}
	return lockGovernanceRun(ctx, runID)
}

// WithGovernanceGuard configures the governance-path guard. With Enabled
// false it turns the guard off. With Enabled true it returns an error, and
// leaves the guard as it was, when merge binding is off (I2 needs the
// bound commit; a hold nobody could decide would follow), when the
// GitOperator is not a MergeBaseLister, or when a path entry is invalid.
// Call order in every wiring: WithBranchProtectPolicy, EnableMergeBinding,
// WithGovernanceGuard. Like the other With* options it is a setup-time
// call, not safe concurrently with orchestration.
func (o *Orchestrator) WithGovernanceGuard(g GovernanceGuard) error {
	if !g.Enabled {
		o.governance = nil
		return nil
	}
	if !o.MergeBindingEnabled() {
		return fmt.Errorf("engine: the governance guard requires SHA-bound merges (call EnableMergeBinding first)")
	}
	lister, ok := o.git.(MergeBaseLister)
	if !ok || lister == nil {
		return fmt.Errorf("engine: the governance guard requires a GitOperator that implements engine.MergeBaseLister (got %T)", o.git)
	}
	entries := g.Paths
	if len(entries) == 0 {
		entries = branchprotect.DefaultGovernancePaths
	}
	entries = append(append([]string(nil), entries...), g.WorkflowDirs...)
	paths, err := branchprotect.NewGovernancePaths(entries)
	if err != nil {
		return fmt.Errorf("engine: governance guard paths: %w", err)
	}
	maxReported := g.MaxReportedPaths
	if maxReported <= 0 {
		maxReported = defaultMaxReportedPaths
	}
	cfg := g
	cfg.Paths = append([]string(nil), g.Paths...)
	cfg.WorkflowDirs = append([]string(nil), g.WorkflowDirs...)
	o.governance = &governanceGuard{cfg: cfg, paths: paths, bases: lister, maxReported: maxReported}
	return nil
}

// GovernanceGuardEnabled reports whether WithGovernanceGuard turned the
// guard on.
func (o *Orchestrator) GovernanceGuardEnabled() bool { return o.governance != nil }

// GovernanceGuardConfig returns a copy of the configuration the guard was
// enabled with, and false when it is off. For wiring checks and display.
func (o *Orchestrator) GovernanceGuardConfig() (GovernanceGuard, bool) {
	if o.governance == nil {
		return GovernanceGuard{}, false
	}
	cfg := o.governance.cfg
	cfg.Paths = append([]string(nil), cfg.Paths...)
	cfg.WorkflowDirs = append([]string(nil), cfg.WorkflowDirs...)
	return cfg, true
}

// governancePass is what governanceGate decided for one merge attempt,
// carried on the mergeSource to mergePrimaryBound.
type governancePass struct {
	check   *branchprotect.GovernanceCheck
	touched []string
	// consumes: the attempt is allowed by an approval (reason
	// governance_approved), so a successful merge records the consumption
	// marker.
	consumes bool
	// lifecycle is the task path the ADR-008 lifecycle exemption excused,
	// or ""; the post-merge assertion re-checks it against the result.
	lifecycle string
}

// governanceGate runs between resolveMergeSource and the first merge
// (MergeRunBranch). It returns done=true with the error the caller
// returns when the merge must not proceed: a hold (precondition_failed
// wrapping ErrGovernanceApprovalRequired), a merge whose changed paths
// could not be computed this time (service_unavailable; nothing recorded,
// retried), one that can never be evaluated (refuseUnevaluableMerge: the
// permanent-failure route, nil error), a policy failure (internal) or
// another deny (forbidden). On allow it records the pass on src. A nil
// guard, or binding off, is a no-op.
func (o *Orchestrator) governanceGate(ctx context.Context, run *domain.Run, src *mergeSource) (bool, error) {
	if o.governance == nil || !o.bindingOn {
		return false, nil
	}
	log := observe.Logger(ctx)
	if err := o.checkGovernanceBlocks(ctx, run); err != nil {
		return true, err
	}
	gc, err := o.buildGovernanceCheck(ctx, run, *src)
	if err != nil && unevaluableForever(err) {
		return true, o.refuseUnevaluableMerge(ctx, run, src, err)
	}
	if err != nil {
		log.Error("governance guard: cannot compute the merge's changed paths; merge refused",
			"run_id", run.RunID, "bound_sha", src.bound, "error", err)
		return true, domain.NewErrorWithCause(domain.ErrUnavailable,
			"governance guard: cannot compute the merge's changed paths; merge refused", err)
	}

	var actor domain.Actor
	if a := domain.ActorFromContext(ctx); a != nil {
		actor = *a
	}
	decision, reasons, perr := o.policy.Evaluate(ctx, branchprotect.Request{
		Branch:     authoritativeBranch,
		Kind:       branchprotect.OpGovernedMerge,
		Actor:      actor,
		RunID:      run.RunID,
		TraceID:    run.TraceID,
		Governance: gc,
	})
	if perr != nil {
		log.Error("branch-protection evaluation failed on governed merge",
			"run_id", run.RunID, "branch", authoritativeBranch, "error", perr.Error())
		return true, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", perr))
	}
	// Engine-side enforcement: the guard's own decision applies whatever
	// the installed Policy said (a permissive or custom policy cannot drop
	// it), and a deny from either side denies. Only the guard's own
	// decision can create a hold: a custom policy that denies with
	// governance_approval_required while the guard sees nothing to approve
	// would otherwise leave a hold DecideGovernanceMerge refuses
	// (ErrPrecondition) — so a policy-only deny is a plain refusal.
	own, ownReasons := branchprotect.EvaluateGovernance(gc)
	touched := gc.Touched()
	if own == branchprotect.DecisionDeny {
		if hasReason(ownReasons, branchprotect.ReasonGovernanceApprovalRequired) {
			o.recordGovernanceHold(ctx, run, gc, touched)
			log.Info("governance guard: merge held for an operator decision",
				"run_id", run.RunID, "bound_sha", gc.BoundSHA, "path_count", len(touched))
			return true, domain.NewErrorWithCause(domain.ErrPrecondition,
				firstMergeDenyMessage(ownReasons, run.RunID), ErrGovernanceApprovalRequired)
		}
		log.Error("governance guard: merge has no bound commit to evaluate; merge refused",
			"run_id", run.RunID, "reasons", denyReasonCodes(ownReasons))
		return true, domain.NewErrorWithCause(domain.ErrUnavailable,
			firstMergeDenyMessage(ownReasons, run.RunID), errGovernanceUnevaluable)
	}
	if decision == branchprotect.DecisionDeny {
		log.Warn("branch-protection denied governed merge",
			"run_id", run.RunID, "branch", authoritativeBranch, "reasons", denyReasonCodes(reasons))
		return true, domain.NewError(domain.ErrForbidden, firstMergeDenyMessage(reasons, run.RunID))
	}
	// Allowed: a hold still recorded on the run's steps is stale — the
	// approval's own clear failed, or the run's commits reached main
	// another way and nothing is left to approve — and would keep the
	// retry budget from ever applying (INIT-023 wave 4 review).
	if err := o.clearGovernanceHold(ctx, run); err != nil {
		log.Warn("governance guard: a stale hold could not be cleared; the next allowed attempt retries",
			"run_id", run.RunID, "error", err)
	}
	src.gov = &governancePass{
		check:    gc,
		touched:  touched,
		consumes: hasReason(ownReasons, branchprotect.ReasonGovernanceApproved),
	}
	if artifact.IsWorkflowPath(run.TaskPath) && hasPath(gc.ExemptPaths, run.TaskPath) {
		src.gov.lifecycle = run.TaskPath
	}
	if src.gov.consumes {
		log.Info("governance guard: approved governance change proceeds to merge",
			"run_id", run.RunID, "bound_sha", gc.BoundSHA, "path_count", len(touched))
	}
	return false, nil
}

// refuseUnevaluableMerge is the permanent refusal of a merge the guard can
// never evaluate (unevaluableForever; review round 1). Nothing has been
// merged — the gate runs before any repository merges — so it takes
// TASK-005's permanent-failure route: the primary outcome is recorded as
// failed, then the publish step's merge_failed (back to execute in
// task-default) or, on a workflow with no publish step, a failed run. The
// author gets a message they can act on. With the guard off git refuses
// the same unrelated-history merge as a permanent failure too.
func (o *Orchestrator) refuseUnevaluableMerge(ctx context.Context, run *domain.Run, src *mergeSource, cause error) error {
	msg := "governance guard: the merge cannot be evaluated: " + unevaluableReason(cause) + "; nothing was merged"
	switch {
	case errors.Is(cause, errGovernanceNoMergeBase):
		msg += " (build the run branch on " + authoritativeBranch + " and resubmit)"
	case errors.Is(cause, git.ErrOutputTooLarge):
		msg += " (split the change, or have an operator land it directly)"
	}
	violation := &git.GitError{Kind: git.ErrKindPermanent, Op: "governance_guard", Message: msg}
	if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", violation, ""); recErr != nil {
		return recErr
	}
	observe.Logger(ctx).Error("governance guard: the merge can never be evaluated; refused permanently, nothing merged",
		"run_id", run.RunID, "bound_sha", src.bound, "merged_sha", src.sha, "error", cause)
	return o.handlePermanentMergeFailure(ctx, run, violation)
}

func hasReason(reasons []branchprotect.Reason, code branchprotect.ReasonCode) bool {
	for _, r := range reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

// buildGovernanceCheck computes the facts of one governed merge of src:
// the changed paths (union over every merge base of main and src.sha),
// the recorded approval, and the engine's exemptions. src.bound is what
// the approval binds to — never src.sha, which the cascade rebuilds with
// a new timestamp on every attempt. Any git failure, no merge base, or no
// bound commit is an error (fail closed, I3).
func (o *Orchestrator) buildGovernanceCheck(ctx context.Context, run *domain.Run, src mergeSource) (*branchprotect.GovernanceCheck, error) {
	g := o.governance
	if !isBoundObjectID(src.bound) || !isBoundObjectID(src.sha) {
		return nil, fmt.Errorf("no bound merge commit (bound %q, merged %q)", truncateForLog(src.bound), truncateForLog(src.sha))
	}
	bases, err := g.bases.MergeBases(ctx, authoritativeBranch, src.sha)
	if err != nil {
		return nil, fmt.Errorf("merge bases of %s and %s: %w", authoritativeBranch, src.sha, err)
	}
	if len(bases) == 0 {
		return nil, fmt.Errorf("%w (%s and %s have no merge base)", errGovernanceNoMergeBase, authoritativeBranch, src.sha)
	}
	changed, err := o.changedPathsFrom(ctx, bases, src.sha)
	if err != nil {
		return nil, err
	}
	gc := &branchprotect.GovernanceCheck{
		Paths:        o.governedPathsFor(changed),
		BoundSHA:     src.bound,
		ChangedPaths: changed,
		Approval:     governanceApprovalFrom(run.CommitMeta),
	}
	gc.ExemptPaths = append(gc.ExemptPaths, o.ownEvidenceExemptions(run, changed)...)
	gc.ExemptPaths = append(gc.ExemptPaths, o.lifecycleExemption(ctx, run, src, bases, changed)...)
	return gc, nil
}

// governedPathsFor is the guarded set for one decision over the changed
// paths (INIT-024 TASK-002, review finding N-5). The set WithGovernanceGuard
// built — Paths (or the defaults) plus WorkflowDirs — was fixed at setup
// and held only what the embedder listed, while the projection installs
// the direct *.yaml / *.yml children of workflows/ AND of
// <artifacts_dir>/workflows/ as live policy: an embedder that configured
// the projection's artifacts_dir without passing WorkflowDirs left that
// directory's workflow definitions to merge unreviewed. The set is
// therefore built at decision time from three sources:
//
//	(a) the configured set (g.paths), as before;
//	(b) <ArtifactsDir()>/workflows when the orchestrator has an artifacts
//	    directory — read here, not in WithGovernanceGuard, so the order
//	    of WithArtifactsDir and WithGovernanceGuard does not matter; the
//	    entry also covers nested files (workflows/steps/x.yaml);
//	(c) the structural backstop: the parent of every changed path that
//	    looks like a workflow definition — a *.yaml / *.yml leaf directly
//	    inside a directory named "workflows" at any depth, case-folded
//	    (branchprotect.WorkflowDefinitionDirs). This over-approximates
//	    the projection's set for every possible artifacts_dir, so a merge
//	    is held even when neither the guard nor the orchestrator knows
//	    the projection's directory.
//
// (b) and (c) are added as literal prefix entries
// (GovernancePaths.WithDirs); Match is unchanged. With the guard off none
// of this runs (the zero value holds nothing). The post-merge assertion
// applies (c) to the paths the merge actually changed as well
// (checkGovernedMerge), so rename following into such a directory is
// refused like rename following into workflows/.
func (o *Orchestrator) governedPathsFor(changed []string) branchprotect.GovernancePaths {
	var dirs []string
	if d := o.ArtifactsDir(); d != "" {
		dirs = append(dirs, d+"/workflows")
	}
	dirs = append(dirs, branchprotect.WorkflowDefinitionDirs(changed)...)
	return o.governance.paths.WithDirs(dirs...)
}

// changedPathsFrom is the union of Path and OldPath of Diff(B, to) over
// every base B. A rename out of a governed tree counts through OldPath,
// a deletion through Path.
func (o *Orchestrator) changedPathsFrom(ctx context.Context, bases []string, to string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, b := range bases {
		if !isBoundObjectID(b) {
			return nil, fmt.Errorf("malformed merge base %q", truncateForLog(b))
		}
		diffs, err := o.git.Diff(ctx, b, to)
		if err != nil {
			return nil, fmt.Errorf("diff %s..%s: %w", b, to, err)
		}
		for _, d := range diffs {
			add(d.Path)
			if d.OldPath != "" {
				add(d.OldPath)
			}
		}
	}
	return out, nil
}

// ownEvidenceExemptions returns the changed paths under the run's own
// evidence directory, `.spine/runs/<run id>/evidence/`, compared
// byte-exactly (owner question 9). Another run's directory stays governed:
// writing it would forge that run's evidence. A path whose remainder has
// an empty, "." or ".." segment is never exempt.
func (o *Orchestrator) ownEvidenceExemptions(run *domain.Run, changed []string) []string {
	if !o.governance.cfg.ExemptOwnRunEvidence || run.RunID == "" || strings.ContainsAny(run.RunID, "/\\") {
		return nil
	}
	prefix := ".spine/runs/" + run.RunID + "/evidence/"
	var out []string
	for _, p := range changed {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" || !cleanRelative(rest) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// cleanRelative reports whether a relative path has no empty, "." or ".."
// segment.
func cleanRelative(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// lifecycleExemption returns [run.TaskPath] when an ADR-008
// workflow-lifecycle planning run may merge its own workflow file without
// an operator approval (owner question 3), and nil otherwise. Every
// condition must hold; any failure to read or decide grants nothing (fail
// toward approval):
//
//   - the deployment enables the exemption;
//   - the run is a planning run whose task path is a workflow path it
//     actually changes, and is not the governing workflow's own path (the
//     governor cannot approve its own edit);
//   - the pinned governing workflow declares applies_to: [Workflow];
//   - exact path shape: run.TaskPath == "workflows/<id>.yaml" for the id
//     parsed from the file being merged, byte-exact (StartPlanningRun
//     takes a caller-supplied path, so a front-matter-wrapped
//     workflows/x.yaml can produce this run shape too);
//   - neither the merged file nor its version at any merge base or at the
//     target's current tip declares applies_to: [Workflow], in any letter
//     case — and the post-merge assertion re-checks the merged result
//     (lifecycleResultStillExempt) — (an exempt edit could
//     otherwise add a second workflow-governing binding — every lifecycle
//     run would then fail to resolve — or shadow
//     <artifacts_dir>/workflows/workflow-lifecycle.yaml from the root);
//   - local separation of duties: the last completed main-path review step
//     was completed by a named actor who neither started the run (the
//     recorded CommitMetaGovernanceStartedBy; none recorded grants
//     nothing) nor authored it in the engine's sense (runAuthor: bound to
//     any author step execution of the run, in any status, or holding an
//     assignment row of one), whatever the deployment's step-entry policy
//     says (in standalone one reviewer could otherwise self-grant
//     workflow_review, draft or start the run with a review-less
//     task-default body, self-review and land it with no operator).
func (o *Orchestrator) lifecycleExemption(ctx context.Context, run *domain.Run, src mergeSource, bases, changed []string) []string {
	if !o.governance.cfg.ExemptWorkflowLifecycleTaskPath {
		return nil
	}
	if run.Mode != domain.RunModePlanning || !artifact.IsWorkflowPath(run.TaskPath) || run.TaskPath == run.WorkflowPath {
		return nil
	}
	if !hasPath(changed, run.TaskPath) {
		return nil
	}
	log := observe.Logger(ctx)
	deny := func(why string, err error) []string {
		log.Info("governance guard: lifecycle exemption not granted; the change needs an operator approval",
			"run_id", run.RunID, "task_path", run.TaskPath, "reason", why, "error", err)
		return nil
	}
	gov, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil || gov == nil {
		return deny("governing workflow unavailable", err)
	}
	if !declaresWorkflowGovernance(gov.AppliesTo) {
		return deny("governing workflow does not govern workflows", nil)
	}
	content, err := o.git.ReadFile(ctx, src.sha, run.TaskPath)
	if err != nil {
		return deny("merged workflow file unreadable", err)
	}
	def, err := workflow.Parse(run.TaskPath, content)
	if err != nil {
		return deny("merged workflow file does not parse", err)
	}
	if run.TaskPath != workflow.FilePath(def.ID) {
		return deny("task path is not workflows/<parsed id>.yaml", nil)
	}
	if declaresWorkflowGovernance(def.AppliesTo) {
		return deny("the edited workflow governs workflows", nil)
	}
	// Every version the merge combines with the run's: each merge base's,
	// and the target's current one (codex review, review round 1) — main
	// may have made the file govern workflows after the run forked, and a
	// clean merge would carry the run's edit into that version.
	tip, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
	if err != nil {
		return deny("the target's current version unreadable", err)
	}
	for _, ref := range append(append([]string(nil), bases...), tip) {
		governs, err := o.workflowVersionGoverns(ctx, ref, run.TaskPath)
		if err != nil {
			return deny("previous workflow version unreadable or does not parse", err)
		}
		if governs {
			return deny("a previous version (a merge base's or the target's) governs workflows", nil)
		}
	}
	if ok, why := o.reviewerIsNotAuthor(ctx, run, gov); !ok {
		return deny(why, nil)
	}
	return []string{run.TaskPath}
}

// workflowVersionGoverns reports whether the version of path at ref
// declares applies_to: [Workflow]. A file missing at ref does not (it is
// new since then); one that cannot be read or parsed is an error, and the
// caller grants nothing.
func (o *Orchestrator) workflowVersionGoverns(ctx context.Context, ref, path string) (bool, error) {
	prev, err := o.git.ReadFile(ctx, ref, path)
	if err != nil {
		var gitErr *git.GitError
		if errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
			return false, nil
		}
		return false, err
	}
	def, err := workflow.Parse(path, prev)
	if err != nil {
		return false, err
	}
	return declaresWorkflowGovernance(def.AppliesTo), nil
}

// lifecycleResultStillExempt re-checks the lifecycle exemption against the
// merge that was actually made (codex review, review round 1): the merged
// file must still be workflows/<parsed id>.yaml and govern no workflows,
// and the target before the merge must not have made it govern workflows.
// The gate read the target's version, but the target can move between the
// gate and the merge (other runs' merges, operator pushes), and a clean
// merge then combines the run's edit with a workflow-governing version the
// gate never saw. Any read or parse failure fails the check.
func (o *Orchestrator) lifecycleResultStillExempt(ctx context.Context, run *domain.Run, res git.MergeResult) (bool, string) {
	content, err := o.git.ReadFile(ctx, res.SHA, run.TaskPath)
	if err != nil {
		return false, "merged workflow file unreadable"
	}
	def, err := workflow.Parse(run.TaskPath, content)
	if err != nil {
		return false, "merged workflow file does not parse"
	}
	if run.TaskPath != workflow.FilePath(def.ID) {
		return false, "task path is not workflows/<parsed id>.yaml in the merged result"
	}
	if declaresWorkflowGovernance(def.AppliesTo) {
		return false, "the merged workflow governs workflows"
	}
	if governs, err := o.workflowVersionGoverns(ctx, res.BaseSHA, run.TaskPath); err != nil || governs {
		return false, "the target's version before the merge governs workflows (or is unreadable)"
	}
	return true, ""
}

func declaresWorkflowGovernance(appliesTo []string) bool {
	for _, a := range appliesTo {
		if strings.EqualFold(strings.TrimSpace(a), string(domain.ArtifactTypeWorkflow)) {
			return true
		}
	}
	return false
}

// withoutPath returns a copy of xs without s.
func withoutPath(xs []string, s string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func hasPath(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// reviewerIsNotAuthor is the exemption's local separation-of-duties
// check: the actor of the last completed main-path review step execution
// is named, is not the actor who started the run, and is not a run author
// (runAuthor, the definition TASK-007's separation of duties uses).
//
// Review round 1: the check used to compare the reviewer only with actors
// who COMPLETED a non-review step. The body of a lifecycle run is written
// onto the branch by whoever starts it (PUT/POST /workflows below
// operator), and the draft step is assigned to someone else, so a
// reviewer could start the run with a review-less body, let another actor
// submit the draft unchanged and approve it alone. The starter is now
// recorded at start (CommitMetaGovernanceStartedBy); a run without that
// record — started before the upgrade, or by a caller with no actor —
// gets no exemption. runAuthor also counts an author execution the
// reviewer was bound to and released or abandoned (its ledger rows), and
// so a write_context write, which TASK-014 limits to the assignee.
func (o *Orchestrator) reviewerIsNotAuthor(ctx context.Context, run *domain.Run, wf *domain.WorkflowDefinition) (bool, string) {
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return false, "step executions unavailable"
	}
	isReview := func(stepID string) bool {
		sd := findStepDef(wf, stepID)
		return sd != nil && sd.Type == domain.StepTypeReview
	}
	var last *domain.StepExecution
	for i := range execs {
		e := &execs[i]
		if e.Status != domain.StepStatusCompleted || e.BranchID != "" || !isReview(e.StepID) {
			continue
		}
		if last == nil || completedAfter(e, last) {
			last = e
		}
	}
	if last == nil || last.ActorID == "" {
		return false, "no completed review step with a named actor"
	}
	starter := run.CommitMeta[CommitMetaGovernanceStartedBy]
	if starter == "" {
		return false, "the run's starter is not recorded"
	}
	if starter == last.ActorID {
		return false, "the reviewer started the run (and wrote its body)"
	}
	stepID, err := o.runAuthor(ctx, run, wf, last, last.ActorID, execs)
	if err != nil {
		return false, "the run's authors could not be determined"
	}
	if stepID != "" {
		return false, "the reviewer is an author of the run (step " + stepID + ")"
	}
	return true, ""
}

// recordLifecycleStarter records who started a workflow-lifecycle planning
// run (CommitMetaGovernanceStartedBy), for the lifecycle exemption's
// separation-of-duties check. Only while the guard and the exemption are
// on (the zero value writes nothing). No actor on ctx, or a failed write,
// records nothing — the run then gets no exemption, and its workflow edit
// needs an operator approval (fail toward approval); the run itself starts
// as before.
func (o *Orchestrator) recordLifecycleStarter(ctx context.Context, runID string) {
	if o.governance == nil || !o.governance.cfg.ExemptWorkflowLifecycleTaskPath {
		return
	}
	a := domain.ActorFromContext(ctx)
	if a == nil || a.ActorID == "" {
		return
	}
	if err := o.mergeRunCommitMeta(ctx, runID, map[string]string{CommitMetaGovernanceStartedBy: a.ActorID}); err != nil {
		observe.Logger(ctx).Warn("governance guard: the lifecycle run's starter was not recorded; its workflow edit will need an operator approval",
			"run_id", runID, "error", err)
	}
}

// completedAfter orders completed executions by completion time, then by
// attempt.
func completedAfter(a, b *domain.StepExecution) bool {
	switch {
	case a.CompletedAt != nil && b.CompletedAt != nil && !a.CompletedAt.Equal(*b.CompletedAt):
		return a.CompletedAt.After(*b.CompletedAt)
	case a.CompletedAt != nil && b.CompletedAt == nil:
		return true
	case a.CompletedAt == nil && b.CompletedAt != nil:
		return false
	}
	return a.Attempt > b.Attempt
}

// governanceApprovalFrom parses the reserved approval keys. A missing or
// malformed value (including a reject's empty tombstone) is no approval.
// The role is parsed as recorded; EvaluateGovernance checks it.
func governanceApprovalFrom(meta map[string]string) *branchprotect.GovernanceApproval {
	sha := meta[domain.CommitMetaGovernanceApprovalSHA]
	if !isBoundObjectID(sha) {
		return nil
	}
	actorID := meta[domain.CommitMetaGovernanceApprovalActorID]
	role := domain.ActorRole(meta[domain.CommitMetaGovernanceApprovalActorRole])
	if actorID == "" || role.RoleLevel() == 0 {
		return nil
	}
	at, err := time.Parse(time.RFC3339, meta[domain.CommitMetaGovernanceApprovalApprovedAt])
	if err != nil {
		return nil
	}
	return &branchprotect.GovernanceApproval{SHA: sha, Actor: domain.Actor{ActorID: actorID, Role: role}, ApprovedAt: at}
}

// commitMetaMerger is the optional RunStore capability for an atomic
// partial commit_meta write (JSONB ||). RunStore does not change, so
// embedders' stores keep compiling; without it the engine falls back to
// read-modify-write.
type commitMetaMerger interface {
	MergeCommitMeta(ctx context.Context, runID string, patch map[string]string) error
}

// mergeRunCommitMeta writes patch over the run's commit_meta, keeping
// every other key. The read-modify-write fallback is safe only because
// the guard writes only committing runs, where routeStepOutcome (whose
// writes replace the map) does not run; concurrent engine writes to the
// same run in committing (a scheduler attempt's consumption marker and an
// operator decision) can lose one another's keys there — stores should
// implement commitMetaMerger (*store.PostgresStore does).
func (o *Orchestrator) mergeRunCommitMeta(ctx context.Context, runID string, patch map[string]string) error {
	if m, ok := o.store.(commitMetaMerger); ok {
		return m.MergeCommitMeta(ctx, runID, patch)
	}
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	meta := make(map[string]string, len(run.CommitMeta)+len(patch))
	for k, v := range run.CommitMeta {
		meta[k] = v
	}
	for k, v := range patch {
		meta[k] = v
	}
	return o.store.SetCommitMeta(ctx, runID, meta)
}

// governanceHoldTarget is the step a hold is recorded on, and whose
// execution id names the hold episode: the run's active publish step, or
// — on a workflow with no publish step, or none live — its last completed
// step (the step whose outcome triggered the merge, as
// recordGitConflictOnStep uses). nil when there is none. A workflow or
// step-execution read that fails is an error, never "no publish step"
// (INIT-023 wave 4 review): the hold record, its clearing and the retry
// budget's held test would otherwise each pick a different step.
func (o *Orchestrator) governanceHoldTarget(ctx context.Context, run *domain.Run) (*domain.StepExecution, error) {
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err == nil && wfDef == nil {
		err = errors.New("no workflow definition")
	}
	if err != nil {
		return nil, fmt.Errorf("load the run's workflow: %w", err)
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return nil, fmt.Errorf("list the run's step executions: %w", err)
	}
	if id := publishStepID(wfDef); id != "" {
		for i := range execs {
			if execs[i].StepID == id && !execs[i].Status.IsTerminal() {
				return &execs[i], nil
			}
		}
	}
	var last *domain.StepExecution
	for i := range execs {
		if execs[i].Status == domain.StepStatusCompleted {
			last = &execs[i]
		}
	}
	return last, nil
}

// publishStepID is the workflow's publish step (type internal, handler
// merge), or "".
func publishStepID(wfDef *domain.WorkflowDefinition) string {
	for i := range wfDef.Steps {
		s := &wfDef.Steps[i]
		if s.Type == domain.StepTypeInternal && s.Execution != nil && s.Execution.Handler == "merge" {
			return s.ID
		}
	}
	return ""
}

// isGovernanceHold reports a hold ErrorDetail (recordGovernanceHold).
func isGovernanceHold(d *domain.ErrorDetail) bool {
	return d != nil && d.Classification == domain.FailureGovernanceApprovalRequired && d.RuleID == GovernanceHoldRuleID
}

// recordGovernanceHold records a held merge: the hold ErrorDetail on the
// target step (written only when it changes, so re-evaluation on every
// scheduler tick makes no write) and one governance.merge_held event per
// bound commit and episode (deterministic id; the event log and the
// delivery queue de-duplicate on it). The episode is the target step's
// execution id: a new publish execution — or a new review — after a
// reject and resubmit gets a new event even for the same commit.
func (o *Orchestrator) recordGovernanceHold(ctx context.Context, run *domain.Run, gc *branchprotect.GovernanceCheck, touched []string) {
	log := observe.Logger(ctx)
	target, err := o.governanceHoldTarget(ctx, run)
	if err != nil {
		// Neither the hold nor its event is recorded on a guessed step
		// (INIT-023 wave 4 review); the merge is still held, and the next
		// attempt records both.
		log.Warn("governance guard: the hold could not be recorded this time; the merge is held all the same",
			"run_id", run.RunID, "error", err)
		return
	}
	episode := "none"
	if target != nil {
		episode = target.ExecutionID
		detail := &domain.ErrorDetail{
			Classification: domain.FailureGovernanceApprovalRequired,
			Message:        branchprotect.GovernanceHoldMessage(gc, touched),
			StepID:         target.StepID,
			RuleID:         GovernanceHoldRuleID,
		}
		if !reflect.DeepEqual(target.ErrorDetail, detail) {
			// Conditional on the row the hold target read (INIT-023
			// TASK-008's step compare-and-swap, when the store has it;
			// INIT-023 wave 4 integration): a stale copy of a step another
			// attempt or decision moved meanwhile is never written back
			// over it.
			prior := stateOf(target)
			target.ErrorDetail = detail
			if err := o.writeStepExecution(ctx, target, prior); err != nil {
				log.Warn("governance guard: failed to record the hold on the step",
					"run_id", run.RunID, "step_id", target.StepID, "error", err)
			}
		}
	}
	stale := ""
	if a := gc.Approval; a != nil && !strings.EqualFold(a.SHA, gc.BoundSHA) {
		stale = a.SHA
	}
	o.emitGovernanceHeld(ctx, run, gc, touched, episode, stale, false, nil)
}

// emitGovernanceHeld emits governance.merge_held. postMerge marks a
// post-merge refusal, which names the paths the evaluation did not cover
// (extra) and gets its own event id.
func (o *Orchestrator) emitGovernanceHeld(ctx context.Context, run *domain.Run, gc *branchprotect.GovernanceCheck, touched []string, episode, stale string, postMerge bool, extra []string) {
	paths, _ := o.capPaths(touched)
	payload := map[string]any{
		"run_id":       run.RunID,
		"task_path":    run.TaskPath,
		"bound_sha":    gc.BoundSHA,
		"paths":        paths,
		"path_count":   len(touched),
		"exempt_paths": nonNil(gc.ExemptPaths),
		"post_merge":   postMerge,
	}
	if stale != "" {
		payload["stale_approval_sha"] = stale
	}
	id := fmt.Sprintf("evt-%s-governance-held-%s-%s", shortTraceID(run.TraceID), shortSHA12(gc.BoundSHA), episode)
	if postMerge {
		capped, _ := o.capPaths(extra)
		payload["extra_paths"] = capped
		payload["extra_path_count"] = len(extra)
		id += "-post-merge"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = nil
	}
	o.emitEvent(ctx, domain.EventGovernanceMergeHeld, run.RunID, run.TraceID, id, raw)
}

// capPaths returns at most the guard's MaxReportedPaths entries and
// whether the list was cut.
func (o *Orchestrator) capPaths(paths []string) ([]string, bool) {
	limit := defaultMaxReportedPaths
	if o.governance != nil {
		limit = o.governance.maxReported
	}
	if len(paths) > limit {
		return append([]string(nil), paths[:limit]...), true
	}
	return nonNil(paths), false
}

func nonNil(ps []string) []string {
	if ps == nil {
		return []string{}
	}
	return append([]string(nil), ps...)
}

func shortTraceID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func shortSHA12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// governedCheck is what checkGovernedMerge decided inside the primary
// merge block, for settleGovernedRefusal to record and route after the
// block has released the shared working tree.
type governedCheck struct {
	// refused: nothing may be pushed. The take-back already ran (tb).
	refused bool
	// extra: governed paths the merge changed that the evaluation did not
	// cover.
	extra []string
	// diffErr: the merge's changes could not be read.
	diffErr error
	// markerErr: the accepted merge could not be recorded — the
	// consumption marker (consumes) or the end of the merge-in-flight
	// record (INIT-023 wave 4 review) — so it was taken back.
	markerErr error
	consumes  bool
	tb        takeBack
	// marker is the consumption marker an accepted, approved merge must
	// record before its push (nil otherwise); mergePrimaryBound writes it
	// with the end of the merge-in-flight record (acceptedMergePatch).
	marker map[string]string
}

// acceptedMergePatch is the one commit_meta write that records an
// accepted merge before its push: the consumption marker, if any, and the
// end of the attempt's merge-in-flight record when one was written.
func acceptedMergePatch(marker map[string]string, inFlight bool) map[string]string {
	if marker == nil && !inFlight {
		return nil
	}
	patch := make(map[string]string, len(marker)+1)
	for k, v := range marker {
		patch[k] = v
	}
	if inFlight {
		patch[CommitMetaMergeUnverified] = ""
	}
	return patch
}

// checkGovernedMerge is the post-merge assertion (§3.4a) and, for an
// approved merge, the consumption marker. It runs in mergePrimaryBound's
// git-only block on the shared working tree (INIT-023 TASK-012), after
// verifyBoundMerge and TASK-017's V1 accepted the merge and before the
// push the block holds the tree across: a refusal is taken back to
// BaseSHA there, before the tree is released, so no later holder builds
// on, or publishes, the refused merge (INIT-023 wave 4 integration). What
// it decided is recorded and routed after the block (settleGovernedRefusal).
//
// The governed paths the merge changed — Diff(BaseSHA, M), Path and
// OldPath, the own-run evidence carve-out and the evaluation's exemptions
// applied — must be covered by the paths the gate evaluated. With no
// approval that set is empty, so any governed path is a violation. A
// violation (merge behaviour the pre-merge evaluation cannot see: rename
// following into a governed path main renamed, under default
// configuration, or non-default merge configuration) is a permanent
// refusal (owner question 6) whose message tells the author to update
// the run branch from main; a Diff failure is too, unless it is a
// retryable git error and the take-back was clean (the attempt then
// simply runs again, as TASK-005 treats a transient read in its own
// check). "Already up to date" (M == BaseSHA, a replay) changed nothing
// and keeps the marker an earlier attempt wrote.
//
// The marker is returned (governedCheck.marker), not written: the block
// writes it before the push, together with the end of the attempt's
// merge-in-flight record (acceptedMergePatch; INIT-023 wave 4 review). It
// must name the merge commit, which exists only after MergeBound, and land
// before the push (§3.8 note). That write and the merge-in-flight record
// are the store writes inside a merge block (besides the run-status
// re-check, a read): each writes one row of runtime.runs and neither emits
// nor routes, so it cannot deadlock with a holder that needs the tree
// (worktree.go).
func (o *Orchestrator) checkGovernedMerge(ctx context.Context, run *domain.Run, src *mergeSource, res git.MergeResult) governedCheck {
	pass := src.gov
	if pass == nil || res.SHA == res.BaseSHA {
		return governedCheck{}
	}
	log := observe.Logger(ctx)
	gc := pass.check
	diffs, derr := o.git.Diff(ctx, res.BaseSHA, res.SHA)
	var extra []string
	if derr == nil {
		var actual []string
		for _, d := range diffs {
			actual = append(actual, d.Path)
			if d.OldPath != "" {
				actual = append(actual, d.OldPath)
			}
		}
		exempt := gc.ExemptPaths
		if pass.lifecycle != "" {
			if ok, why := o.lifecycleResultStillExempt(ctx, run, res); !ok {
				log.Warn("governance guard: the lifecycle exemption does not hold for the merged result; the workflow edit is not covered",
					"run_id", run.RunID, "task_path", pass.lifecycle, "merge_sha", res.SHA, "reason", why)
				exempt = withoutPath(exempt, pass.lifecycle)
			}
		}
		post := &branchprotect.GovernanceCheck{
			// The evaluated set plus the backstop over what the merge
			// actually changed (INIT-024 TASK-002): rename following can
			// land a definition in a "workflows" directory the
			// pre-merge diff never named.
			Paths:        gc.Paths.WithDirs(branchprotect.WorkflowDefinitionDirs(actual)...),
			ChangedPaths: actual,
			ExemptPaths:  append(append([]string(nil), exempt...), o.ownEvidenceExemptions(run, actual)...),
		}
		covered := make(map[string]struct{}, len(pass.touched))
		for _, p := range pass.touched {
			covered[p] = struct{}{}
		}
		for _, p := range post.Touched() {
			if _, ok := covered[p]; !ok {
				extra = append(extra, p)
			}
		}
	}
	if derr == nil && len(extra) == 0 {
		if pass.consumes {
			return governedCheck{marker: map[string]string{
				domain.CommitMetaGovernanceApprovalMergeSHA: res.SHA,
				domain.CommitMetaGovernanceApprovalBaseSHA:  res.BaseSHA,
			}}
		}
		return governedCheck{}
	}
	return governedCheck{refused: true, extra: extra, diffErr: derr, tb: o.takeBackGovernedMerge(ctx, res)}
}

// settleGovernedRefusal records and routes a merge checkGovernedMerge
// refused, after the merge block released the tree (events, outcome rows
// and routing never run inside a block). It returns what
// mergePrimaryBound returns.
func (o *Orchestrator) settleGovernedRefusal(ctx context.Context, run *domain.Run, src *mergeSource, res git.MergeResult, gv governedCheck) (bool, error) {
	log := observe.Logger(ctx)
	tb, extra, derr := gv.tb, gv.extra, gv.diffErr
	if gv.markerErr != nil {
		// Also reached with the guard off (V1 alone): the write that
		// records the accepted merge carries the end of the merge-in-flight
		// record too (INIT-023 wave 4 review).
		if !tb.clean {
			// The merge is still on the local target without its
			// marker (codex review round 3): a retry would find it
			// up to date, skip the marker and push it, and the
			// approved change would have no provenance. Block until
			// the target is restored, like a refused merge. The
			// merge-in-flight record, still on the run, makes the
			// next attempt take it back first.
			o.blockGovernedMergesOn(ctx, run, res.SHA, res.BaseSHA)
		}
		if gv.consumes {
			log.Error("governance guard: consumption marker not recorded; merge taken back, nothing pushed, the run stays committing",
				"run_id", run.RunID, "merge_sha", res.SHA, "take_back", tb.detail, "error", gv.markerErr)
			return false, fmt.Errorf("record governance consumption marker: %w", gv.markerErr)
		}
		log.Error("merge binding: the accepted merge could not be recorded; merge taken back, nothing pushed, the run stays committing",
			"run_id", run.RunID, "merge_sha", res.SHA, "take_back", tb.detail, "error", gv.markerErr)
		return false, fmt.Errorf("record the accepted merge: %w", gv.markerErr)
	}
	pass := src.gov
	gc := pass.check

	if !tb.clean {
		// The refused merge is still on the local target (codex review
		// round 2): routing the run away would leave it there for the next
		// governed merge — of any run — to build on and publish. Block every
		// governed merge until the target is restored, and keep this run in
		// committing so its retries keep trying the take-back.
		o.recordGovernanceBlock(ctx, run, res.SHA, res.BaseSHA)
		blocked := &git.GitError{Kind: git.ErrKindTransient, Op: "governance_guard",
			Message: "governance guard: a refused merge could not be taken back; governed merges are blocked until the local " +
				authoritativeBranch + " is restored to " + res.BaseSHA + " (will retry): " + tb.detail}
		if recErr := o.recordPrimaryMergeOutcome(ctx, run, res.SHA, blocked, ""); recErr != nil {
			return false, recErr
		}
		log.Error("governance guard: refused merge could not be taken back; governed merges are blocked until the local target is restored",
			"run_id", run.RunID, "merge_sha", res.SHA, "base_sha", res.BaseSHA, "extra_path_count", len(extra), "error", derr)
		o.emitGovernanceHeld(ctx, run, gc, pass.touched, "attempt-"+shortSHA12(res.SHA), "", true, extra)
		return true, o.retryMerge(ctx, run)
	}
	if derr != nil {
		var gitErr *git.GitError
		if errors.As(derr, &gitErr) && gitErr.IsRetryable() {
			retry := &git.GitError{Kind: git.ErrKindTransient, Op: "governance_guard",
				Message: "governance guard: the merge's changes could not be read (will retry): " + derr.Error() + "; " + tb.detail}
			if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", retry, ""); recErr != nil {
				return false, recErr
			}
			log.Warn("governance guard: post-merge check could not read the merge, will retry",
				"run_id", run.RunID, "merge_sha", res.SHA, "error", derr)
			return true, o.retryMerge(ctx, run)
		}
	}
	msg := "merge changed governed paths not covered by the evaluation: " + strings.Join(quotePaths(extra, governanceMessageLimit), ", ") +
		" (git carried the run's changes onto them, e.g. following a rename on " + authoritativeBranch +
		" since the run branch forked; update the run branch from " + authoritativeBranch +
		" and resubmit: the change is then evaluated and held for an operator decision)"
	if derr != nil {
		msg = "the merge's changed paths could not be read: " + derr.Error()
	}
	violation := &git.GitError{Kind: git.ErrKindPermanent, Op: "governance_guard", Message: msg + "; " + tb.detail}
	if recErr := o.recordPrimaryMergeOutcome(ctx, run, res.SHA, violation, ""); recErr != nil {
		return false, recErr
	}
	log.Error("governance guard: post-merge assertion refused the merge; nothing pushed",
		"run_id", run.RunID, "bound_sha", src.bound, "merge_sha", res.SHA, "base_sha", res.BaseSHA,
		"extra_path_count", len(extra), "error", derr)
	o.emitGovernanceHeld(ctx, run, gc, pass.touched, "attempt-"+shortSHA12(res.SHA), "", true, extra)
	return true, o.handlePermanentMergeFailure(ctx, run, violation)
}

// governanceMessageLimit caps the paths named in an outcome message.
const governanceMessageLimit = 5

func quotePaths(ps []string, limit int) []string {
	var out []string
	for i, p := range ps {
		if i == limit {
			out = append(out, "…")
			break
		}
		out = append(out, fmt.Sprintf("%q", p))
	}
	return out
}

// takeBackGovernedMerge restores the local target to exactly BaseSHA after
// the guard refused a merge that verifyBoundMerge had accepted (or after a
// failed marker write): takeBackToBase.
func (o *Orchestrator) takeBackGovernedMerge(ctx context.Context, res git.MergeResult) takeBack {
	return o.takeBackToBase(ctx, res, "governance guard")
}

// takeBackToBase restores the local target to exactly BaseSHA after a
// check that runs once verifyBoundMerge has accepted the merge refused it
// — TASK-006's post-merge assertion or failed marker write, TASK-017's V1
// (who names it in the log). Unlike takeBackRefusedMerge it never moves
// the target to a first parent: an accepted merge is [BaseSHA, S], a
// fast-forward to S, or nothing, so the target held BaseSHA before it —
// and for a fast-forward, S's own first parent is run-branch content,
// which must not stay on the target. Compare-and-swap through
// ResetBranch, inside the primary merge block (git.CLIClient's armed,
// tree-locked take-back on the shared tree); a failure is logged at Error
// (the local target then still holds the refused merge and must be reset
// before the next merge).
func (o *Orchestrator) takeBackToBase(ctx context.Context, res git.MergeResult, who string) takeBack {
	if res.SHA == res.BaseSHA {
		return takeBack{detail: "the local " + authoritativeBranch + " was not moved", clean: true}
	}
	if !isBoundObjectID(res.SHA) || !isBoundObjectID(res.BaseSHA) {
		observe.Logger(ctx).Error(who+": refused merge NOT taken back (merge result incomplete); reset the local branch before the next governed merge",
			"branch", authoritativeBranch, "merge_sha", res.SHA, "base_sha", res.BaseSHA)
		return takeBack{detail: "the local " + authoritativeBranch + " may still hold the refused merge (merge result incomplete)"}
	}
	if err := o.binder.ResetBranch(ctx, authoritativeBranch, res.BaseSHA, res.SHA); err != nil {
		observe.Logger(ctx).Error(who+": refused merge NOT taken back; reset the local branch before the next governed merge",
			"branch", authoritativeBranch, "merge_sha", res.SHA, "restore_to", res.BaseSHA, "error", err)
		return takeBack{detail: "the local " + authoritativeBranch + " still holds the refused merge (take-back failed)", target: res.BaseSHA}
	}
	return takeBack{detail: "the local " + authoritativeBranch + " was restored to " + res.BaseSHA, clean: true, restoredTo: res.BaseSHA, target: res.BaseSHA}
}

// blockGovernedMergesOn records a refused merge whose take-back failed as
// a governance block (recordGovernanceBlock) while the guard is on,
// whichever check refused it (INIT-023 wave 4 integration). With the
// guard off, or without the commits to name, it does nothing: the shared
// tree's pending take-back still refuses every later holder (INIT-023
// TASK-012), and an embedder's operator keeps TASK-005's behaviour.
func (o *Orchestrator) blockGovernedMergesOn(ctx context.Context, run *domain.Run, merge, restoreTo string) {
	if o.governance == nil || !o.bindingOn || !isBoundObjectID(merge) || !isBoundObjectID(restoreTo) {
		return
	}
	o.recordGovernanceBlock(ctx, run, merge, restoreTo)
}

// governanceKeepsCommitting reports why the merge retry budget
// (AbandonExhaustedMerge, INIT-023 TASK-012) must not abandon a committing
// run while the guard is on (INIT-023 wave 4 integration), or "":
//
//   - a refused merge of the run is still blocked on the local target
//     (CommitMetaGovernanceBlockedTarget) and still in its history: like a
//     merge that landed locally, routing the run away would contradict the
//     branch, and its retries are what keep trying the take-back after a
//     restart. A recorded block whose merge is no longer in the local
//     target's history (another run's gate took it back, an operator reset
//     the branch) is stale: the key is cleared and the budget applies
//     (INIT-023 wave 4 review);
//   - the merge is held for an operator decision (the hold target carries
//     the hold): a hold records no attempt, but the transient failures
//     before the gate on the held run's ticks do, and a long hold must not
//     turn them into an abandonment the operator never decided. The
//     approval clears the hold (as does any attempt the gate allows); the
//     approved merge's own transient failures are bounded again.
//
// A read it needs that fails is returned (INIT-023 wave 4 review): the
// abandonment then skips the tick instead of deciding "not held".
func (o *Orchestrator) governanceKeepsCommitting(ctx context.Context, run *domain.Run) (string, error) {
	if o.governance == nil || !o.bindingOn {
		return "", nil
	}
	if v := run.CommitMeta[CommitMetaGovernanceBlockedTarget]; v != "" {
		if m, b, ok := strings.Cut(v, " "); ok && isBoundObjectID(m) && isBoundObjectID(b) {
			on, err := o.mergeOnLocalTarget(ctx, m)
			if err != nil {
				return "", fmt.Errorf("check the run's blocked merge %s: %w", m, err)
			}
			if on {
				return "a merge the guard refused for this run is still blocked on the local " + authoritativeBranch, nil
			}
			if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{CommitMetaGovernanceBlockedTarget: ""}); err != nil {
				observe.Logger(ctx).Warn("governance guard: failed to clear a stale blocked target on the run",
					"run_id", run.RunID, "merge_sha", m, "error", err)
			}
		}
	}
	t, err := o.governanceHoldTarget(ctx, run)
	if err != nil {
		return "", fmt.Errorf("read the run's governance hold: %w", err)
	}
	if t != nil && isGovernanceHold(t.ErrorDetail) {
		return "the merge is held for an operator governance decision", nil
	}
	return "", nil
}

// mergeOnLocalTarget reports whether merge is in the local target's
// history. A merge commit that no longer exists (taken back and pruned)
// is not.
func (o *Orchestrator) mergeOnLocalTarget(ctx context.Context, merge string) (bool, error) {
	tip, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
	if err != nil {
		return false, err
	}
	in, err := o.binder.IsAncestor(ctx, merge, tip)
	if err != nil {
		var gitErr *git.GitError
		if errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
			return false, nil
		}
		return false, err
	}
	return in, nil
}

// recordGovernanceBlock remembers a refused merge that is still on the
// local target: in process (every run's gate checks it) and on the run
// (CommitMetaGovernanceBlockedTarget, so the run's own retries restore the
// target after a restart).
func (o *Orchestrator) recordGovernanceBlock(ctx context.Context, run *domain.Run, merge, base string) {
	g := o.governance
	g.mu.Lock()
	if g.blocks == nil {
		g.blocks = map[string]string{}
	}
	g.blocks[merge] = base
	g.mu.Unlock()
	if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{CommitMetaGovernanceBlockedTarget: merge + " " + base}); err != nil {
		observe.Logger(ctx).Error("governance guard: failed to persist the blocked target on the run",
			"run_id", run.RunID, "merge_sha", merge, "error", err)
	}
}

// checkGovernanceBlocks runs before any governed merge while the guard is
// on. For every refused merge still recorded (in process, or on this run):
// if it is no longer in the local target's history the block is cleared;
// if the target is still exactly at it, it is taken back now
// (compare-and-swap) and cleared; otherwise — the take-back fails, or
// something was committed on top of it — the merge is refused as
// unavailable (the run stays committing), because merging now would
// publish the refused content. An operator clears a stuck block by
// resetting the local target so it no longer contains the refused merge.
func (o *Orchestrator) checkGovernanceBlocks(ctx context.Context, run *domain.Run) error {
	g := o.governance
	g.mu.Lock()
	blocks := make(map[string]string, len(g.blocks)+1)
	for m, b := range g.blocks {
		blocks[m] = b
	}
	g.mu.Unlock()
	own := ""
	if v := run.CommitMeta[CommitMetaGovernanceBlockedTarget]; v != "" {
		if m, b, ok := strings.Cut(v, " "); ok && isBoundObjectID(m) && isBoundObjectID(b) {
			blocks[m] = b
			own = m
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	log := observe.Logger(ctx)
	unavailable := func(msg string, cause error) error {
		log.Error("governance guard: governed merge refused; "+msg, "run_id", run.RunID, "error", cause)
		return domain.NewErrorWithCause(domain.ErrUnavailable, "governance guard: "+msg, errors.Join(errGovernanceTargetBlocked, cause))
	}
	for m, b := range blocks {
		tip, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
		if err != nil {
			return unavailable("the local "+authoritativeBranch+" could not be read to check a refused merge", err)
		}
		in, err := o.binder.IsAncestor(ctx, m, tip)
		if err != nil {
			return unavailable("could not check whether refused merge "+m+" is still on the local "+authoritativeBranch, err)
		}
		if in && tip == m {
			if err := o.binder.ResetBranch(ctx, authoritativeBranch, b, m); err != nil {
				return unavailable("refused merge "+m+" is still on the local "+authoritativeBranch+" and could not be taken back to "+b, err)
			}
			log.Warn("governance guard: refused merge taken back", "merge_sha", m, "restored_to", b)
			in = false
		}
		if in {
			return unavailable("refused merge "+m+" is in the history of the local "+authoritativeBranch+
				" (something was committed on top of it); reset it so it no longer contains the refused merge", nil)
		}
		g.mu.Lock()
		delete(g.blocks, m)
		g.mu.Unlock()
		if m == own {
			if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{CommitMetaGovernanceBlockedTarget: ""}); err != nil {
				log.Warn("governance guard: failed to clear the blocked target on the run", "run_id", run.RunID, "error", err)
			} else if run.CommitMeta != nil {
				run.CommitMeta[CommitMetaGovernanceBlockedTarget] = ""
			}
		}
	}
	return nil
}
