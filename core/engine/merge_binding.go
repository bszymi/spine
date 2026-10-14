package engine

// SHA-bound governed merges (INIT-023 TASK-005; audit F2 merge half, P2,
// and P3-37).
//
// The defect. A reviewer's "accepted" outcome was not bound to the commit
// the reviewer (or the validate step) saw: the governed merge resolved
// the run branch NAME when it ran and published its tip. Anyone able to
// push the run branch could land unvalidated, unreviewed commits on
// `main` under the trusted "Merge run …" message — in three windows:
// (a) after validate passed and before the reviewer accepted, (b) after
// the reviewer read the diff and before they submitted, and (c) while
// the run sat in `committing` between merge attempts.
//
// The fix, opt-in through EnableMergeBinding (zero value: today's
// behaviour):
//
//  1. Capture. Every main-path step completion records the run-branch
//     head the engine resolved at that moment (captureHead →
//     routeStepOutcome) in reserved runs.commit_meta keys (commitmeta.go).
//     A client may also assert the head it worked on
//     (SubmitRequest.ExpectedHeadSHA); a mismatch is a 409.
//  2. Moved-head check. A gate step (`review`, `convergence`) that
//     submits an outcome that leads to the merge compares the current head
//     with the previous binding. Under the default policy a moved head is
//     refused with 409 (run_branch_moved_since_gate): commits after the
//     binding were neither validated nor reviewed, and only a rework
//     outcome may follow. This closes window (a).
//  3. Frozen binding. From the approving completion onwards nothing but
//     the engine moves the binding (convergence, recovery ledger), and
//     every merge attempt — the immediate one, the publish handler, every
//     scheduler retry and every partially-merged resume — merges exactly
//     the bound commit (resolveMergeSource → MergeBound). Commits pushed
//     afterwards never reach `main` through this run: window (c), and
//     window (b) together with the assertion.
//  4. Structural verification. Before any push, the merge result must be
//     one of three shapes built only from the verified base and the bound
//     commit (verifyBoundMerge), and the push is a non-forced push of
//     exactly that merge commit (PushCommit).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// MovedHeadPolicy decides what an advancing gate submit does when the
// run-branch head moved since the previous binding.
type MovedHeadPolicy string

const (
	// MovedHeadReject refuses the approval with 409
	// (run_branch_moved_since_gate); the reviewer can still choose a rework
	// outcome. The default (owner question 4).
	MovedHeadReject MovedHeadPolicy = "reject"
	// MovedHeadRebind accepts the current head as the new binding. It still
	// closes windows (b) — with an assertion — and (c), but not (a).
	MovedHeadRebind MovedHeadPolicy = "rebind"
)

// MergeBindingConfig configures EnableMergeBinding.
type MergeBindingConfig struct {
	MovedHead MovedHeadPolicy // "" means MovedHeadReject
}

// Reasons carried in a binding refusal's HeadBindingDetail (and so in the
// HTTP error detail). Embedders map them to their own error codes.
const (
	ReasonExpectedHeadInvalid       = "expected_head_sha_invalid"
	ReasonExpectedHeadNotApplicable = "expected_head_sha_not_applicable"
	ReasonExpectedHeadUnsupported   = "expected_head_sha_unsupported"
	ReasonRunBranchHeadUnavailable  = "run_branch_head_unavailable"
	ReasonRunBranchMoved            = "run_branch_moved"
	ReasonRunBranchMovedSinceGate   = "run_branch_moved_since_gate"
)

// HeadBindingDetail is the Detail of every *domain.SpineError a binding
// check returns. Only object ids and the reason are carried — never git
// output.
type HeadBindingDetail struct {
	Reason          string `json:"reason"`
	HeadSHA         string `json:"head_sha,omitempty"`
	ExpectedHeadSHA string `json:"expected_head_sha,omitempty"`
	BoundSHA        string `json:"bound_sha,omitempty"`
	BoundBy         string `json:"bound_by,omitempty"`
	RepositoryID    string `json:"repository_id,omitempty"`
}

// EnableMergeBinding turns on SHA-bound governed merges. It returns an
// error, and leaves binding off, if the orchestrator's GitOperator is not
// a CommitBinder or the policy is unknown. Like the other With* options
// it is a setup-time call, not safe concurrently with orchestration.
//
// Code repositories need SHA-capable clients too; that is checked per run
// at StartRun (createRunBranches), because their clients are resolved
// lazily.
func (o *Orchestrator) EnableMergeBinding(cfg MergeBindingConfig) error {
	policy := cfg.MovedHead
	if policy == "" {
		policy = MovedHeadReject
	}
	if policy != MovedHeadReject && policy != MovedHeadRebind {
		return fmt.Errorf("engine: unknown moved-head policy %q", cfg.MovedHead)
	}
	binder, ok := o.git.(CommitBinder)
	if !ok || binder == nil {
		return fmt.Errorf("engine: merge binding requires a GitOperator that implements engine.CommitBinder (got %T)", o.git)
	}
	o.binder = binder
	o.movedHead = policy
	o.bindingOn = true
	return nil
}

// MergeBindingEnabled reports whether EnableMergeBinding succeeded.
func (o *Orchestrator) MergeBindingEnabled() bool { return o.bindingOn }

// commitBinder returns the primary operator's CommitBinder capability, or
// nil.
func (o *Orchestrator) commitBinder() CommitBinder {
	if o.binder != nil {
		return o.binder
	}
	if b, ok := o.git.(CommitBinder); ok {
		return b
	}
	return nil
}

// isGateStep reports whether a step is a gate: a step that approves
// rather than produces, and so is not expected to move the head.
func isGateStep(stepDef *domain.StepDefinition) bool {
	return stepDef.Type == domain.StepTypeReview || stepDef.Type == domain.StepTypeConvergence
}

// isAdvancingOutcome reports whether an outcome leads towards the merge
// ("merge-bound"): it ends the run with a commit, or its next step is a
// gate or an internal step, or the next step is unknown (fail closed).
// Any other outcome is a rework outcome: its next step is a producer, or
// it ends the run without a commit (no merge happens).
func isAdvancingOutcome(wfDef *domain.WorkflowDefinition, outcome *domain.OutcomeDefinition) bool {
	next := outcome.NextStep
	if next == "" || next == "end" {
		return len(outcome.Commit) > 0
	}
	nd := findStepDef(wfDef, next)
	if nd == nil {
		return true
	}
	return isGateStep(nd) || nd.Type == domain.StepTypeInternal
}

func bindingError(code domain.ErrorCode, msg string, d HeadBindingDetail) *domain.SpineError {
	return domain.NewErrorWithDetail(code, msg, d)
}

// checkHeadAssertion is rule 1 of the capture: a client-supplied
// expected_head_sha must be a full lowercase hex id, must apply to the
// step, and needs a SHA-capable operator — even with binding off, so a
// client that asks for protection never gets silent acceptance. It has no
// side effects; IngestResult runs it before anything can fail the step
// (DC-8) and captureHead runs it for direct SubmitStepResult callers.
func (o *Orchestrator) checkHeadAssertion(run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition, expected string) error {
	if expected == "" {
		return nil
	}
	if !isBoundObjectID(expected) {
		return bindingError(domain.ErrInvalidParams,
			"expected_head_sha must be a full lowercase hex commit id (40 or 64 characters)",
			HeadBindingDetail{Reason: ReasonExpectedHeadInvalid})
	}
	if run.BranchName == "" || exec.BranchID != "" || stepDef.Type == domain.StepTypeInternal {
		return bindingError(domain.ErrInvalidParams,
			"expected_head_sha applies only to main-path, non-internal steps of a run with a branch",
			HeadBindingDetail{Reason: ReasonExpectedHeadNotApplicable, ExpectedHeadSHA: expected})
	}
	if o.commitBinder() == nil {
		return bindingError(domain.ErrUnavailable,
			"expected_head_sha cannot be checked: the git operator does not support SHA-bound merges",
			HeadBindingDetail{Reason: ReasonExpectedHeadUnsupported, ExpectedHeadSHA: expected})
	}
	return nil
}

