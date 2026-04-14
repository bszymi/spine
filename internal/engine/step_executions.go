package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// StepExecutionQuery defines parameters for querying active step executions.
type StepExecutionQuery struct {
	ActorID   string // filter by assigned actor ID (empty = all)
	ActorType string // filter by eligible actor type via workflow definition (empty = all)
	Status    string // "waiting", "assigned", or "" for both
	Limit     int    // max results (default 10)
}

// StepExecutionItem is the response model for a single active step execution.
type StepExecutionItem struct {
	ExecutionID string    `json:"execution_id"`
	RunID       string    `json:"run_id"`
	StepID      string    `json:"step_id"`
	TaskPath    string    `json:"task_path"`
	Status      string    `json:"status"`
	Attempt     int       `json:"attempt"`
	CreatedAt   time.Time `json:"created_at"`
}

// stepListingStore extends the blocking store with step execution and run queries.
type stepListingStore interface {
	ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error)
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
}

// ListStepExecutions returns non-terminal step executions (waiting or assigned)
// matching the query, enriched with task_path from the parent run.
//
// When actor_type is specified the workflow definition is consulted to check
// eligible_actor_types for each step. Steps with no eligible_actor_types
// restriction are included regardless of the requested actor_type (any type
// may claim them, matching the claim eligibility logic).
func (o *Orchestrator) ListStepExecutions(ctx context.Context, q StepExecutionQuery) ([]StepExecutionItem, error) {
	if o.blocking == nil {
		return nil, fmt.Errorf("step execution listing requires blocking store")
	}
	lister, ok := o.blocking.(stepListingStore)
	if !ok {
		return nil, fmt.Errorf("blocking store does not support step execution listing")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	execs, err := lister.ListActiveStepExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active step executions: %w", err)
	}

	// Cache runs and workflow definitions to avoid redundant lookups per run.
	runCache := map[string]*domain.Run{}
	wfCache := map[string]*domain.WorkflowDefinition{} // key: path@version

	var result []StepExecutionItem
	for _, exec := range execs {
		// Only expose waiting and assigned statuses to actors.
		if exec.Status != domain.StepStatusWaiting && exec.Status != domain.StepStatusAssigned {
			continue
		}
		// Apply status filter.
		if q.Status != "" && string(exec.Status) != q.Status {
			continue
		}
		// Apply actor_id filter: only meaningful for assigned steps, since waiting
		// steps have no assigned actor yet. Skip waiting steps when actor_id is
		// requested — they haven't been claimed by anyone.
		if q.ActorID != "" && exec.Status == domain.StepStatusAssigned && exec.ActorID != q.ActorID {
			continue
		}

		// Load the parent run for task_path and workflow coordinates.
		run, hit := runCache[exec.RunID]
		if !hit {
			r, err := lister.GetRun(ctx, exec.RunID)
			if err != nil {
				continue // skip if run no longer accessible
			}
			runCache[exec.RunID] = r
			run = r
		}

		// Apply actor_type filter via the workflow step definition.
		if q.ActorType != "" {
			cacheKey := run.WorkflowPath + "@" + run.WorkflowVersion
			wfDef, wfHit := wfCache[cacheKey]
			if !wfHit {
				def, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
				if err != nil {
					continue // skip if workflow cannot be loaded
				}
				wfCache[cacheKey] = def
				wfDef = def
			}
			stepDef := findStepDef(wfDef, exec.StepID)
			if stepDef != nil && stepDef.Execution != nil && len(stepDef.Execution.EligibleActorTypes) > 0 {
				if !containsStr(stepDef.Execution.EligibleActorTypes, q.ActorType) {
					continue
				}
			}
		}

		result = append(result, StepExecutionItem{
			ExecutionID: exec.ExecutionID,
			RunID:       exec.RunID,
			StepID:      exec.StepID,
			TaskPath:    run.TaskPath,
			Status:      string(exec.Status),
			Attempt:     exec.Attempt,
			CreatedAt:   exec.CreatedAt,
		})

		if len(result) >= limit {
			break
		}
	}

	if result == nil {
		result = []StepExecutionItem{}
	}
	return result, nil
}
