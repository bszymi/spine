package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/workflow"
)

// activeRepo returns a repoLookup that succeeds with an active code repo.
func activeRepo(id string) repoLookup {
	return repoLookup{
		repo: &repository.Repository{
			ID:     id,
			Kind:   repository.KindCode,
			Status: "active",
		},
	}
}

// inactiveRepo returns a repoLookup that fails with ErrRepositoryInactive.
func inactiveRepo() repoLookup {
	return repoLookup{err: repository.ErrRepositoryInactive}
}

// missingRepo returns a repoLookup that fails with ErrRepositoryNotFound.
func missingRepo() repoLookup {
	return repoLookup{err: repository.ErrRepositoryNotFound}
}

// TestResolveStepRepositoryID_AnchorsAC_i_ii_iii pins ADR-015's two-rule
// resolution: explicit step.repository wins; absence falls back to spine;
// explicit "spine" is equivalent to omission.
func TestResolveStepRepositoryID_AnchorsAC_i_ii_iii(t *testing.T) {
	tests := []struct {
		name string
		step *domain.StepDefinition
		want string
	}{
		// AC (i)
		{"explicit code repo resolves to that repo",
			&domain.StepDefinition{ID: "build", Repository: "payments-service"},
			"payments-service"},
		// AC (ii)
		{"omitted repository resolves to spine",
			&domain.StepDefinition{ID: "review"},
			domain.PrimaryRepositoryID},
		// AC (iii)
		{"explicit spine equivalent to omission",
			&domain.StepDefinition{ID: "publish", Repository: domain.PrimaryRepositoryID},
			domain.PrimaryRepositoryID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveStepRepositoryID(tt.step); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateStepRouting_AnchorsAC_iv_unknownRepo pins AC (iv): a step
// targeting an unregistered repo at run start fails fast.
func TestValidateStepRouting_AnchorsAC_iv_unknownRepo(t *testing.T) {
	o := &Orchestrator{
		repositories: newRepoResolver(map[string]repoLookup{
			"payments-service": missingRepo(),
		}),
	}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{{
			ID:         "build",
			Repository: "payments-service",
		}},
	}
	art := &domain.Artifact{Repositories: []string{"payments-service"}}

	err := o.validateStepRouting(context.Background(), wf, art)
	assertStepRoutingFailure(t, err, "build", "payments-service", stepRoutingFailureUnregistered)
}

// TestValidateStepRouting_AnchorsAC_v_inactiveRepo pins AC (v): a step
// targeting an inactive repo at run start fails fast.
func TestValidateStepRouting_AnchorsAC_v_inactiveRepo(t *testing.T) {
	o := &Orchestrator{
		repositories: newRepoResolver(map[string]repoLookup{
			"shared-libs": inactiveRepo(),
		}),
	}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{{
			ID:         "publish",
			Repository: "shared-libs",
		}},
	}
	art := &domain.Artifact{Repositories: []string{"shared-libs"}}

	err := o.validateStepRouting(context.Background(), wf, art)
	assertStepRoutingFailure(t, err, "publish", "shared-libs", stepRoutingFailureInactive)
}

// TestValidateStepRouting_AnchorsAC_vi_notOptedIn pins AC (vi): a step
// targeting a repo the task hasn't opted into via task.repositories
// (and which isn't `spine`) is rejected — even if the repo is otherwise
// registered and active. Prevents a workflow from leaking step work
// into a repo the task author didn't sanction.
func TestValidateStepRouting_AnchorsAC_vi_notOptedIn(t *testing.T) {
	o := &Orchestrator{
		repositories: newRepoResolver(map[string]repoLookup{
			"api-gateway": activeRepo("api-gateway"),
		}),
	}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{{
			ID:         "build",
			Repository: "api-gateway",
		}},
	}
	// Task only opted into payments-service.
	art := &domain.Artifact{Repositories: []string{"payments-service"}}

	err := o.validateStepRouting(context.Background(), wf, art)
	assertStepRoutingFailure(t, err, "build", "api-gateway", stepRoutingFailureNotOptedIn)
}

// TestValidateStepRouting_AnchorsAC_vii_badFormat pins AC (vii): a
// repository value that doesn't match the catalog ID format is
// rejected at workflow load time before any state mutation.
func TestValidateStepRouting_AnchorsAC_vii_badFormat(t *testing.T) {
	o := &Orchestrator{}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{{
			ID:         "build",
			Repository: "Payments-Service", // capital letters disallowed
		}},
	}
	art := &domain.Artifact{Repositories: []string{}}

	err := o.validateStepRouting(context.Background(), wf, art)
	assertStepRoutingFailure(t, err, "build", "Payments-Service", stepRoutingFailureBadFormat)

	// Length cap.
	long := make([]byte, workflow.StepRepositoryIDMaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	wf.Steps[0].Repository = string(long)
	err = o.validateStepRouting(context.Background(), wf, art)
	if err == nil {
		t.Fatalf("expected length-cap rejection, got nil")
	}
}

