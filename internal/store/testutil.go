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
		"runtime.runs",
		"auth.actor_skills",
		"auth.tokens",
		"auth.skills",
		"auth.actors",
		"projection.artifact_links",
		"projection.artifacts",
		"projection.workflows",
		"projection.sync_state",
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
