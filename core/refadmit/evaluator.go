package refadmit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// RunReader is the read surface Decide needs. It is satisfied, with no
// store-interface change, by *store.PostgresStore, the union
// store.Store and SMP's spinecore.RunStore.
//
// Contract: a missing run or branch is reported as a *domain.SpineError
// with Code == domain.ErrNotFound (what adapters/store produces). Any
// other error is an evaluation failure, which Decide returns and the
// caller turns into a refusal.
type RunReader interface {
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)
	GetBranch(ctx context.Context, branchID string) (*domain.Branch, error)
}

// StepDefLookup resolves the definition of a step in a run's workflow.
// It is the review-freeze hook (TASK-016 owner question 4): Decide asks
// it for the type of the step the bound actor holds, and refuses a
// non-operator while that step is a review or engine-internal step.
//
// The signature is the one *engine.Orchestrator (which loads the run's
// pinned workflow version) and the standalone gateway's store fallback
// already have, so either can be passed as is, and TASK-014's API-path
// freeze can share the same source. A nil definition means "cannot
// resolve", which Decide treats as a refusal (fail closed); the
// returned *domain.Run is ignored.
type StepDefLookup interface {
	LookupStepDef(ctx context.Context, runID, stepID string) (*domain.StepDefinition, *domain.Run)
}

// FrozenStepType reports whether a run or divergence branch is frozen
// for non-operators while its current step has type t: a `review` step
// (owner question 4: the reviewer judges the branch, it does not edit
// it) or an `internal` step (engine-owned, such as task-default's
// `publish` between approval and the governed merge; the same set as
// TASK-014's API-path freeze, its I6 as amended).
func FrozenStepType(t domain.StepType) bool {
	return t == domain.StepTypeReview || t == domain.StepTypeInternal
}

// Request is one ref update to admit.
type Request struct {
	// Ref is the fully-qualified ref the push updates.
	Ref string
	// Delete is true when the update deletes the ref.
	Delete bool
	// Actor is the authenticated principal; nil means unauthenticated
	// (for example the standalone gateway's devMode).
	Actor *domain.Actor
	// RepositoryID is the repository the push targets; "" and
	// domain.PrimaryRepositoryID both mean the workspace primary.
	RepositoryID string
}

// Reason is the precise cause of a Decision. It goes to the server log;
// the wire text comes from Decision.Message, which never names who is
// bound.
type Reason string

const (
	// ReasonInvalidRef: the ref carries a control byte (rule 0, every
	// principal).
	ReasonInvalidRef Reason = "invalid_ref"
	// ReasonShadowingRef: creating or updating a ref with a non-leading
	// "refs" or any "HEAD" component (rule 0b, every principal).
	ReasonShadowingRef Reason = "shadowing_ref"
	// ReasonOrdinaryBranch: an ordinary branch; the caller continues to
	// branchprotect.Policy.
	ReasonOrdinaryBranch Reason = "ordinary_branch"
	// ReasonOperator: operator or admin; the store is never read.
	ReasonOperator Reason = "operator"
	// ReasonOperatorRequired: an init, reserved or non-branch ref pushed
	// by a non-operator.
	ReasonOperatorRequired Reason = "operator_required"
	// ReasonDeleteRequiresOperator: a non-operator deleting a run or
	// divergence branch (ADR-032 §5 parity).
	ReasonDeleteRequiresOperator Reason = "delete_requires_operator"
	// ReasonNotBound: the actor does not hold the branch's current step,
	// is below contributor, is unauthenticated, or the run or branch is
	// not open.
	ReasonNotBound Reason = "not_bound"
	// ReasonResolverUnavailable: no RunReader or no StepDefLookup is
	// wired, so a non-operator cannot be admitted (fail closed, I2).
	ReasonResolverUnavailable Reason = "resolver_unavailable"
	// ReasonNotEvaluated: an earlier store-backed ref in the same push
	// was already refused, so this one is refused without a lookup.
	ReasonNotEvaluated Reason = "not_evaluated"
	// ReasonUnresolved: the ref names no branch the store knows: an
	// unknown run, a branch name that is not the run's own, or no
	// matching divergence branch.
	ReasonUnresolved Reason = "unresolved"
	// ReasonReviewFrozen: the actor is bound, but the step is a review
	// or internal step (FrozenStepType).
	ReasonReviewFrozen Reason = "review_frozen"
	// ReasonStepUnresolved: the actor is bound, but the step's
	// definition cannot be resolved, so the freeze cannot be ruled out.
	ReasonStepUnresolved Reason = "step_unresolved"
	// ReasonBoundActor: the actor holds the branch's current step.
	ReasonBoundActor Reason = "bound_actor"
)

