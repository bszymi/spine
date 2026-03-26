package harness

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/store"
)

// TestDB wraps a test PostgreSQL store for scenario testing.
//
// Each TestDB connects to a shared PostgreSQL instance (via docker-compose.test.yaml)
// with migrations applied automatically. Isolation between concurrent tests is
// achieved through cleanup — each scenario cleans its own data via Cleanup().
//
// The shared instance approach avoids the overhead of per-test database creation
// while maintaining isolation through data cleanup. Per Constitution §8 (Disposable
// Database), the database is an accelerator — all durable state lives in Git.
type TestDB struct {
	Store *store.PostgresStore
}

// NewTestDB creates a test database with migrations applied.
// Skips the test if the database is not available.
// The store connection is automatically closed when the test ends.
func NewTestDB(t *testing.T) *TestDB {
	t.Helper()

	return &TestDB{
		Store: store.NewTestStore(t),
	}
}

// Cleanup removes all test data from runtime and projection tables,
// including sync state. Call this in t.Cleanup() or defer to ensure
// data isolation between tests.
func (db *TestDB) Cleanup(ctx context.Context, t *testing.T) {
	t.Helper()
	db.Store.CleanupTestData(ctx, t)
}

// Ping verifies the database connection is alive.
func (db *TestDB) Ping(ctx context.Context) error {
	return db.Store.Ping(ctx)
}
