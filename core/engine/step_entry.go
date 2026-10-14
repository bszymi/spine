package engine

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/refadmit"
	"github.com/bszymi/spine/core/workflow"
)

// StepEntryPolicy configures what the engine checks before an actor is bound
// to a step execution, and what happens to live executions when a run ends
// (INIT-023 TASK-007; audit P3-3, P3-4, P3-15, P3-16 and the F3
// separation-of-duties amplifier).
//
// Before this policy the engine's two binding paths disagreed: ClaimStep
// enforced the execution's allow-list, eligible_actor_types and
// required_skills, while AssignStep bound any string to any step, never
// looked at execution.mode, preconditions or run status, and nothing stopped
// the executor of a run from being bound to its review. Every entry point
// also ignored run status, and a cancelled run's live executions stayed
// bindable and submittable.
//
// The zero value is the pre-INIT-023 behaviour: no bool is read on any path,
// and with Actors == nil the only always-on change (ClaimStep's actor lookup
// going through entryActor) is the same call as before. Embedders opt in
// field by field; `spine serve` installs StrictStepEntryPolicy() unless
// SPINE_STEP_ENTRY_POLICY=legacy.
type StepEntryPolicy struct {
	// EnforceRunStatus (P3-16): AssignStep, ClaimStep, AcknowledgeStep,
	// IngestResult, SubmitStepResult, ActivateStep and RetryStep refuse a
	// run whose status is not `active` (409), the pull-model candidate
	// listing hides executions of non-active runs, and a late submit on an
	// execution the engine terminated with its run answers 409 instead of
	// the idempotent terminal response.
	EnforceRunStatus bool
	// EnforceEligibility (P3-3): the actor must resolve and be `active`; on
	// AssignStep it must also be in the effective eligible_actor_ids list
	// and match the step's eligible_actor_types. ClaimStep keeps its own
	// allow-list and type checks and gains only existence + active status.
	EnforceEligibility bool
	// EnforceExecutionMode (P3-15): engine-owned steps (`type: internal` or
	// `mode: spine_only`) cannot be assigned or claimed, and automated_only /
	// ai_only / human_only bind only automated_system / ai_agent / human
	// actors respectively.
	EnforceExecutionMode bool
	// EnforcePreconditions (P3-4): AssignStep and ClaimStep run the engine's
	// own precondition evaluator, and that evaluator becomes strict: unknown
	// types (including `custom`), cross_artifact_valid without a validator
	// and discussions_resolved without a checker fail instead of passing,
	// and field_value / field_present on id, type, title and status read the
	// typed artifact fields (the parser keeps them out of Metadata).
	EnforcePreconditions bool
	// RequireSkillsOnAssign: AssignStep requires the step's required_skills
	// (ClaimStep already checks them). Fails closed (503) when no skill
	// lookup is wired, unlike ClaimStep's silent skip.
	RequireSkillsOnAssign bool
	// SeparationOfDuties: a run author (any actor bound, now or ever, to an
	// execution of the run whose step type is neither review nor internal —
	// manual, automated, convergence, or an empty or unknown type; see
	// isAuthorStepType) cannot be bound to a review step of that run, and an
	// actor holding a live review execution cannot be bound to an author
	// step of the same run.
	SeparationOfDuties bool
	// ReofferToPriorHolder (audit P3-6 residual; INIT-023 TASK-014 owner
	// question 7): ClaimStep of an author step (isAuthorStepType — every
	// type whose bound actor may write the run or divergence branch through
	// push admission or the standalone API) is refused (403) for any actor
	// other than the step's prior holder in this run. TASK-014 limits API
	// writes to a run branch to the actor bound to the run's current step,
	// and TASK-016 does the same for pushes; without this rule an eligible
	// contributor could still claim a colleague's step the moment it is
	// waiting again and then write as its bound actor. The prior holder is:
	//
	//   - the actor still named on the execution being claimed — a waiting
	//     row reset by scheduler restart recovery keeps its actor_id; or
	//   - otherwise the actor of the immediately preceding attempt of the
	//     same step on the same branch, if that attempt completed — a
	//     needs_rework loop creates a new, unbound attempt of the author
	//     step — unless this attempt itself was claimed and released since.
	//
	// A step never held in this run (a first attempt — owner question 4: no
	// auto-bind at run start) stays claimable by any eligible actor, as
	// does a retry of a failed attempt (a failed holder is not re-offered
	// the work), an attempt whose prior holder no longer resolves or is not
	// active (it could never claim it back), and — with EnforceExecutionMode
	// on — an automated_only or ai_only step, whose claimers are
	// operator-provisioned pool members (reofferExempt; review round 1).
	// AssignStep (step.assign, operator) is never restricted:
	// "re-offered to the prior holder unless an operator assigns". The
	// candidate listing hides what the claim would refuse. A step its holder
	// released is claimable by others — release is the holder's own
	// hand-off, and it stays attributed through the cancelled claim row.
	ReofferToPriorHolder bool
	// TerminateStepsOnCancel (P3-16): CancelRun, FailRun, the permanent
	// merge failure and the scheduler's run-timeout hook move every live
	// execution of the run to `skipped` with classification run_terminated
	// and cancel its active assignment rows. Only safe together with
	// EnforceRunStatus: termination is a blind write that a concurrent bind
	// can overwrite, and the run-status guards are the backstop.
	TerminateStepsOnCancel bool

	// Actors resolves the actor being bound, for the eligibility and mode
	// checks. Nil uses the blocking store's GetActor, which is ClaimStep's
	// lookup. An embedder whose actors are not all rows in the Spine actor
	// table (SMP's `human-<uuid>` handles) supplies its own.
	//
	// A non-nil resolver is used even when every bool is false: it replaces
	// ClaimStep's actor lookup. A resolver-only policy is therefore NOT the
	// zero value (IsZero ignores this field).
	//
	// Contract. Separation of duties and an embedder's submit-ownership
	// check are exact string compares, so one identity must have exactly
	// one actor ID that binds:
	//
	//   - Enforced by the engine: the returned Actor's ActorID must equal
	//     the requested ID byte for byte, or the bind answers ErrNotFound.
	//     This only stops a resolver from answering for a DIFFERENT ID
	//     than the one asked about (mapping "Alice" to "alice", say).
	//   - The resolver's own duty, which the engine cannot check: answer
	//     ErrNotFound for every input that is not, byte for byte, the
	//     stored canonical handle of an actor — no case folding, trimming,
	//     brace / urn: UUID forms or other lenient parsing. A resolver
	//     that builds the Actor from the requested ID (echoing any
	//     "human-*" string, or parsing it with a lenient UUID parser)
	//     passes the echo check for every variant, so a case or whitespace
	//     variant of a run author's handle binds as a different actor and
	//     defeats separation of duties.
	//
	// A *domain.SpineError keeps its code (ErrUnavailable stays 503); an
	// untyped error becomes ErrInternal. The engine passes the requested
	// ID to the resolver unmodified.
	Actors EntryActorResolver
}

