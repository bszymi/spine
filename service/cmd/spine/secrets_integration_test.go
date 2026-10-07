//go:build integration

package main

// INIT-023 TASK-009 integration tests for the standalone wiring:
//   - AC14: buildStore (file resolver) binds the store to the resolver's
//     workspace id and sweeps before it returns.
//   - AC14, review round 2: buildServeWorkspaces — the start-up step
//     serveCmd calls — binds every store in the configured mode, end to
//     end from the environment, for the file, db and platform-binding
//     resolvers.
//   - AC23: `spine secrets reencrypt --all-workspaces` over a registry of
//     workspaces on separate databases (one inactive and reachable, one
//     inactive whose database is gone), and the single-workspace path.
//
// Each test creates its own databases next to SPINE_TEST_DATABASE_URL
// (named <that database>_<suffix>) and drops them afterwards, so the
// sweep only ever sees this test's rows. Without CREATEDB the tests skip.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/jackc/pgx/v5"
)

const t009Purpose = "runtime.event_subscriptions.signing_secret"

// t009CreateDB creates <base database>_<suffix> and returns its DSN.
func t009CreateDB(t *testing.T, suffix string) string {
	t.Helper()
	ctx := context.Background()
	base := store.TestDSN()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse SPINE_TEST_DATABASE_URL: %v", err)
	}
	name := strings.TrimPrefix(u.Path, "/") + "_" + suffix
	if len(name) > 63 {
		name = name[:63]
	}
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
	return u.String()
}

func t009Migrate(t *testing.T, dsn, dir string) *store.PostgresStore {
	t.Helper()
	st, err := store.NewPostgresStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.ApplyMigrations(context.Background(), dir); err != nil {
		t.Fatalf("migrate %s: %v", dir, err)
	}
	return st
}

func t009Insert(t *testing.T, st *store.PostgresStore, id, secret string) {
	t.Helper()
	if err := st.ExecRaw(context.Background(), `INSERT INTO runtime.event_subscriptions
		  (subscription_id, workspace_id, name, target_type, target_url, event_types, signing_secret, status)
		 VALUES ($1, NULL, $1, 'webhook', 'https://hook.invalid', '{}'::text[], $2, 'active')`, id, secret); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func t009Raw(t *testing.T, st *store.PostgresStore, id string) string {
	t.Helper()
	var raw string
	if err := st.QueryRowSecret(context.Background(), id, &raw); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return raw
}

func t009Key(seed byte) []byte {
	k := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

// clearSecretBackendEnv makes buildFileResolverSecretClient use the
// SPINE_DATABASE_URL env fallback, as a plain single-workspace deployment.
func clearSecretBackendEnv(t *testing.T) {
	t.Setenv("SPINE_SECRET_PROVIDER", "")
	t.Setenv("SPINE_SECRET_FILE_ROOT", "")
	t.Setenv("WORKSPACE_RESOLVER", "")
	t.Setenv("SPINE_ENV", "")
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "")
	t.Setenv("SPINE_INSECURE_LOCAL", "1")
}

// AC14 (integration, file mode): a pre-seeded plaintext row is enc:v2
// bound to SPINE_WORKSPACE_ID once buildStore returns.
func TestBuildStore_FileResolverBindsAndSweeps(t *testing.T) {
	dsn := t009CreateDB(t, "t009_bs")
	admin := t009Migrate(t, dsn, store.FindMigrationsDir())
	t009Insert(t, admin, "sub-file", "whsec-plain-file")

	clearSecretBackendEnv(t)
	t.Setenv("SPINE_WORKSPACE_ID", "ws-int")
	t.Setenv("SPINE_DATABASE_URL", dsn)
	ctx := context.Background()
	sc, err := buildFileResolverSecretClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := spinecrypto.NewSecretCipher(t009Key(0x10))
	if err != nil {
		t.Fatal(err)
	}
	st, err := buildStore(ctx, workspace.NewFileProvider(sc), c, store.SecretEnvelopeMigrate, slog.New(slog.DiscardHandler))
	if err != nil || st == nil {
		t.Fatalf("buildStore: %v", err)
	}
	t.Cleanup(st.(*store.PostgresStore).Close)

	raw := t009Raw(t, admin, "sub-file")
	if !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("row not converted to enc:v2 by buildStore")
	}
	if op, err := c.Open(raw, spinecrypto.AssociatedData(t009Purpose, "ws-int", "sub-file")); err != nil || op.Plaintext != "whsec-plain-file" {
		t.Fatalf("row not bound to SPINE_WORKSPACE_ID: %v", err)
	}
	if _, err := c.Open(raw, spinecrypto.AssociatedData(t009Purpose, "default", "sub-file")); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("row opened under the default workspace id: %v", err)
	}
	got, err := st.GetSubscription(ctx, "sub-file")
	if err != nil || got.SigningSecret != "whsec-plain-file" {
		t.Fatalf("read through the bound store: %v", err)
	}
}

