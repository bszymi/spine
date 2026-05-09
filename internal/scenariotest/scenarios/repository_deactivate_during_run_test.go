//go:build scenario

package scenarios_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/repository"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/store"
)

// State keys used to thread the bespoke deactivate-flow wiring between
// scenario steps. The bundle in stateDeactivateEnv carries the
// repository.Manager + Registry + gateway client built specifically for
// this scenario; it is intentionally separate from sc.Runtime so the
// shared orchestrator harness does not need to grow a Manager dependency.
const (
	stateDeactivateEnv  = "deactivate_env"
	stateDeactivateRun  = "deactivate_run_id"
	stateBillingRepoDir = "deactivate_billing_dir"
)

// repoDeactivateEnv bundles the repository-management surface the
// deactivate scenarios drive. The orchestrator already lives at
// sc.Runtime.Orchestrator and reads code-repo paths from the in-memory
// resolver harness.WithCodeRepos installs; the Manager + Registry + HTTP
// gateway built here exist only to exercise the
// /api/v1/repositories/{id}/deactivate endpoint against the same
// runtime.repositories binding rows the orchestrator's runs read.
type repoDeactivateEnv struct {
	WorkspaceID string
	Catalog     *repository.InMemoryCatalogStore
	Manager     *repository.Manager
	Registry    *repository.Registry
	OpClient    *cli.Client
	Server      *httptest.Server
}

// strictRunReferenceChecker is the regression bait the AC mutation case
// pins. It always reports an active run, so swapping it in for the v0.x
// NopRunReferenceChecker default forces Manager.Deactivate to surface
// the precondition_failed sentinel. The NoOp variant is the production
// default — without that contract this scenario would be testing
// nothing, so the strict-checker test is what proves the scenario is
// load-bearing.
type strictRunReferenceChecker struct{}