// EntryActorResolver resolves an actor ID for the step-entry checks.
type EntryActorResolver interface {
	// A *domain.SpineError with ErrNotFound means "no such actor".
	ResolveEntryActor(ctx context.Context, actorID string) (*domain.Actor, error)
}

// StrictStepEntryPolicy returns a policy with every check enabled and no
// actor resolver — the `spine serve` default.
func StrictStepEntryPolicy() StepEntryPolicy {
	return StepEntryPolicy{
		EnforceRunStatus:       true,
		EnforceEligibility:     true,
		EnforceExecutionMode:   true,
		EnforcePreconditions:   true,
		RequireSkillsOnAssign:  true,
		SeparationOfDuties:     true,
		ReofferToPriorHolder:   true,
		TerminateStepsOnCancel: true,
	}
}

// IsZero reports whether no check is enabled. Actors is ignored: a policy
// that only installs a resolver still changes ClaimStep's actor lookup (see
// StepEntryPolicy.Actors), so do not describe it as "the zero value".
func (p StepEntryPolicy) IsZero() bool {
	return !p.gatesBinding() && !p.TerminateStepsOnCancel
}

// gatesBinding reports whether any binding check is on. AssignStep loads the
// run and the pinned workflow only when it is, so a zero-value orchestrator
// (including hand-built test values with no workflow loader) runs exactly the
// pre-INIT-023 code.
func (p StepEntryPolicy) gatesBinding() bool {
	return p.EnforceRunStatus || p.EnforceEligibility || p.EnforceExecutionMode ||
		p.EnforcePreconditions || p.RequireSkillsOnAssign || p.SeparationOfDuties ||
		p.ReofferToPriorHolder
}

// WithStepEntryPolicy installs the step-entry policy. The zero value (the
// default) keeps the pre-INIT-023 behaviour on every path.
//
// TerminateStepsOnCancel without EnforceRunStatus logs a WARN: termination
// writes with a blind UpdateStepExecution, so a bind that read the row
// before termination can write it back as live; only the run-status guards
// stop such a row from being acted on afterwards.
func (o *Orchestrator) WithStepEntryPolicy(p StepEntryPolicy) {
	o.entryPolicy = p
	if p.TerminateStepsOnCancel && !p.EnforceRunStatus {
		observe.Logger(context.Background()).Warn(
			"step-entry policy: TerminateStepsOnCancel is set without EnforceRunStatus; a bind racing run termination can revive a terminated execution and nothing refuses it afterwards")
	}
}

// StepEntryPolicy returns the installed step-entry policy (read-only; used by
// wiring tests).
func (o *Orchestrator) StepEntryPolicy() StepEntryPolicy {
	return o.entryPolicy
}

// entryOp names the binding path a check runs for.
type entryOp int

const (
	entryAssign entryOp = iota
	entryClaim
)

func (op entryOp) verb() string {
	if op == entryClaim {
		return "claimed"
	}
	return "assigned"
}

// requireActiveRun is the EnforceRunStatus guard. It refuses every
// non-active status — pending, paused, committing and partially-merged as
// well as the terminal ones — because no actor step is live in those states
// (committing is entered only by the merge handler or CompleteRun, and
// paused has no production setter). action completes "a step can only be
// <action> on an active run".
func (o *Orchestrator) requireActiveRun(run *domain.Run, action string) error {
	if !o.entryPolicy.EnforceRunStatus {
		return nil
	}
	if run == nil {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run is unknown; a step can only be %s on an active run", action))
	}
	if run.Status == domain.RunStatusActive {
		return nil
	}
	return domain.NewError(domain.ErrConflict,
		fmt.Sprintf("run %s is %s; a step can only be %s on an active run", run.RunID, run.Status, action))
}

// entryActorLoader is the duck-typed actor lookup on the blocking store
// (*store.PostgresStore satisfies it).
type entryActorLoader interface {
	GetActor(ctx context.Context, actorID string) (*domain.Actor, error)
}

// entrySkillLister is the duck-typed skill lookup on the blocking store.
type entrySkillLister interface {
	ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error)
}

