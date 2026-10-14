package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/internal/gateway"
)

type fakeRunMergeResolver struct {
	resolveCalls  []resolverCall
	retryCalls    []resolverCall
	resolveErr    error
	retryErr      error
	resolveResult *engine.MergeRecoveryResult
	retryResult   *engine.MergeRecoveryResult
}

type resolverCall struct {
	runID, repoID, reason, targetSHA string
}

func (f *fakeRunMergeResolver) ResolveRepositoryMergeExternally(_ context.Context, runID, repositoryID, reason, targetCommitSHA string) (*engine.MergeRecoveryResult, error) {
	f.resolveCalls = append(f.resolveCalls, resolverCall{runID, repositoryID, reason, targetCommitSHA})
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.resolveResult != nil {
		return f.resolveResult, nil
	}
	return &engine.MergeRecoveryResult{LedgerCommitSHA: "ledger-test-sha", ReadyToResume: true}, nil
}

func (f *fakeRunMergeResolver) RetryRepositoryMerge(_ context.Context, runID, repositoryID, reason string) (*engine.MergeRecoveryResult, error) {
	f.retryCalls = append(f.retryCalls, resolverCall{runID, repositoryID, reason, ""})
	if f.retryErr != nil {
		return nil, f.retryErr
	}
	if f.retryResult != nil {
		return f.retryResult, nil
	}
	return &engine.MergeRecoveryResult{LedgerCommitSHA: "ledger-test-sha", ReadyToResume: true}, nil
}

func setupResolverServer(t *testing.T, resolver gateway.RunMergeResolver, role domain.ActorRole) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Role: role, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, err := authSvc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:            fs,
		Auth:             authSvc,
		RunMergeResolver: resolver,
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleRunRepositoryResolve_Success(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"manual force-merge","target_commit_sha":"abc123def456"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/resolve", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(resolver.resolveCalls) != 1 {
		t.Fatalf("expected 1 resolve call, got %d", len(resolver.resolveCalls))
	}
	got := resolver.resolveCalls[0]
	if got.runID != "run-1" || got.repoID != "payments-service" || got.reason != "manual force-merge" || got.targetSHA != "abc123def456" {
		t.Fatalf("unexpected resolver call: %+v", got)
	}
}

func TestHandleRunRepositoryResolve_RequiresReason(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"   "}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/resolve", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace reason, got %d", resp.StatusCode)
	}
	if len(resolver.resolveCalls) != 0 {
		t.Fatalf("resolver should not be called when reason is empty")
	}
}

func TestHandleRunRepositoryResolve_ForbiddenForReader(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleReader)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"reason"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/resolve", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for reader role, got %d", resp.StatusCode)
	}
}

func TestHandleRunRepositoryResolve_PropagatesEngineError(t *testing.T) {
	resolver := &fakeRunMergeResolver{
		resolveErr: domain.NewError(domain.ErrConflict, "already merged"),
	}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"reason"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/resolve", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestHandleRunRepositoryResolve_NoResolverConfigured(t *testing.T) {
	ts, token := setupResolverServer(t, nil, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"reason"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/resolve", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when resolver missing, got %d", resp.StatusCode)
	}
}

func TestHandleRunRepositoryRetry_Success(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"transient flake retest"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/retry", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(resolver.retryCalls) != 1 {
		t.Fatalf("expected 1 retry call, got %d", len(resolver.retryCalls))
	}
	got := resolver.retryCalls[0]
	if got.runID != "run-1" || got.repoID != "payments-service" || got.reason != "transient flake retest" {
		t.Fatalf("unexpected retry call: %+v", got)
	}
}

func TestHandleRunRepositoryRetry_ForbiddenForReader(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleReader)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"reason"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/retry", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandleRunRepositoryRetry_SurfacesBlockingRepositories(t *testing.T) {
	resolver := &fakeRunMergeResolver{
		retryResult: &engine.MergeRecoveryResult{
			LedgerCommitSHA:      "ledger-zzz",
			ReadyToResume:        false,
			BlockingRepositories: []string{"api-gateway", "auth-service"},
		},
	}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{"reason":"transient flake"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/retry", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got struct {
		LedgerCommitSHA      string   `json:"ledger_commit_sha"`
		ReadyToResume        bool     `json:"ready_to_resume"`
		BlockingRepositories []string `json:"blocking_repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.LedgerCommitSHA != "ledger-zzz" {
		t.Fatalf("expected ledger_commit_sha=ledger-zzz, got %q", got.LedgerCommitSHA)
	}
	if got.ReadyToResume {
		t.Fatalf("expected ready_to_resume=false")
	}
	if len(got.BlockingRepositories) != 2 || got.BlockingRepositories[0] != "api-gateway" || got.BlockingRepositories[1] != "auth-service" {
		t.Fatalf("unexpected blocking_repositories: %+v", got.BlockingRepositories)
	}
}

func TestHandleRunRepositoryRetry_RequiresReason(t *testing.T) {
	resolver := &fakeRunMergeResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()

	body := strings.NewReader(`{}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/retry", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if len(resolver.retryCalls) != 0 {
		t.Fatalf("resolver should not be called when reason missing")
	}
}

// headRetryResolver adds the optional head_sha capability.
type headRetryResolver struct {
	fakeRunMergeResolver
	headCalls []resolverCall
}

func (h *headRetryResolver) RetryRepositoryMergeAt(_ context.Context, runID, repositoryID, reason, headSHA string) (*engine.MergeRecoveryResult, error) {
	h.headCalls = append(h.headCalls, resolverCall{runID, repositoryID, reason, headSHA})
	return &engine.MergeRecoveryResult{LedgerCommitSHA: "ledger-test-sha", ReadyToResume: true}, nil
}

// TestHandleRunRepositoryRetry_HeadSHA is INIT-023 TASK-005 DC-7: a
// retry that names head_sha reaches RetryRepositoryMergeAt; a resolver
// without that capability answers 503 instead of ignoring it; a retry
// without head_sha keeps the existing call.
func TestHandleRunRepositoryRetry_HeadSHA(t *testing.T) {
	head := strings.Repeat("cd", 20)
	post := func(t *testing.T, ts *httptest.Server, token, body string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/repositories/payments-service/retry", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	resolver := &headRetryResolver{}
	ts, token := setupResolverServer(t, resolver, domain.RoleOperator)
	defer ts.Close()
	if code := post(t, ts, token, `{"reason":"fixed","head_sha":"`+head+`"}`); code != http.StatusOK {
		t.Fatalf("retry with head_sha: %d", code)
	}
	if len(resolver.headCalls) != 1 || resolver.headCalls[0].targetSHA != head || len(resolver.retryCalls) != 0 {
		t.Fatalf("head calls %+v, plain calls %+v", resolver.headCalls, resolver.retryCalls)
	}
	if code := post(t, ts, token, `{"reason":"again"}`); code != http.StatusOK || len(resolver.retryCalls) != 1 {
		t.Fatalf("retry without head_sha: %d, plain calls %d", code, len(resolver.retryCalls))
	}

	plain := &fakeRunMergeResolver{}
	ts2, token2 := setupResolverServer(t, plain, domain.RoleOperator)
	defer ts2.Close()
	if code := post(t, ts2, token2, `{"reason":"fixed","head_sha":"`+head+`"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("head_sha against a resolver without the capability: %d, want 503", code)
	}
	if len(plain.retryCalls) != 0 {
		t.Fatal("head_sha was silently dropped into a plain retry")
	}
}
