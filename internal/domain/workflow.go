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
	ID                string                  `json:"id" yaml:"id"`
	Name              string                  `json:"name" yaml:"name"`
	Version           string                  `json:"version" yaml:"version"`
	Status            WorkflowStatus          `json:"status" yaml:"status"`
	Description       string                  `json:"description" yaml:"description"`
	AppliesTo         []string                `json:"applies_to" yaml:"applies_to"`
	EntryStep         string                  `json:"entry_step" yaml:"entry_step"`
	Steps             []StepDefinition        `json:"steps" yaml:"steps"`
	DivergencePoints  []DivergenceDefinition  `json:"divergence_points,omitempty" yaml:"divergence_points,omitempty"`
	ConvergencePoints []ConvergenceDefinition `json:"convergence_points,omitempty" yaml:"convergence_points,omitempty"`
	Path              string                  `json:"path" yaml:"-"`       // set by parser, not from YAML
	CommitSHA         string                  `json:"commit_sha" yaml:"-"` // set at binding time
}

// StepDefinition represents a single step within a workflow.
// Per workflow-definition-format.md §3.2.
type StepDefinition struct {
	ID              string              `json:"id" yaml:"id"`
	Name            string              `json:"name" yaml:"name"`
	Type            StepType            `json:"type" yaml:"type"`
	Execution       *ExecutionConfig    `json:"execution,omitempty" yaml:"execution,omitempty"`
	Preconditions   []Precondition      `json:"preconditions,omitempty" yaml:"preconditions,omitempty"`
	RequiredInputs  []string            `json:"required_inputs,omitempty" yaml:"required_inputs,omitempty"`
	RequiredOutputs []string            `json:"required_outputs,omitempty" yaml:"required_outputs,omitempty"`
	Validation      []ValidationRule    `json:"validation,omitempty" yaml:"validation,omitempty"`
	Outcomes        []OutcomeDefinition `json:"outcomes" yaml:"outcomes"`
	Retry           *RetryConfig        `json:"retry,omitempty" yaml:"retry,omitempty"`
	Timeout         string              `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	TimeoutOutcome  string              `json:"timeout_outcome,omitempty" yaml:"timeout_outcome,omitempty"`
	Diverge         string              `json:"diverge,omitempty" yaml:"diverge,omitempty"`
	Converge        string              `json:"converge,omitempty" yaml:"converge,omitempty"`
}

// ExecutionConfig represents execution configuration for a step.
// Per workflow-definition-format.md §3.2 execution block.
type ExecutionConfig struct {
	Mode                 ExecutionMode `json:"mode" yaml:"mode"`
	EligibleActorTypes   []string      `json:"eligible_actor_types,omitempty" yaml:"eligible_actor_types,omitempty"`
	RequiredCapabilities []string      `json:"required_capabilities,omitempty" yaml:"required_capabilities,omitempty"`
}

// ValidationRule represents a validation check applied before accepting a step result.
type ValidationRule struct {
	Type   string            `json:"type" yaml:"type"`
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
}

// OutcomeDefinition represents a possible outcome of a step.
type OutcomeDefinition struct {
	ID       string            `json:"id" yaml:"id"`
	Name     string            `json:"name" yaml:"name"`
	NextStep string            `json:"next_step,omitempty" yaml:"next_step,omitempty"`
	Commit   map[string]string `json:"commit,omitempty" yaml:"commit,omitempty"`
}

// RetryConfig represents retry behavior for a step.
type RetryConfig struct {
	Limit   int    `json:"limit" yaml:"limit"`
	Backoff string `json:"backoff" yaml:"backoff"`
}

// Precondition represents a condition that must be met before a step executes.
type Precondition struct {
	Type   string            `json:"type" yaml:"type"`
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
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
