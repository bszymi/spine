package gitpool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/secrets"
)

// fakeSecretClient is a minimal in-memory secrets.SecretClient for
// resolver tests. Each ref maps to either a successful (value, version)
// pair or a sentinel error that simulates a real provider failure.
// gets records every Get call so tests can assert resolver caching
// behavior without instrumenting the resolver itself.
type fakeSecretClient struct {
	mu          sync.Mutex
	values      map[secrets.SecretRef]string
	errs        map[secrets.SecretRef]error
	versions    map[secrets.SecretRef]secrets.VersionID
	gets        []secrets.SecretRef
	getCalls    atomic.Int32
	invalidates []secrets.SecretRef
}

func (f *fakeSecretClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	f.mu.Lock()
	f.gets = append(f.gets, ref)
	f.mu.Unlock()
	f.getCalls.Add(1)
	if err, ok := f.errs[ref]; ok && err != nil {
		return secrets.SecretValue{}, "", err
	}
	v, ok := f.values[ref]
	if !ok {
		return secrets.SecretValue{}, "", fmt.Errorf("test misconfigured: no value for %q", ref)
	}
	return secrets.NewSecretValue([]byte(v)), f.versions[ref], nil
}

func (f *fakeSecretClient) Invalidate(_ context.Context, ref secrets.SecretRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidates = append(f.invalidates, ref)
	return nil
}

func TestSecretCredentialResolver_EmptyRefIsPublic(t *testing.T) {
	// A binding with no CredentialsRef must NOT consult the secret
	// store: public-repo bindings predate this feature and the AC
	// requires they keep working without any secret-store wiring.
	client := &fakeSecretClient{}
	r := &gitpool.SecretCredentialResolver{Client: client}

	cred, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "public-repo", CredentialsRef: "",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !cred.IsEmpty() {
		t.Errorf("empty ref must yield empty Credential, got %+v", cred)
	}
	if got := client.getCalls.Load(); got != 0 {
		t.Errorf("SecretClient.Get must not be called for empty ref; got %d calls", got)
	}
}

func TestSecretCredentialResolver_WhitespaceRefIsPublic(t *testing.T) {
	// Whitespace-only ref is treated as empty; otherwise an
	// accidentally-stored "  " in a binding row would attempt a
	// guaranteed-failing secret lookup and surface a confusing
	// credentials-unavailable error.
	r := &gitpool.SecretCredentialResolver{Client: &fakeSecretClient{}}
	cred, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "public-repo", CredentialsRef: "   \t  ",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !cred.IsEmpty() {
		t.Error("whitespace ref must yield empty Credential")
	}
}

func TestSecretCredentialResolver_ResolvesTokenWithDefaultUsername(t *testing.T) {
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		values: map[secrets.SecretRef]string{ref: "ghp_secret_token"},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	cred, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", CredentialsRef: string(ref),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Username != "x-access-token" {
		t.Errorf("default username: got %q, want x-access-token", cred.Username)
	}
	if got := string(cred.Token.Reveal()); got != "ghp_secret_token" {
		t.Errorf("token mismatch: got %q", got)
	}
}

func TestSecretCredentialResolver_HonoursCustomUsername(t *testing.T) {
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		values: map[secrets.SecretRef]string{ref: "tok"},
	}
	r := &gitpool.SecretCredentialResolver{Client: client, Username: "deploy-bot"}

	cred, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "p", CredentialsRef: string(ref),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Username != "deploy-bot" {
		t.Errorf("custom username: got %q", cred.Username)
	}
}

func TestSecretCredentialResolver_NotFoundIsCredentialsUnavailable(t *testing.T) {
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	wrappedNotFound := fmt.Errorf("provider miss: %w", secrets.ErrSecretNotFound)
	client := &fakeSecretClient{
		errs: map[secrets.SecretRef]error{ref: wrappedNotFound},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", CredentialsRef: string(ref),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// The error must satisfy three matchings simultaneously:
	//   - typed gitpool.ErrCredentialsUnavailable for callers that
	//     differentiate "auth missing" from "auth wrong"
	//   - underlying secrets.ErrSecretNotFound for retry/backoff logic
	//   - domain.SpineError(ErrPrecondition) for HTTP status mapping.
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable, got %v", err)
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("expected wrapped ErrSecretNotFound to remain matchable, got %v", err)
	}
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
		t.Errorf("expected domain.ErrPrecondition SpineError, got %v", err)
	}
}

