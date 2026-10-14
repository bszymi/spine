package engine

// Operator decisions on held governance merges, and the read API
// (ADR-016 §4; INIT-023 TASK-006 §3.6, §3.9). The approval is a
// server-side record of one bound commit, written only here, after the
// engine itself checked the actor's role — never read from commit text,
// branch content or workflow data.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// GovernanceDecision is an operator's decision on a held merge.
type GovernanceDecision string

const (
	// GovernanceApprove records an approval of the bound commit and runs
	// the merge.
	GovernanceApprove GovernanceDecision = "approve"
	// GovernanceReject clears any approval and takes the workflow's
	// merge_failed route (or fails a run whose workflow has no publish
	// step).
	GovernanceReject GovernanceDecision = "reject"
)

// MaxGovernanceDecisionReason bounds GovernanceDecisionRequest.Reason.
const MaxGovernanceDecisionReason = 2000

// GovernanceDecisionRequest asks DecideGovernanceMerge to approve or
// reject a held merge.
type GovernanceDecisionRequest struct {
	RunID string
	// CommitSHA must equal the run's bound commit (full hex, any case).
	CommitSHA string
	Decision  GovernanceDecision
	// Actor is the authenticated caller; the engine checks its role again
	// (operator or higher).
	Actor domain.Actor
	// Reason is at most MaxGovernanceDecisionReason bytes; required for a
	// reject.
	Reason string
}

// GovernanceDecisionResult is the outcome of DecideGovernanceMerge.
type GovernanceDecisionResult struct {
	RunID     string
	CommitSHA string
	Decision  GovernanceDecision
	Paths     []string // capped like events
	PathCount int
	// MergeAttempted is true for an approve: the merge ran right after the
	// approval was recorded.
	MergeAttempted bool
	// MergeError is set when that merge did not complete (it failed, or is
	// waiting for a retry). The approval stands; approving the same commit
	// again retries the merge.
	MergeError string
	RunStatus  domain.RunStatus
}

var commitSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// DecideGovernanceMerge approves or rejects a held governance merge.
// Checks, in order — each failure makes no write and emits no event:
//
//  1. the guard is enabled (ErrUnavailable);
//  2. the actor is named and operator or higher (ErrForbidden);
//  3. the decision is known, the reason within bounds, and a reject has
//     one (ErrInvalidParams);
//  4. CommitSHA is a full hex object id (ErrInvalidParams);
//  5. the run exists (ErrNotFound) and is committing (ErrConflict);
//  6. the merge's governance facts can be computed (ErrUnavailable);
//  7. the merge changes a governance path (ErrPrecondition);
//  8. CommitSHA is the bound commit (ErrConflict).
//
// Approve records the approval (reserved commit_meta keys), re-reads the
// run (ErrConflict if it left committing meanwhile, without merging),
// emits governance.merge_decided, clears the hold and runs the merge; a
// merge error is reported in MergeError and the call still succeeds,
// because the approval stands. Approving the same commit again is a
// manual merge retry.
//
// Reject first revokes any approval (the keys are emptied: a JSONB merge
// cannot delete a key, and an empty commit parses as no approval), so a
// resubmit of the same commit after the loop-back is held again. Then it
// takes the publish step's merge_failed route (back to execute in
// task-default) or, on a workflow with no publish step, fails the run
// with a compare-and-swap from committing (the branch is preserved). A
// run that completed meanwhile is ErrConflict, and no run_failed or
// decision event is emitted.
//
// There is no separation-of-duties rule on the approver (TASK-006 D11):
// an operator can already land the same change with an override push.
func (o *Orchestrator) DecideGovernanceMerge(ctx context.Context, req GovernanceDecisionRequest) (*GovernanceDecisionResult, error) {
	if o.governance == nil || !o.bindingOn {
		return nil, domain.NewError(domain.ErrUnavailable, "governance guard is not enabled")
	}
	if req.Actor.ActorID == "" || !req.Actor.Role.HasAtLeast(domain.RoleOperator) {
		return nil, domain.NewError(domain.ErrForbidden, "only an operator or admin may decide a governance change")
	}
	switch req.Decision {
	case GovernanceApprove, GovernanceReject:
	default:
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("decision must be %q or %q", GovernanceApprove, GovernanceReject))
	}
	if len(req.Reason) > MaxGovernanceDecisionReason {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("reason is longer than %d bytes", MaxGovernanceDecisionReason))
	}
	if req.Decision == GovernanceReject && strings.TrimSpace(req.Reason) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "a reject needs a reason")
	}
	sha := strings.ToLower(req.CommitSHA)
	if !commitSHAPattern.MatchString(sha) {
		return nil, domain.NewError(domain.ErrInvalidParams, "commit_sha must be a full hex commit id (40 or 64 characters)")
	}

	run, err := o.store.GetRun(ctx, req.RunID)
	if err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) && se.Code == domain.ErrNotFound {
			return nil, err
		}
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "load run", err)
	}
	if run.Status != domain.RunStatusCommitting {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is %s, not committing: only a held merge can be decided", run.RunID, run.Status))
	}
	gc, err := o.governanceCheckForRun(ctx, run)
	if err != nil {
		observe.Logger(ctx).Warn("governance decision: the merge's governance facts are unavailable",
			"run_id", run.RunID, "error", err)
		return nil, domain.NewErrorWithCause(domain.ErrUnavailable,
			"the merge's changed paths could not be computed; try again", err)
	}
	touched := gc.Touched()
	if len(touched) == 0 {
		return nil, domain.NewError(domain.ErrPrecondition, "run does not require governance approval: its merge changes no governance path")
	}
	if sha != gc.BoundSHA {
		return nil, domain.NewError(domain.ErrConflict, fmt.Sprintf("merge is bound to %s, not %s", gc.BoundSHA, sha))
	}

	paths, _ := o.capPaths(touched)
	result := &GovernanceDecisionResult{
		RunID: run.RunID, CommitSHA: gc.BoundSHA, Decision: req.Decision,
		Paths: paths, PathCount: len(touched),
	}
	if req.Decision == GovernanceApprove {
		return o.approveGovernanceMerge(ctx, run, gc, touched, req, result)
	}
	return o.rejectGovernanceMerge(ctx, run, gc, touched, req, result)
}

