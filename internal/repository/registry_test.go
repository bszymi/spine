package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
)

const testWorkspaceID = "ws-acme"

type fakeBindingStore struct {
	bindings    map[string]store.RepositoryBinding
	getErr      error
	listErr     error
	getCallsRaw int
}

func newFakeStore(bs ...store.RepositoryBinding) *fakeBindingStore {
	m := make(map[string]store.RepositoryBinding, len(bs))
	for i := range bs {
		m[bs[i].RepositoryID] = bs[i]
	}
	return &fakeBindingStore{bindings: m}
}

func (f *fakeBindingStore) GetRepositoryBinding(_ context.Context, ws, id string) (*store.RepositoryBinding, error) {
	f.getCallsRaw++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if ws != testWorkspaceID {
		return nil, domain.NewError(domain.ErrNotFound, "wrong workspace")
	}
	b, ok := f.bindings[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "repository binding not found")
	}
	out := b
	return &out, nil
}

func (f *fakeBindingStore) ListRepositoryBindings(_ context.Context, ws string) ([]store.RepositoryBinding, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if ws != testWorkspaceID {
		return nil, nil
	}
	out := make([]store.RepositoryBinding, 0, len(f.bindings))
	for id := range f.bindings {
		out = append(out, f.bindings[id])
	}
	return out, nil
}

func loaderFor(cat *repository.Catalog, err error) repository.CatalogLoader {
	return func(_ context.Context) (*repository.Catalog, error) {
		if err != nil {
			return nil, err
		}
		return cat, nil
	}
}

func mustParse(t *testing.T, body string, spec repository.PrimarySpec) *repository.Catalog {
	t.Helper()
	cat, err := repository.ParseCatalog([]byte(body), spec)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	return cat
}

func multiRepoCatalog(t *testing.T) *repository.Catalog {
	t.Helper()
	return mustParse(t, `
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
- id: payments-service
  kind: code
  name: Payments Service
  default_branch: main
  role: service
- id: api-gateway
  kind: code
  name: API Gateway
  default_branch: develop
`, repository.PrimarySpec{})
}

func primarySpec() repository.PrimarySpec {
	return repository.PrimarySpec{
		Name:          "Fallback Spine Name",
		DefaultBranch: "trunk",
		LocalPath:     "/var/spine/workspaces/acme/repos/spine",
	}
}

func TestLookupSpinePrimaryAlwaysResolves(t *testing.T) {
	// Even with no catalog file present and a nil binding store, the
	// primary repo must resolve so single-repo workspaces keep working.
	cat, err := repository.ParseCatalog(nil, primarySpec())
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), nil)

	got, err := reg.Lookup(context.Background(), "spine")
	if err != nil {
		t.Fatalf("Lookup(spine): %v", err)
	}
	if !got.IsPrimary() || got.Kind != repository.KindSpine {
		t.Errorf("expected primary kind, got %+v", got)
	}
	if got.LocalPath != primarySpec().LocalPath {
		t.Errorf("primary local path: got %q, want %q", got.LocalPath, primarySpec().LocalPath)
	}
	if !got.IsActive() {
		t.Errorf("primary must be active")
	}
}

func TestLookupSpinePrefersCatalogFieldsWhenPresent(t *testing.T) {
	cat := mustParse(t, `
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
- id: payments-service
  kind: code
  name: P
  default_branch: main
`, repository.PrimarySpec{})
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore())

	got, err := reg.Lookup(context.Background(), "spine")
	if err != nil {
		t.Fatalf("Lookup(spine): %v", err)
	}
	if got.Name != "Acme Spine" {
		t.Errorf("expected catalog name, got %q", got.Name)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("expected catalog default_branch 'main', got %q", got.DefaultBranch)
	}
	if got.LocalPath != primarySpec().LocalPath {
		t.Errorf("expected local path from PrimarySpec, got %q", got.LocalPath)
	}
}

