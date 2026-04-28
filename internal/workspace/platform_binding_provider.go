package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/secrets"
)

// PlatformBindingProvider implements Resolver by fetching a workspace
// runtime binding from the platform's internal API and dereferencing
// the binding's secret references via secrets.SecretClient. See
// ADR-011.
//
// The provider caches the binding shape (refs, URL, IDs, mode) per
// workspace with a configurable TTL. Secret values are NOT cached;
// every Resolve dereferences the refs through the SecretClient so
// in-place rotations under the same ref propagate within the
// SecretClient's own cache window.
//
// On a platform fetch failure, a previously-cached binding is served
// stale for up to StaleGrace beyond the TTL. Uncached workspaces
// return ErrWorkspaceUnavailable on platform failure.
type PlatformBindingProvider struct {
	cfg PlatformBindingConfig

	mu    sync.RWMutex
	cache map[string]bindingCacheEntry

	now func() time.Time
}

// PlatformBindingConfig configures PlatformBindingProvider.
type PlatformBindingConfig struct {
	// PlatformBaseURL is the platform API origin, e.g.
	// "https://platform.example.com". The provider appends the
	// internal binding path to this base.
	PlatformBaseURL string

	// ServiceToken is sent as "Authorization: Bearer {token}" on
	// every binding fetch.
	ServiceToken string

	// SecretClient dereferences the binding's secret references.
	// Required.
	SecretClient secrets.SecretClient

	// HTTPClient is used for binding fetches. If nil, a default
	// client with a 10s timeout is constructed.
	HTTPClient *http.Client

	// CacheTTL is how long a fetched binding is considered fresh.
	// On miss past TTL, the provider re-fetches. Default 5 min
	// (ADR-011).
	CacheTTL time.Duration

	// StaleGrace is the additional window past TTL during which a
	// cached binding may be served if the platform fetch fails.
	// Default 30 min (ADR-011).
	StaleGrace time.Duration
}

type bindingCacheEntry struct {
	binding   PlatformBinding
	etag      string
	fetchedAt time.Time
}

// PlatformBinding is the shape returned by
// GET /api/v1/internal/workspaces/{ws}/runtime-binding.
//
// It carries no secret values — only references that the resolver
// dereferences via SecretClient.
type PlatformBinding struct {
	WorkspaceID      string            `json:"workspace_id"`
	DisplayName      string            `json:"display_name"`
	SpineAPIURL      string            `json:"spine_api_url"`
	SpineWorkspaceID string            `json:"spine_workspace_id"`
	DeploymentMode   string            `json:"deployment_mode"`
	RepoPath         string            `json:"repo_path"`
	ActorScope       string            `json:"actor_scope"`
	RuntimeDBRef     secrets.SecretRef `json:"runtime_db_ref"`
	ProjectionDBRef  secrets.SecretRef `json:"projection_db_ref"`
	GitRef           secrets.SecretRef `json:"git_ref"`
}

