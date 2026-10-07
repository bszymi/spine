package main

// INIT-023 TASK-009 AC13 (standalone configuration) and the AC23 unit
// case for `spine secrets reencrypt`.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
)

func b64Key(t *testing.T) (string, []byte) {
	t.Helper()
	k := make([]byte, spinecrypto.EncryptionKeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k), k
}

func TestLoadSecretCipher_PreviousKeysBuildRing(t *testing.T) {
	primary, pk := b64Key(t)
	prev1, k1 := b64Key(t)
	prev2, k2 := b64Key(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", primary)
	// Whitespace is trimmed and empty entries are ignored.
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", " "+prev1+" ,, "+prev2+" ,")
	c, err := loadSecretCipher("production")
	if err != nil {
		t.Fatal(err)
	}
	ids := c.KeyIDs()
	want := make([]string, 0, 3)
	for _, k := range [][]byte{pk, k1, k2} {
		id, _ := spinecrypto.KeyID(k)
		want = append(want, id)
	}
	if len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("ring ids = %v, want %v", ids, want)
	}
	// A value sealed under a previous key opens through the ring.
	old, _ := spinecrypto.NewSecretCipher(k2)
	ct, _ := old.Encrypt("s")
	if got, err := c.Decrypt(ct); err != nil || got != "s" {
		t.Fatalf("previous key does not open: %v", err)
	}
}

func TestLoadSecretCipher_MalformedPreviousKeyNamesIndexNotValue(t *testing.T) {
	primary, _ := b64Key(t)
	good, _ := b64Key(t)
	shortKey := base64.StdEncoding.EncodeToString([]byte("sixteen-byte-key"))
	cases := []struct {
		name, prev, wantIndex string
	}{
		{"bad base64", good + ",not$$base64", "entry 2"},
		{"wrong length", shortKey, "entry 1"},
		{"wrong length after an empty entry", good + ",," + shortKey, "entry 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", primary)
			t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", tc.prev)
			_, err := loadSecretCipher("development")
			if err == nil || !strings.Contains(err.Error(), "SPINE_SECRET_PREVIOUS_KEYS "+tc.wantIndex) {
				t.Fatalf("err = %v, want it to name %q", err, tc.wantIndex)
			}
			for _, material := range []string{good, shortKey, "not$$base64", primary} {
				if strings.Contains(err.Error(), material) {
					t.Fatalf("error leaks key material: %v", err)
				}
			}
		})
	}
}

func TestLoadSecretCipher_PreviousKeysRequirePrimary(t *testing.T) {
	prev, _ := b64Key(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", prev)
	for _, env := range []string{"development", "production"} {
		if _, err := loadSecretCipher(env); err == nil || !strings.Contains(err.Error(), "SPINE_SECRET_ENCRYPTION_KEY") {
			t.Fatalf("%s: previous keys without a primary: %v", env, err)
		}
	}
}

func TestLoadSecretCipher_ProductionGateUnchanged(t *testing.T) {
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	if _, err := loadSecretCipher("production"); err == nil || !strings.Contains(err.Error(), "required when SPINE_ENV=production") {
		t.Fatalf("production gate: %v", err)
	}
	if c, err := loadSecretCipher("development"); err != nil || c != nil {
		t.Fatalf("development without a key: %v %v", c, err)
	}
}

func TestLoadSecretEnvelopeMode(t *testing.T) {
	cases := []struct {
		in   string
		want store.SecretEnvelopeMode
		ok   bool
	}{
		{"", store.SecretEnvelopeMigrate, true},
		{"migrate", store.SecretEnvelopeMigrate, true},
		{"Strict", store.SecretEnvelopeStrict, true},
		{"DOWNGRADE", store.SecretEnvelopeDowngrade, true},
		{"bogus", "", false},
	}
	for _, tc := range cases {
		t.Setenv("SPINE_SECRET_ENVELOPE_MODE", tc.in)
		got, err := loadSecretEnvelopeMode()
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("SPINE_SECRET_ENVELOPE_MODE=%q: %q, %v", tc.in, got, err)
		}
		if err != nil && !strings.Contains(err.Error(), "SPINE_SECRET_ENVELOPE_MODE") {
			t.Errorf("error does not name the variable: %v", err)
		}
	}
}

// AC23 (unit): without a key the command refuses before it opens any
// database. SPINE_DATABASE_URL points at an address that would fail to
// connect, so reaching the database would surface a different error.
func TestSecretsReencrypt_RequiresKeyAndTouchesNothing(t *testing.T) {
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_ENV", "")
	t.Setenv("WORKSPACE_RESOLVER", "")
	t.Setenv("SPINE_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	t.Setenv("SPINE_INSECURE_LOCAL", "1")
	var out bytes.Buffer
	cmd := secretsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"reencrypt"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SPINE_SECRET_ENCRYPTION_KEY") {
		t.Fatalf("reencrypt without a key: %v", err)
	}
	// "secret envelope:" is printed only once a ring is built, and every
	// per-workspace line starts with "workspace <id>:".
	if strings.Contains(out.String(), "secret envelope:") || strings.Contains(out.String(), "workspace default:") {
		t.Fatalf("command went past the key check: %q", out.String())
	}
}

