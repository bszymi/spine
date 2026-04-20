package divergence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/branchprotect"
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
	policy branchprotect.Policy
}

// NewService creates a new divergence service.
func NewService(st store.Store, gitClient git.GitClient, events event.EventRouter) *Service {
	return &Service{store: st, git: gitClient, events: events}
}

// WithBranchProtectPolicy installs the branch-protection policy consulted
// before every divergence-branch mutation (ADR-009 §3). Divergence
// branches live in the spine/* namespace and so match no user-authored
// rule by construction — the check is an audit-consistency hook rather
// than an enforcement gate. A nil policy skips the check: divergence
// paths cannot be blocked by the policy anyway, so silent skip matches
// the null-op behaviour production would see with a real policy.
func (s *Service) WithBranchProtectPolicy(p branchprotect.Policy) {
	s.policy = p
}

// evaluateBranchOp consults the installed policy (if any) before a
// divergence-branch ref operation. Returns an error only when a wired
// policy denies or errors — nil policy is treated as a no-op per
// WithBranchProtectPolicy's contract.
func (s *Service) evaluateBranchOp(ctx context.Context, branch string, kind branchprotect.OperationKind, runID string) error {
	if s.policy == nil {
		return nil
	}
	var actor domain.Actor
	if a := domain.ActorFromContext(ctx); a != nil {
		actor = *a
	}
	decision, reasons, err := s.policy.Evaluate(ctx, branchprotect.Request{
		Branch:  branch,
		Kind:    kind,
		Actor:   actor,
		RunID:   runID,
		TraceID: observe.TraceID(ctx),
	})
	if err != nil {
		observe.Logger(ctx).Error("branch-protection evaluation failed on divergence",
			"branch", branch, "kind", kind, "run_id", runID, "error", err.Error())
		return domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", err))
	}
	if decision == branchprotect.DecisionDeny {
		msg := fmt.Sprintf("branch %q blocked by branch protection", branch)
		if len(reasons) > 0 {
			msg = reasons[0].Message
		}
		return domain.NewError(domain.ErrForbidden, msg)
	}
	return nil
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
		MinBranches:    divDef.MinBranches,
		MaxBranches:    divDef.MaxBranches,
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
	payload, err := json.Marshal(map[string]any{
		"divergence_id": divCtx.DivergenceID,
		"mode":          divDef.Mode,
		"branch_count":  len(divDef.Branches),
	})
	if err != nil {
		log.Warn("failed to marshal divergence event payload", "error", err)
	}
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID:   fmt.Sprintf("div-started-%s", divCtx.DivergenceID),
		Type:      domain.EventDivergenceStarted,
		Timestamp: now,
		RunID:     run.RunID,
		Payload:   payload,
	})

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
		MaxBranches: divCtx.MaxBranches,
	}); err != nil {
		return nil, err
	}

	run := &domain.Run{RunID: divCtx.RunID}
	branch, err := s.createBranchRecord(ctx, run, divCtx, branchID, startStep)
	if err != nil {
		return nil, err
	}

	// Auto-close window when max branches reached.
	if divCtx.MaxBranches > 0 && len(branches)+1 >= divCtx.MaxBranches {
		divCtx.DivergenceWindow = "closed"
		if err := s.store.UpdateDivergenceContext(ctx, divCtx); err != nil {
			observe.Logger(ctx).Warn("failed to auto-close window", "error", err)
		} else {
			observe.Logger(ctx).Info("divergence window auto-closed",
				"divergence_id", divCtx.DivergenceID, "branch_count", len(branches)+1)
		}
	}

	return branch, nil
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

	// Create Git branch first so a failure doesn't leave an orphaned DB record.
	gitBranchName := fmt.Sprintf("spine/%s/%s/%s", run.RunID, divCtx.DivergenceID, branchID)
	// Route through the branch-protection policy (ADR-009 §3). spine/*
	// branches never match user rules, so this is audit-consistency only.
	if err := s.evaluateBranchOp(ctx, gitBranchName, branchprotect.OpDirectWrite, run.RunID); err != nil {
		return nil, err
	}
	if err := s.git.CreateBranch(ctx, gitBranchName, "HEAD"); err != nil {
		return nil, fmt.Errorf("create git branch %s: %w", gitBranchName, err)
	}

	if err := s.store.CreateBranch(ctx, branch); err != nil {
		// Clean up the Git branch on DB failure.
		_ = s.git.DeleteBranch(ctx, gitBranchName)
		return nil, err
	}

	return branch, nil
}
