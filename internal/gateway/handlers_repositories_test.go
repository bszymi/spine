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
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workspace"
)

// ── Repository handler test scaffolding ──
//
// The gateway tests hold the manager and its dependencies directly so
// each test gets a fresh InMemoryCatalogStore plus a small in-memory
// ManagerStore implementation. Auth and actor lookups still flow
// through skillStore (re-used from the broader gateway test
// scaffolding) because the gateway's auth middleware reads tokens
// from store.Store.

type repoBindingsFake struct {
	bindings map[string]*store.RepositoryBinding
}

func newRepoBindingsFake() *repoBindingsFake {
	return &repoBindingsFake{bindings: map[string]*store.RepositoryBinding{}}
}

func (f *repoBindingsFake) GetRepositoryBinding(_ context.Context, _, id string) (*store.RepositoryBinding, error) {
	b, ok := f.bindings[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "binding not found")
	}
	out := *b
	return &out, nil
}

func (f *repoBindingsFake) ListRepositoryBindings(_ context.Context, _ string) ([]store.RepositoryBinding, error) {
	out := make([]store.RepositoryBinding, 0, len(f.bindings))
	for _, b := range f.bindings {
		out = append(out, *b)
	}
	return out, nil
}

func (f *repoBindingsFake) GetActiveRepositoryBinding(ctx context.Context, ws, id string) (*store.RepositoryBinding, error) {
	b, err := f.GetRepositoryBinding(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if b.Status != store.RepositoryBindingStatusActive {
		return nil, domain.NewError(domain.ErrNotFound, "binding inactive")
	}
	return b, nil
}

func (f *repoBindingsFake) CreateRepositoryBinding(_ context.Context, b *store.RepositoryBinding) error {
	if _, exists := f.bindings[b.RepositoryID]; exists {
		return domain.NewError(domain.ErrAlreadyExists, "duplicate binding")
	}
	if b.Status == "" {
		b.Status = store.RepositoryBindingStatusActive
	}
	cp := *b
	f.bindings[b.RepositoryID] = &cp
	return nil
}

func (f *repoBindingsFake) UpdateRepositoryBinding(_ context.Context, b *store.RepositoryBinding) error {
	cur, ok := f.bindings[b.RepositoryID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "binding not found")
	}
	cur.CloneURL = b.CloneURL
	cur.CredentialsRef = b.CredentialsRef
	cur.LocalPath = b.LocalPath
	if b.DefaultBranch != "" {
		cur.DefaultBranch = b.DefaultBranch
	}
	if b.Status != "" {
		cur.Status = b.Status
	}
	return nil
}

func (f *repoBindingsFake) DeactivateRepositoryBinding(_ context.Context, _, id string) error {
	cur, ok := f.bindings[id]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "binding not found")
	}
	cur.Status = store.RepositoryBindingStatusInactive
	return nil
}