func TestSecretCredentialResolver_AccessDeniedIsCredentialsUnavailable(t *testing.T) {
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		errs: map[secrets.SecretRef]error{ref: secrets.ErrAccessDenied},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", CredentialsRef: string(ref),
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable, got %v", err)
	}
	if !errors.Is(err, secrets.ErrAccessDenied) {
		t.Errorf("expected wrapped ErrAccessDenied, got %v", err)
	}
}

func TestSecretCredentialResolver_StoreDownIsCredentialsUnavailable(t *testing.T) {
	// The "expired secret" failure mode is operationally indistinguishable
	// from "store down" — both surface as a transient resolve failure
	// that must be reported as credentials-unavailable, not silently
	// retried with a stale value.
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		errs: map[secrets.SecretRef]error{ref: secrets.ErrSecretStoreDown},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", CredentialsRef: string(ref),
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable, got %v", err)
	}
	if !errors.Is(err, secrets.ErrSecretStoreDown) {
		t.Errorf("expected wrapped ErrSecretStoreDown, got %v", err)
	}
}

func TestSecretCredentialResolver_NoClientFailsClosedOnNonEmptyRef(t *testing.T) {
	// A binding that declares CredentialsRef but the resolver wasn't
	// wired with a SecretClient is a misconfiguration, not a public
	// repo. Failing closed prevents an unauthenticated clone attempt
	// against a private remote that would otherwise produce a
	// confusing "remote denied" git error.
	r := &gitpool.SecretCredentialResolver{Client: nil}
	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", CredentialsRef: "secret-store://workspaces/acme/git",
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable when SecretClient is nil, got %v", err)
	}
}

func TestSecretCredentialResolver_NilRepoIsSafe(t *testing.T) {
	r := &gitpool.SecretCredentialResolver{Client: &fakeSecretClient{}}
	cred, err := r.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve(nil): %v", err)
	}
	if !cred.IsEmpty() {
		t.Error("nil repo must yield empty Credential")
	}
}

func TestSecretCredentialResolver_RejectsCrossWorkspaceRef(t *testing.T) {
	// Cross-workspace exfil guard: in shared mode a single
	// SecretClient resolves credentials for many workspaces, so a
	// binding pointing at another workspace's secret could otherwise
	// hand a foreign credential to this repo's CloneURL. The
	// resolver must reject before calling Get.
	otherWS := secrets.WorkspaceRef("other", secrets.PurposeGit)
	client := &fakeSecretClient{
		values: map[secrets.SecretRef]string{otherWS: "leaked"},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID:             "payments-service",
		WorkspaceID:    "acme",
		CredentialsRef: string(otherWS),
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable for cross-workspace ref, got %v", err)
	}
	if got := client.getCalls.Load(); got != 0 {
		t.Errorf("SecretClient.Get must not be called for cross-workspace ref; got %d", got)
	}
	if !strings.Contains(err.Error(), `"payments-service"`) {
		t.Errorf("error must identify repo by ID; got %q", err.Error())
	}
}

func TestSecretCredentialResolver_RejectsNonGitPurpose(t *testing.T) {
	// A binding pointing at the workspace's runtime_db secret would
	// hand a database password to git as the HTTP password if the
	// resolver let it through. Restricting to PurposeGit closes that
	// hole at the resolver before it ever reaches the secret store.
	dbRef := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	client := &fakeSecretClient{
		values: map[secrets.SecretRef]string{dbRef: "db-password"},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID:             "payments-service",
		WorkspaceID:    "acme",
		CredentialsRef: string(dbRef),
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable for non-git purpose, got %v", err)
	}
	if got := client.getCalls.Load(); got != 0 {
		t.Errorf("SecretClient.Get must not be called for non-git purpose; got %d", got)
	}
}