func TestSecretsReencrypt_SharedResolverNeedsAllWorkspaces(t *testing.T) {
	k, _ := b64Key(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", k)
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	t.Setenv("WORKSPACE_RESOLVER", "db")
	var out bytes.Buffer
	if err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{}); err == nil || !strings.Contains(err.Error(), "--all-workspaces") {
		t.Fatalf("db resolver without --all-workspaces: %v", err)
	}
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", "")
	if err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{allWorkspaces: true}); err == nil || !strings.Contains(err.Error(), "SPINE_REGISTRY_DATABASE_URL") {
		t.Fatalf("--all-workspaces without a registry: %v", err)
	}
}

// reencryptEnv sets a key and points every database variable at an
// address that refuses connections, so a test that expects a refusal
// before any database is opened would see a different error otherwise.
func reencryptEnv(t *testing.T) {
	t.Helper()
	k, _ := b64Key(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", k)
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_ENV", "")
	t.Setenv("SPINE_SECRET_PROVIDER", "")
	t.Setenv("SPINE_SECRET_FILE_ROOT", "")
	t.Setenv("SPINE_INSECURE_LOCAL", "1")
	t.Setenv("SPINE_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", "postgres://nobody@127.0.0.1:1/registry?sslmode=disable")
}

// Review round 1: the command writes every workspace database, so it
// refuses to guess the mode. An unset SPINE_SECRET_ENVELOPE_MODE would
// otherwise mean migrate, which converts (launders) the legacy rows a
// strict deployment refuses and reports the run clean.
func TestSecretsReencrypt_RequiresExplicitMode(t *testing.T) {
	reencryptEnv(t)
	t.Setenv("WORKSPACE_RESOLVER", "")
	for _, mode := range []string{"", "   "} {
		t.Setenv("SPINE_SECRET_ENVELOPE_MODE", mode)
		for _, opts := range []reencryptOptions{{}, {allWorkspaces: true}} {
			var out bytes.Buffer
			err := runSecretsReencrypt(context.Background(), &out, opts)
			if err == nil || !strings.Contains(err.Error(), "SPINE_SECRET_ENVELOPE_MODE must be set") {
				t.Fatalf("mode %q, %+v: %v", mode, opts, err)
			}
			if out.Len() != 0 {
				t.Fatalf("command went past the mode check: %q", out.String())
			}
		}
	}
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "bogus")
	var out bytes.Buffer
	if err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{}); err == nil || !strings.Contains(err.Error(), "SPINE_SECRET_ENVELOPE_MODE") {
		t.Fatalf("bogus mode: %v", err)
	}
}

// Review round 1: --all-workspaces binds every registry database under its
// registry id, which spine serve binds only with WORKSPACE_RESOLVER=db.
// Every other resolver is refused before the registry is opened.
func TestSecretsReencrypt_AllWorkspacesNeedsDBResolver(t *testing.T) {
	reencryptEnv(t)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	cases := map[string]string{
		"":                 "WORKSPACE_RESOLVER=db",
		"file":             "WORKSPACE_RESOLVER=db",
		"FILE":             "WORKSPACE_RESOLVER=db",
		"platform-binding": "subscription secrets swept",
		"bogus":            "WORKSPACE_RESOLVER=db",
	}
	for resolver, want := range cases {
		t.Setenv("WORKSPACE_RESOLVER", resolver)
		var out bytes.Buffer
		err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{allWorkspaces: true})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("WORKSPACE_RESOLVER=%q: %v", resolver, err)
		}
		if strings.Contains(err.Error(), "registry:") || strings.Contains(out.String(), "workspace ") {
			t.Fatalf("WORKSPACE_RESOLVER=%q: the registry was touched: %v %q", resolver, err, out.String())
		}
	}
}

func TestSecretsReencrypt_AllowUnreachableNeedsAllWorkspaces(t *testing.T) {
	reencryptEnv(t)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	t.Setenv("WORKSPACE_RESOLVER", "")
	var out bytes.Buffer
	err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{allowUnreachableInactive: []string{"ws-gone"}})
	if err == nil || !strings.Contains(err.Error(), "--allow-unreachable-inactive applies only with --all-workspaces") {
		t.Fatalf("flag without --all-workspaces: %v", err)
	}
}

