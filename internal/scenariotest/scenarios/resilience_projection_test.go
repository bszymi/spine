//go:build scenario

package scenarios_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestProjectionRebuild_MatchesOriginalState validates that projections
// rebuilt from Git history match the original state exactly.
//
// Scenario: Rebuilt projections match original state exactly
//   Given a hierarchy and governance doc with synced projections
//   When the DB is wiped and projections are rebuilt from Git
//   Then all projections should match original titles, statuses, and types
func TestProjectionRebuild_MatchesOriginalState(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rebuild-matches-original",
		Description: "Rebuilt projections match original state exactly",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Build state through normal operations.
			engine.SeedHierarchy("INIT-050", "EPIC-050", "TASK-050"),
			{
				Name: "create-governance-doc",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/rebuild-test.md", engine.ArtifactOpts{
						Title: "Rebuild Test",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Record original state.
			engine.AssertProjection("initiatives/init-050/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection("initiatives/init-050/epics/epic-050/tasks/task-050.md", "Status", "Pending"),
			engine.AssertProjection("governance/rebuild-test.md", "Title", "Rebuild Test"),

			// Drop all projections by cleaning up the database.
			{
				Name: "drop-projections",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return nil
				},
			},

			// Verify projections are gone.
			{
				Name: "verify-projections-cleared",
				Action: func(sc *engine.ScenarioContext) error {
					assert.ArtifactProjectionNotExists(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-050/initiative.md")
					return nil
				},
			},

			// Rebuild projections from Git history.
			engine.SyncProjections(),

			// Verify rebuilt projections match original state.
			engine.AssertProjection("initiatives/init-050/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection("initiatives/init-050/initiative.md", "ArtifactType", "Initiative"),
			engine.AssertProjection("initiatives/init-050/epics/epic-050/epic.md", "Title", "Test Epic"),
			engine.AssertProjection("initiatives/init-050/epics/epic-050/tasks/task-050.md", "Title", "Test Task"),
			engine.AssertProjection("initiatives/init-050/epics/epic-050/tasks/task-050.md", "Status", "Pending"),
			engine.AssertProjection("governance/rebuild-test.md", "Title", "Rebuild Test"),
			engine.AssertProjection("governance/rebuild-test.md", "ArtifactType", "Governance"),
		},
	})
}

// TestProjectionRebuild_IdempotentMultipleRebuilds validates that
// running rebuild multiple times produces identical results.
//
// Scenario: Multiple rebuilds produce identical projection state
//   Given a seeded hierarchy with projections
//   When projections are rebuilt 3 additional times
//   Then the initiative title and task status should be identical each time
func TestProjectionRebuild_IdempotentMultipleRebuilds(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "idempotent-multiple-rebuilds",
		Description: "Multiple rebuilds produce identical projection state",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-051", "EPIC-051", "TASK-051"),
			engine.SyncProjections(),

			// Rebuild 3 times — each should produce identical state.
			engine.SyncProjections(),
			engine.AssertProjection("initiatives/init-051/initiative.md", "Title", "Test Initiative"),

			engine.SyncProjections(),
			engine.AssertProjection("initiatives/init-051/initiative.md", "Title", "Test Initiative"),

			engine.SyncProjections(),
			engine.AssertProjection("initiatives/init-051/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection("initiatives/init-051/epics/epic-051/tasks/task-051.md", "Status", "Pending"),
		},
	})
}

// TestProjectionRebuild_AllArtifactTypesReconstructed validates that
// rebuilding captures all artifact types from Git history.
//
// Scenario: All artifact types are reconstructed from Git on rebuild
//   Given Governance, Architecture, and Initiative artifacts with synced projections
//   When the DB is wiped and projections are rebuilt
//   Then all three artifact types should be present with correct ArtifactType values
func TestProjectionRebuild_AllArtifactTypesReconstructed(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "all-types-reconstructed",
		Description: "All artifact types are reconstructed from Git on rebuild",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Create diverse artifact types.
			{
				Name: "create-all-types",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/rebuild-all-gov.md", engine.ArtifactOpts{
						Title: "Gov Rebuild",
					})
					engine.FixtureArchitecture(sc, "architecture/rebuild-all-arch.md", engine.ArtifactOpts{
						Title: "Arch Rebuild",
					})
					engine.FixtureInitiative(sc, "initiatives/init-052/initiative.md", engine.ArtifactOpts{
						ID: "INIT-052", Title: "Init Rebuild",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Drop and rebuild.
			{
				Name: "drop-and-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},

			// All types reconstructed.
			engine.AssertProjection("governance/rebuild-all-gov.md", "ArtifactType", "Governance"),
			engine.AssertProjection("architecture/rebuild-all-arch.md", "ArtifactType", "Architecture"),
			engine.AssertProjection("initiatives/init-052/initiative.md", "ArtifactType", "Initiative"),
		},
	})
}

// TestProjectionRebuild_RelationshipsPreserved validates that
// parent-child link relationships survive a full projection rebuild.
//
// Scenario: Parent-child links survive full projection rebuild
//   Given a hierarchy with verified parent links
//   When the DB is wiped and projections are rebuilt from Git
//   Then the epic-to-initiative parent link should still exist
func TestProjectionRebuild_RelationshipsPreserved(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "relationships-preserved",
		Description: "Parent-child links survive full projection rebuild",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-053", "EPIC-053", "TASK-053"),
			engine.SyncProjections(),

			// Verify links exist.
			{
				Name: "verify-links-pre-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, epicPath, "parent", initPath)
					return nil
				},
			},

			// Drop and rebuild.
			{
				Name: "drop-and-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Verify links still exist after rebuild.
			{
				Name: "verify-links-post-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, epicPath, "parent", initPath)
					return nil
				},
			},
		},
	})
}

// TestProjectionRebuild_QueriesReturnCorrectResults validates that
// queries against rebuilt projections return correct results.
//
// Scenario: Queries against rebuilt projections return correct results
//   Given a seeded hierarchy with synced projections
//   When the DB is wiped and projections are rebuilt
//   Then querying by path should return the task with title "Test Task"
func TestProjectionRebuild_QueriesReturnCorrectResults(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "queries-after-rebuild",
		Description: "Queries against rebuilt projections return correct results",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-054", "EPIC-054", "TASK-054"),
			engine.SyncProjections(),

			// Drop and rebuild.
			{
				Name: "drop-and-rebuild",
				Action: func(sc *engine.ScenarioContext) error {
					sc.DB.Cleanup(context.Background(), sc.T)
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},

			// Query specific artifact by path — should work after rebuild.
			{
				Name: "query-by-path",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					proj, err := sc.Runtime.Store.GetArtifactProjection(sc.Ctx, taskPath)
					if err != nil {
						sc.T.Errorf("query by path failed: %v", err)
						return nil
					}
					if proj.Title != "Test Task" {
						sc.T.Errorf("expected title 'Test Task', got %q", proj.Title)
					}
					return nil
				},
			},
		},
	})
}
