//go:build scenario

package scenarios_test

// INIT-023 TASK-006 AC 24 (ADR-016): with the governance-path guard on, a
// contributor's run that edits workflows/task-default.yaml is held at the
// publish step; a contributor cannot decide it; an operator approves the
// bound commit through the API; main advances, and the projection installs
// the new workflow version.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// governedWorkflowV11 is publishStepWorkflowYAML with a bumped version: the
// contributor's governance change.
var governedWorkflowV11 = strings.Replace(publishStepWorkflowYAML, `version: "1.0"`, `version: "1.1"`, 1)

func governanceGatewayServer() engine.Step {
	return engine.Step{
		Name: "setup-gateway-with-governance-decider",
		Action: func(sc *engine.ScenarioContext) error {
			authSvc := auth.NewService(sc.Runtime.Store)
			srv := gateway.NewServer(":0", gateway.ServerConfig{
				Store:            sc.Runtime.Store,
				Auth:             authSvc,
				RunMergeResolver: sc.Runtime.Orchestrator,
			})
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)
			sc.Set("gw_url", ts.URL)
			for _, a := range []struct {
				id   string
				role domain.ActorRole
				key  string
			}{{"gov-contributor", domain.RoleContributor, "tok_contrib"}, {"gov-operator", domain.RoleOperator, "tok_op"}} {
				if err := sc.Runtime.Store.CreateActor(sc.Ctx, &domain.Actor{ActorID: a.id, Type: domain.ActorTypeHuman,
					Name: a.id, Role: a.role, Status: domain.ActorStatusActive}); err != nil {
					return fmt.Errorf("create actor %s: %w", a.id, err)
				}
				tok, _, err := authSvc.CreateToken(sc.Ctx, a.id, "scenario", nil)
				if err != nil {
					return fmt.Errorf("token for %s: %w", a.id, err)
				}
				sc.Set(a.key, tok)
			}
			return nil
		},
	}
}

func assertGovernanceHeld() engine.Step {
	return engine.Step{
		Name: "assert-merge-held-for-governance",
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			if run.Status != domain.RunStatusCommitting {
				return fmt.Errorf("run status = %s, want committing (held)", run.Status)
			}
			st, err := sc.Runtime.Orchestrator.GovernanceMergeStatus(sc.Ctx, runID)
			if err != nil {
				return err
			}
			if !st.Held || strings.Join(st.Paths, ",") != "workflows/task-default.yaml" {
				return fmt.Errorf("governance status = %+v, want held on workflows/task-default.yaml", st)
			}
			sc.Set("bound_sha", st.BoundSHA)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			for i := range execs {
				if execs[i].StepID == "publish" && !execs[i].Status.IsTerminal() {
					if d := execs[i].ErrorDetail; d == nil || d.Classification != domain.FailureGovernanceApprovalRequired {
						return fmt.Errorf("publish step detail = %+v", d)
					}
					return nil
				}
			}
			return fmt.Errorf("no live publish step execution")
		},
	}
}

func decideThroughAPI(tokenKey string, wantStatus int) engine.Step {
	return engine.Step{
		Name: "governance-decision-" + tokenKey,
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			body := fmt.Sprintf(`{"decision":"approve","commit_sha":%q}`, sc.MustGet("bound_sha").(string))
			code, resp, err := doPOST(sc, "/api/v1/runs/"+runID+"/governance-decision", tokenKey, body)
			if err != nil {
				return err
			}
			if code != wantStatus {
				return fmt.Errorf("POST governance-decision as %s = %d (%s), want %d", tokenKey, code, resp, wantStatus)
			}
			if wantStatus == 200 {
				var out map[string]any
				if err := json.Unmarshal(resp, &out); err != nil {
					return err
				}
				if out["run_status"] != string(domain.RunStatusCompleted) || out["merge_error"] != "" {
					return fmt.Errorf("decision response = %s", resp)
				}
			}
			return nil
		},
	}
}

func assertWorkflowProjected(version string) engine.Step {
	return engine.Step{
		Name: "assert-workflow-projected-" + version,
		Action: func(sc *engine.ScenarioContext) error {
			wf, err := sc.Runtime.Store.GetWorkflowProjection(context.Background(), "workflows/task-default.yaml")
			if err != nil {
				return err
			}
			if wf.Version != version {
				return fmt.Errorf("projected task-default version = %q, want %q", wf.Version, version)
			}
			return nil
		},
	}
}

func TestGovernancePathGuard_HeldUntilOperatorApproves(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "governance-path-guard-operator-approval",
		Description: "A run that edits a workflow is held until an operator approves the bound commit through the API",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithRuntimeGovernanceGuard(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit("workflows/task-default.yaml", publishStepWorkflowYAML, "seed task-default"),
			engine.SeedHierarchy("INIT-160", "EPIC-160", "TASK-160"),
			engine.SyncProjections(),
			assertWorkflowProjected("1.0"),

			engine.StartRun("initiatives/init-160/epics/epic-160/tasks/task-160.md"),
			engine.WriteOnBranch("workflows/task-default.yaml", governedWorkflowV11, "contributor bumps the workflow"),
			engine.SubmitStepResult("completed", "deliverable"),
			engine.SubmitStepResult("accepted"),

			assertGovernanceHeld(),
			engine.AssertFileContains("workflows/task-default.yaml", `version: "1.0"`),

			governanceGatewayServer(),
			decideThroughAPI("tok_contrib", 403),
			decideThroughAPI("tok_op", 200),

			engine.AssertRunCompleted(),
			engine.AssertFileContains("workflows/task-default.yaml", `version: "1.1"`),
			engine.SyncProjections(),
			assertWorkflowProjected("1.1"),
			{
				Name: "assert-approval-recorded",
				Action: func(sc *engine.ScenarioContext) error {
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, sc.MustGet("run_id").(string))
					if err != nil {
						return err
					}
					if run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] != "gov-operator" ||
						run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA] == "" {
						return fmt.Errorf("commit_meta = %v", run.CommitMeta)
					}
					if _, ok := sc.Runtime.Orchestrator.GovernanceGuardConfig(); !ok {
						return fmt.Errorf("guard not on")
					}
					return nil
				},
			},
		},
	})
}