// headUnavailable is the refusal for a required capture whose head could
// not be resolved. The cause is wrapped for errors.Is/As but never put in
// the message or detail (it carries git output).
func headUnavailable(repoID string, cause error) error {
	e := domain.NewErrorWithCause(domain.ErrUnavailable,
		"run branch head could not be resolved; the step was not submitted", cause)
	e.Detail = HeadBindingDetail{Reason: ReasonRunBranchHeadUnavailable, RepositoryID: repoID}
	return e
}

// captureHead resolves and checks the run-branch head for one step
// submit, before anything about the step changes (so a refusal leaves the
// execution exactly as it was). It returns the binding routeStepOutcome
// records, or nil when nothing is to be recorded (binding off, or a case
// rule 2 exempts). Rules, in order:
//
//  1. an assertion is checked by checkHeadAssertion;
//  2. nothing is captured with binding off and no assertion, for a run
//     with no branch, a divergence-branch step, an internal step (the
//     merge handler records its own outcome) or a terminal execution (the
//     scheduler's step recovery re-submits completed executions; their
//     binding was recorded the first time, DC-8);
//  3. the head is resolved; capture is required with binding on unless a
//     gate submits a rework outcome, and whenever an assertion was given.
//     A required capture that cannot resolve is ErrUnavailable; an
//     optional one logs and leaves the binding unchanged (a reviewer can
//     always send work back);
//  4. an assertion must equal the head (ErrConflict, run_branch_moved);
//  5. with binding on, a gate's advancing outcome compares the head with
//     the previous binding (checkMovedHead);
//  6. code repositories follow the same rules through their own clients.
func (o *Orchestrator) captureHead(ctx context.Context, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, wfDef *domain.WorkflowDefinition, expected string) (*headBinding, error) {
	if err := o.checkHeadAssertion(run, exec, stepDef, expected); err != nil {
		return nil, err
	}
	if (!o.bindingOn && expected == "") || run.BranchName == "" || exec.BranchID != "" ||
		stepDef.Type == domain.StepTypeInternal || exec.Status.IsTerminal() {
		return nil, nil
	}
	log := observe.Logger(ctx)
	binder := o.commitBinder() // non-nil: EnableMergeBinding or checkHeadAssertion guaranteed it
	gate := isGateStep(stepDef)
	advancing := isAdvancingOutcome(wfDef, outcome)
	required := expected != "" || (o.bindingOn && (!gate || advancing))

	head, err := binder.ResolveBranchHead(ctx, run.BranchName)
	if err == nil && !isBoundObjectID(head) {
		err = fmt.Errorf("operator returned a malformed head %q", truncateForLog(head))
	}
	if err != nil {
		if required {
			return nil, headUnavailable(domain.PrimaryRepositoryID, err)
		}
		log.Warn("merge binding: run branch head unavailable on a rework submit; binding unchanged",
			"run_id", run.RunID, "step_id", exec.StepID, "error", err)
		return nil, nil
	}
	if expected != "" && expected != head {
		return nil, bindingError(domain.ErrConflict,
			fmt.Sprintf("run branch %s moved: it is at %s, not at the expected %s; review the current head and submit again",
				run.BranchName, head, expected),
			HeadBindingDetail{Reason: ReasonRunBranchMoved, HeadSHA: head, ExpectedHeadSHA: expected})
	}
	if !o.bindingOn {
		return nil, nil
	}

	prev, prevBy, trusted, present := trustedBinding(run.CommitMeta, run.RunID)
	if gate && advancing {
		if err := o.checkMovedHead(ctx, run, exec, "", head, prev, prevBy, trusted, present); err != nil {
			return nil, err
		}
	}

	hb := &headBinding{
		head:            head,
		by:              boundByStep(exec.StepID, exec.ExecutionID),
		stepID:          exec.StepID,
		replaceRepoKeys: true,
	}
	for _, repoID := range codeRepoIDs(run) {
		rb, err := o.codeRepoBinder(ctx, repoID)
		var rhead string
		if err == nil {
			rhead, err = rb.ResolveBranchHead(ctx, runBranchIn(run, repoID))
			if err == nil && !isBoundObjectID(rhead) {
				err = fmt.Errorf("client returned a malformed head %q", truncateForLog(rhead))
			}
		}
		if err != nil {
			if required {
				return nil, headUnavailable(repoID, err)
			}
			log.Warn("merge binding: code repository head unavailable on a rework submit; binding unchanged",
				"run_id", run.RunID, "step_id", exec.StepID, "repository_id", repoID, "error", err)
			return nil, nil
		}
		if gate && advancing {
			rprev := run.CommitMeta[RepoBoundSHAKey(repoID)]
			// A code repository's binding is only as trustworthy as the
			// primary one: every engine write keeps the two together.
			if err := o.checkMovedHead(ctx, run, exec, repoID, rhead, rprev, prevBy,
				trusted && isBoundObjectID(rprev), rprev != ""); err != nil {
				return nil, err
			}
		}
		if hb.repoHeads == nil {
			hb.repoHeads = make(map[string]string)
		}
		hb.repoHeads[repoID] = rhead
	}
	return hb, nil
}

// checkMovedHead is rule 5: a gate's advancing outcome with a head that
// differs from the previous binding. A previous binding this run did not
// write (DC-9: a value planted before the upgrade) counts as moved. No
// previous binding at all (a run in flight at enablement, or a workflow
// whose first step is a gate) has nothing to compare with.
func (o *Orchestrator) checkMovedHead(ctx context.Context, run *domain.Run, exec *domain.StepExecution, repoID, head, prev, prevBy string, trusted, present bool) error {
	log := observe.Logger(ctx)
	if !present {
		log.Info("merge binding: no previous binding to compare the gate's head with",
			"run_id", run.RunID, "step_id", exec.StepID, "repository_id", repoIDForLog(repoID), "head_sha", head)
		return nil
	}
	if trusted && head == prev {
		return nil
	}
	if o.movedHead == MovedHeadRebind {
		log.Warn("merge binding: run branch moved since the previous binding; rebinding to the current head (policy rebind)",
			"run_id", run.RunID, "step_id", exec.StepID, "repository_id", repoIDForLog(repoID),
			"bound_sha", prev, "bound_by", prevBy, "head_sha", head)
		return nil
	}
	where := "run branch " + run.BranchName
	if repoID != "" {
		where = fmt.Sprintf("run branch %s in repository %s", runBranchIn(run, repoID), repoID)
	}
	msg := fmt.Sprintf("%s moved since it was bound at %s (%s); it is now at %s. "+
		"The commits after the binding were not validated: choose a rework outcome so they are validated again",
		where, prev, prevBy, head)
	if !trusted {
		msg = fmt.Sprintf("%s has a binding this run did not record (%s); it is now at %s. "+
			"Choose a rework outcome so the run binds a head it validated", where, prev, head)
	}
	return bindingError(domain.ErrConflict, msg, HeadBindingDetail{
		Reason: ReasonRunBranchMovedSinceGate, HeadSHA: head, BoundSHA: prev, BoundBy: prevBy, RepositoryID: repoID,
	})
}

