//go:build scenario

package scenarios_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestReconstruction_MultiHierarchyState validates that a complex
// multi-initiative, multi-epic, multi-task state can be fully
// reconstructed from Git by a fresh runtime instance.
func TestReconstruction_MultiHierarchyState(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "multi-hierarchy-reconstruction",
		Description: "Complex multi-hierarchy state is fully reconstructed from Git",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Build complex state: two initiatives with epics and tasks.
			engine.SeedHierarchy("INIT-060", "EPIC-060", "TASK-060"),
			engine.SeedHierarchy("INIT-061", "EPIC-061", "TASK-061"),
			{
				Name: "create-governance",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/recon-test.md", engine.ArtifactOpts{
						Title: "Reconstruction Test",
					})
					engine.FixtureArchitecture(sc, "architecture/recon-arch.md", engine.ArtifactOpts{
						Title: "Reconstruction Architecture",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Drop all projections — simulate starting from scratch.
			{
				Name: "drop-all-state",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return nil
				},
			},

			// Create a fresh runtime pointing at the same repo and DB.
			{
				Name: "reconstruct-with-fresh-runtime",
				Action: func(sc *engine.ScenarioContext) error {
					rt2 := harness.NewTestRuntime(sc.T, sc.Repo, sc.DB)
					return rt2.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Verify all state is reconstructed.
			engine.AssertProjection("initiatives/init-060/initiative.md", "ArtifactType", "Initiative"),
			engine.AssertProjection("initiatives/init-060/epics/epic-060/epic.md", "ArtifactType", "Epic"),
			engine.AssertProjection("initiatives/init-060/epics/epic-060/tasks/task-060.md", "ArtifactType", "Task"),
			engine.AssertProjection("initiatives/init-061/initiative.md", "ArtifactType", "Initiative"),
			engine.AssertProjection("initiatives/init-061/epics/epic-061/tasks/task-061.md", "ArtifactType", "Task"),
			engine.AssertProjection("governance/recon-test.md", "Title", "Reconstruction Test"),
			engine.AssertProjection("architecture/recon-arch.md", "Title", "Reconstruction Architecture"),
		},
	})
}

// TestReconstruction_LinksIntactAfterRebuild validates that artifact
// links are correctly reconstructed.
func TestReconstruction_LinksIntactAfterRebuild(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "links-intact-after-rebuild",
		Description: "Artifact link relationships survive reconstruction from Git",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-062", "EPIC-062", "TASK-062"),
			engine.SyncProjections(),

			// Drop and reconstruct.
			{
				Name: "drop-and-reconstruct",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					rt2 := harness.NewTestRuntime(sc.T, sc.Repo, sc.DB)
					return rt2.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Verify links are intact.
			{
				Name: "verify-epic-parent-link",
				Action: func(sc *engine.ScenarioContext) error {
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-062/epics/epic-062/epic.md",
						"parent",
						"/initiatives/init-062/initiative.md")
					return nil
				},
			},
			{
				Name: "verify-task-parent-link",
				Action: func(sc *engine.ScenarioContext) error {
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-062/epics/epic-062/tasks/task-062.md",
						"parent",
						"/initiatives/init-062/epics/epic-062/epic.md")
					return nil
				},
			},
		},
	})
}

// TestReconstruction_OperationalAfterRebuild validates that the system
// is fully operational after reconstruction — new artifacts can be
// created and projected.
func TestReconstruction_OperationalAfterRebuild(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "operational-after-rebuild",
		Description: "System is fully operational after Git reconstruction",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Build initial state.
			engine.SeedHierarchy("INIT-063", "EPIC-063", "TASK-063"),
			engine.SyncProjections(),

			// Drop and reconstruct.
			{
				Name: "drop-and-reconstruct",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Create new artifacts AFTER reconstruction.
			{
				Name: "create-new-artifact-post-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/post-rebuild.md", engine.ArtifactOpts{
						Title: "Post-Rebuild Artifact",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Verify both old and new artifacts are accessible.
			engine.AssertProjection("initiatives/init-063/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection("governance/post-rebuild.md", "Title", "Post-Rebuild Artifact"),
		},
	})
}

// TestReconstruction_GovernanceDocsReconstructed validates that seeded
// governance documents (charter, schema, etc.) survive reconstruction.
func TestReconstruction_GovernanceDocsReconstructed(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "governance-docs-reconstructed",
		Description: "Seeded governance documents survive full reconstruction",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SyncProjections(),

			// Verify seeded governance docs exist.
			engine.AssertProjection("governance/charter.md", "ArtifactType", "Governance"),

			// Drop and reconstruct.
			{
				Name: "drop-and-reconstruct",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Governance docs reconstructed from Git.
			engine.AssertProjection("governance/charter.md", "ArtifactType", "Governance"),
		},
	})
}