// NewPlatformBindingProvider constructs a provider. Returns an error
// if the configuration is incomplete.
func NewPlatformBindingProvider(cfg PlatformBindingConfig) (*PlatformBindingProvider, error) {
	if cfg.PlatformBaseURL == "" {
		return nil, errors.New("platform binding provider: PlatformBaseURL is required")
	}
	if _, err := url.Parse(cfg.PlatformBaseURL); err != nil {
		return nil, fmt.Errorf("platform binding provider: invalid PlatformBaseURL: %w", err)
	}
	if cfg.ServiceToken == "" {
		return nil, errors.New("platform binding provider: ServiceToken is required")
	}
	if cfg.SecretClient == nil {
		return nil, errors.New("platform binding provider: SecretClient is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.StaleGrace == 0 {
		cfg.StaleGrace = 30 * time.Minute
	}
	cfg.PlatformBaseURL = strings.TrimRight(cfg.PlatformBaseURL, "/")

	return &PlatformBindingProvider{
		cfg:   cfg,
		cache: make(map[string]bindingCacheEntry),
		now:   time.Now,
	}, nil
}

// Resolve returns the WorkspaceConfig for workspaceID by fetching the
// binding from the platform (or serving a cached binding) and
// dereferencing each secret reference.
func (p *PlatformBindingProvider) Resolve(ctx context.Context, workspaceID string) (*Config, error) {
	if err := ValidateID(workspaceID); err != nil {
		return nil, ErrWorkspaceNotFound
	}

	binding, err := p.fetchBinding(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	cfg, err := p.assembleConfig(ctx, binding)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// List returns the bindings currently held in the cache. The platform
// binding API does not (currently) expose a multi-workspace listing
// endpoint, so the provider can only report what it has already
// resolved at least once. Callers that depend on full enumeration
// (background scheduler, projection sync) must drive resolution from
// another source until that endpoint is added.
func (p *PlatformBindingProvider) List(ctx context.Context) ([]Config, error) {
	p.mu.RLock()
	bindings := make([]PlatformBinding, 0, len(p.cache))
	for ws := range p.cache {
		bindings = append(bindings, p.cache[ws].binding)
	}
	p.mu.RUnlock()

	configs := make([]Config, 0, len(bindings))
	for i := range bindings {
		cfg, err := p.assembleConfig(ctx, bindings[i])
		if err != nil {
			// Skip workspaces whose secrets are momentarily
			// unresolvable; List is best-effort.
			continue
		}
		configs = append(configs, *cfg)
	}
	return configs, nil
}

// Invalidate drops the cached binding for workspaceID so the next
// Resolve hits the platform. Idempotent.
func (p *PlatformBindingProvider) Invalidate(workspaceID string) {
	p.mu.Lock()
	delete(p.cache, workspaceID)
	p.mu.Unlock()
}

// fetchBinding returns a current-or-stale binding for workspaceID,
// refreshing from the platform when the cache is empty or expired.
func (p *PlatformBindingProvider) fetchBinding(ctx context.Context, workspaceID string) (PlatformBinding, error) {
	p.mu.RLock()
	entry, hasCached := p.cache[workspaceID]
	p.mu.RUnlock()

	if hasCached && p.now().Sub(entry.fetchedAt) < p.cfg.CacheTTL {
		return entry.binding, nil
	}

	binding, etag, status, err := p.doFetch(ctx, workspaceID, entry.etag)
	switch {
	case err == nil && status == http.StatusOK:
		// Defence in depth: a binding whose own workspace_id (or any
		// of its secret refs) names a different workspace would route
		// the request to another tenant's credentials. Reject it
		// before caching so a misbehaving or compromised platform
		// response cannot cross-wire workspaces.
		if mismatchErr := validateBindingMatchesWorkspace(binding, workspaceID); mismatchErr != nil {
			return PlatformBinding{}, fmt.Errorf("%w: %v", ErrWorkspaceUnavailable, mismatchErr)
		}
		newEntry := bindingCacheEntry{
			binding:   binding,
			etag:      etag,
			fetchedAt: p.now(),
		}
		p.mu.Lock()
		p.cache[workspaceID] = newEntry
		p.mu.Unlock()
		return binding, nil

	case err == nil && status == http.StatusNotModified:
		if !hasCached {
			// Platform said "unchanged" but we have nothing to
			// fall back to — treat as unavailable.
			return PlatformBinding{}, fmt.Errorf("%w: platform returned 304 without cached binding for %q", ErrWorkspaceUnavailable, workspaceID)
		}
		refreshed := bindingCacheEntry{
			binding:   entry.binding,
			etag:      entry.etag,
			fetchedAt: p.now(),
		}
		p.mu.Lock()
		p.cache[workspaceID] = refreshed
		p.mu.Unlock()
		return entry.binding, nil

	case errors.Is(err, ErrWorkspaceNotFound):
		// Authoritative absence: drop any stale cache so a later
		// re-creation isn't masked.
		p.mu.Lock()
		delete(p.cache, workspaceID)
		p.mu.Unlock()
		return PlatformBinding{}, ErrWorkspaceNotFound

	case errors.Is(err, ErrWorkspaceUnavailable):
		// Authoritative refusal (e.g. access denied) — do not
		// serve stale, surface the structured error.
		return PlatformBinding{}, err
	}

	// Transient platform failure (network, 5xx, decode). Serve
	// stale-on-error if we have a cache within the grace window.
	if hasCached && p.now().Sub(entry.fetchedAt) < p.cfg.CacheTTL+p.cfg.StaleGrace {
		return entry.binding, nil
	}

	if err != nil {
		return PlatformBinding{}, fmt.Errorf("%w: platform fetch for %q: %v", ErrWorkspaceUnavailable, workspaceID, err)
	}
	return PlatformBinding{}, fmt.Errorf("%w: platform fetch for %q: unexpected status %d", ErrWorkspaceUnavailable, workspaceID, status)
}

// doFetch issues the HTTP request and maps the response. Returns
// (binding, etag, statusCode, err). On 304, binding is zero. On 404,
// returns ErrWorkspaceNotFound. On 401/403, returns
// ErrWorkspaceUnavailable wrapped with "access_denied". On other
// non-2xx, returns nil err and the raw status so the caller can apply
// stale-on-error.
func (p *PlatformBindingProvider) doFetch(ctx context.Context, workspaceID, ifNoneMatch string) (PlatformBinding, string, int, error) {
	bindingURL := p.cfg.PlatformBaseURL + "/api/v1/internal/workspaces/" + url.PathEscape(workspaceID) + "/runtime-binding"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bindingURL, http.NoBody)
	if err != nil {
		return PlatformBinding{}, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.ServiceToken)
	req.Header.Set("Accept", "application/json")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return PlatformBinding{}, "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return PlatformBinding{}, "", resp.StatusCode, readErr
		}
		var binding PlatformBinding
		if err := json.Unmarshal(body, &binding); err != nil {
			return PlatformBinding{}, "", resp.StatusCode, fmt.Errorf("decode binding: %w", err)
		}
		return binding, resp.Header.Get("ETag"), http.StatusOK, nil

	case http.StatusNotModified:
		return PlatformBinding{}, ifNoneMatch, http.StatusNotModified, nil

	case http.StatusNotFound:
		return PlatformBinding{}, "", resp.StatusCode, ErrWorkspaceNotFound

	case http.StatusUnauthorized, http.StatusForbidden:
		return PlatformBinding{}, "", resp.StatusCode,
			fmt.Errorf("%w: platform binding access denied for %q (status %d)", ErrWorkspaceUnavailable, workspaceID, resp.StatusCode)

	default:
		return PlatformBinding{}, "", resp.StatusCode, nil
	}
}

