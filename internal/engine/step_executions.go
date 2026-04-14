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
// When actor_id is provided, the actor's type is looked up and used to filter
// steps via the workflow definition's eligible_actor_types. Steps with no
// eligible_actor_types restriction are included for all actor types (backward
// compatible). actor_type in the query is used as a fallback when actor_id is
// absent or the actor lookup fails.
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

	// Derive actor type from the registered actor when actor_id is provided.
	// This ensures actors only see steps compatible with their type, without
	// requiring callers to pass actor_type separately.
	actorType := q.ActorType
	if q.ActorID != "" {
		if actor, err := o.loadActor(ctx, q.ActorID); err == nil {
			actorType = string(actor.Type)
		}
	}

	// Cache runs and workflow definitions to avoid redundant lookups per run.
	runCache := map[string]*domain.Run{}
	wfCache := map[string]*domain.WorkflowDefinition{} // key: path@version

	var result []StepExecutionItem
	for i := range execs {
		exec := &execs[i]
		// Only expose waiting, assigned, and in_progress statuses to actors.
		if exec.Status != domain.StepStatusWaiting &&
			exec.Status != domain.StepStatusAssigned &&
			exec.Status != domain.StepStatusInProgress {
			continue
		}
		// Apply status filter.
		if q.Status != "" && string(exec.Status) != q.Status {
			continue
		}
		// Apply actor_id filter using eligible_actor_ids when set, otherwise fall
		// back to matching the assigned actor_id for claimed/in-progress steps.
		// For waiting steps: include if eligible_actor_ids is empty (any actor)
		// or contains the requested actor_id.
		// For assigned/in_progress steps: include if the actor_id matches the assigned actor.
		if q.ActorID != "" {
			if exec.Status == domain.StepStatusWaiting {
				if len(exec.EligibleActorIDs) > 0 && !containsStr(exec.EligibleActorIDs, q.ActorID) {
					continue
				}
			} else if exec.ActorID != q.ActorID {
				continue
			}
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
		if actorType != "" {
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
				if !containsStr(stepDef.Execution.EligibleActorTypes, actorType) {
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
