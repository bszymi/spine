package domain

// StepOutcome defines well-known step-level outcome identifiers.
// These are distinct from task-level acceptance outcomes (TaskAcceptance).
//
// Step outcomes are runtime workflow control decisions made by actors
// during step execution. Task acceptance outcomes are durable governance
// decisions recorded in the artifact YAML.
type StepOutcome string

const (
	// StepOutcomeAccepted routes to the next step in the workflow.
	StepOutcomeAccepted StepOutcome = "accepted_to_continue"

	// StepOutcomeNeedsRework routes back to a previous step for revision.
	StepOutcomeNeedsRework StepOutcome = "needs_rework"

	// StepOutcomeFailed triggers step failure handling (retry or permanent).
	StepOutcomeFailed StepOutcome = "failed"
)

// MaxReworkCycles is the default maximum number of times a step can be
// revisited via rework routing before the engine treats it as a permanent
// failure. This prevents infinite rework loops.
const MaxReworkCycles = 10