func TestLookupCodeRepoActiveBindingMerges(t *testing.T) {
	cat := multiRepoCatalog(t)
	now := time.Now()
	binding := store.RepositoryBinding{
		RepositoryID:   "payments-service",
		WorkspaceID:    testWorkspaceID,
		CloneURL:       "https://example.com/payments.git",
		CredentialsRef: "secret://payments",
		LocalPath:      "/r/payments",
		Status:         store.RepositoryBindingStatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(binding))

	got, err := reg.Lookup(context.Background(), "payments-service")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Kind != repository.KindCode {
		t.Errorf("expected kind=code, got %q", got.Kind)
	}
	if got.CloneURL != binding.CloneURL || got.CredentialsRef != binding.CredentialsRef || got.LocalPath != binding.LocalPath {
		t.Errorf("operational fields not merged: got %+v", got)
	}
	if got.Name != "Payments Service" || got.Role != "service" {
		t.Errorf("identity fields not merged: got %+v", got)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("expected catalog default_branch, got %q", got.DefaultBranch)
	}
	if !got.IsActive() {
		t.Errorf("expected IsActive=true")
	}
}

func TestLookupBindingDefaultBranchOverridesCatalog(t *testing.T) {
	cat := multiRepoCatalog(t)
	binding := store.RepositoryBinding{
		RepositoryID:  "api-gateway",
		WorkspaceID:   testWorkspaceID,
		CloneURL:      "https://example.com/gw.git",
		LocalPath:     "/r/gw",
		Status:        store.RepositoryBindingStatusActive,
		DefaultBranch: "release-2026-04",
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(binding))
	got, err := reg.Lookup(context.Background(), "api-gateway")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.DefaultBranch != "release-2026-04" {
		t.Errorf("expected binding override 'release-2026-04', got %q", got.DefaultBranch)
	}
}

func TestLookupUnknownIDReturnsTypedNotFound(t *testing.T) {
	cat := multiRepoCatalog(t)
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore())

	_, err := reg.Lookup(context.Background(), "unknown-repo")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrNotFound {
		t.Errorf("expected SpineError code=%q, got %v", domain.ErrNotFound, err)
	}
}

func TestLookupCatalogOnlyReturnsUnbound(t *testing.T) {
	cat := multiRepoCatalog(t)
	// Catalog lists payments-service but no binding row exists.
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore())

	_, err := reg.Lookup(context.Background(), "payments-service")
	if err == nil {
		t.Fatalf("expected unbound error")
	}
	if !errors.Is(err, repository.ErrRepositoryUnbound) {
		t.Errorf("expected ErrRepositoryUnbound, got %v", err)
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected SpineError code=%q, got %v", domain.ErrPrecondition, err)
	}
}

func TestLookupBindingOnlyOrphanReturnsNotFound(t *testing.T) {
	// Binding row exists for an ID that is not in the catalog. The
	// catalog is authoritative — orphan bindings must not resolve.
	cat := mustParse(t, `
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
`, repository.PrimarySpec{})

	orphan := store.RepositoryBinding{
		RepositoryID: "ghost-service",
		WorkspaceID:  testWorkspaceID,
		CloneURL:     "https://example.com/ghost.git",
		LocalPath:    "/r/ghost",
		Status:       store.RepositoryBindingStatusActive,
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(orphan))

	_, err := reg.Lookup(context.Background(), "ghost-service")
	if err == nil {
		t.Fatalf("expected not-found error for orphan binding")
	}
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
}

func TestLookupInactiveBindingReturnsTypedInactive(t *testing.T) {
	cat := multiRepoCatalog(t)
	binding := store.RepositoryBinding{
		RepositoryID: "payments-service",
		WorkspaceID:  testWorkspaceID,
		CloneURL:     "https://example.com/payments.git",
		LocalPath:    "/r/payments",
		Status:       store.RepositoryBindingStatusInactive,
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(binding))

	_, err := reg.Lookup(context.Background(), "payments-service")
	if err == nil {
		t.Fatalf("expected inactive error")
	}
	if !errors.Is(err, repository.ErrRepositoryInactive) {
		t.Errorf("expected ErrRepositoryInactive, got %v", err)
	}
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("inactive must NOT match ErrRepositoryNotFound — different typed error")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected SpineError code=%q, got %v", domain.ErrPrecondition, err)
	}
}

func TestLookupEmptyIDInvalid(t *testing.T) {
	cat := multiRepoCatalog(t)
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore())
	_, err := reg.Lookup(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty id")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %v", err)
	}
}

