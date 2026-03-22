package actor_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ── Fake Store ──

type fakeStore struct {
	store.Store
	actors map[string]*domain.Actor
}

func newFakeStore() *fakeStore {
	return &fakeStore{actors: make(map[string]*domain.Actor)}
}

func (f *fakeStore) CreateActor(_ context.Context, a *domain.Actor) error {
	if _, exists := f.actors[a.ActorID]; exists {
		return domain.NewError(domain.ErrAlreadyExists, "actor exists")
	}
	f.actors[a.ActorID] = a
	return nil
}

func (f *fakeStore) GetActor(_ context.Context, id string) (*domain.Actor, error) {
	a, ok := f.actors[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return a, nil
}

func (f *fakeStore) UpdateActor(_ context.Context, a *domain.Actor) error {
	if _, ok := f.actors[a.ActorID]; !ok {
		return domain.NewError(domain.ErrNotFound, "not found")
	}
	f.actors[a.ActorID] = a
	return nil
}

func (f *fakeStore) ListActors(_ context.Context) ([]domain.Actor, error) {
	var result []domain.Actor
	for _, a := range f.actors {
		result = append(result, *a)
	}
	return result, nil
}

func (f *fakeStore) ListActorsByStatus(_ context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	var result []domain.Actor
	for _, a := range f.actors {
		if a.Status == status {
			result = append(result, *a)
		}
	}
	return result, nil
}

// ── Service Tests ──

func TestRegisterAndGet(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	a := &domain.Actor{ActorID: "a1", Type: domain.ActorTypeHuman, Name: "Alice", Role: domain.RoleContributor}
	if err := svc.Register(context.Background(), a); err != nil {
		t.Fatalf("register: %v", err)
	}
	if a.Status != domain.ActorStatusActive {
		t.Errorf("expected active status, got %s", a.Status)
	}

	got, err := svc.Get(context.Background(), "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("expected Alice, got %s", got.Name)
	}
}

func TestRegisterMissingID(t *testing.T) {
	svc := actor.NewService(newFakeStore())
	err := svc.Register(context.Background(), &domain.Actor{})
	if err == nil {
		t.Error("expected error for missing actor_id")
	}
}

func TestSuspendAndReactivate(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)
	svc.Register(context.Background(), &domain.Actor{ActorID: "a1", Type: domain.ActorTypeHuman, Role: domain.RoleContributor})

	if err := svc.Suspend(context.Background(), "a1"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	a, _ := svc.Get(context.Background(), "a1")
	if a.Status != domain.ActorStatusSuspended {
		t.Errorf("expected suspended, got %s", a.Status)
	}

	if err := svc.Reactivate(context.Background(), "a1"); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	a, _ = svc.Get(context.Background(), "a1")
	if a.Status != domain.ActorStatusActive {
		t.Errorf("expected active, got %s", a.Status)
	}
}

func TestDeactivateCannotReactivate(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)
	svc.Register(context.Background(), &domain.Actor{ActorID: "a1", Type: domain.ActorTypeHuman, Role: domain.RoleContributor})

	if err := svc.Deactivate(context.Background(), "a1"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	err := svc.Reactivate(context.Background(), "a1")
	if err == nil {
		t.Error("expected error reactivating deactivated actor")
	}
}

// ── Selection Tests ──

func setupActors(t *testing.T) (*actor.Service, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	svc := actor.NewService(fs)

	actors := []*domain.Actor{
		{ActorID: "human-1", Type: domain.ActorTypeHuman, Name: "Alice", Role: domain.RoleContributor, Capabilities: []string{"code_review"}, Status: domain.ActorStatusActive},
		{ActorID: "human-2", Type: domain.ActorTypeHuman, Name: "Bob", Role: domain.RoleReviewer, Capabilities: []string{"code_review", "architecture_review"}, Status: domain.ActorStatusActive},
		{ActorID: "ai-1", Type: domain.ActorTypeAIAgent, Name: "Claude", Role: domain.RoleContributor, Capabilities: []string{"code_generation"}, Status: domain.ActorStatusActive},
		{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "CI Bot", Role: domain.RoleReader, Capabilities: []string{"testing"}, Status: domain.ActorStatusActive},
		{ActorID: "suspended-1", Type: domain.ActorTypeHuman, Name: "Suspended", Role: domain.RoleContributor, Status: domain.ActorStatusSuspended},
		{ActorID: "deactivated-1", Type: domain.ActorTypeHuman, Name: "Gone", Role: domain.RoleAdmin, Status: domain.ActorStatusDeactivated},
	}
	for _, a := range actors {
		fs.actors[a.ActorID] = a
	}
	return svc, fs
}

func TestSelectAnyEligible(t *testing.T) {
	svc, _ := setupActors(t)

	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy: actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.Status != domain.ActorStatusActive {
		t.Errorf("expected active actor, got %s", a.Status)
	}
}

func TestSelectFilterByType(t *testing.T) {
	svc, _ := setupActors(t)

	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		EligibleActorTypes: []string{"ai_agent"},
		Strategy:           actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.ActorID != "ai-1" {
		t.Errorf("expected ai-1, got %s", a.ActorID)
	}
}

func TestSelectFilterByCapability(t *testing.T) {
	svc, _ := setupActors(t)

	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		RequiredCapabilities: []string{"architecture_review"},
		Strategy:             actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.ActorID != "human-2" {
		t.Errorf("expected human-2 (Bob), got %s", a.ActorID)
	}
}

func TestSelectFilterByRole(t *testing.T) {
	svc, _ := setupActors(t)

	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		MinRole:  domain.RoleReviewer,
		Strategy: actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !a.Role.HasAtLeast(domain.RoleReviewer) {
		t.Errorf("expected reviewer or above, got %s", a.Role)
	}
}

func TestSelectNoEligible(t *testing.T) {
	svc, _ := setupActors(t)

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		RequiredCapabilities: []string{"nonexistent_capability"},
		Strategy:             actor.StrategyAnyEligible,
	})
	if err == nil {
		t.Error("expected error when no eligible actor")
	}
}

