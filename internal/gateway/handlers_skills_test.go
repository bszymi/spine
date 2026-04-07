package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
)

// ── Skill-aware Fake Store ──

type skillStore struct {
	store.Store
	actors      map[string]*domain.Actor
	tokens      map[string]*fakeTokenEntry
	skills      map[string]*domain.Skill
	actorSkills map[string]map[string]bool // actorID -> skillID -> exists
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

var skillIDCounter int

func (s *skillStore) CreateSkill(_ context.Context, skill *domain.Skill) error {
	if skill.SkillID == "" {
		skillIDCounter++
		skill.SkillID = "sk-" + strings.Repeat("0", 5) + string(rune('0'+skillIDCounter))
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
	if body["status"] != "deprecated" {
		t.Errorf("expected status 'deprecated', got %v", body["status"])
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
