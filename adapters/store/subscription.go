package store

import "time"

// EventSubscription represents a configured event delivery target.
type EventSubscription struct {
	SubscriptionID string    `json:"subscription_id"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	Name           string    `json:"name"`
	TargetType     string    `json:"target_type"`     // "webhook"
	TargetURL      string    `json:"target_url"`
	EventTypes     []string  `json:"event_types"`     // empty = all events
	SigningSecret  string    `json:"signing_secret"`
	Status         string    `json:"status"`           // active, paused, disabled
	Metadata       []byte    `json:"metadata"`         // JSONB
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
