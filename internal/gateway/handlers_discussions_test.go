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

// ── Discussion-aware Fake Store ──

type discussionStore struct {
	store.Store
	actors      map[string]*domain.Actor
	tokens      map[string]*fakeTokenEntry
	threads     map[string]*domain.DiscussionThread
	comments    map[string][]domain.Comment
	projections map[string]*store.ArtifactProjection
	runs        map[string]*domain.Run
	executions  map[string]*domain.StepExecution
}

func newDiscussionStore() *discussionStore {
	return &discussionStore{
		actors:      make(map[string]*domain.Actor),
		tokens:      make(map[string]*fakeTokenEntry),
		threads:     make(map[string]*domain.DiscussionThread),
		comments:    make(map[string][]domain.Comment),
		projections: make(map[string]*store.ArtifactProjection),
		runs:        make(map[string]*domain.Run),
		executions:  make(map[string]*domain.StepExecution),
	}
}

func (d *discussionStore) Ping(_ context.Context) error { return nil }

func (d *discussionStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := d.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

func (d *discussionStore) GetActorByTokenHash(_ context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	entry, ok := d.tokens[tokenHash]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	return entry.actor, entry.token, nil
}

func (d *discussionStore) CreateToken(_ context.Context, record *store.TokenRecord) error {
	actor, ok := d.actors[record.ActorID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	d.tokens[record.TokenHash] = &fakeTokenEntry{
		actor: actor,
		token: &domain.Token{TokenID: record.TokenID, ActorID: record.ActorID, Name: record.Name, ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt},
	}
	return nil
}

func (d *discussionStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	p, ok := d.projections[path]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "artifact not found")
	}
	return p, nil
}

