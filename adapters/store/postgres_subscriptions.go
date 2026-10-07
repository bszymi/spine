package store

import (
	"context"
	"fmt"

	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
)

// ── Event Subscriptions ──

// CreateSubscription inserts a subscription. Under a binding a successful
// insert also records SigningSecret as the secret sub last saw (as a bound
// read does), so a later UpdateSubscription of the same value judges
// "kept" from it rather than from a fresh read of the row (see
// UpdateSubscription).
func (s *PostgresStore) CreateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.sealSubscriptionSecret(sub.SubscriptionID, sub.SigningSecret)
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
	if err == nil {
		s.recordSecretRead(sub)
	}
	return err
}

// encryptSubscriptionSecret is the unbound write path (no
// SetSubscriptionSecretBinding): it encrypts a signing secret as enc:v1
// when a cipher is configured. If the value is already ciphertext (i.e.
// it was re-saved without being decrypted first) it is passed through so
// we never double-encrypt. Bound stores use sealSubscriptionSecret
// (postgres_subscription_secrets.go) instead.
func (s *PostgresStore) encryptSubscriptionSecret(secret string) (string, error) {
	if s.cipher == nil || secret == "" || spinecrypto.IsEncrypted(secret) {
		return secret, nil
	}
	return s.cipher.Encrypt(secret)
}

// decryptSubscriptionSecret is the unbound read path: it reverses
// encryptSubscriptionSecret. Plaintext rows (written before a key was
// configured) are returned as-is. They are NOT re-encrypted by a later
// write: BootstrapInternalSubscription and SMP's realign compare the
// decrypted secret and skip unchanged rows, so no write ever comes
// (audit P3-1). Converting them is the job of the explicit sweep,
// ReencryptSubscriptionSecrets, which needs a binding.
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
	secret, err := s.openSubscriptionSecret(sub.SubscriptionID, sub.SigningSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt signing_secret: %w", err)
	}
	sub.SigningSecret = secret
	s.recordSecretRead(&sub)
	return &sub, nil
}

// UpdateSubscription replaces a subscription's mutable columns.
//
// Under a binding (SetSubscriptionSecretBinding) the signing_secret column
// is written only when the caller set a secret; when it kept the one it
// read — the common read-modify-write: PATCH, pause, activate, a
// target-URL realign — only the other columns are written. Re-sealing a
// kept secret would let a read-modify-write convert a legacy (plaintext
// or enc:v1) value into this row's enc:v2 silently: in migrate mode a
// planted copy of another workspace's pre-upgrade enc:v1 value opens
// (enc:v1 binds nothing), and re-sealing what was read would make it a
// valid enc:v2 row that strict later accepts, with no conversion record
// (INIT-023 TASK-009 review round 1). Leaving the bytes alone keeps the
// re-encryption sweep, which records every conversion, the only path from
// a legacy value to enc:v2; a set secret replaces the stored value
// outright, so nothing read from the row is carried over.
//
// "Kept" is decided from what the caller read, never from a later read
// of the row (codex review): for a value returned by a bound read,
// SigningSecret is compared with the secret that read returned, so a
// rotation that lands between the caller's read and this update is
// neither reverted nor replaced by the stale (possibly planted) secret.
// Only a value that did not come from a bound read (a struct the caller
// built) falls back to comparing against the stored value — one extra
// SELECT, and a caller that copies a secret it read into a new struct
// while a rotation races it can still write that copy; build updates from
// the value the store returned.
//
// After a successful bound update, SigningSecret becomes the secret sub
// last saw, whichever branch ran (codex review, round 2): the value just
// written, or the value just found kept. A caller that reuses sub for a
// further update is then judged against what this update left, not
// against its original read. Without that, reading A, setting B, then
// setting A back would be judged "kept" against the stale A and silently
// leave B in the row; and after setting B, a metadata-only update would
// see B ≠ A, write B again, and revert a rotation that landed in between.
// A failed update records nothing.
//
// A side effect: an update that keeps the secret no longer reverts a
// concurrent rotate-secret. With no binding the historical single UPDATE
// is unchanged and nothing is recorded.
func (s *PostgresStore) UpdateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.sealSubscriptionSecret(sub.SubscriptionID, sub.SigningSecret)
	if err != nil {
		return err
	}
	if s.secretBinding != nil {
		kept, err := s.boundSecretKept(ctx, sub)
		if err != nil {
			return err
		}
		if kept {
			_, err = s.pool.Exec(ctx, `
				UPDATE runtime.event_subscriptions SET
					name = $2, target_type = $3, target_url = $4, event_types = $5,
					status = $6, metadata = $7, updated_at = now()
				WHERE subscription_id = $1`,
				sub.SubscriptionID, sub.Name, sub.TargetType, sub.TargetURL,
				sub.EventTypes, sub.Status, sub.Metadata,
			)
			if err == nil {
				s.recordSecretRead(sub)
			}
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE runtime.event_subscriptions SET
			name = $2, target_type = $3, target_url = $4, event_types = $5,
			signing_secret = $6, status = $7, metadata = $8, updated_at = now()
		WHERE subscription_id = $1`,
		sub.SubscriptionID, sub.Name, sub.TargetType, sub.TargetURL,
		sub.EventTypes, secret, sub.Status, sub.Metadata,
	)
	if err == nil {
		// No-op without a binding (recordSecretRead checks).
		s.recordSecretRead(sub)
	}
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
		secret, err := s.openSubscriptionSecret(sub.SubscriptionID, sub.SigningSecret)
		if err != nil {
			return nil, fmt.Errorf("decrypt signing_secret: %w", err)
		}
		sub.SigningSecret = secret
		s.recordSecretRead(&sub)
		results = append(results, sub)
	}
	return results, rows.Err()
}
