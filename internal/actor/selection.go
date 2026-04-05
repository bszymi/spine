package actor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
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
	EligibleActorTypes []string
	RequiredSkills     []string
	MinRole            domain.ActorRole
	Strategy           SelectionStrategy
	ExplicitActorID    string // for explicit strategy
}

// roundRobinState tracks assignment rotation per eligible pool key.
var (
	rrMu      sync.Mutex
	rrIndices = make(map[string]int)
)

// SelectActor chooses an actor based on the selection criteria.
// Per Actor Model §4.2: filter by type → skill → role → availability → strategy.
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

	// Check actor type eligibility
	if len(req.EligibleActorTypes) > 0 && !contains(req.EligibleActorTypes, string(actor.Type)) {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("actor type %q is not eligible (allowed: %v)", actor.Type, req.EligibleActorTypes))
	}

	// Check role eligibility
	if req.MinRole != "" && !actor.Role.HasAtLeast(req.MinRole) {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("actor role %q does not meet minimum required role %q", actor.Role, req.MinRole))
	}

	// Check skill eligibility with descriptive error
	if len(req.RequiredSkills) > 0 {
		result, err := s.ValidateSkillEligibility(ctx, actor.ActorID, req.RequiredSkills)
		if err != nil {
			return nil, fmt.Errorf("validate skill eligibility: %w", err)
		}
		if !result.Eligible {
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("actor %q missing required skills: %v", actor.ActorID, result.MissingSkills))
		}
	}

	return actor, nil
}

// filterActorsWithSkills applies the selection criteria filters.
func (s *Service) filterActorsWithSkills(ctx context.Context, actors []domain.Actor, req SelectionRequest) []domain.Actor {
	var result []domain.Actor

	for i := range actors {
		actor := &actors[i]

		// Filter by type
		if len(req.EligibleActorTypes) > 0 && !contains(req.EligibleActorTypes, string(actor.Type)) {
			continue
		}

		// Filter by skills via skill registry
		if len(req.RequiredSkills) > 0 {
			has, err := s.actorHasSkills(ctx, actor, req.RequiredSkills)
			if err != nil {
				observe.Logger(ctx).Warn("skill lookup failed during actor selection, skipping actor",
					"actor_id", actor.ActorID, "error", err)
				continue
			}
			if !has {
				continue
			}
		}

		// Filter by role
		if req.MinRole != "" && !actor.Role.HasAtLeast(req.MinRole) {
			continue
		}

		result = append(result, *actor)
	}
	return result
}

// actorHasSkills checks if an actor has all required skills by looking up
// assigned active skills via the store. Returns an error if the skill lookup
// fails so callers can distinguish DB failures from genuine skill mismatches.
func (s *Service) actorHasSkills(ctx context.Context, actor *domain.Actor, required []string) (bool, error) {
	skills, err := s.store.ListActorSkills(ctx, actor.ActorID)
	if err != nil {
		return false, fmt.Errorf("list skills for actor %s: %w", actor.ActorID, err)
	}
	skillNames := make(map[string]bool, len(skills))
	for _, sk := range skills {
		if sk.Status == domain.SkillStatusActive {
			skillNames[sk.Name] = true
		}
	}
	for _, req := range required {
		if !skillNames[req] {
			return false, nil
		}
	}
	return true, nil
}

func selectRoundRobin(actors []domain.Actor, req SelectionRequest) *domain.Actor {
	// Build a pool key from the selection criteria to keep rotation per-pool
	key := strings.Join(req.EligibleActorTypes, ",") + "|" + strings.Join(req.RequiredSkills, ",") + "|" + string(req.MinRole)

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
