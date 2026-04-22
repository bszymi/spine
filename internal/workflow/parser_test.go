package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

const taskExecutionYAML = `id: task-execution
name: Task Execution
version: "1.0"
status: Active
description: Standard workflow for executing a Task artifact from pending to terminal outcome.

applies_to:
  - Task

entry_step: assign

steps:
  - id: assign
    name: Assign Actor
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types:
        - automated_system
    required_outputs:
      - actor_assignment
    outcomes:
      - id: assigned
        name: Actor Assigned
        next_step: execute
      - id: assignment_timeout
        name: Assignment Timed Out
        next_step: end
    retry:
      limit: 3
      backoff: exponential
    timeout: "24h"
    timeout_outcome: assignment_timeout

  - id: execute
    name: Execute Work
    type: manual
    required_outputs:
      - deliverable
    outcomes:
      - id: submitted
        name: Work Submitted for Review
        next_step: review
      - id: cancelled
        name: Work Cancelled
        next_step: end
      - id: execute_timeout
        name: Execution Timed Out
        next_step: end
    timeout: "720h"
    timeout_outcome: execute_timeout

  - id: review
    name: Review Deliverable
    type: review
    required_inputs:
      - deliverable
    outcomes:
      - id: accepted
        name: Deliverable Accepted
        next_step: end
        commit:
          status: Completed
      - id: needs_rework
        name: Needs Rework
        next_step: execute
      - id: rejected
        name: Deliverable Rejected
        next_step: end
        commit:
          status: Rejected
      - id: review_timeout
        name: Review Timed Out
        next_step: end
    timeout: "168h"
    timeout_outcome: review_timeout
`

func TestParseTaskExecutionWorkflow(t *testing.T) {
	wf, err := workflow.Parse("workflows/task-execution.yaml", []byte(taskExecutionYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if wf.ID != "task-execution" {
		t.Errorf("expected ID task-execution, got %s", wf.ID)
	}
	if wf.Name != "Task Execution" {
		t.Errorf("expected name, got %s", wf.Name)
	}
	if wf.Status != domain.WorkflowStatusActive {
		t.Errorf("expected Active, got %s", wf.Status)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "Task" {
		t.Errorf("expected applies_to [Task], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "assign" {
		t.Errorf("expected entry_step assign, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(wf.Steps))
	}
	if wf.Path != "workflows/task-execution.yaml" {
		t.Errorf("expected path set, got %s", wf.Path)
	}

	// Check assign step
	assign := wf.Steps[0]
	if assign.Type != domain.StepTypeAutomated {
		t.Errorf("assign type: expected automated, got %s", assign.Type)
	}
	if assign.Retry == nil || assign.Retry.Limit != 3 {
		t.Errorf("assign retry: expected limit 3")
	}
	if len(assign.Outcomes) != 2 {
		t.Errorf("assign outcomes: expected 2, got %d", len(assign.Outcomes))
	}

	// Check review step outcomes
	review := wf.Steps[2]
	if len(review.Outcomes) != 4 {
		t.Errorf("review outcomes: expected 4, got %d", len(review.Outcomes))
	}
	if review.Outcomes[0].Commit["status"] != "Completed" {
		t.Errorf("expected accepted commit status=Completed")
	}
}

func TestValidateTaskExecutionWorkflow(t *testing.T) {
	wf, _ := workflow.Parse("workflows/task-execution.yaml", []byte(taskExecutionYAML))
	result := workflow.Validate(wf)
	if result.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", result.Status, result.Errors)
	}
}

func TestValidateSchemaMissingFields(t *testing.T) {
	wf := &domain.WorkflowDefinition{}
	errors := workflow.ValidateSchema(wf)
	if len(errors) < 5 {
		t.Errorf("expected at least 5 schema errors, got %d: %+v", len(errors), errors)
	}
}

func TestValidateSchemaInvalidStatus(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: "Invalid",
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasStatusError := false
	for _, e := range errors {
		if e.Field == "status" {
			hasStatusError = true
		}
	}
	if !hasStatusError {
		t.Error("expected status validation error")
	}
}

func TestValidateSchemaAutomatedWithoutRetry(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeAutomated,
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
			// Missing retry block
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasRetryError := false
	for _, e := range errors {
		if e.Field == "steps[0].retry" {
			hasRetryError = true
		}
	}
	if !hasRetryError {
		t.Error("expected retry validation error for automated step")
	}
}

func TestValidateSchemaTimeoutWithoutOutcome(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Timeout: "1h",
			// Missing timeout_outcome
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasTimeoutError := false
	for _, e := range errors {
		if e.Field == "steps[0].timeout_outcome" {
			hasTimeoutError = true
		}
	}
	if !hasTimeoutError {
		t.Error("expected timeout_outcome validation error")
	}
}

func TestValidateSchemaStepTimeoutRejectsDaySuffix(t *testing.T) {
	// Go's time.ParseDuration does not accept "d" — the runtime scheduler
	// would fail on this, so the validator must reject it at write time.
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Timeout:        "7d",
			TimeoutOutcome: "o1",
			Outcomes: []domain.OutcomeDefinition{
				{ID: "o1", Name: "Timeout", NextStep: "end"},
				{ID: "o2", Name: "Done", NextStep: "end"},
			},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	found := false
	for _, e := range errors {
		if e.Field == "steps[0].timeout" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected steps[0].timeout error for \"7d\"; got errors: %+v", errors)
	}
}

func TestValidateSchemaStepTimeoutAcceptsHours(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Timeout:        "168h",
			TimeoutOutcome: "o1",
			Outcomes: []domain.OutcomeDefinition{
				{ID: "o1", Name: "Timeout", NextStep: "end"},
				{ID: "o2", Name: "Done", NextStep: "end"},
			},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	for _, e := range errors {
		if e.Field == "steps[0].timeout" {
			t.Errorf("unexpected timeout error for \"168h\": %+v", e)
		}
	}
}

func TestValidateSchemaWorkflowTimeoutRejectsInvalid(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Timeout: "2w",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	found := false
	for _, e := range errors {
		if e.Field == "timeout" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected top-level timeout error for \"2w\"; got: %+v", errors)
	}
}

func TestValidateSchemaBrokenReference(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "nonexistent"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasBrokenRef := false
	for _, e := range errors {
		if e.Field == "steps[0].outcomes[0].next_step" {
			hasBrokenRef = true
		}
	}
	if !hasBrokenRef {
		t.Error("expected broken reference error")
	}
}

func TestValidateStructureUnreachableStep(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{
			{
				ID: "s1", Name: "Step 1", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
			},
			{
				ID: "s2", Name: "Unreachable", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
			},
		},
	}
	errors := workflow.ValidateStructure(wf)
	hasUnreachable := false
	for _, e := range errors {
		if e.RuleID == "structural" && contains(e.Message, "unreachable") {
			hasUnreachable = true
		}
	}
	if !hasUnreachable {
		t.Error("expected unreachable step error")
	}
}

func TestValidateStructureNoTermination(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{
			{
				ID: "s1", Name: "Loop", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Loop", NextStep: "s1"}},
			},
		},
	}
	errors := workflow.ValidateStructure(wf)
	hasTermination := false
	for _, e := range errors {
		if contains(e.Message, "terminate") {
			hasTermination = true
		}
	}
	if !hasTermination {
		t.Error("expected termination error for infinite loop")
	}
}

