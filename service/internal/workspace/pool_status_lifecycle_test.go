package workspace

// INIT-023 TASK-017 AC 13, pooled half: a workspace ServiceSet's workflow
// service refuses an undeclared commit.status, and its artifact service
// carries the repository's artifacts_dir (which the per-workspace
// orchestrator builder hands to the governed merge).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/testutil"
)

const lifecycleProbeWorkflow = `id: foreign-status
name: Foreign
version: "1.0"
status: Active
description: commit.status that is not a Task status
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: accept
        name: Accept
        next_step: end
        commit:
          status: Accepted
`

func TestBuildServiceSet_StatusLifecycle_NoStore(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo := testutil.NewTempRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".spine.yaml"), []byte("artifacts_dir: spine\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
	ss, err := buildServiceSet(context.Background(), Config{ID: "ws-lifecycle", RepoPath: repo}, nil, nil, "", nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")
	if res := ss.Workflows.ValidateBody(context.Background(), "foreign-status", lifecycleProbeWorkflow); res.Status != "failed" {
		t.Fatalf("pooled workflow ValidateBody = %s %v, want failed (commit_status)", res.Status, res.Errors)
	}
	if got := ss.Artifacts.ArtifactsDir(); got != "spine" {
		t.Fatalf("artifact service artifacts dir = %q, want spine", got)
	}
}
