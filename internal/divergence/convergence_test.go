package divergence_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	gitpkg "github.com/bszymi/spine/internal/git"
)

// ── Fake Git Client with Merge ──

type fakeGitClientWithMerge struct {
	gitpkg.GitClient
	branches []string
	merges   []string
}

func (f *fakeGitClientWithMerge) CreateBranch(_ context.Context, name, _ string) error {
	f.branches = append(f.branches, name)
	return nil
}

func (f *fakeGitClientWithMerge) Merge(_ context.Context, opts gitpkg.MergeOpts) (gitpkg.MergeResult, error) {
	f.merges = append(f.merges, opts.Source)
	return gitpkg.MergeResult{SHA: "abc123"}, nil
}

// ── Strategy Tests ──

func TestSelectOneStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		DivergenceID: "div-1",
		Strategy:     domain.ConvergenceSelectOne,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"art1.md"}},
			{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"art2.md"}},
		},
	}

	output, err := svc.EvaluateConvergence(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if output.Result.SelectedBranch == "" {
		t.Error("expected selected branch")
	}
	if output.Result.StrategyApplied != domain.ConvergenceSelectOne {
		t.Errorf("expected select_one, got %s", output.Result.StrategyApplied)
	}
}

func TestSelectOneNoCompletedBranches(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceSelectOne,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusFailed},
		},
	}

	_, err := svc.EvaluateConvergence(context.Background(), input)
	if err == nil {
		t.Error("expected error for no completed branches")
	}
}

func TestSelectSubsetStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceSelectSubset,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
			{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a2.md"}},
			{BranchID: "b3", Status: domain.BranchStatusFailed},
		},
	}

	output, err := svc.EvaluateConvergence(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(output.Result.SelectedBranches) != 2 {
		t.Errorf("expected 2 selected branches, got %d", len(output.Result.SelectedBranches))
	}
	if len(output.SelectedArtifacts) != 2 {
		t.Errorf("expected 2 selected artifacts, got %d", len(output.SelectedArtifacts))
	}
}

func TestMergeStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceMerge,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
			{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a2.md"}},
		},
	}

	output, err := svc.EvaluateConvergence(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(output.Result.SelectedBranches) != 2 {
		t.Errorf("expected 2 merged branches, got %d", len(output.Result.SelectedBranches))
	}
}

func TestMergeStrategyTooFewBranches(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceMerge,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted},
		},
	}

	_, err := svc.EvaluateConvergence(context.Background(), input)
	if err == nil {
		t.Error("expected error for merge with < 2 branches")
	}
}

func TestRequireAllStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceRequireAll,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted},
			{BranchID: "b2", Status: domain.BranchStatusCompleted},
		},
	}

	output, err := svc.EvaluateConvergence(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(output.Result.SelectedBranches) != 2 {
		t.Errorf("expected 2 branches, got %d", len(output.Result.SelectedBranches))
	}
}

func TestRequireAllWithFailure(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceRequireAll,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted},
			{BranchID: "b2", Status: domain.BranchStatusFailed},
		},
	}

	_, err := svc.EvaluateConvergence(context.Background(), input)
	if err == nil {
		t.Error("expected error for require_all with failed branch")
	}
}

func TestExperimentStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: domain.ConvergenceExperiment,
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted},
			{BranchID: "b2", Status: domain.BranchStatusCompleted},
		},
	}

	output, err := svc.EvaluateConvergence(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(output.Result.SelectedBranches) != 2 {
		t.Errorf("expected 2 experiment variants, got %d", len(output.Result.SelectedBranches))
	}
}

func TestUnknownStrategy(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	input := divergence.ConvergenceInput{
		Strategy: "unknown",
		Branches: []domain.Branch{
			{BranchID: "b1", Status: domain.BranchStatusCompleted},
		},
	}

	_, err := svc.EvaluateConvergence(context.Background(), input)
	if err == nil {
		t.Error("expected error for unknown strategy")
	}
}

// ── Entry Policy Tests ──

