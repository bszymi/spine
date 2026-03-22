package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// Service handles token validation and management.
type Service struct {
	store store.Store
}

// NewService creates a new auth service.
func NewService(st store.Store) *Service {
	return &Service{store: st}
}

// ValidateToken validates a raw bearer token and returns the associated actor.
// Returns ErrUnauthorized if the token is invalid, expired, or revoked.
func (s *Service) ValidateToken(ctx context.Context, rawToken string) (*domain.Actor, error) {
	if rawToken == "" {
		return nil, domain.NewError(domain.ErrUnauthorized, "token required")
	}

	hash := HashToken(rawToken)
	actor, token, err := s.store.GetActorByTokenHash(ctx, hash)
	if err != nil {
		return nil, err
	}

	if token.RevokedAt != nil {
		return nil, domain.NewError(domain.ErrUnauthorized, "token revoked")
	}
	if token.ExpiresAt != nil && token.ExpiresAt.Before(time.Now()) {
		return nil, domain.NewError(domain.ErrUnauthorized, "token expired")
	}
	if actor.Status != domain.ActorStatusActive {
		return nil, domain.NewError(domain.ErrUnauthorized,
			fmt.Sprintf("actor %s", actor.Status))
	}

	return actor, nil
}

// CreateToken generates a new token for an actor.
// Returns the plaintext token (only available at creation time) and the token ID.
func (s *Service) CreateToken(ctx context.Context, actorID, name string, expiresAt *time.Time) (string, string, error) {
	// Verify actor exists
	if _, err := s.store.GetActor(ctx, actorID); err != nil {
		return "", "", err
	}

	plaintext, err := GenerateToken()
	if err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}

	tokenID := fmt.Sprintf("tok_%s", plaintext[:16])
	now := time.Now()

	record := &store.TokenRecord{
		TokenID:   tokenID,
		ActorID:   actorID,
		TokenHash: HashToken(plaintext),
		Name:      name,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := s.store.CreateToken(ctx, record); err != nil {
		return "", "", fmt.Errorf("store token: %w", err)
	}

	return plaintext, tokenID, nil
}

// RevokeToken marks a token as revoked.
func (s *Service) RevokeToken(ctx context.Context, tokenID string) error {
	return s.store.RevokeToken(ctx, tokenID)
}