func (o *Orchestrator) approveGovernanceMerge(ctx context.Context, run *domain.Run, gc *branchprotect.GovernanceCheck, touched []string, req GovernanceDecisionRequest, result *GovernanceDecisionResult) (*GovernanceDecisionResult, error) {
	log := observe.Logger(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{
		domain.CommitMetaGovernanceApprovalSHA:        gc.BoundSHA,
		domain.CommitMetaGovernanceApprovalActorID:    req.Actor.ActorID,
		domain.CommitMetaGovernanceApprovalActorRole:  string(req.Actor.Role),
		domain.CommitMetaGovernanceApprovalApprovedAt: now.Format(time.RFC3339),
	}); err != nil {
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "record the governance approval", err)
	}
	fresh, err := o.store.GetRun(ctx, run.RunID)
	if err != nil {
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "reload run", err)
	}
	if fresh.Status != domain.RunStatusCommitting {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s left committing (%s) while the approval was recorded; nothing was merged", run.RunID, fresh.Status))
	}
	o.emitGovernanceDecided(ctx, run, gc, touched, "approved", req, fmt.Sprintf("approve-%s-%d", shortSHA12(gc.BoundSHA), now.UnixMilli()))
	log.Info("governance guard: governance change approved",
		"run_id", run.RunID, "bound_sha", gc.BoundSHA, "actor_id", req.Actor.ActorID, "actor_role", string(req.Actor.Role))
	o.clearGovernanceHold(ctx, fresh)

	result.MergeAttempted = true
	if err := o.MergeRunBranch(ctx, run.RunID); err != nil {
		result.MergeError = err.Error()
		log.Warn("governance guard: merge after approval did not complete; the approval stands",
			"run_id", run.RunID, "error", err)
	}
	if after, err := o.store.GetRun(ctx, run.RunID); err == nil {
		result.RunStatus = after.Status
	}
	return result, nil
}

