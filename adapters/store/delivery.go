package store

import "time"

// DeliveryEntry represents a pending event delivery in the queue.
type DeliveryEntry struct {
	DeliveryID     string     `json:"delivery_id"`
	SubscriptionID string     `json:"subscription_id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	Payload        []byte     `json:"payload"` // JSONB
	Status         string     `json:"status"`  // pending, delivering, delivered, failed, dead
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

// EventLogEntry represents a single emitted event in the event log.
type EventLogEntry struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// DeliveryHold is the request of DeliveryStore.HoldDueDeliveries: take
// one subscription's due deliveries off the claimable set until a given
// instant, without consuming an attempt.
type DeliveryHold struct {
	// SubscriptionID names the subscription whose deliveries are held.
	SubscriptionID string
	// Until is when the held deliveries become claimable again. Rows of
	// the subscription in 'pending' or 'failed' whose next_retry_at is
	// NULL or before Until are held; a row already scheduled at or after
	// Until is left alone.
	Until time.Time
	// Reason becomes last_error of a held 'pending' row. A 'failed' row
	// keeps the text of its last real attempt.
	Reason string
	// ExpireBefore, when non-zero, dead-letters instead of holding every
	// due row created before it, with ExpiredReason as last_error. This is
	// how an age cap on holds (the webhook dispatcher's MaxCircuitHold)
	// reaches rows the caller never claimed.
	ExpireBefore  time.Time
	ExpiredReason string
}

// DeliveryHistoryQuery defines parameters for querying delivery history.
type DeliveryHistoryQuery struct {
	SubscriptionID string
	Limit          int
}

// DeliveryStats contains aggregate delivery statistics for a subscription.
type DeliveryStats struct {
	TotalDeliveries int     `json:"total_deliveries"`
	Delivered       int     `json:"delivered"`
	Failed          int     `json:"failed"`
	Dead            int     `json:"dead"`
	Pending         int     `json:"pending"`
	SuccessRate     float64 `json:"success_rate"`
	AvgLatencyMs    *int    `json:"avg_latency_ms,omitempty"`
}