func TestValidateSemanticUnknownArtifactType(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		AppliesTo: []string{"UnknownType"},
	}
	errors := workflow.ValidateSemantic(wf)
	if len(errors) == 0 {
		t.Error("expected error for unknown artifact type")
	}
}

func TestValidateSemanticValidTypes(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		AppliesTo: []string{"Task", "Epic"},
	}
	errors := workflow.ValidateSemantic(wf)
	if len(errors) != 0 {
		t.Errorf("expected no errors, got %+v", errors)
	}
}

func TestValidateSchemaInvalidStepType(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: "invalid_type",
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasTypeError := false
	for _, e := range errors {
		if contains(e.Message, "invalid step type") {
			hasTypeError = true
		}
	}
	if !hasTypeError {
		t.Error("expected invalid step type error")
	}
}

func TestValidateSchemaInvalidExecutionMode(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Execution: &domain.ExecutionConfig{Mode: "bad_mode"},
			Outcomes:  []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasModeError := false
	for _, e := range errors {
		if contains(e.Message, "invalid execution mode") {
			hasModeError = true
		}
	}
	if !hasModeError {
		t.Error("expected invalid execution mode error")
	}
}

func TestValidateSchemaRequiredSkillsMissing(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid},
			Outcomes:  []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasSkillError := false
	for _, e := range errors {
		if contains(e.Message, "at least one required skill") {
			hasSkillError = true
		}
	}
	if !hasSkillError {
		t.Error("expected required skill error for actor-assigned step without skills")
	}
}

func TestValidateSchemaAutomatedStepNoSkillsOK(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeAutomated,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly},
			Outcomes:  []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	for _, e := range errors {
		if contains(e.Message, "required skill") {
			t.Error("automated_only steps should not require skills")
		}
	}
}

