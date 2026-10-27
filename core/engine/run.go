package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// StartRunResult contains the result of starting a new run.
type StartRunResult struct {
	Run       *domain.Run
	EntryStep *domain.StepExecution
}

// startRunParams holds the pre-configured run and workflow for startRunCommon.
type startRunParams struct {
	run      *domain.Run
	wfDef    *domain.WorkflowDefinition
	onBranch func(ctx context.Context, branchName string) error // optional: called after branch creation
}

// startRunCommon handles the shared startup sequence for both standard and planning runs:
// parse timeout → persist run → create branch → (onBranch callback) → create entry step
// → activate run → emit event → activate step.
func (o *Orchestrator) startRunCommon(ctx context.Context, p startRunParams) (*StartRunResult, error) {
	log := observe.Logger(ctx)
	run := p.run
	wfDef := p.wfDef
	now := run.CreatedAt

	// Set run-level timeout if configured on the workflow.
	if wfDef.Timeout != "" {
		if d, err := time.ParseDuration(wfDef.Timeout); err == nil {
			t := now.Add(d)
			run.TimeoutAt = &t
		} else {
			log.Warn("invalid workflow timeout duration", "timeout", wfDef.Timeout, "error", err)
		}
	}

	// Create Git branches first so a failure doesn't leave an orphaned DB
	// record that the scheduler's recovery logic could later activate.
	// The primary branch is cut from HEAD; each code repo's branch is
	// cut from that repo's default branch. The branch name is identical
	// across repos so downstream tooling can address the run by a
	// single ref regardless of which repository it touches. Auto-push
	// runs per-repo inside createRunBranches so a later failure can
	// roll back symmetric refs (local + remote) on every prior repo.
	created, err := o.createRunBranches(ctx, run)
	if err != nil {
		return nil, err
	}

	if err := o.store.CreateRun(ctx, run); err != nil {
		o.rollbackRunBranches(ctx, run, created)
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Optional callback for work on the branch (e.g., writing artifacts in planning runs).
	if p.onBranch != nil {
		if err := p.onBranch(ctx, run.BranchName); err != nil {
			return nil, err
		}
	}

	// Create entry step execution BEFORE activation so that a failure here
	// leaves the run in pending (recoverable by scheduler) rather than
	// active with no step (unrecoverable).
	entryRepoID := resolveStepRepoIDByStepID(wfDef, wfDef.EntryStep)
	entryStep := &domain.StepExecution{
		ExecutionID:  fmt.Sprintf("%s-%s-1", run.RunID, wfDef.EntryStep),
		RunID:        run.RunID,
		StepID:       wfDef.EntryStep,
		RepositoryID: entryRepoID,
		Status:       domain.StepStatusWaiting,
		Attempt:      1,
		CreatedAt:    now,
	}
	if err := o.store.CreateStepExecution(ctx, entryStep); err != nil {
		return nil, fmt.Errorf("create entry step: %w", err)
	}
	recordStepRoutingDecision(ctx, run.RunID, wfDef.EntryStep, findStepDef(wfDef, wfDef.EntryStep), entryRepoID)

	// Activate: pending → active. Compare-and-swap (INIT-023 TASK-008): a
	// run that left pending meanwhile (scheduler recovery activated it, an
	// operator cancelled it) is not written over, and no run_started is
	// emitted for it.
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerActivate,
	})
	if err != nil {
		return nil, fmt.Errorf("activate run: %w", err)
	}

	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, result.ToStatus)
	if err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	if !applied {
		return nil, domain.NewErrorWithCause(domain.ErrConflict,
			fmt.Sprintf("run %s left %s before it could be activated", run.RunID, run.Status), ErrRunStateChanged)
	}
	run.Status = result.ToStatus
	startedAt := now
	run.StartedAt = &startedAt

	o.emitEvent(ctx, domain.EventRunStarted, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", run.TraceID[:12]), nil)

	log.Info("run started",
		"run_id", run.RunID,
		"task_path", run.TaskPath,
		"workflow_id", wfDef.ID,
		"entry_step", wfDef.EntryStep,
		"mode", run.Mode,
	)

	// Activate the entry step so it gets an actor assignment.
	if err := o.ActivateStep(ctx, run.RunID, wfDef.EntryStep); err != nil {
		log.Warn("entry step activation failed", "step_id", wfDef.EntryStep, "error", err)
	}

	return &StartRunResult{Run: run, EntryStep: entryStep}, nil
}

