package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
)

// planningFakeStore embeds fakeStore and overrides GetRun to return a planning run.
type planningFakeStore struct {
	*fakeStore
}

func (p *planningFakeStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	return &domain.Run{
		RunID:         runID,
		Mode:          domain.RunModePlanning,
		Status:        domain.RunStatusActive,
		CurrentStepID: "draft",
		BranchName:    "spine/run/plan-branch",
	}, nil
}

// newArtifactEntryFullServer creates a server with planningRunStarter + artSvc + gitReader.
func newArtifactEntryFullServer(t *testing.T, artSvc gateway.ArtifactService) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:              fs,
		Auth:               authSvc,
		Artifacts:          artSvc,
		PlanningRunStarter: &fakePlanningRunStarterStub{},
		Git:                &fakeGitReader{files: map[string][]byte{}},
	})
	return httptest.NewServer(srv.Handler()), token
}

// newArtifactAddFullServer creates a server with a planning-mode store + artSvc + gitReader.
func newArtifactAddFullServer(t *testing.T, artSvc gateway.ArtifactService) (*httptest.Server, string) {
	t.Helper()
	base := newFakeStore()
	base.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ps := &planningFakeStore{fakeStore: base}
	authSvc := auth.NewService(base)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     ps,
		Auth:      authSvc,
		Artifacts: artSvc,
		Git:       &fakeGitReader{files: map[string][]byte{}},
	})
	return httptest.NewServer(srv.Handler()), token
}

// TestHandleArtifactEntryCreate_NoGitReader verifies that if only planningRunStarter and
// artSvc are configured (no gitReader), the handler returns 503.
func TestHandleArtifactEntryCreate_NoGitReader(t *testing.T) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:              fs,
		Auth:               authSvc,
		Artifacts:          newFakeArtifactService(),
		PlanningRunStarter: &fakePlanningRunStarterStub{},
		// No Git configured → gitFrom returns nil
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"artifact_type":"Initiative","title":"My Initiative"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no git reader), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactEntryCreate_InitiativeSuccess verifies the full success path for
// creating an Initiative artifact (no parent required).
func TestHandleArtifactEntryCreate_InitiativeSuccess(t *testing.T) {
	artSvc := newFakeArtifactService()
	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	body := `{"artifact_type":"Initiative","title":"My New Initiative"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["run_id"] == "" {
		t.Error("expected run_id in response")
	}
	if result["artifact_id"] == "" {
		t.Error("expected artifact_id in response")
	}
}

// TestHandleArtifactEntryCreate_TaskWithEpicParent covers resolveParentFromList
// by providing an Epic in the artifact service and requesting a Task under it.
func TestHandleArtifactEntryCreate_TaskWithEpicParent(t *testing.T) {
	artSvc := newFakeArtifactService()
	// Pre-seed an Epic that will be found by resolveParentFromList.
	artSvc.artifacts["initiatives/INIT-001/epics/EPIC-001/epic.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/epics/EPIC-001/epic.md",
		ID:    "EPIC-001",
		Type:  domain.ArtifactTypeEpic,
		Title: "My Epic",
	}

	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	body := `{"artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

// TestHandleArtifactEntryCreate_EpicWithInitiativeParent covers resolveParentFromList
// for the Epic → Initiative parent relationship.
func TestHandleArtifactEntryCreate_EpicWithInitiativeParent(t *testing.T) {
	artSvc := newFakeArtifactService()
	artSvc.artifacts["initiatives/INIT-001/initiative.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/initiative.md",
		ID:    "INIT-001",
		Type:  domain.ArtifactTypeInitiative,
		Title: "My Initiative",
	}

	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	body := `{"artifact_type":"Epic","title":"My Epic","parent":"INIT-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

// TestHandleArtifactEntryCreate_ParentNotFound verifies that a 404 is returned
// when the specified parent artifact does not exist.
func TestHandleArtifactEntryCreate_ParentNotFound(t *testing.T) {
	artSvc := newFakeArtifactService()
	// artSvc has no artifacts — parent lookup will fail.
	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	body := `{"artifact_type":"Task","title":"My Task","parent":"EPIC-999"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (parent not found), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactEntryCreate_WrongParentType verifies that using a Task as parent
// for another Task returns 400 (wrong parent type).
func TestHandleArtifactEntryCreate_WrongParentType(t *testing.T) {
	artSvc := newFakeArtifactService()
	// Pre-seed a Task (not an Epic) as the "parent".
	artSvc.artifacts["initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md",
		ID:    "TASK-001",
		Type:  domain.ArtifactTypeTask,
		Title: "Existing Task",
	}

	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	body := `{"artifact_type":"Task","title":"New Task","parent":"TASK-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (wrong parent type), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactAdd_PlanningMode_NoArtSvc verifies that if artSvc is missing
// on a valid planning run, the handler returns 503.
func TestHandleArtifactAdd_PlanningMode_NoArtSvc(t *testing.T) {
	base := newFakeStore()
	base.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ps := &planningFakeStore{fakeStore: base}
	authSvc := auth.NewService(base)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: ps,
		Auth:  authSvc,
		// No Artifacts, no Git.
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no artSvc), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactAdd_PlanningMode_NoGitReader verifies that if artSvc is present
// but gitReader is missing, the handler returns 503.
func TestHandleArtifactAdd_PlanningMode_NoGitReader(t *testing.T) {
	base := newFakeStore()
	base.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ps := &planningFakeStore{fakeStore: base}
	authSvc := auth.NewService(base)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     ps,
		Auth:      authSvc,
		Artifacts: newFakeArtifactService(),
		// No Git configured.
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no git reader), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactAdd_PlanningMode_Success covers the full success path for
// adding a Task to an existing planning run, exercising resolveParentFromRef.
func TestHandleArtifactAdd_PlanningMode_Success(t *testing.T) {
	artSvc := newFakeArtifactService()
	// Pre-seed an Epic that resolveParentFromRef will find.
	artSvc.artifacts["initiatives/INIT-001/epics/EPIC-001/epic.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/epics/EPIC-001/epic.md",
		ID:    "EPIC-001",
		Type:  domain.ArtifactTypeEpic,
		Title: "My Epic",
	}

	ts, token := newArtifactAddFullServer(t, artSvc)
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["artifact_id"] == "" {
		t.Error("expected artifact_id in response")
	}
	if result["branch"] != "spine/run/plan-branch" {
		t.Errorf("expected branch spine/run/plan-branch, got %v", result["branch"])
	}
}

// TestHandleArtifactAdd_PlanningMode_ParentNotFound verifies 404 when parent
// is not found in resolveParentFromRef.
func TestHandleArtifactAdd_PlanningMode_ParentNotFound(t *testing.T) {
	artSvc := newFakeArtifactService()
	// No artifacts pre-seeded → parent lookup will fail.
	ts, token := newArtifactAddFullServer(t, artSvc)
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task","parent":"EPIC-999"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (parent not found on branch), got %d", resp.StatusCode)
	}
}

// TestHandleTokenRevoke_NotFound verifies that revoking a non-existent token returns 404.
func TestHandleTokenRevoke_NotFound(t *testing.T) {
	ts, _, adminToken := setupAuthServer(t)
	defer ts.Close()

	// Attempt to revoke a token that doesn't exist.
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/tokens/nonexistent-token-id", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (token not found), got %d", resp.StatusCode)
	}
}
