package auth

import (
	"github.com/bszymi/spine/internal/domain"
)

// Operation identifies an API operation for authorization.
type Operation string

// operationRoles maps each operation to its minimum required role.
// Per API Operations §6 and Security Model §4.
var operationRoles = map[Operation]domain.ActorRole{
	// Artifacts
	"artifact.create":   domain.RoleContributor,
	"artifact.update":   domain.RoleContributor,
	"artifact.read":     domain.RoleReader,
	"artifact.list":     domain.RoleReader,
	"artifact.validate": domain.RoleReader,
	"artifact.links":    domain.RoleReader,

	// Workflow
	"run.start":   domain.RoleContributor,
	"run.status":  domain.RoleReader,
	"run.cancel":  domain.RoleOperator,
	"step.assign": domain.RoleOperator,
	"step.submit": domain.RoleContributor,

	// Task governance
	"task.accept":    domain.RoleReviewer,
	"task.reject":    domain.RoleReviewer,
	"task.cancel":    domain.RoleReviewer,
	"task.abandon":   domain.RoleReviewer,
	"task.supersede": domain.RoleReviewer,

	// Execution
	"execution.query":      domain.RoleReader,
	"execution.candidates": domain.RoleReader,
	"execution.claim":      domain.RoleContributor,
	"execution.release":    domain.RoleContributor,

	// Query
	"query.artifacts": domain.RoleReader,
	"query.graph":     domain.RoleReader,
	"query.history":   domain.RoleReader,
	"query.runs":      domain.RoleReader,

	// Discussions
	"discussion.list":    domain.RoleReader,
	"discussion.get":     domain.RoleReader,
	"discussion.create":  domain.RoleContributor,
	"discussion.comment": domain.RoleContributor,
	"discussion.resolve": domain.RoleReviewer,
	"discussion.reopen":  domain.RoleReviewer,

	// Divergence
	"divergence.create_branch": domain.RoleContributor,
	"divergence.close_window":  domain.RoleOperator,

	// Assignments
	"assignments.list": domain.RoleReader,

	// System
	"system.metrics":  domain.RoleOperator,
	"system.rebuild":  domain.RoleOperator,
	"system.validate": domain.RoleOperator,

	// Skills
	"skill.create":    domain.RoleContributor,
	"skill.read":      domain.RoleReader,
	"skill.update":    domain.RoleContributor,
	"skill.deprecate": domain.RoleContributor,

	// Token management
	"token.create": domain.RoleAdmin,
	"token.revoke": domain.RoleAdmin,
	"token.list":   domain.RoleAdmin,
}

// RequiredRole returns the minimum role for an operation.
// Returns false if the operation is unknown.
func RequiredRole(op Operation) (domain.ActorRole, bool) {
	role, ok := operationRoles[op]
	return role, ok
}

// Authorize checks whether the actor has sufficient privileges for the operation.
// Returns nil if authorized, or a SpineError with ErrForbidden if not.
func Authorize(actor *domain.Actor, op Operation) error {
	required, ok := RequiredRole(op)
	if !ok {
		return domain.NewError(domain.ErrForbidden, "unknown operation")
	}
	if !actor.Role.HasAtLeast(required) {
		return domain.NewErrorWithDetail(domain.ErrForbidden,
			"insufficient permissions",
			map[string]string{
				"required_role": string(required),
				"actor_role":    string(actor.Role),
				"operation":     string(op),
			})
	}
	return nil
}
