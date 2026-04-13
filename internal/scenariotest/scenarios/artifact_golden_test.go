//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestArtifact_ProjectInitialization validates that a bare repo can be
// initialized with Charter and Constitution, producing a valid project.
// Uses a bare environment (no seeded governance) to test from scratch.
//
// Scenario: Initialize a project with governance documents
//   Given a bare repository with no seeded governance
//   When a Charter artifact is created
//     And a Constitution artifact is created with status "Foundational"
//     And projections are synced
//   Then the Charter projection should exist with type "Governance" and status "Living Document"
//     And the Constitution projection should exist with status "Foundational"
//     And both files should exist in Git
func TestArtifact_ProjectInitialization(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "project-initialization",
		Description: "Initialize a project with Charter and Constitution governance documents",
		Steps: []engine.Step{
			{
				Name: "create-charter",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/charter.md", engine.ArtifactOpts{
						Title: "Project Charter",
					})
					return nil
				},
			},
			{
				Name: "create-constitution",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/constitution.md", engine.ArtifactOpts{
						Title:  "Project Constitution",
						Status: "Foundational",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			{
				Name: "verify-governance-artifacts",
				Action: func(sc *engine.ScenarioContext) error {
					// Charter
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, "governance/charter.md")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, "governance/charter.md", "Title", "Project Charter")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, "governance/charter.md", "ArtifactType", "Governance")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, "governance/charter.md", "Status", "Living Document")

					// Constitution
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, "governance/constitution.md")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, "governance/constitution.md", "Title", "Project Constitution")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, "governance/constitution.md", "Status", "Foundational")

					// Both exist in Git
					assert.FileExists(sc.T, sc.Repo, "governance/charter.md")
					assert.FileExists(sc.T, sc.Repo, "governance/constitution.md")

					return nil
				},
			},
		},
	})
}

// TestArtifact_FullHierarchyCreation validates creating a complete
// Initiative -> Epic -> Task chain with correct linkage.
//
// Scenario: Create a linked Initiative -> Epic -> Task hierarchy
//   Given a seeded governance environment
//   When an Initiative, Epic, and Task are created as a hierarchy
//     And projections are synced
//   Then the Initiative projection should exist with type "Initiative"
//     And the Epic projection should exist with type "Epic"
//     And the Task projection should exist with type "Task" and status "Pending"
//     And the Epic should have a parent link to the Initiative
//     And the Task should have a parent link to the Epic
func TestArtifact_FullHierarchyCreation(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "full-hierarchy-creation",
		Description: "Create linked Initiative -> Epic -> Task hierarchy and verify relationships",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.SyncProjections(),
			{
				Name: "verify-initiative",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("init_path").(string)
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, path)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactType", "Initiative")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactID", "INIT-001")
					assert.FileExists(sc.T, sc.Repo, path)
					return nil
				},
			},
			{
				Name: "verify-epic",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("epic_path").(string)
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, path)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactType", "Epic")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactID", "EPIC-001")
					assert.FileExists(sc.T, sc.Repo, path)
					return nil
				},
			},
			{
				Name: "verify-task",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("task_path").(string)
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, path)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactType", "Task")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactID", "TASK-001")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "Status", "Pending")
					assert.FileExists(sc.T, sc.Repo, path)
					return nil
				},
			},
			{
				Name: "verify-link-relationships",
				Action: func(sc *engine.ScenarioContext) error {
					epicPath := sc.MustGet("epic_path").(string)
					taskPath := sc.MustGet("task_path").(string)
					initPath := sc.MustGet("init_path").(string)

					// Epic links to Initiative
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, epicPath, "parent", initPath)
					// Task links to Epic
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, taskPath, "parent", epicPath)

					return nil
				},
			},
		},
	})
}

// TestArtifact_SchemaValidationPasses validates that all correctly-formed
// artifacts pass schema validation.
//
// Scenario: Valid artifacts pass schema validation
//   Given a seeded environment with validation enabled
//   When a correctly-formed Governance artifact is created
//     And projections are synced
//   Then the artifact should pass validation
func TestArtifact_SchemaValidationPasses(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "schema-validation-passes",
		Description: "Verify that valid artifacts pass the validation engine",
		EnvOpts: append(harness.Seeded(),
			harness.WithRuntimeValidation(),
		),
		Steps: []engine.Step{
			{
				Name: "create-governance-artifact",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/validated.md", engine.ArtifactOpts{
						Title: "Validated Doc",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			{
				Name: "verify-validation-passes",
				Action: func(sc *engine.ScenarioContext) error {
					assert.ArtifactValidationPasses(sc.T, sc.Runtime, sc.Ctx, "governance/validated.md")
					return nil
				},
			},
		},
	})
}
