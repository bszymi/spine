//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func TestRepositoryBindingCRUD(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.CleanupTestData(ctx, t) })

	const ws = "ws-repo-crud"

	binding := &store.RepositoryBinding{
		RepositoryID:   "payments-service",
		WorkspaceID:    ws,
		CloneURL:       "https://github.com/acme/payments-service.git",
		CredentialsRef: "secret://payments-deploy",
		LocalPath:      "/var/spine/workspaces/" + ws + "/repos/payments-service",
		DefaultBranch:  "main",
		Status:         store.RepositoryBindingStatusActive,
	}

	if err := s.CreateRepositoryBinding(ctx, binding); err != nil {
		t.Fatalf("CreateRepositoryBinding: %v", err)
	}

	got, err := s.GetRepositoryBinding(ctx, ws, "payments-service")
	if err != nil {
		t.Fatalf("GetRepositoryBinding: %v", err)
	}
	if got.CloneURL != binding.CloneURL || got.CredentialsRef != binding.CredentialsRef {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, binding)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("expected non-zero timestamps, got %+v", got)
	}

	got.CloneURL = "git@github.com:acme/payments-service.git"
	got.DefaultBranch = "develop"
	if err := s.UpdateRepositoryBinding(ctx, got); err != nil {
		t.Fatalf("UpdateRepositoryBinding: %v", err)
	}

	updated, err := s.GetRepositoryBinding(ctx, ws, "payments-service")
	if err != nil {
		t.Fatalf("GetRepositoryBinding (after update): %v", err)
	}
	if updated.CloneURL != "git@github.com:acme/payments-service.git" {
		t.Errorf("expected updated clone url, got %q", updated.CloneURL)
	}
	if updated.DefaultBranch != "develop" {
		t.Errorf("expected updated default branch 'develop', got %q", updated.DefaultBranch)
	}
}

