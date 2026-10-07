//go:build integration

package store_test

// INIT-023 TASK-009 AC11 against real Postgres: the re-encryption sweep's
// compare-and-swap never overwrites a value another writer changed after
// the sweep read it (I5).

import (
	"context"
	"errors"
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
// its write. It matches the statement's SET/WHERE prefix, not the
// compare-and-swap clause itself, so the AC11 mutation (dropping
// "AND signing_secret = $3") still triggers the concurrent write and
// fails on the overwrite.
type casHookQuerier struct {
	*pgxpool.Pool
	before func(ctx context.Context)
}

func (q *casHookQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if q.before != nil && strings.Contains(sql, "SET signing_secret = $2 WHERE subscription_id = $1") {
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

// ReplaceSubscriptionSecret against real Postgres: a refused row (here a
// legacy plaintext row in strict mode) is replaced without being opened,
// and updated_at moves.
func TestReplaceSubscriptionSecret_RealPostgres(t *testing.T) {
	admin, pool, c := t009Setup(t)
	s := store.NewPostgresStoreWithQuerier(pool)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "ws-int", Mode: store.SecretEnvelopeStrict}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubscription(context.Background(), "t009-sub-1"); !errors.Is(err, store.ErrLegacySecretRefused) {
		t.Fatalf("fixture: %v", err)
	}
	var before time.Time
	if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM runtime.event_subscriptions WHERE subscription_id = 't009-sub-1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSubscriptionSecret(context.Background(), "t009-sub-1", "whsec_rotated"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSubscription(context.Background(), "t009-sub-1")
	if err != nil || got.SigningSecret != "whsec_rotated" || !got.UpdatedAt.After(before) {
		t.Fatalf("after replace: %v", err)
	}
	if raw := t009RawSecret(t, admin); !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("replacement not sealed as enc:v2")
	}
	if err := s.ReplaceSubscriptionSecret(context.Background(), "t009-missing", "whsec_x"); err == nil {
		t.Fatal("replacing a missing subscription succeeded")
	}
}

// Review round 1 (P2) against real Postgres: a bound read-modify-write
// that keeps the secret (the gateway's pause handler) writes the other
// columns with the keep-secret UPDATE and leaves a planted legacy value's
// bytes alone, so the next sweep — not the update — converts it, and
// records it.
func TestBoundUpdate_RealPostgresKeepsUnchangedSecret(t *testing.T) {
	admin, pool, c := t009Setup(t)
	ctx := context.Background()
	s := store.NewPostgresStoreWithQuerier(pool)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "ws-int"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReencryptSubscriptionSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	plant, err := c.Encrypt("whsec_A_receiver_key") // another workspace's pre-upgrade enc:v1 value
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecRaw(ctx, `UPDATE runtime.event_subscriptions SET signing_secret = $1 WHERE subscription_id = 't009-sub-1'`, plant); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM runtime.event_subscriptions WHERE subscription_id = 't009-sub-1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	sub, err := s.GetSubscription(ctx, "t009-sub-1")
	if err != nil || sub.SigningSecret != "whsec_A_receiver_key" {
		t.Fatalf("fixture read (migrate accepts legacy): %v", err)
	}
	sub.Status = "paused"
	sub.Name = "t009-cas-renamed"
	if err := s.UpdateSubscription(ctx, sub); err != nil {
		t.Fatalf("update: %v", err)
	}
	if raw := t009RawSecret(t, admin); raw != plant {
		t.Fatal("a read-modify-write re-sealed the planted legacy value: an unrecorded conversion")
	}
	got, err := s.GetSubscription(ctx, "t009-sub-1")
	if err != nil || got.Status != "paused" || got.Name != "t009-cas-renamed" || !got.UpdatedAt.After(before) {
		t.Fatalf("other columns not written: %+v %v", got, err)
	}

	rep, err := s.ReencryptSubscriptionSecrets(ctx)
	if conv, ok := t009Converted(rep); err != nil || !ok || conv.From != spinecrypto.EnvelopeV1 {
		t.Fatalf("the sweep must convert and record the plant: %+v %v", rep, err)
	}

	// codex review: a rotation between the caller's read and its update
	// survives the update; the stale secret the caller read is not
	// re-sealed over it.
	stale, err := s.GetSubscription(ctx, "t009-sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSubscriptionSecret(ctx, "t009-sub-1", "whsec_rotated"); err != nil {
		t.Fatal(err)
	}
	rotatedRaw := t009RawSecret(t, admin)
	stale.Status = "active"
	if err := s.UpdateSubscription(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if t009RawSecret(t, admin) != rotatedRaw {
		t.Fatal("the update re-sealed a stale secret over a concurrent rotation")
	}
	got, err = s.GetSubscription(ctx, "t009-sub-1")
	if err != nil || got.SigningSecret != "whsec_rotated" || got.Status != "active" {
		t.Fatalf("after the racing update: %v", err)
	}

	// A changed secret is written (sealed for the binding) by the full
	// UPDATE.
	got.SigningSecret = "whsec_new"
	if err := s.UpdateSubscription(ctx, got); err != nil {
		t.Fatal(err)
	}
	if raw := t009RawSecret(t, admin); !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("changed secret not sealed as enc:v2")
	}
	if again, err := s.GetSubscription(ctx, "t009-sub-1"); err != nil || again.SigningSecret != "whsec_new" {
		t.Fatalf("read-back: %v", err)
	}

	// codex review (round 2): the same value, reused after that set, is
	// judged against what the set wrote. Setting the secret back to the
	// value the read returned writes it (a stale snapshot judged it kept
	// and left whsec_new in the row).
	got.SigningSecret = "whsec_rotated"
	if err := s.UpdateSubscription(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, err := s.GetSubscription(ctx, "t009-sub-1"); err != nil || again.SigningSecret != "whsec_rotated" {
		t.Fatalf("setting the secret back did not write it: %v", err)
	}
}