func setupRepoServer(t *testing.T, runs repository.RunReferenceChecker) (*httptest.Server, *repoBindingsFake, map[string]string) {
	t.Helper()

	st := newSkillStore()
	st.actors["operator-1"] = &domain.Actor{
		ActorID: "operator-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	st.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}

	authSvc := auth.NewService(st)
	opTok, _, err := authSvc.CreateToken(context.Background(), "operator-1", "test", nil)
	if err != nil {
		t.Fatalf("create operator token: %v", err)
	}
	readerTok, _, err := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	if err != nil {
		t.Fatalf("create reader token: %v", err)
	}

	primary := repository.PrimarySpec{
		Name: "Acme Spine", DefaultBranch: "main",
		LocalPath: "/var/spine/workspaces/acme/repos/spine",
	}
	cat := repository.NewInMemoryCatalogStore(primary)
	bindings := newRepoBindingsFake()
	mgr := repository.NewManager("acme", primary, cat, bindings, runs)

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: st, Auth: authSvc, RepositoryManager: mgr,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, bindings, map[string]string{"op": opTok, "reader": readerTok}
}

func doRepoRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	var br *strings.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if br != nil {
		req, err = http.NewRequest(method, url, br)
	} else {
		req, err = http.NewRequest(method, url, http.NoBody)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func decodeRepoBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestRepositoryCreateRoundTrip(t *testing.T) {
	ts, bindings, toks := setupRepoServer(t, nil)
	body := `{
        "id": "payments-service",
        "name": "Payments Service",
        "default_branch": "main",
        "role": "service",
        "clone_url": "https://example.com/payments.git",
        "credentials_ref": "secret://payments",
        "local_path": "/var/spine/workspaces/acme/repos/payments-service"
    }`
	resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	out := decodeRepoBody(t, resp)
	if out["id"] != "payments-service" || out["kind"] != "code" || out["status"] != "active" {
		t.Errorf("unexpected response: %+v", out)
	}
	if _, ok := bindings.bindings["payments-service"]; !ok {
		t.Errorf("binding not persisted")
	}
}

func TestRepositoryCreateRedactsCredentialsInCloneURL(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	body := `{
        "id": "secret-svc",
        "name": "S",
        "default_branch": "main",
        "clone_url": "https://deploy:abc123@example.com/s.git",
        "local_path": "/r/s"
    }`
	resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	out := decodeRepoBody(t, resp)
	clone, _ := out["clone_url"].(string)
	if strings.Contains(clone, "deploy") || strings.Contains(clone, "abc123") {
		t.Errorf("clone_url not redacted: %q", clone)
	}
}

func TestRepositoryCreateRequiresOperatorRole(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	body := `{
        "id": "x", "name": "x", "default_branch": "main",
        "clone_url": "https://example.com/x.git", "local_path": "/r/x"
    }`
	resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["reader"], body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("reader should be forbidden, got %d", resp.StatusCode)
	}
}

func TestRepositoryCreateValidatesInput(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	cases := map[string]string{
		"bad id":            `{"id":"Bad ID","name":"x","default_branch":"main","clone_url":"https://e.com/x.git","local_path":"/r/x"}`,
		"missing clone_url": `{"id":"ok-id","name":"x","default_branch":"main","local_path":"/r/x"}`,
		"http clone_url":    `{"id":"ok-id","name":"x","default_branch":"main","clone_url":"http://e.com/x.git","local_path":"/r/x"}`,
		"primary id":        `{"id":"spine","name":"x","default_branch":"main","clone_url":"https://e.com/x.git","local_path":"/r/x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", resp.StatusCode)
			}
		})
	}
}

func TestRepositoryListIncludesPrimaryAndRegistered(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	create := `{"id":"payments-service","name":"P","default_branch":"main","clone_url":"https://e.com/p.git","local_path":"/r/p"}`
	createResp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], create)
	createResp.Body.Close()

	resp := doRepoRequest(t, "GET", ts.URL+"/api/v1/repositories", toks["reader"], "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeRepoBody(t, resp)
	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 items, got %v", body["items"])
	}
	first := items[0].(map[string]any)
	if first["id"] != "spine" {
		t.Errorf("primary not pinned first: %v", first["id"])
	}
}

func TestRepositoryGetReturnsRedactedClone(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	create := `{"id":"payments-service","name":"P","default_branch":"main","clone_url":"https://user:pw@e.com/p.git","local_path":"/r/p"}`
	createResp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], create)
	createResp.Body.Close()

	resp := doRepoRequest(t, "GET", ts.URL+"/api/v1/repositories/payments-service", toks["reader"], "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	out := decodeRepoBody(t, resp)
	clone, _ := out["clone_url"].(string)
	if strings.Contains(clone, "user") || strings.Contains(clone, "pw") {
		t.Errorf("clone_url not redacted on GET: %q", clone)
	}
}

func TestRepositoryUpdate(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	create := `{"id":"payments-service","name":"P","default_branch":"main","clone_url":"https://e.com/p.git","local_path":"/r/p"}`
	createResp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], create)
	createResp.Body.Close()

	update := `{"name":"Payments","clone_url":"https://e.com/p-new.git"}`
	resp := doRepoRequest(t, "PUT", ts.URL+"/api/v1/repositories/payments-service", toks["op"], update)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	out := decodeRepoBody(t, resp)
	if out["name"] != "Payments" || out["clone_url"] != "https://e.com/p-new.git" {
		t.Errorf("update did not apply: %+v", out)
	}
}

func TestRepositoryDeactivate(t *testing.T) {
	ts, _, toks := setupRepoServer(t, nil)
	create := `{"id":"payments-service","name":"P","default_branch":"main","clone_url":"https://e.com/p.git","local_path":"/r/p"}`
	createResp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], create)
	createResp.Body.Close()

	resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories/payments-service/deactivate", toks["op"], "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	out := decodeRepoBody(t, resp)
	if out["status"] != "inactive" {
		t.Errorf("expected status=inactive, got %v", out["status"])
	}
}

type stubActiveRunsChecker struct{ active bool }

func (s stubActiveRunsChecker) AnyActiveRunReferences(context.Context, string, string) (bool, error) {
	return s.active, nil
}

func TestRepositoryDeactivateRefusesWithActiveRuns(t *testing.T) {
	ts, _, toks := setupRepoServer(t, stubActiveRunsChecker{active: true})
	create := `{"id":"payments-service","name":"P","default_branch":"main","clone_url":"https://e.com/p.git","local_path":"/r/p"}`
	createResp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories", toks["op"], create)
	createResp.Body.Close()

	resp := doRepoRequest(t, "POST", ts.URL+"/api/v1/repositories/payments-service/deactivate", toks["op"], "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected 412 precondition_failed, got %d", resp.StatusCode)
	}
}

func TestRepositoryRefusedInSharedMode(t *testing.T) {
	st := newSkillStore()
	st.actors["operator-1"] = &domain.Actor{
		ActorID: "operator-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(st)
	tok, _, err := authSvc.CreateToken(context.Background(), "operator-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	primary := repository.PrimarySpec{Name: "Acme Spine", DefaultBranch: "main", LocalPath: "/r/spine"}
	cat := repository.NewInMemoryCatalogStore(primary)
	mgr := repository.NewManager("acme", primary, cat, newRepoBindingsFake(), nil)

	// Configure WorkspaceResolver — that flips the gateway into
	// shared multi-workspace mode. The single process-level manager
	// cannot serve multiple workspaces correctly, so the handler
	// must refuse rather than silently leak across tenants.
	resolver := &fakeWorkspaceResolver{config: &workspace.Config{ID: "acme", RepoPath: "/r/spine"}}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: st, Auth: authSvc, RepositoryManager: mgr,
		WorkspaceResolver: resolver,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doRepoRequest(t, "GET", ts.URL+"/api/v1/repositories", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 in shared mode, got %d", resp.StatusCode)
	}
}

func TestRepositoryWithoutManagerReturns503(t *testing.T) {
	st := newSkillStore()
	st.actors["operator-1"] = &domain.Actor{
		ActorID: "operator-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(st)
	tok, _, err := authSvc.CreateToken(context.Background(), "operator-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: st, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doRepoRequest(t, "GET", ts.URL+"/api/v1/repositories", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when manager unconfigured, got %d", resp.StatusCode)
	}
}