// Decision is the outcome of admitting one ref.
type Decision struct {
	Allow  bool
	Class  Class
	Reason Reason
	// RunID is the run the ref was resolved against, when any.
	RunID string
	// ExecutionID is the bound execution, on a ReasonBoundActor allow.
	ExecutionID string
	// Ref is the classification Decide worked from.
	Ref Ref
}

// maxEcho bounds a ref name echoed into wire text. A ref under the
// spine namespace that reaches a run or divergence message is already
// bounded by maxReservedShort; this also bounds the operator-only and
// shadowing messages, whose refs are bounded only by the parser's
// command-section cap.
const maxEcho = maxReservedShort

// Message returns the wire text for a refusal ("" for an allow). It is
// prefixed "ref-admission:" and never names who is bound. A ref is
// echoed only for classes other than ClassInvalid: control bytes are
// never echoed, and the pre-receive parser (INIT-023 TASK-001) has
// already refused them on every command line.
func (d Decision) Message() string {
	if d.Allow {
		return ""
	}
	short := echo(d.Ref.Short)
	switch d.Reason {
	case ReasonInvalidRef:
		return "ref-admission: ref name contains a control byte; push refused"
	case ReasonShadowingRef:
		return fmt.Sprintf("ref-admission: %s has a 'refs' component after the first or a 'HEAD' component, "+
			"which would shadow another ref; push refused (deleting it is allowed)", echo(d.Ref.Full))
	case ReasonDeleteRequiresOperator:
		return fmt.Sprintf("ref-admission: deleting %s requires the operator role", short)
	case ReasonResolverUnavailable:
		return fmt.Sprintf("ref-admission: run-branch admission is not configured on this server; operator role required for %s", short)
	case ReasonNotEvaluated:
		return fmt.Sprintf("ref-admission: %s not evaluated (push already refused)", short)
	case ReasonReviewFrozen:
		return fmt.Sprintf("ref-admission: %s is frozen while its current step is a review or engine-internal step; operator role required", short)
	case ReasonStepUnresolved:
		return fmt.Sprintf("ref-admission: cannot resolve the current step of %s; operator role required", short)
	}
	switch d.Class {
	case ClassRunBranch:
		return fmt.Sprintf("ref-admission: %s is a run branch; only the actor assigned to the run's current step, or an operator, may push it", short)
	case ClassDivergenceBranch:
		return fmt.Sprintf("ref-admission: %s is a divergence branch; only the actor assigned to that branch's current step, or an operator, may push it", short)
	case ClassInitBranch:
		return "ref-admission: spine/init is reserved for 'spine init-repo'; operator role required"
	case ClassReserved:
		return fmt.Sprintf("ref-admission: %s is in the reserved spine namespace; operator role required", short)
	case ClassNonBranch:
		return fmt.Sprintf("ref-admission: %s is not a branch; only operators may push tags or other non-branch refs", echo(d.Ref.Full))
	}
	return fmt.Sprintf("ref-admission: %s refused", short)
}