// The single-workspace path refuses a --workspace that differs from the
// file-mode workspace spine serve binds, before it opens the database
// (sealing under another id would make every row unreadable to serve).
func TestSecretsReencrypt_WorkspaceFlagMustMatchFileWorkspace(t *testing.T) {
	reencryptEnv(t)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	t.Setenv("WORKSPACE_RESOLVER", "file")
	t.Setenv("SPINE_WORKSPACE_ID", "ws-a")
	prev := globalWorkspaceID
	t.Cleanup(func() { globalWorkspaceID = prev })
	globalWorkspaceID = "ws-b"
	var out bytes.Buffer
	err := runSecretsReencrypt(context.Background(), &out, reencryptOptions{})
	if err == nil || !strings.Contains(err.Error(), `--workspace "ws-b" does not match the file-mode workspace "ws-a"`) {
		t.Fatalf("mismatched --workspace: %v", err)
	}
	if strings.Contains(out.String(), "workspace ws-") {
		t.Fatalf("command went past the guard: %q", out.String())
	}
}

// Database-sourced text is quoted in the command output: a subscription id
// planted by a runtime-database writer cannot inject a forged line or a
// terminal escape into the output operators gate strict on.
func TestSweepOneWorkspace_QuotesDatabaseText(t *testing.T) {
	forged := "x\nworkspace ws-b: clean scanned=1 rewritten=0\x1b[2K\nsummary: workspaces=1 clean=1 not_clean=0"
	var out bytes.Buffer
	sweepOneWorkspace(context.Background(), &out, sweepTarget{id: "ws-b", resolveErr: errors.New(forged)}, nil, store.SecretEnvelopeMigrate)
	printRefused(&out, sweepTarget{id: "ws-b"}, store.SecretSweepIssue{SubscriptionID: forged, Envelope: spinecrypto.EnvelopeV1, Reason: forged})
	got := out.String()
	if strings.Count(got, "\n") != 2 || strings.Contains(got, "\x1b") || strings.Contains(got, "\nworkspace ws-b: clean") || strings.Contains(got, "\nsummary:") {
		t.Fatalf("database text escaped its line:\n%s", got)
	}
	// A registry id that is not a valid workspace id is printed quoted.
	if l := (sweepTarget{id: "bad\nid", inactive: true}).label(); l != `"bad\nid" (inactive)` {
		t.Fatalf("label: %q", l)
	}
}

