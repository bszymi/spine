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
	// SeparationOfDuties: a run author (any actor bound, now or ever, to a
	// manual or automated execution of the run) cannot be bound to a review
	// step of that run, and an actor holding a live review execution cannot
	// be bound to a manual or automated step of the same run.
	SeparationOfDuties bool
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
		p.EnforcePreconditions || p.RequireSkillsOnAssign || p.SeparationOfDuties
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
// actor a run author. review, convergence and internal executions do not,
// so one actor may both verify and review (Owner question 1 default).
func isAuthorStepType(t domain.StepType) bool {
	return t == domain.StepTypeManual || t == domain.StepTypeAutomated
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
// Package level, not per Orchestrator: `spine serve` builds a process-level
// orchestrator and one per workspace, and the pool rebuilds them on
// eviction, so the lock must not depend on which instance serves a call.
// Striped by run ID; two runs sharing a stripe only serialise needlessly.
// A holder never takes a second stripe, and the stripe is released right
// after the bind write — before events are emitted or assignments are
// delivered — so a synchronous event handler that binds again cannot
// deadlock on it.
//
// Residual (documented in KNOWN-LIMITATIONS.md): this does not serialise
// binds made by different processes sharing one database (an embedder that
// runs several replicas with SeparationOfDuties on); closing that needs a
// database-level lock per run.
var sodBindStripes [64]sync.Mutex

// lockSoDBind takes the run's bind stripe when SeparationOfDuties is on and
// returns an idempotent release. With the policy off it takes nothing, so
// the zero value is unchanged.
func (o *Orchestrator) lockSoDBind(runID string) (unlock func()) {
	if !o.entryPolicy.SeparationOfDuties {
		return func() {}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	m := &sodBindStripes[h.Sum32()%uint32(len(sodBindStripes))]
	m.Lock()
	var once sync.Once
	return func() { once.Do(m.Unlock) }
}

// checkSeparationOfDuties is check 7.
//
//   - Review step: refused when the candidate is a run author (runAuthor).
//     The rule is per run: a reviewer who sent work back may execute the
//     rework, but cannot then review that rework.
//   - Manual or automated step (the symmetric guard): refused when the
//     candidate holds a non-terminal review execution of the same run. This
//     matters only with divergence branches, where a review and an author
//     step can be live at once; in a linear workflow a reviewer who sent
//     work back holds only a terminal review, so rework stays allowed.
//
// Step types come from the workflow version the run is pinned to; an
// execution whose step is missing from it counts against the candidate
// (fail closed). Callers that bind hold lockSoDBind from before this check
// until their bind write.
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
//	(a) it is the ActorID of a manual or automated execution of the run, in
//	    any status (failed, in-progress and completed attempts all count);
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
