package actor

import (
	"context"
	"sync"

	"github.com/bszymi/spine/internal/domain"
)

// SelectionStrategy determines how an actor is chosen from eligible candidates.
type SelectionStrategy string

const (
	StrategyExplicit    SelectionStrategy = "explicit"
	StrategyAnyEligible SelectionStrategy = "any_eligible"
	StrategyRoundRobin  SelectionStrategy = "round_robin"
)

// SelectionRequest describes criteria for selecting an actor.
type SelectionRequest struct {
	EligibleActorTypes   []string
	RequiredCapabilities []string
	MinRole              domain.ActorRole
	Strategy             SelectionStrategy
	ExplicitActorID      string // for explicit strategy
}

// roundRobinState tracks assignment rotation for round-robin selection.
var (
	rrMu    sync.Mutex
	rrIndex int
)

// SelectActor chooses an actor based on the selection criteria.
// Per Actor Model §4.2: filter by type → capability → role → availability → strategy.
func (s *Service) SelectActor(ctx context.Context, req SelectionRequest) (*domain.Actor, error) {
	// For explicit strategy, validate the named actor
	if req.Strategy == StrategyExplicit {
		return s.selectExplicit(ctx, req)
	}

	// Load active actors
	actors, err := s.store.ListActorsByStatus(ctx, domain.ActorStatusActive)
	if err != nil {
		return nil, err
	}

	eligible := filterActors(actors, req)

	if len(eligible) == 0 {
		return nil, domain.NewError(domain.ErrNotFound, "no eligible actor found")
	}

	switch req.Strategy {
	case StrategyRoundRobin:
		return selectRoundRobin(eligible), nil
	default: // any_eligible
		return &eligible[0], nil
	}
}

func (s *Service) selectExplicit(ctx context.Context, req SelectionRequest) (*domain.Actor, error) {
	if req.ExplicitActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "explicit strategy requires actor_id")
	}

	actor, err := s.store.GetActor(ctx, req.ExplicitActorID)
	if err != nil {
		return nil, err
	}

	if actor.Status != domain.ActorStatusActive {
		return nil, domain.NewError(domain.ErrConflict, "actor is not active")
	}

	// Verify eligibility
	eligible := filterActors([]domain.Actor{*actor}, req)
	if len(eligible) == 0 {
		return nil, domain.NewError(domain.ErrConflict, "actor does not meet selection criteria")
	}

	return &eligible[0], nil
}

// filterActors applies the selection criteria filters.
func filterActors(actors []domain.Actor, req SelectionRequest) []domain.Actor {
	var result []domain.Actor

	for i := range actors {
		actor := &actors[i]

		// Filter by type
		if len(req.EligibleActorTypes) > 0 && !contains(req.EligibleActorTypes, string(actor.Type)) {
			continue
		}

		// Filter by capabilities
		if !hasAllCapabilities(actor.Capabilities, req.RequiredCapabilities) {
			continue
		}

		// Filter by role
		if req.MinRole != "" && !actor.Role.HasAtLeast(req.MinRole) {
			continue
		}

		result = append(result, *actor)
	}
	return result
}

func selectRoundRobin(actors []domain.Actor) *domain.Actor {
	rrMu.Lock()
	defer rrMu.Unlock()

	idx := rrIndex % len(actors)
	rrIndex++
	return &actors[idx]
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func hasAllCapabilities(actorCaps, required []string) bool {
	if len(required) == 0 {
		return true
	}
	capSet := make(map[string]bool, len(actorCaps))
	for _, c := range actorCaps {
		capSet[c] = true
	}
	for _, req := range required {
		if !capSet[req] {
			return false
		}
	}
	return true
}
