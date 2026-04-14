package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
)

func newExecQueryServer(fs *fakeStore) (*httptest.Server, string) {
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

var sampleProj = store.ExecutionProjection{
	TaskPath:         "tasks/TASK-001.md",
	TaskID:           "TASK-001",
	Title:            "Sample Task",
	Status:           "pending",
	AssignmentStatus: "unassigned",
	LastUpdated:      time.Now(),
}

func TestHandleExecutionTasksReady_Success(t *testing.T) {
	fs := newFakeStore()
	fs.execProjs = []store.ExecutionProjection{sampleProj}
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/ready", http.NoBody)
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

func TestHandleExecutionTasksReady_StoreError(t *testing.T) {
	fs := newFakeStore()
	fs.execProjErr = domain.NewError(domain.ErrInternal, "db error")
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/ready", http.NoBody)
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

func TestHandleExecutionTasksBlocked_Success(t *testing.T) {
	fs := newFakeStore()
	blocked := sampleProj
	blocked.Blocked = true
	fs.execProjs = []store.ExecutionProjection{blocked}
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/blocked", http.NoBody)
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

func TestHandleExecutionTasksAssigned_Success(t *testing.T) {
	fs := newFakeStore()
	assigned := sampleProj
	assigned.AssignedActorID = "bot-1"
	assigned.AssignmentStatus = "assigned"
	fs.execProjs = []store.ExecutionProjection{assigned}
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/assigned?actor_id=bot-1", http.NoBody)
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

func TestHandleExecutionTasksAssigned_MissingActorID(t *testing.T) {
	fs := newFakeStore()
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/assigned", http.NoBody)
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

func TestHandleExecutionTasksAll_Success(t *testing.T) {
	fs := newFakeStore()
	fs.execProjs = []store.ExecutionProjection{sampleProj}
	ts, token := newExecQueryServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks", http.NoBody)
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

func TestHandleExecutionTasksAll_Unauthorized(t *testing.T) {
	fs := newFakeStore()
	ts, _ := newExecQueryServer(fs)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/execution/tasks")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}