// StartRun creates a run for a task, resolves the governing workflow,
// transitions the run from pending to active, and activates the first step.
func (o *Orchestrator) StartRun(ctx context.Context, taskPath string) (*StartRunResult, error) {
	if taskPath == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "task_path is required")
	}

	art, err := o.artifacts.Read(ctx, taskPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read task artifact: %w", err)
	}

	// Check if the task is blocked by unresolved dependencies.
	if o.blocking != nil {
		blockResult, err := o.IsBlocked(ctx, taskPath)
		if err != nil {
			return nil, fmt.Errorf("check blocking status: %w", err)
		}
		if blockResult.Blocked {
			return nil, domain.NewError(domain.ErrPrecondition,
				fmt.Sprintf("task is blocked by: %v", blockResult.BlockedBy))
		}
	}

	// Repository runtime preconditions (INIT-014 EPIC-002 TASK-004) —
	// after blocking, before any branch-creating work, so a missing or
	// inactive binding fails the run start cleanly without touching Git.
	// Catalog-existence checks are TASK-003 territory and are expected
	// to have run at validate time before this point.
	if err := o.checkRepositoryPreconditions(ctx, art); err != nil {
		return nil, err
	}

	binding, err := o.workflows.ResolveWorkflow(ctx, string(art.Type), "")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	// Validate every step's resolved target repository against the
	// catalog, runtime binding, and the task's opt-in set per ADR-015
	// *Validation*. This runs before any branch creation so a
	// misconfigured workflow / task pairing fails the run start cleanly
	// without partial state.
	if err := o.validateStepRouting(ctx, binding.Workflow, art); err != nil {
		return nil, err
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])

	return o.startRunCommon(ctx, startRunParams{
		run: &domain.Run{
			RunID:                runID,
			TaskPath:             taskPath,
			WorkflowPath:         binding.Workflow.Path,
			WorkflowID:           binding.Workflow.ID,
			WorkflowVersion:      binding.CommitSHA,
			WorkflowVersionLabel: binding.VersionLabel,
			Status:               domain.RunStatusPending,
			CurrentStepID:        binding.Workflow.EntryStep,
			BranchName:           generateBranchNameWithSuffix(domain.RunModeStandard, art.ID, taskPath, runID),
			AffectedRepositories: domain.AffectedRepositoriesForTask(art),
			PrimaryRepository:    true,
			TraceID:              traceID,
			CreatedAt:            time.Now(),
		},
		wfDef: binding.Workflow,
	})
}

