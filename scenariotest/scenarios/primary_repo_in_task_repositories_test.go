//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/adapters/repository"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/scenariotest/harness"
)

// TestPrimaryRepoInTaskRepositories_AnchorsTASK031 is the scenario
// regression for INIT-022/EPIC-001/TASK-031.
//
// A task whose frontmatter explicitly declares the primary repo in
// `repositories: [spine, ...]` must clear engine.checkRepositoryPreconditions
// and reach branch creation. Before TASK-031 the resolver installed by
// harness.WithCodeRepos held no entry for the primary, so Lookup("spine")
// returned ErrRepositoryNotFound and StartRun failed at the precondition
// gate. The unit-layer pin lives in
// internal/scenariotest/harness/multirepo_internal_test.go
// (TestFixedRepoResolverReturnsPrimary); this scenario is the
// end-to-end counterpart that drives StartRun through the same
// resolver wiring all production scenarios use.
//
// Reverting just the multirepo.go fix and leaving every other change
// in place fails this test at startPrimaryAndCodeRunAndAssertSuccess
// with a repository precondition error wrapping
// repository.ErrRepositoryNotFound for id "spine".
func TestPrimaryRepoInTaskRepositories_AnchorsTASK031(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "primary-repo-in-task-repositories",
		Description: "Task frontmatter explicitly lists the primary repo: StartRun clears the precondition gate and branches both repos",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupPrimaryAndCodeRepoOrchestrator(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				primaryAndCodeRepoWorkflowYAML,
				"seed primary-plus-code task workflow",
			),
			seedPrimaryAndCodeRepoTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startPrimaryAndCodeRunAndAssertSuccess(),
		},
	})
}

const primaryAndCodeRepoWorkflowYAML = `id: task-default
name: Primary-And-Code Repo Workflow
version: "1.0"
status: Active
description: Workflow for a task declaring both the primary repo and a code repo.
applies_to:
  - Task
entry_step: build
steps:
  - id: build
    name: Build
    type: manual
    outcomes:
      - id: completed
        name: Build complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
    timeout: "24h"
`

// primaryAndCodeRepoTaskFrontmatter declares `spine` first in the
// repositories list. AffectedRepositoriesForTask deduplicates "spine"
// from the run record, but the precondition check loops over the raw
// frontmatter list — so this list is exactly what triggers the
// pre-TASK-031 Lookup("spine") path.
const primaryAndCodeRepoTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Primary-And-Code Repo Task"
status: Pending
epic: /initiatives/init-902/epics/epic-902/epic.md
initiative: /initiatives/init-902/initiative.md
repositories:
  - spine
  - billing
links:
  - type: parent
    target: /initiatives/init-902/epics/epic-902/epic.md
---

# Primary-And-Code Repo Task
`

// setupPrimaryAndCodeRepoOrchestrator wires one code repo plus the
// primary onto sc.Runtime.Orchestrator via harness.WithCodeRepos. The
// `sc.Repo` argument is the part TASK-031 added — without it the
// installed resolver would have no entry for the primary repo ID.
func setupPrimaryAndCodeRepoOrchestrator() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-primary-and-code-repo-orchestrator",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
			)
			sc.Set("billing_dir", repos["billing"].Dir)
			return nil
		},
	}
}

func seedPrimaryAndCodeRepoTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-primary-and-code-repo-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-902/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-902"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-902/epics/epic-902/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-902",
				Init: "/initiatives/init-902/initiative.md",
			})
			taskPath := "initiatives/init-902/epics/epic-902/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, primaryAndCodeRepoTaskFrontmatter); err != nil {
				return fmt.Errorf("create primary-and-code-repo task: %w", err)
			}
			sc.Set("task_path", taskPath)
			return nil
		},
	}
}

// startPrimaryAndCodeRunAndAssertSuccess is the inversion of TASK-031's
// bug: StartRun must succeed when the task explicitly declares the
// primary in `repositories:`. Assertions are intentionally narrow —
// AffectedRepositories shape, baseline capture, and Postgres roundtrip
// are pinned by TestMultiRepoRunLifecycle_AnchorsTASK006; this test
// only owns the precondition-clearance signal.
func startPrimaryAndCodeRunAndAssertSuccess() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "start-run-with-primary-in-frontmatter",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			taskPath := sc.MustGet("task_path").(string)
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("StartRun with repositories: [spine, billing]: %w", err)
			}
			run := result.Run
			if run == nil {
				return fmt.Errorf("StartRun returned no run record")
			}
			wantAffected := []string{repository.PrimaryRepositoryID, "billing"}
			if !sameStringSlice(run.AffectedRepositories, wantAffected) {
				return fmt.Errorf("AffectedRepositories: got %v, want %v", run.AffectedRepositories, wantAffected)
			}
			// Branch must exist in both repos. The primary repo lives at
			// sc.Repo.Dir; billing at the dir stashed by setup.
			repoDirs := map[string]string{
				repository.PrimaryRepositoryID: sc.Repo.Dir,
				"billing":                      sc.MustGet("billing_dir").(string),
			}
			for repoID, dir := range repoDirs {
				if err := assertBranchExistsAt(dir, run.BranchName); err != nil {
					return fmt.Errorf("repo %s: %w", repoID, err)
				}
			}
			return nil
		},
	}
}