func repoIDForLog(repoID string) string {
	if repoID == "" {
		return domain.PrimaryRepositoryID
	}
	return repoID
}

func truncateForLog(s string) string {
	const n = 80
	if len(s) > n {
		return s[:n]
	}
	return s
}

// codeRepoIDs lists the run's affected non-primary repositories.
func codeRepoIDs(run *domain.Run) []string {
	var ids []string
	for _, id := range run.AffectedRepositories {
		if id != "" && id != repository.PrimaryRepositoryID {
			ids = append(ids, id)
		}
	}
	return ids
}

// runBranchIn is the run branch's name in repoID.
func runBranchIn(run *domain.Run, repoID string) string {
	if b := run.RepositoryBranches[repoID]; b != "" {
		return b
	}
	return run.BranchName
}

// codeRepoBinder resolves a code repository's client and its CommitBinder
// capability.
func (o *Orchestrator) codeRepoBinder(ctx context.Context, repoID string) (CommitBinder, error) {
	if o.repoClients == nil {
		return nil, fmt.Errorf("repository git clients not wired")
	}
	client, err := o.repoClients.Client(ctx, repoID)
	if err != nil {
		return nil, err
	}
	b, ok := client.(CommitBinder)
	if !ok {
		return nil, fmt.Errorf("repository %q client %T does not implement engine.CommitBinder", repoID, client)
	}
	return b, nil
}

// requireCodeRepoBinders refuses, before any branch exists, a run whose
// code repositories cannot be bound (P3-37): binding cannot capture or
// merge a repository whose client has no CommitBinder capability.
func (o *Orchestrator) requireCodeRepoBinders(ctx context.Context, run *domain.Run) error {
	for _, repoID := range codeRepoIDs(run) {
		if _, err := o.codeRepoBinder(ctx, repoID); err != nil {
			return domain.NewErrorWithCause(domain.ErrPrecondition,
				fmt.Sprintf("merge binding requires SHA-capable clients for code repositories (repository %q)", repoID), err)
		}
	}
	return nil
}

// mergeSource is what a bound merge merges.
type mergeSource struct {
	bound string // spine.bound_sha
	sha   string // the commit merged: the cascade commit, or bound
	// excludedTip is the run-branch tip at merge time when it was not
	// contained in the merge (commits after the binding were left out).
	excludedTip string
	// tipUnknown: the run branch exists (or could not be read) but whether
	// the merge contains its tip could not be decided. The branch is kept,
	// as for excluded commits: it may be the only ref to them (codex
	// review, round 2; the code-repository check already kept it).
	tipUnknown bool
}

// resolveMergeSource is the binding-on entry of every merge attempt. It
// returns the commit to merge, or done=true after it has recorded a
// fail-closed refusal (or put the run on the transient retry track).
// With binding off it returns the zero value and the caller merges the
// branch as before.
func (o *Orchestrator) resolveMergeSource(ctx context.Context, run *domain.Run) (mergeSource, bool, error) {
	if !o.bindingOn {
		return mergeSource{}, false, nil
	}
	bound, by, ok, present := trustedBinding(run.CommitMeta, run.RunID)
	if !ok {
		reason := "the run has no recorded binding (owner question 2: a run in flight at enablement fails closed)"
		if present {
			reason = fmt.Sprintf("the stored binding is malformed or was not recorded by this run (bound_sha %q, bound_by %q)",
				truncateForLog(bound), truncateForLog(by))
		}
		return o.bindingFailure(ctx, run, reason)
	}
	if err := o.binder.VerifyCommit(ctx, bound); err != nil {
		var gitErr *git.GitError
		if errors.As(err, &gitErr) && gitErr.IsRetryable() {
			observe.Logger(ctx).Warn("merge binding: transient failure verifying the bound commit, will retry",
				"run_id", run.RunID, "bound_sha", bound, "error", err)
			return mergeSource{}, true, o.retryMerge(ctx, run)
		}
		return o.bindingFailure(ctx, run, fmt.Sprintf("bound commit %s is not available: %v", bound, err))
	}
	src := mergeSource{bound: bound, sha: bound}
	if newStatus := run.CommitMeta["status"]; newStatus != "" {
		c, err := o.applyCommitStatusAt(ctx, run, newStatus, bound)
		switch {
		case errors.Is(err, errNotChildOfBound):
			// Not a failed rewrite but a commit that would carry more than
			// the bound commit into the merge (review round 1): refused like
			// a structural violation of the merge itself. Nothing has been
			// merged yet, so there is nothing to take back.
			observe.Logger(ctx).Error("merge binding: status cascade commit is not a child of the bound commit; refusing to merge",
				"run_id", run.RunID, "bound_sha", bound, "error", err)
			violation := &git.GitError{Kind: git.ErrKindPermanent, Op: "merge_binding",
				Message: "merge binding violation: " + err.Error()}
			if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", violation, ""); recErr != nil {
				return mergeSource{}, true, recErr
			}
			return mergeSource{}, true, o.handlePermanentMergeFailure(ctx, run, violation)
		case err != nil:
			// Today's "proceed with merge" semantics: a failed status rewrite
			// must not block the merge of the bound commit.
			observe.Logger(ctx).Warn("failed to apply commit status at the bound commit, proceeding with merge",
				"run_id", run.RunID, "bound_sha", bound, "error", err)
		default:
			src.sha = c
		}
	}
	return src, false, nil
}

// bindingFailure refuses a merge whose binding is missing, invalid or
// unavailable: the primary outcome is recorded as failed (class
// precondition) and the run takes the permanent-failure route — a publish
// workflow routes merge_failed back to its producer, a legacy
// terminal-commit workflow fails the run. Nothing is merged or pushed.
func (o *Orchestrator) bindingFailure(ctx context.Context, run *domain.Run, reason string) (mergeSource, bool, error) {
	failure := &git.GitError{Kind: git.ErrKindNotFound, Op: "merge_binding", Message: "merge binding: " + reason}
	observe.Logger(ctx).Error("merge binding: refusing to merge",
		"run_id", run.RunID, "branch", run.BranchName, "reason", reason)
	if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", failure, ""); recErr != nil {
		return mergeSource{}, true, recErr
	}
	return mergeSource{}, true, o.handlePermanentMergeFailure(ctx, run, failure)
}

