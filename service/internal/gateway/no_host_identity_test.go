package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/testutil"
)

// TestNoHostIdentity_GatewayWritesCommitAsSpine (INIT-024 TASK-010,
// GitHub CI after the push): the gateway's write paths on a repository
// with no identity of its own — an authoritative repository an operator
// mounts, a workspace cloned from an existing Spine repository — on a
// host with none (Spine's standalone image, a GitHub runner). Both
// failed 500 git_error "Committer identity unknown" (CI run 41856447149:
// TestF7_OperatorUpdate_KeepsBypassTrailer, TestP36_Assignee_CanWriteOwnRunBranch).
// Now they commit: the actor is the author, Spine the committer.
func TestNoHostIdentity_GatewayWritesCommitAsSpine(t *testing.T) {
	t.Run("operator workflow update (bypass) onto main", func(t *testing.T) {
		testutil.NoGitIdentity(t)
		repo, bodies := seedWorkflowRepo(t, "task-default")
		fs := newFakeStore()
		secActor(fs, "op-1", domain.RoleOperator)
		authSvc := auth.NewService(fs)
		wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
		ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc, WFPlanningStarter: &secPlanner{}}).Handler())
		defer ts.Close()

		body := strings.Replace(bodies["task-default"], `version: "1.0"`, `version: "1.1"`, 1)
		if code, out := secDo(t, "PUT", ts.URL+"/api/v1/workflows/task-default", secToken(t, authSvc, "op-1"), map[string]string{"body": body}); code != http.StatusOK {
			t.Fatalf("operator PUT with no host identity: %d %s, want 200", code, out)
		}
		if got := secGit(t, repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>", "main"); got != "op-1 <op-1@spine.local>|Spine <spine@spine.local>" {
			t.Fatalf("author|committer = %q, want the operator and Spine", got)
		}
	})
	t.Run("assignee artifact write onto the run branch", func(t *testing.T) {
		testutil.NoGitIdentity(t)
		t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
		repo, _ := seedRunRepo(t)
		_, authSvc, ts := secRunServer(t, repo, "execute", []domain.StepExecution{
			{ExecutionID: "e-exec", StepID: "execute", Status: domain.StepStatusInProgress, ActorID: "alice", Attempt: 1}})
		code, out := secDo(t, "POST", ts.URL+"/api/v1/artifacts", secToken(t, authSvc, "alice"), map[string]any{
			"path": "governance/notes.md", "content": strings.Replace(secCharter, "Charter", "Notes", 2),
			"write_context": map[string]string{"run_id": "run-alice", "task_path": secTask}})
		if code != http.StatusCreated {
			t.Fatalf("assignee write with no host identity: %d %s, want 201", code, out)
		}
		if got := secGit(t, repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>", secBranch); got != "alice <alice@spine.local>|Spine <spine@spine.local>" {
			t.Fatalf("author|committer = %q, want the assignee and Spine", got)
		}
	})
}
