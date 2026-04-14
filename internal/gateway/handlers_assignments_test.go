package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
)

func newAssignmentsServer(fs *fakeStore) (*httptest.Server, string) {
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleListAssignments_Success(t *testing.T) {
	fs := newFakeStore()
	active := domain.AssignmentStatusActive
	fs.assignments = []domain.Assignment{
		{AssignmentID: "assign-1", ActorID: "bot-1", Status: active},
		{AssignmentID: "assign-2", ActorID: "bot-2", Status: active},
	}
	ts, token := newAssignmentsServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/assignments?actor_id=bot-1", http.NoBody)
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

func TestHandleListAssignments_MissingActorID(t *testing.T) {
	fs := newFakeStore()
	ts, token := newAssignmentsServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/assignments", http.NoBody)
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

func TestHandleListAssignments_WithStatusFilter(t *testing.T) {
	fs := newFakeStore()
	active := domain.AssignmentStatusActive
	completed := domain.AssignmentStatusCompleted
	fs.assignments = []domain.Assignment{
		{AssignmentID: "a1", ActorID: "bot-1", Status: active},
		{AssignmentID: "a2", ActorID: "bot-1", Status: completed},
	}
	ts, token := newAssignmentsServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/assignments?actor_id=bot-1&status=active", http.NoBody)
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
		t.Errorf("expected count=1 (only active), got %v", body["count"])
	}
}

func TestHandleListAssignments_EmptyResult(t *testing.T) {
	fs := newFakeStore()
	ts, token := newAssignmentsServer(fs)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/assignments?actor_id=nobody", http.NoBody)
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
	if body["count"].(float64) != 0 {
		t.Errorf("expected count=0, got %v", body["count"])
	}
}