// verifyBoundMerge checks a MergeBound result before anything is pushed
// (DC-3). Accepted shapes, with B = res.BaseSHA and S = src.sha:
//
//   - M == B and S is an ancestor of B (already up to date — a replay);
//   - M == S and B is an ancestor of S (a fast-forward);
//   - M's parents are exactly [B, S].
//
// Every accepted shape has B as an ancestor of M, so the non-forced push
// is always a fast-forward of the verified base, and M holds nothing but
// B and S. Anything else — another parent, a missing base, an error — is
// a violation. The run-branch tip is read only to report excluded
// commits: it never fails the merge, because anyone who can push the run
// branch could otherwise reset it (for example to main's tip) and refuse
// every approved merge. A tip it cannot judge keeps the branch
// (src.tipUnknown); only a missing branch counts as nothing excluded.
func (o *Orchestrator) verifyBoundMerge(ctx context.Context, binder CommitBinder, branch string, src *mergeSource, res git.MergeResult) error {
	m, base := res.SHA, res.BaseSHA
	if !isBoundObjectID(base) {
		return fmt.Errorf("merge reported no base commit")
	}
	if !isBoundObjectID(m) {
		return fmt.Errorf("merge reported no result commit")
	}
	switch m {
	case base:
		in, err := binder.IsAncestor(ctx, src.sha, base)
		if err != nil {
			return fmt.Errorf("check the bound commit is in the base: %w", err)
		}
		if !in {
			return fmt.Errorf("merge result %s is the base, which does not contain the bound commit %s", m, src.sha)
		}
	case src.sha:
		in, err := binder.IsAncestor(ctx, base, src.sha)
		if err != nil {
			return fmt.Errorf("check the fast-forward from the base: %w", err)
		}
		if !in {
			return fmt.Errorf("merge result %s does not descend from the base %s", m, base)
		}
	default:
		parents, err := binder.CommitParents(ctx, m)
		if err != nil {
			return fmt.Errorf("read the merge commit's parents: %w", err)
		}
		if len(parents) == 2 && parents[0] != base && parents[1] == src.sha && isBoundObjectID(parents[0]) {
			return &targetMovedError{merge: m, firstParent: parents[0], base: base, source: src.sha}
		}
		if len(parents) != 2 || parents[0] != base || parents[1] != src.sha {
			return fmt.Errorf("merge commit %s has parents %v, want exactly [%s %s]", m, parents, base, src.sha)
		}
	}

	tip, err := binder.ResolveBranchHead(ctx, branch)
	if err == nil && !isBoundObjectID(tip) {
		err = fmt.Errorf("operator returned a malformed head %q", truncateForLog(tip))
	}
	if err != nil {
		var gitErr *git.GitError
		if errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
			return nil // no run branch left: no commit of it can be lost
		}
		observe.Logger(ctx).Warn("merge binding: run branch tip unknown; keeping the run branch",
			"branch", branch, "error", err)
		src.tipUnknown = true
		return nil
	}
	in, err := binder.IsAncestor(ctx, tip, m)
	if err != nil {
		observe.Logger(ctx).Warn("merge binding: excluded-commits check failed; keeping the run branch",
			"branch", branch, "tip_sha", tip, "error", err)
		src.tipUnknown = true
		return nil
	}
	if !in {
		src.excludedTip = tip
	}
	return nil
}

// targetMovedError is verifyBoundMerge's verdict on a merge commit whose
// parents are exactly [P, S] with P not the reported base: the shape a
// write to the local target makes when it lands between MergeBound's base
// read and its merge (another writer; the worktree is not serialized
// until TASK-012). It is still a refusal — P was never verified as the
// merge's base, so the merge is neither pushed nor kept — but when the
// take-back restored the target to P (P descends from the base and the
// compare-and-swap held) the caller retries instead of taking the
// permanent route (review round 2): the retry computes on P and verifies
// against it, which is what binding off does in the same race.
type targetMovedError struct {
	merge, firstParent, base, source string
}

func (e *targetMovedError) Error() string {
	return fmt.Sprintf("merge commit %s has parents [%s %s], want exactly [%s %s]",
		e.merge, e.firstParent, e.source, e.base, e.source)
}

// takeBack is what takeBackRefusedMerge did.
type takeBack struct {
	detail string // for the outcome's failure detail
	// clean: the local target is known not to hold the refused merge (it
	// did not move, or it was restored).
	clean bool
	// restoredTo is the commit the target was moved back to; "" when it
	// was not moved or the take-back failed.
	restoredTo string
}

// takeBackRefusedMerge restores the local target after verifyBoundMerge
// refused a MergeBound result (review round 1). MergeBound has already
// moved the local target to the refused merge M, and "nothing is pushed"
// alone does not keep M off the published branch: the next governed merge
// computes its base on the local target, passes its own structural check
// with M as its first parent, and its non-forced push then publishes M;
// an in-place artifact write's auto-push does the same; and with auto-push
// off the local target IS the published branch. So the target is moved
// back, compare-and-swap (ResetBranch only moves a target still at M).
//
// It goes back to the state before this merge: the reported base, unless
// M is a merge whose first parent is a different descendant of the base —
// the target moved between the base read and the merge (another writer;
// the worktree is not serialized until TASK-012), and dropping that commit
// would lose a write that may already be published. A first parent that
// does not descend from the base, an unreadable parent list, or any other
// shape goes back to the base. When the verdict already read M's parents
// as [P, S] (a *targetMovedError) that P is the candidate, so a second
// parents read that fails cannot turn a kept write into a dropped one.
// Nothing is done when the target did not move (M == base). A failure is
// logged at Error — the local target then still holds the refused merge
// and must be reset before the next governed merge.
func (o *Orchestrator) takeBackRefusedMerge(ctx context.Context, binder CommitBinder, repoID, target string, res git.MergeResult, verdict error) takeBack {
	log := observe.Logger(ctx)
	m, base := res.SHA, res.BaseSHA
	if m == base && isBoundObjectID(m) {
		return takeBack{detail: "the local " + target + " was not moved", clean: true}
	}
	if !isBoundObjectID(m) || !isBoundObjectID(base) {
		log.Error("merge binding: refused merge NOT taken back (merge result incomplete); reset the local branch before the next governed merge",
			"repository_id", repoIDForLog(repoID), "branch", target, "merge_sha", m, "base_sha", base)
		return takeBack{detail: "the local " + target + " may still hold the refused merge (merge result incomplete)"}
	}
	restoreTo, first := base, ""
	var moved *targetMovedError
	if errors.As(verdict, &moved) && moved.merge == m {
		first = moved.firstParent
	} else if parents, err := binder.CommitParents(ctx, m); err == nil && len(parents) >= 2 {
		first = parents[0]
	}
	if first != "" && first != base && isBoundObjectID(first) {
		if in, err := binder.IsAncestor(ctx, base, first); err == nil && in {
			restoreTo = first
		}
	}
	if err := binder.ResetBranch(ctx, target, restoreTo, m); err != nil {
		log.Error("merge binding: refused merge NOT taken back; reset the local branch before the next governed merge",
			"repository_id", repoIDForLog(repoID), "branch", target, "merge_sha", m, "restore_to", restoreTo, "error", err)
		return takeBack{detail: "the local " + target + " still holds the refused merge (take-back failed)"}
	}
	log.Warn("merge binding: refused merge taken back",
		"repository_id", repoIDForLog(repoID), "branch", target, "merge_sha", m, "restored_to", restoreTo)
	return takeBack{detail: "the local " + target + " was restored to " + restoreTo, clean: true, restoredTo: restoreTo}
}

// refusalRetryable reports whether a merge verifyBoundMerge refused may
// simply run again — the run stays in committing for the scheduler's
// retry — instead of taking the permanent route (merge_failed back to
// execute, or a failed legacy run). It returns the outcome message for
// the retry. Both cases need a clean take-back: otherwise the retry would
// meet the unverified merge on the target and accept it as "already up to
// date".
//
//   - A read the check needed failed transiently (review round 1): not a
//     verdict on the merge.
//   - The target moved during the merge (review round 2): the merge's
//     parents are exactly [P, S] with S the merged commit, and the
//     take-back restored the target to P — which it does only when P
//     descends from the reported base and the target was still at the
//     refused merge. The target then holds what it held before the merge,
//     the concurrent write included, and the retry computes on P and
//     verifies against it. Treating it as permanent failed a correctly
//     reviewed legacy run, or sent a publish run back to execute for
//     rework nobody asked for, over a race binding off merges through. A
//     MergeBound that persistently reports a stale base would retry
//     without end (nothing is pushed on that path; each attempt logs at
//     Warn); the conformance suite pins BaseSHA (MergeBound and
//     ResetBranch cases, with the local target ahead of the remote).
//
// Not retried: a write that lands during a replay. The bound commit is
// then already in the base, git reports "already up to date" and the
// write itself as the result, and nothing tells that write from a commit
// the binder made — trusting "any descendant of the base" there would let
// a binder that merged the wrong commit in a replay through on the retry.
// It stays a refusal taken back to the base (KNOWN-LIMITATIONS 7).
//
// The retry message names contention on the unlocked target, so the
// outcome's class is remote_unavailable — the class classifyMergeFailure
// gives lock contention — not network.
func refusalRetryable(err error, tb takeBack) (string, bool) {
	if !tb.clean {
		return "", false
	}
	var moved *targetMovedError
	if errors.As(err, &moved) && tb.restoredTo != "" && tb.restoredTo == moved.firstParent && moved.firstParent != moved.base {
		return "merge binding: the target moved during the merge (contention on the unlocked target; will retry): " + err.Error() + "; " + tb.detail, true
	}
	var gitErr *git.GitError
	if errors.As(err, &gitErr) && gitErr.IsRetryable() {
		return "merge binding: the merge result could not be verified (will retry): " + err.Error() + "; " + tb.detail, true
	}
	return "", false
}

