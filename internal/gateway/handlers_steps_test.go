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

type fakeStepExecutionLister struct {
	steps []engine.StepExecutionItem
	err   error
}

func (f *fakeStepExecutionLister) ListStepExecutions(_ context.Context, _ engine.StepExecutionQuery) ([]engine.StepExecutionItem, error) {
	return f.steps, f.err
}

func newStepsServer(lister gateway.StepExecutionLister) (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:               fs,
		Auth:                authSvc,
		StepExecutionLister: lister,
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleListStepExecutions_Success(t *testing.T) {
	lister := &fakeStepExecutionLister{
		steps: []engine.StepExecutionItem{
			{ExecutionID: "exec-1", RunID: "run-1", StepID: "step1", Status: "assigned"},
		},
	}
	ts, token := newStepsServer(lister)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/steps?actor_id=bot-1&status=assigned", http.NoBody)
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
	steps := body["steps"].([]any)
	if len(steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(steps))
	}
}

func TestHandleListStepExecutions_Unavailable(t *testing.T) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/steps", http.NoBody)
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

func TestHandleListStepExecutions_InvalidStatus(t *testing.T) {
	lister := &fakeStepExecutionLister{}
	ts, token := newStepsServer(lister)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/steps?status=invalid", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleListStepExecutions_InvalidLimit(t *testing.T) {
	lister := &fakeStepExecutionLister{}
	ts, token := newStepsServer(lister)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/steps?limit=abc", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleListStepExecutions_LimitZero(t *testing.T) {
	lister := &fakeStepExecutionLister{}
	ts, token := newStepsServer(lister)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/steps?limit=0", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
