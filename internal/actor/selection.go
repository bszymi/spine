package actor

import (
	"context"
	"strings"
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

// roundRobinState tracks assignment rotation per eligible pool key.
var (
	rrMu      sync.Mutex
	rrIndices = make(map[string]int)
)

// SelectActor chooses an actor based on the selection criteria.
// Per Actor Model §4.2: filter by type → capability → role → availability → strategy.
// Capability matching uses actor skills (via store) with fallback to the legacy
// Capabilities field for backward compatibility during migration.
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

	eligible := s.filterActorsWithSkills(ctx, actors, req)

	if len(eligible) == 0 {
		return nil, domain.NewError(domain.ErrNotFound, "no eligible actor found")
	}

	switch req.Strategy {
	case StrategyRoundRobin:
		return selectRoundRobin(eligible, req), nil
	case StrategyAnyEligible, "":
		return &eligible[0], nil
	default:
		return nil, domain.NewError(domain.ErrInvalidParams, "unknown selection strategy: "+string(req.Strategy))
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
	eligible := s.filterActorsWithSkills(ctx, []domain.Actor{*actor}, req)
	if len(eligible) == 0 {
		return nil, domain.NewError(domain.ErrConflict, "actor does not meet selection criteria")
	}

	return &eligible[0], nil
}

// filterActorsWithSkills applies the selection criteria filters.
// For capability matching, it first checks actor skills via the store.
// If the actor has no skills assigned, it falls back to the legacy Capabilities field.
func (s *Service) filterActorsWithSkills(ctx context.Context, actors []domain.Actor, req SelectionRequest) []domain.Actor {
	var result []domain.Actor

	for i := range actors {
		actor := &actors[i]

		// Filter by type
		if len(req.EligibleActorTypes) > 0 && !contains(req.EligibleActorTypes, string(actor.Type)) {
			continue
		}

		// Filter by capabilities: try skills first, fall back to legacy field
		if len(req.RequiredCapabilities) > 0 && !s.actorHasCapabilities(ctx, actor, req.RequiredCapabilities) {
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

// actorHasCapabilities checks if an actor has all required capabilities.
// It first checks assigned skills via the store. If the actor has no skills,
// it falls back to the legacy Capabilities []string field.
func (s *Service) actorHasCapabilities(ctx context.Context, actor *domain.Actor, required []string) bool {
	skills, err := s.store.ListActorSkills(ctx, actor.ActorID)
	if err == nil && len(skills) > 0 {
		skillNames := make(map[string]bool, len(skills))
		for _, sk := range skills {
			skillNames[sk.Name] = true
		}
		for _, req := range required {
			if !skillNames[req] {
				return false
			}
		}
		return true
	}

	// Fallback to legacy capabilities field
	return hasAllCapabilities(actor.Capabilities, required)
}

func selectRoundRobin(actors []domain.Actor, req SelectionRequest) *domain.Actor {
	// Build a pool key from the selection criteria to keep rotation per-pool
	key := strings.Join(req.EligibleActorTypes, ",") + "|" + strings.Join(req.RequiredCapabilities, ",") + "|" + string(req.MinRole)

	rrMu.Lock()
	defer rrMu.Unlock()

	idx := rrIndices[key] % len(actors)
	rrIndices[key]++
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
