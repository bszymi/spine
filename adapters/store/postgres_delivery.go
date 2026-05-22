package store

import (
	"context"
	"fmt"
	"time"
)

// ── Event Delivery Queue ──

func (s *PostgresStore) EnqueueDelivery(ctx context.Context, entry *DeliveryEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_delivery_queue
			(delivery_id, subscription_id, event_id, event_type, payload, status, attempt_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (subscription_id, event_id) DO NOTHING`,
		entry.DeliveryID, entry.SubscriptionID, entry.EventID, entry.EventType,
		entry.Payload, entry.Status, entry.AttemptCount, entry.CreatedAt,
	)
	return err
}

func (s *PostgresStore) ClaimDeliveries(ctx context.Context, limit int) ([]DeliveryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'delivering'
		WHERE delivery_id IN (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE status IN ('pending', 'failed')
			  AND (next_retry_at IS NULL OR next_retry_at <= now())
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING delivery_id, subscription_id, event_id, event_type, payload,
		          status, attempt_count, next_retry_at, last_error, created_at, delivered_at`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryEntry
	for rows.Next() {
		var e DeliveryEntry
		var lastError *string
		if err := rows.Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
			&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
			&e.CreatedAt, &e.DeliveredAt); err != nil {
			return nil, err
		}
		if lastError != nil {
			e.LastError = *lastError
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = $2, last_error = $3, next_retry_at = $4, attempt_count = attempt_count + 1
		WHERE delivery_id = $1`,
		deliveryID, status, nilIfEmpty(lastError), nextRetryAt)
	return err
}

func (s *PostgresStore) MarkDelivered(ctx context.Context, deliveryID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'delivered', delivered_at = now(), attempt_count = attempt_count + 1
		WHERE delivery_id = $1`,
		deliveryID)
	return err
}

func (s *PostgresStore) LogDeliveryAttempt(ctx context.Context, entry *DeliveryLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_delivery_log
			(log_id, delivery_id, subscription_id, event_id, status_code, duration_ms, error, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.LogID, entry.DeliveryID, entry.SubscriptionID, entry.EventID,
		entry.StatusCode, entry.DurationMs, nilIfEmpty(entry.Error), entry.CreatedAt,
	)
	return err
}

func (s *PostgresStore) ListDeliveryHistory(ctx context.Context, query DeliveryHistoryQuery) ([]DeliveryLogEntry, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT log_id, delivery_id, subscription_id, event_id, status_code, duration_ms, error, created_at
		FROM runtime.event_delivery_log
		WHERE subscription_id = $1
		ORDER BY created_at DESC
		LIMIT $2`,
		query.SubscriptionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryLogEntry
	for rows.Next() {
		var e DeliveryLogEntry
		var statusCode, durationMs *int
		var errStr *string
		if err := rows.Scan(&e.LogID, &e.DeliveryID, &e.SubscriptionID, &e.EventID,
			&statusCode, &durationMs, &errStr, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.StatusCode = statusCode
		e.DurationMs = durationMs
		if errStr != nil {
			e.Error = *errStr
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) GetDelivery(ctx context.Context, deliveryID string) (*DeliveryEntry, error) {
	var e DeliveryEntry
	var lastError *string
	err := s.pool.QueryRow(ctx, `
		SELECT delivery_id, subscription_id, event_id, event_type, payload,
		       status, attempt_count, next_retry_at, last_error, created_at, delivered_at
		FROM runtime.event_delivery_queue WHERE delivery_id = $1`, deliveryID,
	).Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
		&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
		&e.CreatedAt, &e.DeliveredAt)
	if err != nil {
		return nil, notFoundOr(err, "delivery not found")
	}
	if lastError != nil {
		e.LastError = *lastError
	}
	return &e, nil
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, subscriptionID string, status string, limit int) ([]DeliveryEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	sql := `SELECT delivery_id, subscription_id, event_id, event_type, payload,
	               status, attempt_count, next_retry_at, last_error, created_at, delivered_at
	        FROM runtime.event_delivery_queue WHERE subscription_id = $1`
	args := []any{subscriptionID}
	argN := 2

	if status != "" {
		sql += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
		argN++
	}

	sql += " ORDER BY created_at DESC"
	sql += fmt.Sprintf(" LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryEntry
	for rows.Next() {
		var e DeliveryEntry
		var lastError *string
		if err := rows.Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
			&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
			&e.CreatedAt, &e.DeliveredAt); err != nil {
			return nil, err
		}
		if lastError != nil {
			e.LastError = *lastError
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) GetDeliveryStats(ctx context.Context, subscriptionID string) (*DeliveryStats, error) {
	var stats DeliveryStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE status = 'dead') AS dead,
			COUNT(*) FILTER (WHERE status IN ('pending', 'delivering')) AS pending
		FROM runtime.event_delivery_queue
		WHERE subscription_id = $1`, subscriptionID,
	).Scan(&stats.TotalDeliveries, &stats.Delivered, &stats.Failed, &stats.Dead, &stats.Pending)
	if err != nil {
		return nil, err
	}

	if stats.TotalDeliveries > 0 {
		stats.SuccessRate = float64(stats.Delivered) / float64(stats.TotalDeliveries)
	}

	// Average latency from delivery log
	var avgMs *float64
	err = s.pool.QueryRow(ctx, `
		SELECT AVG(duration_ms)::float8
		FROM runtime.event_delivery_log
		WHERE subscription_id = $1 AND status_code IS NOT NULL`, subscriptionID,
	).Scan(&avgMs)
	if err == nil && avgMs != nil {
		v := int(*avgMs)
		stats.AvgLatencyMs = &v
	}

	return &stats, nil
}

func (s *PostgresStore) WriteEventLog(ctx context.Context, entry *EventLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_log (event_id, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		entry.EventID, entry.EventType, entry.Payload, entry.CreatedAt)
	return err
}

func (s *PostgresStore) ListEventsAfter(ctx context.Context, afterEventID string, eventTypes []string, limit int) ([]EventLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	sql := `SELECT event_id, event_type, payload, created_at
	        FROM runtime.event_log WHERE 1=1`
	args := []any{}
	argN := 1

	if afterEventID != "" {
		sql += fmt.Sprintf(` AND created_at > (SELECT created_at FROM runtime.event_log WHERE event_id = $%d)`, argN)
		args = append(args, afterEventID)
		argN++
	}

	if len(eventTypes) > 0 {
		sql += fmt.Sprintf(` AND event_type = ANY($%d)`, argN)
		args = append(args, eventTypes)
		argN++
	}

	sql += ` ORDER BY created_at ASC`
	sql += fmt.Sprintf(` LIMIT $%d`, argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EventLogEntry
	for rows.Next() {
		var e EventLogEntry
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) DeleteExpiredDeliveries(ctx context.Context, before time.Time) (int64, error) {
	// Delete log entries for expired deliveries first (FK constraint)
	_, err := s.pool.Exec(ctx, `
		DELETE FROM runtime.event_delivery_log
		WHERE delivery_id IN (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE created_at < $1 AND status IN ('delivered', 'dead')
		)`, before)
	if err != nil {
		return 0, err
	}

	tag, err := s.pool.Exec(ctx, `
		DELETE FROM runtime.event_delivery_queue
		WHERE created_at < $1 AND status IN ('delivered', 'dead')`, before)
	if err != nil {
		return 0, err
	}

	// Clean up event log entries past retention
	_, _ = s.pool.Exec(ctx, `DELETE FROM runtime.event_log WHERE created_at < $1`, before)

	return tag.RowsAffected(), nil
}
