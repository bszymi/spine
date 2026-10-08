//go:build integration

package workspace

// INIT-023 TASK-009 AC14 (integration, shared mode): buildServiceSet binds
// a DB-backed workspace store to Config.ID and sweeps it before the
// builder hook runs, so the hook (which wires event delivery in
// cmd/spine) never sees a legacy signing secret.

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
)

// t009WorkspaceDB creates <SPINE_TEST_DATABASE_URL database>_<suffix>,
// migrates it and returns its DSN; the database is dropped afterwards.
func t009WorkspaceDB(t *testing.T, suffix string) string {
	t.Helper()
	ctx := context.Background()
	base := store.TestDSN()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(u.Path, "/") + "_" + suffix
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}
	defer conn.Close(ctx)
	ident := pgx.Identifier{name}.Sanitize()
	_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Skipf("cannot create a test database (CREATEDB needed): %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	})
	u.Path = "/" + name
	dsn := u.String()
	st, err := store.NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
		t.Fatal(err)
	}
	if err := st.ExecRaw(ctx, `INSERT INTO runtime.event_subscriptions
		  (subscription_id, workspace_id, name, target_type, target_url, event_types, signing_secret, status)
		 VALUES ('sub-svc', NULL, 'sub-svc', 'webhook', 'https://hook.invalid', '{}'::text[], 'whsec-plain-svc', 'active')`); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestBuildServiceSet_SweepsBeforeBuilderHook(t *testing.T) {
	dsn := t009WorkspaceDB(t, "t009_svc")
	key := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range key {
		key[i] = 0x70 + byte(i)
	}
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{ID: "ws-svc", DatabaseURL: secrets.NewSecretValue([]byte(dsn)), RepoPath: ".", Status: StatusActive}

	var rawInHook string
	hook := func(ctx context.Context, ss *ServiceSet) error {
		pg, ok := ss.Store.(*store.PostgresStore)
		if !ok {
			return errors.New("store is not a PostgresStore")
		}
		return pg.QueryRowSecret(ctx, "sub-svc", &rawInHook)
	}
	ss, err := buildServiceSet(context.Background(), cfg, hook, c, store.SecretEnvelopeMigrate, nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	t.Cleanup(func() { ss.close("shutdown") })

	if !strings.HasPrefix(rawInHook, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("the builder hook observed a signing secret that was not yet enc:v2")
	}
	aad := spinecrypto.AssociatedData("runtime.event_subscriptions.signing_secret", "ws-svc", "sub-svc")
	if op, err := c.Open(rawInHook, aad); err != nil || op.Plaintext != "whsec-plain-svc" {
		t.Fatalf("not bound to Config.ID: %v", err)
	}
	got, err := ss.Store.GetSubscription(context.Background(), "sub-svc")
	if err != nil || got.SigningSecret != "whsec-plain-svc" {
		t.Fatalf("read through the service set store: %v", err)
	}

	// A bad mode is a binding error: the build fails closed.
	if _, err := buildServiceSet(context.Background(), cfg, nil, c, "bogus", nil, PoolPolicy{}, ""); !errors.Is(err, ErrSubscriptionSecretBinding) {
		t.Fatalf("binding error not surfaced: %v", err)
	}
}

// Review round 1: the configured mode reaches every per-workspace store
// through the real pool path — PoolConfig → NewServicePool → Get →
// initializeEntry → buildServiceSet → PrepareSubscriptionSecrets. With
// strict configured, the store is bound strict: the pre-seeded plaintext
// row is refused and left untouched. A store silently bound in migrate
// (the zero value) would convert it instead.
func TestServicePool_BindsStoresInConfiguredMode(t *testing.T) {
	dsn := t009WorkspaceDB(t, "t009_pool")
	key := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range key {
		key[i] = 0x30 + byte(i)
	}
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-pool": {ID: "ws-pool", DatabaseURL: secrets.NewSecretValue([]byte(dsn)), RepoPath: ".", Status: StatusActive},
	}}
	ctx := context.Background()
	pool := NewServicePool(ctx, resolver, PoolConfig{SecretCipher: c, SecretEnvelopeMode: store.SecretEnvelopeStrict, IdleTimeout: time.Minute})
	t.Cleanup(pool.Close)
	ss, err := pool.Get(ctx, "ws-pool")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { pool.Release("ws-pool") })

	pg, ok := ss.Store.(*store.PostgresStore)
	if !ok {
		t.Fatal("store is not a PostgresStore")
	}
	b, bound := pg.SubscriptionSecretBinding()
	if !bound || b.Mode != store.SecretEnvelopeStrict || b.WorkspaceID != "ws-pool" {
		t.Fatalf("pooled store binding = %+v (bound %v), want strict for ws-pool", b, bound)
	}
	var raw string
	if err := pg.QueryRowSecret(ctx, "sub-svc", &raw); err != nil || raw != "whsec-plain-svc" {
		t.Fatalf("strict sweep converted the legacy row (raw is plaintext: %v): %v", raw == "whsec-plain-svc", err)
	}
	if _, err := ss.Store.GetSubscription(ctx, "sub-svc"); !errors.Is(err, store.ErrLegacySecretRefused) {
		t.Fatalf("legacy row not refused by the pooled strict store: %v", err)
	}
}

// Review round 2: the premise of the platform-binding runbook. A pooled
// workspace is swept only when the pool builds its ServiceSet: a Get for a
// workspace the pool already holds does not sweep, so a row another process
// swept back (here: an enc:v1 value, as a downgrade-mode process writes
// during an overlapping rollout) stays as it is, however many requests
// follow. The first Get after an eviction (idle, binding invalidation — or a
// process restart, which starts with an empty pool) rebuilds and sweeps it.
func TestServicePool_SweepsOnlyWhenItBuilds(t *testing.T) {
	dsn := t009WorkspaceDB(t, "t009_rebuild")
	key := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range key {
		key[i] = 0x50 + byte(i)
	}
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := store.NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	raw := func() string {
		t.Helper()
		var v string
		if err := admin.QueryRowSecret(ctx, "sub-svc", &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v2Prefix := "enc:v2:" + c.PrimaryKeyID() + ":"

	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-rebuild": {ID: "ws-rebuild", DatabaseURL: secrets.NewSecretValue([]byte(dsn)), RepoPath: ".", Status: StatusActive},
	}}
	pool := NewServicePool(ctx, resolver, PoolConfig{SecretCipher: c, SecretEnvelopeMode: store.SecretEnvelopeMigrate, IdleTimeout: time.Hour})
	t.Cleanup(pool.Close)
	get := func() {
		t.Helper()
		if _, err := pool.Get(ctx, "ws-rebuild"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		pool.Release("ws-rebuild")
	}

	get()
	if !strings.HasPrefix(raw(), v2Prefix) {
		t.Fatal("the first build did not sweep the workspace")
	}

	v1, err := c.Encrypt("whsec-plain-svc")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecRaw(ctx, `UPDATE runtime.event_subscriptions SET signing_secret = $1 WHERE subscription_id = 'sub-svc'`, v1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		get()
	}
	if raw() != v1 {
		t.Fatal("a Get for a workspace the pool already holds swept it; the runbook's restart step would be unnecessary")
	}

	pool.Evict("ws-rebuild")
	get()
	if !strings.HasPrefix(raw(), v2Prefix) {
		t.Fatal("the first Get after an eviction did not rebuild and sweep the workspace")
	}
}