// StartPlanningRun creates a planning run that governs artifact creation on a branch.
// The artifact is written to the branch, not to main. Per ADR-006 §2.
func (o *Orchestrator) StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*StartRunResult, error) {
	if o.artifactWriter == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "artifact writer not configured")
	}
	// Separation of duties counts the starter as a run author (INIT-024
	// TASK-001): no starter, no run — refused before anything is read or
	// persisted.
	if err := o.requirePlanningStarter(ctx); err != nil {
		return nil, err
	}
	if artifactContent == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "artifact_content is required")
	}
	if artifactPath == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "artifact_path is required")
	}
	// One producer of workflow task paths (INIT-023 TASK-014 I9, V4): only
	// the workflow starters below may create a run whose TaskPath is under
	// workflows/. A generic planning run there would merge its Markdown into
	// workflows/ under its own creation workflow's review (not
	// workflow_review), and would carry the exact run.TaskPath key the
	// standalone gateway's one-open-lifecycle-run rule and lifecycle
	// write_context binding trust — letting a contributor lock any workflow
	// against governed edits. Refused before Parse, so nothing is persisted.
	// The test is canonical (dot segments, case folding): see
	// workflow.InWorkflowsDir. SMP's only caller (CreateEntry) builds .md
	// paths under the artifact type directories, never under workflows/.
	if workflow.InWorkflowsDir(artifactPath) {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"workflow definitions are created through the workflow-lifecycle planning run (ADR-007/ADR-008)")
	}

	parsed, err := artifact.Parse(artifactPath, []byte(artifactContent))
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("invalid artifact content: %v", err))
	}
	vResult := artifact.Validate(parsed)
	if vResult.Status != "passed" {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("artifact validation failed: %v", vResult.Errors))
	}

	binding, err := o.workflows.ResolveWorkflowForMode(ctx, string(parsed.Type), "", "creation")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	// Planning runs only touch the primary Spine repo (per ADR-006);
	// validate the creation workflow against an empty opt-in set so a
	// `repository: <code-repo>` declaration is rejected up front rather
	// than producing a clone-time failure on a non-existent run branch
	// in the code repo. The synthesized artifact carries an empty
	// Repositories list so the opt-in check resolves to {spine} only.
	if err := o.validateStepRouting(ctx, binding.Workflow, &domain.Artifact{Path: artifactPath, Type: parsed.Type}); err != nil {
		return nil, err
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	branchName := generateBranchNameWithSuffix(domain.RunModePlanning, parsed.ID, artifactPath, runID)

	run := &domain.Run{
		RunID:                runID,
		TaskPath:             artifactPath,
		WorkflowPath:         binding.Workflow.Path,
		WorkflowID:           binding.Workflow.ID,
		WorkflowVersion:      binding.CommitSHA,
		WorkflowVersionLabel: binding.VersionLabel,
		Status:               domain.RunStatusPending,
		Mode:                 domain.RunModePlanning,
		CurrentStepID:        binding.Workflow.EntryStep,
		BranchName:           branchName,
		AffectedRepositories: []string{domain.PrimaryRepositoryID},
		PrimaryRepository:    true,
		TraceID:              traceID,
		CreatedAt:            time.Now(),
	}
	return o.startRunCommon(ctx, startRunParams{
		run:   run,
		wfDef: binding.Workflow,
		onBranch: func(ctx context.Context, branch string) error {
			// The starter is on record before their content is on the
			// branch (INIT-024 TASK-001). A failed record under separation
			// of duties aborts the start like a failed write: the branch is
			// deleted, and — new failure, so only with the policy on — the
			// pending row is marked failed rather than left for recovery
			// to activate.
			if err := o.recordPlanningStarter(ctx, run); err != nil {
				o.deletePlanningBranch(ctx, branch, "planning")
				o.failOrphanPlanningRun(ctx, runID)
				return err
			}
			branchCtx := artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branch})
			branchCtx = observe.WithTraceID(branchCtx, traceID)
			branchCtx = observe.WithRunID(branchCtx, runID)
			if _, err := o.artifactWriter.Create(branchCtx, artifactPath, artifactContent); err != nil {
				o.deletePlanningBranch(ctx, branch, "planning")
				return fmt.Errorf("create artifact on branch: %w", err)
			}
			return nil
		},
	})
}

// deletePlanningBranch removes a planning run's branch (and, with
// auto-push, its remote copy) after its start failed on the branch. Best
// effort: a failure is logged and the caller's error is what is returned.
// label names the run kind in the log ("planning", "workflow planning").
func (o *Orchestrator) deletePlanningBranch(ctx context.Context, branch, label string) {
	log := observe.Logger(ctx)
	if delErr := o.git.DeleteBranch(ctx, branch); delErr != nil {
		log.Warn("failed to clean up "+label+" branch", "branch", branch, "error", delErr)
	}
	if autoPushEnabled() {
		if delErr := o.git.DeleteRemoteBranch(ctx, "origin", branch); delErr != nil {
			log.Warn("auto-push: failed to clean up remote "+label+" branch", "branch", branch, "error", delErr)
		}
	}
}

// StartWorkflowPlanningRun creates a planning-mode run governing the
// creation of a NEW workflow definition (ADR-008). The workflow body is
// validated, written to a fresh task branch, and the governing workflow is
// resolved as the `applies_to: [Workflow]` / `mode: creation` binding
// (today: workflow-lifecycle).
//
// An id whose definition already exists at the ref the run branch is cut
// from is refused with ErrAlreadyExists before any branch or run record
// exists (INIT-023 TASK-014, design-check L1); StartWorkflowUpdatePlanningRun
// is the path for an existing definition.
func (o *Orchestrator) StartWorkflowPlanningRun(ctx context.Context, workflowID, body string) (*StartRunResult, error) {
	return o.startWorkflowPlanningRun(ctx, workflowID, body, false)
}

