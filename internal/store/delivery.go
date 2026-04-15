package store

import "time"

// DeliveryEntry represents a pending event delivery in the queue.
type DeliveryEntry struct {
	DeliveryID     string     `json:"delivery_id"`
	SubscriptionID string     `json:"subscription_id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	Payload        []byte     `json:"payload"`          // JSONB
	Status         string     `json:"status"`           // pending, delivering, delivered, failed, dead
	AttemptCount   int        `json:"attempt_count"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}

// DeliveryLogEntry records a single delivery attempt.
type DeliveryLogEntry struct {
	LogID          string    `json:"log_id"`
	DeliveryID     string    `json:"delivery_id"`
	SubscriptionID string    `json:"subscription_id"`
	EventID        string    `json:"event_id"`
	StatusCode     *int      `json:"status_code,omitempty"`
	DurationMs     *int      `json:"duration_ms,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// DeliveryHistoryQuery defines parameters for querying delivery history.
type DeliveryHistoryQuery struct {
	SubscriptionID string
	Limit          int
}
