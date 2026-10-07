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