// assembleConfig dereferences each ref in the binding and builds the
// resolver Config. Errors from SecretClient are mapped to
// ErrWorkspaceUnavailable; ErrInvalidRef is mapped to
// ErrWorkspaceNotFound on the assumption that the platform sent a
// malformed ref for a workspace that effectively cannot be served.
func (p *PlatformBindingProvider) assembleConfig(ctx context.Context, b PlatformBinding) (*Config, error) {
	runtimeDB, err := p.revealRef(ctx, b.RuntimeDBRef)
	if err != nil {
		return nil, err
	}
	// The projection-DB and git refs are dereferenced eagerly so a
	// missing or denied ref surfaces here instead of mid-request when
	// downstream code first reaches for them. Their values are not
	// retained on Config: pool wiring per-purpose lands in EPIC-003,
	// and git credentials are fetched from the secret store at the
	// boundary where they're used.
	if _, err := p.revealRef(ctx, b.ProjectionDBRef); err != nil {
		return nil, err
	}
	if _, err := p.revealRef(ctx, b.GitRef); err != nil {
		return nil, err
	}

	return &Config{
		ID:             b.WorkspaceID,
		DisplayName:    b.DisplayName,
		DatabaseURL:    secrets.NewSecretValue([]byte(runtimeDB)),
		RepoPath:       b.RepoPath,
		Status:         StatusActive,
		ActorScope:     b.ActorScope,
		SMPWorkspaceID: b.SpineWorkspaceID,
	}, nil
}

// validateBindingMatchesWorkspace ensures every workspace-scoped
// field in the binding (top-level workspace_id and the workspace
// segment of each secret ref) names the workspace that was actually
// requested. A mismatch usually indicates a stale platform response
// or a serialization bug, but it would be a tenant-isolation
// violation if we honoured it, so we surface it as a hard error.
func validateBindingMatchesWorkspace(b PlatformBinding, requested string) error {
	if b.WorkspaceID != requested {
		return fmt.Errorf("binding workspace_id %q does not match requested %q", b.WorkspaceID, requested)
	}
	for _, ref := range []struct {
		name string
		ref  secrets.SecretRef
	}{
		{"runtime_db_ref", b.RuntimeDBRef},
		{"projection_db_ref", b.ProjectionDBRef},
		{"git_ref", b.GitRef},
	} {
		ws, _, err := secrets.ParseRef(ref.ref)
		if err != nil {
			return fmt.Errorf("%s is malformed: %w", ref.name, err)
		}
		if ws != requested {
			return fmt.Errorf("%s names workspace %q, expected %q", ref.name, ws, requested)
		}
	}
	return nil
}

// revealRef wraps SecretClient.Get and maps its sentinel errors onto
// the resolver's surface.
func (p *PlatformBindingProvider) revealRef(ctx context.Context, ref secrets.SecretRef) (string, error) {
	v, _, err := p.cfg.SecretClient.Get(ctx, ref)
	switch {
	case err == nil:
		return string(v.Reveal()), nil
	case errors.Is(err, secrets.ErrInvalidRef):
		return "", fmt.Errorf("%w: invalid secret ref %q: %v", ErrWorkspaceNotFound, ref, err)
	case errors.Is(err, secrets.ErrSecretNotFound),
		errors.Is(err, secrets.ErrAccessDenied),
		errors.Is(err, secrets.ErrSecretStoreDown):
		return "", fmt.Errorf("%w: secret %q: %v", ErrWorkspaceUnavailable, ref, err)
	default:
		return "", fmt.Errorf("%w: secret %q: %v", ErrWorkspaceUnavailable, ref, err)
	}
}
