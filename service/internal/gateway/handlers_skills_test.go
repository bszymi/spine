package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/adapters/store"
)

// ── Skill-aware Fake Store ──
//
// Composes the per-role no-op stubs from stubstore_test.go and overrides
// only the methods the skill handlers exercise.

type skillStore struct {
	stubRoleStore
	actors              map[string]*domain.Actor
	tokens              map[string]*fakeTokenEntry
	skills              map[string]*domain.Skill
	actorSkills         map[string]map[string]bool // actorID -> skillID -> exists
	workflowProjections []store.WorkflowProjection
}

func newSkillStore() *skillStore {
	return &skillStore{
		actors:      make(map[string]*domain.Actor),
		tokens:      make(map[string]*fakeTokenEntry),
		skills:      make(map[string]*domain.Skill),
		actorSkills: make(map[string]map[string]bool),
	}
}

func (s *skillStore) Ping(_ context.Context) error { return nil }

func (s *skillStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := s.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

func (s *skillStore) GetActorByTokenHash(_ context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	entry, ok := s.tokens[tokenHash]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	return entry.actor, entry.token, nil
}

func (s *skillStore) CreateToken(_ context.Context, record *store.TokenRecord) error {
	actor, ok := s.actors[record.ActorID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	s.tokens[record.TokenHash] = &fakeTokenEntry{
		actor: actor,
		token: &domain.Token{TokenID: record.TokenID, ActorID: record.ActorID, Name: record.Name, ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt},
	}
	return nil
}

func (s *skillStore) CreateSkill(_ context.Context, skill *domain.Skill) error {
	// Mirror prod schema: auth.skills.skill_id is PRIMARY KEY NOT NULL,
	// so an empty skill_id would duplicate-PK on the second insert.
	// Reject it here so the fake fails the same way prod would when a
	// handler forgets to populate the column (regression fence for the
	// INIT-008 / EPIC-003 / TASK-018 onboarding-seed-skills 500s).
	if skill.SkillID == "" {
		return domain.NewError(domain.ErrInvalidParams, "skill_id required")
	}
	if _, exists := s.skills[skill.SkillID]; exists {
		return domain.NewError(domain.ErrConflict, "skill already exists")
	}
	now := time.Now()
	skill.CreatedAt = now
	skill.UpdatedAt = now
	s.skills[skill.SkillID] = skill
	return nil
}

func (s *skillStore) GetSkill(_ context.Context, skillID string) (*domain.Skill, error) {
	sk, ok := s.skills[skillID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "skill not found")
	}
	return sk, nil
}

func (s *skillStore) UpdateSkill(_ context.Context, skill *domain.Skill) error {
	if _, ok := s.skills[skill.SkillID]; !ok {
		return domain.NewError(domain.ErrNotFound, "skill not found")
	}
	skill.UpdatedAt = time.Now()
	s.skills[skill.SkillID] = skill
	return nil
}

func (s *skillStore) ListSkills(_ context.Context) ([]domain.Skill, error) {
	var result []domain.Skill
	for _, sk := range s.skills {
		result = append(result, *sk)
	}
	return result, nil
}

func (s *skillStore) ListSkillsByCategory(_ context.Context, category string) ([]domain.Skill, error) {
	var result []domain.Skill
	for _, sk := range s.skills {
		if sk.Category == category {
			result = append(result, *sk)
		}
	}
	return result, nil
}

func (s *skillStore) AddSkillToActor(_ context.Context, actorID, skillID string) error {
	if _, ok := s.actorSkills[actorID]; !ok {
		s.actorSkills[actorID] = make(map[string]bool)
	}
	s.actorSkills[actorID][skillID] = true
	return nil
}

func (s *skillStore) RemoveSkillFromActor(_ context.Context, actorID, skillID string) error {
	if m, ok := s.actorSkills[actorID]; ok {
		if _, exists := m[skillID]; exists {
			delete(m, skillID)
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "actor-skill assignment not found")
}

func (s *skillStore) ListActiveWorkflowProjections(_ context.Context) ([]store.WorkflowProjection, error) {
	return s.workflowProjections, nil
}

func (s *skillStore) ListActorSkills(_ context.Context, actorID string) ([]domain.Skill, error) {
	skillIDs, ok := s.actorSkills[actorID]
	if !ok {
		return nil, nil
	}
	var result []domain.Skill
	for sid := range skillIDs {
		if sk, ok := s.skills[sid]; ok {
			result = append(result, *sk)
		}
	}
	return result, nil
}

func makeWorkflowDefJSON(skills ...string) []byte {
	wf := domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{
				ID:   "execute",
				Name: "Execute",
				Execution: &domain.ExecutionConfig{
					RequiredSkills: skills,
				},
			},
		},
	}
	data, _ := json.Marshal(wf)
	return data
}

