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

// ClaimDeliveries moves up to limit due deliveries (pending, or failed
// with next_retry_at reached) to 'delivering' and returns them.
//
// The claim is fair across subscriptions (INIT-023 TASK-010 §2a, audit
// P3-5): claimable rows are ranked within their subscription by
// created_at and claimed in (rank, created_at) order — round-robin across
// subscriptions, FIFO within each. With the old global ORDER BY
// created_at, any subscription's old claimable rows took every claim slot
// ahead of every other subscription's newer rows: a backlog held behind
// an open circuit (all deferred to one probe instant) larger than
// concurrency x (recovery timeout / poll interval) = 300 rows starved
// every other subscription in the workspace indefinitely. Now each
// subscription's oldest due row competes for a slot on its own age, so
// while fewer than limit subscriptions have due rows, none waits more
// than one poll behind another's backlog. The fair claim alone does not
// bound the case of limit or more subscriptions each holding an old
// backlog: their oldest rows would take every slot. The dispatcher closes
// that by holding the whole due backlog of a subscription it cannot
// deliver to (HoldDueDeliveries), so such a backlog is not claimable at
// all between holds. A single-subscription queue — SMP's shape, one
// subscription per runtime DB — is claimed in exactly the old order.
//
// Postgres refuses FOR UPDATE beside a window function at the same query
// level, so the ranking sits in a subquery and the row lock is taken one
// level up. LIMIT applies before SKIP LOCKED: when dispatchers race (SMP
// replicas) one may claim fewer than limit rows in a poll, never a row
// twice. The outer status and due-time re-check is re-evaluated under
// the lock (READ COMMITTED), so a row another claimer took — or took and
// deferred into the future — in between is skipped. Measured on
// PostgreSQL 17 with 100 000 claimable rows across 50 subscriptions:
// about 40 ms per claim against about 20 ms for the old query.
func (s *PostgresStore) ClaimDeliveries(ctx context.Context, limit int) ([]DeliveryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'delivering'
		WHERE delivery_id IN (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE delivery_id IN (
				SELECT delivery_id FROM (
					SELECT delivery_id, created_at,
					       row_number() OVER (PARTITION BY subscription_id ORDER BY created_at) AS rn
					FROM runtime.event_delivery_queue
					WHERE status IN ('pending', 'failed')
					  AND (next_retry_at IS NULL OR next_retry_at <= now())
				) ranked
				ORDER BY rn, created_at
				LIMIT $1
			)
			AND status IN ('pending', 'failed')
			AND (next_retry_at IS NULL OR next_retry_at <= now())
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

// DeferDelivery re-schedules a claimed delivery without consuming an
// attempt (see DeliveryStore.DeferDelivery). Unlike UpdateDeliveryStatus
// it does not bump attempt_count — nothing was sent — and it only
// touches a row still in 'delivering'.
func (s *PostgresStore) DeferDelivery(ctx context.Context, deliveryID, reason string, until time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'pending', last_error = $2, next_retry_at = $3
		WHERE delivery_id = $1 AND status = 'delivering'`,
		deliveryID, nilIfEmpty(reason), until)
	return err
}

// HoldDueDeliveries takes a subscription's due deliveries off the
// claimable set (see DeliveryStore.HoldDueDeliveries).
//
// Why it exists: the fair claim gives each subscription's oldest due row
// a slot ahead of any newer row. A subscription the dispatcher cannot
// deliver to (open circuit, unreadable subscription) used to have only
// the rows it was handed deferred, one per poll, while the rest of its
// backlog stayed due — so once `concurrency` (5) such subscriptions each
// had a backlog, their oldest rows took every claim slot on every poll
// and every other subscription starved (INIT-023 TASK-010 review round
// 1). Holding the whole due backlog at once leaves such a subscription
// with nothing claimable until the hold ends, so it takes about one slot
// per hold, however many rows it has.
//
// The candidate rows are locked FOR UPDATE SKIP LOCKED in a CTE: a row a
// dispatcher is claiming (or another hold is writing) is skipped, never
// waited on, so two replicas holding the same subscription cannot
// deadlock. Locked rows are re-checked against the predicate under READ
// COMMITTED, so a row claimed or rescheduled in between is not touched.
// The scan uses idx_delivery_queue_subscription (subscription_id,
// created_at).
func (s *PostgresStore) HoldDueDeliveries(ctx context.Context, hold DeliveryHold) ([]DeliveryEntry, error) {
	var expireBefore *time.Time
	if !hold.ExpireBefore.IsZero() {
		t := hold.ExpireBefore
		expireBefore = &t
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE subscription_id = $1
			  AND status IN ('pending', 'failed')
			  AND (next_retry_at IS NULL OR next_retry_at < $2)
			FOR UPDATE SKIP LOCKED
		), held AS (
			UPDATE runtime.event_delivery_queue q
			SET status = CASE WHEN q.created_at < $4 THEN 'dead' ELSE q.status END,
			    last_error = CASE WHEN q.created_at < $4 THEN $5
			                      WHEN q.status = 'pending' THEN $3
			                      ELSE q.last_error END,
			    next_retry_at = CASE WHEN q.created_at < $4 THEN NULL ELSE $2 END
			FROM due
			WHERE q.delivery_id = due.delivery_id
			RETURNING q.delivery_id, q.subscription_id, q.event_id, q.event_type,
			          q.status, q.attempt_count, q.last_error, q.created_at
		)
		SELECT delivery_id, subscription_id, event_id, event_type,
		       status, attempt_count, last_error, created_at
		FROM held WHERE status = 'dead'`,
		hold.SubscriptionID, hold.Until, nilIfEmpty(hold.Reason), expireBefore, nilIfEmpty(hold.ExpiredReason))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expired []DeliveryEntry
	for rows.Next() {
		var e DeliveryEntry
		var lastError *string
		if err := rows.Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
			&e.Status, &e.AttemptCount, &lastError, &e.CreatedAt); err != nil {
			return nil, err
		}
		if lastError != nil {
			e.LastError = *lastError
		}
		expired = append(expired, e)
	}
	return expired, rows.Err()
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
