package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
)

type fakeManagerStore struct {
	*fakeBindingStore
	createErr error
	updateErr error
}

func newFakeManagerStore(bs ...store.RepositoryBinding) *fakeManagerStore {
	return &fakeManagerStore{fakeBindingStore: newFakeStore(bs...)}
}

func (f *fakeManagerStore) CreateRepositoryBinding(_ context.Context, b *store.RepositoryBinding) error {
	if f.createErr != nil {
		return f.createErr
	}
	if _, exists := f.bindings[b.RepositoryID]; exists {
		return domain.NewError(domain.ErrAlreadyExists, "duplicate binding")
	}
	if b.Status == "" {
		b.Status = store.RepositoryBindingStatusActive
	}
	f.bindings[b.RepositoryID] = *b
	return nil
}

func (f *fakeManagerStore) UpdateRepositoryBinding(_ context.Context, b *store.RepositoryBinding) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	cur, ok := f.bindings[b.RepositoryID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "binding not found")
	}
	cur.CloneURL = b.CloneURL
	cur.CredentialsRef = b.CredentialsRef
	cur.LocalPath = b.LocalPath
	if b.DefaultBranch != "" {
		cur.DefaultBranch = b.DefaultBranch
	}
	if b.Status != "" {
		cur.Status = b.Status
	}
	f.bindings[b.RepositoryID] = cur
	return nil
}

func (f *fakeManagerStore) DeactivateRepositoryBinding(_ context.Context, _, id string) error {
	cur, ok := f.bindings[id]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "binding not found")
	}
	cur.Status = store.RepositoryBindingStatusInactive
	f.bindings[id] = cur
	return nil
}

