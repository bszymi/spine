//go:build scenario

package engine_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

func TestFixtureGovernance(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "fixture-governance",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-governance",
				Action: func(sc *engine.ScenarioContext) error {
					path := engine.FixtureGovernance(sc, "governance/test-charter.md", engine.ArtifactOpts{
						Title: "Test Charter",
					})
					sc.Set("path", path)
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("governance/test-charter.md", "Title", "Test Charter"),
			engine.AssertProjection("governance/test-charter.md", "ArtifactType", "Governance"),
		},
	})
}

func TestFixtureInitiative(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "fixture-initiative",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-initiative",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureInitiative(sc, "initiatives/init-001/initiative.md", engine.ArtifactOpts{
						ID:    "INIT-001",
						Title: "Test Initiative",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("initiatives/init-001/initiative.md", "Title", "Test Initiative"),
			engine.AssertProjection("initiatives/init-001/initiative.md", "ArtifactType", "Initiative"),
		},
	})
}

func TestFixtureHierarchy(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "fixture-hierarchy",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.SyncProjections(),
			{
				Name: "verify-hierarchy",
				Action: func(sc *engine.ScenarioContext) error {
					initPath := sc.MustGet("init_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					taskPath := sc.MustGet("task_path").(string)

					engine.AssertFileExists(initPath).Action(sc)
					engine.AssertFileExists(epicPath).Action(sc)
					engine.AssertFileExists(taskPath).Action(sc)

					engine.AssertProjection(initPath, "ArtifactType", "Initiative").Action(sc)
					engine.AssertProjection(epicPath, "ArtifactType", "Epic").Action(sc)
					engine.AssertProjection(taskPath, "ArtifactType", "Task").Action(sc)
					return nil
				},
			},
		},
	})
}

func TestFixtureWithOverrides(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "fixture-overrides",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-with-custom-title",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/custom.md", engine.ArtifactOpts{
						Title:  "Custom Title",
						Status: "Living Document",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("governance/custom.md", "Title", "Custom Title"),
			engine.AssertProjection("governance/custom.md", "Status", "Living Document"),
		},
	})
}