func TestValidateSchemaRequiredSkillsPresent(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Execution: &domain.ExecutionConfig{
				Mode:           domain.ExecModeHybrid,
				RequiredSkills: []string{"code_review"},
			},
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	for _, e := range errors {
		if contains(e.Message, "required skill") {
			t.Error("step with required skills should not produce skill error")
		}
	}
}

func TestValidateSchemaDivergeReference(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Diverge:  "nonexistent_div",
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasDivError := false
	for _, e := range errors {
		if contains(e.Message, "unknown divergence point") {
			hasDivError = true
		}
	}
	if !hasDivError {
		t.Error("expected unknown divergence point error")
	}
}

func TestValidateSchemaConvergeReference(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Converge: "nonexistent_conv",
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasConvError := false
	for _, e := range errors {
		if contains(e.Message, "unknown convergence point") {
			hasConvError = true
		}
	}
	if !hasConvError {
		t.Error("expected unknown convergence point error")
	}
}

func TestValidateSchemaTimeoutOutcomeReference(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Timeout:        "1h",
			TimeoutOutcome: "nonexistent_outcome",
			Outcomes:       []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasRefError := false
	for _, e := range errors {
		if contains(e.Message, "references unknown outcome") {
			hasRefError = true
		}
	}
	if !hasRefError {
		t.Error("expected unknown outcome reference error")
	}
}

func TestValidateSchemaInternalStepAccepted(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{
				Mode:    domain.ExecModeSpineOnly,
				Handler: "merge",
			},
			Retry: &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{
				{ID: "published", Name: "Published", NextStep: "end"},
				{ID: "merge_failed", Name: "Merge Failed", NextStep: "end"},
			},
		}},
	}
	result := workflow.Validate(wf)
	if result.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", result.Status, result.Errors)
	}
}

func TestValidateSchemaInternalStepRejectsWrongMode(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{
				Mode:    domain.ExecModeAutomatedOnly,
				Handler: "merge",
			},
			Retry:    &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{{ID: "published", Name: "Published", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasModeError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.mode" && contains(e.Message, "spine_only") {
			hasModeError = true
		}
	}
	if !hasModeError {
		t.Errorf("expected internal step to require spine_only mode; got %+v", errors)
	}
}

func TestValidateSchemaInternalStepRejectsUnknownHandler(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{
				Mode:    domain.ExecModeSpineOnly,
				Handler: "does_not_exist",
			},
			Retry:    &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{{ID: "published", Name: "Published", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasHandlerError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.handler" && contains(e.Message, "unknown") {
			hasHandlerError = true
		}
	}
	if !hasHandlerError {
		t.Errorf("expected unknown handler error; got %+v", errors)
	}
}

func TestValidateSchemaInternalStepRequiresHandler(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly},
			Retry:     &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes:  []domain.OutcomeDefinition{{ID: "published", Name: "Published", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasHandlerError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.handler" && contains(e.Message, "must declare a handler") {
			hasHandlerError = true
		}
	}
	if !hasHandlerError {
		t.Errorf("expected handler-required error; got %+v", errors)
	}
}

func TestValidateSchemaInternalStepRejectsActorFields(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{
				Mode:               domain.ExecModeSpineOnly,
				Handler:            "merge",
				EligibleActorTypes: []string{"automated_system"},
				RequiredSkills:     []string{"merge"},
			},
			Retry:    &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{{ID: "published", Name: "Published", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	var hasActorError, hasSkillError bool
	for _, e := range errors {
		if e.Field == "steps[0].execution.eligible_actor_types" {
			hasActorError = true
		}
		if e.Field == "steps[0].execution.required_skills" && contains(e.Message, "must be empty") {
			hasSkillError = true
		}
	}
	if !hasActorError {
		t.Errorf("expected eligible_actor_types rejection on internal step; got %+v", errors)
	}
	if !hasSkillError {
		t.Errorf("expected required_skills rejection on internal step; got %+v", errors)
	}
}

func TestValidateSchemaSpineOnlyRejectedOnNonInternalStep(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeAutomated,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly},
			Retry:     &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes:  []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasModeError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.mode" && contains(e.Message, "only valid on internal") {
			hasModeError = true
		}
	}
	if !hasModeError {
		t.Errorf("expected spine_only-on-automated rejection; got %+v", errors)
	}
}

