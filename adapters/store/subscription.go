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

	// secretRead and readSecret are set only through a store bound with
	// SetSubscriptionSecretBinding: by a read (GetSubscription,
	// ListSubscriptions, ListActiveSubscriptionsByEventType) to the secret
	// it returned, and refreshed by a successful CreateSubscription or
	// UpdateSubscription of this value to the secret it left in the row. A
	// bound UpdateSubscription compares SigningSecret with it to tell "the
	// caller kept the secret it last saw" (write the other columns only)
	// from "the caller set a secret" (INIT-023 TASK-009). Unbound stores
	// leave them zero, so values from an unbound store are exactly as
	// before.
	secretRead bool
	readSecret string
}