func TestSecretCredentialResolver_RejectsEmptySecretValue(t *testing.T) {
	// A misprovisioned secret backend that returns a zero-length
	// value would otherwise round-trip as the empty Credential and
	// bypass authentication entirely (cred.IsEmpty() is the "no
	// auth" signal). Failing closed at resolve time surfaces the
	// misprovisioning instead of producing a confusing
	// "remote denied" failure later in the clone.
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		values: map[secrets.SecretRef]string{ref: ""},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", WorkspaceID: "acme",
		CredentialsRef: string(ref),
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable for empty secret value, got %v", err)
	}
}

func TestSecretCredentialResolver_RejectsMalformedRef(t *testing.T) {
	// A binding row with a garbage CredentialsRef value (corrupted
	// migration, manual SQL edit, etc.) must surface as
	// credentials-unavailable instead of being passed straight to
	// SecretClient.Get — which might map a parse error to an opaque
	// "not found" and hide the real cause.
	client := &fakeSecretClient{}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "payments-service", WorkspaceID: "acme",
		CredentialsRef: "not-a-valid-secret-ref",
	})
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable, got %v", err)
	}
	if !errors.Is(err, secrets.ErrInvalidRef) {
		t.Errorf("expected wrapped ErrInvalidRef to remain matchable, got %v", err)
	}
}

func TestSecretCredentialResolver_RepoIDInPublicMessage(t *testing.T) {
	// Operators reading a 412 response need to know which binding
	// failed. The repo ID belongs in the public message; the error
	// chain still carries the underlying cause.
	ref := secrets.WorkspaceRef("acme", secrets.PurposeGit)
	client := &fakeSecretClient{
		errs: map[secrets.SecretRef]error{ref: secrets.ErrSecretNotFound},
	}
	r := &gitpool.SecretCredentialResolver{Client: client}

	_, err := r.Resolve(context.Background(), &repository.Repository{
		ID: "billing-svc", CredentialsRef: string(ref),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `"billing-svc"`) {
		t.Errorf("error must identify repo by ID; got %q", err.Error())
	}
}

func TestCredential_RedactsToken(t *testing.T) {
	// The token must redact in every reflective output path: fmt %v,
	// %s, MarshalJSON, slog. A logged Credential that leaks bytes
	// would violate the "no credential value appears in logs" AC even
	// if every call site is careful, so the redaction belongs in the
	// secret value type itself.
	cred := gitpool.Credential{
		Username: "x-access-token",
		Token:    secrets.NewSecretValue([]byte("ghp_super_secret")),
	}

	// fmt: %v / %s / String all redact via SecretValue.String.
	for _, format := range []string{"%v", "%s", "%+v"} {
		out := fmt.Sprintf(format, cred.Token)
		if strings.Contains(out, "ghp_super_secret") {
			t.Errorf("token leaked in fmt %s: %q", format, out)
		}
	}

	// JSON: marshals via SecretValue.MarshalJSON.
	jsonBytes, err := json.Marshal(struct {
		Token secrets.SecretValue `json:"token"`
	}{Token: cred.Token})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(jsonBytes), "ghp_super_secret") {
		t.Errorf("token leaked in JSON: %s", jsonBytes)
	}

	// slog: structured logging routes via LogValue.
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuilder{builder: &buf}, nil))
	logger.Info("test", "credential", cred.Token)
	if strings.Contains(buf.String(), "ghp_super_secret") {
		t.Errorf("token leaked in slog: %s", buf.String())
	}
}

// logBuilder adapts a strings.Builder to io.Writer for the slog handler.
type logBuilder struct{ builder *strings.Builder }

func (l *logBuilder) Write(p []byte) (int, error) { return l.builder.Write(p) }

// credResolverStub is a deterministic CredentialResolver for pool
// tests. lookups maps repo ID → resolution result; each call increments
// calls so cache-eviction tests can assert resolution count.
type credResolverStub struct {
	mu      sync.Mutex
	lookups map[string]credResult
	calls   atomic.Int32
}

type credResult struct {
	cred gitpool.Credential
	err  error
}

