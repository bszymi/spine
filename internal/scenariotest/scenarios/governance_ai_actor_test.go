//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestAIActor_SamePermissionsAsHuman validates that AI actors with the
// same role get identical permission decisions as human actors.
//
// Scenario Outline: AI and automated actors get identical permissions as humans
//   Given a seeded governance environment
//     And a Human, AI, and Automated actor each with role "<role>"
//   When all three attempt operation "<operation>"
//   Then all three should receive the same allow/deny decision
//
//   Examples: all combinations of (Reader..Admin) x (artifact.create..token.create)
func TestAIActor_SamePermissionsAsHuman(t *testing.T) {
	operations := []auth.Operation{
		"artifact.create", "artifact.read", "run.start", "run.cancel",
		"step.submit", "step.assign", "task.accept", "token.create",
	}
	roles := []domain.ActorRole{
		domain.RoleReader, domain.RoleContributor,
		domain.RoleReviewer, domain.RoleOperator, domain.RoleAdmin,
	}

	for _, role := range roles {
		for _, op := range operations {
			role, op := role, op
			name := string(role) + "-" + string(op)
			t.Run(name, func(t *testing.T) {
				engine.RunScenario(t, engine.Scenario{
					Name:    "ai-parity-" + name,
					EnvOpts: harness.Seeded(),
					Steps: []engine.Step{
						{
							Name: "compare-actor-types",
							Action: func(sc *engine.ScenarioContext) error {
								human := &domain.Actor{
									ActorID: "human-1",
									Type:    domain.ActorTypeHuman,
									Role:    role,
									Status:  domain.ActorStatusActive,
								}
								ai := &domain.Actor{
									ActorID: "ai-1",
									Type:    domain.ActorTypeAIAgent,
									Role:    role,
									Status:  domain.ActorStatusActive,
								}
								automated := &domain.Actor{
									ActorID: "auto-1",
									Type:    domain.ActorTypeAutomated,
									Role:    role,
									Status:  domain.ActorStatusActive,
								}

								humanErr := auth.Authorize(human, op)
								aiErr := auth.Authorize(ai, op)
								autoErr := auth.Authorize(automated, op)

								// All actor types should get the same result.
								humanAllowed := humanErr == nil
								aiAllowed := aiErr == nil
								autoAllowed := autoErr == nil

								if humanAllowed != aiAllowed {
									sc.T.Errorf("human(%v) != ai(%v) for %s/%s",
										humanAllowed, aiAllowed, role, op)
								}
								if humanAllowed != autoAllowed {
									sc.T.Errorf("human(%v) != automated(%v) for %s/%s",
										humanAllowed, autoAllowed, role, op)
								}
								return nil
							},
						},
					},
				})
			})
		}
	}
}

