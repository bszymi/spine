package harness

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/store"
)

// TestDB wraps a test PostgreSQL store for scenario testing.
type TestDB struct {
	Store *store.PostgresStore
}

// NewTestDB creates a test database with migrations applied.
// Skips the test if the database is not available.
func NewTestDB(t *testing.T) *TestDB {
	t.Helper()

	return &TestDB{
		Store: store.NewTestStore(t),
	}
}

// Cleanup removes all test data from runtime and projection tables,
// including sync state.
func (db *TestDB) Cleanup(ctx context.Context, t *testing.T) {
	t.Helper()
	db.Store.CleanupTestData(ctx, t)
}
