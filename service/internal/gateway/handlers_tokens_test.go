package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/service/internal/workspace"
)

// tokenStubStore is a typed minimal store.AuthStore implementation for
// token-handler routing tests (INIT-022 EPIC-001 TASK-029). It depends
// on the narrowest role auth.NewService needs, so adding a method to
// AuthStore fails the var _ assertion at the bottom of this file rather
// than silently leaving a runtime panic in these tests.
type tokenStubStore struct {
	actors  map[string]*domain.Actor
	tokens  map[string]*store.TokenRecord
	revoked map[string]bool
}

func newTokenStubStore() *tokenStubStore {
	return &tokenStubStore{
		actors:  map[string]*domain.Actor{},
		tokens:  map[string]*store.TokenRecord{},
		revoked: map[string]bool{},
	}
}

func (s *tokenStubStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := s.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

func (s *tokenStubStore) CreateToken(_ context.Context, r *store.TokenRecord) error {
	s.tokens[r.TokenID] = r
	return nil
}

func (s *tokenStubStore) RevokeToken(_ context.Context, tokenID string) error {
	if _, ok := s.tokens[tokenID]; !ok {
		return domain.NewError(domain.ErrNotFound, "token not found")
	}
	s.revoked[tokenID] = true
	return nil
}

// Remaining store.AuthStore methods are no-ops; the token routing tests
// never exercise them. Listing them here keeps the var _ store.AuthStore
// assertion below truthful and forces an explicit decision (override vs.
// stay no-op) whenever AuthStore grows.
func (s *tokenStubStore) CreateActor(_ context.Context, _ *domain.Actor) error { return nil }
func (s *tokenStubStore) UpdateActor(_ context.Context, _ *domain.Actor) error { return nil }
func (s *tokenStubStore) ListActors(_ context.Context) ([]domain.Actor, error) { return nil, nil }
func (s *tokenStubStore) ListActorsByStatus(_ context.Context, _ domain.ActorStatus) ([]domain.Actor, error) {
	return nil, nil
}
func (s *tokenStubStore) GetActorByTokenHash(_ context.Context, _ string) (*domain.Actor, *domain.Token, error) {
	return nil, nil, nil
}
func (s *tokenStubStore) ListTokensByActor(_ context.Context, _ string) ([]domain.Token, error) {
	return nil, nil
}

var _ store.AuthStore = (*tokenStubStore)(nil)

func seedAdminActor(s *tokenStubStore, id string) {
	s.actors[id] = &domain.Actor{
		ActorID: id,
		Type:    domain.ActorTypeHuman,
		Name:    "admin",
		Role:    domain.RoleAdmin,
		Status:  domain.ActorStatusActive,
	}
}

func withWorkspaceAuth(req *http.Request, a *auth.Service) *http.Request {
	ctx := context.WithValue(req.Context(), serviceSetKey{}, &workspace.ServiceSet{Auth: a})
	return req.WithContext(ctx)
}

func TestHandleTokenCreate_UsesWorkspaceScopedAuth(t *testing.T) {
	serverStore := newTokenStubStore()
	wsStore := newTokenStubStore()
	seedAdminActor(serverStore, "actor-1")
	seedAdminActor(wsStore, "actor-1")

	srv := &Server{auth: auth.NewService(serverStore), devMode: true}
	wsAuth := auth.NewService(wsStore)

	body, _ := json.Marshal(createTokenRequest{ActorID: "actor-1", Name: "ci"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withWorkspaceAuth(req, wsAuth)

	rec := httptest.NewRecorder()
	srv.handleTokenCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(wsStore.tokens) != 1 {
		t.Errorf("expected token written to workspace store, got %d", len(wsStore.tokens))
	}
	if len(serverStore.tokens) != 0 {
		t.Errorf("expected server-level store untouched, got %d tokens", len(serverStore.tokens))
	}
}

func TestHandleTokenRevoke_UsesWorkspaceScopedAuth(t *testing.T) {
	serverStore := newTokenStubStore()
	wsStore := newTokenStubStore()
	seedAdminActor(wsStore, "actor-1")

	wsAuth := auth.NewService(wsStore)
	plaintext, tokenID, err := wsAuth.CreateToken(context.Background(), "actor-1", "to-revoke", nil)
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}
	_ = plaintext

	srv := &Server{auth: auth.NewService(serverStore), devMode: true}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+tokenID, http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("token_id", tokenID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withWorkspaceAuth(req, wsAuth)

	rec := httptest.NewRecorder()
	srv.handleTokenRevoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !wsStore.revoked[tokenID] {
		t.Errorf("expected workspace store to record revoke for %s", tokenID)
	}
	if len(serverStore.tokens) != 0 || len(serverStore.revoked) != 0 {
		t.Errorf("expected server-level store untouched, got tokens=%d revoked=%d", len(serverStore.tokens), len(serverStore.revoked))
	}
}

func TestHandleTokenRevoke_WorkspaceTokenIDNotInServerStore(t *testing.T) {
	// Revocation must hit only the workspace's auth backing store. If the
	// handler fell through to the server-level service, a token issued by
	// the workspace would surface as not_found there even though it
	// exists in the workspace.
	serverStore := newTokenStubStore()
	wsStore := newTokenStubStore()
	seedAdminActor(wsStore, "actor-1")

	wsAuth := auth.NewService(wsStore)
	_, tokenID, err := wsAuth.CreateToken(context.Background(), "actor-1", "ws-only", nil)
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}

	srv := &Server{auth: auth.NewService(serverStore), devMode: true}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+tokenID, http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("token_id", tokenID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withWorkspaceAuth(req, wsAuth)

	rec := httptest.NewRecorder()
	srv.handleTokenRevoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (workspace-scoped revoke), got %d: %s", rec.Code, rec.Body.String())
	}
}
