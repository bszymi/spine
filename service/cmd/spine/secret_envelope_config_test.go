package main

// INIT-023 TASK-009 AC13 (standalone configuration) and the AC23 unit
// case for `spine secrets reencrypt`.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "")
	t.Setenv("WORKSPACE_RESOLVER", "db")
	var out bytes.Buffer
	if err := runSecretsReencrypt(context.Background(), &out, false); err == nil || !strings.Contains(err.Error(), "--all-workspaces") {
		t.Fatalf("db resolver without --all-workspaces: %v", err)
	}
	t.Setenv("WORKSPACE_RESOLVER", "")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", "")
	if err := runSecretsReencrypt(context.Background(), &out, true); err == nil || !strings.Contains(err.Error(), "SPINE_REGISTRY_DATABASE_URL") {
		t.Fatalf("--all-workspaces without a registry: %v", err)
	}
}