func TestSelectExcluesSuspendedAndDeactivated(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	// Only suspended and deactivated actors
	fs.actors["s1"] = &domain.Actor{ActorID: "s1", Status: domain.ActorStatusSuspended, Role: domain.RoleAdmin}
	fs.actors["d1"] = &domain.Actor{ActorID: "d1", Status: domain.ActorStatusDeactivated, Role: domain.RoleAdmin}

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy: actor.StrategyAnyEligible,
	})
	if err == nil {
		t.Error("expected no eligible actor")
	}
}

func TestSelectExplicit(t *testing.T) {
	svc, _ := setupActors(t)

	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy:        actor.StrategyExplicit,
		ExplicitActorID: "human-2",
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.ActorID != "human-2" {
		t.Errorf("expected human-2, got %s", a.ActorID)
	}
}

func TestSelectExplicitInactive(t *testing.T) {
	svc, _ := setupActors(t)

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy:        actor.StrategyExplicit,
		ExplicitActorID: "suspended-1",
	})
	if err == nil {
		t.Error("expected error for suspended explicit actor")
	}
}

func TestSelectExplicitMissingID(t *testing.T) {
	svc, _ := setupActors(t)

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy: actor.StrategyExplicit,
	})
	if err == nil {
		t.Error("expected error for missing explicit actor_id")
	}
}

func TestSelectExplicitNotEligible(t *testing.T) {
	svc, _ := setupActors(t)

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy:             actor.StrategyExplicit,
		ExplicitActorID:      "human-1",
		RequiredCapabilities: []string{"nonexistent"},
	})
	if err == nil {
		t.Error("expected error for ineligible explicit actor")
	}
}

func TestSelectRoundRobin(t *testing.T) {
	svc, _ := setupActors(t)

	// Select humans only (2 active humans) multiple times
	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
			EligibleActorTypes: []string{"human"},
			Strategy:           actor.StrategyRoundRobin,
		})
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		seen[a.ActorID]++
	}

	// Both humans should be selected roughly equally (3 each for 6 iterations)
	if seen["human-1"] == 0 || seen["human-2"] == 0 {
		t.Errorf("round-robin should distribute: %v", seen)
	}
}

func TestSelectCombinedFilters(t *testing.T) {
	svc, _ := setupActors(t)

	// Human + code_review capability + contributor role
	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		EligibleActorTypes:   []string{"human"},
		RequiredCapabilities: []string{"code_review"},
		MinRole:              domain.RoleContributor,
		Strategy:             actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.Type != domain.ActorTypeHuman {
		t.Errorf("expected human, got %s", a.Type)
	}
}