// Review round 1: buildStore binds in the mode it is given, not the zero
// value. With strict, the pre-seeded plaintext row is refused and left
// untouched; a store silently bound in migrate would convert it.
func TestBuildStore_FileResolverBindsConfiguredMode(t *testing.T) {
	dsn := t009CreateDB(t, "t009_bsm")
	admin := t009Migrate(t, dsn, store.FindMigrationsDir())
	t009Insert(t, admin, "sub-file", "whsec-plain-file")

	clearSecretBackendEnv(t)
	t.Setenv("SPINE_WORKSPACE_ID", "ws-int")
	t.Setenv("SPINE_DATABASE_URL", dsn)
	ctx := context.Background()
	sc, err := buildFileResolverSecretClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := spinecrypto.NewSecretCipher(t009Key(0x20))
	if err != nil {
		t.Fatal(err)
	}
	st, err := buildStore(ctx, workspace.NewFileProvider(sc), c, store.SecretEnvelopeStrict, slog.New(slog.DiscardHandler))
	if err != nil || st == nil {
		t.Fatalf("buildStore: %v", err)
	}
	pg := st.(*store.PostgresStore)
	t.Cleanup(pg.Close)
	if b, ok := pg.SubscriptionSecretBinding(); !ok || b.Mode != store.SecretEnvelopeStrict || b.WorkspaceID != "ws-int" {
		t.Fatalf("buildStore binding = %+v (bound %v), want strict for ws-int", b, ok)
	}
	if raw := t009Raw(t, admin, "sub-file"); raw != "whsec-plain-file" {
		t.Fatal("a strict buildStore converted the legacy row")
	}
	if _, err := st.GetSubscription(ctx, "sub-file"); !errors.Is(err, store.ErrLegacySecretRefused) {
		t.Fatalf("legacy row not refused: %v", err)
	}
}

// t009AssertBoundStrict checks that st is bound strict to wsID and that the
// plaintext row subID seeded before start-up was refused and left untouched
// by the start-up sweep. A store bound in migrate — what a dropped mode
// argument produces, since "" means migrate — would have converted it.
func t009AssertBoundStrict(t *testing.T, st store.Store, admin *store.PostgresStore, wsID, subID, plain string) {
	t.Helper()
	pg, ok := st.(*store.PostgresStore)
	if !ok {
		t.Fatalf("store is %T, not a PostgresStore", st)
	}
	if b, bound := pg.SubscriptionSecretBinding(); !bound || b.Mode != store.SecretEnvelopeStrict || b.WorkspaceID != wsID {
		t.Fatalf("store binding = %+v (bound %v), want strict for %s", b, bound, wsID)
	}
	if t009Raw(t, admin, subID) != plain {
		t.Fatal("the start-up sweep converted the legacy row: the store was not bound strict")
	}
	if _, err := st.GetSubscription(context.Background(), subID); !errors.Is(err, store.ErrLegacySecretRefused) {
		t.Fatalf("legacy row not refused by the strict store: %v", err)
	}
}

// t009ServeEnv sets the environment every TestBuildServeWorkspaces_* test
// shares: a key, SPINE_SECRET_ENVELOPE_MODE=strict, and no secret backend.
func t009ServeEnv(t *testing.T, seed byte) {
	t.Helper()
	clearSecretBackendEnv(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(t009Key(seed)))
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "strict")
}