// StartWorkflowUpdatePlanningRun creates a planning-mode run governing an
// edit of an EXISTING workflow definition (ADR-008 §1; INIT-023 TASK-014,
// audit F7). Before it, a reviewer had no governed way to edit an existing
// workflow — StartWorkflowPlanningRun always wrote through Create, which
// fails on an existing file — while workflow.update committed straight to
// main.
//
// The run is shaped exactly like a create run (Mode planning, TaskPath
// workflows/<id>.yaml, governed by the Workflow/creation binding, a
// spine/plan/... branch); only the branch write differs: the workflow
// writer's Update, which re-checks the version bump against the branch's
// copy. The prior definition is read first, at the ref the run branch is cut
// from, so a missing workflow (ErrNotFound), an unparsable prior file
// (ErrInternal) or an unchanged version (ErrValidationFailed, the message
// workflow.Service.Update uses) is refused before any branch or run record
// exists. The authoritative branch is untouched until the lifecycle review
// approves and the governed merge runs.
func (o *Orchestrator) StartWorkflowUpdatePlanningRun(ctx context.Context, workflowID, body string) (*StartRunResult, error) {
	return o.startWorkflowPlanningRun(ctx, workflowID, body, true)
}

// workflowPlanningBaseRef is the ref createRunBranches cuts the primary run
// branch from (multi_repo_branch.go). The pre-checks read the definition at
// the same ref, so they judge exactly what the branch will start from.
const workflowPlanningBaseRef = "HEAD"