// EvaluationErrorMessage is the wire text for a ref whose Decide
// returned an error (a store failure). The cause goes to the server
// log only.
func EvaluationErrorMessage(ref string) string {
	return fmt.Sprintf("ref-admission: evaluation error on %s", echo(Classify(ref).Short))
}

// echo bounds s to maxEcho bytes for wire text, cutting at a UTF-8
// rune start so the cut never splits a character.
func echo(s string) string {
	if len(s) <= maxEcho {
		return s
	}
	cut := maxEcho
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// maxDivergenceSplits caps the GetBranch lookups one divergence ref can
// cause (TASK-016 DC-5). Every '/' after a segment that starts with
// "<run-id>-div-" is a candidate split, so without a cap one long ref
// drives one read per '/'. Eight covers a divergence definition ID and
// a branch ID with several '/' each.
const maxDivergenceSplits = 8

// Evaluator admits the ref updates of ONE push. It memoizes run,
// execution, divergence-branch and step-definition reads by key for the
// life of the push and, once a store-backed ref has been refused,
// refuses the remaining run and divergence refs without further reads
// (Decision 9), so an unbound pusher costs about two reads per push
// whatever the number of refs, and a bound pusher repeating the refs it
// is admitted for costs no read after the first. It is not safe for
// concurrent use.
type Evaluator struct {
	rr   RunReader
	defs StepDefLookup

	runs  map[string]*domain.Run
	execs map[string][]domain.StepExecution
	steps map[string]*domain.StepDefinition
	// branches memoizes findBranch by run ID and divergence path,
	// including "no such branch" (a nil entry). Without it a push that
	// repeats an admitted divergence ref thousands of times (the
	// command section allows ~8,000 within its 1 MiB cap, and the
	// parser does not dedupe) cost one GetBranch per repetition, since
	// the not_evaluated short-circuit only trips after a refusal
	// (TASK-016 review round 1).
	branches map[string]*domain.Branch

	// denied is set once a store-backed ref is refused (or errors).
	denied bool
}

// NewEvaluator returns an Evaluator over rr. A nil rr is valid: every
// run and divergence ref is then operator-only (resolver_unavailable),
// and operators and every other class are unaffected.
//
// The review-freeze hook is WithStepDefs; without it, too, run and
// divergence refs are operator-only (owner question 4, fail closed).
func NewEvaluator(rr RunReader) *Evaluator {
	return &Evaluator{
		rr:       rr,
		runs:     map[string]*domain.Run{},
		execs:    map[string][]domain.StepExecution{},
		steps:    map[string]*domain.StepDefinition{},
		branches: map[string]*domain.Branch{},
	}
}

// WithStepDefs installs the review-freeze hook and returns e. A nil l
// leaves non-operators unable to push run and divergence branches.
func (e *Evaluator) WithStepDefs(l StepDefLookup) *Evaluator {
	e.defs = l
	return e
}

// Decide admits one ref update. Rules, in order (TASK-016 §1.3 with
// the design-check amendments):
//
//   - Rule 0: ClassInvalid → Deny invalid_ref, for every principal.
//   - Rule 0b: Shadowing, unless deleting → Deny shadowing_ref, for
//     every principal.
//   - Rule 1: ClassBranch → Allow ordinary_branch; the caller continues
//     to branchprotect.Policy.
//   - Rule 2: operator or admin → Allow operator, with no store read.
//   - Rule 3: init, reserved and non-branch refs → Deny
//     operator_required.
//   - Rule 4: run and divergence pre-checks → Deny
//     delete_requires_operator; not_bound (nil actor, empty ID, role
//     below contributor); resolver_unavailable (no reader or no
//     step-definition hook); not_evaluated (an earlier store-backed ref
//     in this push was refused).
//   - Rules 5-7 and the review freeze: decideBound.
//
// A non-nil error is a store failure: the caller must refuse the push.
// Rules 0 and 0b sit before the operator short-circuit on purpose: no
// principal can create a truncated refs/heads/spine or a shadow of
// main, not even by a client bug; deleting a shadowing ref stays
// possible (subject to the other rules) so it can be cleaned up.
func (e *Evaluator) Decide(ctx context.Context, req Request) (Decision, error) {
	c := Classify(req.Ref)
	d := Decision{Class: c.Class, RunID: c.RunID, Ref: c}

	if c.Class == ClassInvalid {
		d.Reason = ReasonInvalidRef
		return d, nil
	}
	if c.Shadowing && !req.Delete {
		d.Reason = ReasonShadowingRef
		return d, nil
	}
	if c.Class == ClassBranch {
		d.Allow, d.Reason = true, ReasonOrdinaryBranch
		return d, nil
	}
	if isOperator(req.Actor) {
		d.Allow, d.Reason = true, ReasonOperator
		return d, nil
	}
	if c.Class != ClassRunBranch && c.Class != ClassDivergenceBranch {
		d.Reason = ReasonOperatorRequired
		return d, nil
	}
	switch {
	case req.Delete:
		d.Reason = ReasonDeleteRequiresOperator
		return d, nil
	case !isContributor(req.Actor):
		d.Reason = ReasonNotBound
		return d, nil
	case e.rr == nil || e.defs == nil:
		d.Reason = ReasonResolverUnavailable
		return d, nil
	case e.denied:
		d.Reason = ReasonNotEvaluated
		return d, nil
	}

	out, err := e.decideBound(ctx, c, req)
	if err != nil || !out.Allow {
		e.denied = true
	}
	return out, err
}

// decideBound applies rules 5-7 and the review freeze to a run or
// divergence ref pushed by a contributor-or-above actor.
func (e *Evaluator) decideBound(ctx context.Context, c Ref, req Request) (Decision, error) {
	d := Decision{Class: c.Class, RunID: c.RunID, Ref: c, Reason: ReasonNotBound}

	// 5. The run must exist and be active. Every other status refuses:
	//    pending, paused, committing (the F2 merge window),
	//    partially-merged and every terminal state.
	run, err := e.getRun(ctx, c.RunID)
	if err != nil {
		if isNotFound(err) {
			d.Reason = ReasonUnresolved
			return d, nil
		}
		return d, err
	}
	if run.Status != domain.RunStatusActive {
		return d, nil
	}

	// 6. The binding target: which (branch, step) the actor must hold.
	repo := repoOrPrimary(req.RepositoryID)
	wantBranch, wantStep := "", run.CurrentStepID
	switch c.Class {
	case ClassRunBranch:
		// The ref must be exactly this run's branch (case-sensitive), so
		// a forged name that reuses a real run's suffix resolves to
		// nothing. During a divergence CurrentStepID is
		// "divergence:<id>", which no run-level execution carries, so
		// the parent run branch is operator-only while diverged.
		want := run.BranchName
		if repo != domain.PrimaryRepositoryID {
			if b := run.RepositoryBranches[repo]; b != "" {
				want = b
			}
		}
		if c.Short != want {
			d.Reason = ReasonUnresolved
			return d, nil
		}
	case ClassDivergenceBranch:
		br, err := e.findBranch(ctx, run.RunID, c.DivergencePath)
		if err != nil {
			return d, err
		}
		if br == nil {
			d.Reason = ReasonUnresolved
			return d, nil
		}
		if br.Status != domain.BranchStatusPending && br.Status != domain.BranchStatusInProgress {
			return d, nil
		}
		wantBranch, wantStep = br.BranchID, br.CurrentStepID
	}
	if wantStep == "" {
		return d, nil
	}

	// 7. Latest attempt wins (DC-6): among the executions of the target
	//    (branch, step), take the LAST in store order (created_at) and
	//    admit only if that one is held by the actor, assigned or in
	//    progress, on the target repository. An older attempt a crash
	//    left live cannot authorize a push once a newer one exists, and
	//    the open slot (assigned with no actor) never matches.
	execs, err := e.listExecs(ctx, run.RunID)
	if err != nil {
		return d, err
	}
	var latest *domain.StepExecution
	for i := range execs {
		if execs[i].BranchID == wantBranch && execs[i].StepID == wantStep {
			latest = &execs[i]
		}
	}
	if latest == nil || latest.ActorID != req.Actor.ActorID || !isLive(latest.Status) ||
		repoOrPrimary(latest.RepositoryID) != repo {
		return d, nil
	}

	// Review freeze (owner question 4): even the bound actor may not
	// move the branch while its current step is a review or internal
	// step. An unresolvable definition refuses too (fail closed).
	def := e.lookupStep(ctx, run.RunID, wantStep)
	if def == nil {
		d.Reason = ReasonStepUnresolved
		return d, nil
	}
	if FrozenStepType(def.Type) {
		d.Reason = ReasonReviewFrozen
		return d, nil
	}

	d.Allow, d.Reason, d.ExecutionID = true, ReasonBoundActor, latest.ExecutionID
	return d, nil
}

// findBranch resolves a divergence ref's DivergencePath
// ("<divergence-id>/<branch-id…>") to its store record. The split
// between the two is ambiguous when either contains '/', so each split
// whose divergence part has the "<run-id>-div-" prefix the engine
// gives it (core/divergence/service.go) is tried in order, at most
// maxDivergenceSplits of them (DC-5). A match must belong to this run
// and divergence: the branch ID alone does not identify the ref. The
// outcome, found or not, is memoized for the push; a store error is
// not (it refuses the rest of the push anyway).
func (e *Evaluator) findBranch(ctx context.Context, runID, path string) (*domain.Branch, error) {
	k := runID + "\x00" + path
	if b, ok := e.branches[k]; ok {
		return b, nil
	}
	b, err := e.resolveBranch(ctx, runID, path)
	if err != nil {
		return nil, err
	}
	e.branches[k] = b
	return b, nil
}

// resolveBranch is findBranch's uncached lookup.
func (e *Evaluator) resolveBranch(ctx context.Context, runID, path string) (*domain.Branch, error) {
	prefix := runID + "-div-"
	tried := 0
	for i := 0; i < len(path); i++ {
		if path[i] != '/' {
			continue
		}
		divID, suffix := path[:i], path[i+1:]
		if suffix == "" || !strings.HasPrefix(divID, prefix) {
			continue
		}
		if tried == maxDivergenceSplits {
			return nil, nil
		}
		tried++
		b, err := e.rr.GetBranch(ctx, divID+"-"+suffix)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("refadmit: get branch for run %s: %w", runID, err)
		}
		if b != nil && b.RunID == runID && b.DivergenceID == divID {
			return b, nil
		}
	}
	return nil, nil
}