// mergePrimaryBound is mergePrimary with binding on: MergeBound of the
// resolved source, the structural check, then a non-forced push of
// exactly the merge commit.
func (o *Orchestrator) mergePrimaryBound(ctx context.Context, run *domain.Run, src *mergeSource) (bool, error) {
	log := observe.Logger(ctx)
	trailers := map[string]string{
		"Run-ID":   run.RunID,
		"Trace-ID": run.TraceID,
	}
	mergeResult, mergeErr := o.binder.MergeBound(ctx, git.MergeOpts{
		Source:    run.BranchName,
		SourceSHA: src.sha,
		Target:    authoritativeBranch,
		Strategy:  "merge-commit",
		Message:   fmt.Sprintf("Merge run %s: %s", run.RunID, run.TaskPath),
		Trailers:  trailers,
	})
	if mergeErr != nil {
		o.abortMerge(ctx)
		if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", mergeErr, ""); recErr != nil {
			return false, recErr
		}
		var gitErr *git.GitError
		if errors.As(mergeErr, &gitErr) && gitErr.IsRetryable() {
			log.Warn("transient merge failure, will retry",
				"run_id", run.RunID, "branch", run.BranchName, "bound_sha", src.bound, "error", mergeErr)
			return true, o.retryMerge(ctx, run)
		}
		// No collision renumber under binding (Decision 11): the renumber
		// commits on the run branch's tip, which would re-open window (c).
		log.Error("permanent merge failure",
			"run_id", run.RunID, "branch", run.BranchName, "bound_sha", src.bound, "error", mergeErr)
		return true, o.handlePermanentMergeFailure(ctx, run, mergeErr)
	}

	if err := o.verifyBoundMerge(ctx, o.binder, run.BranchName, src, mergeResult); err != nil {
		// The take-back runs before anything else can build on the
		// refused merge (TASK-012 moves this whole sequence under its
		// worktree lock).
		tb := o.takeBackRefusedMerge(ctx, o.binder, domain.PrimaryRepositoryID, authoritativeBranch, mergeResult, err)
		if msg, ok := refusalRetryable(err, tb); ok {
			// The target is back where it was (or at the write that moved
			// it) and nothing of the attempt survives: not a verdict on
			// the reviewed commit. Stay in committing and merge again, as
			// for a transient merge failure.
			retry := &git.GitError{Kind: git.ErrKindTransient, Op: "merge_binding", Message: msg}
			if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", retry, ""); recErr != nil {
				return false, recErr
			}
			log.Warn("merge binding: merge result not accepted, will retry",
				"run_id", run.RunID, "bound_sha", src.bound, "merge_sha", mergeResult.SHA,
				"base_sha", mergeResult.BaseSHA, "restored_to", tb.restoredTo, "error", err)
			return true, o.retryMerge(ctx, run)
		}
		violation := &git.GitError{Kind: git.ErrKindPermanent, Op: "merge_binding",
			Message: "merge binding violation: " + err.Error() + "; " + tb.detail}
		if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeResult.SHA, violation, ""); recErr != nil {
			return false, recErr
		}
		log.Error("merge binding: merge result failed verification; nothing pushed",
			"run_id", run.RunID, "bound_sha", src.bound, "merge_sha", mergeResult.SHA,
			"base_sha", mergeResult.BaseSHA, "error", err)
		return true, o.handlePermanentMergeFailure(ctx, run, violation)
	}
	if src.excludedTip != "" {
		log.Warn("merge_binding: commits after the bound head were not merged",
			"run_id", run.RunID, "bound_sha", src.bound, "tip_sha", src.excludedTip)
	}
	log.Info("run branch merged",
		"run_id", run.RunID,
		"branch", run.BranchName,
		"bound_sha", src.bound,
		"merged_sha", src.sha,
		"merge_sha", mergeResult.SHA,
		"fast_forward", mergeResult.FastForward,
	)

	if autoPushEnabled() {
		if pushErr := o.binder.PushCommit(ctx, "origin", authoritativeBranch, mergeResult.SHA); pushErr != nil {
			if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeResult.SHA, pushErr, "push"); recErr != nil {
				return false, recErr
			}
			var gitErr *git.GitError
			if errors.As(pushErr, &gitErr) && !gitErr.IsRetryable() {
				log.Error("auto-push: permanent push failure (auth or rejected), failing run",
					"run_id", run.RunID, "error", pushErr)
				return true, o.handlePermanentMergeFailure(ctx, run, pushErr)
			}
			log.Warn("auto-push: transient push failure, staying in committing for retry",
				"run_id", run.RunID, "error", pushErr)
			return true, o.retryMerge(ctx, run)
		}
	}

	if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeResult.SHA, nil, ""); recErr != nil {
		return false, recErr
	}
	return false, nil
}

// applyCommitStatusAt is the status cascade under binding (§7). It
// applies the same filters as applyCommitStatus, but to the bound commit:
// paths are what bound added since its merge-base with main, files are
// read at bound, and the rewrites become one CommitFilesOnto commit whose
// parent is bound. No checkout, no working-tree write and no ref move:
// the cascade commit is not on the run branch and reaches main only
// through the merge. It returns bound unchanged when nothing needs
// rewriting.
func (o *Orchestrator) applyCommitStatusAt(ctx context.Context, run *domain.Run, newStatus, bound string) (string, error) {
	base, err := o.cascadeBaseAt(ctx, run, newStatus, bound)
	if err != nil || base == "" {
		observe.Logger(ctx).Warn("merge-base for status cascade failed, falling back to primary only",
			"run_id", run.RunID, "error", err)
		base = ""
	}
	c, updates, err := o.cascadeCommitFrom(ctx, run, newStatus, bound, base)
	if err != nil {
		return "", err
	}
	if len(updates) == 0 {
		observe.Logger(ctx).Info("artifact statuses already match at the bound commit, no cascade commit needed",
			"run_id", run.RunID, "status", newStatus)
		return bound, nil
	}
	for path := range updates {
		observe.Logger(ctx).Info("artifact status updated at the bound commit",
			"run_id", run.RunID, "artifact_path", path, "new_status", newStatus, "cascade_sha", c)
	}
	return c, nil
}

