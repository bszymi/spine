package store

import "time"

// EventSubscription represents a configured event delivery target.
type EventSubscription struct {
	SubscriptionID string    `json:"subscription_id"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	Name           string    `json:"name"`
	TargetType     string    `json:"target_type"` // "webhook"
	TargetURL      string    `json:"target_url"`
	EventTypes     []string  `json:"event_types"` // empty = all events
	SigningSecret  string    `json:"signing_secret"`
	Status         string    `json:"status"`   // active, paused, disabled
	Metadata       []byte    `json:"metadata"` // JSONB
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// secretRead and readSecret are set only by a read through a store
	// bound with SetSubscriptionSecretBinding (GetSubscription,
	// ListSubscriptions, ListActiveSubscriptionsByEventType): the secret
	// that read returned. A bound UpdateSubscription compares
	// SigningSecret with it to tell "the caller kept the secret it read"
	// (write the other columns only) from "the caller set a secret"
	// (INIT-023 TASK-009). Unbound reads leave them zero, so values from
	// an unbound store are exactly as before.
	secretRead bool
	readSecret string
}