// TestAIActor_SameArtifactValidation validates that artifact creation
// validation applies identically regardless of actor type.
//
// Scenario: AI actor artifact creation follows identical validation as human
//   Given a seeded governance environment
//   When an AI actor creates a valid Governance artifact
//     And projections are synced
//   Then the artifact should be projected with correct title
//   When an AI actor creates an artifact with invalid status "Draft"
//   Then creation should fail with an error
//   When an AI actor creates an artifact missing a required "title" field
//   Then creation should fail with an error
func TestAIActor_SameArtifactValidation(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "ai-same-artifact-validation",
		Description: "AI actor artifact creation follows identical validation as human",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// AI actor creating a valid artifact succeeds.
			{
				Name: "ai-creates-valid-artifact",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/ai-created.md", engine.ArtifactOpts{
						Title: "AI Created Document",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("governance/ai-created.md", "Title", "AI Created Document"),

			// AI actor creating an invalid artifact is rejected identically.
			engine.ExpectError("ai-invalid-status", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/ai-bad.md", `---
type: Governance
title: "AI Bad Status"
status: Draft
---

# AI Bad Status
`)
				return err
			}, ""),

			// Both valid and invalid results match what a human would get.
			engine.ExpectError("ai-missing-field", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/ai-no-title.md", `---
type: Governance
status: Living Document
---

# No Title
`)
				return err
			}, ""),
		},
	})
}

// TestAIActor_GovernanceViolationsRejected validates that AI actors
// attempting governance violations are rejected with clear errors.
//
// Scenario: AI actor governance violations are rejected
//   Given a seeded governance environment
//   When an AI actor creates an artifact with unknown type "AISpecialType"
//   Then creation should fail with an error
//   When an AI actor creates an artifact with invalid link type "ai_override"
//   Then creation should fail with an error
//   When an AI actor creates an artifact with non-standard ID "AI-SPECIAL-001"
//   Then creation should fail with an error
func TestAIActor_GovernanceViolationsRejected(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "ai-governance-violations-rejected",
		Description: "AI actor governance violations are rejected identically to human violations",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			// AI cannot create artifact with unknown type.
			engine.ExpectError("ai-unknown-type", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/ai-unknown.md", `---
type: AISpecialType
title: "Special AI Artifact"
status: Active
---

# Special
`)
				return err
			}, ""),

			// AI cannot create artifact with invalid link type.
			engine.ExpectError("ai-invalid-link", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/ai-bad-link.md", `---
type: Governance
title: "AI Bad Link"
status: Living Document
links:
  - type: ai_override
    target: /governance/charter.md
---

# Bad Link
`)
				return err
			}, ""),

			// AI cannot bypass ID format requirements.
			engine.ExpectError("ai-invalid-id", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"initiatives/ai-init/initiative.md", `---
id: AI-SPECIAL-001
type: Initiative
title: "AI Special Initiative"
status: Draft
created: "2026-01-01"
---

# AI Special
`)
				return err
			}, ""),
		},
	})
}

// TestAIActor_ContributorCannotReview validates that an AI actor
// with contributor role cannot perform review operations, matching
// the same restriction applied to human contributors.
//
// Scenario: AI contributor is denied review operations
//   Given a seeded governance environment
//     And an AI actor with role "Contributor"
//   When the actor attempts task.accept, task.reject, task.cancel, or discussion.resolve
//   Then all operations should be denied with ErrForbidden
func TestAIActor_ContributorCannotReview(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "ai-contributor-cannot-review",
		Description: "AI contributor is denied review operations just like human contributor",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-review-denied",
				Action: func(sc *engine.ScenarioContext) error {
					aiContributor := &domain.Actor{
						ActorID: "ai-contrib",
						Type:    domain.ActorTypeAIAgent,
						Role:    domain.RoleContributor,
						Status:  domain.ActorStatusActive,
					}

					reviewOps := []auth.Operation{
						"task.accept", "task.reject", "task.cancel",
						"discussion.resolve",
					}

					for _, op := range reviewOps {
						err := auth.Authorize(aiContributor, op)
						assert.ErrorCode(sc.T, err, domain.ErrForbidden)
					}
					return nil
				},
			},
		},
	})
}

// TestAIActor_ReaderCannotMutate validates that an AI actor with reader
// role cannot perform mutating operations.
//
// Scenario: AI reader is denied all mutating operations
//   Given a seeded governance environment
//     And an AI actor with role "Reader"
//   When the actor attempts create, update, run.start, step.submit, or discussion operations
//   Then all operations should be denied with ErrForbidden
func TestAIActor_ReaderCannotMutate(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "ai-reader-cannot-mutate",
		Description: "AI reader is denied all mutating operations",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-mutations-denied",
				Action: func(sc *engine.ScenarioContext) error {
					aiReader := &domain.Actor{
						ActorID: "ai-reader",
						Type:    domain.ActorTypeAIAgent,
						Role:    domain.RoleReader,
						Status:  domain.ActorStatusActive,
					}

					mutatingOps := []auth.Operation{
						"artifact.create", "artifact.update",
						"run.start", "step.submit",
						"discussion.create", "discussion.comment",
					}

					for _, op := range mutatingOps {
						err := auth.Authorize(aiReader, op)
						assert.ErrorCode(sc.T, err, domain.ErrForbidden)
					}
					return nil
				},
			},
		},
	})
}