func TestRepositoryBindingListsAndDeactivate(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.CleanupTestData(ctx, t) })

	const ws = "ws-repo-list"

	bindings := []store.RepositoryBinding{
		{RepositoryID: "api-gateway", WorkspaceID: ws, CloneURL: "https://example.com/api-gateway.git", LocalPath: "/r/api-gateway"},
		{RepositoryID: "payments-service", WorkspaceID: ws, CloneURL: "https://example.com/payments.git", LocalPath: "/r/payments"},
		{RepositoryID: "shared-libs", WorkspaceID: ws, CloneURL: "https://example.com/shared.git", LocalPath: "/r/shared"},
	}
	for i := range bindings {
		if err := s.CreateRepositoryBinding(ctx, &bindings[i]); err != nil {
			t.Fatalf("CreateRepositoryBinding %s: %v", bindings[i].RepositoryID, err)
		}
	}

	// Bindings under a different workspace must not leak in.
	other := &store.RepositoryBinding{
		RepositoryID: "api-gateway",
		WorkspaceID:  "ws-other",
		CloneURL:     "https://example.com/other.git",
		LocalPath:    "/r/other",
	}
	if err := s.CreateRepositoryBinding(ctx, other); err != nil {
		t.Fatalf("CreateRepositoryBinding (other workspace): %v", err)
	}

	all, err := s.ListRepositoryBindings(ctx, ws)
	if err != nil {
		t.Fatalf("ListRepositoryBindings: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 bindings for %q, got %d", ws, len(all))
	}
	wantOrder := []string{"api-gateway", "payments-service", "shared-libs"}
	for i, b := range all {
		if b.RepositoryID != wantOrder[i] {
			t.Errorf("ordering mismatch at %d: got %q, want %q", i, b.RepositoryID, wantOrder[i])
		}
	}

	if err := s.DeactivateRepositoryBinding(ctx, ws, "shared-libs"); err != nil {
		t.Fatalf("DeactivateRepositoryBinding: %v", err)
	}

	active, err := s.ListActiveRepositoryBindings(ctx, ws)
	if err != nil {
		t.Fatalf("ListActiveRepositoryBindings: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active bindings, got %d", len(active))
	}
	for _, b := range active {
		if b.Status != store.RepositoryBindingStatusActive {
			t.Errorf("expected active status, got %q for %q", b.Status, b.RepositoryID)
		}
		if b.RepositoryID == "shared-libs" {
			t.Errorf("inactive binding leaked into ListActiveRepositoryBindings")
		}
	}

	if _, err := s.GetActiveRepositoryBinding(ctx, ws, "shared-libs"); err == nil {
		t.Errorf("expected GetActiveRepositoryBinding to return not-found for inactive binding")
	} else {
		var spineErr *domain.SpineError
		if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	}

	// The plain getter still surfaces the inactive row for admin views.
	inactive, err := s.GetRepositoryBinding(ctx, ws, "shared-libs")
	if err != nil {
		t.Fatalf("GetRepositoryBinding for inactive: %v", err)
	}
	if inactive.Status != store.RepositoryBindingStatusInactive {
		t.Errorf("expected status 'inactive', got %q", inactive.Status)
	}

	// Updating an inactive binding without an explicit status must
	// not silently reactivate it — operational rewrites (rotated
	// clone URL, moved local path) are common, and a flip back to
	// active would expose a deactivated repo to execution
	// resolution.
	inactive.CloneURL = "https://example.com/shared-rotated.git"
	inactive.Status = ""
	if err := s.UpdateRepositoryBinding(ctx, inactive); err != nil {
		t.Fatalf("UpdateRepositoryBinding (preserve inactive): %v", err)
	}
	stillInactive, err := s.GetRepositoryBinding(ctx, ws, "shared-libs")
	if err != nil {
		t.Fatalf("GetRepositoryBinding after preserve-status update: %v", err)
	}
	if stillInactive.Status != store.RepositoryBindingStatusInactive {
		t.Errorf("update with empty status reactivated binding: status=%q", stillInactive.Status)
	}
	if stillInactive.CloneURL != "https://example.com/shared-rotated.git" {
		t.Errorf("expected updated clone url, got %q", stillInactive.CloneURL)
	}
}

func TestRepositoryBindingPrimaryRejection(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.CleanupTestData(ctx, t) })

	primary := &store.RepositoryBinding{
		RepositoryID: store.PrimaryRepositoryID,
		WorkspaceID:  "ws-primary",
		CloneURL:     "https://example.com/spine.git",
		LocalPath:    "/r/spine",
	}
	err := s.CreateRepositoryBinding(ctx, primary)
	if err == nil {
		t.Fatalf("expected primary 'spine' binding to be rejected")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %v", err)
	}

	// Sanity-check: after a failed primary create, no primary row exists.
	all, err := s.ListRepositoryBindings(ctx, "ws-primary")
	if err != nil {
		t.Fatalf("ListRepositoryBindings: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected no rows after rejected primary create, got %d", len(all))
	}
}

func TestRepositoryBindingNotFound(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() { s.CleanupTestData(ctx, t) })

	if _, err := s.GetRepositoryBinding(ctx, "ws-missing", "nope"); err == nil {
		t.Errorf("expected not-found error from GetRepositoryBinding")
	} else {
		var spineErr *domain.SpineError
		if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	}

	missing := &store.RepositoryBinding{
		RepositoryID: "nope",
		WorkspaceID:  "ws-missing",
		CloneURL:     "https://example.com/x.git",
		LocalPath:    "/r/nope",
	}
	if err := s.UpdateRepositoryBinding(ctx, missing); err == nil {
		t.Errorf("expected not-found error from UpdateRepositoryBinding")
	}
	if err := s.DeactivateRepositoryBinding(ctx, "ws-missing", "nope"); err == nil {
		t.Errorf("expected not-found error from DeactivateRepositoryBinding")
	}
}