// Review round 2 (AC14, file mode): buildServeWorkspaces, from the
// environment, binds the process-level store strict to SPINE_WORKSPACE_ID.
func TestBuildServeWorkspaces_FileResolverBindsConfiguredMode(t *testing.T) {
	dsn := t009CreateDB(t, "t009_srv_file")
	admin := t009Migrate(t, dsn, store.FindMigrationsDir())
	t009Insert(t, admin, "sub-file", "whsec-plain-file")

	t009ServeEnv(t, 0x61)
	t.Setenv("SPINE_WORKSPACE_ID", "ws-srv-file")
	t.Setenv("SPINE_DATABASE_URL", dsn)
	ws, err := buildServeWorkspaces(context.Background(), "development", slog.New(slog.DiscardHandler), workspaceDeliveryConfig{}, "", engine.StrictStepEntryPolicy())
	if err != nil {
		t.Fatalf("buildServeWorkspaces: %v", err)
	}
	t.Cleanup(ws.Close)
	if ws.Wiring.Pool != nil || ws.Store == nil {
		t.Fatalf("file wiring: pool %v, process store %v", ws.Wiring.Pool != nil, ws.Store != nil)
	}
	t009AssertBoundStrict(t, ws.Store, admin, "ws-srv-file", "sub-file", "whsec-plain-file")
}

// Review round 2 (AC14, db mode): buildServeWorkspaces with
// WORKSPACE_RESOLVER=db builds the registry-backed pool, and a registry
// workspace's store — built by Pool.Get exactly as a request builds it — is
// bound strict to its registry id.
func TestBuildServeWorkspaces_DBResolverBindsConfiguredMode(t *testing.T) {
	regDSN := t009CreateDB(t, "t009_srv_reg")
	wsDSN := t009CreateDB(t, "t009_srv_db")
	t009Migrate(t, regDSN, filepath.Join(store.FindMigrationsDir(), "registry"))
	admin := t009Migrate(t, wsDSN, store.FindMigrationsDir())
	t009Insert(t, admin, "sub-db", "whsec-plain-db")
	ctx := context.Background()
	provider, err := workspace.NewDBProvider(ctx, regDSN, workspace.DBProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.CreateWorkspace(ctx, workspace.Config{
		ID: "ws-srv-db", DisplayName: "ws-srv-db", DatabaseURL: secrets.NewSecretValue([]byte(wsDSN)),
		RepoPath: ".", Status: workspace.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	provider.Close()

	t009ServeEnv(t, 0x62)
	t.Setenv("WORKSPACE_RESOLVER", "db")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", regDSN)
	ws, err := buildServeWorkspaces(ctx, "development", slog.New(slog.DiscardHandler), workspaceDeliveryConfig{}, "", engine.StrictStepEntryPolicy())
	if err != nil {
		t.Fatalf("buildServeWorkspaces: %v", err)
	}
	t.Cleanup(ws.Close)
	if ws.Wiring.Pool == nil || ws.Wiring.DBProvider == nil || ws.Store != nil {
		t.Fatalf("db wiring: pool %v, registry %v, process store %v", ws.Wiring.Pool != nil, ws.Wiring.DBProvider != nil, ws.Store != nil)
	}
	ss, err := ws.Wiring.Pool.Get(ctx, "ws-srv-db")
	if err != nil {
		t.Fatalf("Pool.Get: %v", err)
	}
	t.Cleanup(func() { ws.Wiring.Pool.Release("ws-srv-db") })
	t009AssertBoundStrict(t, ss.Store, admin, "ws-srv-db", "sub-db", "whsec-plain-db")
}

// Review round 2 (AC14, platform-binding mode): buildServeWorkspaces with
// WORKSPACE_RESOLVER=platform-binding builds the binding-backed pool; a
// workspace resolved from a stand-in platform (its runtime database
// reference dereferenced through the file secret backend) is bound strict
// to its platform workspace id once Pool.Get builds it.
func TestBuildServeWorkspaces_PlatformBindingBindsConfiguredMode(t *testing.T) {
	const wsID = "ws-srv-pb"
	wsDSN := t009CreateDB(t, "t009_srv_pb")
	admin := t009Migrate(t, wsDSN, store.FindMigrationsDir())
	t009Insert(t, admin, "sub-pb", "whsec-plain-pb")

	root := t.TempDir()
	dir := filepath.Join(root, "workspaces", wsID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for purpose, value := range map[string]string{
		secrets.PurposeRuntimeDB:    wsDSN,
		secrets.PurposeProjectionDB: wsDSN,
		secrets.PurposeGit:          "unused-git-credential",
	} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, purpose+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer svc-token" || r.URL.Path != "/api/v1/internal/workspaces/"+wsID+"/runtime-binding" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.PlatformBinding{
			WorkspaceID:      wsID,
			DisplayName:      wsID,
			SpineWorkspaceID: wsID + "-smp",
			DeploymentMode:   "shared",
			RepoPath:         ".",
			ActorScope:       "workspace:" + wsID,
			RuntimeDBRef:     secrets.WorkspaceRef(wsID, secrets.PurposeRuntimeDB),
			ProjectionDBRef:  secrets.WorkspaceRef(wsID, secrets.PurposeProjectionDB),
			GitRef:           secrets.WorkspaceRef(wsID, secrets.PurposeGit),
		})
	}))
	t.Cleanup(platform.Close)

	t009ServeEnv(t, 0x63)
	t.Setenv("WORKSPACE_RESOLVER", "platform-binding")
	t.Setenv("SPINE_PLATFORM_URL", platform.URL)
	t.Setenv("SPINE_PLATFORM_SERVICE_TOKEN", "svc-token")
	t.Setenv("SPINE_SECRET_PROVIDER", "file")
	t.Setenv("SPINE_SECRET_FILE_ROOT", root)
	ctx := context.Background()
	ws, err := buildServeWorkspaces(ctx, "development", slog.New(slog.DiscardHandler), workspaceDeliveryConfig{}, "", engine.StrictStepEntryPolicy())
	if err != nil {
		t.Fatalf("buildServeWorkspaces: %v", err)
	}
	t.Cleanup(ws.Close)
	if ws.Wiring.Pool == nil || ws.Wiring.BindingInvalidationHandler == nil || ws.Store != nil {
		t.Fatalf("platform-binding wiring: pool %v, invalidation handler %v, process store %v",
			ws.Wiring.Pool != nil, ws.Wiring.BindingInvalidationHandler != nil, ws.Store != nil)
	}
	ss, err := ws.Wiring.Pool.Get(ctx, wsID)
	if err != nil {
		t.Fatalf("Pool.Get: %v", err)
	}
	t.Cleanup(func() { ws.Wiring.Pool.Release(wsID) })
	t009AssertBoundStrict(t, ss.Store, admin, wsID, "sub-pb", "whsec-plain-pb")
}