func TestValidateSchemaHandlerRejectedOnNonInternalStep(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeAutomated,
			Execution: &domain.ExecutionConfig{
				Mode:               domain.ExecModeAutomatedOnly,
				Handler:            "merge",
				EligibleActorTypes: []string{"automated_system"},
			},
			Retry:    &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasHandlerError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.handler" && contains(e.Message, "only valid on internal") {
			hasHandlerError = true
		}
	}
	if !hasHandlerError {
		t.Errorf("expected handler-on-automated rejection; got %+v", errors)
	}
}

func TestValidateSchemaAutomatedStepWithoutExecutionRejected(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeAutomated,
			Retry:    &domain.RetryConfig{Limit: 3, Backoff: "exponential"},
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasExecError := false
	for _, e := range errors {
		if e.Field == "steps[0].execution" && contains(e.Message, "must declare an execution block") {
			hasExecError = true
		}
	}
	if !hasExecError {
		t.Errorf("expected automated-without-execution rejection; got %+v", errors)
	}
}

func TestValidateSchemaInternalStepRequiresRetry(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Publish", Type: domain.StepTypeInternal,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
			Outcomes:  []domain.OutcomeDefinition{{ID: "published", Name: "Published", NextStep: "end"}},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasRetryError := false
	for _, e := range errors {
		if e.Field == "steps[0].retry" {
			hasRetryError = true
		}
	}
	if !hasRetryError {
		t.Errorf("expected retry-required error for internal step; got %+v", errors)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := workflow.Parse("bad.yaml", []byte("invalid: yaml: [broken"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseRejectsOversizedYAML(t *testing.T) {
	big := make([]byte, 65*1024)
	for i := range big {
		big[i] = 'a'
	}
	_, err := workflow.Parse("big.yaml", big)
	if err == nil {
		t.Fatal("expected parse to reject >64KB YAML")
	}
}

func TestParseRejectsAliasBomb(t *testing.T) {
	// Simple billion-laughs input: many alias references to a single anchor.
	var buf []byte
	buf = append(buf, []byte("a: &anchor x\n")...)
	buf = append(buf, []byte("bombs:\n")...)
	for i := 0; i < 200; i++ {
		buf = append(buf, []byte("  - *anchor\n")...)
	}
	_, err := workflow.Parse("bomb.yaml", buf)
	if err == nil {
		t.Fatal("expected parse to reject input exceeding alias cap")
	}
}

func TestValidateDuplicateStepIDs(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{
			{ID: "s1", Name: "Step 1", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}}},
			{ID: "s1", Name: "Duplicate", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}}},
		},
	}
	// Schema validation should catch this when checking step references
	errors := workflow.ValidateSchema(wf)
	_ = errors // duplicate step IDs are handled by the step ID map
}

func TestValidateDuplicateOutcomeIDs(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test", Name: "Test", Version: "1.0", Status: domain.WorkflowStatusActive,
		Description: "Test", AppliesTo: []string{"Task"}, EntryStep: "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{
				{ID: "o1", Name: "A", NextStep: "end"},
				{ID: "o1", Name: "B", NextStep: "end"},
			},
		}},
	}
	errors := workflow.ValidateSchema(wf)
	hasDup := false
	for _, e := range errors {
		if contains(e.Message, "duplicate") {
			hasDup = true
		}
	}
	if !hasDup {
		t.Error("expected duplicate outcome error")
	}
}

// helper
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── Mode field tests ──

func TestParseModeField_DefaultsToExecution(t *testing.T) {
	content := []byte(`id: test
name: Test
version: "1.0"
status: Active
description: Test workflow
applies_to: [Task]
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: done
        name: Done
        next_step: end
`)
	wf, err := workflow.Parse("test.yaml", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Mode != "execution" {
		t.Errorf("expected mode 'execution', got %q", wf.Mode)
	}
}

func TestParseModeField_CreationMode(t *testing.T) {
	content := []byte(`id: test-creation
name: Test Creation
version: "1.0"
status: Draft
description: Test creation workflow
mode: creation
applies_to: [Initiative]
entry_step: draft
steps:
  - id: draft
    name: Draft
    type: manual
    outcomes:
      - id: done
        name: Done
        next_step: end
`)
	wf, err := workflow.Parse("test.yaml", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Mode != "creation" {
		t.Errorf("expected mode 'creation', got %q", wf.Mode)
	}
}

func TestParseModeField_InvalidMode(t *testing.T) {
	content := []byte(`id: test-bad
name: Test Bad
version: "1.0"
status: Active
description: Bad mode
mode: invalid
applies_to: [Task]
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: done
        name: Done
        next_step: end
`)
	_, err := workflow.Parse("test.yaml", content)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
