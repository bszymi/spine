package divergence_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
)

// ── Fakes ──

type fakeStore struct {
	store.Store
	divContexts map[string]*domain.DivergenceContext
	branches    map[string]*domain.Branch
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		divContexts: make(map[string]*domain.DivergenceContext),
		branches:    make(map[string]*domain.Branch),
	}
}

func (f *fakeStore) CreateDivergenceContext(_ context.Context, div *domain.DivergenceContext) error {
	f.divContexts[div.DivergenceID] = div
	return nil
}

func (f *fakeStore) UpdateDivergenceContext(_ context.Context, div *domain.DivergenceContext) error {
	f.divContexts[div.DivergenceID] = div
	return nil
}

func (f *fakeStore) GetDivergenceContext(_ context.Context, id string) (*domain.DivergenceContext, error) {
	d, ok := f.divContexts[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return d, nil
}

func (f *fakeStore) CreateBranch(_ context.Context, b *domain.Branch) error {
	f.branches[b.BranchID] = b
	return nil
}

func (f *fakeStore) UpdateBranch(_ context.Context, b *domain.Branch) error {
	f.branches[b.BranchID] = b
	return nil
}

func (f *fakeStore) ListBranchesByDivergence(_ context.Context, divID string) ([]domain.Branch, error) {
	var result []domain.Branch
	for _, b := range f.branches {
		if b.DivergenceID == divID {
			result = append(result, *b)
		}
	}
	return result, nil
}

type fakeGitClient struct {
	git.GitClient // embed to satisfy interface
	branches      []string
}

func (f *fakeGitClient) CreateBranch(_ context.Context, name, _ string) error {
	f.branches = append(f.branches, name)
	return nil
}

type fakeEventRouter struct {
	events []domain.Event
}

func (f *fakeEventRouter) Emit(_ context.Context, evt domain.Event) error {
	f.events = append(f.events, evt)
	return nil
}

func (f *fakeEventRouter) Subscribe(_ context.Context, _ domain.EventType, _ event.EventHandler) error {
	return nil
}

// ── Tests ──

func TestStartStructuredDivergence(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeStructured,
		Branches: []domain.BranchDefinition{
			{ID: "a", Name: "Branch A", StartStep: "step-a"},
			{ID: "b", Name: "Branch B", StartStep: "step-b"},
		},
	}

	divCtx, err := svc.StartDivergence(context.Background(), run, divDef, "conv-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if divCtx.Status != domain.DivergenceStatusActive {
		t.Errorf("expected active, got %s", divCtx.Status)
	}
	if divCtx.DivergenceWindow != "closed" {
		t.Errorf("structured should have closed window, got %s", divCtx.DivergenceWindow)
	}
	if divCtx.TriggeredAt == nil {
		t.Error("expected triggered_at to be set")
	}

	// Should have 2 branches
	if len(fs.branches) != 2 {
		t.Errorf("expected 2 branches, got %d", len(fs.branches))
	}

	// Should have 2 git branches
	if len(gitClient.branches) != 2 {
		t.Errorf("expected 2 git branches, got %d", len(gitClient.branches))
	}

	// Should emit divergence_started event
	if len(events.events) != 1 || events.events[0].Type != domain.EventDivergenceStarted {
		t.Error("expected divergence_started event")
	}
}

func TestStartExploratoryDivergence(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeExploratory,
	}

	divCtx, err := svc.StartDivergence(context.Background(), run, divDef, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if divCtx.DivergenceWindow != "open" {
		t.Errorf("exploratory should have open window, got %s", divCtx.DivergenceWindow)
	}
	// No branches yet
	if len(fs.branches) != 0 {
		t.Errorf("expected 0 branches for exploratory, got %d", len(fs.branches))
	}
}

func TestCreateExploratoryBranch(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeExploratory,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")

	branch, err := svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-1", "step-1")
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if branch.Status != domain.BranchStatusPending {
		t.Errorf("expected pending, got %s", branch.Status)
	}
}

func TestCreateBranchOnStructuredFails(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeStructured,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")

	_, err := svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-1", "step-1")
	if err == nil {
		t.Error("expected error creating branch on structured divergence")
	}
}

func TestCreateExploratoryBranchWindowClosed(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeExploratory,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")
	divCtx.DivergenceWindow = "closed"
	fs.divContexts[divCtx.DivergenceID] = divCtx

	_, err := svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-1", "step-1")
	if err == nil {
		t.Error("expected error creating branch with closed window")
	}
}

func TestCloseWindow(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:   "div-1",
		Mode: domain.DivergenceModeExploratory,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")

	if err := svc.CloseWindow(context.Background(), divCtx); err != nil {
		t.Fatalf("close window: %v", err)
	}
	if divCtx.DivergenceWindow != "closed" {
		t.Errorf("expected closed, got %s", divCtx.DivergenceWindow)
	}
}

func TestCreateExploratoryBranch_AutoClosesOnMax(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:          "div-1",
		Mode:        domain.DivergenceModeExploratory,
		MaxBranches: 2,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")

	// Create first branch — window should remain open.
	_, err := svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-1", "step-1")
	if err != nil {
		t.Fatalf("create branch 1: %v", err)
	}
	if divCtx.DivergenceWindow != "open" {
		t.Errorf("expected window open after 1 branch, got %s", divCtx.DivergenceWindow)
	}

	// Create second branch — should auto-close window (max=2 reached).
	_, err = svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-2", "step-2")
	if err != nil {
		t.Fatalf("create branch 2: %v", err)
	}
	if divCtx.DivergenceWindow != "closed" {
		t.Errorf("expected window closed after max reached, got %s", divCtx.DivergenceWindow)
	}

	// Third branch should be rejected.
	_, err = svc.CreateExploratoryBranch(context.Background(), divCtx, "variant-3", "step-3")
	if err == nil {
		t.Error("expected error creating branch after window closed")
	}
}

func TestCreateExploratoryBranch_PersistsMinMax(t *testing.T) {
	fs := newFakeStore()
	gitClient := &fakeGitClient{}
	events := &fakeEventRouter{}
	svc := divergence.NewService(fs, gitClient, events)

	run := &domain.Run{RunID: "run-1"}
	divDef := domain.DivergenceDefinition{
		ID:          "div-1",
		Mode:        domain.DivergenceModeExploratory,
		MinBranches: 2,
		MaxBranches: 5,
	}

	divCtx, _ := svc.StartDivergence(context.Background(), run, divDef, "")

	if divCtx.MinBranches != 2 {
		t.Errorf("expected min_branches 2, got %d", divCtx.MinBranches)
	}
	if divCtx.MaxBranches != 5 {
		t.Errorf("expected max_branches 5, got %d", divCtx.MaxBranches)
	}
}
