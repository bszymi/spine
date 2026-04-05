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
	actors      map[string]*domain.Actor
	actorSkills map[string]map[string]bool // actorID -> set of skillIDs
	skills      map[string]*domain.Skill
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		actors:      make(map[string]*domain.Actor),
		actorSkills: make(map[string]map[string]bool),
		skills:      make(map[string]*domain.Skill),
	}
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

func (f *fakeStore) AddSkillToActor(_ context.Context, actorID, skillID string) error {
	if _, ok := f.actorSkills[actorID]; !ok {
		f.actorSkills[actorID] = make(map[string]bool)
	}
	f.actorSkills[actorID][skillID] = true
	return nil
}

func (f *fakeStore) RemoveSkillFromActor(_ context.Context, actorID, skillID string) error {
	if m, ok := f.actorSkills[actorID]; ok {
		delete(m, skillID)
	}
	return nil
}

func (f *fakeStore) ListActorSkills(_ context.Context, actorID string) ([]domain.Skill, error) {
	skillIDs, ok := f.actorSkills[actorID]
	if !ok {
		return nil, nil
	}
	var result []domain.Skill
	for sid := range skillIDs {
		if sk, ok := f.skills[sid]; ok {
			result = append(result, *sk)
		}
	}
	return result, nil
}

func (f *fakeStore) ListActorsBySkills(_ context.Context, skillNames []string) ([]domain.Actor, error) {
	if len(skillNames) == 0 {
		var result []domain.Actor
		for _, a := range f.actors {
			if a.Status == domain.ActorStatusActive {
				result = append(result, *a)
			}
		}
		return result, nil
	}

	// AND matching: actor must have ALL requested skill names
	var result []domain.Actor
	for actorID, a := range f.actors {
		if a.Status != domain.ActorStatusActive {
			continue
		}
		assignedSkillIDs := f.actorSkills[actorID]
		// Build set of skill names for this actor
		actorSkillNames := make(map[string]bool)
		for sid := range assignedSkillIDs {
			if sk, ok := f.skills[sid]; ok {
				actorSkillNames[sk.Name] = true
			}
		}
		allMatch := true
		for _, name := range skillNames {
			if !actorSkillNames[name] {
				allMatch = false
				break
			}
		}
		if allMatch {
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

// ── Skill Assignment Tests ──

func TestAddAndListSkills(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)
	svc.Register(context.Background(), &domain.Actor{ActorID: "a1", Type: domain.ActorTypeHuman, Role: domain.RoleContributor})

	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review", Status: domain.SkillStatusActive}
	fs.skills["s2"] = &domain.Skill{SkillID: "s2", Name: "testing", Status: domain.SkillStatusActive}

	if err := svc.AddSkill(context.Background(), "a1", "s1"); err != nil {
		t.Fatalf("add skill: %v", err)
	}
	if err := svc.AddSkill(context.Background(), "a1", "s2"); err != nil {
		t.Fatalf("add skill: %v", err)
	}

	skills, err := svc.ListSkills(context.Background(), "a1")
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(skills))
	}
}

func TestRemoveSkill(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)
	svc.Register(context.Background(), &domain.Actor{ActorID: "a1", Type: domain.ActorTypeHuman, Role: domain.RoleContributor})

	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review", Status: domain.SkillStatusActive}

	svc.AddSkill(context.Background(), "a1", "s1")
	if err := svc.RemoveSkill(context.Background(), "a1", "s1"); err != nil {
		t.Fatalf("remove skill: %v", err)
	}

	skills, err := svc.ListSkills(context.Background(), "a1")
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills after removal, got %d", len(skills))
	}
}

func TestAddSkillMissingParams(t *testing.T) {
	svc := actor.NewService(newFakeStore())
	if err := svc.AddSkill(context.Background(), "", "s1"); err == nil {
		t.Error("expected error for missing actor_id")
	}
	if err := svc.AddSkill(context.Background(), "a1", ""); err == nil {
		t.Error("expected error for missing skill_id")
	}
}

func TestSelectBySkillsOverCapabilities(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	// Actor with legacy capabilities but also with skills assigned
	fs.actors["a1"] = &domain.Actor{
		ActorID: "a1", Type: domain.ActorTypeHuman, Name: "Alice",
		Role: domain.RoleContributor, Capabilities: []string{"old_cap"},
		Status: domain.ActorStatusActive,
	}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "new_skill", Status: domain.SkillStatusActive}
	fs.actorSkills["a1"] = map[string]bool{"s1": true}

	// Should match by skill name, not legacy capabilities
	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		RequiredCapabilities: []string{"new_skill"},
		Strategy:             actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.ActorID != "a1" {
		t.Errorf("expected a1, got %s", a.ActorID)
	}

	// Should NOT match old capability when skills are assigned
	_, err = svc.SelectActor(context.Background(), actor.SelectionRequest{
		RequiredCapabilities: []string{"old_cap"},
		Strategy:             actor.StrategyAnyEligible,
	})
	if err == nil {
		t.Error("expected no match — skills take precedence over legacy capabilities")
	}
}

