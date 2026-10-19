//go:build scenario

package scenarios_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	spineEngine "github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// INIT-023 TASK-008 (audit P3-19), end to end on PostgreSQL and real git,
// with the harness's default merge binding and the store's compare-and-swap
// capability: step-output evidence is recorded at every capture and read
// back for run detail from the reserved commit_meta keys alone.
//
// Scenario: a step that declared required_outputs but changed nothing is flagged
//
//	Given a run of a workflow whose execute step declares required_outputs
//	When execute completes with nothing pushed
//	Then its evidence is unchanged against the baseline and flagged
//	When a deliverable is pushed and the review sends the run back
//	Then the review's evidence is changed (one path)
//	When execute attempt 2 completes with nothing new pushed
//	Then it is unchanged against the review's binding and flagged
//	And the event log holds the three step_completed events with the same evidence
//	And run detail returns the same three entries after the run's event log rows are deleted
func TestStepOutputEvidence_RecordedAndReadBack(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	taskPath := "initiatives/init-081/epics/epic-081/tasks/task-081.md"
	engine.RunScenario(t, engine.Scenario{
		Name:        "step-output-evidence",
		Description: "Evidence is recorded per execution and survives without the event log",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit("workflows/task-default.yaml", standardRunMergeWorkflowYAML, "seed task-default workflow"),
			engine.SeedHierarchy("INIT-081", "EPIC-081", "TASK-081"),
			engine.SyncProjections(),
			{
				// spine serve's wiring: the delivery subscriber writes every
				// routed event to runtime.event_log (review round 1: without
				// it the log stayed empty and the deletion below proved
				// nothing).
				Name: "event-log-subscriber",
				Action: func(sc *engine.ScenarioContext) error {
					sub := delivery.NewDeliverySubscriber(sc.Runtime.Store, delivery.NewStoreSubscriptionLister(sc.Runtime.Store))
					return sub.Subscribe(sc.Ctx, sc.Runtime.Events)
				},
			},
			engine.StartRun(taskPath),
			engine.AssertRunStatus(domain.RunStatusActive),

			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),
			engine.WriteOnBranch(
				"initiatives/init-081/epics/epic-081/tasks/task-081-deliverable.md",
				"# Deliverable\n", "Add deliverable"),
			engine.SubmitStepResult("needs_rework"),
			engine.AssertCurrentStep("execute"),
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			{
				Name: "run-detail-evidence",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					before, err := sc.Runtime.Orchestrator.StepOutputEvidence(sc.Ctx, runID)
					if err != nil {
						return err
					}
					// The audit-trail copy: the run's three step_completed
					// events reach the event log (asynchronously, through
					// the queue) and carry the same evidence run detail
					// returns.
					logged, err := waitStepCompletedLogged(sc.Ctx, sc.Runtime.Store, runID, 3)
					if err != nil {
						return err
					}
					for _, ev := range before {
						p, ok := logged[ev.ExecutionID]
						if !ok {
							return fmt.Errorf("no step_completed in the event log for %s", ev.ExecutionID)
						}
						if p.BranchChange == nil || !reflect.DeepEqual(*p.BranchChange, ev.BranchChange) ||
							p.RequiredOutputsUnchanged == nil || *p.RequiredOutputsUnchanged != ev.RequiredOutputsUnchanged {
							return fmt.Errorf("event log evidence for %s = %+v, run detail %+v", ev.ExecutionID, p, ev)
						}
					}
					// Run detail must not depend on the retention-pruned
					// event log. runtime.event_log has no run_id column;
					// its payload is the event JSON, which names the run.
					if err := sc.Runtime.Store.ExecRaw(sc.Ctx, `DELETE FROM runtime.event_log WHERE payload::text LIKE '%' || $1 || '%'`, runID); err != nil {
						return fmt.Errorf("clear event log: %w", err)
					}
					if left, err := stepCompletedLogged(sc.Ctx, sc.Runtime.Store, runID); err != nil || len(left) != 0 {
						return fmt.Errorf("after the delete the event log still holds %d step_completed rows of the run (%v)", len(left), err)
					}
					got, err := sc.Runtime.Orchestrator.StepOutputEvidence(sc.Ctx, runID)
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(got, before) {
						return fmt.Errorf("run detail changed when the event log was deleted:\nbefore %+v\nafter  %+v", before, got)
					}
					if len(got) != 3 {
						return fmt.Errorf("%d evidence entries, want 3: %+v", len(got), got)
					}
					ex1, rv1, ex2 := got[0], got[1], got[2]
					if ex1.StepID != "execute" || ex1.Attempt != 1 || ex1.BranchChange.Status != spineEngine.BranchChangeUnchanged ||
						ex1.BranchChange.PreviousSource != spineEngine.EvidencePreviousSourceBaseline || !ex1.RequiredOutputsUnchanged {
						return fmt.Errorf("execute attempt 1 = %+v, want unchanged against the baseline, flagged", ex1)
					}
					if rv1.StepID != "review" || rv1.BranchChange.Status != spineEngine.BranchChangeChanged ||
						rv1.BranchChange.ChangedPaths == nil || *rv1.BranchChange.ChangedPaths != 1 || rv1.RequiredOutputsUnchanged {
						return fmt.Errorf("review = %+v, want changed with one path, not flagged", rv1)
					}
					if !strings.HasPrefix(rv1.BranchChange.PreviousSource, "step:execute:") {
						return fmt.Errorf("review previous_source = %q, want the execute binding", rv1.BranchChange.PreviousSource)
					}
					if ex2.StepID != "execute" || ex2.Attempt != 2 || ex2.BranchChange.Status != spineEngine.BranchChangeUnchanged ||
						ex2.BranchChange.PreviousSource != "step:review:"+rv1.ExecutionID || !ex2.RequiredOutputsUnchanged {
						return fmt.Errorf("execute attempt 2 = %+v, want unchanged against the review's binding, flagged", ex2)
					}
					if ex2.BranchChange.HeadSHA != rv1.BranchChange.HeadSHA {
						return fmt.Errorf("execute attempt 2 head %s, review head %s", ex2.BranchChange.HeadSHA, rv1.BranchChange.HeadSHA)
					}
					return nil
				},
			},
		},
	})
}

