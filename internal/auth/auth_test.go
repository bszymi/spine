package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ── Fake Store ──

type fakeStore struct {
	store.Store
	actors map[string]*domain.Actor
	tokens map[string]*fakeTokenEntry
}

type fakeTokenEntry struct {
	actor *domain.Actor
	token *domain.Token
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		actors: make(map[string]*domain.Actor),
		tokens: make(map[string]*fakeTokenEntry),
	}
}

func (f *fakeStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := f.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

func (f *fakeStore) GetActorByTokenHash(_ context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	entry, ok := f.tokens[tokenHash]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	return entry.actor, entry.token, nil
}

func (f *fakeStore) CreateToken(_ context.Context, record *store.TokenRecord) error {
	actor := f.actors[record.ActorID]
	if actor == nil {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	f.tokens[record.TokenHash] = &fakeTokenEntry{
		actor: actor,
		token: &domain.Token{
			TokenID:   record.TokenID,
			ActorID:   record.ActorID,
			Name:      record.Name,
			ExpiresAt: record.ExpiresAt,
			CreatedAt: record.CreatedAt,
		},
	}
	return nil
}

func (f *fakeStore) RevokeToken(_ context.Context, tokenID string) error {
	for _, entry := range f.tokens {
		if entry.token.TokenID == tokenID {
			now := time.Now()
			entry.token.RevokedAt = &now
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "token not found")
}

// ── ValidateToken Tests ──

func TestValidateTokenSuccess(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}

	svc := auth.NewService(fs)
	plaintext, _, err := svc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	actor, err := svc.ValidateToken(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if actor.ActorID != "actor-1" {
		t.Errorf("expected actor-1, got %s", actor.ActorID)
	}
}

func TestValidateTokenEmpty(t *testing.T) {
	svc := auth.NewService(newFakeStore())
	_, err := svc.ValidateToken(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	svc := auth.NewService(newFakeStore())
	_, err := svc.ValidateToken(context.Background(), "nonexistent-token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestValidateTokenRevoked(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}

	svc := auth.NewService(fs)
	plaintext, tokenID, err := svc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.RevokeToken(context.Background(), tokenID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err = svc.ValidateToken(context.Background(), plaintext)
	if err == nil {
		t.Fatal("expected error for revoked token")
	}
}

func TestValidateTokenExpired(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}

	svc := auth.NewService(fs)
	past := time.Now().Add(-1 * time.Hour)
	plaintext, _, err := svc.CreateToken(context.Background(), "actor-1", "test", &past)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = svc.ValidateToken(context.Background(), plaintext)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestValidateTokenSuspendedActor(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: domain.RoleContributor, Status: domain.ActorStatusSuspended,
	}

	svc := auth.NewService(fs)
	plaintext, _, err := svc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = svc.ValidateToken(context.Background(), plaintext)
	if err == nil {
		t.Fatal("expected error for suspended actor")
	}
}

func TestValidateTokenDeactivatedActor(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: domain.RoleContributor, Status: domain.ActorStatusDeactivated,
	}

	svc := auth.NewService(fs)
	plaintext, _, err := svc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = svc.ValidateToken(context.Background(), plaintext)
	if err == nil {
		t.Fatal("expected error for deactivated actor")
	}
}

// ── CreateToken Tests ──

func TestCreateTokenSuccess(t *testing.T) {
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Status: domain.ActorStatusActive,
	}

	svc := auth.NewService(fs)
	plaintext, tokenID, err := svc.CreateToken(context.Background(), "actor-1", "my-token", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if plaintext == "" {
		t.Error("expected non-empty plaintext")
	}
	if tokenID == "" {
		t.Error("expected non-empty token ID")
	}
}

func TestCreateTokenMissingActor(t *testing.T) {
	svc := auth.NewService(newFakeStore())
	_, _, err := svc.CreateToken(context.Background(), "nonexistent", "test", nil)
	if err == nil {
		t.Fatal("expected error for missing actor")
	}
}

// ── Hash Tests ──

func TestGenerateTokenUniqueness(t *testing.T) {
	t1, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	t2, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if t1 == t2 {
		t.Error("expected unique tokens")
	}
	if len(t1) != 64 {
		t.Errorf("expected 64-char hex token, got %d chars", len(t1))
	}
}

func TestHashTokenDeterministic(t *testing.T) {
	h1 := auth.HashToken("test-token")
	h2 := auth.HashToken("test-token")
	if h1 != h2 {
		t.Error("expected deterministic hash")
	}
	h3 := auth.HashToken("different-token")
	if h1 == h3 {
		t.Error("expected different hash for different input")
	}
}

// ── Permissions Tests ──

func TestAuthorizeAllOperations(t *testing.T) {
	tests := []struct {
		op      auth.Operation
		role    domain.ActorRole
		allowed bool
	}{
		// Reader can read
		{"artifact.read", domain.RoleReader, true},
		{"query.artifacts", domain.RoleReader, true},
		// Reader cannot create
		{"artifact.create", domain.RoleReader, false},
		// Contributor can create
		{"artifact.create", domain.RoleContributor, true},
		{"run.start", domain.RoleContributor, true},
		// Contributor cannot cancel runs
		{"run.cancel", domain.RoleContributor, false},
		// Reviewer can accept tasks
		{"task.accept", domain.RoleReviewer, true},
		// Reviewer cannot cancel runs
		{"run.cancel", domain.RoleReviewer, false},
		// Operator can cancel runs
		{"run.cancel", domain.RoleOperator, true},
		{"system.rebuild", domain.RoleOperator, true},
		// Operator cannot manage tokens
		{"token.create", domain.RoleOperator, false},
		// Admin can do everything
		{"token.create", domain.RoleAdmin, true},
		{"artifact.create", domain.RoleAdmin, true},
		{"run.cancel", domain.RoleAdmin, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.op)+"/"+string(tt.role), func(t *testing.T) {
			actor := &domain.Actor{Role: tt.role}
			err := auth.Authorize(actor, tt.op)
			if tt.allowed && err != nil {
				t.Errorf("expected allowed, got error: %v", err)
			}
			if !tt.allowed && err == nil {
				t.Errorf("expected denied for %s with role %s", tt.op, tt.role)
			}
		})
	}
}

func TestAuthorizeUnknownOperation(t *testing.T) {
	actor := &domain.Actor{Role: domain.RoleAdmin}
	err := auth.Authorize(actor, "unknown.op")
	if err == nil {
		t.Error("expected error for unknown operation")
	}
}

func TestRequiredRoleKnown(t *testing.T) {
	role, ok := auth.RequiredRole("artifact.create")
	if !ok {
		t.Fatal("expected known operation")
	}
	if role != domain.RoleContributor {
		t.Errorf("expected contributor, got %s", role)
	}
}

func TestRequiredRoleUnknown(t *testing.T) {
	_, ok := auth.RequiredRole("nonexistent")
	if ok {
		t.Error("expected unknown operation")
	}
}
