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

// TestPermission_RoleHierarchy validates that the role hierarchy is
// correctly enforced: reader < contributor < reviewer < operator < admin.
// Each operation requires a minimum role; lower roles are denied.
func TestPermission_RoleHierarchy(t *testing.T) {
	cases := []struct {
		operation auth.Operation
		role      domain.ActorRole
		allowed   bool
	}{
		// Reader can read but not create.
		{"artifact.read", domain.RoleReader, true},
		{"artifact.create", domain.RoleReader, false},
		{"run.start", domain.RoleReader, false},

		// Contributor can create and submit but not cancel runs.
		{"artifact.create", domain.RoleContributor, true},
		{"step.submit", domain.RoleContributor, true},
		{"run.cancel", domain.RoleContributor, false},
		{"step.assign", domain.RoleContributor, false},

		// Reviewer can accept/reject tasks but not manage system.
		{"task.accept", domain.RoleReviewer, true},
		{"task.reject", domain.RoleReviewer, true},
		{"system.rebuild", domain.RoleReviewer, false},

		// Operator can cancel runs and assign steps.
		{"run.cancel", domain.RoleOperator, true},
		{"step.assign", domain.RoleOperator, true},
		{"token.create", domain.RoleOperator, false},

		// Admin can do everything.
		{"token.create", domain.RoleAdmin, true},
		{"system.rebuild", domain.RoleAdmin, true},
	}

	for _, tc := range cases {
		tc := tc
		name := string(tc.role) + "-" + string(tc.operation)
		t.Run(name, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "role-" + name,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					{
						Name: "check-permission",
						Action: func(sc *engine.ScenarioContext) error {
							actor := &domain.Actor{
								ActorID: "test-actor",
								Role:    tc.role,
								Status:  domain.ActorStatusActive,
							}
							err := auth.Authorize(actor, tc.operation)
							if tc.allowed {
								assert.NoError(sc.T, err)
							} else {
								assert.ErrorCode(sc.T, err, domain.ErrForbidden)
							}
							return nil
						},
					},
				},
			})
		})
	}
}

// TestPermission_RoleInheritance validates that higher roles inherit
// all permissions of lower roles (reader -> contributor -> reviewer ->
// operator -> admin).
func TestPermission_RoleInheritance(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "role-inheritance",
		Description: "Higher roles inherit all permissions of lower roles",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-inheritance-chain",
				Action: func(sc *engine.ScenarioContext) error {
					roles := []domain.ActorRole{
						domain.RoleReader,
						domain.RoleContributor,
						domain.RoleReviewer,
						domain.RoleOperator,
						domain.RoleAdmin,
					}

					for i, higher := range roles {
						for _, lower := range roles[:i] {
							if !higher.HasAtLeast(lower) {
								sc.T.Errorf("%s should have at least %s privileges", higher, lower)
							}
						}
						// Each role has at least its own privileges.
						if !higher.HasAtLeast(higher) {
							sc.T.Errorf("%s should have at least its own privileges", higher)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestPermission_UnknownOperationDenied validates that unknown operations
// are always denied.
func TestPermission_UnknownOperationDenied(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "unknown-operation-denied",
		Description: "Unknown operations are denied regardless of role",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-unknown-op",
				Action: func(sc *engine.ScenarioContext) error {
					admin := &domain.Actor{
						ActorID: "admin-actor",
						Role:    domain.RoleAdmin,
						Status:  domain.ActorStatusActive,
					}
					err := auth.Authorize(admin, "nonexistent.operation")
					assert.ErrorCode(sc.T, err, domain.ErrForbidden)
					return nil
				},
			},
		},
	})
}

// TestPermission_WorkflowOperationRoles validates that workflow-specific
// operations require the correct minimum roles.
func TestPermission_WorkflowOperationRoles(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-operation-roles",
		Description: "Workflow operations enforce correct minimum role requirements",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-workflow-roles",
				Action: func(sc *engine.ScenarioContext) error {
					expected := map[auth.Operation]domain.ActorRole{
						"run.start":   domain.RoleContributor,
						"run.status":  domain.RoleReader,
						"run.cancel":  domain.RoleOperator,
						"step.assign": domain.RoleOperator,
						"step.submit": domain.RoleContributor,
					}

					for op, expectedRole := range expected {
						role, ok := auth.RequiredRole(op)
						if !ok {
							sc.T.Errorf("operation %s should be defined", op)
							continue
						}
						if role != expectedRole {
							sc.T.Errorf("operation %s: expected role %s, got %s", op, expectedRole, role)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestPermission_TaskGovernanceRequiresReviewer validates that task
// governance operations (accept, reject, cancel) require at least
// reviewer role.
func TestPermission_TaskGovernanceRequiresReviewer(t *testing.T) {
	governanceOps := []auth.Operation{
		"task.accept",
		"task.reject",
		"task.cancel",
		"task.abandon",
		"task.supersede",
	}

	for _, op := range governanceOps {
		op := op
		t.Run(string(op), func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "governance-" + string(op),
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					{
						Name: "contributor-denied",
						Action: func(sc *engine.ScenarioContext) error {
							contributor := &domain.Actor{
								ActorID: "contrib-actor",
								Role:    domain.RoleContributor,
								Status:  domain.ActorStatusActive,
							}
							err := auth.Authorize(contributor, op)
							assert.ErrorCode(sc.T, err, domain.ErrForbidden)
							return nil
						},
					},
					{
						Name: "reviewer-allowed",
						Action: func(sc *engine.ScenarioContext) error {
							reviewer := &domain.Actor{
								ActorID: "reviewer-actor",
								Role:    domain.RoleReviewer,
								Status:  domain.ActorStatusActive,
							}
							err := auth.Authorize(reviewer, op)
							assert.NoError(sc.T, err)
							return nil
						},
					},
				},
			})
		})
	}
}

// TestPermission_AdminOnlyOperations validates that token management
// requires admin role.
func TestPermission_AdminOnlyOperations(t *testing.T) {
	adminOps := []auth.Operation{
		"token.create",
		"token.revoke",
		"token.list",
	}

	for _, op := range adminOps {
		op := op
		t.Run(string(op), func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "admin-only-" + string(op),
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					{
						Name: "operator-denied",
						Action: func(sc *engine.ScenarioContext) error {
							operator := &domain.Actor{
								ActorID: "op-actor",
								Role:    domain.RoleOperator,
								Status:  domain.ActorStatusActive,
							}
							err := auth.Authorize(operator, op)
							assert.ErrorCode(sc.T, err, domain.ErrForbidden)
							return nil
						},
					},
					{
						Name: "admin-allowed",
						Action: func(sc *engine.ScenarioContext) error {
							admin := &domain.Actor{
								ActorID: "admin-actor",
								Role:    domain.RoleAdmin,
								Status:  domain.ActorStatusActive,
							}
							err := auth.Authorize(admin, op)
							assert.NoError(sc.T, err)
							return nil
						},
					},
				},
			})
		})
	}
}
