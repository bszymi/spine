package workflow_test

// INIT-023 TASK-007 AC 15: DeferredStatusPreconditions finds status
// preconditions on the run's own artifact that can never hold mid-run.
// Every case asserts the full DeferredStatusPrecondition value, because
// SMP TASK-011's legacy loader removes exactly what the report names.
// Mutation: reporting Index 0 for every entry fails
// TestDeferredStatusPreconditions_ReportsRealIndex.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// spineEpicLifecycle61994fe is Spine's workflows/epic-lifecycle.yaml as it
// stood at 61994fe, before TASK-007 removed execute's precondition.
const spineEpicLifecycle61994fe = `id: epic-lifecycle
name: Epic Lifecycle Workflow
version: "1.0"
status: Active
description: >
  Workflow for epic progression. An epic is planned, tasks are
  executed under it, and the epic is reviewed for completion.

applies_to:
  - Epic

entry_step: plan

steps:
  - id: plan
    name: Plan Epic
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills: [planning]
    preconditions:
      - type: artifact_status
        config:
          status: Pending
    required_outputs:
      - task_breakdown
    outcomes:
      - id: planned
        name: Epic Planned
        next_step: execute
        commit:
          status: In Progress
    timeout: "72h"

  - id: execute
    name: Execute Tasks
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills: [execution]
    preconditions:
      - type: artifact_status
        config:
          status: In Progress
    outcomes:
      - id: tasks_complete
        name: All Tasks Complete
        next_step: review
      - id: blocked
        name: Blocked
        next_step: plan
    timeout: "168h"

  - id: review
    name: Review Epic
    type: review
    execution:
      mode: human_only
      eligible_actor_types:
        - human
      required_skills: [review]
    outcomes:
      - id: completed
        name: Epic Complete
        next_step: end
        commit:
          status: Completed
      - id: needs_more_work
        name: Needs More Work
        next_step: execute
    timeout: "48h"
`

// smpTaskDefaultV8 carries SMP workflows/task-default.yaml v8.0's steps
// (descriptions and timeouts trimmed; preconditions, outcomes and commit
// blocks verbatim).
const smpTaskDefaultV8 = `id: task-default
name: Default Task Workflow
version: "8.0"
status: Active
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types: [ai_agent, human]
      required_skills: [implementation]
    preconditions:
      - type: artifact_status
        config:
          status: Pending
    required_outputs:
      - deliverable
    outcomes:
      - id: completed
        name: Implementation Complete
        next_step: validate
        commit:
          status: In Progress
      - id: blocked
        name: Blocked on Dependency
        next_step: execute
  - id: validate
    name: Validate Build and Tests
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types: [automated_system]
    outcomes:
      - id: passed
        name: Validation Passed
        next_step: verify
      - id: failed
        name: Validation Failed
        next_step: execute
  - id: verify
    name: Verify Architecture Compliance
    type: review
    execution:
      mode: hybrid
      eligible_actor_types: [human, ai_agent]
      required_skills: [architecture]
    outcomes:
      - id: compliant
        name: Architecture Compliant
        next_step: review
      - id: non_compliant
        name: Architecture Non-Compliant
        next_step: execute
  - id: review
    name: Code Review
    type: review
    execution:
      mode: hybrid
      eligible_actor_types: [human, ai_agent]
      required_skills: [review]
    preconditions:
      - type: field_value
        config:
          field: status
          value: In Progress
    required_inputs:
      - deliverable
    outcomes:
      - id: accepted
        name: Review Passed
        next_step: publish
      - id: needs_rework
        name: Needs Rework
        next_step: execute
  - id: publish
    name: Publish Accepted Outcome
    type: internal
    execution:
      mode: spine_only
      handler: merge
    outcomes:
      - id: published
        name: Published
        next_step: end
        commit:
          status: Completed
      - id: merge_failed
        name: Merge or Push Failed
        next_step: execute
`

func parseLintFixture(t *testing.T, name, body string) *domain.WorkflowDefinition {
	t.Helper()
	wf, err := workflow.Parse("workflows/"+name, []byte(body))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return wf
}