// entryActor resolves the actor being bound. With no resolver installed it
// is exactly loadActor — ClaimStep's lookup before INIT-023, errors and all —
// so the zero value is unchanged. With a resolver it enforces the resolver
// contract documented on StepEntryPolicy.Actors.
func (o *Orchestrator) entryActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	if o.entryPolicy.Actors == nil {
		return o.loadActor(ctx, actorID)
	}
	a, err := o.entryPolicy.Actors.ResolveEntryActor(ctx, actorID)
	return checkResolvedActor(a, err, actorID)
}

// policyActor is the actor lookup the policy checks use. Unlike loadActor it
// fails closed with typed errors: an unwired lookup is ErrUnavailable (503),
// an unknown actor ErrNotFound naming the ID, any other typed error keeps its
// code, and an untyped error is ErrInternal.
func (o *Orchestrator) policyActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	if o.entryPolicy.Actors != nil {
		return o.entryActor(ctx, actorID)
	}
	loader, ok := o.blocking.(entryActorLoader)
	if !ok {
		return nil, domain.NewError(domain.ErrUnavailable,
			"step-entry policy needs an actor lookup (blocking store with GetActor, or StepEntryPolicy.Actors)")
	}
	a, err := loader.GetActor(ctx, actorID)
	return checkResolvedActor(a, err, actorID)
}

// checkResolvedActor applies the lookup contract to a resolver or store
// answer. The exact-ID echo check stops a resolver from answering for a
// different identity than the one requested; it cannot tell a canonical
// handle from a variant the resolver chose to accept, which is why the
// resolver itself must refuse non-canonical input (StepEntryPolicy.Actors).
// The store path (no resolver) is exact: GetActor is an equality lookup on
// auth.actors.actor_id.
func checkResolvedActor(a *domain.Actor, err error, actorID string) (*domain.Actor, error) {
	if err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) {
			if se.Code == domain.ErrNotFound {
				return nil, domain.NewError(domain.ErrNotFound, fmt.Sprintf("actor %q not found", actorID))
			}
			return nil, err
		}
		return nil, domain.NewErrorWithCause(domain.ErrInternal,
			fmt.Sprintf("actor lookup for %q failed", actorID), err)
	}
	if a == nil || a.ActorID != actorID {
		return nil, domain.NewError(domain.ErrNotFound, fmt.Sprintf("actor %q not found", actorID))
	}
	return a, nil
}

// modeActorType reports whether a step is engine-owned and, otherwise, the
// actor type its execution mode implies (Design §5). The implied types match
// resolveAutoActorID's mode inference. hybrid, an unset mode and a step with
// no execution block imply no type: eligible_actor_types (when declared)
// applies instead.
func modeActorType(stepDef *domain.StepDefinition) (engineOwned bool, implied domain.ActorType) {
	if stepDef.Type == domain.StepTypeInternal {
		return true, ""
	}
	if stepDef.Execution == nil {
		return false, ""
	}
	switch stepDef.Execution.Mode {
	case domain.ExecModeSpineOnly:
		return true, ""
	case domain.ExecModeAutomatedOnly:
		return false, domain.ActorTypeAutomated
	case domain.ExecModeAIOnly:
		return false, domain.ActorTypeAIAgent
	case domain.ExecModeHumanOnly:
		return false, domain.ActorTypeHuman
	}
	return false, ""
}

// checkStepEntryActor runs the actor-side step-entry checks in their fixed
// order; the first failure wins and nothing has been written yet:
//
//  1. run status is active               (EnforceRunStatus)      409
//  2. step is not engine-owned           (EnforceExecutionMode)  409
//  3. actor type matches the mode        (EnforceExecutionMode)  409 / 404
//  4. actor exists and is active         (EnforceEligibility)    404 / 403
//  5. allow-list, eligible_actor_types   (EnforceEligibility)    403 / 409  assign only
//  6. required_skills                    (RequireSkillsOnAssign) 409 / 503  assign only
//  7. separation of duties               (SeparationOfDuties)    403 / 503
//
// Check 6b, claim only, runs between 6 and 7: the prior holder
// (ReofferToPriorHolder, INIT-023 TASK-014), 403 / 503.
//
// Check 8, preconditions, is checkStepEntryPreconditions; it runs last
// because it may read git. allow is the effective eligible_actor_ids list
// (assign: the request's list when non-empty, else the stored one). ClaimStep
// keeps its own allow-list, type and skill checks, so 5 and 6 are assign-only.
func (o *Orchestrator) checkStepEntryActor(ctx context.Context, op entryOp, run *domain.Run,
	wfDef *domain.WorkflowDefinition, stepDef *domain.StepDefinition, exec *domain.StepExecution,
	actorID string, allow []string) error {
	p := o.entryPolicy

	if err := o.requireActiveRun(run, op.verb()); err != nil {
		return err
	}

	var resolved *domain.Actor
	actorFor := func() (*domain.Actor, error) {
		if resolved != nil {
			return resolved, nil
		}
		a, err := o.policyActor(ctx, actorID)
		if err != nil {
			return nil, err
		}
		resolved = a
		return a, nil
	}

	if p.EnforceExecutionMode {
		engineOwned, implied := modeActorType(stepDef)
		if engineOwned {
			return domain.NewError(domain.ErrConflict,
				fmt.Sprintf("step %q is engine-owned (type %s, mode %s); the engine advances it and it cannot be %s",
					stepDef.ID, stepDef.Type, executionModeForLog(stepDef), op.verb()))
		}
		if implied != "" {
			a, err := actorFor()
			if err != nil {
				return err
			}
			if a.Type != implied {
				return domain.NewError(domain.ErrConflict,
					fmt.Sprintf("actor %q is %s; step %q (mode %s) can only be %s to a %s actor",
						actorID, a.Type, stepDef.ID, executionModeForLog(stepDef), op.verb(), implied))
			}
		}
	}

	if p.EnforceEligibility {
		a, err := actorFor()
		if err != nil {
			return err
		}
		if a.Status != domain.ActorStatusActive {
			return domain.NewError(domain.ErrForbidden,
				fmt.Sprintf("actor %q is %s and cannot be %s to a step", actorID, a.Status, op.verb()))
		}
		if op == entryAssign {
			if len(allow) > 0 && !containsStr(allow, actorID) {
				return domain.NewError(domain.ErrForbidden,
					fmt.Sprintf("actor %q is not in the eligible actor list for this step", actorID))
			}
			if stepDef.Execution != nil && len(stepDef.Execution.EligibleActorTypes) > 0 &&
				!containsStr(stepDef.Execution.EligibleActorTypes, string(a.Type)) {
				return domain.NewError(domain.ErrConflict,
					fmt.Sprintf("actor type %q is not eligible for this step (allowed: %v)", a.Type, stepDef.Execution.EligibleActorTypes))
			}
		}
	}

	if op == entryAssign && p.RequireSkillsOnAssign {
		if err := o.requireSkillsOnAssign(ctx, stepDef, actorID); err != nil {
			return err
		}
	}

	if op == entryClaim && p.ReofferToPriorHolder {
		if err := o.checkPriorHolder(ctx, run, stepDef, exec, actorID); err != nil {
			return err
		}
	}

	if p.SeparationOfDuties {
		if err := o.checkSeparationOfDuties(ctx, run, wfDef, stepDef, exec, actorID); err != nil {
			return err
		}
	}
	return nil
}

