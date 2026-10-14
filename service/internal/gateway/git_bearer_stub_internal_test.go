package gateway

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
)

// gitBearerStore is tokenStubStore with token validation: it resolves a
// token hash to its actor, so auth.Service.ValidateToken accepts the
// tokens auth.Service.CreateToken minted against it.
type gitBearerStore struct{ *tokenStubStore }

func (s gitBearerStore) GetActorByTokenHash(_ context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	for _, r := range s.tokens {
		if r.TokenHash != hash || s.revoked[r.TokenID] {
			continue
		}
		a, ok := s.actors[r.ActorID]
		if !ok {
			break
		}
		return a, &domain.Token{TokenID: r.TokenID, ActorID: r.ActorID, ExpiresAt: r.ExpiresAt}, nil
	}
	return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
}

// gitBearerAuth returns an auth service and a valid bearer for a new
// active actor with the given role.
func gitBearerAuth(t *testing.T, actorID string, role domain.ActorRole) (*auth.Service, string) {
	t.Helper()
	st := gitBearerStore{newTokenStubStore()}
	st.actors[actorID] = &domain.Actor{
		ActorID: actorID, Type: domain.ActorTypeHuman, Name: actorID,
		Role: role, Status: domain.ActorStatusActive,
	}
	svc := auth.NewService(st)
	tok, _, err := svc.CreateToken(context.Background(), actorID, "git-test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return svc, tok
}