func (o *Orchestrator) startWorkflowPlanningRun(ctx context.Context, workflowID, body string, update bool) (*StartRunResult, error) {
	if o.workflowWriter == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "workflow writer not configured")
	}
	// As in StartPlanningRun (INIT-024 TASK-001): refused before the
	// pre-checks, so an actor-less caller also learns nothing about which
	// workflows exist.
	if err := o.requirePlanningStarter(ctx); err != nil {
		return nil, err
	}
	if workflowID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow id is required")
	}
	if body == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow body is required")
	}
	// Before TASK-014 a bad id failed only inside onBranch, after the run
	// record and branch existed.
	if err := workflow.ValidateID(workflowID); err != nil {
		return nil, err
	}

	path := workflow.FilePath(workflowID)

	// ProjectionDefinition, not bare Parse: the body is what the
	// projection will read after the planning run merges, so a body it
	// would refuse (strict-parse or round-trip failure) is refused here
	// with the same ErrInvalidParams a parse failure returns
	// (INIT-023 TASK-011 §7).
	parsed, _, err := workflow.ProjectionDefinition(path, []byte(body))
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("invalid workflow body: %v", err))
	}
	if parsed.ID != workflowID {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("body id %q does not match requested id %q", parsed.ID, workflowID))
	}
	if vResult := workflow.Validate(parsed); vResult.Status != "passed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"workflow validation failed", vResult.Errors)
	}
	// Declared commit.status (INIT-023 TASK-017), with the orchestrator's
	// lifecycle: refused here, before any branch or run record exists, the
	// way workflow.Service refuses it inside the write (which would leave
	// a pending run behind). nil lifecycle = no check.
	if errs := workflow.ValidateCommitStatuses(parsed, o.lifecycle); len(errs) > 0 {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"workflow validation failed", errs)
	}

	// Existence pre-checks, before ResolveWorkflowForMode and therefore
	// before any branch or run record exists. Before TASK-014 a create of
	// an existing id failed only inside onBranch (the writer's Create
	// reported already_exists) and left a pending run behind, which
	// scheduler recovery could later promote to active (design-check L1).
	if err := o.workflowPlanningPreCheck(ctx, path, parsed, update); err != nil {
		return nil, err
	}

	binding, err := o.workflows.ResolveWorkflowForMode(ctx, string(domain.ArtifactTypeWorkflow), "", "creation")
	if err != nil {
		return nil, fmt.Errorf("resolve governing workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "governing workflow has no entry_step")
	}

	// Workflow planning runs only touch the primary Spine repo. Validate
	// the governing workflow's step routing against an empty opt-in set so
	// a `repository: <code-repo>` declaration is rejected here rather than
	// producing a clone-time failure later.
	if err := o.validateStepRouting(ctx, binding.Workflow, &domain.Artifact{Path: path, Type: domain.ArtifactTypeWorkflow}); err != nil {
		return nil, err
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	branchName := generateBranchNameWithSuffix(domain.RunModePlanning, parsed.ID, path, runID)

	write, verb := o.workflowWriter.Create, "create"
	if update {
		write, verb = o.workflowWriter.Update, "update"
	}

	run := &domain.Run{
		RunID:                runID,
		TaskPath:             path,
		WorkflowPath:         binding.Workflow.Path,
		WorkflowID:           binding.Workflow.ID,
		WorkflowVersion:      binding.CommitSHA,
		WorkflowVersionLabel: binding.VersionLabel,
		Status:               domain.RunStatusPending,
		Mode:                 domain.RunModePlanning,
		CurrentStepID:        binding.Workflow.EntryStep,
		BranchName:           branchName,
		AffectedRepositories: []string{domain.PrimaryRepositoryID},
		PrimaryRepository:    true,
		TraceID:              traceID,
		CreatedAt:            time.Now(),
	}
	return o.startRunCommon(ctx, startRunParams{
		run:   run,
		wfDef: binding.Workflow,
		onBranch: func(ctx context.Context, branch string) error {
			// The caller writes the body: separation of duties (INIT-024
			// TASK-001) and the lifecycle exemption (INIT-023 TASK-006) need
			// them on record, before the body is on the branch. A failed
			// record under separation of duties aborts the start like a
			// failed write.
			if err := o.recordPlanningStarter(ctx, run); err != nil {
				o.deletePlanningBranch(ctx, branch, "workflow planning")
				o.failOrphanPlanningRun(ctx, runID)
				return err
			}
			branchCtx := workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
			branchCtx = observe.WithTraceID(branchCtx, traceID)
			branchCtx = observe.WithRunID(branchCtx, runID)
			if _, err := write(branchCtx, workflowID, body); err != nil {
				o.deletePlanningBranch(ctx, branch, "workflow planning")
				o.failOrphanPlanningRun(ctx, runID)
				return fmt.Errorf("%s workflow on branch: %w", verb, err)
			}
			return nil
		},
	})
}

// workflowPlanningPreCheck reads the definition at the ref the run branch
// is cut from. Create: an existing file is ErrAlreadyExists. Update: a
// missing file is ErrNotFound, an unparsable prior file ErrInternal, and an
// unchanged version ErrValidationFailed. A read failure other than git's
// not-found is returned wrapped (fail closed: no run is started).
func (o *Orchestrator) workflowPlanningPreCheck(ctx context.Context, path string, submitted *domain.WorkflowDefinition, update bool) error {
	prior, err := o.git.ReadFile(ctx, workflowPlanningBaseRef, path)
	if err != nil {
		var gerr *git.GitError
		if !errors.As(err, &gerr) || gerr.Kind != git.ErrKindNotFound {
			return fmt.Errorf("read %s at %s: %w", path, workflowPlanningBaseRef, err)
		}
		if update {
			return domain.NewError(domain.ErrNotFound, fmt.Sprintf("workflow not found: %s", path))
		}
		return nil
	}
	if !update {
		return domain.NewError(domain.ErrAlreadyExists,
			fmt.Sprintf("workflow already exists: %s; use PUT /workflows/{id} to open an update run", path))
	}
	priorDef, err := workflow.Parse(path, prior)
	if err != nil {
		return domain.NewError(domain.ErrInternal, fmt.Sprintf("parse existing workflow: %v", err))
	}
	if priorDef.Version == submitted.Version {
		return domain.NewError(domain.ErrValidationFailed,
			fmt.Sprintf("workflow.update requires a version bump (prior=%s, submitted=%s)",
				priorDef.Version, submitted.Version))
	}
	return nil
}

// failOrphanPlanningRun marks a planning run whose start failed on its
// branch as failed (design-check L1). startRunCommon persists the run
// before onBranch runs, so without this the record stays pending with its
// branch deleted: scheduler recovery promotes pending runs to active, and
// for a workflow planning run the standalone gateway's
// one-open-lifecycle-run rule would then answer 409 for that workflow
// until an operator cancelled the orphan. Used for every workflow planning
// failure on the branch and for a failed starter record (INIT-024
// TASK-001, reachable only under separation of duties); a generic planning
// run whose artifact write fails keeps its pre-INIT-024 behaviour (the row
// is left for recovery, which embedders rely on). Best effort: a failed
// transition is logged and the caller's error is still what the caller
// sees. The transition is a compare-and-swap from pending, through the
// existing RunStore method (no new store method).
func (o *Orchestrator) failOrphanPlanningRun(ctx context.Context, runID string) {
	applied, err := o.store.TransitionRunStatus(context.WithoutCancel(ctx), runID, domain.RunStatusPending, domain.RunStatusFailed)
	if err != nil || !applied {
		observe.Logger(ctx).Warn("planning run start failed; could not mark the run failed",
			"run_id", runID, "applied", applied, "error", err)
	}
}

// CompleteRun transitions an active run to completed (or committing if
// hasCommit is true) when the terminal step has been reached.
// When hasCommit is true, the run enters the committing state for Git
// persistence before completing. Uses the run state machine to validate.
//
// The write is a compare-and-swap with re-evaluation (transitionRun,
// INIT-023 TASK-008): a run another writer moved meanwhile — a cancel that
// committed inside this read is the case that matters — is judged again
// from the state that won, so a cancelled run is never resurrected,
// merged or announced completed; the state machine's conflict is returned.
func (o *Orchestrator) CompleteRun(ctx context.Context, runID string, hasCommit bool) error {
	run, _, result, err := o.transitionRun(ctx, runID, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "end",
		HasCommit:  hasCommit,
	})
	if err != nil {
		return err
	}

	log := observe.Logger(ctx)

	// Only emit run_completed when the run actually reached completed state,
	// not when it moved to committing (which still needs Git commit).
	if result.ToStatus == domain.RunStatusCompleted {
		o.emitEvent(ctx, domain.EventRunCompleted, runID, run.TraceID,
			fmt.Sprintf("evt-%s-completed", run.TraceID[:12]), nil)
		log.Info("run completed", "run_id", runID)
		if run.StartedAt != nil {
			observe.GlobalMetrics.RunDuration.ObserveDuration(time.Since(*run.StartedAt))
		}
		// Re-evaluate tasks that were blocked by this task.
		o.CheckAndEmitBlockingTransition(ctx, run.TaskPath)
		// Update execution projection to reflect completion.
		o.updateExecutionProjection(ctx, run.TaskPath, func(proj *store.ExecutionProjection) {
			proj.AssignedActorID = ""
			proj.AssignmentStatus = "unassigned"
			proj.Status = string(domain.StatusCompleted)
		})
		// Clean up the run branch after successful completion.
		// Per-repo cleanup failures are already recorded as
		// EventRunBranchCleanupFailed inside CleanupRunBranch; the only
		// signal the call site adds is when the *primary* delete fails
		// (the only condition that makes CleanupRunBranch return non-nil).
		// Logging that here keeps orphan-branch troubleshooting attached
		// to the originating run-completion line.
		if err := o.CleanupRunBranch(ctx, runID); err != nil {
			log.Warn("cleanup_run_branch failed",
				"run_id", runID,
				"branch", run.BranchName,
				"error", err,
			)
		}
	} else {
		log.Info("run entering commit phase", "run_id", runID)

		// Immediately attempt the merge rather than waiting for the
		// scheduler poll. This is critical for planning runs where
		// artifacts live only on the branch until merged. The scheduler
		// remains as a safety net for transient failures.
		if err := o.MergeRunBranch(ctx, runID); err != nil {
			log.Warn("immediate merge attempt failed, scheduler will retry",
				"run_id", runID, "error", err)
		}
	}

	return nil
}

