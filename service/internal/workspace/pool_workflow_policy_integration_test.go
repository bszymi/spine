//go:build integration

package workspace

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-014 AC 12: with a store, the pooled workflow service reads
// the projection-backed rules (migration 018 seeds the bootstrap
// `main: no-delete, no-direct-write` row), so a reviewer's non-bypass write
// to main is refused at the service.
func TestBuildServiceSet_WorkflowServiceHasProjectionPolicy(t *testing.T) {
	if os.Getenv("SPINE_TEST_DATABASE_URL") == "" {
		t.Skip("SPINE_TEST_DATABASE_URL not set")
	}
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	// buildServiceSet does not migrate; NewTestStore applies the schema
	// (and the 018 bootstrap branch-protection seed) first.
	_ = store.NewTestStore(t)
	dsn := store.TestDSN()
	cfg := Config{ID: "ws-wfpolicy-db", RepoPath: testutil.NewTempRepo(t), DatabaseURL: secrets.NewSecretValue([]byte(dsn))}
	ss, err := buildServiceSet(context.Background(), cfg, nil, nil, "", nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")
	rules, err := ss.Store.ListBranchProtectionRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mainProtected := false
	for _, r := range rules {
		if r.BranchPattern == "main" {
			mainProtected = true
		}
	}
	if !mainProtected {
		t.Skip("test database has no rule for main; the projection-backed policy has nothing to deny")
	}

	ctx := observe.WithActorID(context.Background(), "rev")
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "rev", Role: domain.RoleReviewer, Status: domain.ActorStatusActive})
	_, err = ss.Workflows.Create(ctx, "probe-flow", policyProbeWorkflow)
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
		t.Fatalf("reviewer non-bypass write to main: %v, want forbidden", err)
	}
}