func (o *Orchestrator) rejectGovernanceMerge(ctx context.Context, run *domain.Run, gc *branchprotect.GovernanceCheck, touched []string, req GovernanceDecisionRequest, result *GovernanceDecisionResult) (*GovernanceDecisionResult, error) {
	log := observe.Logger(ctx)
	// Revoke first: TASK-005 keeps reserved keys across later outcome
	// writes, so an approval that survived the reject would merge the same
	// commit when the author resubmits it unchanged after the loop-back.
	if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{
		domain.CommitMetaGovernanceApprovalSHA:        "",
		domain.CommitMetaGovernanceApprovalActorID:    "",
		domain.CommitMetaGovernanceApprovalActorRole:  "",
		domain.CommitMetaGovernanceApprovalApprovedAt: "",
	}); err != nil {
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "revoke the governance approval", err)
	}
	if fresh, err := o.store.GetRun(ctx, run.RunID); err == nil {
		run = fresh
	}
	o.clearGovernanceHold(ctx, run)
	rejectErr := &git.GitError{Kind: git.ErrKindPermanent, Op: "governance_guard",
		Message: fmt.Sprintf("governance change rejected by %s: %s", req.Actor.ActorID, req.Reason)}

	if wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion); err == nil && wfDef != nil {
		if exec, _ := o.findActiveMergeStep(ctx, run, wfDef); exec != nil {
			// Publish step: the merge_failed route, whose run transition is
			// a compare-and-swap from committing (advancePublishStepIfAny).
			o.recordGitConflictOnStep(ctx, run, rejectErr)
			if err := o.advancePublishStepIfAny(ctx, run, "merge_failed"); err != nil {
				return nil, domain.NewErrorWithCause(domain.ErrInternal, "route the rejected merge back", err)
			}
			after, err := o.store.GetRun(ctx, run.RunID)
			if err == nil && (after.Status == domain.RunStatusCompleted || after.Status == domain.RunStatusCommitting) {
				return nil, domain.NewError(domain.ErrConflict,
					fmt.Sprintf("run %s is %s: the reject did not take effect", run.RunID, after.Status))
			}
			o.emitGovernanceDecided(ctx, run, gc, touched, "rejected", req, fmt.Sprintf("reject-%s-%d", shortSHA12(gc.BoundSHA), time.Now().UnixMilli()))
			log.Info("governance guard: governance change rejected; run routed back",
				"run_id", run.RunID, "bound_sha", gc.BoundSHA, "actor_id", req.Actor.ActorID)
			if err == nil {
				result.RunStatus = after.Status
			}
			return result, nil
		}
	}

	// No publish step: fail the run, compare-and-swap from committing
	// (failRunOnMergeError's write is blind until INIT-023 TASK-008, and
	// an approve on another request may have completed the merge).
	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, domain.RunStatusCommitting, domain.RunStatusFailed)
	if err != nil {
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "fail the rejected run", err)
	}
	if !applied {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s left committing before the reject took effect", run.RunID))
	}
	o.recordGitConflictOnStep(ctx, run, rejectErr)
	o.terminateLiveSteps(ctx, run.RunID, "run failed: governance change rejected")
	o.emitEvent(ctx, domain.EventRunFailed, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-failed", shortTraceID(run.TraceID)), nil)
	o.emitGovernanceDecided(ctx, run, gc, touched, "rejected", req, fmt.Sprintf("reject-%s-%d", shortSHA12(gc.BoundSHA), time.Now().UnixMilli()))
	log.Info("governance guard: governance change rejected; run failed, branch preserved",
		"run_id", run.RunID, "bound_sha", gc.BoundSHA, "actor_id", req.Actor.ActorID)
	result.RunStatus = domain.RunStatusFailed
	return result, nil
}