// FailRun transitions an active run to failed due to a permanent failure.
// The write is a compare-and-swap with re-evaluation (transitionRun); the
// executions are closed, the event emitted and the branch cleaned up only
// after it applied.
func (o *Orchestrator) FailRun(ctx context.Context, runID, reason string) error {
	run, _, _, err := o.transitionRun(ctx, runID, workflow.TransitionRequest{
		Trigger: workflow.TriggerStepFailedPermanently,
	})
	if err != nil {
		return err
	}
	// TerminateStepsOnCancel (INIT-023 TASK-007): close the run's live
	// executions now that the run row says failed.
	o.terminateLiveSteps(ctx, runID, "run failed: "+reason)

	payload, _ := json.Marshal(map[string]string{"reason": reason})
	o.emitEvent(ctx, domain.EventRunFailed, runID, run.TraceID,
		fmt.Sprintf("evt-%s-failed", run.TraceID[:12]), payload)

	log := observe.Logger(ctx)
	log.Info("run failed", "run_id", runID, "reason", reason)
	if err := o.CleanupRunBranch(ctx, runID); err != nil {
		log.Warn("cleanup_run_branch failed",
			"run_id", runID,
			"branch", run.BranchName,
			"error", err,
		)
	}
	return nil
}

// CancelRun cancels an active or paused run.
//
// The write is a compare-and-swap with re-evaluation (transitionRun,
// INIT-023 TASK-008): a cancel that raced a pause re-evaluates from paused
// and still cancels; one that raced a merge start re-evaluates from
// committing, where run.cancel is invalid, and returns the state machine's
// ErrConflict (409) instead of cancelling a run whose merge is landing
// (owner question 2). Executions are closed, the event emitted and the
// branch cleaned up only after an applied write.
func (o *Orchestrator) CancelRun(ctx context.Context, runID string) error {
	run, _, _, err := o.transitionRun(ctx, runID, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		return err
	}
	// TerminateStepsOnCancel (INIT-023 TASK-007): engine-state-machine.md
	// §2.2 "terminate in-progress steps" on run.cancel — every live
	// execution moves to skipped/run_terminated.
	o.terminateLiveSteps(ctx, runID, "run cancelled")

	o.emitEvent(ctx, domain.EventRunCancelled, runID, run.TraceID,
		fmt.Sprintf("evt-%s-cancelled", run.TraceID[:12]), nil)

	log := observe.Logger(ctx)
	log.Info("run cancelled", "run_id", runID)
	if err := o.CleanupRunBranch(ctx, runID); err != nil {
		log.Warn("cleanup_run_branch failed",
			"run_id", runID,
			"branch", run.BranchName,
			"error", err,
		)
	}
	return nil
}