func (f *fakeManagerStore) GetActiveRepositoryBinding(ctx context.Context, ws, id string) (*store.RepositoryBinding, error) {
	b, err := f.GetRepositoryBinding(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if b.Status != store.RepositoryBindingStatusActive {
		return nil, domain.NewError(domain.ErrNotFound, "binding inactive")
	}
	return b, nil
}

type stubRunChecker struct {
	active bool
	err    error
}

func (s stubRunChecker) AnyActiveRunReferences(context.Context, string, string) (bool, error) {
	return s.active, s.err
}

func newTestManager(t *testing.T, runs repository.RunReferenceChecker, bs ...store.RepositoryBinding) (*repository.Manager, *repository.InMemoryCatalogStore, *fakeManagerStore) {
	t.Helper()
	cat := repository.NewInMemoryCatalogStore(primarySpec())
	bindings := newFakeManagerStore(bs...)
	mgr := repository.NewManager(testWorkspaceID, primarySpec(), cat, bindings, runs, "")
	return mgr, cat, bindings
}

func TestManagerRegisterPersistsCatalogAndBinding(t *testing.T) {
	mgr, cat, bindings := newTestManager(t, nil)

	got, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID:            "payments-service",
		Name:          "Payments Service",
		DefaultBranch: "main",
		Role:          "service",
		Description:   "Core payments.",
		CloneURL:      "https://example.com/payments.git",
		LocalPath:     "/r/payments",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.ID != "payments-service" || got.Kind != repository.KindCode {
		t.Errorf("unexpected returned repository: %+v", got)
	}

	c, err := cat.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := c.Get("payments-service"); !ok {
		t.Errorf("catalog entry not persisted")
	}
	if _, ok := bindings.bindings["payments-service"]; !ok {
		t.Errorf("binding row not persisted")
	}
}

func TestManagerRegisterRejectsBadIDsAndCloneURLs(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	cases := map[string]repository.RegisterRequest{
		"empty id": {
			ID: "", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"bad id": {
			ID: "Bad_ID", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"primary id reserved": {
			ID: "spine", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"reserved http segment info": {
			ID: "info", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"reserved http segment git-upload-pack": {
			ID: "git-upload-pack", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"missing name": {
			ID: "ok-id", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"missing default_branch": {
			ID: "ok-id", Name: "x",
			CloneURL: "https://example.com/x.git", LocalPath: "/r/x",
		},
		"missing local_path": {
			ID: "ok-id", Name: "x", DefaultBranch: "main",
			CloneURL: "https://example.com/x.git",
		},
		"missing clone_url": {
			ID: "ok-id", Name: "x", DefaultBranch: "main",
			LocalPath: "/r/x",
		},
		"http clone_url rejected": {
			ID: "ok-id", Name: "x", DefaultBranch: "main",
			CloneURL: "http://example.com/x.git", LocalPath: "/r/x",
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := mgr.Register(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error")
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
				t.Errorf("expected ErrInvalidParams, got %v", err)
			}
		})
	}
}

func TestManagerRegisterRollsBackCatalogOnBindingFailure(t *testing.T) {
	mgr, cat, bindings := newTestManager(t, nil)
	bindings.createErr = errors.New("postgres down")

	_, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID:   "payments-service",
		Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	})
	if err == nil {
		t.Fatalf("expected error")
	}

	c, err := cat.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := c.Get("payments-service"); ok {
		t.Errorf("catalog entry was not rolled back after binding failure")
	}

	// And a retry succeeds without manual cleanup.
	bindings.createErr = nil
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID:   "payments-service",
		Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Errorf("expected retry to succeed after rollback, got %v", err)
	}
}

func TestManagerRegisterRejectsDuplicate(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	req := repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}
	if _, err := mgr.Register(context.Background(), req); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err := mgr.Register(context.Background(), req)
	if err == nil {
		t.Fatalf("expected duplicate error")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestManagerUpdateAppliesIdentityAndOperationalChanges(t *testing.T) {
	mgr, _, bindings := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://old.example.com/p.git", LocalPath: "/r/p",
		CredentialsRef: "secret://old", Role: "service",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	newName := "Payments Service"
	newURL := "https://new.example.com/p.git"
	newCreds := "secret://new"
	got, err := mgr.Update(context.Background(), "payments-service", repository.UpdateRequest{
		Name:           &newName,
		CloneURL:       &newURL,
		CredentialsRef: &newCreds,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != newName || got.CloneURL != newURL || got.CredentialsRef != newCreds {
		t.Errorf("update did not apply: %+v", got)
	}
	if got.Role != "service" {
		t.Errorf("untouched fields should be preserved, got role=%q", got.Role)
	}
	saved := bindings.bindings["payments-service"]
	if saved.CloneURL != newURL || saved.CredentialsRef != newCreds {
		t.Errorf("binding row not updated: %+v", saved)
	}
}

func TestManagerUpdatePreservesInactiveStatus(t *testing.T) {
	mgr, _, bindings := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := mgr.Deactivate(context.Background(), "payments-service"); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	newURL := "https://example.com/p-rotated.git"
	if _, err := mgr.Update(context.Background(), "payments-service", repository.UpdateRequest{
		CloneURL: &newURL,
	}); err != nil {
		t.Fatalf("Update on inactive: %v", err)
	}
	saved := bindings.bindings["payments-service"]
	if saved.Status != store.RepositoryBindingStatusInactive {
		t.Errorf("update on inactive binding flipped status to %q (want inactive)", saved.Status)
	}
	if saved.CloneURL != newURL {
		t.Errorf("clone url not updated: %q", saved.CloneURL)
	}
}

func TestManagerUpdateRejectsPrimary(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	name := "x"
	_, err := mgr.Update(context.Background(), "spine", repository.UpdateRequest{Name: &name})
	if err == nil {
		t.Fatalf("expected error for primary update")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %v", err)
	}
}

func TestManagerUpdateValidatesBeforeWritingCatalog(t *testing.T) {
	mgr, cat, _ := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "Original", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Mix a valid identity change with an invalid clone URL — the
	// whole request must fail and the identity rewrite must NOT have
	// been persisted.
	newName := "Renamed"
	badURL := "http://example.com/p.git"
	_, err := mgr.Update(context.Background(), "payments-service", repository.UpdateRequest{
		Name:     &newName,
		CloneURL: &badURL,
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}

	c, err := cat.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, _ := c.Get("payments-service")
	if got.Name != "Original" {
		t.Errorf("identity rewrite leaked into catalog despite later validation failure: name=%q", got.Name)
	}
}

func TestManagerUpdateRollsBackCatalogWhenBindingUpdateFails(t *testing.T) {
	mgr, cat, bindings := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "Original", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	bindings.updateErr = errors.New("postgres down")
	newName := "Renamed"
	newURL := "https://example.com/p-rotated.git"
	_, err := mgr.Update(context.Background(), "payments-service", repository.UpdateRequest{
		Name:     &newName,
		CloneURL: &newURL,
	})
	if err == nil {
		t.Fatalf("expected binding-store error")
	}

	c, err := cat.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, _ := c.Get("payments-service")
	if got.Name != "Original" {
		t.Errorf("catalog identity should have rolled back after binding failure, got name=%q", got.Name)
	}
}

func TestManagerUpdateUnknownIDIsNotFound(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	name := "x"
	_, err := mgr.Update(context.Background(), "ghost-service", repository.UpdateRequest{Name: &name})
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
}

func TestManagerDeactivateRefusesWhenActiveRunsReference(t *testing.T) {
	mgr, _, bindings := newTestManager(t, stubRunChecker{active: true})
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := mgr.Deactivate(context.Background(), "payments-service")
	if err == nil {
		t.Fatalf("expected deactivation refusal")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %v", err)
	}
	if bindings.bindings["payments-service"].Status != store.RepositoryBindingStatusActive {
		t.Errorf("binding should remain active when deactivation is refused")
	}
}

func TestManagerDeactivateIdempotent(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := mgr.Deactivate(context.Background(), "payments-service"); err != nil {
		t.Fatalf("Deactivate first: %v", err)
	}
	if err := mgr.Deactivate(context.Background(), "payments-service"); err != nil {
		t.Errorf("Deactivate (already inactive) should be a no-op, got %v", err)
	}
}

func TestManagerDeactivateRejectsPrimary(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	if err := mgr.Deactivate(context.Background(), "spine"); err == nil {
		t.Fatalf("expected error for primary deactivate")
	}
}

func TestManagerListMergesCatalogAndBindings(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	all, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries (primary + payments), got %d", len(all))
	}
	if all[0].ID != "spine" {
		t.Errorf("primary not pinned first: %+v", all[0])
	}
}

func TestManagerGetReturnsInactiveForAdminViews(t *testing.T) {
	mgr, _, _ := newTestManager(t, nil)
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID: "payments-service", Name: "P", DefaultBranch: "main",
		CloneURL: "https://example.com/p.git", LocalPath: "/r/p",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := mgr.Deactivate(context.Background(), "payments-service"); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	got, err := mgr.Get(context.Background(), "payments-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.RepositoryBindingStatusInactive {
		t.Errorf("expected inactive status surfaced by Get, got %q", got.Status)
	}
}

// newManagerWithBase builds a Manager bound to the given codeRepoBase.
// Used by the LocalPath containment tests so each scenario runs with a
// fresh fake store.
func newManagerWithBase(t *testing.T, codeRepoBase string) (*repository.Manager, *fakeManagerStore) {
	t.Helper()
	cat := repository.NewInMemoryCatalogStore(primarySpec())
	bindings := newFakeManagerStore()
	mgr := repository.NewManager(testWorkspaceID, primarySpec(), cat, bindings, nil, codeRepoBase)
	return mgr, bindings
}

// TestManagerRegisterRejectsLocalPathOutsideCodeRepoBase verifies the
// containment guard rejects every recognized escape pattern with
// ErrInvalidParams and leaves the binding store untouched. Mirrors
// the P1 attack scenario: an Operator posting `local_path=/etc` (or
// `<base>/../etc`) must not land a binding row that would later make
// git-http-backend serve the host's /etc.
func TestManagerRegisterRejectsLocalPathOutsideCodeRepoBase(t *testing.T) {
	base := t.TempDir()
	cases := map[string]string{
		"absolute outside":         "/etc",
		"parent traversal":         filepath.Join(base, "..", "etc"),
		"sibling base":             base + "-other/repo",
		"base itself (not subdir)": base,
		"empty":                    "",
		"whitespace only":          "   ",
	}
	for name, localPath := range cases {
		t.Run(name, func(t *testing.T) {
			mgr, bindings := newManagerWithBase(t, base)
			_, err := mgr.Register(context.Background(), repository.RegisterRequest{
				ID:            "payments-service",
				Name:          "P",
				DefaultBranch: "main",
				CloneURL:      "https://example.com/p.git",
				LocalPath:     localPath,
			})
			if err == nil {
				t.Fatalf("expected ErrInvalidParams for local_path=%q", localPath)
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
				t.Errorf("expected ErrInvalidParams, got %v", err)
			}
			if len(bindings.bindings) != 0 {
				t.Errorf("binding row should not be created on validation failure, got %d", len(bindings.bindings))
			}
		})
	}
}

// TestManagerRegisterAcceptsLocalPathUnderCodeRepoBase confirms the
// happy path: a LocalPath under the configured base goes through.
// Both the cleaned-only form and the unclean form (with a redundant
// "/./") are accepted because filepath.Clean normalizes them
// identically.
func TestManagerRegisterAcceptsLocalPathUnderCodeRepoBase(t *testing.T) {
	base := t.TempDir()
	cases := map[string]string{
		"direct subdir":       filepath.Join(base, "payments"),
		"nested subdir":       filepath.Join(base, "team-a", "payments"),
		"redundant separator": filepath.Join(base, ".", "payments"),
	}
	for name, localPath := range cases {
		t.Run(name, func(t *testing.T) {
			mgr, _ := newManagerWithBase(t, base)
			req := repository.RegisterRequest{
				ID:            "payments-service",
				Name:          "P",
				DefaultBranch: "main",
				CloneURL:      "https://example.com/p.git",
				LocalPath:     localPath,
			}
			if _, err := mgr.Register(context.Background(), req); err != nil {
				t.Errorf("expected accept for local_path=%q under base=%q, got %v",
					localPath, base, err)
			}
		})
	}
}

// TestManagerUpdateRejectsLocalPathOutsideCodeRepoBase confirms the
// guard runs on Update too — an Operator cannot register a valid
// LocalPath then PATCH it to point outside the base.
func TestManagerUpdateRejectsLocalPathOutsideCodeRepoBase(t *testing.T) {
	base := t.TempDir()
	mgr, bindings := newManagerWithBase(t, base)
	original := filepath.Join(base, "payments")
	if _, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID:            "payments-service",
		Name:          "P",
		DefaultBranch: "main",
		CloneURL:      "https://example.com/p.git",
		LocalPath:     original,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	bad := "/etc"
	_, err := mgr.Update(context.Background(), "payments-service", repository.UpdateRequest{
		LocalPath: &bad,
	})
	if err == nil {
		t.Fatalf("expected ErrInvalidParams when patching local_path to %q", bad)
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %v", err)
	}
	if got := bindings.bindings["payments-service"].LocalPath; got != original {
		t.Errorf("binding LocalPath should be unchanged after rejected update, got %q", got)
	}
}

// TestManagerRegisterRejectsRelativeLocalPathWhenBaseConfigured
// confirms a relative local_path is refused when a base is wired.
// Without this check, filepath.Abs would silently anchor "repo-a" to
// the current process CWD: validation would pass against the
// CWD-relative path, but the binding row would persist the unresolved
// relative string. After a restart with a different CWD the gitpool
// re-resolves the same binding against a different directory, so an
// accepted binding could clone the wrong repo (or fail on a path that
// previously worked). Reject up front so the persisted string is
// always the one validation saw.
func TestManagerRegisterRejectsRelativeLocalPathWhenBaseConfigured(t *testing.T) {
	base := t.TempDir()
	cases := map[string]string{
		"bare relative":   "repo-a",
		"dot-prefixed":    "./repo-a",
		"nested relative": "team-a/repo-a",
		"parent relative": "../sibling/repo",
	}
	for name, localPath := range cases {
		t.Run(name, func(t *testing.T) {
			mgr, bindings := newManagerWithBase(t, base)
			_, err := mgr.Register(context.Background(), repository.RegisterRequest{
				ID:            "payments-service",
				Name:          "P",
				DefaultBranch: "main",
				CloneURL:      "https://example.com/p.git",
				LocalPath:     localPath,
			})
			if err == nil {
				t.Fatalf("expected ErrInvalidParams for relative local_path=%q", localPath)
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
				t.Errorf("expected ErrInvalidParams, got %v", err)
			}
			if len(bindings.bindings) != 0 {
				t.Errorf("binding row should not be created on validation failure, got %d", len(bindings.bindings))
			}
		})
	}
}

// TestManagerEmptyCodeRepoBaseSkipsContainmentCheck confirms the
// dev/test escape hatch: when no base is configured (the existing
// newTestManager construction), any non-empty LocalPath is accepted —
// matches gitpool.WithRepoBase semantics so the existing fixture-style
// tests with paths like "/r/payments" keep working without changes.
func TestManagerEmptyCodeRepoBaseSkipsContainmentCheck(t *testing.T) {
	mgr, _ := newManagerWithBase(t, "")
	_, err := mgr.Register(context.Background(), repository.RegisterRequest{
		ID:            "payments-service",
		Name:          "P",
		DefaultBranch: "main",
		CloneURL:      "https://example.com/p.git",
		LocalPath:     "/anywhere/works",
	})
	if err != nil {
		t.Errorf("expected accept when codeRepoBase is empty, got %v", err)
	}
}