// cascadeCommitFrom builds the bound cascade against base: run.TaskPath
// plus every .md file bound added since base (only run.TaskPath when base
// is ""), read at bound, rewritten into one CommitFilesOnto commit whose
// only parent is bound. It returns bound and no updates when nothing needs
// rewriting. It logs nothing on success, so cascadeBaseAt can use it to
// recompute an earlier attempt's cascade commit. CommitFilesOnto is
// deterministic and the commit carries this run's Run-ID and Trace-ID
// trailers, so the same run, status, bound commit and base always give the
// same commit, and another run's cascade never equals it.
func (o *Orchestrator) cascadeCommitFrom(ctx context.Context, run *domain.Run, newStatus, bound, base string) (string, map[string]string, error) {
	paths := o.cascadePathsFrom(ctx, run, base, bound)
	updates, err := o.collectStatusRewrites(ctx, run, bound, "at bound commit "+bound, paths, newStatus)
	if err != nil {
		return "", nil, err
	}
	if len(updates) == 0 {
		return bound, nil, nil
	}
	res, err := o.binder.CommitFilesOnto(ctx, bound, updates, git.CommitOpts{
		Message: statusCascadeMessage(newStatus, len(updates)),
		Trailers: map[string]string{
			"Run-ID":   run.RunID,
			"Trace-ID": run.TraceID,
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("commit status update onto %s: %w", bound, err)
	}
	if !isBoundObjectID(res.SHA) {
		return "", nil, fmt.Errorf("commit status update onto %s returned no commit id", bound)
	}
	if err := requireChildOf(ctx, o.binder, res.SHA, bound, "status cascade"); err != nil {
		return "", nil, err
	}
	return res.SHA, updates, nil
}

// errNotChildOfBound marks a commit the engine asked CommitFilesOnto to
// build on the bound commit that is not a child of exactly that commit.
var errNotChildOfBound = errors.New("not a child of the bound commit")

// requireChildOf checks that c — a commit CommitFilesOnto made for the
// engine (the status cascade, the recovery ledger) — has exactly one
// parent, bound (review round 1). The merge source may be the bound commit
// or a child the engine made itself (§0), and verifyBoundMerge only checks
// that the merge's parents are [base, source]: an implementation that
// built the commit on the run-branch tip instead (say, by committing in a
// persistent clone with the branch checked out) would carry every
// unreviewed tip commit into a merge that passes that check. A wrong
// parent list wraps errNotChildOfBound (a binding violation); an error
// reading it does not (a failed cascade, which the caller degrades as
// any other).
func requireChildOf(ctx context.Context, binder CommitBinder, c, bound, what string) error {
	parents, err := binder.CommitParents(ctx, c)
	if err != nil {
		return fmt.Errorf("read the parents of the %s commit %s: %w", what, c, err)
	}
	if len(parents) != 1 || parents[0] != bound {
		return fmt.Errorf("the %s commit %s has parents %v, want exactly [%s]: %w", what, c, parents, bound, errNotChildOfBound)
	}
	return nil
}

// cascadePathsFrom is cascadePaths for the bound commit: run.TaskPath plus
// every .md file bound added since base (cascadeBaseAt). A file added
// after the bound commit is never considered. An empty base, or a diff
// that fails, gives run.TaskPath alone.
func (o *Orchestrator) cascadePathsFrom(ctx context.Context, run *domain.Run, base, bound string) []string {
	paths := []string{run.TaskPath}
	if base == "" {
		return paths
	}
	diffs, err := o.git.Diff(ctx, base, bound)
	if err != nil {
		observe.Logger(ctx).Warn("diff for status cascade failed, falling back to primary only",
			"run_id", run.RunID, "error", err)
		return paths
	}
	return appendAddedMarkdown(paths, run.TaskPath, diffs)
}

// priorMergeWalkLimit bounds cascadeBaseAt's walk down main's first-parent
// chain. Between two attempts of one run, main gains a commit per other
// governed merge or in-place write; far fewer than this.
const priorMergeWalkLimit = 64

// cascadeBaseAt is the commit the bound cascade diffs bound against: its
// merge-base with main AS IT WAS when this run's merge was made, so every
// attempt computes the same paths and — CommitFilesOnto being
// deterministic — the same cascade commit (DC-2; review round 1).
//
// A plain MergeBase(main, bound) is not stable across attempts. Once an
// earlier attempt's merge reached main — a transient push failure (local
// main), a push that landed but reported an error, a partially-merged
// resume, a replay after an outcome-record failure — main contains bound,
// the merge-base is bound itself, the diff is empty and the cascade
// shrinks to run.TaskPath. That is a different commit as soon as the run
// added a child artifact, so the replay merged a second cascade commit
// and a second merge commit instead of finding main up to date.
//
// So when main already contains bound, this run's earlier merge is looked
// for on main's first-parent chain (from the tip that ResolveBranchHead
// reports — the same main MergeBase reads, local for CLIClient, origin for
// a remote-backed operator), and the merge-base is taken against that
// merge's first parent: main just before this run merged.
//
// The merge must provably be this run's (wave 3 review): a two-parent
// commit [P, C] whose second parent C is exactly the cascade commit this
// run computes against MergeBase(P, bound) (cascadeCommitFrom; it carries
// the run's Run-ID and Trace-ID trailers). Matching only the graph — "the
// second parent is bound or a child of bound" — let a run whose branch was
// reset onto an earlier run's merged commit B0 (the bound actor may
// force-push the branch) take that run's merge [x, B0] for its own: the
// cascade then diffed MergeBase(x, B0)..B0 and rewrote the status of every
// artifact the earlier run added, although this run's reviewed diff was
// empty. A merge of bound itself is never taken: it can only be this run's
// if the cascade needed no commit against its base, and then the plain
// merge-base (whose paths are a subset) needs none either, so the replay
// finds main up to date all the same. Anything else — bound on main for
// another reason (a run with no commits; a branch reset onto main or onto
// another run's commit), a walk past the limit, a read error — keeps the
// plain merge-base, the pre-review-round behaviour, whose cascade is
// run.TaskPath alone.
func (o *Orchestrator) cascadeBaseAt(ctx context.Context, run *domain.Run, newStatus, bound string) (string, error) {
	base, err := o.git.MergeBase(ctx, authoritativeBranch, bound)
	if err != nil || base != bound {
		return base, err
	}
	if prior, ok := o.priorMergeBase(ctx, run, newStatus, bound); ok {
		return prior, nil
	}
	return base, nil
}

// priorMergeBase walks main's first-parent chain from its tip for this
// run's earlier merge of bound (see cascadeBaseAt) and returns the
// merge-base the cascade of that merge was computed against.
func (o *Orchestrator) priorMergeBase(ctx context.Context, run *domain.Run, newStatus, bound string) (string, bool) {
	log := observe.Logger(ctx)
	x, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
	if err != nil || !isBoundObjectID(x) {
		log.Warn("merge binding: main head unavailable for the replay check; the cascade uses the plain merge-base",
			"run_id", run.RunID, "error", err)
		return "", false
	}
	for i := 0; i < priorMergeWalkLimit && x != bound; i++ {
		parents, err := o.binder.CommitParents(ctx, x)
		if err != nil {
			log.Warn("merge binding: replay check could not read main's history; the cascade uses the plain merge-base",
				"run_id", run.RunID, "commit", x, "error", err)
			return "", false
		}
		if len(parents) == 0 {
			return "", false
		}
		if len(parents) == 2 && isBoundObjectID(parents[0]) {
			if prior, ok := o.thisRunsCascadeMerge(ctx, run, newStatus, bound, parents[0], parents[1]); ok {
				return prior, true
			}
		}
		x = parents[0]
	}
	if x != bound {
		log.Warn("merge binding: replay check found no earlier merge of this run within the walk limit; the cascade uses the plain merge-base",
			"run_id", run.RunID, "bound_sha", bound, "limit", priorMergeWalkLimit)
	}
	return "", false
}

// thisRunsCascadeMerge reports whether the merge [first, second] on main
// is this run's earlier merge of bound, and returns the merge-base its
// cascade was computed against: second must be a child of exactly bound
// and equal the cascade commit this run builds against
// MergeBase(first, bound). Anything it cannot read or build is "not this
// run's" (the caller keeps walking, then falls back to the plain
// merge-base).
func (o *Orchestrator) thisRunsCascadeMerge(ctx context.Context, run *domain.Run, newStatus, bound, first, second string) (string, bool) {
	if !isBoundObjectID(second) || second == bound {
		return "", false
	}
	pp, err := o.binder.CommitParents(ctx, second)
	if err != nil || len(pp) != 1 || pp[0] != bound {
		return "", false
	}
	log := observe.Logger(ctx)
	prior, err := o.git.MergeBase(ctx, first, bound)
	if err != nil || prior == "" {
		log.Warn("merge binding: merge-base against an earlier merge of the bound commit failed; not taken as this run's",
			"run_id", run.RunID, "first_parent", first, "error", err)
		return "", false
	}
	c, _, err := o.cascadeCommitFrom(ctx, run, newStatus, bound, prior)
	if err != nil {
		log.Warn("merge binding: could not recompute the cascade of an earlier merge of the bound commit; not taken as this run's",
			"run_id", run.RunID, "merged_sha", second, "error", err)
		return "", false
	}
	if c != second {
		log.Info("merge binding: an earlier merge of the bound commit on main is not this run's; the replay check ignores it",
			"run_id", run.RunID, "bound_sha", bound, "merged_sha", second, "recomputed_sha", c)
		return "", false
	}
	return prior, true
}

// rebindAfterConvergence moves the binding to the run-branch head the
// convergence produced (§5, DC-6). Convergence moves the head on the
// engine's behalf, so without a rebind the evaluation gate would always
// trip; but it rebinds only from the bound head: h0 (resolved before
// EvaluateAndCommit) must equal the previous binding (or there is none),
// and the new head must descend from h0. Otherwise commits pushed to the
// main run branch while the divergence was open would become the binding
// unchecked, so the binding is left as it is and the evaluation gate
// fails closed (run_branch_moved_since_gate). Every failure is a Warn.
func (o *Orchestrator) rebindAfterConvergence(ctx context.Context, runID, divergenceID, h0 string, h0Err error) {
	log := observe.Logger(ctx)
	if h0Err != nil || !isBoundObjectID(h0) {
		log.Warn("merge binding: no pre-convergence head; binding unchanged",
			"run_id", runID, "divergence_id", divergenceID, "error", h0Err)
		return
	}
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		log.Warn("merge binding: reload run after convergence failed; binding unchanged",
			"run_id", runID, "divergence_id", divergenceID, "error", err)
		return
	}
	prev, _, trusted, present := trustedBinding(run.CommitMeta, run.RunID)
	if present && (!trusted || prev != h0) {
		log.Warn("merge binding: run branch moved during the divergence; binding unchanged, the evaluation gate will refuse",
			"run_id", runID, "divergence_id", divergenceID, "bound_sha", prev, "pre_convergence_sha", h0)
		return
	}
	head, err := o.binder.ResolveBranchHead(ctx, run.BranchName)
	if err != nil || !isBoundObjectID(head) {
		log.Warn("merge binding: post-convergence head unavailable; binding unchanged",
			"run_id", runID, "divergence_id", divergenceID, "error", err)
		return
	}
	in, err := o.binder.IsAncestor(ctx, h0, head)
	if err != nil || !in {
		log.Warn("merge binding: post-convergence head does not descend from the pre-convergence head; binding unchanged",
			"run_id", runID, "divergence_id", divergenceID, "pre_convergence_sha", h0, "head_sha", head, "error", err)
		return
	}
	meta := withEngineBinding(run.CommitMeta, &headBinding{
		head: head,
		by:   boundByConvergence(run.RunID, divergenceID),
		// No trusted binding before: any code-repository key present was
		// not written by this run.
		replaceRepoKeys: !present,
	})
	if err := o.store.SetCommitMeta(ctx, run.RunID, meta); err != nil {
		log.Warn("merge binding: store convergence rebind failed; binding unchanged",
			"run_id", runID, "divergence_id", divergenceID, "error", err)
		return
	}
	log.Info("merge binding: rebound after convergence",
		"run_id", runID, "divergence_id", divergenceID, "bound_sha", head)
}

// codeRepoBoundContained reports whether repoID's bound commit is already
// on its default branch — the condition under which binding keeps the
// loop-back skip of an already-merged code repository (P3-37). A missing
// binding is "not contained", so the repository is attempted again and
// fails closed there.
func (o *Orchestrator) codeRepoBoundContained(ctx context.Context, run *domain.Run, repoID string) (bool, error) {
	repoBound := run.CommitMeta[RepoBoundSHAKey(repoID)]
	if !isBoundObjectID(repoBound) {
		return false, nil
	}
	repo, err := o.repositories.Lookup(ctx, repoID)
	if err != nil {
		return false, err
	}
	if err := git.ValidateShortBranchName(repo.DefaultBranch); err != nil {
		return false, defaultBranchInvalid(repoID, err)
	}
	binder, err := o.codeRepoBinder(ctx, repoID)
	if err != nil {
		return false, err
	}
	head, err := binder.ResolveBranchHead(ctx, repo.DefaultBranch)
	if err != nil {
		return false, err
	}
	return binder.IsAncestor(ctx, repoBound, head)
}

// codeRepoHasExcludedCommits reports whether any code repository's run
// branch holds commits its default branch does not contain after the
// bound merges — commits the binding deliberately left out, which branch
// cleanup would otherwise delete (owner question 3 applies to every
// repository, not only the primary). It is decided from git state at
// completion, so a repository merged in an earlier attempt counts too.
// Any failure to decide answers true: keeping a branch is always safe,
// deleting one with unmerged commits is not. A branch that no longer
// exists has nothing to keep.
//
// Only a repository whose outcome is `merged` — merged by this run's
// bound merge — is judged (review round 1). One the operator resolved
// externally was merged outside Spine, so its default branch here need
// not contain the run tip, and the operator, not the binding, decided
// what it carries; a skipped one had nothing to merge. Both are cleaned
// up as with binding off, instead of keeping every branch of the run
// under a false "commits were not merged" warning.
func (o *Orchestrator) codeRepoHasExcludedCommits(ctx context.Context, run *domain.Run) bool {
	log := observe.Logger(ctx)
	if len(codeRepoIDs(run)) == 0 {
		return false
	}
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, run.RunID)
	if err != nil {
		log.Warn("merge_binding: keeping run branches; code repository merge outcomes unavailable",
			"run_id", run.RunID, "error", err)
		return true
	}
	status := make(map[string]domain.RepositoryMergeStatus, len(outcomes))
	for i := range outcomes {
		status[outcomes[i].RepositoryID] = outcomes[i].Status
	}
	for _, repoID := range codeRepoIDs(run) {
		keep := func(reason string, err error) bool {
			log.Warn("merge_binding: keeping run branches; code repository branch state unknown",
				"run_id", run.RunID, "repository_id", repoID, "reason", reason, "error", err)
			return true
		}
		switch st, ok := status[repoID]; {
		case !ok:
			return keep("no merge outcome", nil)
		case st != domain.RepositoryMergeStatusMerged:
			continue // resolved externally or skipped: not this run's merge to judge
		}
		if o.repositories == nil {
			return keep("no repository resolver", nil)
		}
		repo, err := o.repositories.Lookup(ctx, repoID)
		if err != nil {
			return keep("lookup", err)
		}
		binder, err := o.codeRepoBinder(ctx, repoID)
		if err != nil {
			return keep("client", err)
		}
		tip, err := binder.ResolveBranchHead(ctx, runBranchIn(run, repoID))
		if err != nil {
			var gitErr *git.GitError
			if errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
				continue // no run branch left in this repository
			}
			return keep("run branch head", err)
		}
		head, err := binder.ResolveBranchHead(ctx, repo.DefaultBranch)
		if err != nil {
			return keep("default branch head", err)
		}
		in, err := binder.IsAncestor(ctx, tip, head)
		if err != nil {
			return keep("ancestry", err)
		}
		if !in {
			log.Warn("merge_binding: commits after the bound head were not merged; keeping run branches",
				"run_id", run.RunID, "repository_id", repoID, "tip_sha", tip,
				"bound_sha", run.CommitMeta[RepoBoundSHAKey(repoID)])
			return true
		}
	}
	return false
}

