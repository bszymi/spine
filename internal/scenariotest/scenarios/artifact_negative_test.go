//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestArtifact_RejectsMissingRequiredFields validates that artifacts with
// missing required fields (type, title, status) are rejected.
func TestArtifact_RejectsMissingRequiredFields(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-missing-required-fields",
		Description: "Artifacts missing required frontmatter fields are rejected",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.ExpectError("missing-type", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/no-type.md", `---
title: No Type
status: Living Document
---
# No Type
`)
				return err
			}, ""),
			engine.ExpectError("missing-title", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/no-title.md", `---
type: Governance
status: Living Document
---
# No Title
`)
				return err
			}, ""),
			engine.ExpectError("missing-status", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/no-status.md", `---
type: Governance
title: No Status
---
# No Status
`)
				return err
			}, ""),
			{
				Name: "verify-no-partial-artifacts",
				Action: func(sc *engine.ScenarioContext) error {
					assert.FileNotExists(sc.T, sc.Repo, "governance/no-type.md")
					assert.FileNotExists(sc.T, sc.Repo, "governance/no-title.md")
					assert.FileNotExists(sc.T, sc.Repo, "governance/no-status.md")
					return nil
				},
			},
		},
	})
}

// TestArtifact_RejectsInvalidStatus validates that artifacts with an invalid
// status for their type are rejected.
func TestArtifact_RejectsInvalidStatus(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-invalid-status",
		Description: "Artifacts with invalid status for their type are rejected",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.ExpectError("governance-with-pending-status", func(sc *engine.ScenarioContext) error {
				// Governance only allows: Living Document, Foundational, Superseded
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/bad-status.md", `---
type: Governance
title: Bad Status
status: Pending
---
# Bad Status
`)
				return err
			}, ""),
		},
	})
}

// TestArtifact_RejectsDuplicatePath validates that creating an artifact at
// an existing path is rejected.
func TestArtifact_RejectsDuplicatePath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-duplicate-path",
		Description: "Creating an artifact at an existing path is rejected",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-original",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/duplicate-test.md", engine.ArtifactOpts{
						Title: "Original",
					})
					return nil
				},
			},
			engine.ExpectError("create-duplicate", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/duplicate-test.md", `---
type: Governance
title: Duplicate
status: Living Document
---
# Duplicate
`)
				return err
			}, "error_msg"),
			{
				Name: "verify-original-unchanged",
				Action: func(sc *engine.ScenarioContext) error {
					assert.FileContains(sc.T, sc.Repo, "governance/duplicate-test.md", "Original")
					return nil
				},
			},
		},
	})
}