// TestValidateStepRouting_AnchorsAC_viii_singleRepoOnMultiCodeRepoTask
// pins AC (viii): a workflow with NO `repository:` declarations runs
// against a multi-code-repo task. Per ADR-015, every step resolves to
// `spine` regardless of task.repositories cardinality — no implicit
// single-code-repo default, no fan-out.
func TestValidateStepRouting_AnchorsAC_viii_singleRepoOnMultiCodeRepoTask(t *testing.T) {
	o := &Orchestrator{
		repositories: newRepoResolver(map[string]repoLookup{
			"payments-service": activeRepo("payments-service"),
			"api-gateway":      activeRepo("api-gateway"),
		}),
	}
	wf := &domain.WorkflowDefinition{
		// Three steps, none declare repository — they all resolve to spine.
		Steps: []domain.StepDefinition{
			{ID: "draft", Name: "Draft"},
			{ID: "execute", Name: "Execute"},
			{ID: "review", Name: "Review"},
		},
	}
	art := &domain.Artifact{Repositories: []string{"payments-service", "api-gateway"}}

	if err := o.validateStepRouting(context.Background(), wf, art); err != nil {
		t.Fatalf("expected validation to pass for default-spine workflow on multi-code-repo task, got %v", err)
	}

	// Confirm every step resolves to spine, not to a code repo.
	for i := range wf.Steps {
		if got := resolveStepRepositoryID(&wf.Steps[i]); got != domain.PrimaryRepositoryID {
			t.Errorf("step %q: expected spine resolution, got %q", wf.Steps[i].ID, got)
		}
	}
}

// TestValidateStepRouting_SpineExceptionPasses pins the ADR-015 spine
// exception: a step with no explicit repository (or with explicit
// `repository: spine`) passes validation even when the task has no
// `repositories` opt-in list and no resolver is wired (single-repo
// workspace path).
func TestValidateStepRouting_SpineExceptionPasses(t *testing.T) {
	o := &Orchestrator{} // No resolver wired — single-repo workspace.
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "draft"},                                     // implicit spine
			{ID: "review", Repository: domain.PrimaryRepositoryID}, // explicit spine
		},
	}
	art := &domain.Artifact{} // No task repositories.

	if err := o.validateStepRouting(context.Background(), wf, art); err != nil {
		t.Fatalf("expected spine-only validation to pass, got %v", err)
	}
}

// TestValidateStepRouting_CodeRepoOptInPasses confirms the happy-path
// for a workflow that explicitly targets a code repo the task opted
// into and which the registry says is active.
func TestValidateStepRouting_CodeRepoOptInPasses(t *testing.T) {
	o := &Orchestrator{
		repositories: newRepoResolver(map[string]repoLookup{
			"payments-service": activeRepo("payments-service"),
		}),
	}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "draft"}, // spine — governance step
			{ID: "build", Repository: "payments-service"},
			{ID: "review"}, // spine — review step
		},
	}
	art := &domain.Artifact{Repositories: []string{"payments-service"}}

	if err := o.validateStepRouting(context.Background(), wf, art); err != nil {
		t.Fatalf("expected validation to pass, got %v", err)
	}
}

// assertStepRoutingFailure verifies the error is a SpineError with
// ErrPrecondition code and a StepRoutingFailure detail matching the
// expected step / repo / category triple.
func assertStepRoutingFailure(t *testing.T, err error, wantStep, wantRepo, wantCategory string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected step routing failure for step=%q repo=%q category=%q, got nil", wantStep, wantRepo, wantCategory)
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected code=%s, got %s", domain.ErrPrecondition, spineErr.Code)
	}
	failure, ok := spineErr.Detail.(StepRoutingFailure)
	if !ok {
		t.Fatalf("expected StepRoutingFailure detail, got %T: %v", spineErr.Detail, spineErr.Detail)
	}
	if failure.StepID != wantStep {
		t.Errorf("step_id: want %q, got %q", wantStep, failure.StepID)
	}
	if failure.RepositoryID != wantRepo {
		t.Errorf("repository_id: want %q, got %q", wantRepo, failure.RepositoryID)
	}
	if failure.Category != wantCategory {
		t.Errorf("category: want %q, got %q", wantCategory, failure.Category)
	}
}
