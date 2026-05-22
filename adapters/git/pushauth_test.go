package git

import (
	"os"
	"testing"
)

func TestLoadPushAuthFromEnv_TokenOnlyScrubsEnv(t *testing.T) {
	resetPushAuthForTest()
	t.Setenv("SPINE_GIT_CREDENTIAL_HELPER", "")
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "ghp_secret")
	t.Setenv("SPINE_GIT_PUSH_USERNAME", "bot")

	warnings, err := LoadPushAuthFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	// After load, the token must no longer be in os.Environ(). This
	// closes the proc/environ leak described in TASK-008.
	if v, ok := os.LookupEnv("SPINE_GIT_PUSH_TOKEN"); ok {
		t.Fatalf("expected SPINE_GIT_PUSH_TOKEN unset, got %q", v)
	}
	if v, ok := os.LookupEnv("SPINE_GIT_PUSH_USERNAME"); ok {
		t.Fatalf("expected SPINE_GIT_PUSH_USERNAME unset, got %q", v)
	}

	opts := PushAuthOpts()
	if len(opts) == 0 {
		t.Fatal("expected push-token CLIOption to be cached")
	}

	// Apply the cached opts to a fresh client and confirm the token
	// is captured in memory — scrubbing from env must not break the
	// in-memory auth path.
	c := NewCLIClient(t.TempDir(), opts...)
	if c.pushToken != "ghp_secret" {
		t.Fatalf("expected token captured in memory, got %q", c.pushToken)
	}
	if c.pushUsername != "bot" {
		t.Fatalf("expected username captured in memory, got %q", c.pushUsername)
	}
	c.Close()
}

func TestLoadPushAuthFromEnv_HelperWinsAndDropsToken(t *testing.T) {
	resetPushAuthForTest()
	t.Setenv("SPINE_GIT_CREDENTIAL_HELPER", "cache")
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "ghp_secret")

	warnings, err := LoadPushAuthFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected a warning when both helper and token are set")
	}
	// Token is dropped — applying the opts to a client must not install it.
	c := NewCLIClient(t.TempDir(), PushAuthOpts()...)
	if c.pushToken != "" {
		t.Fatalf("expected token to be dropped, got %q", c.pushToken)
	}
	if c.credentialHelper != "cache" {
		t.Fatalf("expected credential helper=cache, got %q", c.credentialHelper)
	}
	// And the env var must be scrubbed too, matching the token-only path.
	if v, ok := os.LookupEnv("SPINE_GIT_PUSH_TOKEN"); ok {
		t.Fatalf("expected SPINE_GIT_PUSH_TOKEN unset, got %q", v)
	}
	c.Close()
}

func TestLoadPushAuthFromEnv_HelperRejectedWhenNotInAllowlist(t *testing.T) {
	resetPushAuthForTest()
	// An arbitrary absolute path would otherwise be handed to git,
	// which treats credential.helper as "run this program." The
	// allowlist is the hard gate; reaffirm here.
	t.Setenv("SPINE_GIT_CREDENTIAL_HELPER", "/tmp/evil.sh")
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "")

	if _, err := LoadPushAuthFromEnv(); err == nil {
		t.Fatal("expected error for non-allowlisted helper")
	}
	if opts := PushAuthOpts(); opts != nil {
		t.Fatalf("expected no cached opts on validation failure, got %d", len(opts))
	}
}

func TestLoadPushAuthFromEnv_NoConfigurationIsNoop(t *testing.T) {
	resetPushAuthForTest()
	t.Setenv("SPINE_GIT_CREDENTIAL_HELPER", "")
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "")

	warnings, err := LoadPushAuthFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if opts := PushAuthOpts(); opts != nil {
		t.Fatalf("expected no opts when nothing configured, got %d", len(opts))
	}
}

func TestLoadPushAuthFromEnv_IdempotentAfterScrub(t *testing.T) {
	resetPushAuthForTest()
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "ghp_one")

	if _, err := LoadPushAuthFromEnv(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	first := PushAuthOpts()

	// A later workspace-pool build calls LoadPushAuthFromEnv again —
	// the env has been scrubbed, but the cached value must still
	// hand back the original token so lazily-built clients keep
	// working.
	if _, err := LoadPushAuthFromEnv(); err != nil {
		t.Fatalf("unexpected err on second call: %v", err)
	}
	second := PushAuthOpts()
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("expected cached opts on second call; got first=%d second=%d", len(first), len(second))
	}

	c := NewCLIClient(t.TempDir(), second...)
	if c.pushToken != "ghp_one" {
		t.Fatalf("expected ghp_one from cache, got %q", c.pushToken)
	}
	c.Close()
}