// attemptCodeRepositoryMergeBound is attemptCodeRepositoryMerge with
// binding on: MergeBound of the repository's bound commit, the structural
// check and a non-forced push. A missing binding or a client without the
// capability is a failed outcome of class precondition.
func (o *Orchestrator) attemptCodeRepositoryMergeBound(ctx context.Context, run *domain.Run, repoID string, repo *repository.Repository, client git.GitClient, outcome domain.RepositoryMergeOutcome, trailers map[string]string) domain.RepositoryMergeOutcome {
	log := observe.Logger(ctx)
	fail := func(err error) domain.RepositoryMergeOutcome {
		class, detail := classifyMergeFailure(err)
		outcome.Status = domain.RepositoryMergeStatusFailed
		outcome.FailureClass = class
		outcome.FailureDetail = detail
		return outcome
	}
	binder, ok := client.(CommitBinder)
	if !ok {
		log.Error("merge binding: code repository client cannot bind", "repository_id", repoID)
		return fail(&git.GitError{Kind: git.ErrKindNotFound, Op: "merge_binding",
			Message: "merge binding: the repository client does not support SHA-bound merges"})
	}
	repoBound := run.CommitMeta[RepoBoundSHAKey(repoID)]
	if !isBoundObjectID(repoBound) {
		log.Error("merge binding: code repository has no recorded binding", "repository_id", repoID)
		return fail(&git.GitError{Kind: git.ErrKindNotFound, Op: "merge_binding",
			Message: "merge binding: the repository has no recorded binding"})
	}
	mergeResult, mergeErr := binder.MergeBound(ctx, git.MergeOpts{
		Source:    run.BranchName,
		SourceSHA: repoBound,
		Target:    repo.DefaultBranch,
		Strategy:  "merge-commit",
		Message:   fmt.Sprintf("Merge run %s: %s", run.RunID, run.TaskPath),
		Trailers:  trailers,
	})
	if mergeErr != nil {
		if _, abortErr := client.Merge(ctx, git.MergeOpts{Source: "--abort", Strategy: "abort"}); abortErr != nil {
			log.Warn("code repo merge abort failed", "repository_id", repoID, "error", abortErr)
		}
		log.Warn("code repo merge failed", "repository_id", repoID, "error", mergeErr)
		return fail(mergeErr)
	}
	src := mergeSource{bound: repoBound, sha: repoBound}
	if err := o.verifyBoundMerge(ctx, binder, runBranchIn(run, repoID), &src, mergeResult); err != nil {
		tb := o.takeBackRefusedMerge(ctx, binder, repoID, repo.DefaultBranch, mergeResult, err)
		if msg, ok := refusalRetryable(err, tb); ok {
			log.Warn("merge binding: code repository merge result not accepted, will retry",
				"repository_id", repoID, "bound_sha", repoBound, "merge_sha", mergeResult.SHA,
				"base_sha", mergeResult.BaseSHA, "restored_to", tb.restoredTo, "error", err)
			return fail(&git.GitError{Kind: git.ErrKindTransient, Op: "merge_binding", Message: msg})
		}
		log.Error("merge binding: code repository merge failed verification; nothing pushed",
			"repository_id", repoID, "bound_sha", repoBound, "merge_sha", mergeResult.SHA, "error", err)
		return fail(&git.GitError{Kind: git.ErrKindPermanent, Op: "merge_binding",
			Message: "merge binding violation: " + err.Error() + "; " + tb.detail})
	}
	if src.excludedTip != "" {
		log.Warn("merge_binding: commits after the bound head were not merged",
			"run_id", run.RunID, "repository_id", repoID, "bound_sha", repoBound, "tip_sha", src.excludedTip)
	}
	if autoPushEnabled() {
		if pushErr := binder.PushCommit(ctx, "origin", repo.DefaultBranch, mergeResult.SHA); pushErr != nil {
			out := fail(pushErr)
			out.FailureDetail = fmt.Sprintf("local merge succeeded (sha=%s) but push failed: %s",
				mergeResult.SHA, out.FailureDetail)
			log.Warn("code repo push after merge failed",
				"repository_id", repoID, "merge_sha", mergeResult.SHA, "error", pushErr)
			return out
		}
	}
	outcome.Status = domain.RepositoryMergeStatusMerged
	outcome.MergeCommitSHA = mergeResult.SHA
	mergedAt := time.Now()
	outcome.MergedAt = &mergedAt
	return outcome
}

