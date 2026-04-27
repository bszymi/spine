package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/domain"
)

// poolQuerier is the subset of *pgxpool.Pool that PostgresStore
// depends on. Both *pgxpool.Pool and the per-workspace
// WorkspaceDBPool (which adds the ADR-012 saturation gate)
// satisfy it, so the store can run gated or ungated against the
// same SQL surface.
type poolQuerier interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
}

// PostgresStore implements Store using PostgreSQL via pgx.
type PostgresStore struct {
	pool poolQuerier
	// rawPool is the underlying pgxpool.Pool, retained for the
	// owning teardown path and any future code that needs raw pool
	// access (e.g. Stat). Nil only in tests; production code always
	// has a backing pgxpool.
	rawPool *pgxpool.Pool
	// ownsPool is true when PostgresStore opened the pgxpool itself
	// (single-workspace path) and false when an outer wrapper handed
	// the pool in (per-workspace WorkspaceDBPool path). Close
	// respects this so the pool is torn down exactly once.
	ownsPool bool
	cipher   *spinecrypto.SecretCipher
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
// The returned store owns the pgxpool and tears it down on Close.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &PostgresStore{pool: pool, rawPool: pool, ownsPool: true}, nil
}

// NewPostgresStoreWithQuerier wraps an externally-owned pool in a
// PostgresStore. The querier may be a saturation-gated wrapper
// (e.g. workspace.WorkspaceDBPool) that satisfies the same Begin /
// Query / QueryRow / Exec / Ping surface as *pgxpool.Pool. The
// store does not Close the pool — the caller retains ownership and
// drives tear-down through the wrapper.
func NewPostgresStoreWithQuerier(querier poolQuerier) *PostgresStore {
	return &PostgresStore{pool: querier, ownsPool: false}
}

// Close closes the connection pool if this store owns it.
func (s *PostgresStore) Close() {
	if s.ownsPool && s.rawPool != nil {
		s.rawPool.Close()
	}
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
