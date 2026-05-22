package store

import (
	"context"
	"os"
	"testing"
)

const defaultTestDSN = "postgres://spine_test:spine_test@localhost:5433/spine_test?sslmode=disable"

// TestDSN returns the database connection string for integration tests.
func TestDSN() string {
	if dsn := os.Getenv("SPINE_TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return defaultTestDSN
}

// NewTestStore creates a PostgresStore for integration tests.
// Skips the test if the database is not available.
// Applies migrations before returning.
func NewTestStore(t *testing.T) *PostgresStore {
	t.Helper()

	ctx := context.Background()
	s, err := NewPostgresStore(ctx, TestDSN())
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}

	t.Cleanup(func() {
		s.Close()
	})

	// Apply migrations
	if err := s.ApplyMigrations(ctx, FindMigrationsDir()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return s
}

// ExecRaw executes a raw SQL statement. For testing only.
func (s *PostgresStore) ExecRaw(ctx context.Context, sql string, args ...any) error {
	_, err := s.pool.Exec(ctx, sql, args...)
	return err
}

// QueryRowSecret reads the raw (still-encrypted) signing_secret column
// for the given subscription ID. For testing the at-rest ciphertext
// invariant — callers bypass the decrypting GetSubscription path.
func (s *PostgresStore) QueryRowSecret(ctx context.Context, subscriptionID string, dest *string) error {
	return s.pool.QueryRow(ctx,
		`SELECT signing_secret FROM runtime.event_subscriptions WHERE subscription_id = $1`,
		subscriptionID,
	).Scan(dest)
}

// CountRepositoryBindings returns the total number of rows in
// runtime.repositories across all workspaces. Used by the single-repo
// backward-compatibility scenario to assert that running a task on a
// pre-INIT-014 workspace never silently materialises a binding row.
func (s *PostgresStore) CountRepositoryBindings(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.repositories`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CleanupTestData removes all test data from runtime and projection tables.
func (s *PostgresStore) CleanupTestData(ctx context.Context, t *testing.T) {
	t.Helper()
	tables := []string{
		"runtime.comments",
		"runtime.discussion_threads",
		"runtime.actor_assignments",
		"runtime.convergence_results",
		"runtime.branches",
		"runtime.divergence_contexts",
		"runtime.queue_entries",
		"runtime.step_executions",
		"runtime.repository_merge_outcomes",
		"runtime.runs",
		"runtime.repositories",
		"auth.actor_skills",
		"auth.tokens",
		"auth.skills",
		"auth.actors",
		"projection.artifact_links",
		"projection.artifacts",
		"projection.execution_projections",
		"projection.workflows",
		"projection.sync_state",
	}
	// projection.branch_protection_rules needs a restore-to-bootstrap
	// rather than a plain DELETE. Migration 018 seeds the bootstrap
	// default and ApplyMigrations won't re-run it, so leaving the
	// table alone lets one test leak its custom ruleset into the
	// next. Reset to the documented bootstrap state (ADR-009 §1) so
	// every test starts from the same predictable baseline.
	ctxInner := context.Background()
	if _, err := s.pool.Exec(ctxInner, "DELETE FROM projection.branch_protection_rules"); err != nil {
		t.Logf("cleanup branch_protection_rules: %v", err)
	}
	if _, err := s.pool.Exec(ctxInner,
		"INSERT INTO projection.branch_protection_rules (branch_pattern, rule_order, protections, source_commit) VALUES ('main', 0, '[\"no-delete\",\"no-direct-write\"]', 'bootstrap')",
	); err != nil {
		t.Logf("reseed branch_protection_rules: %v", err)
	}
	for _, table := range tables {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
}

// FindMigrationsDir walks up from the working directory to find the migrations/ folder.
func FindMigrationsDir() string {
	// Try common paths relative to where tests run
	candidates := []string{
		"migrations",
		"../../migrations",
		"../../../migrations",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return "migrations"
}
