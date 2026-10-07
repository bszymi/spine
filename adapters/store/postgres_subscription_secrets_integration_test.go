//go:build integration

package store_test

// INIT-023 TASK-009 AC11 against real Postgres: the re-encryption sweep's
// compare-and-swap never overwrites a value another writer changed after
// the sweep read it (I5).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// casHookQuerier is a *pgxpool.Pool whose Exec runs before() ahead of
// the sweep's compare-and-swap UPDATE, i.e. between the sweep's read and
// its write.
type casHookQuerier struct {
	*pgxpool.Pool
	before func(ctx context.Context)
}

func (q *casHookQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if q.before != nil && strings.Contains(sql, "AND signing_secret = $3") {
		f := q.before
		q.before = nil
		f(ctx)
	}
	return q.Pool.Exec(ctx, sql, args...)
}

const t009Purpose = "runtime.event_subscriptions.signing_secret"

func t009Setup(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, *spinecrypto.SecretCipher) {
	t.Helper()
	ctx := context.Background()
	admin := store.NewTestStore(t) // applies migrations; skips without a database
	cleanup := func() {
		_ = admin.ExecRaw(context.Background(), `DELETE FROM runtime.event_subscriptions WHERE subscription_id LIKE 't009-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	pool, err := pgxpool.New(ctx, store.TestDSN())
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	key := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range key {
		key[i] = byte(0x90 + i)
	}
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	// A plaintext row, as a deployment that ran without a key wrote it.
	if err := admin.ExecRaw(ctx, `INSERT INTO runtime.event_subscriptions
		  (subscription_id, workspace_id, name, target_type, target_url, event_types, signing_secret, status)
		 VALUES ('t009-sub-1', NULL, 't009-cas', 'webhook', 'https://hook.invalid', '{}'::text[], 'old-plaintext', 'active')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return admin, pool, c
}

func t009RawSecret(t *testing.T, admin *store.PostgresStore) string {
	t.Helper()
	var raw string
	if err := admin.QueryRowSecret(context.Background(), "t009-sub-1", &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// t009Converted reports whether the sweep rewrote t009-sub-1. The
// shared test database may hold other packages' subscription rows, so
// assertions look at this test's row rather than at global counters.
func t009Converted(rep store.SecretSweepReport) (store.SecretSweepConversion, bool) {
	for _, c := range rep.Converted {
		if c.SubscriptionID == "t009-sub-1" {
			return c, true
		}
	}
	return store.SecretSweepConversion{}, false
}

func t009Assert(t *testing.T, admin, s *store.PostgresStore, rep store.SecretSweepReport, err error, rotated string) {
	t.Helper()
	if _, converted := t009Converted(rep); err != nil || converted || rep.Conflicts < 1 || rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if raw := t009RawSecret(t, admin); raw != rotated {
		t.Fatal("the sweep overwrote the concurrently rotated value")
	}
	got, err := s.GetSubscription(context.Background(), "t009-sub-1")
	if err != nil || got.SigningSecret != "rotated" {
		t.Fatalf("read-back: %v", err)
	}
}

// The concurrent writer commits before the sweep's UPDATE runs.
func TestSweepCAS_ConcurrentCommitWins(t *testing.T) {
	admin, pool, c := t009Setup(t)
	rotated, err := c.Seal("rotated", spinecrypto.AssociatedData(t009Purpose, "ws-int", "t009-sub-1"))
	if err != nil {
		t.Fatal(err)
	}
	q := &casHookQuerier{Pool: pool}
	q.before = func(ctx context.Context) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.event_subscriptions SET signing_secret = $1 WHERE subscription_id = 't009-sub-1'`, rotated); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s := store.NewPostgresStoreWithQuerier(q)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "ws-int"}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.ReencryptSubscriptionSecrets(context.Background())
	t009Assert(t, admin, s, rep, err, rotated)
}

// Lock-wait variant: the concurrent writer still holds the row lock when
// the sweep's UPDATE starts, and commits only once that UPDATE is seen
// waiting on the lock. Postgres then re-evaluates the UPDATE's WHERE
// clause against the committed row version, which no longer carries the
// value the sweep read, so zero rows are affected.
func TestSweepCAS_LockWaitThenConcurrentCommitWins(t *testing.T) {
	admin, pool, c := t009Setup(t)
	rotated, err := c.Seal("rotated", spinecrypto.AssociatedData(t009Purpose, "ws-int", "t009-sub-1"))
	if err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	q := &casHookQuerier{Pool: pool}
	q.before = func(ctx context.Context) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.event_subscriptions SET signing_secret = $1 WHERE subscription_id = 't009-sub-1'`, rotated); err != nil {
			t.Fatal(err)
		}
		go func() {
			committed <- commitOnceSweepWaits(pool, tx)
		}()
	}
	s := store.NewPostgresStoreWithQuerier(q)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "ws-int"}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.ReencryptSubscriptionSecrets(context.Background())
	if cerr := <-committed; cerr != nil {
		t.Fatalf("concurrent writer: %v", cerr)
	}
	t009Assert(t, admin, s, rep, err, rotated)
}

// commitOnceSweepWaits polls pg_stat_activity until the sweep's
// compare-and-swap UPDATE is blocked on a lock, then commits tx.
func commitOnceSweepWaits(pool *pgxpool.Pool, tx pgx.Tx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		var waiting int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE 'UPDATE runtime.event_subscriptions SET signing_secret = $2%'`).Scan(&waiting)
		if err != nil {
			_ = tx.Rollback(context.Background())
			return err
		}
		if waiting > 0 {
			return tx.Commit(ctx)
		}
		select {
		case <-ctx.Done():
			_ = tx.Rollback(context.Background())
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A bound sweep against real Postgres converts a plaintext row and is
// idempotent; the unbound integration test
// (TestEventSubscription_SigningSecretEncryption) stays the legacy guard.
func TestSweep_RealPostgresConvertsAndIsIdempotent(t *testing.T) {
	admin, pool, c := t009Setup(t)
	s := store.NewPostgresStoreWithQuerier(pool)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "ws-int"}); err != nil {
		t.Fatal(err)
	}
	var updatedBefore, updatedAfter time.Time
	if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM runtime.event_subscriptions WHERE subscription_id = 't009-sub-1'`).Scan(&updatedBefore); err != nil {
		t.Fatal(err)
	}
	rep, err := s.ReencryptSubscriptionSecrets(context.Background())
	if conv, ok := t009Converted(rep); err != nil || !ok || conv.From != spinecrypto.EnvelopePlaintext {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	raw := t009RawSecret(t, admin)
	if !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("row is not enc:v2 under the primary key")
	}
	if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM runtime.event_subscriptions WHERE subscription_id = 't009-sub-1'`).Scan(&updatedAfter); err != nil {
		t.Fatal(err)
	}
	if !updatedAfter.Equal(updatedBefore) {
		t.Fatal("the sweep bumped updated_at; the secret itself did not change")
	}
	got, err := s.GetSubscription(context.Background(), "t009-sub-1")
	if err != nil || got.SigningSecret != "old-plaintext" {
		t.Fatalf("read-back: %v", err)
	}
	rep, err = s.ReencryptSubscriptionSecrets(context.Background())
	if _, again := t009Converted(rep); err != nil || again || rep.Current < 1 {
		t.Fatalf("second sweep: %+v %v", rep, err)
	}
	if t009RawSecret(t, admin) != raw {
		t.Fatal("second sweep rewrote a current row")
	}
}