// requireSkillsOnAssign is check 6. It fails closed (503) when the skill
// lookup is not wired or errors: a switch that asks for enforcement must not
// silently do nothing (ClaimStep's silent skip is left alone so its zero
// value stays byte-identical).
func (o *Orchestrator) requireSkillsOnAssign(ctx context.Context, stepDef *domain.StepDefinition, actorID string) error {
	if stepDef.Execution == nil || len(stepDef.Execution.RequiredSkills) == 0 {
		return nil
	}
	lister, ok := o.blocking.(entrySkillLister)
	if !ok {
		return domain.NewError(domain.ErrUnavailable,
			"required_skills cannot be checked: no skill lookup is configured")
	}
	skills, err := lister.ListActorSkills(ctx, actorID)
	if err != nil {
		return domain.NewErrorWithCause(domain.ErrUnavailable, "required_skills cannot be checked: skill lookup failed", err)
	}
	held := make(map[string]bool, len(skills))
	for i := range skills {
		if skills[i].Status == domain.SkillStatusActive {
			held[skills[i].Name] = true
		}
	}
	var missing []string
	for _, s := range stepDef.Execution.RequiredSkills {
		if !held[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return domain.NewError(domain.ErrConflict, fmt.Sprintf("actor missing required skills: %v", missing))
	}
	return nil
}

// checkPriorHolder is check 6b (ReofferToPriorHolder, claim only). It
// reads the run's executions once; a read failure fails closed (503). The
// refusal never names the prior holder (the TASK-016 wire-message rule: a
// refusal does not tell a caller who holds a run).
func (o *Orchestrator) checkPriorHolder(ctx context.Context, run *domain.Run, stepDef *domain.StepDefinition,
	exec *domain.StepExecution, actorID string) error {
	if !isAuthorStepType(stepDef.Type) || o.entryPolicy.reofferExempt(stepDef) {
		return nil
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return domain.NewErrorWithCause(domain.ErrUnavailable, "prior-holder check: cannot list the run's executions", err)
	}
	return o.priorHolderRefusal(ctx, run, stepDef, exec, actorID, execs)
}

// priorHolderRefusal is check 6b over an already-read execution list (the
// candidate listing reads it once per run): nil, ErrForbidden, or
// ErrUnavailable when the assignment ledger cannot be read.
func (o *Orchestrator) priorHolderRefusal(ctx context.Context, run *domain.Run, stepDef *domain.StepDefinition,
	exec *domain.StepExecution, actorID string, execs []domain.StepExecution) error {
	if !isAuthorStepType(stepDef.Type) || o.entryPolicy.reofferExempt(stepDef) {
		return nil
	}
	holder, err := o.priorHolder(ctx, exec, execs)
	if err != nil {
		return err
	}
	if holder == "" || holder == actorID {
		return nil
	}
	canClaim, err := o.priorHolderCanClaim(ctx, holder)
	if err != nil {
		return err
	}
	if !canClaim {
		return nil
	}
	return domain.NewError(domain.ErrForbidden,
		fmt.Sprintf("step %q of run %s is re-offered to the actor who held it before (rework or restart recovery); "+
			"only that actor may claim it, or an operator may assign it to another actor (POST /runs/%s/steps/%s/assign)",
			stepDef.ID, run.RunID, run.RunID, stepDef.ID))
}

// reofferExempt reports whether a step's claimers are interchangeable pool
// members, to which the re-offer does not apply (review round 1): with
// EnforceExecutionMode on, an automated_only or ai_only step binds only
// automated_system or ai_agent actors, which only operators provision
// (actor.create), so no contributor can claim it to take over a
// colleague's work. Its rework is also already open to the pool on the
// path that usually binds it: ActivateStep's auto-selection picks any
// eligible actor of the mode's type, never the prior holder. Re-offering
// the claim to one pool identity would only strand the attempt when that
// runner or agent has gone (scaled down, or replaced by a new identity)
// — the step then waits, hidden from every other pool member,
// until an operator assigns it. Without EnforceExecutionMode the mode does
// not restrict who claims, so the re-offer stays.
func (p StepEntryPolicy) reofferExempt(stepDef *domain.StepDefinition) bool {
	if !p.EnforceExecutionMode {
		return false
	}
	_, implied := modeActorType(stepDef)
	return implied == domain.ActorTypeAutomated || implied == domain.ActorTypeAIAgent
}

// priorHolderCanClaim reports whether the prior holder could still claim
// the attempt it is re-offered (review round 1). An actor that no longer
// resolves, or is not active, can never claim it — EnforceEligibility
// refuses it, and a suspended or deactivated identity must not keep work
// pinned to itself — so re-offering would strand the attempt until an
// operator assigns it; it is then open to any eligible actor, as a first
// attempt is (attributed by the claim like any other). With no actor lookup
// wired the holder cannot be judged and the re-offer stands (the stricter
// answer). A lookup failure fails the claim closed (503); neither answer
// names the holder.
func (o *Orchestrator) priorHolderCanClaim(ctx context.Context, holder string) (bool, error) {
	if o.entryPolicy.Actors == nil {
		if _, ok := o.blocking.(entryActorLoader); !ok {
			return true, nil
		}
	}
	a, err := o.policyActor(ctx, holder)
	if err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) && se.Code == domain.ErrNotFound {
			return false, nil
		}
		return false, domain.NewErrorWithCause(domain.ErrUnavailable, "prior-holder check: cannot resolve the prior holder", err)
	}
	return a.Status == domain.ActorStatusActive, nil
}

