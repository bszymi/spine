package main

// INIT-024 TASK-007 (review finding N-16): `spine serve` and `spine secrets
// reencrypt` refuse to start unless SPINE_ENV names a known environment.
// An unset or mistyped value used to resolve to "", which every production
// gate (secret key required, SPINE_CODE_REPO_BASE required, weak
// SMP_ADMIN_TOKEN refused, SPINE_DEV_MODE refused) treated as
// non-production — so a production deployment that forgot or misspelled
// the variable ran with every one of them off.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// spineEnvRefusal reports whether err is requireRuntimeEnv's refusal: it
// names the variable and all three accepted values.
func spineEnvRefusal(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SPINE_ENV") &&
		strings.Contains(msg, "production") &&
		strings.Contains(msg, "staging") &&
		strings.Contains(msg, "development") &&
		(strings.Contains(msg, "is not set") || strings.Contains(msg, "is not a known runtime environment"))
}

// unsetOrBadSpineEnv are values that must refuse to start. "" is how
// t.Setenv expresses "unset" (os.Getenv cannot tell the two apart, and
// requireRuntimeEnv treats them alike). The rest are the misspellings a
// real manifest carries, plus a value that only looks like a known one.
var unsetOrBadSpineEnv = []string{
	"",
	"   ",
	"prod",
	"prodution",
	"Production1",
	"live",
	"dev",
	"test",
	"production,staging",
	"production\u200b", // zero-width space: not trimmed, so not "production"
	"staging\x01",
}

// knownSpineEnv maps accepted spellings to the value every gate sees.
var knownSpineEnv = map[string]string{
	"production":      "production",
	"PRODUCTION":      "production",
	"  Production\t":  "production",
	"staging":         "staging",
	"Staging":         "staging",
	"development":     "development",
	" DEVELOPMENT \n": "development",
}

// serveEnvForGateTest makes every startup step after the SPINE_ENV gate
// deterministic: no flag that another gate refuses, every production
// requirement met, and a listen port that cannot be bound — so a serve
// that passed every startup gate stops at its listener ("server failed to
// start") instead of serving, and never binds a real port. (The file-mode
// store connects lazily, so the unreachable database URL alone would not
// stop it.)
func serveEnvForGateTest(t *testing.T) {
	t.Helper()
	t.Setenv("SPINE_DEV_MODE", "")
	t.Setenv("SPINE_GIT_HTTP_DEV_NO_AUTH", "")
	t.Setenv("SPINE_OPERATOR_TOKEN", "")
	t.Setenv("SMP_ADMIN_TOKEN", "")
	t.Setenv("WORKSPACE_RESOLVER", "")
	t.Setenv("SPINE_SECRET_PROVIDER", "")
	t.Setenv("SPINE_SECRET_FILE_ROOT", "")
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "")
	k, _ := b64Key(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", k)
	t.Setenv("SPINE_CODE_REPO_BASE", t.TempDir())
	t.Setenv("SPINE_INSECURE_LOCAL", "1")
	t.Setenv("SPINE_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	t.Setenv("SPINE_SERVER_PORT", "-1")
}

// TestServe_RequiresKnownSpineEnv is AC 4: an unset or mistyped SPINE_ENV
// refuses to start, naming the three values, before any store, secret or
// listener is opened; every known value starts (it runs every startup step
// and stops only at the unbindable listen port). At the scaffold base every
// refused value started too, on the non-production path.
func TestServe_RequiresKnownSpineEnv(t *testing.T) {
	for _, raw := range unsetOrBadSpineEnv {
		t.Run("refuse/"+raw, func(t *testing.T) {
			serveEnvForGateTest(t)
			t.Setenv("SPINE_ENV", raw)
			err := serveCmd().RunE(serveCmd(), nil)
			if !spineEnvRefusal(err) {
				t.Fatalf("SPINE_ENV=%q: spine serve err = %v, want the SPINE_ENV refusal naming production, staging and development", raw, err)
			}
		})
	}
	for raw := range knownSpineEnv {
		t.Run("start/"+raw, func(t *testing.T) {
			serveEnvForGateTest(t)
			t.Setenv("SPINE_ENV", raw)
			err := serveCmd().RunE(serveCmd(), nil)
			if err == nil {
				t.Fatalf("SPINE_ENV=%q: spine serve returned nil against an unreachable database", raw)
			}
			if spineEnvRefusal(err) || !strings.Contains(err.Error(), "server failed to start") {
				t.Fatalf("SPINE_ENV=%q: err = %v, want serve to reach its listener", raw, err)
			}
		})
	}
}

// The same gate guards `spine secrets reencrypt`, before the key ring is
// built or any database is opened.
func TestSecretsReencrypt_RequiresKnownSpineEnv(t *testing.T) {
	for _, raw := range unsetOrBadSpineEnv {
		t.Run(raw, func(t *testing.T) {
			reencryptEnv(t)
			t.Setenv("SPINE_ENV", raw)
			t.Setenv("WORKSPACE_RESOLVER", "")
			t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "migrate")
			var out bytes.Buffer
			cmd := secretsCmd()
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"reencrypt"})
			err := cmd.ExecuteContext(context.Background())
			if !spineEnvRefusal(err) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("SPINE_ENV=%q: reencrypt err = %v, want the SPINE_ENV refusal", raw, err)
			}
			if strings.Contains(out.String(), "secret envelope:") {
				t.Fatalf("SPINE_ENV=%q: reencrypt went past the gate: %q", raw, out.String())
			}
		})
	}
}