func TestSelectFallsBackToCapabilities(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	// Actor with legacy capabilities but NO skills assigned
	fs.actors["a1"] = &domain.Actor{
		ActorID: "a1", Type: domain.ActorTypeHuman, Name: "Alice",
		Role: domain.RoleContributor, Capabilities: []string{"legacy_cap"},
		Status: domain.ActorStatusActive,
	}

	// Should match by legacy capabilities
	a, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		RequiredCapabilities: []string{"legacy_cap"},
		Strategy:             actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if a.ActorID != "a1" {
		t.Errorf("expected a1, got %s", a.ActorID)
	}
}

// ── Skill Eligibility Validation Tests ──

func TestValidateSkillEligibility_AllPresent(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusActive}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review"}
	fs.skills["s2"] = &domain.Skill{SkillID: "s2", Name: "testing"}
	fs.actorSkills["a1"] = map[string]bool{"s1": true, "s2": true}

	result, err := svc.ValidateSkillEligibility(context.Background(), "a1", []string{"code_review", "testing"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.Eligible {
		t.Errorf("expected eligible, got missing: %v", result.MissingSkills)
	}
}

func TestValidateSkillEligibility_MissingSkills(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusActive}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review"}
	fs.actorSkills["a1"] = map[string]bool{"s1": true}

	result, err := svc.ValidateSkillEligibility(context.Background(), "a1", []string{"code_review", "deployment"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Eligible {
		t.Error("expected not eligible")
	}
	if len(result.MissingSkills) != 1 || result.MissingSkills[0] != "deployment" {
		t.Errorf("expected missing [deployment], got %v", result.MissingSkills)
	}
}

func TestValidateSkillEligibility_EmptyRequirements(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusActive}

	result, err := svc.ValidateSkillEligibility(context.Background(), "a1", nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.Eligible {
		t.Error("expected eligible with no requirements")
	}
}

func TestValidateSkillEligibility_FallsBackToCapabilities(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{
		ActorID: "a1", Status: domain.ActorStatusActive,
		Capabilities: []string{"legacy_cap"},
	}

	result, err := svc.ValidateSkillEligibility(context.Background(), "a1", []string{"legacy_cap"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.Eligible {
		t.Error("expected eligible via legacy capabilities fallback")
	}
}

func TestSelectExplicitDescriptiveSkillError(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{
		ActorID: "a1", Type: domain.ActorTypeHuman, Name: "Alice",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review"}
	fs.actorSkills["a1"] = map[string]bool{"s1": true}

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		Strategy:             actor.StrategyExplicit,
		ExplicitActorID:      "a1",
		RequiredCapabilities: []string{"code_review", "deployment"},
	})
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	// Error should mention the specific missing skill
	errStr := err.Error()
	if !contains_(errStr, "deployment") {
		t.Errorf("expected error to mention missing skill 'deployment', got: %s", errStr)
	}
}

func contains_(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── Skill Query Interface Tests ──

func TestFindEligibleActors_AllSkills(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusActive}
	fs.actors["a2"] = &domain.Actor{ActorID: "a2", Status: domain.ActorStatusActive}
	fs.actors["a3"] = &domain.Actor{ActorID: "a3", Status: domain.ActorStatusActive}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review"}
	fs.skills["s2"] = &domain.Skill{SkillID: "s2", Name: "testing"}
	// a1 has both skills, a2 has only one, a3 has none
	fs.actorSkills["a1"] = map[string]bool{"s1": true, "s2": true}
	fs.actorSkills["a2"] = map[string]bool{"s1": true}

	actors, err := svc.FindEligibleActors(context.Background(), []string{"code_review", "testing"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(actors) != 1 {
		t.Fatalf("expected 1 eligible actor, got %d", len(actors))
	}
	if actors[0].ActorID != "a1" {
		t.Errorf("expected a1, got %s", actors[0].ActorID)
	}
}

func TestFindEligibleActors_EmptySkills(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusActive}
	fs.actors["a2"] = &domain.Actor{ActorID: "a2", Status: domain.ActorStatusSuspended}

	actors, err := svc.FindEligibleActors(context.Background(), nil)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	// Should return all active actors
	if len(actors) != 1 {
		t.Errorf("expected 1 active actor, got %d", len(actors))
	}
}

func TestFindEligibleActors_ExcludesSuspended(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)

	fs.actors["a1"] = &domain.Actor{ActorID: "a1", Status: domain.ActorStatusSuspended}
	fs.skills["s1"] = &domain.Skill{SkillID: "s1", Name: "code_review"}
	fs.actorSkills["a1"] = map[string]bool{"s1": true}

	actors, err := svc.FindEligibleActors(context.Background(), []string{"code_review"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(actors) != 0 {
		t.Errorf("expected 0 actors (suspended), got %d", len(actors))
	}
}
