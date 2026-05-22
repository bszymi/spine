package domain

import "time"

// Token represents an API token bound to an actor.
// Per Security Model §3.3.
type Token struct {
	TokenID   string     `json:"token_id" yaml:"token_id"`
	ActorID   string     `json:"actor_id" yaml:"actor_id"`
	Name      string     `json:"name" yaml:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty" yaml:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" yaml:"created_at"`
}
