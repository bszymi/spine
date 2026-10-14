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
//  3. Post-merge assertion (assertGovernedMerge). Before the push, the
//     governed paths the merge actually changed (Diff(BaseSHA, M)) must be
//     covered by the evaluation; anything else (non-default merge
//     configuration: merge.directoryRenames, merge drivers) is a permanent
//     refusal, taken back to BaseSHA and never pushed (owner question 6).
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
	"reflect"
	"strings"
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
// object id. It is not part of GitOperator, so embedders whose operator
// lacks it keep compiling; WithGovernanceGuard type-asserts it and
// refuses to turn the guard on without it. *git.CLIClient implements it.
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

// GovernanceHoldRuleID is the ErrorDetail.RuleID of a held merge.
const GovernanceHoldRuleID = "governance-path-guard"

// defaultMaxReportedPaths caps reported path lists.
const defaultMaxReportedPaths = 100

// governanceGuard is the validated configuration.
type governanceGuard struct {
	cfg         GovernanceGuard
	paths       branchprotect.GovernancePaths
	bases       MergeBaseLister
	maxReported int
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
}

// governanceGate runs between resolveMergeSource and the first merge
// (MergeRunBranch). It returns done=true with the error the caller
// returns when the merge must not proceed: a hold (precondition_failed
// wrapping ErrGovernanceApprovalRequired), an unevaluable merge
// (service_unavailable; nothing recorded), a policy failure (internal) or
// another deny (forbidden). On allow it records the pass on src. A nil
// guard, or binding off, is a no-op.
func (o *Orchestrator) governanceGate(ctx context.Context, run *domain.Run, src *mergeSource) (bool, error) {
	if o.governance == nil || !o.bindingOn {
		return false, nil
	}
	log := observe.Logger(ctx)
	gc, err := o.buildGovernanceCheck(ctx, run, *src)
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
	src.gov = &governancePass{
		check:    gc,
		touched:  touched,
		consumes: hasReason(ownReasons, branchprotect.ReasonGovernanceApproved),
	}
	if src.gov.consumes {
		log.Info("governance guard: approved governance change proceeds to merge",
			"run_id", run.RunID, "bound_sha", gc.BoundSHA, "path_count", len(touched))
	}
	return false, nil
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
		return nil, fmt.Errorf("%s and %s have no merge base", authoritativeBranch, src.sha)
	}
	changed, err := o.changedPathsFrom(ctx, bases, src.sha)
	if err != nil {
		return nil, err
	}
	gc := &branchprotect.GovernanceCheck{
		Paths:        g.paths,
		BoundSHA:     src.bound,
		ChangedPaths: changed,
		Approval:     governanceApprovalFrom(run.CommitMeta),
	}
	gc.ExemptPaths = append(gc.ExemptPaths, o.ownEvidenceExemptions(run, changed)...)
	gc.ExemptPaths = append(gc.ExemptPaths, o.lifecycleExemption(ctx, run, src, bases, changed)...)
	return gc, nil
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
//   - neither the merged file nor its version at any merge base declares
//     applies_to: [Workflow], in any letter case (an exempt edit could
//     otherwise add a second workflow-governing binding — every lifecycle
//     run would then fail to resolve — or shadow
//     <artifacts_dir>/workflows/workflow-lifecycle.yaml from the root);
//   - local separation of duties: the last completed main-path review step
//     was completed by a named actor who completed no non-review step of
//     the run, whatever the deployment's step-entry policy says (in
//     standalone one reviewer could otherwise self-grant workflow_review,
//     draft, self-review and land a review-less task-default with no
//     operator).
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
	for _, b := range bases {
		prev, err := o.git.ReadFile(ctx, b, run.TaskPath)
		if err != nil {
			var gitErr *git.GitError
			if errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
				continue // the file is new since this base
			}
			return deny("previous workflow version unreadable", err)
		}
		prevDef, err := workflow.Parse(run.TaskPath, prev)
		if err != nil {
			return deny("previous workflow version does not parse", err)
		}
		if declaresWorkflowGovernance(prevDef.AppliesTo) {
			return deny("the previous version governs workflows", nil)
		}
	}
	if ok, why := o.reviewerIsNotAuthor(ctx, run, gov); !ok {
		return deny(why, nil)
	}
	return []string{run.TaskPath}
}