func TestLookupLoaderError(t *testing.T) {
	loaderErr := errors.New("git read failed")
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(nil, loaderErr), newFakeStore())
	if _, err := reg.Lookup(context.Background(), "spine"); !errors.Is(err, loaderErr) {
		t.Errorf("expected loader error to propagate, got %v", err)
	}
}

func TestLookupStoreErrorPropagates(t *testing.T) {
	cat := multiRepoCatalog(t)
	storeErr := errors.New("postgres unreachable")
	fake := newFakeStore()
	fake.getErr = storeErr
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), fake)

	_, err := reg.Lookup(context.Background(), "payments-service")
	if !errors.Is(err, storeErr) {
		t.Errorf("expected store error to propagate, got %v", err)
	}
}

func TestListMergesBindingsAndShowsUnbound(t *testing.T) {
	cat := multiRepoCatalog(t)
	now := time.Now()
	active := store.RepositoryBinding{
		RepositoryID: "payments-service",
		WorkspaceID:  testWorkspaceID,
		CloneURL:     "https://example.com/p.git",
		LocalPath:    "/r/p",
		Status:       store.RepositoryBindingStatusActive,
		UpdatedAt:    now,
	}
	// api-gateway intentionally has no binding row.
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(active))

	all, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
	if all[0].ID != "spine" {
		t.Errorf("primary not pinned first: got %q", all[0].ID)
	}
	wantOrder := []string{"spine", "api-gateway", "payments-service"}
	for i, want := range wantOrder {
		if all[i].ID != want {
			t.Errorf("List()[%d] = %q, want %q", i, all[i].ID, want)
		}
	}
	for _, r := range all {
		switch r.ID {
		case "spine":
			if r.LocalPath != primarySpec().LocalPath {
				t.Errorf("spine.LocalPath=%q, want %q", r.LocalPath, primarySpec().LocalPath)
			}
		case "payments-service":
			if r.Status != store.RepositoryBindingStatusActive || r.CloneURL == "" {
				t.Errorf("payments-service should be active with binding details: %+v", r)
			}
		case "api-gateway":
			if r.Status != "" || r.CloneURL != "" {
				t.Errorf("api-gateway should appear unbound (empty operational fields): %+v", r)
			}
		}
	}
}

func TestListIgnoresOrphanBindings(t *testing.T) {
	cat := mustParse(t, `
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
- id: payments-service
  kind: code
  name: P
  default_branch: main
`, repository.PrimarySpec{})

	bindings := []store.RepositoryBinding{
		{RepositoryID: "payments-service", WorkspaceID: testWorkspaceID, CloneURL: "u", LocalPath: "/p", Status: store.RepositoryBindingStatusActive},
		{RepositoryID: "ghost", WorkspaceID: testWorkspaceID, CloneURL: "g", LocalPath: "/g", Status: store.RepositoryBindingStatusActive},
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(bindings...))

	all, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range all {
		if r.ID == "ghost" {
			t.Errorf("orphan binding leaked into List output")
		}
	}
	if len(all) != 2 {
		t.Errorf("expected 2 entries (primary + payments-service), got %d", len(all))
	}
}

func TestListActiveExcludesInactiveAndUnbound(t *testing.T) {
	cat := multiRepoCatalog(t)
	bindings := []store.RepositoryBinding{
		{RepositoryID: "payments-service", WorkspaceID: testWorkspaceID, CloneURL: "u", LocalPath: "/p", Status: store.RepositoryBindingStatusActive},
		{RepositoryID: "api-gateway", WorkspaceID: testWorkspaceID, CloneURL: "g", LocalPath: "/g", Status: store.RepositoryBindingStatusInactive},
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore(bindings...))

	active, err := reg.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active (primary + payments-service), got %d", len(active))
	}
	for _, r := range active {
		if !r.IsActive() {
			t.Errorf("ListActive returned non-active: %+v", r)
		}
	}
}

func TestListSingleRepoWorkspaceWithoutCatalog(t *testing.T) {
	cat, err := repository.ParseCatalog(nil, primarySpec())
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	reg := repository.New(testWorkspaceID, primarySpec(), loaderFor(cat, nil), newFakeStore())

	all, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ID != "spine" {
		t.Errorf("expected only primary entry, got %+v", all)
	}
}
