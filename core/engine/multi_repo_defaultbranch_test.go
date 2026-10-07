package engine

// INIT-023 TASK-002, audit P3-36 (dormant): a code repository's
// default_branch reaches `git branch <run> <base>`, `git switch`, `git
// push` and RefSHA. The audit PoC gap3-multi-repo-code-repo showed
// "--force" force-pushing over a collaborator's commit and
// "--pathspec-from-file=<host file>" echoing the file into
// RepositoryMergeOutcome.FailureDetail. These tests pin the engine guard:
// a non-branch default_branch is a precondition failure with zero git
// calls, no clone, and no persisted run or outcome row.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

// countingRepoClients counts RepositoryGitClients.Client calls (each one
// is a clone or a pool checkout in production) before delegating.
type countingRepoClients struct {
	inner RepositoryGitClients
	calls int
}

func (c *countingRepoClients) Client(ctx context.Context, repoID string) (git.GitClient, error) {
	c.calls++
	return c.inner.Client(ctx, repoID)
}

var invalidDefaultBranches = []string{
	"--force",
	"--pathspec-from-file=/etc/hosts",
	"HEAD",
	"a..b",
	"refs/tags/v1",
	"x y",
	"0123456789abcdef0123456789abcdef01234567",
	// default_branch is the SHORT name (review round 1): a refs/heads/
	// form is refused, not stored verbatim and echoed as TargetBranch.
	"refs/heads/develop",
}

// TestDefaultBranchGuard_StartRunRefusesBeforeAnyCodeRepoGit is AC 11
// (run start): StartRun returns ErrPrecondition, the code-repo client
// factory is never invoked (no clone), the recording client sees no
// call, no run row is persisted, and the primary run branch that
// createRunBranches cut and pushed before reaching the code repo is
// rolled back locally and on origin.
func TestDefaultBranchGuard_StartRunRefusesBeforeAnyCodeRepoGit(t *testing.T) {
	for _, bad := range invalidDefaultBranches {
		t.Run(bad, func(t *testing.T) {
			testutil.HermeticGit(t)
			t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
			primary := testutil.NewTempRepo(t)
			origin := filepath.Join(t.TempDir(), "origin.git")
			rsGit(t, primary, "init", "-q", "--bare", origin)
			rsGit(t, primary, "remote", "add", "origin", origin)
			rsGit(t, primary, "push", "-q", "origin", "refs/heads/main:refs/heads/main")

			art := taskWithRepos([]string{"code-a"})
			resolver := newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", bad)})
			stubs := newStubRepoGitClients("code-a")
			clients := &countingRepoClients{inner: stubs}
			orch, st := multiRepoOrchestrator(t, art, resolver, clients, git.NewCLIClient(primary))

			_, err := orch.StartRun(context.Background(), art.Path)
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
				t.Fatalf("StartRun = %v, want ErrPrecondition", err)
			}
			if !strings.Contains(err.Error(), "default_branch is not a valid branch name") {
				t.Fatalf("StartRun error %q does not name the invalid default_branch", err)
			}
			if clients.calls != 0 {
				t.Errorf("the code-repo client factory was invoked %d times; no clone may be attempted", clients.calls)
			}
			s := stubs.clients["code-a"]
			if len(s.createCalls)+len(s.deleteCalls)+len(s.pushCalls)+len(s.deleteRemoteCalls) != 0 {
				t.Errorf("code-repo client recorded calls: %+v", s)
			}
			if st.createdRun != nil {
				t.Errorf("a run row was persisted: %+v", st.createdRun)
			}
			if refs := rsGit(t, primary, "for-each-ref", "refs/heads/spine"); refs != "" {
				t.Errorf("primary run branch left behind locally: %q", refs)
			}
			if refs := rsGit(t, origin, "for-each-ref", "refs/heads/spine"); refs != "" {
				t.Errorf("primary run branch left behind on origin: %q", refs)
			}
		})
	}
}