// loggedStepCompleted is the evidence part of a logged step_completed.
type loggedStepCompleted struct {
	ExecutionID              string                    `json:"execution_id"`
	BranchChange             *spineEngine.BranchChange `json:"branch_change"`
	RequiredOutputsUnchanged *bool                     `json:"required_outputs_unchanged"`
}

// stepCompletedLogged returns the run's step_completed payloads in
// runtime.event_log, by execution id.
func stepCompletedLogged(ctx context.Context, st *store.PostgresStore, runID string) (map[string]loggedStepCompleted, error) {
	rows, err := st.ListEventsAfter(ctx, "", []string{string(domain.EventStepCompleted)}, 1000)
	if err != nil {
		return nil, fmt.Errorf("list event log: %w", err)
	}
	out := map[string]loggedStepCompleted{}
	for i := range rows {
		var ev domain.Event
		if err := json.Unmarshal(rows[i].Payload, &ev); err != nil {
			return nil, fmt.Errorf("event %s: %w", rows[i].EventID, err)
		}
		if ev.RunID != runID {
			continue
		}
		var p loggedStepCompleted
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, fmt.Errorf("event %s payload: %w", rows[i].EventID, err)
		}
		out[p.ExecutionID] = p
	}
	return out, nil
}

// waitStepCompletedLogged waits (bounded) until the event log holds at
// least n step_completed events of the run.
func waitStepCompletedLogged(ctx context.Context, st *store.PostgresStore, runID string, n int) (map[string]loggedStepCompleted, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := stepCompletedLogged(ctx, st, runID)
		if err != nil {
			return nil, err
		}
		if len(got) >= n {
			return got, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("event log holds %d step_completed events of the run, want %d", len(got), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// INIT-023 TASK-008 (audit P3-24), on PostgreSQL: a completed execution is
// never routed twice. A re-submit of the completed execute step through
// IngestResult answers the persisted state; through SubmitStepResult it is
// refused; and RecoverCompletedStep refuses because the step was routed.
func TestCompletedStep_IsNotReRouted(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "completed-step-not-rerouted",
		Description: "No second routing of a completed execution",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit("workflows/task-default.yaml", standardRunMergeWorkflowYAML, "seed task-default workflow"),
			engine.SeedHierarchy("INIT-082", "EPIC-082", "TASK-082"),
			engine.SyncProjections(),
			engine.StartRun("initiatives/init-082/epics/epic-082/tasks/task-082.md"),
			{
				Name: "remember-execute",
				Action: func(sc *engine.ScenarioContext) error {
					sc.Set("execute_id", sc.MustGet("current_execution_id").(string))
					return nil
				},
			},
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),
			{
				Name: "resubmit-completed",
				Action: func(sc *engine.ScenarioContext) error {
					execID := sc.MustGet("execute_id").(string)
					runID := sc.MustGet("run_id").(string)
					before, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					resp, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
						ExecutionID: execID, OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}})
					if err != nil || resp.Status != domain.StepStatusCompleted {
						return fmt.Errorf("duplicate IngestResult = %+v, %v; want the persisted completed state", resp, err)
					}
					if err := sc.Runtime.Orchestrator.SubmitStepResult(sc.Ctx, execID, spineEngine.StepResult{OutcomeID: "completed"}); err == nil {
						return fmt.Errorf("SubmitStepResult re-routed a completed execution")
					}
					if err := sc.Runtime.Orchestrator.RecoverCompletedStep(sc.Ctx, execID); err == nil {
						return fmt.Errorf("RecoverCompletedStep re-routed a routed execution")
					}
					after, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					if len(after) != len(before) {
						return fmt.Errorf("executions %d -> %d: a completed step was routed again", len(before), len(after))
					}
					return nil
				},
			},
		},
	})
}
