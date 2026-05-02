//go:build scenario

package scenarios_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/projection"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// ── Adapters ──────────────────────────────────────────────────────────────────

// gwRunAdapter adapts engine.Orchestrator to gateway.RunStarter.
type gwRunAdapter struct{ orch *spineEngine.Orchestrator }

func (a *gwRunAdapter) StartRun(ctx context.Context, taskPath string) (*gateway.RunStartResult, error) {
	result, err := a.orch.StartRun(ctx, taskPath)
	if err != nil {
		return nil, err
	}
	return &gateway.RunStartResult{
		RunID:      result.Run.RunID,
		TaskPath:   result.Run.TaskPath,
		WorkflowID: result.Run.WorkflowID,
		Status:     string(result.Run.Status),
		BranchName: result.Run.BranchName,
	}, nil
}

// gwPlanningRunAdapter adapts engine.Orchestrator to gateway.PlanningRunStarter.
type gwPlanningRunAdapter struct{ orch *spineEngine.Orchestrator }

func (a *gwPlanningRunAdapter) StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*gateway.PlanningRunResult, error) {
	result, err := a.orch.StartPlanningRun(ctx, artifactPath, artifactContent)
	if err != nil {
		return nil, err
	}
	entryStepID := ""
	if result.EntryStep != nil {
		entryStepID = result.EntryStep.StepID
	}
	return &gateway.PlanningRunResult{
		RunID:       result.Run.RunID,
		TaskPath:    result.Run.TaskPath,
		WorkflowID:  result.Run.WorkflowID,
		Status:      string(result.Run.Status),
		Mode:        string(result.Run.Mode),
		BranchName:  result.Run.BranchName,
		EntryStepID: entryStepID,
	}, nil
}

// gwResultAdapter adapts engine.Orchestrator to gateway.ResultHandler.
type gwResultAdapter struct{ orch *spineEngine.Orchestrator }

func (a *gwResultAdapter) IngestResult(ctx context.Context, req gateway.ResultSubmission) (*gateway.ResultResponse, error) {
	resp, err := a.orch.IngestResult(ctx, spineEngine.SubmitRequest{
		ExecutionID:       req.ExecutionID,
		OutcomeID:         req.OutcomeID,
		ArtifactsProduced: req.ArtifactsProduced,
	})
	if err != nil {
		return nil, err
	}
	return &gateway.ResultResponse{
		ExecutionID: resp.ExecutionID,
		StepID:      resp.StepID,
		Status:      string(resp.Status),
		OutcomeID:   resp.OutcomeID,
	}, nil
}

// ── Server setup ──────────────────────────────────────────────────────────────

// setupGatewayServer starts a real gateway.Server backed by the scenario runtime.
// The httptest.Server is closed via t.Cleanup. State keys set:
//   - "gw_url"  — base URL of the test server (e.g. "http://127.0.0.1:PORT")
//   - "gw_auth" — *auth.Service for token creation in subsequent steps
func setupGatewayServer(withOrchestrator bool) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-gateway-server",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			authSvc := auth.NewService(sc.Runtime.Store)
			projQuerier := projection.NewQueryService(sc.Runtime.Store, sc.Repo.Git)

			cfg := gateway.ServerConfig{
				Store:     sc.Runtime.Store,
				Auth:      authSvc,
				Artifacts: sc.Runtime.Artifacts,
				ProjQuery: projQuerier,
				ProjSync:  sc.Runtime.Projections,
			}

			if withOrchestrator && sc.Runtime.Orchestrator != nil {
				o := sc.Runtime.Orchestrator
				cfg.RunStarter = &gwRunAdapter{orch: o}
				cfg.PlanningRunStarter = &gwPlanningRunAdapter{orch: o}
				cfg.ResultHandler = &gwResultAdapter{orch: o}
				cfg.StepExecutionLister = o
				cfg.StepClaimer = o
				cfg.StepReleaser = o
				cfg.StepAcknowledger = o
				cfg.RunCanceller = o
			}

			srv := gateway.NewServer(":0", cfg)
			ts := httptest.NewServer(srv.Handler())
			sc.T.Cleanup(ts.Close)

			sc.Set("gw_url", ts.URL)
			sc.Set("gw_auth", authSvc)
			return nil
		},
	}
}

// ── Token helper ──────────────────────────────────────────────────────────────

