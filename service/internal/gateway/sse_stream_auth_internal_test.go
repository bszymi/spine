package gateway

import (
	"context"
	"net/http"
	"sync"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
)

// streamAuthStore gives the internal SSE tests, which call handleEventStream
// with a hand-built authenticated context, the bearer and auth service
// authMiddleware would have left behind: since INIT-024 TASK-007 (N-14) a
// stream captures both at connect to re-validate on every heartbeat, and
// refuses to open without them. Each actor ID gets its own token.
type streamAuthStore struct {
	store.AuthStore // unused methods
	mu              sync.Mutex
	actors          map[string]string // token hash -> actor ID
}

func newStreamAuth() (*streamAuthStore, *auth.Service) {
	sa := &streamAuthStore{actors: map[string]string{}}
	return sa, auth.NewService(sa)
}

// bearer returns actorID's token and registers it.
func (s *streamAuthStore) bearer(actorID string) string {
	tok := "t007-stream-token-" + actorID
	s.mu.Lock()
	s.actors[auth.HashToken(tok)] = actorID
	s.mu.Unlock()
	return tok
}

func (s *streamAuthStore) GetActorByTokenHash(_ context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	s.mu.Lock()
	id, ok := s.actors[hash]
	s.mu.Unlock()
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	return &domain.Actor{ActorID: id, Role: domain.RoleReader, Status: domain.ActorStatusActive}, &domain.Token{ActorID: id}, nil
}

// withStreamBearer sets actorID's bearer on req.
func (s *streamAuthStore) withStreamBearer(req *http.Request, actorID string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+s.bearer(actorID))
	return req
}
