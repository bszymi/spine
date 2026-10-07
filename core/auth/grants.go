package auth

import (
	"github.com/bszymi/spine/core/domain"
)

// Reasons carried in the ErrForbidden detail of an identity-escalation
// refusal (INIT-023 TASK-013).
const (
	ReasonSelfSkillChange = "self_skill_change_forbidden"
	ReasonRoleCeiling     = "role_ceiling_exceeded"
)

// AuthorizeActorSkillChange authorizes op against targetActorID.
// Inert plumbing: delegates to Authorize.
func AuthorizeActorSkillChange(caller *domain.Actor, op Operation, targetActorID string) error {
	return Authorize(caller, op)
}

// AuthorizeRoleGrant enforces the actor.create role ceiling.
// Inert plumbing: allows everything.
func AuthorizeRoleGrant(caller *domain.Actor, granted domain.ActorRole) error {
	return nil
}
