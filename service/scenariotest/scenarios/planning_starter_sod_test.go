//go:build scenario

package scenarios_test

// INIT-024 TASK-001 (2026-10-22 review finding N-3; the committed form of
// the review's standalone PoC sa1). Under `spine serve`'s step-entry policy
// the actor who starts a planning run through the gateway — POST /runs with
// mode=planning, or POST /artifacts/entry — wrote the run's content, so
// separation of duties counts them as a run author: once another actor has
// submitted the draft unchanged, the same token's claim of the review is
// 403, the pull-model listing does not offer it to them, and an independent
// reviewer's approval lands the artifact on main. The servers here are the
// real router, auth middleware and handlers over the scenario's
// orchestrator, wired like serve_wiring's planningRunAdapter (the request
// context, which carries the authenticated actor, goes to the orchestrator
// unchanged).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// planningGatewayServer serves the scenario runtime through the real
// gateway with the planning starter, the step claimer and the step listing,
// and mints a token for each named actor (state key "tok_<actor>").
func planningGatewayServer(actors ...string) engine.Step {
	return engine.Step{
		Name: "setup-gateway-with-planning-starter",
		Action: func(sc *engine.ScenarioContext) error {
			authSvc := auth.NewService(sc.Runtime.Store)
			o := sc.Runtime.Orchestrator
			srv := gateway.NewServer(":0", gateway.ServerConfig{
				Store:               sc.Runtime.Store,
				Auth:                authSvc,
				Artifacts:           sc.Runtime.Artifacts,
				Git:                 sc.Repo.Git,
				PlanningRunStarter:  &gwPlanningRunAdapter{orch: o},
				StepClaimer:         o,
				StepExecutionLister: o,
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

// startPlanningRunThroughAPI posts body to urlPath as actorID, expects 201,
// and records the run, its live entry step and (when the response names
// one) the artifact path.
func startPlanningRunThroughAPI(actorID, urlPath, body string) engine.Step {
	return engine.Step{
		Name: "post-" + strings.TrimPrefix(urlPath, "/api/v1/") + "-as-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			code, resp, err := doPOST(sc, urlPath, "tok_"+actorID, body)
			if err != nil {
				return err
			}
			if code != http.StatusCreated {
				return fmt.Errorf("POST %s as %s = %d (%s), want 201", urlPath, actorID, code, resp)
			}
			var out map[string]any
			if err := json.Unmarshal(resp, &out); err != nil {
				return err
			}
			runID, _ := out["run_id"].(string)
			if runID == "" {
				return fmt.Errorf("POST %s response = %s, want a run", urlPath, resp)
			}
			sc.Set("run_id", runID)
			if p, _ := out["artifact_path"].(string); p != "" {
				sc.Set("artifact_path", p)
			} else if p, _ := out["task_path"].(string); p != "" {
				sc.Set("artifact_path", p)
			}
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

// claimCurrentStepThroughAPI is POST /execution/claim of the current step
// with actorID's own token; it expects want, and on a refusal a body
// containing mustContain.
func claimCurrentStepThroughAPI(actorID string, want int, mustContain string) engine.Step {
	return engine.Step{
		Name: fmt.Sprintf("claim-through-api-as-%s-want-%d", actorID, want),
		Action: func(sc *engine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			body := fmt.Sprintf(`{"actor_id":%q,"execution_id":%q}`, actorID, execID)
			code, resp, err := doPOST(sc, "/api/v1/execution/claim", "tok_"+actorID, body)
			if err != nil {
				return err
			}
			if code != want {
				return fmt.Errorf("claim of %s by %s = %d (%s), want %d", execID, actorID, code, resp, want)
			}
			if mustContain != "" && !strings.Contains(string(resp), mustContain) {
				return fmt.Errorf("claim response %s must contain %q", resp, mustContain)
			}
			return nil
		},
	}
}

// assertListingOffersCurrentStep checks GET /execution/steps for actorID
// (its own token) offers the current waiting execution, or not.
func assertListingOffersCurrentStep(actorID string, offered bool) engine.Step {
	return engine.Step{
		Name: fmt.Sprintf("listing-for-%s-offers-current-%v", actorID, offered),
		Action: func(sc *engine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			code, resp, err := doGET(sc, "/api/v1/execution/steps?status=waiting&limit=100&actor_id="+actorID, "tok_"+actorID)
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("GET /execution/steps as %s = %d (%s)", actorID, code, resp)
			}
			if got := strings.Contains(string(resp), execID); got != offered {
				return fmt.Errorf("listing for %s offers %s = %v, want %v (%s)", actorID, execID, got, offered, resp)
			}
			return nil
		},
	}
}

func assertArtifactOnMain() engine.Step {
	return engine.Step{
		Name: "assert-artifact-on-main",
		Action: func(sc *engine.ScenarioContext) error {
			p := sc.MustGet("artifact_path").(string)
			if _, err := sc.Repo.Git.ReadFile(sc.Ctx, "main", p); err != nil {
				return fmt.Errorf("%s is not on main: %w", p, err)
			}
			return nil
		},
	}
}

// planningSoDActors registers the starter (a contributor, the least role
// that may start a planning run and claim), the drafter and an independent
// reviewer, and installs `spine serve`'s step-entry policy.
func planningSoDActors() []engine.Step {
	return []engine.Step{
		registerActor("pl-starter", domain.ActorTypeHuman, domain.RoleContributor),
		registerActor("pl-drafter", domain.ActorTypeHuman, domain.RoleContributor),
		registerActor("pl-reviewer", domain.ActorTypeHuman, domain.RoleReviewer),
		installStrictStepEntryPolicy(),
	}
}

// planningSelfReviewSteps: the drafter submits the draft (unchanged) and
// passes validation, the starter is refused its review through the API and
// is not offered it, the independent reviewer is offered it and approves,
// the artifact lands on main, and the run records its starter.
func planningSelfReviewSteps() []engine.Step {
	return []engine.Step{
		engine.AssertCurrentStep("draft"),
		claimCurrentStep("pl-drafter"),
		engine.SubmitStepResult("ready_for_review", "artifact_content"),
		engine.AssertCurrentStep("validate"),
		claimCurrentStep("pl-drafter"),
		engine.SubmitStepResult("valid"),
		engine.AssertCurrentStep("review"),

		claimCurrentStepThroughAPI("pl-starter", http.StatusForbidden, "(run start)"),
		assertListingOffersCurrentStep("pl-starter", false),
		assertListingOffersCurrentStep("pl-reviewer", true),

		claimCurrentStepThroughAPI("pl-reviewer", http.StatusOK, ""),
		engine.SubmitStepResult("approved"),
		engine.AssertRunCompleted(),
		assertArtifactOnMain(),
		assertLifecycleStarter("pl-starter"),
	}
}

// AC 2 (regression): POST /runs mode=planning, then the same token claims
// the review: 403. At the scaffold base the claim answers 200.
func TestScenario_PlanningRunStarterCannotSelfReview(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	start, err := json.Marshal(map[string]string{
		"mode": "planning", "task_path": "initiatives/init-099/initiative.md", "artifact_content": testInitiativeContent,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := []engine.Step{seedCreationWorkflow(), engine.SyncProjections()}
	steps = append(steps, planningSoDActors()...)
	steps = append(steps,
		planningGatewayServer("pl-starter", "pl-reviewer"),
		startPlanningRunThroughAPI("pl-starter", "/api/v1/runs", string(start)),
	)
	steps = append(steps, planningSelfReviewSteps()...)
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-starter-cannot-self-review",
		Description: "A planning run started through POST /runs is not reviewable by its starter under the strict step-entry policy",
		EnvOpts:     []harness.EnvOption{harness.WithGovernance(), harness.WithRuntimeOrchestrator()},
		Steps:       steps,
	})
}

// The same through POST /artifacts/entry, the gateway's other planning
// start (the artifact content is built by the handler, but the caller
// started the run and is its recorded author).
func TestScenario_ArtifactEntryStarterCannotSelfReview(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	steps := []engine.Step{seedCreationWorkflow(), engine.SyncProjections()}
	steps = append(steps, planningSoDActors()...)
	steps = append(steps,
		planningGatewayServer("pl-starter", "pl-reviewer"),
		startPlanningRunThroughAPI("pl-starter", "/api/v1/artifacts/entry",
			`{"artifact_type":"Initiative","title":"Starter Initiative"}`),
	)
	steps = append(steps, planningSelfReviewSteps()...)
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifact-entry-starter-cannot-self-review",
		Description: "A planning run started through POST /artifacts/entry is not reviewable by its starter under the strict step-entry policy",
		EnvOpts:     []harness.EnvOption{harness.WithGovernance(), harness.WithRuntimeOrchestrator()},
		Steps:       steps,
	})
}

// A planning start under the strict policy with no actor on the context
// (the harness bypassing the gateway, as a dev-mode or misbuilt caller
// would) is refused with ErrUnauthorized and leaves no run behind.
func TestScenario_PlanningStartWithoutActorRefusedUnderStrictPolicy(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-start-without-actor-refused",
		Description: "StartPlanningRun with no authenticated actor is 401 under the strict step-entry policy",
		EnvOpts:     []harness.EnvOption{harness.WithGovernance(), harness.WithRuntimeOrchestrator()},
		Steps: []engine.Step{
			seedCreationWorkflow(),
			engine.SyncProjections(),
			installStrictStepEntryPolicy(),
			{
				Name: "start-without-actor",
				Action: func(sc *engine.ScenarioContext) error {
					if domain.ActorFromContext(sc.Ctx) != nil {
						return fmt.Errorf("the scenario context unexpectedly binds an actor")
					}
					_, err := sc.Runtime.Orchestrator.StartPlanningRun(sc.Ctx, "initiatives/init-099/initiative.md", testInitiativeContent)
					var se *domain.SpineError
					if !errors.As(err, &se) || se.Code != domain.ErrUnauthorized {
						return fmt.Errorf("actor-less start: %v, want %s", err, domain.ErrUnauthorized)
					}
					runs, err := sc.Runtime.Store.ListRunsByStatus(sc.Ctx, domain.RunStatusPending)
					if err != nil {
						return err
					}
					if len(runs) != 0 {
						return fmt.Errorf("a refused start left %d pending runs", len(runs))
					}
					return nil
				},
			},
		},
	})
}
