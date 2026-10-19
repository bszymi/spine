//go:build scenario

package scenarios_test

// INIT-023 wave 4 follow-up (TASK-006 integration): the ADR-008 lifecycle
// exemption needs the run's starter (engine.CommitMetaGovernanceStartedBy),
// which the engine records from the actor bound to the start's context
// (domain.ActorFromContext).
//
//   - `spine serve` starts a workflow-lifecycle run only through POST
//     /workflows or PUT /workflows/{id} for an authenticated caller below
//     operator; the gateway's auth middleware binds that actor, and serve's
//     workflowPlanningRunAdapter hands the request context to the
//     orchestrator unchanged. The Gateway* scenarios drive exactly that
//     path — the real router, auth middleware and handler over the
//     scenario's orchestrator with the governance guard and the lifecycle
//     exemption on — and show the starter recorded, a run reviewed by
//     someone else exempt, and one reviewed by its starter held.
//   - POST /runs with mode=planning never starts one (a workflows/ task
//     path is refused before any starter runs, TASK-014).
//   - The scenario harness called the orchestrator with a context that
//     carried only a log actor id, so under the guard every lifecycle run
//     was held as "the run's starter is not recorded". The harness's start
//     steps now bind the scenario's actor (lifecycleStartCtx);
//     ..._DraftAuthorCannotReview_GuardOn is TASK-014's scenario under the
//     guard.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	spineEngine "github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// lifecycleStarterAdapter is `spine serve`'s workflowPlanningRunAdapter
// (service/cmd/spine/serve_wiring.go) over the scenario's orchestrator: the
// request context goes to the orchestrator unchanged.
type lifecycleStarterAdapter struct{ orch *spineEngine.Orchestrator }

func (a lifecycleStarterAdapter) result(r *spineEngine.StartRunResult) *gateway.PlanningRunResult {
	out := &gateway.PlanningRunResult{
		RunID: r.Run.RunID, TaskPath: r.Run.TaskPath, WorkflowID: r.Run.WorkflowID, Status: string(r.Run.Status),
		Mode: string(r.Run.Mode), BranchName: r.Run.BranchName, TraceID: r.Run.TraceID,
	}
	if r.EntryStep != nil {
		out.EntryStepID = r.EntryStep.StepID
	}
	return out
}

func (a lifecycleStarterAdapter) StartWorkflowPlanningRun(ctx context.Context, id, body string) (*gateway.PlanningRunResult, error) {
	r, err := a.orch.StartWorkflowPlanningRun(ctx, id, body)
	if err != nil {
		return nil, err
	}
	return a.result(r), nil
}

func (a lifecycleStarterAdapter) StartWorkflowUpdatePlanningRun(ctx context.Context, id, body string) (*gateway.PlanningRunResult, error) {
	r, err := a.orch.StartWorkflowUpdatePlanningRun(ctx, id, body)
	if err != nil {
		return nil, err
	}
	return a.result(r), nil
}

// lifecycleGatewayServer serves the scenario runtime through the real
// gateway with a workflow service and the lifecycle starter, and mints a
// token for each named actor (state key "tok_<actor>").
func lifecycleGatewayServer(actors ...string) engine.Step {
	return engine.Step{
		Name: "setup-gateway-with-lifecycle-starter",
		Action: func(sc *engine.ScenarioContext) error {
			authSvc := auth.NewService(sc.Runtime.Store)
			srv := gateway.NewServer(":0", gateway.ServerConfig{
				Store:             sc.Runtime.Store,
				Auth:              authSvc,
				Workflows:         sc.Runtime.Workflows,
				WFPlanningStarter: lifecycleStarterAdapter{orch: sc.Runtime.Orchestrator},
			})
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)
			sc.Set("gw_url", ts.URL)
			for _, id := range actors {
				tok, _, err := authSvc.CreateToken(sc.Ctx, id, "scenario", nil)
				if err != nil {
					return fmt.Errorf("token for %s: %w", id, err)
				}
				sc.Set("tok_"+id, tok)
			}
			return nil
		},
	}
}