// checkRetryHeadSHA validates an operator's retry head_sha (DC-7): with
// binding on it must equal the code repository's current run-branch head,
// and the retry then rebinds to it. It returns the SHA to rebind to.
func (o *Orchestrator) checkRetryHeadSHA(ctx context.Context, run *domain.Run, repoID, headSHA string) (string, error) {
	if headSHA == "" {
		return "", nil
	}
	if !o.bindingOn {
		return "", bindingError(domain.ErrUnavailable,
			"head_sha needs SHA-bound merges, which are not enabled",
			HeadBindingDetail{Reason: ReasonExpectedHeadUnsupported, ExpectedHeadSHA: headSHA, RepositoryID: repoID})
	}
	binder, err := o.codeRepoBinder(ctx, repoID)
	if err != nil {
		return "", headUnavailable(repoID, err)
	}
	head, err := binder.ResolveBranchHead(ctx, runBranchIn(run, repoID))
	if err != nil {
		return "", headUnavailable(repoID, err)
	}
	if head != headSHA {
		return "", bindingError(domain.ErrConflict,
			fmt.Sprintf("run branch in repository %s is at %s, not at the given head_sha %s", repoID, head, headSHA),
			HeadBindingDetail{Reason: ReasonRunBranchMoved, HeadSHA: head, ExpectedHeadSHA: headSHA, RepositoryID: repoID})
	}
	return head, nil
}

// statusCascadeMessage is the cascade commit's summary line, shared by
// the binding-off and binding-on cascades.
func statusCascadeMessage(newStatus string, n int) string {
	if n > 1 {
		return fmt.Sprintf("Cascade artifact status to %s across %d artifacts", newStatus, n)
	}
	return fmt.Sprintf("Update artifact status to %s", newStatus)
}

// errNoTrustedBinding is returned by recovery paths that need the run's
// binding and find none this run recorded.
var errNoTrustedBinding = errors.New("the run has no trusted merge binding")

// trustedPrimaryBinding returns the run's trusted primary bound head.
func trustedPrimaryBinding(run *domain.Run) (string, error) {
	sha, _, ok, _ := trustedBinding(run.CommitMeta, run.RunID)
	if !ok {
		return "", errNoTrustedBinding
	}
	return sha, nil
}
