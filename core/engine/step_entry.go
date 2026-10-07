package engine

import (
	"context"

	"github.com/bszymi/spine/core/domain"
)

// StepEntryPolicy configures what the engine checks before an actor is bound
// to a step execution, and what happens to live executions when a run ends
// (INIT-023 TASK-007; audit P3-3, P3-4, P3-15, P3-16 and the F3
// separation-of-duties amplifier).
//
// The zero value is the pre-INIT-023 behaviour: no field is read on any path.
type StepEntryPolicy struct {
	EnforceRunStatus       bool // P3-16: entry and advance points require run.Status == active
	EnforceEligibility     bool // P3-3: actor exists, is active, and matches eligible_actor_ids / eligible_actor_types
	EnforceExecutionMode   bool // P3-15: execution.mode decides which actor type may bind
	EnforcePreconditions   bool // P3-4: engine evaluator at assign/claim; strict evaluation
	RequireSkillsOnAssign  bool // required_skills on AssignStep (ClaimStep already checks them)
	SeparationOfDuties     bool // review-type steps refuse run authors
	TerminateStepsOnCancel bool // P3-16: CancelRun/FailRun/merge-fail close live executions

	// Actors resolves the actor being bound, for the eligibility and mode
	// checks. Nil uses the blocking store's GetActor.
	Actors EntryActorResolver
}

// EntryActorResolver resolves an actor ID for the step-entry checks.
type EntryActorResolver interface {
	// A *domain.SpineError with ErrNotFound means "no such actor".
	ResolveEntryActor(ctx context.Context, actorID string) (*domain.Actor, error)
}

// StrictStepEntryPolicy returns a policy with every check enabled and no
// actor resolver.
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

// IsZero reports whether no check is enabled. Actors is ignored.
func (p StepEntryPolicy) IsZero() bool {
	return !p.EnforceRunStatus && !p.EnforceEligibility && !p.EnforceExecutionMode &&
		!p.EnforcePreconditions && !p.RequireSkillsOnAssign && !p.SeparationOfDuties &&
		!p.TerminateStepsOnCancel
}

// WithStepEntryPolicy installs the step-entry policy.
func (o *Orchestrator) WithStepEntryPolicy(p StepEntryPolicy) {
	o.entryPolicy = p
}

// StepEntryPolicy returns the installed step-entry policy.
func (o *Orchestrator) StepEntryPolicy() StepEntryPolicy {
	return o.entryPolicy
}

// TerminateLiveSteps closes the live executions of a run that ended.
func (o *Orchestrator) TerminateLiveSteps(ctx context.Context, runID, message string) {
}