// TestDefaultBranchGuard_MergeRefusesBeforeAnyCodeRepoGit is AC 11 (code
// repository merge): a default_branch changed after run start to
// "--pathspec-from-file=<host file>" or "--force" makes
// attemptCodeRepositoryMerge return ErrPrecondition with no client, no
// Merge, no Push and no outcome row, and the file's contents appear in
// no error or log line.
func TestDefaultBranchGuard_MergeRefusesBeforeAnyCodeRepoGit(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.env")
	const secretValue = "SERVICE_TOKEN=sk-live-0a1b2c3d"
	if err := os.WriteFile(secret, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, bad := range []string{"--pathspec-from-file=" + secret, "--force"} {
		t.Run(bad, func(t *testing.T) {
			run := committingRun("run-0a1b2c3d", "spine/run/task-001-x-0a1b2c3d")
			run.AffectedRepositories = []string{domain.PrimaryRepositoryID, "code-a"}
			store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
			tracker := &trackingCodeRepoClient{repoID: "code-a"}
			clients := &countingRepoClients{inner: &trackingRepoGitClients{clients: map[string]*trackingCodeRepoClient{"code-a": tracker}}}
			orch := &Orchestrator{store: store, git: &stubGitOperator{}, events: &mockEventEmitter{}, wfLoader: &stubWorkflowLoader{}}
			orch.WithRepositoryResolver(newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", bad)}))
			orch.WithRepositoryGitClients(clients)

			_, err := orch.attemptCodeRepositoryMerge(context.Background(), run, "code-a")
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
				t.Fatalf("attemptCodeRepositoryMerge = %v, want ErrPrecondition", err)
			}
			if err := orch.mergeAffectedCodeRepositories(context.Background(), run); err == nil {
				t.Fatal("mergeAffectedCodeRepositories accepted an invalid default_branch")
			} else if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("error leaks the host file: %v", err)
			}
			if clients.calls != 0 || len(tracker.mergeCalls) != 0 || len(tracker.pushCalls) != 0 {
				t.Errorf("git work started: client calls=%d merges=%d pushes=%d", clients.calls, len(tracker.mergeCalls), len(tracker.pushCalls))
			}
			if len(store.mergeOutcomes) != 0 {
				t.Errorf("an outcome row was written: %+v", store.mergeOutcomes)
			}
		})
	}
	if strings.Contains(logs.String(), secretValue) {
		t.Fatalf("a log line leaks the host file:\n%s", logs.String())
	}
}

// TestDefaultBranchGuard_ValidBranchesStillMerge keeps the guard honest:
// ordinary default branches still reach the client.
func TestDefaultBranchGuard_ValidBranchesStillMerge(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	for _, good := range []string{"main", "trunk", "release/2026.10"} {
		run := committingRun("run-0a1b2c3d", "spine/run/task-001-x-0a1b2c3d")
		tracker := &trackingCodeRepoClient{repoID: "code-a"}
		orch := &Orchestrator{store: &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}, git: &stubGitOperator{}, events: &mockEventEmitter{}, wfLoader: &stubWorkflowLoader{}}
		orch.WithRepositoryResolver(newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", good)}))
		orch.WithRepositoryGitClients(&trackingRepoGitClients{clients: map[string]*trackingCodeRepoClient{"code-a": tracker}})
		outcome, err := orch.attemptCodeRepositoryMerge(context.Background(), run, "code-a")
		if err != nil || outcome.Status != domain.RepositoryMergeStatusMerged {
			t.Errorf("default_branch %q: outcome %+v, err %v; want merged", good, outcome, err)
		}
		if len(tracker.mergeCalls) != 1 || tracker.mergeCalls[0].Target != good {
			t.Errorf("default_branch %q: merge calls %+v", good, tracker.mergeCalls)
		}
	}
}

// TestGenerateBranchName_PassesTheBranchGrammar is AC 12's last item:
// every branch name the engine generates is a valid branch name under
// the converged grammar, so run branches keep working.
func TestGenerateBranchName_PassesTheBranchGrammar(t *testing.T) {
	type in struct {
		mode            domain.RunMode
		id, path, runID string
	}
	for _, c := range []in{
		{domain.RunModeStandard, "TASK-003", "initiatives/init-001/epics/epic-001/tasks/task-003-git-push.md", "run-0a5d0f6d"},
		{domain.RunModePlanning, "INIT-001", "initiatives/init-001/initiative.md", "run-abcd1234"},
		{domain.RunModeStandard, "TASK-001", "initiatives/init-001/epics/EPIC-009/tasks/TASK-001-credential-schema-and-storage.md", "run-4390cc54"},
		{domain.RunModeStandard, "EPIC-001", "initiatives/init-001/epics/epic-001/epic.md", "run-abcd1234"},
		{domain.RunModeStandard, "TASK-099", "initiatives/init-001/epics/epic-001/tasks/task-099-this-is-a-very-long-task-name-that-exceeds-the-maximum-allowed-length.md", "run-abcd1234"},
		{domain.RunModePlanning, "workflow-lifecycle", "workflows/workflow-lifecycle.yaml", "run-0a5d0f6d"},
		{domain.RunModeStandard, "", "governance/charter.md", "run-abcd1234"},
		{domain.RunModeStandard, "TASK-001", "tasks/.hidden.lock.md", "run-0a1b2c3d"},
		{domain.RunModeStandard, "TASK-001", "tasks/Weird Name (v2).md", "custom-id"},
	} {
		for _, name := range []string{
			generateBranchName(c.mode, c.id, c.path, c.runID),
			generateBranchNameWithSuffix(c.mode, c.id, c.path, c.runID),
		} {
			if err := refname.Validate(name); err != nil {
				t.Errorf("generated branch %q fails refname.Validate: %v", name, err)
			}
			if err := git.ValidateBranchName(name); err != nil {
				t.Errorf("generated branch %q fails git.ValidateBranchName: %v", name, err)
			}
		}
	}
}