// startLifecycleRunThroughAPI is a reviewer's POST /workflows: the gateway
// routes it to a workflow-lifecycle planning run (ADR-008). It records the
// run and its entry step like engine.StartWorkflowPlanningRun.
func startLifecycleRunThroughAPI(actorID, id, body string) engine.Step {
	return engine.Step{
		Name: "post-workflow-as-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			req, err := json.Marshal(map[string]string{"id": id, "body": body})
			if err != nil {
				return err
			}
			code, resp, err := doPOST(sc, "/api/v1/workflows", "tok_"+actorID, string(req))
			if err != nil {
				return err
			}
			if code != 201 {
				return fmt.Errorf("POST /workflows as %s = %d (%s), want 201", actorID, code, resp)
			}
			var out map[string]any
			if err := json.Unmarshal(resp, &out); err != nil {
				return err
			}
			runID, _ := out["run_id"].(string)
			if out["write_mode"] != "planning" || runID == "" {
				return fmt.Errorf("POST /workflows response = %s, want a lifecycle planning run", resp)
			}
			sc.Set("run_id", runID)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			for i := range execs {
				if !execs[i].Status.IsTerminal() {
					sc.Set("current_execution_id", execs[i].ExecutionID)
					sc.Set("current_step_id", execs[i].StepID)
					return nil
				}
			}
			return fmt.Errorf("run %s has no live entry step", runID)
		},
	}
}

func assertLifecycleStarter(want string) engine.Step {
	return engine.Step{
		Name: "assert-lifecycle-starter-" + want,
		Action: func(sc *engine.ScenarioContext) error {
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, sc.MustGet("run_id").(string))
			if err != nil {
				return err
			}
			if got := run.CommitMeta[spineEngine.CommitMetaGovernanceStartedBy]; got != want {
				return fmt.Errorf("recorded starter = %q, want %q (commit_meta %v)", got, want, run.CommitMeta)
			}
			return nil
		},
	}
}

// assertLifecycleRunHeld: the approved run waits in committing for an
// operator decision on the workflow file, and main does not have it.
func assertLifecycleRunHeld(path string) engine.Step {
	return engine.Step{
		Name: "assert-lifecycle-run-held",
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			if run.Status != domain.RunStatusCommitting {
				return fmt.Errorf("run status = %s, want committing (held for an operator)", run.Status)
			}
			st, err := sc.Runtime.Orchestrator.GovernanceMergeStatus(sc.Ctx, runID)
			if err != nil {
				return err
			}
			if !st.Held || strings.Join(st.Paths, ",") != path {
				return fmt.Errorf("governance status = %+v, want held on %s", st, path)
			}
			if _, err := sc.Runtime.Workflows.Read(sc.Ctx, strings.TrimSuffix(strings.TrimPrefix(path, "workflows/"), ".yaml"), "main"); err == nil {
				return fmt.Errorf("%s reached main while the run is held", path)
			}
			return nil
		},
	}
}

// lifecycleActors registers the starter and two reviewers with the
// workflow-lifecycle skills, and installs `spine serve`'s step-entry
// policy.
func lifecycleActors() []engine.Step {
	steps := []engine.Step{
		registerSkill("skill-wf-author", "workflow_authoring", "governance"),
		registerSkill("skill-wf-review", "workflow_review", "governance"),
	}
	for _, id := range []string{"wl-starter", "rev-a", "rev-b"} {
		steps = append(steps,
			registerActor(id, domain.ActorTypeHuman, domain.RoleReviewer),
			assignSkillToActor(id, "skill-wf-author"),
			assignSkillToActor(id, "skill-wf-review"))
	}
	return append(steps, installStrictStepEntryPolicy())
}

