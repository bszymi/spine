//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
)

// Scenario: Create a Governance artifact end-to-end
//   Given a bare repository
//   When a Governance artifact is created via the service at "governance/spike-charter.md"
//     And projections are synced
//   Then the file should exist in Git with content "Scenario Test Charter"
//     And the projection should exist with title "Scenario Test Charter"
//     And the projection type should be "Governance" with status "Living Document"
func TestArtifact_CreateGovernanceArtifact(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "create-governance-artifact",
		Description: "Creates a Governance artifact via the service, syncs to DB, and verifies both layers",
		Steps: []engine.Step{
			{
				Name: "create-artifact-via-service",
				Action: func(sc *engine.ScenarioContext) error {
					content := `---
type: Governance
title: Scenario Test Charter
status: Living Document
version: "0.1"
---

# Scenario Test Charter

This artifact was created by the scenario testing spike.
`
					result, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/spike-charter.md", content)
					if err != nil {
						return fmt.Errorf("create artifact: %w", err)
					}
					sc.Set("artifact_path", result.Artifact.Path)
					return nil
				},
			},
			{
				Name: "sync-projections",
				Action: func(sc *engine.ScenarioContext) error {
					return sc.Runtime.Projections.FullRebuild(sc.Ctx)
				},
			},
			{
				Name: "verify-artifact-in-git-and-database",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("artifact_path").(string)

					// Git-layer assertions
					assert.FileExists(sc.T, sc.Repo, path)
					assert.FileContains(sc.T, sc.Repo, path, "Scenario Test Charter")

					// DB-layer assertions
					assert.ArtifactProjectionExists(sc.T, sc.DB, sc.Ctx, path)
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "Title", "Scenario Test Charter")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "ArtifactType", "Governance")
					assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, "Status", "Living Document")

					return nil
				},
			},
		},
	})
}
