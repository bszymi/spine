package store

import (
	"context"
	"fmt"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/jackc/pgx/v5"
)

// ── Event Subscriptions ──

func (s *PostgresStore) CreateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.encryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO runtime.event_subscriptions
			(subscription_id, workspace_id, name, target_type, target_url, event_types,
			 signing_secret, status, metadata, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		sub.SubscriptionID, nilIfEmpty(sub.WorkspaceID), sub.Name, sub.TargetType, sub.TargetURL,
		sub.EventTypes, secret, sub.Status, sub.Metadata,
		sub.CreatedBy, sub.CreatedAt, sub.UpdatedAt,
	)
	return err
}

// encryptSubscriptionSecret encrypts a signing secret at rest when a
// cipher is configured. If the value is already ciphertext (i.e. it
// was re-saved without being decrypted first) it is passed through so
// we never double-encrypt.
func (s *PostgresStore) encryptSubscriptionSecret(secret string) (string, error) {
	if s.cipher == nil || secret == "" || spinecrypto.IsEncrypted(secret) {
		return secret, nil
	}
	return s.cipher.Encrypt(secret)
}

// decryptSubscriptionSecret reverses encryptSubscriptionSecret.
// Pre-migration plaintext rows are returned as-is; they will be
// re-encrypted on their next UpdateSubscription call.
func (s *PostgresStore) decryptSubscriptionSecret(stored string) (string, error) {
	if stored == "" {
		return stored, nil
	}
	return s.cipher.Decrypt(stored)
}

func (s *PostgresStore) GetSubscription(ctx context.Context, subscriptionID string) (*EventSubscription, error) {
	var sub EventSubscription
	var wsID *string
	err := s.pool.QueryRow(ctx, `
		SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
		       signing_secret, status, metadata, created_by, created_at, updated_at
		FROM runtime.event_subscriptions WHERE subscription_id = $1`, subscriptionID,
	).Scan(&sub.SubscriptionID, &wsID, &sub.Name, &sub.TargetType, &sub.TargetURL,
		&sub.EventTypes, &sub.SigningSecret, &sub.Status, &sub.Metadata,
		&sub.CreatedBy, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return nil, notFoundOr(err, "subscription not found")
	}
	if wsID != nil {
		sub.WorkspaceID = *wsID
	}
	secret, err := s.decryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt signing_secret: %w", err)
	}
	sub.SigningSecret = secret
	return &sub, nil
}

func (s *PostgresStore) UpdateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.encryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE runtime.event_subscriptions SET
			name = $2, target_type = $3, target_url = $4, event_types = $5,
			signing_secret = $6, status = $7, metadata = $8, updated_at = now()
		WHERE subscription_id = $1`,
		sub.SubscriptionID, sub.Name, sub.TargetType, sub.TargetURL,
		sub.EventTypes, secret, sub.Status, sub.Metadata,
	)
	return err
}

func (s *PostgresStore) DeleteSubscription(ctx context.Context, subscriptionID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM runtime.event_subscriptions WHERE subscription_id = $1`, subscriptionID)
	return err
}

func (s *PostgresStore) ListSubscriptions(ctx context.Context, workspaceID string) ([]EventSubscription, error) {
	var rows pgx.Rows
	var err error
	if workspaceID == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
			       signing_secret, status, metadata, created_by, created_at, updated_at
			FROM runtime.event_subscriptions ORDER BY created_at`)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
			       signing_secret, status, metadata, created_by, created_at, updated_at
			FROM runtime.event_subscriptions WHERE workspace_id = $1 ORDER BY created_at`, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubscriptions(rows)
}

func (s *PostgresStore) ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]EventSubscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
		       signing_secret, status, metadata, created_by, created_at, updated_at
		FROM runtime.event_subscriptions
		WHERE status = 'active'
		  AND (event_types = '{}' OR $1 = ANY(event_types))
		ORDER BY created_at`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubscriptions(rows)
}

func (s *PostgresStore) scanSubscriptions(rows pgx.Rows) ([]EventSubscription, error) {
	var results []EventSubscription
	for rows.Next() {
		var sub EventSubscription
		var wsID *string
		if err := rows.Scan(&sub.SubscriptionID, &wsID, &sub.Name, &sub.TargetType, &sub.TargetURL,
			&sub.EventTypes, &sub.SigningSecret, &sub.Status, &sub.Metadata,
			&sub.CreatedBy, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		if wsID != nil {
			sub.WorkspaceID = *wsID
		}
		secret, err := s.decryptSubscriptionSecret(sub.SigningSecret)
		if err != nil {
			return nil, fmt.Errorf("decrypt signing_secret: %w", err)
		}
		sub.SigningSecret = secret
		results = append(results, sub)
	}
	return results, rows.Err()
}