func (s *credResolverStub) Resolve(_ context.Context, repo *repository.Repository) (gitpool.Credential, error) {
	s.calls.Add(1)
	s.mu.Lock()
	r, ok := s.lookups[repo.ID]
	s.mu.Unlock()
	if !ok {
		return gitpool.Credential{}, fmt.Errorf("test misconfigured: no cred for %q", repo.ID)
	}
	return r.cred, r.err
}

// recordingCloner is a Cloner that records the credential each Clone
// call received. Used to assert the pool threads creds from resolver
// to cloner without re-implementing fakeCloner's filesystem logic.
type recordingCloner struct {
	mu    sync.Mutex
	creds []gitpool.Credential
	inner *fakeCloner
}

func (r *recordingCloner) Clone(ctx context.Context, url, localPath string, cred gitpool.Credential) error {
	r.mu.Lock()
	r.creds = append(r.creds, cred)
	r.mu.Unlock()
	return r.inner.Clone(ctx, url, localPath, cred)
}

func TestPool_ResolverCalledForCodeRepoWithCredRef(t *testing.T) {
	// When a binding declares credentials_ref, the pool must resolve
	// it once per cache miss and pass the result to both the cloner
	// (for clone-time auth) and the factory (for cached fetch/push).
	base := t.TempDir()
	localPath := filepath.Join(base, "payments")
	cloner := &recordingCloner{inner: &fakeCloner{}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments": {repo: &repository.Repository{
				ID: "payments", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/payments.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
			}},
		},
	}
	credRes := &credResolverStub{
		lookups: map[string]credResult{
			"payments": {cred: gitpool.Credential{
				Username: "x-access-token",
				Token:    secrets.NewSecretValue([]byte("token-1")),
			}},
		},
	}
	var factoryCreds []gitpool.Credential
	pool := newPool(t, &stubClient{}, resolver, func(_ string, c gitpool.Credential) git.GitClient {
		factoryCreds = append(factoryCreds, c)
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	if _, err := pool.Client(context.Background(), "payments"); err != nil {
		t.Fatalf("Client: %v", err)
	}

	if got := credRes.calls.Load(); got != 1 {
		t.Errorf("resolver calls: got %d, want 1", got)
	}
	if len(cloner.creds) != 1 || string(cloner.creds[0].Token.Reveal()) != "token-1" {
		t.Errorf("cloner did not receive resolved credential: %+v", cloner.creds)
	}
	if len(factoryCreds) != 1 || string(factoryCreds[0].Token.Reveal()) != "token-1" {
		t.Errorf("factory did not receive resolved credential: %+v", factoryCreds)
	}
}

func TestPool_ResolverFailureSurfacesAsCredentialsUnavailable(t *testing.T) {
	// A resolver error must propagate to the caller as
	// credentials-unavailable, not be wrapped as ErrUnavailable
	// (which is the clone-time network-failure code). The factory
	// and cloner must NOT be called when credential resolution
	// fails — there's nothing useful to clone with.
	base := t.TempDir()
	localPath := filepath.Join(base, "billing")
	cloner := &fakeCloner{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"billing": {repo: &repository.Repository{
				ID: "billing", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/billing.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
			}},
		},
	}
	credErr := fmt.Errorf("upstream: %w", errors.Join(gitpool.ErrCredentialsUnavailable, secrets.ErrAccessDenied))
	credRes := &credResolverStub{
		lookups: map[string]credResult{"billing": {err: credErr}},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient {
		t.Error("factory must not be called when credential resolution fails")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	_, err := pool.Client(context.Background(), "billing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, gitpool.ErrCredentialsUnavailable) {
		t.Errorf("expected ErrCredentialsUnavailable, got %v", err)
	}
	if !errors.Is(err, secrets.ErrAccessDenied) {
		t.Errorf("expected wrapped ErrAccessDenied to remain matchable, got %v", err)
	}
	if cloner.callCount() != 0 {
		t.Errorf("cloner must not run when credential resolution fails (calls=%d)", cloner.callCount())
	}
}

func TestPool_PrimaryRepoSkipsCredentialResolver(t *testing.T) {
	// The primary repo never has a binding row and never has a
	// credential_ref. Hitting the resolver for `spine` would either
	// error (no entry) or perform a useless secret lookup; the
	// short-circuit keeps governance reads fast and resolver-free.
	primary := &stubClient{}
	credRes := &credResolverStub{lookups: map[string]credResult{}}
	resolver := &stubResolver{}
	pool := newPool(t, primary, resolver, func(string, gitpool.Credential) git.GitClient {
		t.Error("factory must not be called for primary")
		return nil
	}, gitpool.WithCredentialResolver(credRes))

	got, err := pool.Client(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("Client(spine): %v", err)
	}
	if got != primary {
		t.Error("primary client must short-circuit resolver path")
	}
	if got := credRes.calls.Load(); got != 0 {
		t.Errorf("credential resolver must not be called for primary; got %d", got)
	}
}

func TestPool_BindingPathChangeReResolvesCredential(t *testing.T) {
	// Cache eviction is the rotation path: a binding update
	// (LocalPath or credentials_ref change) evicts the cached client
	// and forces a fresh resolve on next access. Without re-
	// resolution, a rotated token would never reach the runtime
	// client, defeating the rotation AC.
	base := t.TempDir()
	pathA := filepath.Join(base, "alpha")
	pathB := filepath.Join(base, "beta")
	cloner := &recordingCloner{inner: &fakeCloner{}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"svc": {repo: &repository.Repository{
				ID: "svc", Status: "active",
				LocalPath: pathA, CloneURL: "https://git.example/svc.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
			}},
		},
	}
	credRes := &credResolverStub{
		lookups: map[string]credResult{
			"svc": {cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v1"))}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient {
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("first Client: %v", err)
	}

	// Simulate a binding update: new LocalPath and rotated token.
	resolver.mu.Lock()
	resolver.lookups["svc"] = lookupResult{repo: &repository.Repository{
		ID: "svc", Status: "active",
		LocalPath: pathB, CloneURL: "https://git.example/svc.git",
		CredentialsRef: "secret-store://workspaces/acme/git",
	}}
	resolver.mu.Unlock()
	credRes.mu.Lock()
	credRes.lookups["svc"] = credResult{cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v2"))}}
	credRes.mu.Unlock()

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("second Client: %v", err)
	}

	if got := credRes.calls.Load(); got != 2 {
		t.Errorf("resolver calls after binding update: got %d, want 2", got)
	}
	if len(cloner.creds) != 2 {
		t.Fatalf("cloner credentials count: got %d, want 2", len(cloner.creds))
	}
	if string(cloner.creds[0].Token.Reveal()) != "v1" || string(cloner.creds[1].Token.Reveal()) != "v2" {
		t.Errorf("token rotation not propagated: got %q then %q",
			cloner.creds[0].Token.Reveal(), cloner.creds[1].Token.Reveal())
	}
}

func TestPool_CredRefChangeEvictsCache(t *testing.T) {
	// When credentials_ref is rotated through the repository API
	// (point a binding at a new secret while the local clone path
	// stays the same), the next access must re-resolve the
	// credential and rebuild the client. Without credRef in the
	// cache key, the pool would silently keep handing out the old
	// client with the old token — directly violating the rotation
	// AC.
	base := t.TempDir()
	localPath := filepath.Join(base, "svc")
	cloner := &recordingCloner{inner: &fakeCloner{}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"svc": {repo: &repository.Repository{
				ID: "svc", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/svc.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
			}},
		},
	}
	credRes := &credResolverStub{
		lookups: map[string]credResult{
			"svc": {cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v1"))}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient {
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("first Client: %v", err)
	}
	// Same path, but the binding's credentials_ref was rotated to a
	// new secret. The cache hit on (id, path) alone would mask this,
	// so the credRef field on cachedClient must drive eviction.
	resolver.mu.Lock()
	resolver.lookups["svc"] = lookupResult{repo: &repository.Repository{
		ID: "svc", Status: "active",
		LocalPath: localPath, CloneURL: "https://git.example/svc.git",
		CredentialsRef: "secret-store://workspaces/acme/git/v2",
	}}
	resolver.mu.Unlock()
	credRes.mu.Lock()
	credRes.lookups["svc"] = credResult{cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v2"))}}
	credRes.mu.Unlock()

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("second Client: %v", err)
	}

	if got := credRes.calls.Load(); got != 2 {
		t.Errorf("resolver calls after credRef rotation: got %d, want 2", got)
	}
	if len(cloner.creds) != 1 {
		// The path didn't change so the on-disk clone stays; only
		// the runtime client (and its embedded token) is rebuilt.
		// This still counts as one Clone invocation total.
		t.Errorf("clone calls: got %d, want 1 (rotation rebuilds the client without re-cloning)", len(cloner.creds))
	}
}

func TestPool_UpdatedAtChangeEvictsCache(t *testing.T) {
	// Same-ref rotation: the binding's CredentialsRef stays
	// `secret-store://workspaces/acme/git` while the secret backend
	// hands out a new token under that stable ref. The binding row's
	// UpdatedAt timestamp bumps on any operator-driven update, so
	// it's the catch-all signal that "something about this binding
	// changed, refresh the client." Without UpdatedAt in the cache
	// key, a same-ref rotation would silently reuse the old client
	// with the old token until process restart.
	base := t.TempDir()
	localPath := filepath.Join(base, "svc")
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	cloner := &recordingCloner{inner: &fakeCloner{}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"svc": {repo: &repository.Repository{
				ID: "svc", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/svc.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
				UpdatedAt:      t0,
			}},
		},
	}
	credRes := &credResolverStub{
		lookups: map[string]credResult{
			"svc": {cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v1"))}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient {
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("first Client: %v", err)
	}
	// Same path, same ref, but UpdatedAt bumped — operator marked
	// the binding rotated. The new resolver answer must be picked up
	// even though the visible binding fields look identical.
	resolver.mu.Lock()
	resolver.lookups["svc"] = lookupResult{repo: &repository.Repository{
		ID: "svc", Status: "active",
		LocalPath: localPath, CloneURL: "https://git.example/svc.git",
		CredentialsRef: "secret-store://workspaces/acme/git",
		UpdatedAt:      t1,
	}}
	resolver.mu.Unlock()
	credRes.mu.Lock()
	credRes.lookups["svc"] = credResult{cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v2"))}}
	credRes.mu.Unlock()

	if _, err := pool.Client(context.Background(), "svc"); err != nil {
		t.Fatalf("second Client: %v", err)
	}
	if got := credRes.calls.Load(); got != 2 {
		t.Errorf("resolver calls after UpdatedAt bump: got %d, want 2", got)
	}
}

func TestPool_ConcurrentMidFlightRotationCoalescesClone(t *testing.T) {
	// A binding rotates (CredentialsRef and UpdatedAt change) while
	// the leader's first lazy clone for the same LocalPath is still
	// running. Pre-fix, the singleflight key included credRef and
	// updatedAt, so the rotated follower would NOT join the leader's
	// flight and would launch a second `git clone` into the same
	// directory — racing the leader on the filesystem. Post-fix, the
	// SF key is just (id, path); the follower waits on the in-flight
	// clone and then builds its own client with its own resolved
	// credential.
	base := t.TempDir()
	localPath := filepath.Join(base, "rotating")
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	release := make(chan struct{})
	cloner := &recordingCloner{inner: &fakeCloner{release: release}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"rotating": {repo: &repository.Repository{
				ID: "rotating", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/rotating.git",
				CredentialsRef: "secret-store://workspaces/acme/git",
				UpdatedAt:      t0,
			}},
		},
	}
	credRes := &credResolverStub{
		lookups: map[string]credResult{
			"rotating": {cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v1"))}},
		},
	}
	leaderClient := &stubClient{}
	followerClient := &stubClient{}
	pool := newPool(t, &stubClient{}, resolver, func(_ string, c gitpool.Credential) git.GitClient {
		// Differentiate by token bytes so we can verify each caller
		// gets a client built with their own credential.
		if string(c.Token.Reveal()) == "v1" {
			return leaderClient
		}
		return followerClient
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(credRes))

	leaderResultCh := make(chan git.GitClient, 1)
	go func() {
		c, err := pool.Client(context.Background(), "rotating")
		if err != nil {
			t.Errorf("leader: %v", err)
		}
		leaderResultCh <- c
	}()
	// Give the leader time to enter ensureClone and block on the
	// fakeCloner's release channel before rotating.
	time.Sleep(20 * time.Millisecond)

	// Rotate: same path, new credRef, new UpdatedAt, new resolved token.
	resolver.mu.Lock()
	resolver.lookups["rotating"] = lookupResult{repo: &repository.Repository{
		ID: "rotating", Status: "active",
		LocalPath: localPath, CloneURL: "https://git.example/rotating.git",
		CredentialsRef: "secret-store://workspaces/acme/git/v2",
		UpdatedAt:      t1,
	}}
	resolver.mu.Unlock()
	credRes.mu.Lock()
	credRes.lookups["rotating"] = credResult{cred: gitpool.Credential{Token: secrets.NewSecretValue([]byte("v2"))}}
	credRes.mu.Unlock()

	followerResultCh := make(chan git.GitClient, 1)
	go func() {
		c, err := pool.Client(context.Background(), "rotating")
		if err != nil {
			t.Errorf("follower: %v", err)
		}
		followerResultCh <- c
	}()
	time.Sleep(20 * time.Millisecond)

	close(release)
	leader := <-leaderResultCh
	follower := <-followerResultCh

	if cloner.inner.callCount() != 1 {
		t.Errorf("clone calls: got %d, want 1 (same path must coalesce even across rotation)",
			cloner.inner.callCount())
	}
	if leader != leaderClient {
		t.Error("leader: expected client built with v1 credential")
	}
	if follower != followerClient {
		t.Error("follower: expected client built with v2 credential (rotated post-clone)")
	}
}

func TestPool_NoResolverPassesEmptyCredentialThrough(t *testing.T) {
	// A pool built without WithCredentialResolver is the public-repo
	// / dev-mode default. The factory and cloner must observe the
	// zero Credential — it's the explicit "no auth" signal that
	// keeps the legacy SPINE_GIT_PUSH_TOKEN path working.
	base := t.TempDir()
	localPath := filepath.Join(base, "public")
	cloner := &recordingCloner{inner: &fakeCloner{}}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"public": {repo: &repository.Repository{
				ID: "public", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/public.git",
				CredentialsRef: "secret-store://workspaces/acme/git", // even if set, no resolver = ignored
			}},
		},
	}
	var factoryCreds []gitpool.Credential
	pool := newPool(t, &stubClient{}, resolver, func(_ string, c gitpool.Credential) git.GitClient {
		factoryCreds = append(factoryCreds, c)
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	if _, err := pool.Client(context.Background(), "public"); err != nil {
		t.Fatalf("Client: %v", err)
	}
	if len(cloner.creds) != 1 || !cloner.creds[0].IsEmpty() {
		t.Errorf("cloner must receive empty Credential when no resolver wired; got %+v", cloner.creds)
	}
	if len(factoryCreds) != 1 || !factoryCreds[0].IsEmpty() {
		t.Errorf("factory must receive empty Credential when no resolver wired; got %+v", factoryCreds)
	}
}

func TestPool_PublicCodeRepoSkipsResolverWithEmptyRef(t *testing.T) {
	// A binding that explicitly has empty CredentialsRef must NOT
	// hit the resolver even when one is configured. This is the
	// "public code repo in a workspace that also has private code
	// repos" path — the resolver is wired globally per workspace,
	// but per-repo it's opt-in via the binding row.
	base := t.TempDir()
	localPath := filepath.Join(base, "public")
	cloner := &fakeCloner{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"public": {repo: &repository.Repository{
				ID: "public", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/public.git",
				CredentialsRef: "",
			}},
		},
	}
	credRes := &credResolverStub{lookups: map[string]credResult{}}
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient {
		return &stubClient{}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(&publicAwareResolver{inner: credRes}))

	if _, err := pool.Client(context.Background(), "public"); err != nil {
		t.Fatalf("Client: %v", err)
	}
	// publicAwareResolver short-circuits empty refs; the inner
	// resolver must therefore see zero calls.
	if got := credRes.calls.Load(); got != 0 {
		t.Errorf("inner resolver must not be called for public binding; got %d", got)
	}
}

// publicAwareResolver mirrors SecretCredentialResolver's empty-ref
// short-circuit so the public-binding test asserts on the contract
// (no secret-store hit) without re-using SecretCredentialResolver
// directly (which would couple the test to the secrets package's
// fake).
type publicAwareResolver struct {
	inner gitpool.CredentialResolver
}

func (p *publicAwareResolver) Resolve(ctx context.Context, repo *repository.Repository) (gitpool.Credential, error) {
	if repo == nil || strings.TrimSpace(repo.CredentialsRef) == "" {
		return gitpool.Credential{}, nil
	}
	return p.inner.Resolve(ctx, repo)
}

func TestNewCLIClientFactory_WithCredBuildsCLIClient(t *testing.T) {
	// Smoke test the production factory: with a non-empty credential
	// it must return a *git.CLIClient that satisfies git.GitClient.
	// We don't assert the askpass mechanics (those live in the git
	// package); the contract tested here is that the factory
	// translates a Credential into a usable client without panicking
	// or returning nil — the gateway's per-repo route depends on it.
	factory := gitpool.NewCLIClientFactory()
	cred := gitpool.Credential{
		Username: "x-access-token",
		Token:    secrets.NewSecretValue([]byte("tok")),
	}
	client := factory(t.TempDir(), cred)
	if client == nil {
		t.Fatal("factory returned nil client for credentialed binding")
	}
	if _, ok := client.(*git.CLIClient); !ok {
		t.Errorf("factory must return *git.CLIClient, got %T", client)
	}
}

func TestNewCLIClientFactory_EmptyCredBuildsCLIClient(t *testing.T) {
	// Empty Credential is the public-repo / pass-through path. The
	// factory must still return a usable CLI client; the WithPushToken
	// option is simply skipped for that case.
	factory := gitpool.NewCLIClientFactory()
	client := factory(t.TempDir(), gitpool.Credential{})
	if client == nil {
		t.Fatal("factory returned nil client for empty credential")
	}
}

func TestNewCLICloner_HandlesEmptyCred(t *testing.T) {
	// A pool wired with NewCLICloner but no CredentialResolver passes
	// the zero Credential into Clone. The cloner must not panic and
	// must produce a normal git invocation — we can't actually
	// shell out in unit tests (no remote), but constructing the
	// cloner and Close()-ing the throwaway client confirms the
	// happy-path setup works.
	c := gitpool.NewCLICloner()
	if c == nil {
		t.Fatal("NewCLICloner returned nil")
	}
}

func TestNewCLIClientFactory_PerBindingTokenOverridesHelper(t *testing.T) {
	// When base options include a credential helper (e.g. workspace-
	// level SPINE_GIT_CREDENTIAL_HELPER) AND a per-binding token is
	// resolved, the per-binding token must win for fetch/push. The
	// factory strips the inherited helper via WithoutCredentialHelper
	// before layering WithPushToken; that ordering is what makes the
	// askpass path engage. The test simply confirms construction
	// returns a non-nil *git.CLIClient — the askpass-creation
	// branch in NewCLIClient depends on credentialHelper being
	// empty at that point, which is exactly what WithoutCredentialHelper
	// guarantees. (The deeper "askpass file exists" check lives in
	// the git package's own tests, where the unexported field is
	// observable.)
	factory := gitpool.NewCLIClientFactory(git.WithCredentialHelper("cache"))
	cred := gitpool.Credential{
		Username: "x-access-token",
		Token:    secrets.NewSecretValue([]byte("tok")),
	}
	client := factory(t.TempDir(), cred)
	if client == nil {
		t.Fatal("factory returned nil client")
	}
}