// priorHolder returns the actor a claimable execution is re-offered to, or
// "" when it has none (see ReofferToPriorHolder):
//
//  1. the actor still named on the execution (restart recovery keeps it);
//  2. else the actor of the immediately preceding attempt of the same step
//     on the same branch, only if that attempt completed — the latest
//     preceding attempt is chosen first and judged afterwards (codex
//     review round 1), so an older completion cannot reinstate ownership
//     across a failed retry;
//  3. but not when this execution itself was held and released since
//     (codex review round 2): ReleaseStep blanks the row's actor, so a
//     released rework attempt looks like a fresh one. A release is the
//     holder's own hand-off and leaves the step claimable by others, as it
//     does for a first attempt (releasedSince).
func (o *Orchestrator) priorHolder(ctx context.Context, exec *domain.StepExecution, execs []domain.StepExecution) (string, error) {
	if exec.ActorID != "" {
		return exec.ActorID, nil
	}
	var prev *domain.StepExecution
	for i := range execs {
		e := &execs[i]
		if e.ExecutionID == exec.ExecutionID || e.StepID != exec.StepID || e.BranchID != exec.BranchID ||
			e.Attempt >= exec.Attempt {
			continue
		}
		if prev == nil || e.Attempt > prev.Attempt {
			prev = e
		}
	}
	if prev == nil || prev.Status != domain.StepStatusCompleted || prev.ActorID == "" {
		return "", nil
	}
	released, err := o.releasedSince(ctx, exec, prev.ActorID)
	if err != nil {
		return "", err
	}
	if released {
		return "", nil
	}
	return prev.ActorID, nil
}

// releasedSince reports whether exec — claimable, with no actor — was
// bound and then released, judged from the assignment ledger rows the
// engine writes with deterministic ids: ClaimStep's
// claim-<execution>-<actor> and ActivateStep's auto-<execution>-<actor>
// for the candidate holder (under this policy the only actor who can claim
// the attempt), and TrackAssignment's <execution> for any actor. It reads
// with GetAssignment only, so no store method is added. With no ledger
// wired, ReleaseStep is unavailable, so nothing was released. A ledger
// read other than not-found is ErrUnavailable (fail closed).
func (o *Orchestrator) releasedSince(ctx context.Context, exec *domain.StepExecution, holder string) (bool, error) {
	if o.assignments == nil {
		return false, nil
	}
	for _, id := range []string{
		"claim-" + exec.ExecutionID + "-" + holder,
		"auto-" + exec.ExecutionID + "-" + holder,
		exec.ExecutionID,
	} {
		a, err := o.assignments.GetAssignment(ctx, id)
		if err != nil {
			var se *domain.SpineError
			if errors.As(err, &se) && se.Code == domain.ErrNotFound {
				continue
			}
			return false, domain.NewErrorWithCause(domain.ErrUnavailable, "prior-holder check: cannot read the assignment ledger", err)
		}
		if a != nil && a.ExecutionID == exec.ExecutionID {
			return true, nil
		}
	}
	return false, nil
}

// assignmentHistory is the optional ledger read behind the separation-of-
// duties author check: every row one actor ever held, in any run. The engine
// type-asserts it on o.assignments so no interface an embedder implements
// grows a method. It is the fallback for runAssignmentHistory.
type assignmentHistory interface {
	ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error)
}

// runAssignmentHistory is the narrower ledger read the engine prefers: one
// actor's rows in one run, any status. *store.PostgresStore provides it (one
// indexed query, `WHERE actor_id = $1 AND run_id = $2`), so a long-lived
// runner or agent with a large assignment history is not loaded in full on
// every review bind. A store offering only assignmentHistory still works.
type runAssignmentHistory interface {
	ListAssignmentsByRunAndActor(ctx context.Context, runID, actorID string) ([]domain.Assignment, error)
}

// isAuthorStepType reports whether executing a step of this type makes an
// actor a run author: every type whose bound actor may write the run or
// divergence branch. That is every type refadmit.FrozenStepType does not
// freeze — manual, automated, convergence, and an empty or unknown type —
// because INIT-023 TASK-016's push admission lets the bound actor of any
// such step push commits to the branch (wave 2 cross-task review). Only
// review and internal executions do not make an actor an author, so one
// actor may still both verify and review in SMP's task-default (Owner
// question 1 default, as amended).
//
// Before the review a convergence step (a divergence workflow's
// evaluation_step) and a step with no declared type were not author
// steps, so their bound actor could push commits to the run branch and
// then review the run. Keying both rules on the one predicate keeps them
// from drifting apart again.
func isAuthorStepType(t domain.StepType) bool {
	return !refadmit.FrozenStepType(t)
}

