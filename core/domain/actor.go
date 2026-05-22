package domain

// ActorType represents the kind of actor.
type ActorType string

const (
	ActorTypeHuman     ActorType = "human"
	ActorTypeAIAgent   ActorType = "ai_agent"
	ActorTypeAutomated ActorType = "automated_system"
)

// ActorRole represents the authorization role of an actor.
// Per security-model.md §4.
type ActorRole string

const (
	RoleReader      ActorRole = "reader"
	RoleContributor ActorRole = "contributor"
	RoleReviewer    ActorRole = "reviewer"
	RoleOperator    ActorRole = "operator"
	RoleAdmin       ActorRole = "admin"
)

// RoleLevel returns the numeric privilege level for role comparison.
// Higher values have more privileges.
func (r ActorRole) RoleLevel() int {
	switch r {
	case RoleReader:
		return 1
	case RoleContributor:
		return 2
	case RoleReviewer:
		return 3
	case RoleOperator:
		return 4
	case RoleAdmin:
		return 5
	default:
		return 0
	}
}

// HasAtLeast returns true if this role has at least the privileges of the required role.
func (r ActorRole) HasAtLeast(required ActorRole) bool {
	return r.RoleLevel() >= required.RoleLevel()
}

// ActorStatus represents the lifecycle status of an actor.
// Per actor-model.md §7.
type ActorStatus string

const (
	ActorStatusActive      ActorStatus = "active"
	ActorStatusSuspended   ActorStatus = "suspended"
	ActorStatusDeactivated ActorStatus = "deactivated"
)

// Actor represents a registered actor in the system.
type Actor struct {
	ActorID string      `json:"actor_id" yaml:"actor_id"`
	Type    ActorType   `json:"type" yaml:"type"`
	Name    string      `json:"name" yaml:"name"`
	Role    ActorRole   `json:"role" yaml:"role"`
	Status  ActorStatus `json:"status" yaml:"status"`
}
