// Package contract provides the cross-provider test suite for
// implementations of secrets.SecretClient. Provider packages call
// RunContract from a *_test.go file with a factory that returns a
// freshly-seeded client.
//
// Required seeded refs (the factory must arrange these before
// returning the client):
//
//	RefRuntimeDB → FixtureRuntimeDBValue   (present)
//	RefGit       → FixtureGitValue         (present)
//	RefMissing   → not present             (Get must return ErrSecretNotFound)
//
// The suite covers Get, Invalidate, sentinel-error mapping, and
// redaction. ErrAccessDenied mapping is provider-specific and is
// covered in each provider's own test file rather than here.
package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
)

// Required fixture refs. Providers seed these in their test setup.
var (
	RefRuntimeDB = secrets.WorkspaceRef("contract", secrets.PurposeRuntimeDB)
	RefGit       = secrets.WorkspaceRef("contract", secrets.PurposeGit)
	RefMissing   = secrets.WorkspaceRef("contract-missing", secrets.PurposeRuntimeDB)
)

// Required fixture values for the seeded refs.
const (
	FixtureRuntimeDBValue = "runtime-db-secret-value"
	FixtureGitValue       = "git-token-value"
)

// RunContract executes the cross-provider contract test suite against
// the SecretClient returned by newClient. newClient is invoked once
// per subtest so providers can isolate state if needed.
func RunContract(t *testing.T, newClient func() secrets.SecretClient) {
	t.Helper()

	t.Run("Get_returns_seeded_value", func(t *testing.T) {
		client := newClient()
		ctx := context.Background()

		val, version, err := client.Get(ctx, RefRuntimeDB)
		if err != nil {
			t.Fatalf("Get(%q): %v", RefRuntimeDB, err)
		}
		if got := string(val.Reveal()); got != FixtureRuntimeDBValue {
			t.Fatalf("Get(%q) value = %q, want %q", RefRuntimeDB, got, FixtureRuntimeDBValue)
		}
		if version == "" {
			t.Fatalf("Get(%q) returned empty VersionID", RefRuntimeDB)
		}

		gitVal, _, err := client.Get(ctx, RefGit)
		if err != nil {
			t.Fatalf("Get(%q): %v", RefGit, err)
		}
		if got := string(gitVal.Reveal()); got != FixtureGitValue {
			t.Fatalf("Get(%q) value = %q, want %q", RefGit, got, FixtureGitValue)
		}
	})

	t.Run("Get_value_redacts_in_string_and_log", func(t *testing.T) {
		client := newClient()
		val, _, err := client.Get(context.Background(), RefRuntimeDB)
		if err != nil {
			t.Fatalf("Get(%q): %v", RefRuntimeDB, err)
		}
		if s := val.String(); strings.Contains(s, FixtureRuntimeDBValue) {
			t.Fatalf("String() leaked secret: %q", s)
		}
		if s := val.LogValue().String(); strings.Contains(s, FixtureRuntimeDBValue) {
			t.Fatalf("LogValue() leaked secret: %q", s)
		}
	})

	t.Run("Get_missing_ref_returns_ErrSecretNotFound", func(t *testing.T) {
		client := newClient()
		_, _, err := client.Get(context.Background(), RefMissing)
		if !errors.Is(err, secrets.ErrSecretNotFound) {
			t.Fatalf("Get(%q): expected ErrSecretNotFound, got %v", RefMissing, err)
		}
	})

	t.Run("Get_invalid_ref_returns_ErrInvalidRef", func(t *testing.T) {
		client := newClient()
		_, _, err := client.Get(context.Background(), secrets.SecretRef("not-a-ref"))
		if !errors.Is(err, secrets.ErrInvalidRef) {
			t.Fatalf("Get(invalid): expected ErrInvalidRef, got %v", err)
		}
	})

	t.Run("Invalidate_seeded_ref_succeeds_and_value_still_resolvable", func(t *testing.T) {
		client := newClient()
		ctx := context.Background()

		if err := client.Invalidate(ctx, RefRuntimeDB); err != nil {
			t.Fatalf("Invalidate(%q): %v", RefRuntimeDB, err)
		}
		val, _, err := client.Get(ctx, RefRuntimeDB)
		if err != nil {
			t.Fatalf("Get after Invalidate: %v", err)
		}
		if got := string(val.Reveal()); got != FixtureRuntimeDBValue {
			t.Fatalf("Get after Invalidate value = %q, want %q", got, FixtureRuntimeDBValue)
		}
	})

	t.Run("Invalidate_unknown_ref_is_idempotent", func(t *testing.T) {
		client := newClient()
		if err := client.Invalidate(context.Background(), RefMissing); err != nil {
			t.Fatalf("Invalidate(%q): expected nil, got %v", RefMissing, err)
		}
	})
}
