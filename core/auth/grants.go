package auth

import (
	"github.com/bszymi/spine/core/domain"
)

// Reasons carried in the ErrForbidden detail of an identity-escalation
// refusal (INIT-023 TASK-013). They let a client, and the audit log,
// tell "you may not change your own authority" and "you may not mint
// an actor at or above your own role" apart from an ordinary
// insufficient-role refusal.
const (
	ReasonSelfSkillChange = "self_skill_change_forbidden"
	ReasonRoleCeiling     = "role_ceiling_exceeded"
)

// opActorCreate is the operation AuthorizeRoleGrant guards. It names
// the operation in the status-class refusal detail.
const opActorCreate Operation = "actor.create"

// AuthorizeActorSkillChange authorizes op ("actor.add_skill" or
// "actor.remove_skill") by caller against the actor targetActorID.
//
// Skills are authorization: workflow steps admit an actor by the skills
// it holds, and every shipped review step requires one. Audit F6 showed
// a contributor granting itself "review", then claiming and accepting
// the review of its own run. So:
//
//  1. A non-active caller is refused with Authorize's status-class
//     detail, first — the same ordering Authorize pins
//     (TestAuthorize_StatusCheckPrecedesRoleCheck).
//  2. A caller below admin may never change its own skill set. This is
//     redundant with today's admin-only role map, on purpose: it keeps
//     a future loosening of the map (for example, operators managing
//     runner skills) from reopening self-grant, and its distinct reason
//     makes the most suspicious request — an actor granting itself a
//     review skill — visible in the response and the audit log.
//  3. Then the role map: Authorize(caller, op).
//
// Admins are exempt from rule 2: they sit at the top of the hierarchy,
// and stopping an approver from approving its own work is step-level
// separation of duties, not a skill rule.
//
// targetActorID must be the decoded actor id the operation will act on
// (the gateway percent-decodes {actor_id} once before calling); a
// comparison against a still-escaped id would let "op%2D1" pass as
// "another" actor. A nil caller is refused.
func AuthorizeActorSkillChange(caller *domain.Actor, op Operation, targetActorID string) error {
	if caller == nil {
		return domain.NewError(domain.ErrForbidden, "no authenticated actor")
	}
	if err := requireActive(caller, op); err != nil {
		return err
	}
	if caller.ActorID == targetActorID && caller.Role != domain.RoleAdmin {
		return domain.NewErrorWithDetail(domain.ErrForbidden,
			"actors below admin cannot change their own skills",
			map[string]string{
				"reason":     ReasonSelfSkillChange,
				"actor_id":   caller.ActorID,
				"actor_role": string(caller.Role),
				"operation":  string(op),
			})
	}
	return Authorize(caller, op)
}

// AuthorizeRoleGrant enforces the actor.create role ceiling (audit
// P3-33): an admin may grant any known role; any other caller may grant
// only roles strictly below its own. "Strictly" means an operator
// cannot mint a peer operator — equal-role minting multiplies operator
// credentials with no admin in the loop.
//
// Call it after Authorize(caller, "actor.create"), but it does not rely
// on that: as an exported primitive it fails closed on its own.
//
//   - A nil or non-active caller is refused (status-class detail, as
//     Authorize), checked first.
//   - An unknown or empty granted role is refused for every caller,
//     admin included. RoleLevel() is 0 for such a role, so an unguarded
//     "granted >= caller" comparison would treat it as below every role
//     and let it through.
func AuthorizeRoleGrant(caller *domain.Actor, granted domain.ActorRole) error {
	if caller == nil {
		return domain.NewError(domain.ErrForbidden, "no authenticated actor")
	}
	if err := requireActive(caller, opActorCreate); err != nil {
		return err
	}
	if granted.RoleLevel() == 0 {
		return roleCeilingError(caller, granted, "cannot create an actor with an unknown role")
	}
	if caller.Role == domain.RoleAdmin {
		return nil
	}
	if granted.RoleLevel() >= caller.Role.RoleLevel() {
		return roleCeilingError(caller, granted, "cannot create an actor at or above your own role")
	}
	return nil
}

func roleCeilingError(caller *domain.Actor, granted domain.ActorRole, msg string) error {
	return domain.NewErrorWithDetail(domain.ErrForbidden, msg,
		map[string]string{
			"reason":       ReasonRoleCeiling,
			"actor_role":   string(caller.Role),
			"granted_role": string(granted),
			"operation":    string(opActorCreate),
		})
}