func requireDeferred(t *testing.T, got, want []workflow.DeferredStatusPrecondition) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeferredStatusPreconditions =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestDeferredStatusPreconditions_SpineEpicLifecycleAt61994fe(t *testing.T) {
	wf := parseLintFixture(t, "epic-lifecycle.yaml", spineEpicLifecycle61994fe)
	requireDeferred(t, workflow.DeferredStatusPreconditions(wf), []workflow.DeferredStatusPrecondition{
		{StepID: "execute", Index: 0, Type: "artifact_status", Status: "In Progress", SetBy: []string{"plan.planned"}},
	})
}

func TestDeferredStatusPreconditions_SMPTaskDefaultV8(t *testing.T) {
	wf := parseLintFixture(t, "task-default.yaml", smpTaskDefaultV8)
	requireDeferred(t, workflow.DeferredStatusPreconditions(wf), []workflow.DeferredStatusPrecondition{
		{StepID: "review", Index: 0, Type: "field_value", Status: "In Progress", SetBy: []string{"execute.completed"}},
	})
}

func TestDeferredStatusPreconditions_SpineTaskDefaultIsClean(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "workflows", "task-default.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := parseLintFixture(t, "task-default.yaml", string(body))
	requireDeferred(t, workflow.DeferredStatusPreconditions(wf), nil)
}

func lintStep(id string, pre []domain.Precondition, outcomes ...domain.OutcomeDefinition) domain.StepDefinition {
	return domain.StepDefinition{ID: id, Name: id, Type: domain.StepTypeManual, Preconditions: pre, Outcomes: outcomes}
}

func TestDeferredStatusPreconditions_ReportsRealIndex(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "wf", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			lintStep("execute", []domain.Precondition{{Type: "artifact_status", Config: map[string]string{"status": "Pending"}}},
				domain.OutcomeDefinition{ID: "completed", NextStep: "review", Commit: map[string]string{"status": "In Progress"}},
				domain.OutcomeDefinition{ID: "handed_off", NextStep: "review", Commit: map[string]string{"status": "In Progress"}}),
			lintStep("review", []domain.Precondition{
				{Type: "links_exist", Config: map[string]string{"link_type": "parent"}},
				{Type: "field_present", Config: map[string]string{"field": "status"}},
				{Type: "field_value", Config: map[string]string{"field": "status", "value": "In Progress"}},
				{Type: "artifact_status", Config: map[string]string{"status": "In Progress"}},
			}, domain.OutcomeDefinition{ID: "accepted", NextStep: "end", Commit: map[string]string{"status": "Completed"}}),
		},
	}
	requireDeferred(t, workflow.DeferredStatusPreconditions(wf), []workflow.DeferredStatusPrecondition{
		{StepID: "review", Index: 2, Type: "field_value", Status: "In Progress", SetBy: []string{"execute.completed", "execute.handed_off"}},
		{StepID: "review", Index: 3, Type: "artifact_status", Status: "In Progress", SetBy: []string{"execute.completed", "execute.handed_off"}},
	})
}

func TestDeferredStatusPreconditions_IgnoresWhatCanHold(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "wf", EntryStep: "draft",
		Steps: []domain.StepDefinition{
			// The entry step requires Pending; an outcome that also commits
			// Pending does not make a later Pending requirement deferred.
			lintStep("draft", []domain.Precondition{{Type: "artifact_status", Config: map[string]string{"status": "Pending"}}},
				domain.OutcomeDefinition{ID: "ready", NextStep: "execute", Commit: map[string]string{"status": "Pending"}}),
			lintStep("execute", []domain.Precondition{
				{Type: "field_value", Config: map[string]string{"field": "status", "value": "Pending"}},
				// Another artifact's status (explicit path) is not the run's own.
				{Type: "artifact_status", Config: map[string]string{"path": "initiatives/x/epic.md", "status": "Completed"}},
				// field_value on a non-status field is out of scope.
				{Type: "field_value", Config: map[string]string{"field": "owner", "value": "Completed"}},
				// field_present requires presence, not a value.
				{Type: "field_present", Config: map[string]string{"field": "status"}},
			}, domain.OutcomeDefinition{ID: "done", NextStep: "end", Commit: map[string]string{"status": "Completed"}}),
		},
	}
	requireDeferred(t, workflow.DeferredStatusPreconditions(wf), nil)
	requireDeferred(t, workflow.DeferredStatusPreconditions(nil), nil)
}