// DecideBoundToRun reports whether actor may act on runID as one of its
// participants, for API entry points that attach new work to a run
// rather than push a ref (TASK-016 owner question 6:
// divergence.create_branch). It allows:
//
//   - an operator or admin, with no store read; or
//   - a contributor-or-above actor who holds a live cursor of the
//     active run: the latest execution (assigned or in progress) of the
//     run-level current step, or of the current step of one of the
//     run's open divergence branches.
//
// Everything else is refused with the same Reasons Decide uses
// (not_bound, unresolved, resolver_unavailable). It never applies the
// review freeze, and the returned Decision has no Ref.
func (e *Evaluator) DecideBoundToRun(ctx context.Context, actor *domain.Actor, runID string) (Decision, error) {
	d := Decision{RunID: runID, Reason: ReasonNotBound}
	if isOperator(actor) {
		d.Allow, d.Reason = true, ReasonOperator
		return d, nil
	}
	if !isContributor(actor) {
		return d, nil
	}
	if e.rr == nil {
		d.Reason = ReasonResolverUnavailable
		return d, nil
	}
	run, err := e.getRun(ctx, runID)
	if err != nil {
		if isNotFound(err) {
			d.Reason = ReasonUnresolved
			return d, nil
		}
		return d, err
	}
	if run.Status != domain.RunStatusActive {
		return d, nil
	}
	execs, err := e.listExecs(ctx, run.RunID)
	if err != nil {
		return d, err
	}

	// Latest attempt per (branch, step), in first-seen order.
	type key struct{ branch, step string }
	latest := map[key]int{}
	var order []key
	for i := range execs {
		k := key{execs[i].BranchID, execs[i].StepID}
		if _, seen := latest[k]; !seen {
			order = append(order, k)
		}
		latest[k] = i
	}
	lookups := 0
	for _, k := range order {
		x := &execs[latest[k]]
		if x.ActorID != actor.ActorID || !isLive(x.Status) {
			continue
		}
		if k.branch == "" {
			if k.step == run.CurrentStepID {
				d.Allow, d.Reason, d.ExecutionID = true, ReasonBoundActor, x.ExecutionID
				return d, nil
			}
			continue
		}
		if lookups == maxDivergenceSplits {
			break
		}
		lookups++
		b, err := e.rr.GetBranch(ctx, k.branch)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return d, fmt.Errorf("refadmit: get branch for run %s: %w", runID, err)
		}
		if b != nil && b.RunID == run.RunID && b.CurrentStepID == k.step &&
			(b.Status == domain.BranchStatusPending || b.Status == domain.BranchStatusInProgress) {
			d.Allow, d.Reason, d.ExecutionID = true, ReasonBoundActor, x.ExecutionID
			return d, nil
		}
	}
	return d, nil
}