// sodBindStripes serialises, within one process, the separation-of-duties
// check and the bind write of every binding path (AssignStep, ClaimStep and
// ActivateStep's auto-selection) for the same run. Without it two binds of
// one actor into one run can both pass check 7 before either writes — the
// review half reads "no author execution bound to erin" while the author
// half reads "erin holds no live review" — and erin ends up reviewing work
// she authors (divergence workflows only: in a linear workflow the review
// execution exists only after the author step was submitted).
//
// The critical section is check 7 and the bind write, nothing else
// (sodBindSection). AssignStep and ClaimStep run all their checks first
// without a stripe — check 7 included, so the first failure is still the
// one the documented order names — then take the stripe, re-run check 7
// against fresh reads of the run's executions and the assignment ledger,
// write, and release. Workflow loads (a `git show`), actor resolution (an
// embedder's resolver query), skill lookups and precondition git reads
// therefore never hold a stripe, and a slow one in one run — in a
// multi-tenant embedder, one workspace — does not delay binds of the
// unrelated runs that share its stripe. ActivateStep runs check 7 only
// under the stripe: its refusal leaves the step waiting instead of
// answering an error, so it has no error order to keep. The price is a
// second read of the run's executions and ledger rows per bind under
// SeparationOfDuties.
//
// Each stripe is a one-slot semaphore, not a sync.Mutex, so a waiter
// honours its context: a caller that gives up while another bind of a run
// on its stripe is writing gets ErrUnavailable (wrapping ctx.Err()) and
// binds nothing, rather than binding after its own deadline.
//
// Package level, not per Orchestrator: `spine serve` builds a process-level
// orchestrator and one per workspace, and the pool rebuilds them on
// eviction, so the lock must not depend on which instance serves a call.
// Striped by run ID; two runs sharing a stripe only serialise needlessly,
// and only for the length of a check-7 read and a write. A holder never
// takes a second stripe, and the stripe is released right after the bind
// write — before events are emitted or assignments are delivered — so a
// synchronous event handler that binds again cannot deadlock on it.
//
// Residual (documented in KNOWN-LIMITATIONS.md): this does not serialise
// binds made by different processes sharing one database (an embedder that
// runs several replicas with SeparationOfDuties on); closing that needs a
// database-level lock per run.
var sodBindStripes = func() (s [64]chan struct{}) {
	for i := range s {
		s[i] = make(chan struct{}, 1)
	}
	return s
}()

// sodStripe is the index of the bind stripe a run ID hashes to.
func sodStripe(runID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return int(h.Sum32() % uint32(len(sodBindStripes)))
}

// lockSoDBind takes the run's bind stripe when SeparationOfDuties is on and
// returns an idempotent release. With the policy off it takes nothing, so
// the zero value is unchanged. A free stripe is taken even when ctx is
// already done (the write that follows fails on its own); a contended one
// is waited for only until ctx ends, and then the error is ErrUnavailable
// wrapping ctx.Err(), with nothing held. unlock is never nil.
func (o *Orchestrator) lockSoDBind(ctx context.Context, runID string) (unlock func(), err error) {
	if !o.entryPolicy.SeparationOfDuties {
		return func() {}, nil
	}
	sem := sodBindStripes[sodStripe(runID)]
	select {
	case sem <- struct{}{}:
	default:
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return func() {}, domain.NewErrorWithCause(domain.ErrUnavailable,
				fmt.Sprintf("separation of duties: gave up waiting to bind into run %s", runID), ctx.Err())
		}
	}
	var once sync.Once
	return func() { once.Do(func() { <-sem }) }, nil
}

// sodBindSection opens the critical section of an AssignStep or ClaimStep
// bind under SeparationOfDuties: it takes the run's stripe and re-runs
// check 7 against fresh reads, because the caller's own check 7 ran without
// the stripe and a concurrent bind into the run may have been written
// since. On success the caller writes the bind and then calls unlock; on a
// refusal nothing is held and nothing has been written. Without
// SeparationOfDuties it takes nothing and checks nothing. run, wfDef and
// stepDef are the ones the caller's checks used (non-nil: the policy gates
// binding, so both callers loaded them).
func (o *Orchestrator) sodBindSection(ctx context.Context, run *domain.Run, wfDef *domain.WorkflowDefinition,
	stepDef *domain.StepDefinition, exec *domain.StepExecution, actorID string) (unlock func(), err error) {
	if !o.entryPolicy.SeparationOfDuties {
		return func() {}, nil
	}
	unlock, err = o.lockSoDBind(ctx, run.RunID)
	if err != nil {
		return unlock, err
	}
	if err := o.checkSeparationOfDuties(ctx, run, wfDef, stepDef, exec, actorID); err != nil {
		unlock()
		return func() {}, err
	}
	return unlock, nil
}

