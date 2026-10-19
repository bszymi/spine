//go:build integration

package workspace

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-017 AC 13: with a store, the pooled validator promotes the
// default blocking status conflicts.
func TestBuildServiceSet_ValidatorBlocksStatusConflicts(t *testing.T) {
	if os.Getenv("SPINE_TEST_DATABASE_URL") == "" {
		t.Skip("SPINE_TEST_DATABASE_URL not set")
	}
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	_ = store.NewTestStore(t) // applies the schema
	cfg := Config{ID: "ws-lifecycle-db", RepoPath: testutil.NewTempRepo(t), DatabaseURL: secrets.NewSecretValue([]byte(store.TestDSN()))}
	ss, err := buildServiceSet(context.Background(), cfg, nil, nil, "", nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")
	if ss.Validator == nil {
		t.Fatal("no validator with a store")
	}
	if got := ss.Validator.BlockingStatusConflicts(); !reflect.DeepEqual(got, validation.DefaultBlockingStatusConflicts) {
		t.Fatalf("pooled validator blocking set = %v, want %v", got, validation.DefaultBlockingStatusConflicts)
	}
}
