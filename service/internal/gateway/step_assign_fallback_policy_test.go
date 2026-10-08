package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
)

// countingAssignStore records step-execution writes so the test can prove
// the engine-less fallback never bound anything.
type countingAssignStore struct {
	*fakeStore
	stepWrites int
}

func (c *countingAssignStore) UpdateStepExecution(ctx context.Context, e *domain.StepExecution) error {
	c.stepWrites++
	return c.fakeStore.UpdateStepExecution(ctx, e)
}

// INIT-023 TASK-007 AC 18: with a step-entry policy installed, `spine
// serve` disables the gateway's engine-less store binder (it performs none
// of the policy's checks). With no orchestrator assigner wired the assign
// route then answers 503 and writes nothing; with the default (false) the
// fallback still binds, exactly as before.
func TestStepAssign_DisabledFallbackAnswers503(t *testing.T) {
	for _, tc := range []struct {
		name       string
		disable    bool
		wantStatus int
		wantWrites int
	}{
		{name: "fallback disabled", disable: true, wantStatus: http.StatusServiceUnavailable, wantWrites: 0},
		{name: "fallback enabled (default)", disable: false, wantStatus: http.StatusOK, wantWrites: 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fs := &countingAssignStore{fakeStore: newFakeStore()}
			fs.actors["admin-1"] = &domain.Actor{
				ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
				Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
			}
			authSvc := auth.NewService(fs)
			token, _, err := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, DisableStepAssignFallback: tc.disable})
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-123/steps/step1/assign", strings.NewReader(`{"actor_id":"admin-1"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if fs.stepWrites != tc.wantWrites {
				t.Fatalf("UpdateStepExecution calls = %d, want %d", fs.stepWrites, tc.wantWrites)
			}
		})
	}
}