// ── Setup Helper ──

func setupSkillServer(t *testing.T) (*httptest.Server, *skillStore, string) {
	t.Helper()
	ss := newSkillStore()

	ss.actors["contributor-1"] = &domain.Actor{
		ActorID: "contributor-1", Type: domain.ActorTypeHuman, Name: "Contributor",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ss.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}

	authSvc := auth.NewService(ss)
	contributorToken, _, err := authSvc.CreateToken(context.Background(), "contributor-1", "test", nil)
	if err != nil {
		t.Fatalf("create contributor token: %v", err)
	}

	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: ss, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, ss, contributorToken
}

func skillRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// ── Skill CRUD Tests ──

func TestSkillCreate(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token,
		`{"name":"Go Development","description":"Writes Go","category":"development"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["name"] != "Go Development" {
		t.Errorf("expected name 'Go Development', got %v", body["name"])
	}
	if body["status"] != "active" {
		t.Errorf("expected status 'active', got %v", body["status"])
	}
}

// TestSkillCreate_GeneratesSkillID is the regression fence for the
// INIT-008 / EPIC-003 / TASK-018 finding: the handler used to build a
// domain.Skill with an empty SkillID, which the fake test store
// papered over by auto-numbering and prod auth.skills.skill_id PRIMARY
// KEY blew up on the second insert. Two sequential creates must yield
// distinct, non-empty skill_ids, and an explicitly-supplied skill_id
// must pass through unchanged.
func TestSkillCreate_GeneratesSkillID(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	first := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token,
		`{"name":"Generated A","description":"x","category":"y"}`)
	defer first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d", first.StatusCode)
	}
	firstBody := decodeBody(t, first)
	firstID, _ := firstBody["skill_id"].(string)
	if firstID == "" {
		t.Fatalf("first response has empty skill_id; handler must generate one")
	}

	second := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token,
		`{"name":"Generated B","description":"x","category":"y"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("second: expected 201, got %d", second.StatusCode)
	}
	secondBody := decodeBody(t, second)
	secondID, _ := secondBody["skill_id"].(string)
	if secondID == "" {
		t.Fatalf("second response has empty skill_id")
	}
	if firstID == secondID {
		t.Fatalf("two sequential creates produced the same skill_id %q — would duplicate-PK in prod", firstID)
	}

	explicit := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token,
		`{"skill_id":"sk-explicit","name":"Explicit","description":"x","category":"y"}`)
	defer explicit.Body.Close()
	if explicit.StatusCode != http.StatusCreated {
		t.Fatalf("explicit: expected 201, got %d", explicit.StatusCode)
	}
	explicitBody := decodeBody(t, explicit)
	if got, _ := explicitBody["skill_id"].(string); got != "sk-explicit" {
		t.Fatalf("explicit skill_id not honored: got %q, want %q", got, "sk-explicit")
	}
}

func TestSkillCreate_MissingName(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token,
		`{"description":"No name"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSkillCreate_Unauthenticated(t *testing.T) {
	ts, _, _ := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills", "invalid-token",
		`{"name":"test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestSkillList(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}
	ss.skills["sk-2"] = &domain.Skill{SkillID: "sk-2", Name: "Review", Category: "review", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/skills", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatal("expected items array")
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestSkillList_CategoryFilter(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}
	ss.skills["sk-2"] = &domain.Skill{SkillID: "sk-2", Name: "Review", Category: "review", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/skills?category=dev", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 item for category=dev, got %d", len(items))
	}
}

func TestSkillGet(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/skills/sk-1", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["skill_id"] != "sk-1" {
		t.Errorf("expected skill_id 'sk-1', got %v", body["skill_id"])
	}
}

