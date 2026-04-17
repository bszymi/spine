package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
)

// Fake ArtifactService that lets us exercise artifact.read without a real
// Git backend. It is only used to confirm the summary-only response shape
// that ADR-007 requires for workflow paths.
type fakeArtifactSvc struct {
	readResult *domain.Artifact
	readErr    error
}

func (f *fakeArtifactSvc) Create(context.Context, string, string) (*artifact.WriteResult, error) {
	return nil, domain.NewError(domain.ErrInternal, "not implemented in fake")
}
func (f *fakeArtifactSvc) Read(_ context.Context, path, _ string) (*domain.Artifact, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.readResult != nil {
		return f.readResult, nil
	}
	return &domain.Artifact{
		Path:    path,
		ID:      "task-default",
		Type:    "Workflow",
		Status:  "Active",
		Title:   "Default Task Workflow",
		Content: "secret body content",
	}, nil
}
func (f *fakeArtifactSvc) Update(context.Context, string, string) (*artifact.WriteResult, error) {
	return nil, domain.NewError(domain.ErrInternal, "not implemented in fake")
}
func (f *fakeArtifactSvc) List(context.Context, string) ([]*domain.Artifact, error) {
	return nil, nil
}
func (f *fakeArtifactSvc) AcceptTask(context.Context, string, string) (*artifact.WriteResult, error) {
	return nil, domain.NewError(domain.ErrInternal, "not implemented in fake")
}
func (f *fakeArtifactSvc) RejectTask(context.Context, string, domain.TaskAcceptance, string) (*artifact.WriteResult, error) {
	return nil, domain.NewError(domain.ErrInternal, "not implemented in fake")
}

func newRejectionServer(t *testing.T, role domain.ActorRole) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: role, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, err := authSvc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     fs,
		Auth:      authSvc,
		Artifacts: &fakeArtifactSvc{},
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestArtifactCreate_RejectsWorkflowPath(t *testing.T) {
	ts, tok := newRejectionServer(t, domain.RoleContributor)
	defer ts.Close()

	body := `{"path":"workflows/task-default.yaml","content":"id: task-default\n"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), "workflow.create") {
		t.Errorf("error payload missing workflow.create hint: %s", raw)
	}
}

func TestArtifactCreate_RejectsLeadingSlashWorkflowPath(t *testing.T) {
	ts, tok := newRejectionServer(t, domain.RoleContributor)
	defer ts.Close()

	// Leading slash variant — per the spec callers may use either form.
	body := `{"path":"/workflows/task-default.yaml","content":"id: task-default\n"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestArtifactUpdate_RejectsWorkflowPath(t *testing.T) {
	ts, tok := newRejectionServer(t, domain.RoleContributor)
	defer ts.Close()

	body := `{"content":"id: task-default\n"}`
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/artifacts/workflows/task-default.yaml", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), "workflow.update") {
		t.Errorf("error payload missing workflow.update hint: %s", raw)
	}
}

func TestArtifactRead_WorkflowPath_ReturnsSummaryOnly(t *testing.T) {
	ts, tok := newRejectionServer(t, domain.RoleReader)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/artifacts/workflows/task-default.yaml", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if _, has := body["content"]; has {
		t.Errorf("response must not include content for workflow path: %v", body)
	}
	if body["title"] != "Default Task Workflow" {
		t.Errorf("title missing from summary: %v", body["title"])
	}
	if _, has := body["note"]; !has {
		t.Errorf("response must include pointer note: %v", body)
	}
}

func TestArtifactRead_NonWorkflowPath_ReturnsFullContent(t *testing.T) {
	ts, tok := newRejectionServer(t, domain.RoleReader)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/artifacts/initiatives/INIT-001/initiative.md", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["content"] == nil {
		t.Error("non-workflow paths must include content")
	}
}
