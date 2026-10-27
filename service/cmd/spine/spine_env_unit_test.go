package main

// INIT-024 TASK-007 (N-16): unit table for requireRuntimeEnv. The serve
// and secrets regression tests, and the value tables, are in
// spine_env_test.go.

import (
	"strings"
	"testing"
)

func TestRequireRuntimeEnv(t *testing.T) {
	for _, raw := range unsetOrBadSpineEnv {
		t.Run("refuse/"+raw, func(t *testing.T) {
			t.Setenv("SPINE_ENV", raw)
			env, err := requireRuntimeEnv()
			if !spineEnvRefusal(err) {
				t.Fatalf("SPINE_ENV=%q: env=%q err=%v, want the refusal naming production, staging and development", raw, env, err)
			}
			if env != "" {
				t.Fatalf("SPINE_ENV=%q: refusal returned env %q", raw, env)
			}
		})
	}
	for raw, want := range knownSpineEnv {
		t.Run("accept/"+raw, func(t *testing.T) {
			t.Setenv("SPINE_ENV", raw)
			env, err := requireRuntimeEnv()
			if err != nil || env != want {
				t.Fatalf("SPINE_ENV=%q: env=%q err=%v, want %q", raw, env, err, want)
			}
		})
	}
	// The refusal quotes an unknown value, bounded.
	t.Setenv("SPINE_ENV", strings.Repeat("x", 4096))
	_, err := requireRuntimeEnv()
	if err == nil || len(err.Error()) > 512 {
		t.Fatalf("an oversized SPINE_ENV must be refused with a bounded message, got %d bytes", len(errString(err)))
	}
}