func (strictRunReferenceChecker) AnyActiveRunReferences(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

// deactivateWorkflowYAML defines a single-step manual workflow whose
// entry step is explicitly routed at billing. Run start fans the run
// branch out to billing's working tree and parks the run in the
// `active` status pending a step result that the scenario never
// supplies — so when the deactivate API call lands the run is
// guaranteed to be in flight.
const deactivateWorkflowYAML = `id: task-default
name: Deactivate-During-Run Test Workflow
version: "1.0"
status: Active
description: Workflow used by the repository-deactivate-during-active-run scenario.
applies_to:
  - Task
entry_step: build
steps:
  - id: build
    name: Build in code repo
    repository: billing
    type: manual
    outcomes:
      - id: completed
        name: Build complete
        next_step: end
        commit:
          status: Completed
    timeout: "4h"
`

// deactivateTaskFrontmatter declares billing in the task's
// `repositories:` list — required for the workflow's `repository:
// billing` entry-step routing to validate as opted-in per ADR-015.
const deactivateTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Deactivate-During-Run Task"
status: Pending
epic: /initiatives/init-902/epics/epic-902/epic.md
initiative: /initiatives/init-902/initiative.md
repositories:
  - billing
links:
  - type: parent
    target: /initiatives/init-902/epics/epic-902/epic.md
---

# Deactivate-During-Run Task
`

// TestRepositoryDeactivate_NopChecker_AllowsDeactivationDuringActiveRun
// pins the v0.x permissive contract from
// internal/repository/manager.go: NopRunReferenceChecker is the
// constructor default, so a deactivation request is accepted even when
// an active run still references the repo. This is a deliberate
// deferral until per-run repository metadata grows a strict reference
// checker (INIT-014 deferral list); without this scenario, a future
// "real" RunReferenceChecker could land silently and change behavior.
//
// The companion test below (StrictChecker_RefusesDuringActiveRun)
// proves the same scenario shape catches the regression by exercising
// the alternative checker, so a future commit that swaps the default
// would have to update both tests in lockstep.
//
// AC anchors:
//   - Deactivation succeeds (HTTP 200).
//   - The still-open run is unaffected — the runtime.runs row stays
//     `active` and re-reads cleanly.
//   - Registry.Lookup against billing now returns
//     ErrRepositoryInactive — new operations get the inactive sentinel
//     while the in-flight run continues with its already-resolved
//     binding.
func TestRepositoryDeactivate_NopChecker_AllowsDeactivationDuringActiveRun(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "repo-deactivate-nop-checker-allows",
		Description: "v0.x permissive deactivation contract: NopRunReferenceChecker lets deactivate succeed during an active run.",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			// nil -> NewManager substitutes NopRunReferenceChecker{} via
			// its constructor default. Passing the no-op checker
			// explicitly here would mask a regression where a future
			// commit changes NewManager's default to a strict checker,
			// which is exactly the contract this scenario claims to lock
			// in. Routing through the constructor default keeps the
			// test load-bearing on the real production wiring.
			setupDeactivateEnv("ws-deactivate-nop", "op-deactivate-nop", "reader-deactivate-nop", nil),
			setupBillingCodeRepo(),
			scenarioEngine.WriteAndCommit("workflows/task-default.yaml", deactivateWorkflowYAML, "seed deactivate workflow"),
			seedDeactivateTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			registerBillingViaAPI(),
			startRunReferencingBilling(),
			deactivateBillingViaAPIExpectingSuccess(),
			assertRunStillActive(),
			assertRegistryReportsBillingInactive(),
		},
	})
}

// TestRepositoryDeactivate_StrictChecker_RefusesDuringActiveRun is the
// AC mutation target. A strict RunReferenceChecker that reports any
// active run blocks deactivation with domain.ErrPrecondition; this
// test proves the same scenario shape catches a regression where a
// future strict checker is wired into the Manager. If that ever lands
// without updating Manager.NewManager's default, both this test and
// the NopChecker variant above must change in lockstep.
//
// We bypass the HTTP layer for the assertion because cli.Client surfaces
// non-2xx responses as an opaque "API error (412): ..." string. The
// gateway handler is a thin pass-through to mgr.Deactivate, so calling
// the manager directly preserves the typed *domain.SpineError sentinel
// without losing any meaningful coverage — the API path is exercised
// in the NopChecker variant.
func TestRepositoryDeactivate_StrictChecker_RefusesDuringActiveRun(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "repo-deactivate-strict-checker-refuses",
		Description: "Mutation target: a strict RunReferenceChecker refuses deactivation with precondition_failed while a run is active.",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupDeactivateEnv("ws-deactivate-strict", "op-deactivate-strict", "reader-deactivate-strict", strictRunReferenceChecker{}),
			setupBillingCodeRepo(),
			scenarioEngine.WriteAndCommit("workflows/task-default.yaml", deactivateWorkflowYAML, "seed deactivate workflow"),
			seedDeactivateTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			registerBillingViaAPI(),
			startRunReferencingBilling(),
			deactivateBillingExpectingPreconditionViaManager(),
			assertBillingStillActive(),
			assertRunStillActive(),
		},
	})
}

// setupDeactivateEnv builds the Manager + Registry + gateway client
// bundle bound to sc.Runtime.Store and stashes it under
// stateDeactivateEnv. Each scenario passes its own actor names so the
// two tests do not collide on auth.actors PK when the suite shares a
// single test database.
//
// codeRepoBase="" disables the Manager's local-path containment check;
// the scenario uses a synthetic /var/spine/... path because the
// orchestrator's resolver is the in-memory fixedRepoResolver from
// harness.WithCodeRepos rather than the binding row, so the path string
// is purely metadata for the deactivation flow.
func setupDeactivateEnv(workspaceID, opActor, readerActor string, runs repository.RunReferenceChecker) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-deactivate-env-" + workspaceID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			primary := repository.PrimarySpec{
				Name:          "Deactivate Spine",
				DefaultBranch: "main",
				LocalPath:     "/var/spine/workspaces/deactivate/spine",
			}
			cat := repository.NewInMemoryCatalogStore(primary)
			mgr := repository.NewManager(workspaceID, primary, cat, sc.Runtime.Store, runs, "")
			reg := repository.New(workspaceID, primary, func(ctx context.Context) (*repository.Catalog, error) {
				return cat.Load(ctx)
			}, sc.Runtime.Store)

			authSvc := auth.NewService(sc.Runtime.Store)
			ctx := context.Background()
			if err := sc.Runtime.Store.CreateActor(ctx, &domain.Actor{
				ActorID: opActor,
				Type:    domain.ActorTypeHuman,
				Name:    opActor,
				Role:    domain.RoleOperator,
				Status:  domain.ActorStatusActive,
			}); err != nil {
				return fmt.Errorf("create operator actor: %w", err)
			}
			if err := sc.Runtime.Store.CreateActor(ctx, &domain.Actor{
				ActorID: readerActor,
				Type:    domain.ActorTypeHuman,
				Name:    readerActor,
				Role:    domain.RoleReader,
				Status:  domain.ActorStatusActive,
			}); err != nil {
				return fmt.Errorf("create reader actor: %w", err)
			}
			opTok, _, err := authSvc.CreateToken(ctx, opActor, "scenario", nil)
			if err != nil {
				return fmt.Errorf("create operator token: %w", err)
			}

			srv := gateway.NewServer(":0", gateway.ServerConfig{
				Store:             sc.Runtime.Store,
				Auth:              authSvc,
				RepositoryManager: mgr,
			})
			ts := httptest.NewServer(srv.Handler())
			// Anchor server cleanup to ParentT so it survives the
			// per-step subtest scope — without this, ts.Close fires when
			// setupDeactivateEnv's subtest ends and the next step sees a
			// closed connection.
			sc.ParentT.Cleanup(ts.Close)

			sc.Set(stateDeactivateEnv, &repoDeactivateEnv{
				WorkspaceID: workspaceID,
				Catalog:     cat,
				Manager:     mgr,
				Registry:    reg,
				OpClient:    cli.NewClient(ts.URL, opTok),
				Server:      ts,
			})
			return nil
		},
	}
}

// setupBillingCodeRepo wires a single on-disk billing code repo into
// sc.Runtime.Orchestrator's resolver via harness.WithCodeRepos. The
// orchestrator needs this for run start to fan the run branch out to
// billing's working tree; the scenario's deactivate API call operates
// on the separate runtime.repositories binding row, so the in-memory
// resolver here and the persisted binding row co-exist.
func setupBillingCodeRepo() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-billing-code-repo",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator,
				harness.CodeRepoSpec{ID: "billing"},
			)
			sc.Set(stateBillingRepoDir, repos["billing"].Dir)
			return nil
		},
	}
}

// seedDeactivateTaskHierarchy seeds Initiative + Epic via the standard
// fixture builders, then writes the Task artifact directly so the
// frontmatter can carry `repositories: [billing]` (a field FixtureTask
// does not expose).
func seedDeactivateTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-deactivate-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-902/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-902"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-902/epics/epic-902/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-902",
				Init: "/initiatives/init-902/initiative.md",
			})
			taskPath := "initiatives/init-902/epics/epic-902/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, deactivateTaskFrontmatter); err != nil {
				return fmt.Errorf("create deactivate task: %w", err)
			}
			sc.Set("task_path", taskPath)
			return nil
		},
	}
}

// registerBillingViaAPI exercises the operator-side registration path
// through the same handler the deactivate test will reuse. After this
// step the runtime.repositories table has an active billing binding the
// Registry can resolve.
func registerBillingViaAPI() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "register-billing-via-api",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateDeactivateEnv).(*repoDeactivateEnv)
			body := map[string]string{
				"id":             "billing",
				"name":           "Billing",
				"default_branch": "main",
				"clone_url":      "https://example.com/billing.git",
				"local_path":     "/var/spine/workspaces/deactivate/repos/billing",
			}
			if _, err := env.OpClient.Post(sc.Ctx, "/api/v1/repositories", body); err != nil {
				return fmt.Errorf("register billing: %w", err)
			}
			// Sanity: the registry must see it as active before the run
			// starts, so a later "inactive" assertion proves a real
			// transition rather than a missing binding.
			got, err := env.Registry.Lookup(sc.Ctx, "billing")
			if err != nil {
				return fmt.Errorf("post-register lookup: %w", err)
			}
			if !got.IsActive() {
				return fmt.Errorf("post-register lookup: status=%q, want active", got.Status)
			}
			return nil
		},
	}
}

// startRunReferencingBilling drives Orchestrator.StartRun and verifies
// the run lands in the active state with billing in its
// AffectedRepositories — both required for the deactivate-during-run
// contract this scenario asserts.
func startRunReferencingBilling() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "start-run-referencing-billing",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			taskPath := sc.MustGet("task_path").(string)
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("StartRun: %w", err)
			}
			run := result.Run
			if run.Status != domain.RunStatusActive {
				return fmt.Errorf("post-start run status: got %q, want %q", run.Status, domain.RunStatusActive)
			}
			foundBilling := false
			for _, id := range run.AffectedRepositories {
				if id == "billing" {
					foundBilling = true
					break
				}
			}
			if !foundBilling {
				return fmt.Errorf("AffectedRepositories=%v, want billing included", run.AffectedRepositories)
			}
			sc.Set(stateDeactivateRun, run.RunID)
			return nil
		},
	}
}

// deactivateBillingViaAPIExpectingSuccess locks the v0.x permissive
// contract: with the no-op checker (constructor default), the
// deactivate handler returns 200 even though billing has an active run.
// We assert on the response body to catch a regression where the
// handler accepts the request but persists the wrong status.
func deactivateBillingViaAPIExpectingSuccess() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "deactivate-billing-via-api-expecting-success",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateDeactivateEnv).(*repoDeactivateEnv)
			body, err := env.OpClient.Post(sc.Ctx, "/api/v1/repositories/billing/deactivate", nil)
			if err != nil {
				return fmt.Errorf("deactivate billing: %w", err)
			}
			got := decodeRepo(sc.T, body)
			if got["status"] != "inactive" {
				return fmt.Errorf("deactivate response status: got %v, want inactive", got["status"])
			}
			return nil
		},
	}
}

// deactivateBillingExpectingPreconditionViaManager calls
// Manager.Deactivate directly so the test can assert on the typed
// *domain.SpineError sentinel. The HTTP layer is a thin pass-through
// to this same call (verified in the NopChecker test), so direct
// invocation does not lose coverage that matters — it gains the
// ability to inspect the error code.
func deactivateBillingExpectingPreconditionViaManager() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "deactivate-billing-expecting-precondition-via-manager",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateDeactivateEnv).(*repoDeactivateEnv)
			err := env.Manager.Deactivate(sc.Ctx, "billing")
			if err == nil {
				return fmt.Errorf("Deactivate succeeded; want %q", domain.ErrPrecondition)
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) {
				return fmt.Errorf("Deactivate error not *domain.SpineError: %T (%v)", err, err)
			}
			if spineErr.Code != domain.ErrPrecondition {
				return fmt.Errorf("Deactivate error code: got %q, want %q (msg=%q)",
					spineErr.Code, domain.ErrPrecondition, spineErr.Message)
			}
			// The message should reference the active-runs reason so
			// operators see why deactivation refused; pin the substring
			// the manager produces today.
			if !strings.Contains(spineErr.Message, "active runs referencing it") {
				return fmt.Errorf("Deactivate error message: got %q, want to contain %q",
					spineErr.Message, "active runs referencing it")
			}
			return nil
		},
	}
}

// assertRunStillActive re-reads the run row through the public store
// API after the deactivate step. Either contract — permissive
// deactivate (NopChecker) or refused deactivate (StrictChecker) — must
// leave the run untouched.
func assertRunStillActive() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-still-active",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet(stateDeactivateRun).(string)
			got, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("GetRun(%s): %w", runID, err)
			}
			if got.Status != domain.RunStatusActive {
				return fmt.Errorf("run %s status: got %q, want %q", runID, got.Status, domain.RunStatusActive)
			}
			return nil
		},
	}
}

// assertRegistryReportsBillingInactive locks the soft-deactivate
// contract: after a successful deactivate, Registry.Lookup must surface
// the inactive sentinel so any new resolution attempt fails closed
// while the in-flight run continues with its already-resolved binding.
func assertRegistryReportsBillingInactive() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-registry-reports-billing-inactive",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateDeactivateEnv).(*repoDeactivateEnv)
			_, err := env.Registry.Lookup(sc.Ctx, "billing")
			if !errors.Is(err, repository.ErrRepositoryInactive) {
				return fmt.Errorf("Registry.Lookup(billing) after deactivate: got %v, want ErrRepositoryInactive", err)
			}
			return nil
		},
	}
}

// assertBillingStillActive is the strict-checker companion to the
// inactive-after-success assertion: when Deactivate refuses, the
// binding row must still be active so the registry continues to serve
// resolutions normally. A regression that rolled the row inactive
// before the precondition check (or after, but didn't roll back) would
// surface here.
func assertBillingStillActive() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-billing-still-active",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateDeactivateEnv).(*repoDeactivateEnv)
			got, err := env.Registry.Lookup(sc.Ctx, "billing")
			if err != nil {
				return fmt.Errorf("Registry.Lookup(billing) after refused deactivate: %w", err)
			}
			if !got.IsActive() {
				return fmt.Errorf("billing binding status after refused deactivate: got %q, want active", got.Status)
			}
			// Also re-read the binding row directly so a regression that
			// silently flipped the column but left the joined view active
			// would still surface.
			binding, err := sc.Runtime.Store.GetRepositoryBinding(sc.Ctx, env.WorkspaceID, "billing")
			if err != nil {
				return fmt.Errorf("GetRepositoryBinding: %w", err)
			}
			if binding.Status != store.RepositoryBindingStatusActive {
				return fmt.Errorf("binding row status: got %q, want %q",
					binding.Status, store.RepositoryBindingStatusActive)
			}
			return nil
		},
	}
}