// PauseRun transitions an active run to paused.
// The write is a compare-and-swap with re-evaluation (transitionRun).
func (o *Orchestrator) PauseRun(ctx context.Context, runID, reason string) error {
	run, _, _, err := o.transitionRun(ctx, runID, workflow.TransitionRequest{
		Trigger:     workflow.TriggerStepBlocked,
		PauseReason: reason,
	})
	if err != nil {
		return err
	}

	o.emitEvent(ctx, domain.EventRunPaused, runID, run.TraceID,
		fmt.Sprintf("evt-%s-paused-%d", run.TraceID[:12], time.Now().UnixMilli()), nil)

	observe.Logger(ctx).Info("run paused", "run_id", runID, "reason", reason)
	return nil
}

// ResumeRun transitions a paused run back to active.
// The write is a compare-and-swap with re-evaluation (transitionRun).
func (o *Orchestrator) ResumeRun(ctx context.Context, runID string) error {
	run, _, _, err := o.transitionRun(ctx, runID, workflow.TransitionRequest{
		Trigger: workflow.TriggerResume,
	})
	if err != nil {
		return err
	}

	o.emitEvent(ctx, domain.EventRunResumed, runID, run.TraceID,
		fmt.Sprintf("evt-%s-resumed-%d", run.TraceID[:12], time.Now().UnixMilli()), nil)

	observe.Logger(ctx).Info("run resumed", "run_id", runID)
	return nil
}

// findStepDef looks up a step definition by ID within a workflow.
func findStepDef(wf *domain.WorkflowDefinition, stepID string) *domain.StepDefinition {
	for i := range wf.Steps {
		if wf.Steps[i].ID == stepID {
			return &wf.Steps[i]
		}
	}
	return nil
}