// checkSeparationOfDuties is check 7.
//
//   - Review step: refused when the candidate is a run author (runAuthor).
//     The rule is per run: a reviewer who sent work back may execute the
//     rework, but cannot then review that rework.
//   - Author step (isAuthorStepType: manual, automated, convergence, or an
//     empty or unknown type; the symmetric guard): refused when the
//     candidate holds a non-terminal review execution of the same run. This
//     matters only with divergence branches, where a review and an author
//     step can be live at once; in a linear workflow a reviewer who sent
//     work back holds only a terminal review, so rework stays allowed.
//
// Step types come from the workflow version the run is pinned to; an
// execution whose step is missing from it counts against the candidate
// (fail closed). A bind runs it twice: once without the stripe, in its place
// in the check order, and again inside sodBindSection (ActivateStep: once,
// under lockSoDBind), so the decision that admits a bind is made under the
// run's stripe and held until the bind is written.
func (o *Orchestrator) checkSeparationOfDuties(ctx context.Context, run *domain.Run,
	wfDef *domain.WorkflowDefinition, stepDef *domain.StepDefinition, exec *domain.StepExecution, actorID string) error {
	if stepDef.Type != domain.StepTypeReview && !isAuthorStepType(stepDef.Type) {
		return nil
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return domain.NewErrorWithCause(domain.ErrUnavailable, "separation of duties: cannot list the run's executions", err)
	}
	return o.separationOfDuties(ctx, run, wfDef, stepDef, exec, actorID, execs)
}

// separationOfDuties is checkSeparationOfDuties over an already-read list
// of the run's executions (the candidate listing reads it once per run).
func (o *Orchestrator) separationOfDuties(ctx context.Context, run *domain.Run, wfDef *domain.WorkflowDefinition,
	stepDef *domain.StepDefinition, exec *domain.StepExecution, actorID string, execs []domain.StepExecution) error {
	switch {
	case stepDef.Type == domain.StepTypeReview:
		authored, err := o.runAuthor(ctx, run, wfDef, exec, actorID, execs)
		if err != nil {
			return err
		}
		if authored != "" {
			return domain.NewError(domain.ErrForbidden,
				fmt.Sprintf("separation of duties: actor %q executed step %q of run %s and cannot review it (step %q)",
					actorID, authored, run.RunID, stepDef.ID))
		}
	case isAuthorStepType(stepDef.Type):
		for i := range execs {
			e := &execs[i]
			if e.ExecutionID == exec.ExecutionID || e.ActorID != actorID || e.Status.IsTerminal() {
				continue
			}
			if sd := findStepDef(wfDef, e.StepID); sd == nil || sd.Type == domain.StepTypeReview {
				return domain.NewError(domain.ErrForbidden,
					fmt.Sprintf("separation of duties: actor %q holds review step %q of run %s and cannot also execute step %q",
						actorID, e.StepID, run.RunID, stepDef.ID))
			}
		}
	}
	return nil
}

// runAuthor returns the step through which actorID authored the run, or ""
// when it did not. An actor is a run author when either holds:
//
//	(a) it is the ActorID of an author execution of the run
//	    (isAuthorStepType: any type but review and internal), in any
//	    status (failed, in-progress and completed attempts all count);
//	(b) it is the actor_id of an actor_assignments row, in any status, of
//	    such an execution.
//
// (b) exists because rebinding reuses the execution row: ReleaseStep blanks
// ActorID and the next claimant overwrites it (as does restart recovery plus
// a rebind), so an executor who pushed commits, released and let someone
// else finish survives only as a cancelled claim row. With no assignment
// store wired, (a) alone is complete for released bindings because
// ReleaseStep is unavailable without one. A wired store that cannot list
// the ledger, or a ledger read error, fails closed (ErrUnavailable), never
// "not an author".
//
// Documented residual (Owner question 6): AssignStep writes no assignment
// row, so an assign-bound execution reset by restart recovery and rebound to
// someone else leaves no record of the first actor. Closing it needs an
// append-only binding ledger (a schema change).
func (o *Orchestrator) runAuthor(ctx context.Context, run *domain.Run, wfDef *domain.WorkflowDefinition,
	target *domain.StepExecution, actorID string, execs []domain.StepExecution) (string, error) {
	known := make(map[string]bool, len(execs))
	authorExecs := make(map[string]string, len(execs)) // execution ID → step ID
	for i := range execs {
		e := &execs[i]
		known[e.ExecutionID] = true
		if e.ExecutionID == target.ExecutionID {
			continue
		}
		if sd := findStepDef(wfDef, e.StepID); sd != nil && !isAuthorStepType(sd.Type) {
			continue
		}
		authorExecs[e.ExecutionID] = e.StepID
		if e.ActorID == actorID {
			return e.StepID, nil
		}
	}

	if o.assignments == nil {
		return "", nil
	}
	var rows []domain.Assignment
	var err error
	switch h := o.assignments.(type) {
	case runAssignmentHistory:
		rows, err = h.ListAssignmentsByRunAndActor(ctx, run.RunID, actorID)
	case assignmentHistory:
		rows, err = h.ListAssignmentsByActor(ctx, actorID, nil)
	default:
		return "", domain.NewError(domain.ErrUnavailable,
			"separation of duties needs the assignment ledger (ListAssignmentsByRunAndActor or ListAssignmentsByActor), which the configured assignment store does not provide")
	}
	if err != nil {
		return "", domain.NewErrorWithCause(domain.ErrUnavailable, "separation of duties: cannot read the assignment ledger", err)
	}
	for i := range rows {
		a := &rows[i]
		// The run filter stays even for the narrow read: it is the whole
		// filter for the fallback, and costs nothing otherwise.
		if a.RunID != run.RunID || a.ExecutionID == target.ExecutionID {
			continue
		}
		if stepID, ok := authorExecs[a.ExecutionID]; ok {
			return stepID, nil
		}
		if !known[a.ExecutionID] {
			// A ledger row for an execution the run does not list: fail
			// closed rather than guess its step type.
			return a.ExecutionID, nil
		}
	}
	return "", nil
}

