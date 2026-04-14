package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
)

func newArtifactEntryServer() (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleArtifactEntryCreate_MissingTitle(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_InvalidType(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Unknown","title":"My Task"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_TaskMissingParent(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Task","title":"My Task"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_EpicMissingParent(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Epic","title":"My Epic"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_InitiativeWithParent(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Initiative","title":"My Initiative","parent":"other-id"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_NoPlanningRunStarter(t *testing.T) {
	// Server without PlanningRunStarter configured.
	ts, token := newArtifactEntryServer()
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
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactAdd_MissingRunID(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactAdd_MissingTitle(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactAdd_MissingParent(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactAdd_InvalidType(t *testing.T) {
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Initiative","title":"My Artifact","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleArtifactEntryCreate_NoArtifactService(t *testing.T) {
	// Server has planningRunStarter but no artifact service → 503
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:               fs,
		Auth:                authSvc,
		PlanningRunStarter:  &fakePlanningRunStarterStub{},
		// No Artifacts service.
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
		t.Errorf("expected 503 (no artifact service), got %d", resp.StatusCode)
	}
}

// fakePlanningRunStarterStub is a minimal stub for PlanningRunStarter.
type fakePlanningRunStarterStub struct{}

func (f *fakePlanningRunStarterStub) StartPlanningRun(_ context.Context, _, _ string) (*gateway.PlanningRunResult, error) {
	return &gateway.PlanningRunResult{RunID: "run-stub", BranchName: "branch-stub"}, nil
}

func TestHandleArtifactAdd_RunLookup(t *testing.T) {
	// This test reaches the GetRun call (store lookup) and the run mode check.
	// The fakeStore returns a non-planning run, so we expect 400.
	ts, token := newArtifactEntryServer()
	defer ts.Close()

	// fakeStore.GetRun returns RunStatusActive and BranchName="spine/run/test-branch",
	// but Mode defaults to empty (not RunModePlanning) → invalid_params.
	body := `{"run_id":"run-1","artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Expect 400 because run mode is not "planning".
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (non-planning run), got %d", resp.StatusCode)
	}
}
