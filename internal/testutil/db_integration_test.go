//go:build integration

package testutil_test

import (
	"testing"

	"github.com/bszymi/spine/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestNewTestConn(t *testing.T) {
	conn := testutil.NewTestConn(t)

	// Verify connection works
	var result int
	err := conn.QueryRow(t.Context(), "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if result != 1 {
		t.Fatalf("expected 1, got %d", result)
	}
}

func TestWithTestTx(t *testing.T) {
	conn := testutil.NewTestConn(t)

	testutil.WithTestTx(t, conn, func(tx pgx.Tx) {
		// Create a temp table inside the transaction
		_, err := tx.Exec(t.Context(), "CREATE TEMP TABLE test_tx (id int)")
		if err != nil {
			t.Fatalf("create table: %v", err)
		}

		_, err = tx.Exec(t.Context(), "INSERT INTO test_tx VALUES (42)")
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		var val int
		err = tx.QueryRow(t.Context(), "SELECT id FROM test_tx").Scan(&val)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if val != 42 {
			t.Fatalf("expected 42, got %d", val)
		}
	})
	// Transaction is rolled back after WithTestTx — temp table no longer exists
}
