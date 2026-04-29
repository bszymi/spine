package gitpool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/secrets"
)

// Credential carries the resolved authentication material for a single
// repository binding. The token is wrapped in secrets.SecretValue so it
// redacts itself in fmt, slog, JSON, and any other reflection-based
// output: the only deliberate accessor is Token.Reveal, which the pool
// calls at the in-process boundary with the git CLI client.
//
// An empty Credential (zero value) means "public repo, no auth" — the
// pool treats it as a no-op and never sets GIT_ASKPASS or rewrites the
// remote URL.
type Credential struct {
	// Username is the HTTP Basic-auth username paired with the token.
	// Empty means "use x-access-token" (the GitHub/GitLab convention
	// for token-only auth).
	Username string

	// Token is the redacting secret value. Reveal() returns the raw
	// bytes; the pool feeds them through GIT_ASKPASS env vars at
	// command time, never via argv or .git/config.
	Token secrets.SecretValue
}

// IsEmpty reports whether the credential carries any token bytes.
// An empty Credential signals "no auth" and is safe to pass to the
// cloner / factory unchanged.
func (c Credential) IsEmpty() bool {
	return len(c.Token.Reveal()) == 0
}

// CredentialResolver resolves a binding's credential reference to a
// concrete Credential at clone, fetch, or push time. The pool calls
// this once per cache miss; resolved values are scoped to the lifetime
// of the cached client (a binding update evicts the cache and forces a
// re-resolve, which is how rotation propagates).
//
// An empty CredentialsRef on the repository must yield the empty
// Credential without contacting the secret store — public repos go
// straight through.
type CredentialResolver interface {
	Resolve(ctx context.Context, repo *repository.Repository) (Credential, error)
}

// ErrCredentialsUnavailable is the matchable sentinel for any failure
// to resolve a binding's credentials. It is wrapped in a
// domain.SpineError(ErrPrecondition) so the gateway maps it to HTTP
// 412 (the binding is registered but its credential cannot be loaded —
// not a transient outage). Callers can recover the underlying secret-
// store error (errors.Is(err, secrets.ErrAccessDenied) and so on)
// because it is joined into the chain alongside this sentinel.
var ErrCredentialsUnavailable = errors.New("repository credentials unavailable")

// newCredentialsUnavailableError builds the typed error surfaced when
// credential resolution fails. The repository ID is included in the
// public message so an operator can identify the binding from a
// gateway response without reading server logs; the cause stays
// matchable via errors.Is for both ErrCredentialsUnavailable and the
// underlying secret-store sentinel.
func newCredentialsUnavailableError(repoID string, cause error) error {
	msg := fmt.Sprintf("repository %q credentials unavailable", repoID)
	if cause == nil {
		return domain.NewErrorWithCause(domain.ErrPrecondition, msg, ErrCredentialsUnavailable)
	}
	return domain.NewErrorWithCause(domain.ErrPrecondition, msg, errors.Join(ErrCredentialsUnavailable, cause))
}

// SecretCredentialResolver is the production CredentialResolver: it
// looks up binding.CredentialsRef in a secrets.SecretClient and wraps
// the bytes as a Credential. An empty CredentialsRef bypasses the
// secret store entirely (public repos), and any secret-store failure
// is mapped to a typed credentials-unavailable error tagged with the
// repository ID.
//
// Username controls the HTTP Basic username paired with the token; the
// empty string falls back to "x-access-token" (the convention used by
// GitHub Apps and most token-based providers).
type SecretCredentialResolver struct {
	Client   secrets.SecretClient
	Username string
}

// Resolve fetches the credential for repo. A nil repo, empty
// CredentialsRef, or whitespace-only ref returns the empty Credential
// without contacting the secret store. Any error from the secret
// store is wrapped as a typed credentials-unavailable error and the
// underlying cause stays matchable via errors.Is.
//
// The ref is validated against the binding's workspace and the git
// purpose before any Get call. In shared mode a single SecretClient
// resolves credentials for many workspaces, so a binding pointing at
// `secret-store://workspaces/other/runtime_db` could otherwise hand a
// foreign workspace's database password to git as the HTTP password
// — a cross-workspace exfil. The validation rejects:
//
//   - any ref that does not parse cleanly as a workspace SecretRef
//   - refs whose workspace differs from the binding's WorkspaceID
//   - refs whose purpose is not PurposeGit
//
// All three failures map to ErrCredentialsUnavailable so the gateway
// returns 412 with the binding ID — operators see "credentials
// unavailable" and the cause without the underlying secret leaking.
func (r *SecretCredentialResolver) Resolve(ctx context.Context, repo *repository.Repository) (Credential, error) {
	if repo == nil {
		return Credential{}, nil
	}
	ref := strings.TrimSpace(repo.CredentialsRef)
	if ref == "" {
		return Credential{}, nil
	}
	if r.Client == nil {
		return Credential{}, newCredentialsUnavailableError(repo.ID, errors.New("no secret client configured"))
	}
	workspaceID, purpose, parseErr := secrets.ParseRef(secrets.SecretRef(ref))
	if parseErr != nil {
		return Credential{}, newCredentialsUnavailableError(repo.ID, parseErr)
	}
	if repo.WorkspaceID != "" && workspaceID != repo.WorkspaceID {
		return Credential{}, newCredentialsUnavailableError(repo.ID,
			fmt.Errorf("credentials_ref workspace %q does not match repository workspace %q", workspaceID, repo.WorkspaceID))
	}
	if purpose != secrets.PurposeGit {
		return Credential{}, newCredentialsUnavailableError(repo.ID,
			fmt.Errorf("credentials_ref purpose %q is not %q", purpose, secrets.PurposeGit))
	}
	val, _, err := r.Client.Get(ctx, secrets.SecretRef(ref))
	if err != nil {
		return Credential{}, newCredentialsUnavailableError(repo.ID, err)
	}
	if len(val.Reveal()) == 0 {
		// A zero-length value from the secret backend is a misprovisioned
		// secret, not a public repo: returning it would round-trip as the
		// empty Credential and downstream cred.IsEmpty() would skip
		// auth entirely. Fail closed so the operator sees a typed
		// credentials-unavailable error instead of a silent unauthenticated
		// clone.
		return Credential{}, newCredentialsUnavailableError(repo.ID,
			errors.New("secret backend returned an empty value"))
	}
	user := r.Username
	if user == "" {
		user = "x-access-token"
	}
	return Credential{Username: user, Token: val}, nil
}