func (d *discussionStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	r, ok := d.runs[runID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	return r, nil
}

func (d *discussionStore) GetStepExecution(_ context.Context, execID string) (*domain.StepExecution, error) {
	e, ok := d.executions[execID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	return e, nil
}

func (d *discussionStore) CreateThread(_ context.Context, thread *domain.DiscussionThread) error {
	d.threads[thread.ThreadID] = thread
	return nil
}

func (d *discussionStore) GetThread(_ context.Context, threadID string) (*domain.DiscussionThread, error) {
	t, ok := d.threads[threadID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "thread not found")
	}
	return t, nil
}

func (d *discussionStore) ListThreads(_ context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error) {
	var result []domain.DiscussionThread
	for _, t := range d.threads {
		if t.AnchorType == anchorType && t.AnchorID == anchorID {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (d *discussionStore) UpdateThread(_ context.Context, thread *domain.DiscussionThread) error {
	if _, ok := d.threads[thread.ThreadID]; !ok {
		return domain.NewError(domain.ErrNotFound, "thread not found")
	}
	d.threads[thread.ThreadID] = thread
	return nil
}

func (d *discussionStore) CreateComment(_ context.Context, comment *domain.Comment) error {
	d.comments[comment.ThreadID] = append(d.comments[comment.ThreadID], *comment)
	return nil
}

func (d *discussionStore) ListComments(_ context.Context, threadID string) ([]domain.Comment, error) {
	return d.comments[threadID], nil
}

// ── Setup Helper ──

func setupDiscussionServer(t *testing.T) (*httptest.Server, *discussionStore, string, string) {
	t.Helper()
	ds := newDiscussionStore()

	// Create actors: contributor and reviewer
	ds.actors["contributor-1"] = &domain.Actor{
		ActorID: "contributor-1", Type: domain.ActorTypeHuman, Name: "Contributor",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ds.actors["reviewer-1"] = &domain.Actor{
		ActorID: "reviewer-1", Type: domain.ActorTypeHuman, Name: "Reviewer",
		Role: domain.RoleReviewer, Status: domain.ActorStatusActive,
	}
	ds.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}

	// Seed an artifact for anchor validation
	ds.projections["tasks/TASK-001.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/TASK-001.md", ArtifactID: "TASK-001",
		ArtifactType: "Task", Title: "Test Task", Status: "Pending",
	}

	authSvc := auth.NewService(ds)
	contributorToken, _, err := authSvc.CreateToken(context.Background(), "contributor-1", "test", nil)
	if err != nil {
		t.Fatalf("create contributor token: %v", err)
	}
	reviewerToken, _, err := authSvc.CreateToken(context.Background(), "reviewer-1", "test", nil)
	if err != nil {
		t.Fatalf("create reviewer token: %v", err)
	}

	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: ds, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, ds, contributorToken, reviewerToken
}

func doRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, bodyReader)
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

// ── Tests ──

func TestDiscussionCreate(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	body := `{"anchor_type":"artifact","anchor_id":"tasks/TASK-001.md","title":"Test thread"}`
	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions", token, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["thread_id"] == nil {
		t.Error("expected thread_id in response")
	}
	if result["status"] != "open" {
		t.Errorf("expected status open, got %v", result["status"])
	}
	if result["anchor_type"] != "artifact" {
		t.Errorf("expected anchor_type artifact, got %v", result["anchor_type"])
	}
}

func TestDiscussionCreateMissingFields(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions", token, `{"anchor_type":"artifact"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDiscussionCreateInvalidAnchorType(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions", token, `{"anchor_type":"invalid","anchor_id":"foo"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDiscussionCreateAnchorNotFound(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions", token, `{"anchor_type":"artifact","anchor_id":"nonexistent.md"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDiscussionList(t *testing.T) {
	ts, ds, token, _ := setupDiscussionServer(t)

	// Seed a thread
	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "GET", ts.URL+"/api/v1/discussions?anchor_type=artifact&anchor_id=tasks/TASK-001.md", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 thread, got %d", len(items))
	}
}

func TestDiscussionListMissingParams(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "GET", ts.URL+"/api/v1/discussions", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDiscussionGet(t *testing.T) {
	ts, ds, token, _ := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}
	ds.comments["t1"] = []domain.Comment{
		{CommentID: "c1", ThreadID: "t1", AuthorID: "contributor-1", AuthorType: "human", Content: "Hello", CreatedAt: time.Now().UTC()},
	}

	resp := doRequest(t, "GET", ts.URL+"/api/v1/discussions/t1", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["thread_id"] != "t1" {
		t.Errorf("expected thread_id t1, got %v", result["thread_id"])
	}
	comments := result["comments"].([]any)
	if len(comments) != 1 {
		t.Errorf("expected 1 comment, got %d", len(comments))
	}
}

func TestDiscussionGetNotFound(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "GET", ts.URL+"/api/v1/discussions/nonexistent", token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDiscussionComment(t *testing.T) {
	ts, ds, token, _ := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	body := `{"content":"This is a comment"}`
	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/comments", token, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["comment_id"] == nil {
		t.Error("expected comment_id in response")
	}
	if result["content"] != "This is a comment" {
		t.Errorf("expected content 'This is a comment', got %v", result["content"])
	}
}

func TestDiscussionCommentWithParent(t *testing.T) {
	ts, ds, token, _ := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	body := `{"content":"Reply","parent_comment_id":"c1"}`
	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/comments", token, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["parent_comment_id"] != "c1" {
		t.Errorf("expected parent_comment_id c1, got %v", result["parent_comment_id"])
	}
}

func TestDiscussionCommentMissingContent(t *testing.T) {
	ts, ds, token, _ := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/comments", token, `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDiscussionCommentThreadNotFound(t *testing.T) {
	ts, _, token, _ := setupDiscussionServer(t)

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/nonexistent/comments", token, `{"content":"test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDiscussionResolve(t *testing.T) {
	ts, ds, _, reviewerToken := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	body := `{"resolution_type":"artifact_updated","resolution_refs":["commit-abc"]}`
	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/resolve", reviewerToken, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "resolved" {
		t.Errorf("expected status resolved, got %v", result["status"])
	}
	if result["resolution_type"] != "artifact_updated" {
		t.Errorf("expected resolution_type artifact_updated, got %v", result["resolution_type"])
	}
}

func TestDiscussionResolveAlreadyResolved(t *testing.T) {
	ts, ds, _, reviewerToken := setupDiscussionServer(t)

	resolved := time.Now().UTC()
	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusResolved, ResolvedAt: &resolved, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/resolve", reviewerToken, `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %d", resp.StatusCode)
	}
}

func TestDiscussionResolvePermission(t *testing.T) {
	ts, ds, contributorToken, _ := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	// Contributors should not be able to resolve (requires reviewer)
	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/resolve", contributorToken, `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDiscussionReopen(t *testing.T) {
	ts, ds, _, reviewerToken := setupDiscussionServer(t)

	resolved := time.Now().UTC()
	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusResolved, ResolvedAt: &resolved,
		ResolutionType: domain.ResolutionArtifactUpdated,
		CreatedBy:      "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/reopen", reviewerToken, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "open" {
		t.Errorf("expected status open, got %v", result["status"])
	}
}

func TestDiscussionReopenAlreadyOpen(t *testing.T) {
	ts, ds, _, reviewerToken := setupDiscussionServer(t)

	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusOpen, CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/reopen", reviewerToken, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %d", resp.StatusCode)
	}
}

func TestDiscussionReopenPermission(t *testing.T) {
	ts, ds, contributorToken, _ := setupDiscussionServer(t)

	resolved := time.Now().UTC()
	ds.threads["t1"] = &domain.DiscussionThread{
		ThreadID: "t1", AnchorType: domain.AnchorTypeArtifact, AnchorID: "tasks/TASK-001.md",
		Status: domain.ThreadStatusResolved, ResolvedAt: &resolved,
		CreatedBy: "contributor-1", CreatedAt: time.Now().UTC(),
	}

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/t1/reopen", contributorToken, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDiscussionReopenNotFound(t *testing.T) {
	ts, _, _, reviewerToken := setupDiscussionServer(t)

	resp := doRequest(t, "POST", ts.URL+"/api/v1/discussions/nonexistent/reopen", reviewerToken, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}
