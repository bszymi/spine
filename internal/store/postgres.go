package store

import (
	"context"
	"fmt"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store using PostgreSQL via pgx.
type PostgresStore struct {
	pool   *pgxpool.Pool
	cipher *spinecrypto.SecretCipher
}

// SetSecretCipher installs the AEAD used to encrypt at-rest secrets
// (currently: event_subscriptions.signing_secret). With no cipher
// configured the store falls back to plaintext, matching behaviour
// before TASK-007 so integration tests and development setups work
// without additional configuration.
func (s *PostgresStore) SetSecretCipher(c *spinecrypto.SecretCipher) {
	s.cipher = c
}

// secretCipher returns the installed cipher or nil.
func (s *PostgresStore) secretCipher() *spinecrypto.SecretCipher {
	return s.cipher
}

// NewPostgresStore creates a new PostgreSQL store with connection pooling.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

// Close closes the connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// Ping checks database connectivity.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// WithTx executes a function within a database transaction.
// The transaction is committed if fn returns nil, rolled back otherwise.
func (s *PostgresStore) WithTx(ctx context.Context, fn func(tx Tx) error) error {
	pgxTx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	tx := &postgresTx{tx: pgxTx}
	if err := fn(tx); err != nil {
		if rbErr := pgxTx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}

	return pgxTx.Commit(ctx)
}

// ── Helpers ──

func modeOrDefault(m domain.RunMode) string {
	if m == "" {
		return string(domain.RunModeStandard)
	}
	return string(m)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