// createGatewayToken creates a Bearer token for an actor that already exists in
// the store and stores it under tokenKey in sc.State.
func createGatewayToken(actorID, tokenKey string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "create-token-" + actorID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			authSvc := sc.MustGet("gw_auth").(*auth.Service)
			token, _, err := authSvc.CreateToken(sc.Ctx, actorID, "scenario-token", nil)
			if err != nil {
				return fmt.Errorf("create token for %s: %w", actorID, err)
			}
			sc.Set(tokenKey, token)
			return nil
		},
	}
}

// ── HTTP assertion helpers ────────────────────────────────────────────────────

// doGET executes a GET against the test server and returns status + body.
func doGET(sc *scenarioEngine.ScenarioContext, urlPath, tokenKey string) (int, []byte, error) {
	base := sc.MustGet("gw_url").(string)
	req, err := http.NewRequestWithContext(sc.Ctx, http.MethodGet, base+urlPath, nil)
	if err != nil {
		return 0, nil, err
	}
	if tokenKey != "" {
		if tok, ok := sc.State[tokenKey]; ok {
			req.Header.Set("Authorization", "Bearer "+tok.(string))
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// doPOST executes a POST against the test server and returns status + body.
func doPOST(sc *scenarioEngine.ScenarioContext, urlPath, tokenKey, jsonBody string) (int, []byte, error) {
	base := sc.MustGet("gw_url").(string)
	req, err := http.NewRequestWithContext(sc.Ctx, http.MethodPost, base+urlPath, strings.NewReader(jsonBody))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tokenKey != "" {
		if tok, ok := sc.State[tokenKey]; ok {
			req.Header.Set("Authorization", "Bearer "+tok.(string))
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// ── Workflow YAML ─────────────────────────────────────────────────────────────

const gwIntegrationWorkflowYAML = `id: task-gw-integration
name: Gateway Integration Test Workflow
version: "1.0"
status: Active
description: Single-step manual workflow for gateway HTTP integration tests.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

// ── Scenarios ─────────────────────────────────────────────────────────────────

// TestGateway_AuthRejection verifies that the auth middleware rejects requests
// without a Bearer token before any handler runs.
//
// Scenario: Unauthenticated request returns 401
//   Given a running gateway server
//   When a GET /api/v1/runs/any-run-id is made without an Authorization header
//   Then the response is 401 Unauthorized
func TestGateway_AuthRejection(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "gateway-auth-rejection",
		Description: "Auth middleware returns 401 for requests without a Bearer token",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGatewayServer(false),
			{
				Name: "get-without-token-returns-401",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					code, body, err := doGET(sc, "/api/v1/runs/any-run", "")
					if err != nil {
						return err
					}
					if code != http.StatusUnauthorized {
						return fmt.Errorf("expected 401, got %d (body: %s)", code, body)
					}
					return nil
				},
			},
			{
				Name: "post-without-token-returns-401",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					code, body, err := doPOST(sc, "/api/v1/actors", "", `{"actor_id":"x"}`)
					if err != nil {
						return err
					}
					if code != http.StatusUnauthorized {
						return fmt.Errorf("expected 401, got %d (body: %s)", code, body)
					}
					return nil
				},
			},
		},
	})
}

// TestGateway_RoleEnforcement verifies that role checks inside handlers return 403
// with the documented error body shape when the caller's role is insufficient.
//
// Scenario: Contributor cannot create actors (requires Operator)
//   Given a Contributor actor with a valid token
//   When POST /api/v1/actors is called
//   Then the response is 403 Forbidden
//     And the body matches {"status":"error","errors":[{"code":"forbidden","message":"..."}]}
func TestGateway_RoleEnforcement(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "gateway-role-enforcement",
		Description: "Contributor token on Operator-only endpoint returns 403 with correct error body",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGatewayServer(false),
			registerActor("contrib-gw", domain.ActorTypeHuman, domain.RoleContributor),
			createGatewayToken("contrib-gw", "contrib_token"),
			{
				Name: "contributor-post-actors-returns-403",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					body := `{"actor_id":"new-actor","name":"New Actor","type":"human","role":"contributor"}`
					code, respBody, err := doPOST(sc, "/api/v1/actors", "contrib_token", body)
					if err != nil {
						return err
					}
					if code != http.StatusForbidden {
						return fmt.Errorf("expected 403, got %d (body: %s)", code, respBody)
					}
					// Assert the documented error body shape.
					var errResp struct {
						Status string `json:"status"`
						Errors []struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"errors"`
					}
					if err := json.Unmarshal(respBody, &errResp); err != nil {
						return fmt.Errorf("response is not valid JSON: %w (body: %s)", err, respBody)
					}
					if errResp.Status != "error" {
						return fmt.Errorf("expected status=error, got %q", errResp.Status)
					}
					if len(errResp.Errors) == 0 {
						return fmt.Errorf("expected at least one error detail, got none")
					}
					if errResp.Errors[0].Code != "forbidden" {
						return fmt.Errorf("expected error code=forbidden, got %q", errResp.Errors[0].Code)
					}
					return nil
				},
			},
		},
	})
}

