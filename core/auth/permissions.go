package auth

import (
	"github.com/bszymi/spine/core/domain"
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

	// Workflow definitions (ADR-007)
	"workflow.create":   domain.RoleReviewer,
	"workflow.update":   domain.RoleReviewer,
	"workflow.read":     domain.RoleReader,
	"workflow.list":     domain.RoleReader,
	"workflow.validate": domain.RoleReader,

	// Workflow execution
	"run.start":         domain.RoleContributor,
	"run.status":        domain.RoleReader,
	"run.cancel":        domain.RoleOperator,
	"run.merge.resolve": domain.RoleOperator,
	"run.merge.retry":   domain.RoleOperator,
	"step.assign":       domain.RoleOperator,
	"step.submit":       domain.RoleContributor,

	// Approve or reject a governed merge held by the governance-path
	// guard (ADR-016; INIT-023 TASK-006). The engine checks the role again.
	"run.governance.decide": domain.RoleOperator,

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
	"system.validate": domain.RoleAdmin,

	// Actors. actor.create stays operator, but a non-admin caller may
	// only register roles strictly below its own (AuthorizeRoleGrant,
	// INIT-023 TASK-013, audit P3-33).
	"actor.create": domain.RoleOperator,

	// Actor-skill grants (api-operations.md §3.6). Skills decide who
	// may approve — every shipped review step names a required skill —
	// so granting or removing one is admin-only, and a non-admin can
	// never change its own skills (AuthorizeActorSkillChange). These
	// replace the routes' former reuse of skill.update (audit F6).
	"actor.add_skill":    domain.RoleAdmin,
	"actor.remove_skill": domain.RoleAdmin,

	// Skill catalogue. Create, update (which can rename, moving
	// eligibility exactly as deprecation does) and deprecate are
	// admin-only (audit F6); reading stays open to readers.
	"skill.create":    domain.RoleAdmin,
	"skill.read":      domain.RoleReader,
	"skill.update":    domain.RoleAdmin,
	"skill.deprecate": domain.RoleAdmin,

	// Subscriptions
	"subscription.create": domain.RoleAdmin,
	"subscription.read":   domain.RoleAdmin,
	"subscription.update": domain.RoleAdmin,
	"subscription.delete": domain.RoleAdmin,

	// Events
	"events.read": domain.RoleReader,

	// Token management
	"token.create": domain.RoleAdmin,
	"token.revoke": domain.RoleAdmin,
	"token.list":   domain.RoleAdmin,

	// Repository catalog & bindings (INIT-014 EPIC-001)
	"repository.create":     domain.RoleOperator,
	"repository.read":       domain.RoleReader,
	"repository.update":     domain.RoleOperator,
	"repository.deactivate": domain.RoleOperator,
}

// RequiredRole returns the minimum role for an operation.
// Returns false if the operation is unknown.
func RequiredRole(op Operation) (domain.ActorRole, bool) {
	role, ok := operationRoles[op]
	return role, ok
}

// Authorize checks whether the actor has sufficient privileges for the operation.
// Returns nil if authorized, or a SpineError with ErrForbidden if not.
//
// Defence-in-depth: rejects non-Active actors here even though
// ValidateToken already does (auth.go:43). A future caller that
// constructs an *Actor and calls Authorize directly — or a regression
// that loosens the token path — would otherwise let suspended /
// deactivated actors slip through.
func Authorize(actor *domain.Actor, op Operation) error {
	if err := requireActive(actor, op); err != nil {
		return err
	}
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

// requireActive is Authorize's status-class refusal, shared with the
// identity-authority helpers in grants.go so every exported
// authorization primitive refuses a non-active actor with the same
// detail, ahead of any role or reason check.
func requireActive(actor *domain.Actor, op Operation) error {
	if actor.Status != domain.ActorStatusActive {
		return domain.NewErrorWithDetail(domain.ErrForbidden,
			"actor is not active",
			map[string]string{
				"actor_id":     actor.ActorID,
				"actor_status": string(actor.Status),
				"operation":    string(op),
			})
	}
	return nil
}
