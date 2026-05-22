//go:build scenario

package scenarios_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/repository"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/scenariotest/harness"
	"github.com/bszymi/spine/service/internal/workspace"
)

// TestRunnerCloneContext_AssignmentPayloadDrivesClone is the AC anchor
// for INIT-014 EPIC-004 TASK-005:
//
//	"Scenario test verifies a runner can clone a code repo branch from
//	an assignment."
//
// The scenario builds the same shape the engine emits in
// buildAssignmentRequest — an actor.AssignmentContext carrying
// WorkspaceID, RepositoryID, CloneURL (always the workspace's git HTTP
// endpoint per ADR-015), and BranchName — then drives `git clone
// --branch <branch_name> <clone_url>` against the workspace's git HTTP
// server. The test asserts the clone succeeds and that the resulting
// working tree is on the branch named in the payload — proving end to
// end that an assignment payload alone is sufficient context for a
// runner to materialise the run branch in the resolved repository.
func TestRunnerCloneContext_AssignmentPayloadDrivesClone(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "runner-clone-context-from-assignment",
		Description: "An AssignmentContext (clone_url + branch_name) drives a successful git clone of a code repo run branch",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupRunnerCloneScenario("payments-service", "spine/run/task-042-build-abc"),
			enableGitHTTPExport(),
			{
				Name: "runner-clones-using-assignment-payload",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					ctx := actor.AssignmentContext{
						WorkspaceID:    "ws-1",
						RepositoryID:   sc.MustGet("code_repo_id").(string),
						CloneURL:       sc.MustGet("clone_url").(string),
						BranchName:     sc.MustGet("branch_name").(string),
						CommitBaseline: sc.MustGet("baseline_sha").(string),
					}
					if ctx.CloneURL == "" {
						return fmt.Errorf("scenario produced empty CloneURL")
					}
					if ctx.BranchName == "" {
						return fmt.Errorf("scenario produced empty BranchName")
					}

					cloneDir := filepath.Join(sc.T.TempDir(), "runner-workspace")
					cmd := exec.Command("git", "clone",
						"--branch", ctx.BranchName,
						ctx.CloneURL, cloneDir,
					)
					if out, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("git clone --branch %s %s: %w\n%s",
							ctx.BranchName, ctx.CloneURL, err, out)
					}

					branchCmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--abbrev-ref", "HEAD")
					branchOut, err := branchCmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("rev-parse --abbrev-ref: %w\n%s", err, branchOut)
					}
					if got := strings.TrimSpace(string(branchOut)); got != ctx.BranchName {
						return fmt.Errorf("clone landed on branch %q, want %q", got, ctx.BranchName)
					}
					return nil
				},
			},
		},
	})
}

// setupRunnerCloneScenario stands up a workspace with a code-repo
// binding, creates the named run branch in the code repo, and exposes
// the gateway URL + the assignment fields a runner would receive. It
// reuses the codeRepoStubResolver / git HTTP wiring from
// git_http_per_repo_test.go so the scenario routes through the same
// gateway path production runners hit.
//
// State keys set:
//   - "gw_url"        — gateway base URL
//   - "code_repo_id"  — repository ID the assignment targets
//   - "branch_name"   — run branch name created in the code repo
//   - "clone_url"     — the workspace-built clone URL: <gw>/git/ws-1/{repo}
//   - "baseline_sha"  — SHA the run branch was cut from (the parent main tip)
func setupRunnerCloneScenario(repoID, branchName string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-runner-clone-scenario",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repoPath := sc.Repo.Dir

			// Create the run branch in the code repo (here: the
			// scenario's primary repo, reused as a code-repo target so
			// the clone has real bytes to fetch). In production
			// createRunBranches lands the same branch on every affected
			// repo from its default branch's tip.
			baseSHA, err := captureHead(repoPath)
			if err != nil {
				return fmt.Errorf("capture baseline: %w", err)
			}
			createCmd := exec.Command("git", "-C", repoPath, "branch", branchName, baseSHA)
			if out, err := createCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("git branch %s: %w\n%s", branchName, err, out)
			}

			wsResolver := &fakeWSResolver{
				workspaces: map[string]*workspace.Config{
					"default": {
						ID:       "default",
						RepoPath: repoPath,
						Status:   workspace.StatusActive,
					},
					"ws-1": {
						ID:       "ws-1",
						RepoPath: repoPath,
						Status:   workspace.StatusActive,
					},
				},
			}

			poolResolver := &codeRepoStubResolver{
				primary: &repository.Repository{
					ID:        repository.PrimaryRepositoryID,
					Kind:      repository.KindSpine,
					LocalPath: repoPath,
				},
				codeRepos: map[string]*repository.Repository{
					repoID: {
						ID:        repoID,
						Kind:      repository.KindCode,
						LocalPath: repoPath,
						CloneURL:  "https://example.invalid/code.git",
						Status:    "active",
					},
				},
				codeActive: map[string]bool{repoID: true},
			}

			primaryClient := git.NewCLIClient(repoPath)
			pool, err := gitpool.New(primaryClient, poolResolver,
				gitpool.NewCLIClientFactory(),
				gitpool.WithCloner(gitpool.NewCLICloner()),
			)
			if err != nil {
				return fmt.Errorf("gitpool.New: %w", err)
			}

			gitHandler, err := githttp.NewHandler(githttp.Config{
				ResolveRepoPath: func(ctx context.Context, workspaceID string) (string, error) {
					cfg, err := wsResolver.Resolve(ctx, workspaceID)
					if err != nil {
						return "", err
					}
					return cfg.RepoPath, nil
				},
				TrustedCIDRs:  []string{"127.0.0.0/8"},
				MaxConcurrent: 5,
			})
			if err != nil {
				return fmt.Errorf("githttp.NewHandler: %w", err)
			}

			authSvc := auth.NewService(sc.Runtime.Store)
			cfg := gateway.ServerConfig{
				Store:             sc.Runtime.Store,
				Auth:              authSvc,
				Artifacts:         sc.Runtime.Artifacts,
				ProjQuery:         projection.NewQueryService(sc.Runtime.Store, sc.Repo.Git),
				ProjSync:          sc.Runtime.Projections,
				WorkspaceResolver: wsResolver,
				GitHTTP:           gitHandler,
				GitPool:           pool,
				DevMode:           true,
			}
			srv := gateway.NewServer(":0", cfg)
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)

			sc.Set("gw_url", ts.URL)
			sc.Set("code_repo_id", repoID)
			sc.Set("branch_name", branchName)
			// Mirrors buildWorkspaceCloneURLBuilder's URL shape so the
			// scenario exercises the same string a production runner
			// would receive in its assignment payload.
			sc.Set("clone_url", fmt.Sprintf("%s/git/%s/%s", ts.URL, "ws-1", repoID))
			sc.Set("baseline_sha", baseSHA)
			return nil
		},
	}
}

func captureHead(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
