package gateway_test

// INIT-023 TASK-006 AC 23: the governance-decision and governance-status
// routes — authorization, the engine call and every status the decision
// route maps (400, 403, 409, 412, 503).

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

// fakeGovernanceDecider is a RunMergeResolver that also decides
// governance merges (the shape *engine.Orchestrator has).
type fakeGovernanceDecider struct {
	fakeRunMergeResolver
	calls     []engine.GovernanceDecisionRequest
	decideErr error
	statusErr error
}

func (f *fakeGovernanceDecider) DecideGovernanceMerge(_ context.Context, req engine.GovernanceDecisionRequest) (*engine.GovernanceDecisionResult, error) {
	f.calls = append(f.calls, req)
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	return &engine.GovernanceDecisionResult{RunID: req.RunID, CommitSHA: req.CommitSHA, Decision: req.Decision,
		Paths: []string{".spine/branch-protection.yaml"}, PathCount: 1, MergeAttempted: true, RunStatus: domain.RunStatusCompleted}, nil
}

func (f *fakeGovernanceDecider) GovernanceMergeStatus(_ context.Context, runID string) (*engine.GovernanceMergeStatus, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return &engine.GovernanceMergeStatus{RunID: runID, RunStatus: domain.RunStatusCommitting, Evaluated: true,
		BoundSHA: strings.Repeat("a", 40), Required: true, Held: true, Paths: []string{"workflows/task-default.yaml"}, PathCount: 1,
		Approval: &engine.GovernanceApprovalRecord{SHA: strings.Repeat("b", 40), ActorID: "op-0", ActorRole: "operator"}}, nil
}

func governanceServer(t *testing.T, resolver gateway.RunMergeResolver, role domain.ActorRole) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{ActorID: "actor-1", Role: role, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	token, _, err := authSvc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, RunMergeResolver: resolver})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, token
}

func govDo(t *testing.T, ts *httptest.Server, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const govBody = `{"decision":"approve","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reason":"ok"}`

func TestRunGovernanceDecision_Authorization(t *testing.T) {
	for _, c := range []struct {
		role domain.ActorRole
		want int
	}{
		{domain.RoleContributor, http.StatusForbidden},
		{domain.RoleReviewer, http.StatusForbidden},
		{domain.RoleOperator, http.StatusOK},
		{domain.RoleAdmin, http.StatusOK},
	} {
		d := &fakeGovernanceDecider{}
		ts, token := governanceServer(t, d, c.role)
		code, body := govDo(t, ts, token, "POST", "/api/v1/runs/run-1/governance-decision", govBody)
		if code != c.want {
			t.Fatalf("%s: status %d, want %d (%v)", c.role, code, c.want, body)
		}
		if c.want == http.StatusForbidden {
			if len(d.calls) != 0 {
				t.Fatalf("%s: the engine was called", c.role)
			}
			continue
		}
		if len(d.calls) != 1 {
			t.Fatalf("%s: engine calls = %d", c.role, len(d.calls))
		}
		got := d.calls[0]
		if got.RunID != "run-1" || got.Decision != engine.GovernanceApprove || got.Actor.ActorID != "actor-1" ||
			got.Actor.Role != c.role || got.Reason != "ok" || got.CommitSHA != strings.Repeat("a", 40) {
			t.Fatalf("%s: engine request = %+v", c.role, got)
		}
		if body["run_status"] != "completed" || body["merge_attempted"] != true || body["path_count"] != float64(1) {
			t.Fatalf("%s: response = %v", c.role, body)
		}
	}
}

func TestRunGovernanceDecision_StatusMapping(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		body string
		want int
	}{
		{"malformed body", nil, `{"decision":`, http.StatusBadRequest},
		{"invalid params", domain.NewError(domain.ErrInvalidParams, "commit_sha must be a full hex commit id"), govBody, http.StatusBadRequest},
		{"not committing", domain.NewError(domain.ErrConflict, "run is active"), govBody, http.StatusConflict},
		{"other commit", domain.NewError(domain.ErrConflict, "merge is bound to bbbb"), govBody, http.StatusConflict},
		{"no governance change", domain.NewError(domain.ErrPrecondition, "run does not require governance approval"), govBody, http.StatusPreconditionFailed},
		{"guard off", domain.NewError(domain.ErrUnavailable, "governance guard is not enabled"), govBody, http.StatusServiceUnavailable},
		{"unknown run", domain.NewError(domain.ErrNotFound, "run not found"), govBody, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &fakeGovernanceDecider{decideErr: c.err}
			ts, token := governanceServer(t, d, domain.RoleOperator)
			if code, body := govDo(t, ts, token, "POST", "/api/v1/runs/run-1/governance-decision", c.body); code != c.want {
				t.Fatalf("status %d, want %d (%v)", code, c.want, body)
			}
		})
	}
	t.Run("resolver without the capability", func(t *testing.T) {
		ts, token := governanceServer(t, &fakeRunMergeResolver{}, domain.RoleOperator)
		if code, _ := govDo(t, ts, token, "POST", "/api/v1/runs/run-1/governance-decision", govBody); code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503", code)
		}
		if code, _ := govDo(t, ts, token, "GET", "/api/v1/runs/run-1/governance", ""); code != http.StatusServiceUnavailable {
			t.Fatalf("status GET %d, want 503", code)
		}
	})
	t.Run("no resolver", func(t *testing.T) {
		ts, token := governanceServer(t, nil, domain.RoleOperator)
		if code, _ := govDo(t, ts, token, "POST", "/api/v1/runs/run-1/governance-decision", govBody); code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503", code)
		}
	})
}

func TestRunGovernanceStatus(t *testing.T) {
	d := &fakeGovernanceDecider{}
	ts, token := governanceServer(t, d, domain.RoleReader)
	code, body := govDo(t, ts, token, "GET", "/api/v1/runs/run-1/governance", "")
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	if body["held"] != true || body["bound_sha"] != strings.Repeat("a", 40) || body["path_count"] != float64(1) {
		t.Fatalf("response = %v", body)
	}
	appr, _ := body["approval"].(map[string]any)
	if appr["sha"] != strings.Repeat("b", 40) || appr["actor_id"] != "op-0" {
		t.Fatalf("approval = %v", body["approval"])
	}
	d.statusErr = domain.NewError(domain.ErrNotFound, "run not found")
	if code, _ := govDo(t, ts, token, "GET", "/api/v1/runs/run-x/governance", ""); code != http.StatusNotFound {
		t.Fatalf("unknown run: status %d", code)
	}
}