func (o *Orchestrator) emitGovernanceDecided(ctx context.Context, run *domain.Run, gc *branchprotect.GovernanceCheck, touched []string, decision string, req GovernanceDecisionRequest, suffix string) {
	paths, _ := o.capPaths(touched)
	raw, err := json.Marshal(map[string]any{
		"run_id":     run.RunID,
		"bound_sha":  gc.BoundSHA,
		"decision":   decision,
		"actor_id":   req.Actor.ActorID,
		"actor_role": string(req.Actor.Role),
		"reason":     req.Reason,
		"paths":      paths,
		"path_count": len(touched),
	})
	if err != nil {
		raw = nil
	}
	o.emitEvent(ctx, domain.EventGovernanceMergeDecided, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-governance-%s", shortTraceID(run.TraceID), suffix), raw)
}

// clearGovernanceHold removes a hold ErrorDetail from the hold target.
func (o *Orchestrator) clearGovernanceHold(ctx context.Context, run *domain.Run) {
	target := o.governanceHoldTarget(ctx, run)
	if target == nil || target.ErrorDetail == nil ||
		target.ErrorDetail.Classification != domain.FailureGovernanceApprovalRequired {
		return
	}
	target.ErrorDetail = nil
	if err := o.store.UpdateStepExecution(ctx, target); err != nil {
		observe.Logger(ctx).Warn("governance guard: failed to clear the hold on the step",
			"run_id", run.RunID, "step_id", target.StepID, "error", err)
	}
}

// governanceCheckForRun computes the governance facts of the run's merge
// exactly as the gate does, without the gate's side effects (no outcome
// row, no routing): the trusted binding, the commit check, and the
// bound status cascade. It mirrors resolveMergeSource (merge_binding.go);
// the two must agree, or a held run could not be decided (pinned by
// TestGovernanceGuard_EveryHoldIsDecidable).
func (o *Orchestrator) governanceCheckForRun(ctx context.Context, run *domain.Run) (*branchprotect.GovernanceCheck, error) {
	bound, _, ok, _ := trustedBinding(run.CommitMeta, run.RunID)
	if !ok {
		return nil, errNoTrustedBinding
	}
	if err := o.binder.VerifyCommit(ctx, bound); err != nil {
		return nil, fmt.Errorf("bound commit %s: %w", bound, err)
	}
	src := mergeSource{bound: bound, sha: bound}
	if newStatus := run.CommitMeta["status"]; newStatus != "" {
		c, err := o.applyCommitStatusAt(ctx, run, newStatus, bound)
		switch {
		case errors.Is(err, errNotChildOfBound):
			return nil, err
		case err != nil:
			// resolveMergeSource merges the bound commit when the cascade
			// fails; so does the evaluation.
		default:
			src.sha = c
		}
	}
	return o.buildGovernanceCheck(ctx, run, src)
}

// GovernanceApprovalRecord is the approval recorded on a run, as stored.
type GovernanceApprovalRecord struct {
	SHA        string
	ActorID    string
	ActorRole  string
	ApprovedAt string
	MergeSHA   string
	BaseSHA    string
}

// GovernanceMergeStatus is the governance state of a run's merge.
type GovernanceMergeStatus struct {
	RunID     string
	RunStatus domain.RunStatus
	// Evaluated is false when the run has no binding yet, or its changed
	// paths could not be computed (EvaluationError says which, without git
	// output).
	Evaluated       bool
	EvaluationError string
	BoundSHA        string
	// Required: the merge changes a governance path.
	Required bool
	// Held: the run is committing, Required, and no approval of the bound
	// commit is recorded.
	Held        bool
	Paths       []string // capped
	PathCount   int
	Truncated   bool
	ExemptPaths []string
	// Approval is the recorded approval, or nil (also after a reject).
	Approval *GovernanceApprovalRecord
}

// GovernanceMergeStatus reports a run's governance state. Read-only
// (it may create the same unreferenced cascade object the gate creates).
// ErrUnavailable when the guard is off; ErrNotFound for an unknown run.
func (o *Orchestrator) GovernanceMergeStatus(ctx context.Context, runID string) (*GovernanceMergeStatus, error) {
	if o.governance == nil || !o.bindingOn {
		return nil, domain.NewError(domain.ErrUnavailable, "governance guard is not enabled")
	}
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) && se.Code == domain.ErrNotFound {
			return nil, err
		}
		return nil, domain.NewErrorWithCause(domain.ErrInternal, "load run", err)
	}
	st := &GovernanceMergeStatus{RunID: run.RunID, RunStatus: run.Status}
	if sha := run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA]; sha != "" {
		st.Approval = &GovernanceApprovalRecord{
			SHA:        sha,
			ActorID:    run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID],
			ActorRole:  run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole],
			ApprovedAt: run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt],
			MergeSHA:   run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA],
			BaseSHA:    run.CommitMeta[domain.CommitMetaGovernanceApprovalBaseSHA],
		}
	}
	if _, _, ok, _ := trustedBinding(run.CommitMeta, run.RunID); !ok {
		st.EvaluationError = "the run has no bound commit yet"
		return st, nil
	}
	gc, err := o.governanceCheckForRun(ctx, run)
	if err != nil {
		observe.Logger(ctx).Warn("governance status: the merge's governance facts are unavailable",
			"run_id", run.RunID, "error", err)
		st.EvaluationError = "the merge's changed paths could not be computed"
		return st, nil
	}
	touched := gc.Touched()
	st.Evaluated = true
	st.BoundSHA = gc.BoundSHA
	st.Required = len(touched) > 0
	st.Paths, st.Truncated = o.capPaths(touched)
	st.PathCount = len(touched)
	st.ExemptPaths = nonNil(gc.ExemptPaths)
	decision, _ := branchprotect.EvaluateGovernance(gc)
	st.Held = run.Status == domain.RunStatusCommitting && decision == branchprotect.DecisionDeny
	return st, nil
}