func guardedLifecycleEnv() []harness.EnvOption {
	return []harness.EnvOption{
		harness.WithWorkflows(),
		harness.WithRuntimeOrchestrator(),
		harness.WithRuntimeGovernanceGuard(),
	}
}

// A reviewer's POST /workflows under the guard: the starter is recorded,
// another reviewer (not the starter, not the draft author) approves, and
// the lifecycle exemption lets the merge through without an operator.
func TestWorkflowLifecycle_GatewayStartedRunReviewedByAnotherIsExempt(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	steps := append([]engine.Step{engine.SyncProjections()}, lifecycleActors()...)
	steps = append(steps,
		lifecycleGatewayServer("wl-starter"),
		startLifecycleRunThroughAPI("wl-starter", "new-flow", newWorkflowBodyV1),
		assertLifecycleStarter("wl-starter"),
		engine.AssertCurrentStep("draft"),
		claimCurrentStep("rev-a"),
		engine.SubmitStepResult("submitted", "workflow_body"),
		engine.AssertCurrentStep("review"),
		claimCurrentStep("rev-b"),
		engine.SubmitStepResult("approved"),
		engine.AssertRunCompleted(),
		engine.AssertFileExists("workflows/new-flow.yaml"),
	)
	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-gateway-start-exempt",
		Description: "A lifecycle run started through POST /workflows records its starter; another reviewer's approval is exempt under the guard",
		EnvOpts:     guardedLifecycleEnv(),
		Steps:       steps,
	})
}

// The same start, but the starter — who wrote the body through POST
// /workflows — reviews it: the step-entry policy lets them claim (they ran
// no step), and the exemption refuses them, so the merge waits for an
// operator.
func TestWorkflowLifecycle_GatewayStartedRunReviewedByItsStarterIsHeld(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	steps := append([]engine.Step{engine.SyncProjections()}, lifecycleActors()...)
	steps = append(steps,
		lifecycleGatewayServer("wl-starter"),
		startLifecycleRunThroughAPI("wl-starter", "new-flow", newWorkflowBodyV1),
		assertLifecycleStarter("wl-starter"),
		claimCurrentStep("rev-a"),
		engine.SubmitStepResult("submitted", "workflow_body"),
		engine.AssertCurrentStep("review"),
		claimCurrentStep("wl-starter"),
		engine.SubmitStepResult("approved"),
		assertLifecycleRunHeld("workflows/new-flow.yaml"),
	)
	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-gateway-start-self-review-held",
		Description: "A lifecycle run reviewed by the reviewer who started it through POST /workflows is held for an operator under the guard",
		EnvOpts:     guardedLifecycleEnv(),
		Steps:       steps,
	})
}

// TASK-014 AC 19 under the guard and the lifecycle exemption (spine
// serve's defaults). The harness starts the run as the scenario's actor
// (engine.ScenarioLifecycleStarter); rev-a drafts and rev-b reviews, so the
// merge is exempt. Before the harness bound that actor the run was held
// ("the run's starter is not recorded").
func TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview_GuardOn(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	steps := append([]engine.Step{engine.SyncProjections()}, lifecycleActors()...)
	steps = append(steps,
		engine.StartWorkflowUpdatePlanningRun("task-spike", taskSpikeBodyV11),
		assertLifecycleStarter(engine.ScenarioLifecycleStarter),
		engine.AssertCurrentStep("draft"),
		claimCurrentStep("rev-a"),
		engine.SubmitStepResult("submitted", "workflow_body"),
		engine.AssertCurrentStep("review"),
		claimCurrentStepRefused("rev-a", domain.ErrForbidden),
		claimCurrentStep("rev-b"),
		engine.SubmitStepResult("approved"),
		engine.AssertRunCompleted(),
		assertMainWorkflowVersion("task-spike", "1.1"),
	)
	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-update-second-reviewer-guard-on",
		Description: "TASK-014 AC 19 with the governance guard and the lifecycle exemption on",
		EnvOpts:     guardedLifecycleEnv(),
		Steps:       steps,
	})
}
