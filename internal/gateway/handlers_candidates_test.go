package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
)

type fakeCandidateFinder struct {
	candidates []engine.ExecutionCandidate
	err        error
}

func (f *fakeCandidateFinder) FindExecutionCandidates(_ context.Context, _ engine.ExecutionCandidateFilter) ([]engine.ExecutionCandidate, error) {
	return f.candidates, f.err
}

func newCandidatesServer(finder gateway.CandidateFinder) (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:           fs,
		Auth:            authSvc,
		CandidateFinder: finder,
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleExecutionCandidates_Success(t *testing.T) {
	finder := &fakeCandidateFinder{
		candidates: []engine.ExecutionCandidate{
			{TaskPath: "tasks/TASK-001.md", TaskID: "TASK-001", Title: "Sample"},
		},
	}
	ts, token := newCandidatesServer(finder)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/candidates", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["count"].(float64) != 1 {
		t.Errorf("expected count=1, got %v", body["count"])
	}
}

func TestHandleExecutionCandidates_Unavailable(t *testing.T) {
	// No CandidateFinder configured.
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/candidates", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleExecutionCandidates_WithFilters(t *testing.T) {
	finder := &fakeCandidateFinder{candidates: []engine.ExecutionCandidate{}}
	ts, token := newCandidatesServer(finder)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/candidates?actor_type=ai_agent&skills=backend&include_blocked=true", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleExecutionCandidates_ServiceError(t *testing.T) {
	finder := &fakeCandidateFinder{err: domain.NewError(domain.ErrInternal, "db error")}
	ts, token := newCandidatesServer(finder)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/candidates", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}