func declaresWorkflowGovernance(appliesTo []string) bool {
	for _, a := range appliesTo {
		if strings.EqualFold(strings.TrimSpace(a), string(domain.ArtifactTypeWorkflow)) {
			return true
		}
	}
	return false
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
// is named and completed no non-review step execution of the run.
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
	for i := range execs {
		e := &execs[i]
		if e.Status == domain.StepStatusCompleted && !isReview(e.StepID) && e.ActorID == last.ActorID {
			return false, "the reviewer also completed a non-review step of the run"
		}
	}
	return true, ""
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
// — on a workflow with no publish step — its last completed step (the
// step whose outcome triggered the merge, as recordGitConflictOnStep
// uses). nil when there is none.
func (o *Orchestrator) governanceHoldTarget(ctx context.Context, run *domain.Run) *domain.StepExecution {
	if wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion); err == nil && wfDef != nil {
		if exec, _ := o.findActiveMergeStep(ctx, run, wfDef); exec != nil {
			return exec
		}
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		observe.Logger(ctx).Warn("governance guard: step executions unavailable for the hold record",
			"run_id", run.RunID, "error", err)
		return nil
	}
	var last *domain.StepExecution
	for i := range execs {
		if execs[i].Status == domain.StepStatusCompleted {
			last = &execs[i]
		}
	}
	return last
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
	target := o.governanceHoldTarget(ctx, run)
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
			target.ErrorDetail = detail
			if err := o.store.UpdateStepExecution(ctx, target); err != nil {
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

// assertGovernedMerge is the post-merge assertion (§3.4a), run in
// mergePrimaryBound after verifyBoundMerge accepted the merge and before
// anything is pushed. It returns done=true when it refused (the caller
// returns err) and records the consumption marker when the attempt
// consumed an approval.
//
// The governed paths the merge changed — Diff(BaseSHA, M), Path and
// OldPath, the own-run evidence carve-out and the evaluation's exemptions
// applied — must be covered by the paths the gate evaluated. With no
// approval that set is empty, so any governed path is a violation. A
// violation (non-default merge behaviour the pre-merge evaluation cannot
// see) is a permanent refusal (owner question 6); a Diff failure is too,
// unless it is a retryable git error and the take-back was clean (the
// attempt then simply runs again, as TASK-005 treats a transient read in
// its own check). Either way the local target is restored to BaseSHA
// first and nothing is pushed. "Already up to date" (M == BaseSHA, a
// replay) changed nothing and keeps the marker an earlier attempt wrote.
func (o *Orchestrator) assertGovernedMerge(ctx context.Context, run *domain.Run, src *mergeSource, res git.MergeResult) (bool, error) {
	pass := src.gov
	if pass == nil || res.SHA == res.BaseSHA {
		return false, nil
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
		post := &branchprotect.GovernanceCheck{
			Paths:        gc.Paths,
			ChangedPaths: actual,
			ExemptPaths:  append(append([]string(nil), gc.ExemptPaths...), o.ownEvidenceExemptions(run, actual)...),
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
			marker := map[string]string{
				domain.CommitMetaGovernanceApprovalMergeSHA: res.SHA,
				domain.CommitMetaGovernanceApprovalBaseSHA:  res.BaseSHA,
			}
			if err := o.mergeRunCommitMeta(ctx, run.RunID, marker); err != nil {
				tb := o.takeBackGovernedMerge(ctx, res)
				log.Error("governance guard: consumption marker not recorded; merge taken back, nothing pushed, the run stays committing",
					"run_id", run.RunID, "merge_sha", res.SHA, "take_back", tb.detail, "error", err)
				return false, fmt.Errorf("record governance consumption marker: %w", err)
			}
		}
		return false, nil
	}

	tb := o.takeBackGovernedMerge(ctx, res)
	if derr != nil {
		var gitErr *git.GitError
		if tb.clean && errors.As(derr, &gitErr) && gitErr.IsRetryable() {
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
	msg := "merge changed governed paths not covered by the evaluation: " + strings.Join(quotePaths(extra, governanceMessageLimit), ", ")
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
// failed marker write). Unlike takeBackRefusedMerge it never moves the
// target to a first parent: an accepted merge is [BaseSHA, S], a
// fast-forward to S, or nothing, so the target held BaseSHA before it —
// and for a fast-forward, S's own first parent is run-branch content,
// which must not stay on the target. Compare-and-swap through
// ResetBranch; a failure is logged at Error (the local target then still
// holds the refused merge and must be reset before the next merge).
func (o *Orchestrator) takeBackGovernedMerge(ctx context.Context, res git.MergeResult) takeBack {
	if res.SHA == res.BaseSHA {
		return takeBack{detail: "the local " + authoritativeBranch + " was not moved", clean: true}
	}
	if !isBoundObjectID(res.SHA) || !isBoundObjectID(res.BaseSHA) {
		observe.Logger(ctx).Error("governance guard: refused merge NOT taken back (merge result incomplete); reset the local branch before the next governed merge",
			"branch", authoritativeBranch, "merge_sha", res.SHA, "base_sha", res.BaseSHA)
		return takeBack{detail: "the local " + authoritativeBranch + " may still hold the refused merge (merge result incomplete)"}
	}
	if err := o.binder.ResetBranch(ctx, authoritativeBranch, res.BaseSHA, res.SHA); err != nil {
		observe.Logger(ctx).Error("governance guard: refused merge NOT taken back; reset the local branch before the next governed merge",
			"branch", authoritativeBranch, "merge_sha", res.SHA, "restore_to", res.BaseSHA, "error", err)
		return takeBack{detail: "the local " + authoritativeBranch + " still holds the refused merge (take-back failed)"}
	}
	return takeBack{detail: "the local " + authoritativeBranch + " was restored to " + res.BaseSHA, clean: true, restoredTo: res.BaseSHA}
}