// TestGateway_RunStart_StepQuery validates the HTTP path from run start through
// step visibility: a Contributor starts a standard run via POST /api/v1/runs and
// can then see the waiting step via GET /api/v1/execution/steps.
//
// Scenario: Full run start → step query cycle over HTTP
//   Given a seeded workflow and task hierarchy with Orchestrator wired to the gateway
//   When POST /api/v1/runs is called with the task path
//   Then the response is 200 with a run_id
//   When GET /api/v1/execution/steps?actor_id=...&status=waiting is called
//   Then the response includes the first step for the new run
func TestGateway_RunStart_StepQuery(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "gateway-run-start-step-query",
		Description: "Start a run via HTTP and verify the waiting step appears in the step query endpoint",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			// Seed workflow, hierarchy, and projections.
			seedWorkflow("task-gw-integration", gwIntegrationWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-GW", "EPIC-GW", "TASK-GW"),
			scenarioEngine.SyncProjections(),

			// Start the gateway server with orchestrator wired in.
			setupGatewayServer(true),

			// Create a Contributor actor with a token (run.start requires Contributor).
			registerActor("gw-runner", domain.ActorTypeHuman, domain.RoleContributor),
			createGatewayToken("gw-runner", "runner_token"),

			// POST /api/v1/runs — start the standard run.
			{
				Name: "post-run-start-returns-200-with-run-id",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := "initiatives/init-gw/epics/epic-gw/tasks/task-gw.md"
					body := fmt.Sprintf(`{"task_path":%q}`, taskPath)
					code, respBody, err := doPOST(sc, "/api/v1/runs", "runner_token", body)
					if err != nil {
						return err
					}
					if code != http.StatusCreated {
						return fmt.Errorf("expected 201, got %d (body: %s)", code, respBody)
					}
					var resp struct {
						RunID string `json:"run_id"`
					}
					if err := json.Unmarshal(respBody, &resp); err != nil {
						return fmt.Errorf("unmarshal response: %w (body: %s)", err, respBody)
					}
					if resp.RunID == "" {
						return fmt.Errorf("expected non-empty run_id in response, got: %s", respBody)
					}
					sc.Set("gw_run_id", resp.RunID)
					return nil
				},
			},

			// GET /api/v1/execution/steps — per Option B (INIT-020/EPIC-001/
			// TASK-004), the entry hybrid step stays in `waiting` until an
			// explicit /assign or /claim binds an actor. Query with
			// status=waiting to verify the step is visible via the HTTP endpoint.
			{
				Name: "get-execution-steps-shows-waiting-step",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					url := "/api/v1/execution/steps?status=waiting"
					code, respBody, err := doGET(sc, url, "runner_token")
					if err != nil {
						return err
					}
					if code != http.StatusOK {
						return fmt.Errorf("expected 200, got %d (body: %s)", code, respBody)
					}
					var resp struct {
						Steps []struct {
							RunID  string `json:"run_id"`
							StepID string `json:"step_id"`
						} `json:"steps"`
					}
					if err := json.Unmarshal(respBody, &resp); err != nil {
						return fmt.Errorf("unmarshal response: %w (body: %s)", err, respBody)
					}
					runID := sc.MustGet("gw_run_id").(string)
					for _, s := range resp.Steps {
						if s.RunID == runID && s.StepID == "execute" {
							return nil // found
						}
					}
					return fmt.Errorf("step 'execute' for run %s not found in steps response: %s", runID, respBody)
				},
			},
		},
	})
}