// checkStepEntryPreconditions is check 8: the engine's own evaluator (the
// one ActivateStep uses), at the run's read ref. A refusal is read-only —
// unlike preparePreconditionFailure it does not persist ErrorDetail, because
// two concurrent binders must not race on the row and the activation's
// record must not be overwritten.
func (o *Orchestrator) checkStepEntryPreconditions(ctx context.Context, run *domain.Run, stepDef *domain.StepDefinition) error {
	if !o.entryPolicy.EnforcePreconditions {
		return nil
	}
	passed, valResult := o.evaluatePreconditions(ctx, stepDef, run)
	if passed {
		return nil
	}
	msg := fmt.Sprintf("preconditions not met for step %s", stepDef.ID)
	if valResult != nil {
		return domain.NewErrorWithDetail(domain.ErrPrecondition, msg, valResult)
	}
	return domain.NewError(domain.ErrPrecondition, msg)
}

// preconditionField reads a front-matter field for field_value /
// field_present. Under EnforcePreconditions the four typed fields the parser
// keeps out of Metadata (id, type, title, status) read the typed values, so
// `field_value status` means what workflow-definition-format.md §5.2 says.
// The zero value keeps reading Metadata, where those keys are always absent,
// so a zero-value embedder's ActivateStep answers exactly as before.
func (o *Orchestrator) preconditionField(art *domain.Artifact, key string) string {
	if o.entryPolicy.EnforcePreconditions {
		switch key {
		case "status":
			return string(art.Status)
		case "id":
			return art.ID
		case "type":
			return string(art.Type)
		case "title":
			return art.Title
		}
	}
	return art.Metadata[key]
}

// activeAssignmentCanceller is the optional port terminateLiveSteps uses to
// close the active assignment rows of terminated executions.
// *store.PostgresStore provides it; it is type-asserted on o.assignments so
// neither store.Store nor engine.AssignmentStore grows a method.
type activeAssignmentCanceller interface {
	CancelActiveAssignmentsForExecution(ctx context.Context, executionID string, at time.Time) (int, error)
}

// TerminateLiveSteps closes the live executions of a run that ended outside
// the engine — the scheduler's run-timeout cancel writes the run row directly
// and calls this through scheduler.WithRunTerminated. It is a no-op unless
// TerminateStepsOnCancel is set.
func (o *Orchestrator) TerminateLiveSteps(ctx context.Context, runID, message string) {
	o.terminateLiveSteps(ctx, runID, message)
}

// terminateLiveSteps moves every waiting, assigned, in_progress and blocked
// execution of the run to `skipped` (run.terminated) with classification
// run_terminated, CompletedAt and the cause in the message, and cancels its
// active assignment rows. Terminal executions are left alone. Errors are
// logged and the loop continues: the run is already terminal, and under
// EnforceRunStatus every entry point refuses a dead run's executions anyway.
//
// It is not atomic against a concurrent bind (UpdateStepExecution is a blind
// write until TASK-008's compare-and-swap); see TerminateStepsOnCancel.
func (o *Orchestrator) terminateLiveSteps(ctx context.Context, runID, message string) {
	if !o.entryPolicy.TerminateStepsOnCancel {
		return
	}
	log := observe.Logger(ctx)
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		log.Warn("terminate live steps: list step executions failed", "run_id", runID, "error", err)
		return
	}
	canceller, _ := o.assignments.(activeAssignmentCanceller)
	now := time.Now()
	terminated := 0
	for i := range execs {
		e := &execs[i]
		if e.Status.IsTerminal() {
			continue
		}
		res, err := workflow.EvaluateStepTransition(e.Status, workflow.StepTransitionRequest{
			Trigger: workflow.StepTriggerRunTerminated,
		})
		if err != nil {
			log.Warn("terminate live steps: transition refused", "run_id", runID, "execution_id", e.ExecutionID, "error", err)
			continue
		}
		completedAt := now
		e.Status = res.ToStatus
		e.CompletedAt = &completedAt
		e.ErrorDetail = &domain.ErrorDetail{
			Classification: domain.FailureRunTerminated,
			Message:        message,
			StepID:         e.StepID,
			ActorID:        e.ActorID,
		}
		if err := o.store.UpdateStepExecution(ctx, e); err != nil {
			log.Warn("terminate live steps: update step execution failed", "run_id", runID, "execution_id", e.ExecutionID, "error", err)
			continue
		}
		terminated++
		if canceller != nil {
			if _, err := canceller.CancelActiveAssignmentsForExecution(ctx, e.ExecutionID, now); err != nil {
				log.Warn("terminate live steps: cancel assignments failed", "run_id", runID, "execution_id", e.ExecutionID, "error", err)
			}
		}
	}
	if terminated > 0 {
		log.Info("live step executions terminated with the run", "run_id", runID, "count", terminated, "cause", message)
	}
}

// isRunTerminated reports whether an execution was closed by
// terminateLiveSteps (as opposed to a workflow skip).
func isRunTerminated(exec *domain.StepExecution) bool {
	return exec.Status == domain.StepStatusSkipped && exec.ErrorDetail != nil &&
		exec.ErrorDetail.Classification == domain.FailureRunTerminated
}

// runTerminatedConflict is IngestResult's EnforceRunStatus answer to a late
// submit on an execution the engine terminated with its run: 409 naming the
// run status, never the idempotent terminal response (which would tell a
// late runner or agent its work was accepted).
func (o *Orchestrator) runTerminatedConflict(ctx context.Context, exec *domain.StepExecution) error {
	status := "no longer active"
	if run, err := o.store.GetRun(ctx, exec.RunID); err == nil && run != nil {
		status = string(run.Status)
	}
	return domain.NewError(domain.ErrConflict,
		fmt.Sprintf("run %s is %s; the step was terminated with the run", exec.RunID, status))
}
