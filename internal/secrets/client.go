// Package secrets defines the SecretClient abstraction used by Spine to
// fetch per-workspace credentials. All workspace credential reads in Spine
// must go through SecretClient; direct env-var or file reads of workspace
// credentials are forbidden (see ADR-010).
//
// Callers MUST construct refs via WorkspaceRef, never by string
// concatenation, so the URI shape stays in one place.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// SecretRef is a typed string in URI form:
//
//	secret-store://workspaces/{workspace_id}/{purpose}
//
// `purpose` is one of: runtime_db, projection_db, git.
type SecretRef string

// Purpose values for SecretRef. New purposes must be added here so that
// WorkspaceRef and ParseRef validate them centrally.
const (
	PurposeRuntimeDB    = "runtime_db"
	PurposeProjectionDB = "projection_db"
	PurposeGit          = "git"
)

const refScheme = "secret-store://workspaces/"

func validPurpose(p string) bool {
	switch p {
	case PurposeRuntimeDB, PurposeProjectionDB, PurposeGit:
		return true
	}
	return false
}

// WorkspaceRef builds the canonical ref for a workspace credential.
//
//	secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
//	→ SecretRef("secret-store://workspaces/acme/runtime_db")
//
// The result is the unvalidated concatenation; pair WorkspaceRef with
// ParseRef at any boundary that accepts refs from outside Spine.
func WorkspaceRef(workspaceID, purpose string) SecretRef {
	return SecretRef(refScheme + workspaceID + "/" + purpose)
}

// ParseRef extracts (workspaceID, purpose) from a SecretRef. Returns
// ErrInvalidRef if the URI shape does not match or if purpose is not
// in the documented allowlist.
func ParseRef(ref SecretRef) (workspaceID, purpose string, err error) {
	s := string(ref)
	rest, ok := strings.CutPrefix(s, refScheme)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, s)
	}
	workspaceID, purpose, ok = strings.Cut(rest, "/")
	if !ok || workspaceID == "" || purpose == "" {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, s)
	}
	if strings.ContainsRune(workspaceID, '/') || strings.ContainsRune(purpose, '/') {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, s)
	}
	if !validPurpose(purpose) {
		return "", "", fmt.Errorf("%w: unsupported purpose %q in %q", ErrInvalidRef, purpose, s)
	}
	return workspaceID, purpose, nil
}

// SecretValue carries credential bytes. It redacts itself in String,
// MarshalJSON, and slog output. The only deliberate accessor is Reveal,
// which is intended to be called at the in-process boundary with a
// database driver or git client.
type SecretValue struct {
	raw []byte
}

// NewSecretValue wraps raw bytes as a SecretValue. The provider takes
// ownership of the slice; callers must not mutate it after wrapping.
func NewSecretValue(raw []byte) SecretValue {
	return SecretValue{raw: raw}
}

// Reveal returns the underlying credential bytes. This is the single
// deliberate accessor; treat the result as sensitive.
func (v SecretValue) Reveal() []byte {
	return v.raw
}

// String redacts. Use Reveal at the boundary with a driver or client.
func (v SecretValue) String() string {
	return "<redacted>"
}

// GoString redacts so that fmt %#v does not leak.
func (v SecretValue) GoString() string {
	return "<redacted>"
}

// MarshalJSON redacts so that JSON-encoded structures never embed the
// raw value.
func (v SecretValue) MarshalJSON() ([]byte, error) {
	return []byte(`"<redacted>"`), nil
}

// LogValue implements slog.LogValuer so that structured logging emits
// the redacted form regardless of log handler.
func (v SecretValue) LogValue() slog.Value {
	return slog.StringValue("<redacted>")
}

// Compile-time guard: SecretValue must satisfy slog.LogValuer.
var _ slog.LogValuer = SecretValue{}

// VersionID identifies a specific version of a secret as reported by
// the provider. Treat it as opaque; equality is the only meaningful
// operation.
type VersionID string

// SecretClient is the read-only interface for fetching workspace
// credentials. Rotation and seeding are platform-side concerns (see
// ADR-010, ADR-011); no Spine code writes secrets through this
// interface.
type SecretClient interface {
	// Get fetches the current value of ref. Implementations must map
	// provider-specific errors to the sentinels in this package
	// (ErrSecretNotFound, ErrAccessDenied, ErrSecretStoreDown).
	Get(ctx context.Context, ref SecretRef) (SecretValue, VersionID, error)

	// Invalidate drops any cached value for ref so that the next Get
	// re-fetches from the underlying store. It is idempotent: calling
	// it on a ref that is not currently cached, or on a provider that
	// does not cache, returns nil.
	Invalidate(ctx context.Context, ref SecretRef) error
}

// Sentinel errors. Providers wrap these (via fmt.Errorf %w) so that
// callers can match on the category with errors.Is.
var (
	ErrSecretNotFound  = errors.New("secret not found")
	ErrAccessDenied    = errors.New("secret access denied")
	ErrSecretStoreDown = errors.New("secret store unreachable")
	ErrInvalidRef      = errors.New("invalid secret ref")
)
