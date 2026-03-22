package divergence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

// Service manages divergence lifecycle and branch operations.
type Service struct {
	store  store.Store
	git    git.GitClient
	events event.EventRouter
}

// NewService creates a new divergence service.
func NewService(st store.Store, gitClient git.GitClient, events event.EventRouter) *Service {
	return &Service{store: st, git: gitClient, events: events}
}

// StartDivergence creates a divergence context and its branches.
func (s *Service) StartDivergence(ctx context.Context, run *domain.Run, divDef domain.DivergenceDefinition, convergenceID string) (*domain.DivergenceContext, error) {
	log := observe.Logger(ctx)
	now := time.Now()

	divCtx := &domain.DivergenceContext{
		DivergenceID:   fmt.Sprintf("%s-div-%s", run.RunID, divDef.ID),
		RunID:          run.RunID,
		Status:         domain.DivergenceStatusPending,
		DivergenceMode: divDef.Mode,
		ConvergenceID:  convergenceID,
	}

	if divDef.Mode == domain.DivergenceModeExploratory {
		divCtx.DivergenceWindow = "open"
	} else {
		divCtx.DivergenceWindow = "closed"
	}

	if err := s.store.CreateDivergenceContext(ctx, divCtx); err != nil {
		return nil, fmt.Errorf("create divergence context: %w", err)
	}

	// Transition to active
	result, err := workflow.EvaluateDivergenceTransition(divCtx.Status, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerStart,
	})
	if err != nil {
		return nil, err
	}
	divCtx.Status = result.ToStatus
	divCtx.TriggeredAt = &now
	if err := s.store.UpdateDivergenceContext(ctx, divCtx); err != nil {
		return nil, fmt.Errorf("update divergence context: %w", err)
	}

	// Create branches for structured divergence
	if divDef.Mode == domain.DivergenceModeStructured {
		for _, branchDef := range divDef.Branches {
			if err := s.createBranch(ctx, run, divCtx, branchDef.ID, branchDef.StartStep); err != nil {
				return nil, fmt.Errorf("create branch %s: %w", branchDef.ID, err)
			}
		}
	}

	// Emit divergence_started event
	payload, _ := json.Marshal(map[string]any{
		"divergence_id": divCtx.DivergenceID,
		"mode":          divDef.Mode,
		"branch_count":  len(divDef.Branches),
	})
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("div-started-%s", divCtx.DivergenceID),
		Type:      domain.EventDivergenceStarted,
		Timestamp: now,
		RunID:     run.RunID,
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit divergence_started event", "error", err)
	}

	log.Info("divergence started",
		"divergence_id", divCtx.DivergenceID,
		"mode", divDef.Mode,
		"branches", len(divDef.Branches),
	)
	return divCtx, nil
}

// CreateExploratoryBranch adds a new branch to an active exploratory divergence.
func (s *Service) CreateExploratoryBranch(ctx context.Context, divCtx *domain.DivergenceContext, branchID, startStep string) (*domain.Branch, error) {
	if divCtx.DivergenceMode != domain.DivergenceModeExploratory {
		return nil, domain.NewError(domain.ErrConflict, "can only create branches in exploratory divergence")
	}

	branches, err := s.store.ListBranchesByDivergence(ctx, divCtx.DivergenceID)
	if err != nil {
		return nil, err
	}

	// Validate transition (checks window + max branches)
	if _, err := workflow.EvaluateDivergenceTransition(divCtx.Status, workflow.DivergenceTransitionRequest{
		Trigger:     workflow.DivergenceTriggerCreateBranch,
		WindowOpen:  divCtx.DivergenceWindow == "open",
		BranchCount: len(branches),
		MaxBranches: 0, // no limit from here; workflow def would provide this
	}); err != nil {
		return nil, err
	}

	run := &domain.Run{RunID: divCtx.RunID}
	return s.createBranchRecord(ctx, run, divCtx, branchID, startStep)
}

// CloseWindow closes the exploratory divergence window.
func (s *Service) CloseWindow(ctx context.Context, divCtx *domain.DivergenceContext) error {
	if _, err := workflow.EvaluateDivergenceTransition(divCtx.Status, workflow.DivergenceTransitionRequest{
		Trigger:    workflow.DivergenceTriggerCloseWindow,
		WindowOpen: divCtx.DivergenceWindow == "open",
	}); err != nil {
		return err
	}

	divCtx.DivergenceWindow = "closed"
	return s.store.UpdateDivergenceContext(ctx, divCtx)
}

func (s *Service) createBranch(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, branchID, startStep string) error {
	_, err := s.createBranchRecord(ctx, run, divCtx, branchID, startStep)
	return err
}

func (s *Service) createBranchRecord(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, branchID, startStep string) (*domain.Branch, error) {
	branch := &domain.Branch{
		BranchID:      fmt.Sprintf("%s-%s", divCtx.DivergenceID, branchID),
		RunID:         run.RunID,
		DivergenceID:  divCtx.DivergenceID,
		Status:        domain.BranchStatusPending,
		CurrentStepID: startStep,
		CreatedAt:     time.Now(),
	}

	if err := s.store.CreateBranch(ctx, branch); err != nil {
		return nil, err
	}

	// Create Git branch for isolation
	gitBranchName := fmt.Sprintf("spine/%s/%s/%s", run.RunID, divCtx.DivergenceID, branchID)
	if err := s.git.CreateBranch(ctx, gitBranchName, "HEAD"); err != nil {
		return nil, fmt.Errorf("create git branch %s: %w", gitBranchName, err)
	}

	return branch, nil
}
