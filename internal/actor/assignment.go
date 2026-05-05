package actor

import "github.com/bszymi/spine/internal/domain"

// AssignmentRequest contains everything an actor needs to execute a step.
// Per Actor Model §5.2.
type AssignmentRequest struct {
	AssignmentID string                `json:"assignment_id"`
	RunID        string                `json:"run_id"`
	TraceID      string                `json:"trace_id"`
	StepID       string                `json:"step_id"`
	StepName     string                `json:"step_name"`
	StepType     domain.StepType       `json:"step_type"`
	ActorID      string                `json:"actor_id"`
	Context      AssignmentContext     `json:"context"`
	Constraints  AssignmentConstraints `json:"constraints"`
}

// AssignmentContext provides the information an actor needs to execute.
type AssignmentContext struct {
	TaskPath        string   `json:"task_path"`
	WorkflowID      string   `json:"workflow_id"`
	RequiredInputs  []string `json:"required_inputs,omitempty"`
	RequiredOutputs []string `json:"required_outputs,omitempty"`
	Instructions    string   `json:"instructions,omitempty"`

	// WorkspaceID is the runtime workspace ID the runner targets when
	// resolving CloneURL through the workspace's git HTTP router.
	// Sourced from the orchestrator's wired workspace identity (the
	// same value baked into CloneURL); included so a runner that has
	// to construct an alternate URL — for example to authenticate
	// against the same workspace through a different host alias —
	// has the canonical ID without re-parsing CloneURL. Omitted from
	// payloads when the orchestrator has no workspace ID wired
	// (single-process embedded mode and tests).
	WorkspaceID string `json:"workspace_id,omitempty"`

	// RepositoryID is the resolved target repository for this step
	// (per ADR-015). The runner clones exactly this repository at
	// BranchName from CloneURL — no list, no fan-out.
	RepositoryID string `json:"repository_id"`

	// CloneURL is the URL the runner clones. Always the workspace's
	// git HTTP endpoint (`<git-http-base>/git/{workspace_id}/{repository_id}`)
	// per ADR-015 *Assignment payload shape*: the run branch is
	// guaranteed to exist on the workspace's served copy, while
	// external upstream URLs depend on best-effort auto-push.
	CloneURL string `json:"clone_url"`

	// BranchName is the run branch name. Same in every affected repo
	// per Multi-Repository Integration §4.2.
	BranchName string `json:"branch_name"`

	// CommitBaseline is the SHA the run branch was cut from in the
	// resolved repository (i.e. the parent default-branch tip at
	// run-cut time, captured by createRunBranches). Optional: set
	// when the engine captured a baseline for the resolved repo at
	// run start, omitted otherwise — primarily so legacy runs
	// created before the field was added still produce valid
	// payloads. Runners use it to detect upstream drift (compare
	// against `git rev-parse HEAD~N` after their commits) and to
	// skip a clone when their cache already holds the baseline.
	CommitBaseline string `json:"commit_baseline,omitempty"`
}

// AssignmentConstraints define execution boundaries.
type AssignmentConstraints struct {
	Timeout          string   `json:"timeout,omitempty"`
	ExpectedOutcomes []string `json:"expected_outcomes"`
	ExcludeActors    []string `json:"exclude_actors,omitempty"` // actors to skip on reassignment
}

// AssignmentResult is the actor's response to a step assignment.
// Per Actor Model §5.3.
type AssignmentResult struct {
	AssignmentID      string   `json:"assignment_id"`
	RunID             string   `json:"run_id"`
	TraceID           string   `json:"trace_id"`
	ActorID           string   `json:"actor_id"`
	OutcomeID         string   `json:"outcome_id"`
	ArtifactsProduced []string `json:"artifacts_produced,omitempty"`
	Summary           string   `json:"summary,omitempty"`
}
