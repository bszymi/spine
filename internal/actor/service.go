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
	actor.Status = domain.ActorStatusActive
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

// AddSkill assigns a skill to an actor.
func (s *Service) AddSkill(ctx context.Context, actorID, skillID string) error {
	if actorID == "" || skillID == "" {
		return domain.NewError(domain.ErrInvalidParams, "actor_id and skill_id required")
	}
	return s.store.AddSkillToActor(ctx, actorID, skillID)
}

// RemoveSkill removes a skill from an actor.
func (s *Service) RemoveSkill(ctx context.Context, actorID, skillID string) error {
	if actorID == "" || skillID == "" {
		return domain.NewError(domain.ErrInvalidParams, "actor_id and skill_id required")
	}
	return s.store.RemoveSkillFromActor(ctx, actorID, skillID)
}

// ListSkills returns skills assigned to an actor.
func (s *Service) ListSkills(ctx context.Context, actorID string) ([]domain.Skill, error) {
	return s.store.ListActorSkills(ctx, actorID)
}

// SkillEligibilityResult describes the outcome of a skill eligibility check.
type SkillEligibilityResult struct {
	Eligible      bool
	MissingSkills []string
}

// ValidateSkillEligibility checks whether an actor has all required capabilities.
// Returns which skills are missing if the actor is not eligible.
func (s *Service) ValidateSkillEligibility(ctx context.Context, actorID string, requiredCapabilities []string) (*SkillEligibilityResult, error) {
	if len(requiredCapabilities) == 0 {
		return &SkillEligibilityResult{Eligible: true}, nil
	}

	actor, err := s.store.GetActor(ctx, actorID)
	if err != nil {
		return nil, err
	}

	// Check skills first, fall back to legacy capabilities
	skills, skillErr := s.store.ListActorSkills(ctx, actorID)
	useSkills := skillErr == nil && len(skills) > 0

	var capSet map[string]bool
	if useSkills {
		capSet = make(map[string]bool, len(skills))
		for _, sk := range skills {
			capSet[sk.Name] = true
		}
	} else {
		capSet = make(map[string]bool, len(actor.Capabilities))
		for _, c := range actor.Capabilities {
			capSet[c] = true
		}
	}

	var missing []string
	for _, req := range requiredCapabilities {
		if !capSet[req] {
			missing = append(missing, req)
		}
	}

	return &SkillEligibilityResult{
		Eligible:      len(missing) == 0,
		MissingSkills: missing,
	}, nil
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
