package engine

// INIT-023 TASK-014 review round 1 (AC 16 gap): the workflow starters'
// existence pre-checks driven by a real *git.CLIClient and a real
// *workflow.Service writer instead of git doubles. The design-check probe
// TestT014Check_CreateExistingWorkflow_LeavesPendingRun models "the id
// exists" only through the writer's error, so on the task branch it reaches
// the writer-failure path (run left failed); the unit test of the pre-check
// (TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun) uses a
// double that serves the file. This test shows the property AC 16 states —
// a reviewer create of an existing id leaves no run record behind — with
// the git the engine really runs, and that real git's not-found answer is
// the one the pre-checks treat as "absent" (a create of a new id proceeds,
// an update of a missing id is 404), so the pre-check neither refuses every
// create nor lets an existing id through.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/testutil"
)

func realGitWorkflowRepo(t *testing.T) (string, *git.CLIClient) {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "workflows/new-workflow.yaml", validWorkflowBody)
	rsGit(t, repo, "add", ".")
	rsGit(t, repo, "commit", "-qm", "seed new-workflow")
	return repo, git.NewCLIClient(repo)
}

func realGitWorkflowOrchestrator(repo string, client *git.CLIClient, st *mockRunStore) *Orchestrator {
	return &Orchestrator{
		workflows:      workflowPlanningResolver(),
		store:          st,
		actors:         &stubActorAssigner{},
		artifacts:      &mockArtifactReader{},
		events:         &mockEventEmitter{},
		git:            client,
		wfLoader:       &stubWorkflowLoader{},
		workflowWriter: workflow.NewService(client, repo),
	}
}

func TestWorkflowStarters_RealGit_PreChecks(t *testing.T) {
	ctx := context.Background()

	t.Run("create of an existing id leaves no run, branch or commit", func(t *testing.T) {
		repo, client := realGitWorkflowRepo(t)
		st := &mockRunStore{}
		orch := realGitWorkflowOrchestrator(repo, client, st)
		refsBefore := rsGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)")

		_, err := orch.StartWorkflowPlanningRun(ctx, "new-workflow", validWorkflowBody)
		if spineErrCode(err) != domain.ErrAlreadyExists {
			t.Fatalf("create of an existing id: %v, want already_exists", err)
		}
		if st.createdRun != nil || len(st.runs) != 0 {
			t.Fatalf("a run record was left behind: %+v", st.createdRun)
		}
		if refsAfter := rsGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)"); refsAfter != refsBefore {
			t.Fatalf("refs changed:\nbefore %s\nafter  %s", refsBefore, refsAfter)
		}
	})

	t.Run("create of a new id proceeds: real git's not-found is absent", func(t *testing.T) {
		repo, client := realGitWorkflowRepo(t)
		st := &mockRunStore{}
		orch := realGitWorkflowOrchestrator(repo, client, st)
		body := strings.Replace(validWorkflowBody, "id: new-workflow", "id: other-workflow", 1)

		res, err := orch.StartWorkflowPlanningRun(ctx, "other-workflow", body)
		if err != nil {
			t.Fatalf("create of a new id: %v", err)
		}
		if st.createdRun == nil || res.Run.TaskPath != "workflows/other-workflow.yaml" {
			t.Fatalf("no run for the new id: %+v", st.createdRun)
		}
		if got := rsGit(t, repo, "show", res.Run.BranchName+":workflows/other-workflow.yaml"); !strings.Contains(got, "id: other-workflow") {
			t.Fatalf("run branch %s lacks the new workflow: %q", res.Run.BranchName, got)
		}
		if _, err := client.ReadFile(ctx, "main", "workflows/other-workflow.yaml"); err == nil {
			t.Fatal("main must not carry the new workflow before review")
		}
	})

	t.Run("update of a missing id is 404 with no run", func(t *testing.T) {
		repo, client := realGitWorkflowRepo(t)
		st := &mockRunStore{}
		orch := realGitWorkflowOrchestrator(repo, client, st)
		body := strings.Replace(validWorkflowBody, "id: new-workflow", "id: absent-workflow", 1)

		_, err := orch.StartWorkflowUpdatePlanningRun(ctx, "absent-workflow", body)
		if spineErrCode(err) != domain.ErrNotFound {
			t.Fatalf("update of a missing id: %v, want not_found", err)
		}
		if st.createdRun != nil {
			t.Fatalf("a run record was left behind: %+v", st.createdRun)
		}
	})
}