func TestCheckEntryPolicyAllTerminal(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1",
		RunID:        "run-1",
		Status:       domain.DivergenceStatusActive,
	}
	fs.divContexts[divCtx.DivergenceID] = divCtx

	// Add 2 completed branches
	fs.branches["b1"] = &domain.Branch{BranchID: "b1", DivergenceID: "div-1", Status: domain.BranchStatusCompleted}
	fs.branches["b2"] = &domain.Branch{BranchID: "b2", DivergenceID: "div-1", Status: domain.BranchStatusCompleted}

	ready, err := svc.CheckEntryPolicy(context.Background(), divCtx, domain.ConvergenceDefinition{
		EntryPolicy: domain.EntryPolicyAllTerminal,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !ready {
		t.Error("expected entry policy satisfied")
	}
}

func TestCheckEntryPolicyNotReady(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1",
		RunID:        "run-1",
		Status:       domain.DivergenceStatusActive,
	}
	fs.divContexts[divCtx.DivergenceID] = divCtx

	fs.branches["b1"] = &domain.Branch{BranchID: "b1", DivergenceID: "div-1", Status: domain.BranchStatusCompleted}
	fs.branches["b2"] = &domain.Branch{BranchID: "b2", DivergenceID: "div-1", Status: domain.BranchStatusInProgress}

	ready, err := svc.CheckEntryPolicy(context.Background(), divCtx, domain.ConvergenceDefinition{
		EntryPolicy: domain.EntryPolicyAllTerminal,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if ready {
		t.Error("expected entry policy NOT satisfied")
	}
}

func TestCheckEntryPolicyMinCompleted(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1",
		RunID:        "run-1",
		Status:       domain.DivergenceStatusActive,
	}
	fs.divContexts[divCtx.DivergenceID] = divCtx

	fs.branches["b1"] = &domain.Branch{BranchID: "b1", DivergenceID: "div-1", Status: domain.BranchStatusCompleted}
	fs.branches["b2"] = &domain.Branch{BranchID: "b2", DivergenceID: "div-1", Status: domain.BranchStatusInProgress}

	ready, err := svc.CheckEntryPolicy(context.Background(), divCtx, domain.ConvergenceDefinition{
		EntryPolicy: domain.EntryPolicyMinCompleted,
		MinBranches: 1,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !ready {
		t.Error("expected min_completed satisfied with 1 completed branch")
	}
}

// ── CommitConvergence Tests ──

func TestCommitConvergence(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1",
		RunID:        "run-1",
		Status:       domain.DivergenceStatusConverging,
	}
	fs.divContexts[divCtx.DivergenceID] = divCtx

	output := &divergence.ConvergenceOutput{
		Result: domain.ConvergenceResult{
			StrategyApplied: domain.ConvergenceSelectOne,
			SelectedBranch:  "div-1-a",
		},
		SelectedArtifacts: []string{"art1.md"},
	}

	if err := svc.CommitConvergence(context.Background(), divCtx, output); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if divCtx.Status != domain.DivergenceStatusResolved {
		t.Errorf("expected resolved, got %s", divCtx.Status)
	}
	if divCtx.ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}

	// Should have attempted merge
	if len(gitClient.merges) != 1 {
		t.Errorf("expected 1 merge, got %d", len(gitClient.merges))
	}

	// Should emit convergence_completed event
	found := false
	for _, evt := range events.events {
		if evt.Type == domain.EventConvergenceCompleted {
			found = true
		}
	}
	if !found {
		t.Error("expected convergence_completed event")
	}
}

func TestCommitConvergenceMultipleBranches(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClientWithMerge{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1",
		RunID:        "run-1",
		Status:       domain.DivergenceStatusConverging,
	}
	fs.divContexts[divCtx.DivergenceID] = divCtx

	output := &divergence.ConvergenceOutput{
		Result: domain.ConvergenceResult{
			StrategyApplied:  domain.ConvergenceMerge,
			SelectedBranches: []string{"div-1-a", "div-1-b"},
		},
	}

	if err := svc.CommitConvergence(context.Background(), divCtx, output); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if len(gitClient.merges) != 2 {
		t.Errorf("expected 2 merges, got %d", len(gitClient.merges))
	}
}
