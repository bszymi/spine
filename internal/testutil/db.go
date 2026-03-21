package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

const defaultTestDSN = "postgres://spine_test:spine_test@localhost:5433/spine_test?sslmode=disable"

// TestDSN returns the database connection string for integration tests.
// Uses SPINE_TEST_DATABASE_URL env var if set, otherwise defaults to
// the test compose DB on port 5433.
func TestDSN() string {
	if dsn := os.Getenv("SPINE_TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return defaultTestDSN
}

// NewTestConn opens a database connection for integration tests.
// The connection is closed when the test ends.
// Skips the test if the database is not available.
func NewTestConn(t *testing.T) *pgx.Conn {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, TestDSN())
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close(ctx)
	})

	return conn
}

// WithTestTx runs a function within a database transaction that is
// rolled back after the test completes. This ensures test isolation.
func WithTestTx(t *testing.T, conn *pgx.Conn, fn func(tx pgx.Tx)) {
	t.Helper()

	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	t.Cleanup(func() {
		_ = tx.Rollback(ctx)
	})

	fn(tx)
}