func (e *Evaluator) getRun(ctx context.Context, runID string) (*domain.Run, error) {
	if r, ok := e.runs[runID]; ok {
		return r, nil
	}
	r, err := e.rr.GetRun(ctx, runID)
	if err != nil {
		if isNotFound(err) {
			return nil, err
		}
		return nil, fmt.Errorf("refadmit: get run %s: %w", runID, err)
	}
	if r == nil {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	e.runs[runID] = r
	return r, nil
}

func (e *Evaluator) listExecs(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	if x, ok := e.execs[runID]; ok {
		return x, nil
	}
	x, err := e.rr.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("refadmit: list step executions of run %s: %w", runID, err)
	}
	e.execs[runID] = x
	return x, nil
}

func (e *Evaluator) lookupStep(ctx context.Context, runID, stepID string) *domain.StepDefinition {
	k := runID + "\x00" + stepID
	if def, ok := e.steps[k]; ok {
		return def
	}
	def, _ := e.defs.LookupStepDef(ctx, runID, stepID)
	e.steps[k] = def
	return def
}

func isOperator(a *domain.Actor) bool {
	return a != nil && a.Role.HasAtLeast(domain.RoleOperator)
}

// isContributor is the role floor for being bound: the same floor as
// step.submit and execution.claim (core/auth/permissions.go).
func isContributor(a *domain.Actor) bool {
	return a != nil && a.ActorID != "" && a.Role.HasAtLeast(domain.RoleContributor)
}

func isLive(s domain.StepExecutionStatus) bool {
	return s == domain.StepStatusAssigned || s == domain.StepStatusInProgress
}

// repoOrPrimary maps "" to domain.PrimaryRepositoryID and leaves every
// other value alone (DC-7). Executions store "spine" for primary steps
// (legacy rows may hold ""), and the gateway passes "" for
// /git/{ws}/... but the literal "spine" for /git/{ws}/spine/...; both
// sides are normalized before comparing.
func repoOrPrimary(id string) string {
	if id == "" {
		return domain.PrimaryRepositoryID
	}
	return id
}

func isNotFound(err error) bool {
	var se *domain.SpineError
	return errors.As(err, &se) && se.Code == domain.ErrNotFound
}
