//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestArtifact_ParentChildLinkConsistency validates that parent links
// between Initiative->Epic->Task are correctly projected.
func TestArtifact_ParentChildLinkConsistency(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "parent-child-link-consistency",
		Description: "Verify parent/child links are correctly stored in projections",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.SeedHierarchy("INIT-002", "EPIC-002", "TASK-002"),
			engine.SyncProjections(),
			{
				Name: "verify-epic-parent-link",
				Action: func(sc *engine.ScenarioContext) error {
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, epicPath, "parent", initPath)
					return nil
				},
			},
			{
				Name: "verify-task-parent-link",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					assert.ArtifactHasLink(sc.T, sc.DB, sc.Ctx, taskPath, "parent", epicPath)
					return nil
				},
			},
		},
	})
}

// TestArtifact_RejectsInvalidLinkType validates that artifacts with
// unknown link types are rejected during creation.
func TestArtifact_RejectsInvalidLinkType(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-invalid-link-type",
		Description: "Artifacts with invalid link types are rejected",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.ExpectError("invalid-link-type", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/bad-link.md", `---
type: Governance
title: Bad Link
status: Living Document
links:
  - type: invalid_type
    target: /governance/charter.md
---

# Bad Link
`)
				return err
			}, ""),
		},
	})
}

// TestArtifact_RejectsNonCanonicalLinkTarget validates that link targets
// must use canonical paths (starting with /).
func TestArtifact_RejectsNonCanonicalLinkTarget(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-non-canonical-link-target",
		Description: "Link targets must use canonical paths starting with /",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.ExpectError("relative-link-target", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/relative-link.md", `---
type: Governance
title: Relative Link
status: Living Document
links:
  - type: parent
    target: governance/charter.md
---

# Relative Link
`)
				return err
			}, ""),
		},
	})
}

// TestArtifact_ValidLinkTypes validates that all allowed link types
// are accepted during artifact creation.
func TestArtifact_ValidLinkTypes(t *testing.T) {
	validTypes := []string{
		"parent", "related_to", "blocks", "blocked_by",
		"supersedes", "superseded_by", "follow_up_to", "follow_up_from",
	}

	for _, linkType := range validTypes {
		linkType := linkType
		t.Run(linkType, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "valid-link-type-" + linkType,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					{
						Name: "create-artifact-with-link",
						Action: func(sc *engine.ScenarioContext) error {
							engine.FixtureGovernance(sc,
								"governance/link-test-"+linkType+".md",
								engine.ArtifactOpts{
									Title: "Link Test " + linkType,
									Links: []engine.LinkOpt{
										{Type: linkType, Target: "/governance/charter.md"},
									},
								},
							)
							return nil
						},
					},
					engine.SyncProjections(),
					engine.AssertProjection("governance/link-test-"+linkType+".md", "Title", "Link Test "+linkType),
				},
			})
		})
	}
}