func TestSkillGet_NotFound(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/skills/nonexistent", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSkillUpdate(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Description: "old", Category: "dev", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-1", token,
		`{"name":"Golang","description":"new desc"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["name"] != "Golang" {
		t.Errorf("expected name 'Golang', got %v", body["name"])
	}
	if body["description"] != "new desc" {
		t.Errorf("expected description 'new desc', got %v", body["description"])
	}
	// Category should remain unchanged
	if body["category"] != "dev" {
		t.Errorf("expected category 'dev' unchanged, got %v", body["category"])
	}
}

func TestSkillUpdate_NotFound(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/nonexistent", token,
		`{"name":"nope"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSkillDeprecate(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-1/deprecate", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	skill, ok := body["skill"].(map[string]any)
	if !ok {
		t.Fatal("expected skill object in response")
	}
	if skill["status"] != "deprecated" {
		t.Errorf("expected status 'deprecated', got %v", skill["status"])
	}
}

func TestSkillDeprecate_NotFound(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills/nonexistent/deprecate", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSkillDeprecate_BlockedByWorkflow(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "execution", Category: "dev", Status: domain.SkillStatusActive}
	ss.workflowProjections = []store.WorkflowProjection{
		{WorkflowID: "task-default", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDefJSON("execution", "review")},
	}

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-1/deprecate", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	workflows, ok := body["workflows"].([]any)
	if !ok {
		t.Fatal("expected workflows array in response")
	}
	if len(workflows) != 1 {
		t.Errorf("expected 1 workflow reference, got %d", len(workflows))
	}
}

func TestSkillDeprecate_ForceOverride(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "execution", Category: "dev", Status: domain.SkillStatusActive}
	ss.workflowProjections = []store.WorkflowProjection{
		{WorkflowID: "task-default", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDefJSON("execution")},
	}

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-1/deprecate?force=true", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	skill, ok := body["skill"].(map[string]any)
	if !ok {
		t.Fatal("expected skill object in response")
	}
	if skill["status"] != "deprecated" {
		t.Errorf("expected status 'deprecated', got %v", skill["status"])
	}
	warnings, ok := body["warnings"].([]any)
	if !ok {
		t.Fatal("expected warnings array in response")
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(warnings))
	}
}

func TestSkillDeprecate_UnrelatedSkillSucceeds(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "unused-skill", Category: "dev", Status: domain.SkillStatusActive}
	ss.workflowProjections = []store.WorkflowProjection{
		{WorkflowID: "task-default", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDefJSON("execution")},
	}

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-1/deprecate", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// ── Actor-Skill Association Tests ──

func TestActorSkillAssign(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["skill_id"] != "sk-1" {
		t.Errorf("expected skill_id 'sk-1', got %v", body["skill_id"])
	}
}

func TestActorSkillAssign_Idempotent(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}

	// Assign twice
	resp1 := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", token, "")
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first assign: expected 200, got %d", resp1.StatusCode)
	}

	resp2 := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", token, "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second assign: expected 200, got %d", resp2.StatusCode)
	}
}

func TestActorSkillAssign_SkillNotFound(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/nonexistent", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestActorSkillRemove(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}
	ss.actorSkills["contributor-1"] = map[string]bool{"sk-1": true}

	resp := skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestActorSkillRemove_NotFound(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/contributor-1/skills/nonexistent", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestActorSkillList(t *testing.T) {
	ts, ss, token := setupSkillServer(t)

	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Category: "dev", Status: domain.SkillStatusActive}
	ss.skills["sk-2"] = &domain.Skill{SkillID: "sk-2", Name: "Review", Category: "review", Status: domain.SkillStatusActive}
	ss.actorSkills["contributor-1"] = map[string]bool{"sk-1": true, "sk-2": true}

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/actors/contributor-1/skills", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatal("expected items array")
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestActorSkillList_Empty(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "GET", ts.URL+"/api/v1/actors/contributor-1/skills", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
