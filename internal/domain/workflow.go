package domain

// WorkflowStatus represents the lifecycle status of a workflow definition.
type WorkflowStatus string

const (
	WorkflowStatusActive     WorkflowStatus = "Active"
	WorkflowStatusDeprecated WorkflowStatus = "Deprecated"
	WorkflowStatusDraft      WorkflowStatus = "Draft"
)

// StepType represents the execution type of a workflow step.
// Per workflow-definition-format.md §3.2.
type StepType string

const (
	StepTypeManual      StepType = "manual"
	StepTypeAutomated   StepType = "automated"
	StepTypeReview      StepType = "review"
	StepTypeConvergence StepType = "convergence"
)

// ExecutionMode represents how a step is executed.
// Per workflow-definition-format.md §3.2 execution.mode.
type ExecutionMode string

const (
	ExecModeAutomatedOnly ExecutionMode = "automated_only"
	ExecModeAIOnly        ExecutionMode = "ai_only"
	ExecModeHumanOnly     ExecutionMode = "human_only"
	ExecModeHybrid        ExecutionMode = "hybrid"
)

// DivergenceMode represents the type of divergence.
type DivergenceMode string

const (
	DivergenceModeStructured  DivergenceMode = "structured"
	DivergenceModeExploratory DivergenceMode = "exploratory"
)

// ConvergenceStrategy represents how branches are evaluated and combined.
type ConvergenceStrategy string

const (
	ConvergenceSelectOne    ConvergenceStrategy = "select_one"
	ConvergenceSelectSubset ConvergenceStrategy = "select_subset"
	ConvergenceMerge        ConvergenceStrategy = "merge"
	ConvergenceRequireAll   ConvergenceStrategy = "require_all"
	ConvergenceExperiment   ConvergenceStrategy = "experiment"
)

// EntryPolicy represents when convergence may begin.
type EntryPolicy string

const (
	EntryPolicyAllTerminal     EntryPolicy = "all_branches_terminal"
	EntryPolicyMinCompleted    EntryPolicy = "minimum_completed_branches"
	EntryPolicyDeadlineReached EntryPolicy = "deadline_reached"
	EntryPolicyManualTrigger   EntryPolicy = "manual_trigger"
)

// WorkflowDefinition represents a parsed workflow YAML file.
// Per workflow-definition-format.md §3.1.
type WorkflowDefinition struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name"`
	Version           string                  `json:"version"`
	Status            WorkflowStatus          `json:"status"`
	Description       string                  `json:"description"`
	AppliesTo         []string                `json:"applies_to"`
	EntryStep         string                  `json:"entry_step"`
	Steps             []StepDefinition        `json:"steps"`
	DivergencePoints  []DivergenceDefinition  `json:"divergence_points,omitempty"`
	ConvergencePoints []ConvergenceDefinition `json:"convergence_points,omitempty"`
	Path              string                  `json:"path"`       // repository-relative path to workflow YAML
	CommitSHA         string                  `json:"commit_sha"` // pinned Git version
}

// StepDefinition represents a single step within a workflow.
// Per workflow-definition-format.md §3.2.
type StepDefinition struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Type            StepType            `json:"type"`
	Execution       *ExecutionConfig    `json:"execution,omitempty"`
	Preconditions   []Precondition      `json:"preconditions,omitempty"`
	RequiredInputs  []string            `json:"required_inputs,omitempty"`
	RequiredOutputs []string            `json:"required_outputs,omitempty"`
	Validation      []ValidationRule    `json:"validation,omitempty"`
	Outcomes        []OutcomeDefinition `json:"outcomes"`
	Retry           *RetryConfig        `json:"retry,omitempty"`
	Timeout         string              `json:"timeout,omitempty"`
	TimeoutOutcome  string              `json:"timeout_outcome,omitempty"`
	Diverge         string              `json:"diverge,omitempty"`  // reference to divergence point ID
	Converge        string              `json:"converge,omitempty"` // reference to convergence point ID
}

// ExecutionConfig represents execution configuration for a step.
// Per workflow-definition-format.md §3.2 execution block.
type ExecutionConfig struct {
	Mode                 ExecutionMode `json:"mode"`
	EligibleActorTypes   []string      `json:"eligible_actor_types,omitempty"`
	RequiredCapabilities []string      `json:"required_capabilities,omitempty"`
}

// ValidationRule represents a validation check applied before accepting a step result.
type ValidationRule struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config,omitempty"`
}

// OutcomeDefinition represents a possible outcome of a step.
type OutcomeDefinition struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	NextStep string            `json:"next_step,omitempty"` // step ID or "end" for terminal
	Commit   map[string]string `json:"commit,omitempty"`    // fields to commit on this outcome
}

// RetryConfig represents retry behavior for a step.
type RetryConfig struct {
	Limit   int    `json:"limit"`
	Backoff string `json:"backoff"` // "fixed", "linear", "exponential"
}

// Precondition represents a condition that must be met before a step executes.
type Precondition struct {
	Type   string            `json:"type"` // "cross_artifact_valid", "custom"
	Config map[string]string `json:"config,omitempty"`
}

// DivergenceDefinition represents a divergence point in a workflow.
// Per workflow-definition-format.md §3.3.
type DivergenceDefinition struct {
	ID          string             `json:"id"`
	Mode        DivergenceMode     `json:"mode"`
	Branches    []BranchDefinition `json:"branches,omitempty"`     // for structured mode
	MaxBranches int                `json:"max_branches,omitempty"` // for exploratory mode
}

// BranchDefinition represents a predefined branch in structured divergence.
type BranchDefinition struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	EntryStep string `json:"entry_step"`
}

// ConvergenceDefinition represents convergence configuration.
// Per workflow-definition-format.md §3.3.
type ConvergenceDefinition struct {
	ID          string              `json:"id"`
	Strategy    ConvergenceStrategy `json:"strategy"`
	EntryPolicy EntryPolicy         `json:"entry_policy"`
	EvalStep    string              `json:"eval_step"`
	MinBranches int                 `json:"min_branches,omitempty"` // for minimum_completed_branches
	Deadline    string              `json:"deadline,omitempty"`     // for deadline_reached
}