// captureDefaultLog routes slog.Default (observe.Logger) into buf for the
// test's duration.
func captureDefaultLog(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func runReencrypt(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := secretsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"reencrypt"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// AC23 (integration): workspace one holds plaintext and enc:v1 rows;
// workspace two holds one row that is an enc:v2 value sealed for
// workspace one (a transplant); workspace three is INACTIVE and holds an
// enc:v1 row (codex review: an inactive workspace still needs the key, so
// it is swept too). The command converts one and three, refuses the
// transplant untouched and exits non-zero; after the bad row is deleted
// it exits 0. Nothing it prints or logs carries a secret or stored value.
func TestSecretsReencrypt_AllWorkspaces(t *testing.T) {
	regDSN := t009CreateDB(t, "t009_reg")
	ws1DSN := t009CreateDB(t, "t009_ws1")
	ws2DSN := t009CreateDB(t, "t009_ws2")
	ws3DSN := t009CreateDB(t, "t009_ws3")
	t009Migrate(t, regDSN, filepath.Join(store.FindMigrationsDir(), "registry"))
	ws1 := t009Migrate(t, ws1DSN, store.FindMigrationsDir())
	ws2 := t009Migrate(t, ws2DSN, store.FindMigrationsDir())
	ws3 := t009Migrate(t, ws3DSN, store.FindMigrationsDir())

	ctx := context.Background()
	provider, err := workspace.NewDBProvider(ctx, regDSN, workspace.DBProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// ws-four is a decommissioned tenant: inactive, its database dropped
	// (never created here), its registry row kept.
	goneDSN := t009MissingDB(t, ws1DSN, "t009_gone")
	for id, dsn := range map[string]string{"ws-one": ws1DSN, "ws-two": ws2DSN, "ws-three": ws3DSN, "ws-four": goneDSN} {
		if err := provider.CreateWorkspace(ctx, workspace.Config{
			ID: id, DisplayName: id, DatabaseURL: secrets.NewSecretValue([]byte(dsn)),
			RepoPath: ".", Status: workspace.StatusActive,
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	for _, id := range []string{"ws-three", "ws-four"} {
		if err := provider.DeactivateWorkspace(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	provider.Close()

	key := t009Key(0x40)
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := c.Encrypt("whsec-LEAK-v1")
	transplant, _ := c.Seal("whsec-LEAK-transplant", spinecrypto.AssociatedData(t009Purpose, "ws-one", "sub-t"))
	t009Insert(t, ws1, "sub-p", "whsec-LEAK-plain")
	t009Insert(t, ws1, "sub-v1", v1)
	t009Insert(t, ws2, "sub-t", transplant)
	v1Inactive, _ := c.Encrypt("whsec-LEAK-inactive")
	t009Insert(t, ws3, "sub-i", v1Inactive)

	clearSecretBackendEnv(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", regDSN)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	t.Setenv("WORKSPACE_RESOLVER", "db")
	var logs bytes.Buffer
	captureDefaultLog(t, &logs)

	out, err := runReencrypt(t, "--all-workspaces")
	t.Logf("first run (exit error: %v):\n%s", err, out)
	if err == nil {
		t.Fatalf("expected a non-zero exit with a refused row; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "ws-two") || !strings.Contains(err.Error(), "ws-four") || strings.Contains(err.Error(), "ws-one") || strings.Contains(err.Error(), "ws-three") {
		t.Fatalf("error should name only the unclean workspaces: %v", err)
	}
	for _, want := range []string{
		"workspace ws-one: clean ",
		"rewritten=2",
		"workspace ws-two: NOT CLEAN ",
		`workspace ws-two: refused subscription "sub-t" envelope=enc:v2 key_id="` + c.PrimaryKeyID() + `"`,
		"workspace ws-three (inactive): clean scanned=1 rewritten=1",
		"workspace ws-four (inactive): unreachable: connect failed: ",
		"workspace ws-four (inactive): NOT CLEAN (unreachable)",
		"summary: workspaces=4 clean=2 not_clean=2 acknowledged_unreachable=0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	for _, id := range []string{"sub-p", "sub-v1"} {
		raw := t009Raw(t, ws1, id)
		if !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
			t.Fatalf("ws-one %s not converted", id)
		}
		if _, err := c.Open(raw, spinecrypto.AssociatedData(t009Purpose, "ws-one", id)); err != nil {
			t.Fatalf("ws-one %s not bound to ws-one: %v", id, err)
		}
	}
	if t009Raw(t, ws2, "sub-t") != transplant {
		t.Fatal("the refused transplant was modified")
	}
	if _, err := c.Open(t009Raw(t, ws3, "sub-i"), spinecrypto.AssociatedData(t009Purpose, "ws-three", "sub-i")); err != nil {
		t.Fatalf("inactive workspace row not converted for ws-three: %v", err)
	}
	assertNoSecretMaterial(t, out+logs.String(), key, v1, transplant, v1Inactive)

	if err := ws2.ExecRaw(ctx, `DELETE FROM runtime.event_subscriptions WHERE subscription_id = 'sub-t'`); err != nil {
		t.Fatal(err)
	}
	// The decommissioned inactive workspace alone still keeps the run
	// unclean until the operator acknowledges it by name.
	out2, err := runReencrypt(t, "--all-workspaces")
	if err == nil || !strings.Contains(err.Error(), "ws-four") || strings.Contains(err.Error(), "ws-two") {
		t.Fatalf("second run should fail on the unreachable workspace only: %v\n%s", err, out2)
	}
	out3, err := runReencrypt(t, "--all-workspaces", "--allow-unreachable-inactive=ws-four,ws-nope")
	if err != nil {
		t.Fatalf("third run should be clean: %v\n%s", err, out3)
	}
	for _, want := range []string{
		"workspace ws-one: clean scanned=2 rewritten=0 current=2",
		"workspace ws-two: clean scanned=0",
		"workspace ws-three (inactive): clean scanned=1 rewritten=0 current=1",
		"workspace ws-four (inactive): unreachable, ACKNOWLEDGED by --allow-unreachable-inactive",
		`note: --allow-unreachable-inactive "ws-nope" matched no unreachable inactive workspace; ignored`,
		"summary: workspaces=4 clean=3 not_clean=0 acknowledged_unreachable=1 (ws-four)",
	} {
		if !strings.Contains(out3, want) {
			t.Fatalf("third run output lacks %q:\n%s", want, out3)
		}
	}
	assertNoSecretMaterial(t, out2+out3+logs.String(), key, v1, transplant, v1Inactive)

	// The acknowledgement never covers an ACTIVE workspace.
	reg := t009Migrate(t, regDSN, filepath.Join(store.FindMigrationsDir(), "registry"))
	if err := reg.ExecRaw(ctx, `UPDATE public.workspace_registry SET status = 'active' WHERE workspace_id = 'ws-four'`); err != nil {
		t.Fatal(err)
	}
	out4, err := runReencrypt(t, "--all-workspaces", "--allow-unreachable-inactive=ws-four")
	if err == nil || !strings.Contains(err.Error(), "ws-four") || !strings.Contains(out4, "workspace ws-four: NOT CLEAN (unreachable)") {
		t.Fatalf("an active unreachable workspace was acknowledged: %v\n%s", err, out4)
	}
}

// t009MissingDB is a DSN on the same server as dsn naming a database that
// does not exist (and is dropped first, in case a previous run left it).
func t009MissingDB(t *testing.T, dsn, suffix string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(store.TestDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(base.Path, "/") + "_" + suffix
	conn, err := pgx.Connect(context.Background(), store.TestDSN())
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}
	defer conn.Close(context.Background())
	_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	u.Path = "/" + name
	return u.String()
}

// The single-workspace path (no --all-workspaces): file mode, the
// database from SPINE_DATABASE_URL, bound to SPINE_WORKSPACE_ID exactly as
// buildStore binds it. migrate converts and exits 0; strict refuses a
// planted plaintext row, leaves it untouched, quotes its id and exits
// non-zero.
func TestSecretsReencrypt_SingleWorkspace(t *testing.T) {
	dsn := t009CreateDB(t, "t009_single")
	st := t009Migrate(t, dsn, store.FindMigrationsDir())
	key := t009Key(0x50)
	c, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := c.Encrypt("whsec-LEAK-single-v1")
	t009Insert(t, st, "sub-p", "whsec-LEAK-single-plain")
	t009Insert(t, st, "sub-v1", v1)

	clearSecretBackendEnv(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("SPINE_DATABASE_URL", dsn)
	t.Setenv("SPINE_WORKSPACE_ID", "ws-file")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	var logs bytes.Buffer
	captureDefaultLog(t, &logs)

	out, err := runReencrypt(t)
	if err != nil {
		t.Fatalf("single-workspace migrate run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace ws-file: clean scanned=2 rewritten=2") {
		t.Fatalf("output:\n%s", out)
	}
	for _, id := range []string{"sub-p", "sub-v1"} {
		if _, err := c.Open(t009Raw(t, st, id), spinecrypto.AssociatedData(t009Purpose, "ws-file", id)); err != nil {
			t.Fatalf("%s not bound to SPINE_WORKSPACE_ID: %v", id, err)
		}
	}

	t009Insert(t, st, "sub-planted\nworkspace ws-file: clean", "attacker-LEAK-plain")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "strict")
	out2, err := runReencrypt(t)
	if err == nil || !strings.Contains(err.Error(), "ws-file") {
		t.Fatalf("strict run with a planted plaintext row should fail: %v\n%s", err, out2)
	}
	if !strings.Contains(out2, `workspace ws-file: refused subscription "sub-planted\nworkspace ws-file: clean" envelope=plaintext`) ||
		!strings.Contains(out2, "workspace ws-file: NOT CLEAN scanned=3 rewritten=0 current=2") {
		t.Fatalf("strict run output:\n%s", out2)
	}
	for _, line := range strings.Split(out2, "\n") {
		if strings.HasPrefix(line, "workspace ws-file: clean") {
			t.Fatalf("a planted subscription id forged a clean line:\n%s", out2)
		}
	}
	if t009Raw(t, st, "sub-planted\nworkspace ws-file: clean") != "attacker-LEAK-plain" {
		t.Fatal("strict run converted a planted legacy row")
	}
	assertNoSecretMaterial(t, out+out2+logs.String(), key, v1)
}

func assertNoSecretMaterial(t *testing.T, text string, key []byte, stored ...string) {
	t.Helper()
	if strings.Contains(text, "LEAK") {
		t.Fatal("output or logs contain a fixture secret")
	}
	if strings.Contains(text, base64.StdEncoding.EncodeToString(key)) {
		t.Fatal("output or logs contain the key")
	}
	for _, v := range stored {
		if payload := v[strings.LastIndex(v, ":")+1:]; strings.Contains(text, payload) {
			t.Fatal("output or logs contain a stored value")
		}
	}
}
