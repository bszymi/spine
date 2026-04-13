//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestRecovery_ProjectionsRecoveredAfterRebuild validates that after
// creating artifacts and syncing, a full projection rebuild recovers
// all projections identically.
//
// Scenario: Full projection rebuild recovers all artifact projections
//   Given a seeded hierarchy INIT-040 -> EPIC-040 -> TASK-040 with synced projections
//   When projections are fully rebuilt
//   Then all three artifact projections should still exist with correct titles
func TestRecovery_ProjectionsRecoveredAfterRebuild(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "projections-recovered-after-rebuild",
		Description: "Full projection rebuild recovers all artifact projections",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Create a hierarchy and sync.
			engine.SeedHierarchy("INIT-040", "EPIC-040", "TASK-040"),
			engine.SyncProjections(),

			// Verify projections exist.
			engine.AssertProjection(
				"initiatives/init-040/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection(
				"initiatives/init-040/epics/epic-040/epic.md", "Title", "Test Epic"),
			engine.AssertProjection(
				"initiatives/init-040/epics/epic-040/tasks/task-040.md", "Title", "Test Task"),

			// Simulate recovery: rebuild projections from scratch.
			// This is what happens when a new runtime starts.
			engine.SyncProjections(),

			// Verify projections are identical after rebuild.
			engine.AssertProjection(
				"initiatives/init-040/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection(
				"initiatives/init-040/epics/epic-040/epic.md", "Title", "Test Epic"),
			engine.AssertProjection(
				"initiatives/init-040/epics/epic-040/tasks/task-040.md", "Title", "Test Task"),
		},
	})
}

// TestRecovery_NewRuntimeRecoversState validates that a new runtime
// instance wired to the same repo and database sees the same state.
//
// Scenario: A new runtime instance recovers state from the same repo and DB
//   Given artifacts created and synced using the original runtime
//   When a second runtime is wired to the same repo and DB and rebuilds projections
//   Then all artifact projections should be visible and fields preserved
func TestRecovery_NewRuntimeRecoversState(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "new-runtime-recovers-state",
		Description: "A new runtime instance recovers state from the same repo and DB",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Create artifacts and sync with the original runtime.
			engine.SeedHierarchy("INIT-041", "EPIC-041", "TASK-041"),
			{
				Name: "create-governance-artifact",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/recovery-test.md", engine.ArtifactOpts{
						Title: "Recovery Test Doc",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Create a second runtime from the same repo and DB.
			// This simulates a fresh process start.
			{
				Name: "verify-with-new-runtime",
				Action: func(sc *engine.ScenarioContext) error {
					rt2 := harness.NewTestRuntime(sc.T, sc.Repo, sc.DB)

					// Rebuild projections using the new runtime.
					if err := rt2.Projections.FullRebuild(sc.Ctx); err != nil {
						return err
					}

					// Verify the new runtime can read all projections.
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-041/initiative.md")
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-041/epics/epic-041/epic.md")
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-041/epics/epic-041/tasks/task-041.md")
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx,
						"governance/recovery-test.md")

					// Verify field values are preserved.
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						"governance/recovery-test.md", "Title", "Recovery Test Doc")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						"initiatives/init-041/initiative.md", "ArtifactType", "Initiative")

					return nil
				},
			},
		},
	})
}

// TestRecovery_DeterministicRebuild validates that repeated projection
// rebuilds produce identical results.
//
// Scenario: Repeated projection rebuilds produce identical results
//   Given a seeded hierarchy with synced projections
//   When projections are rebuilt three times
//   Then the task title and status should be identical after each rebuild
func TestRecovery_DeterministicRebuild(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "deterministic-rebuild",
		Description: "Repeated projection rebuilds produce identical results",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-042", "EPIC-042", "TASK-042"),
			engine.SyncProjections(),

			// Record state after first build.
			{
				Name: "record-first-build",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Title", "Test Task")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Status", "Pending")
					return nil
				},
			},

			// Rebuild twice more and verify determinism.
			engine.SyncProjections(),
			{
				Name: "verify-second-build",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Title", "Test Task")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Status", "Pending")
					return nil
				},
			},
			engine.SyncProjections(),
			{
				Name: "verify-third-build",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Title", "Test Task")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx,
						taskPath, "Status", "Pending")
					return nil
				},
			},
		},
	})
}

// TestRecovery_GitAsSourceOfTruth validates that Git is the source of
// truth by modifying an artifact in Git and verifying the projection
// updates on rebuild.
//
// Scenario: Projections follow Git state, not stale DB state
//   Given an artifact with title "Original Title" and synced projection
//   When the artifact file is updated directly in Git to "Updated Title"
//     And projections are rebuilt
//   Then the projection should reflect the new title "Updated Title"
func TestRecovery_GitAsSourceOfTruth(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "git-as-source-of-truth",
		Description: "Projections follow Git state, not stale DB state",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-artifact",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/truth-test.md", engine.ArtifactOpts{
						Title: "Original Title",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("governance/truth-test.md", "Title", "Original Title"),

			// Modify the artifact directly in Git (bypassing service).
			engine.WriteAndCommit("governance/truth-test.md", `---
type: Governance
title: "Updated Title"
status: Living Document
---

# Updated Title
`, "update governance doc"),

			// Rebuild projections — should reflect Git state.
			engine.SyncProjections(),
			engine.AssertProjection("governance/truth-test.md", "Title", "Updated Title"),
		},
	})
}

// TestRecovery_NoDataLossOnRebuild validates that projections for
// all artifact types survive a full rebuild.
//
// Scenario: All artifact types survive a full projection rebuild
//   Given Governance, Architecture, and Initiative artifacts with synced projections
//   When projections are fully rebuilt
//   Then all three projections should still have correct ArtifactType values
func TestRecovery_NoDataLossOnRebuild(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "no-data-loss-on-rebuild",
		Description: "All artifact types survive a full projection rebuild",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// Create multiple artifact types.
			{
				Name: "create-diverse-artifacts",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/rebuild-gov.md", engine.ArtifactOpts{
						Title: "Rebuild Governance",
					})
					engine.FixtureArchitecture(sc, "architecture/rebuild-arch.md", engine.ArtifactOpts{
						Title: "Rebuild Architecture",
					})
					engine.FixtureInitiative(sc, "initiatives/init-043/initiative.md", engine.ArtifactOpts{
						ID:    "INIT-043",
						Title: "Rebuild Initiative",
					})
					return nil
				},
			},
			engine.SyncProjections(),

			// Verify all exist.
			engine.AssertProjection("governance/rebuild-gov.md", "Title", "Rebuild Governance"),
			engine.AssertProjection("architecture/rebuild-arch.md", "Title", "Rebuild Architecture"),
			engine.AssertProjection("initiatives/init-043/initiative.md", "Title", "Rebuild Initiative"),

			// Full rebuild — verify no data loss.
			engine.SyncProjections(),
			engine.AssertProjection("governance/rebuild-gov.md", "ArtifactType", "Governance"),
			engine.AssertProjection("architecture/rebuild-arch.md", "ArtifactType", "Architecture"),
			engine.AssertProjection("initiatives/init-043/initiative.md", "ArtifactType", "Initiative"),
		},
	})
}
