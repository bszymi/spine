//go:build scenario

package engine_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

const testGovernanceArtifact = `---
type: Governance
title: Step Test Doc
status: Draft
---

# Step Test Doc
`

func TestCreateArtifactStep(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "create-artifact-step",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			engine.CreateArtifact("governance/step-test.md", testGovernanceArtifact, "artifact_path"),
			{
				Name: "verify-state-key",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("artifact_path").(string)
					if path != "governance/step-test.md" {
						return fmt.Errorf("expected governance/step-test.md, got %s", path)
					}
					return nil
				},
			},
		},
	})
}

func TestSyncAndAssertSteps(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "sync-and-assert-steps",
		EnvOpts: harness.Seeded(),
		Steps: engine.Steps(
			engine.CommonSetup("governance/assert-test.md", testGovernanceArtifact, "path"),
			[]engine.Step{
				engine.AssertFileExists("governance/assert-test.md"),
				engine.AssertFileContains("governance/assert-test.md", "Step Test Doc"),
				engine.AssertProjection("governance/assert-test.md", "Title", "Step Test Doc"),
				engine.AssertProjection("governance/assert-test.md", "ArtifactType", "Governance"),
			},
		),
	})
}

func TestExpectErrorStep(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "expect-error-step",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			engine.CreateArtifact("governance/dup-test.md", testGovernanceArtifact, "path"),
			engine.ExpectError("duplicate-create", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/dup-test.md", testGovernanceArtifact)
				return err
			}, "error_msg"),
			{
				Name: "verify-error-captured",
				Action: func(sc *engine.ScenarioContext) error {
					msg := sc.MustGet("error_msg").(string)
					if msg == "" {
						return fmt.Errorf("expected non-empty error message")
					}
					return nil
				},
			},
		},
	})
}

func TestComposeStep(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:    "compose-step",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			engine.Compose("create-and-sync",
				engine.CreateArtifact("governance/compose-test.md", testGovernanceArtifact, "path"),
				engine.SyncProjections(),
			),
			engine.AssertProjection("governance/compose-test.md", "Title", "Step Test Doc"),
		},
	})
}

func TestStepsConcatenation(t *testing.T) {
	setup := []engine.Step{
		engine.CreateArtifact("governance/concat-test.md", testGovernanceArtifact, "path"),
		engine.SyncProjections(),
	}
	verify := []engine.Step{
		engine.AssertFileExists("governance/concat-test.md"),
		engine.AssertProjection("governance/concat-test.md", "Status", "Draft"),
	}

	combined := engine.Steps(setup, verify)
	if len(combined) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(combined))
	}
}
