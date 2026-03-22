package actor

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// Service manages actor lifecycle and provides actor queries.
type Service struct {
	store store.Store
}

// NewService creates a new actor service.
func NewService(st store.Store) *Service {
	return &Service{store: st}
}

// Register creates a new actor with active status.
func (s *Service) Register(ctx context.Context, actor *domain.Actor) error {
	if actor.ActorID == "" {
		return domain.NewError(domain.ErrInvalidParams, "actor_id required")
	}
	if actor.Status == "" {
		actor.Status = domain.ActorStatusActive
	}
	return s.store.CreateActor(ctx, actor)
}

// Get retrieves an actor by ID.
func (s *Service) Get(ctx context.Context, actorID string) (*domain.Actor, error) {
	return s.store.GetActor(ctx, actorID)
}

// Suspend marks an actor as suspended. Suspended actors cannot be assigned new work.
func (s *Service) Suspend(ctx context.Context, actorID string) error {
	return s.updateStatus(ctx, actorID, domain.ActorStatusSuspended)
}

// Deactivate permanently deactivates an actor.
func (s *Service) Deactivate(ctx context.Context, actorID string) error {
	return s.updateStatus(ctx, actorID, domain.ActorStatusDeactivated)
}

// Reactivate restores a suspended actor to active status.
func (s *Service) Reactivate(ctx context.Context, actorID string) error {
	actor, err := s.store.GetActor(ctx, actorID)
	if err != nil {
		return err
	}
	if actor.Status == domain.ActorStatusDeactivated {
		return domain.NewError(domain.ErrConflict, "cannot reactivate a deactivated actor")
	}
	return s.updateStatus(ctx, actorID, domain.ActorStatusActive)
}

func (s *Service) updateStatus(ctx context.Context, actorID string, status domain.ActorStatus) error {
	log := observe.Logger(ctx)
	actor, err := s.store.GetActor(ctx, actorID)
	if err != nil {
		return err
	}
	actor.Status = status
	if err := s.store.UpdateActor(ctx, actor); err != nil {
		return fmt.Errorf("update actor status: %w", err)
	}
	log.Info("actor status updated", "actor_id", actorID, "status", status)
	return nil
}