// Review round 1: both shared resolvers (db and platform-binding) build
// their ServicePool through sharedModePoolConfig, so this pins that the
// configured cipher and envelope mode reach the pool. A dropped field
// would compile ("" means migrate) while the startup log still printed the
// configured mode, and every per-workspace store would be bound in migrate.
func TestSharedModePoolConfig_CarriesSecretEnvelope(t *testing.T) {
	c, err := spinecrypto.NewSecretCipher(make([]byte, spinecrypto.EncryptionKeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []store.SecretEnvelopeMode{store.SecretEnvelopeStrict, store.SecretEnvelopeDowngrade, store.SecretEnvelopeMigrate} {
		cfg := sharedModePoolConfig(workspaceDeliveryConfig{}, slog.New(slog.DiscardHandler), c, mode, nil, "")
		if cfg.SecretCipher != c || cfg.SecretEnvelopeMode != mode || cfg.Builder == nil {
			t.Fatalf("mode %s: pool config does not carry the secret envelope: cipher=%v mode=%q", mode, cfg.SecretCipher == c, cfg.SecretEnvelopeMode)
		}
	}
}

// Review round 2: the `spine serve` start-up path — environment → key ring
// and envelope mode → buildWorkspaceResolver → ServicePool — carries the
// configured mode into the pool that binds every shared-mode workspace
// store. It drives buildServeWorkspaces, the one function serveCmd calls for
// this, so a mode dropped anywhere on that path (it would compile: "" means
// migrate) fails here, in the plain `go test ./...` CI runs. It uses
// WORKSPACE_RESOLVER=platform-binding because that branch is constructible
// without a database; the db branch builds its pool through the same
// newSharedPool, and both are driven end to end, through Pool.Get to a bound
// store, by the integration tests TestBuildServeWorkspaces_*BindsConfiguredMode.
func TestBuildServeWorkspaces_SharedPoolCarriesConfiguredMode(t *testing.T) {
	key, raw := b64Key(t)
	kid, err := spinecrypto.KeyID(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_ENV", "")
	t.Setenv("WORKSPACE_RESOLVER", "platform-binding")
	// Never dialled: the provider fetches a binding only when a workspace
	// is first requested.
	t.Setenv("SPINE_PLATFORM_URL", "http://127.0.0.1:1")
	t.Setenv("SPINE_PLATFORM_SERVICE_TOKEN", "svc-token")
	t.Setenv("SPINE_SECRET_PROVIDER", "file")
	t.Setenv("SPINE_SECRET_FILE_ROOT", t.TempDir())
	ctx := context.Background()
	discard := slog.New(slog.DiscardHandler)

	cases := []struct {
		name, key, mode string
		want            store.SecretEnvelopeMode
	}{
		{"strict", key, "strict", store.SecretEnvelopeStrict},
		{"downgrade", key, "downgrade", store.SecretEnvelopeDowngrade},
		{"migrate", key, "migrate", store.SecretEnvelopeMigrate},
		{"unset means migrate", key, "", store.SecretEnvelopeMigrate},
		// Without a key no store is bound; the pool carries no cipher.
		{"no key", "", "strict", store.SecretEnvelopeStrict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", tc.key)
			t.Setenv("SPINE_SECRET_ENVELOPE_MODE", tc.mode)
			ws, err := buildServeWorkspaces(ctx, "development", discard, workspaceDeliveryConfig{}, "")
			if err != nil {
				t.Fatalf("buildServeWorkspaces: %v", err)
			}
			t.Cleanup(ws.Close)
			if ws.Wiring == nil || ws.Wiring.Pool == nil || ws.Store != nil {
				t.Fatalf("platform-binding wiring: pool %v, process store %v", ws.Wiring != nil && ws.Wiring.Pool != nil, ws.Store)
			}
			cipher, mode := ws.Wiring.Pool.SubscriptionSecretConfig()
			if mode != tc.want || ws.SecretEnvelopeMode != tc.want {
				t.Fatalf("pool binds stores in %q (start-up loaded %q), want %q", mode, ws.SecretEnvelopeMode, tc.want)
			}
			if cipher != ws.SecretCipher {
				t.Fatal("the pool does not carry the start-up key ring")
			}
			if tc.key == "" {
				if cipher != nil {
					t.Fatal("a cipher was built without a key")
				}
				return
			}
			if cipher == nil || cipher.PrimaryKeyID() != kid {
				t.Fatalf("pool cipher is not the configured key ring")
			}
		})
	}

	// A bad mode fails start-up before any resolver or pool is built.
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", key)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "bogus")
	if ws, err := buildServeWorkspaces(ctx, "development", discard, workspaceDeliveryConfig{}, ""); err == nil || ws != nil || !strings.Contains(err.Error(), "SPINE_SECRET_ENVELOPE_MODE") {
		t.Fatalf("bogus mode: %v", err)
	}
}

// codex review (round 1, P2): pgx masks a URL's userinfo password but a
// ParseConfigError echoes a password passed as a query parameter. The
// command must never print one, for a workspace database or the registry.
func TestSecretsReencrypt_DatabaseErrorsCarryNoCredentials(t *testing.T) {
	ctx := context.Background()
	for name, dsn := range map[string]string{
		"query password, bad option":    "postgres://u@127.0.0.1:1/db?password=TOPSECRET1&connect_timeout=no",
		"query sslpassword, bad option": "postgres://u@127.0.0.1:1/db?sslpassword=TOPSECRET2&connect_timeout=no",
		"query password, refused":       "postgres://u@127.0.0.1:1/db?sslmode=disable&password=TOPSECRET3&connect_timeout=2",
		"userinfo password, refused":    "postgres://u:TOPSECRET4@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"key=value, bad option":         "host=127.0.0.1 port=1 user=u password=TOPSECRET5 connect_timeout=no",
	} {
		var out bytes.Buffer
		if got := sweepOneWorkspace(ctx, &out, sweepTarget{id: "ws-x", dbURL: dsn}, nil, store.SecretEnvelopeMigrate); got != sweepUnreachable {
			t.Fatalf("%s: outcome %v", name, got)
		}
		if strings.Contains(out.String(), "TOPSECRET") || !strings.Contains(out.String(), "workspace ws-x: unreachable: connect failed: ") {
			t.Fatalf("%s: output leaks a credential or lacks the line: %q", name, out.String())
		}
	}
	if got := describeDBError(errors.New("boom TOPSECRET6 boom"), "postgres://u:TOPSECRET6@h/db"); got != "boom xxxxx boom" {
		t.Fatalf("describeDBError did not mask the DSN's password: %q", got)
	}

	reencryptEnv(t)
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
	t.Setenv("WORKSPACE_RESOLVER", "db")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", "postgres://u@127.0.0.1:1/reg?sslmode=disable&password=REGSECRET&connect_timeout=no")
	var out bytes.Buffer
	err := runSecretsReencrypt(ctx, &out, reencryptOptions{allWorkspaces: true})
	if err == nil || strings.Contains(err.Error()+out.String(), "REGSECRET") || !strings.Contains(err.Error(), "connect to workspace registry") {
		t.Fatalf("registry error leaks a credential: %v %q", err, out.String())
	}
}
