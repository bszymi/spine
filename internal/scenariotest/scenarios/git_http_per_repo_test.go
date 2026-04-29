//go:build scenario

package scenarios_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/repository"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/workspace"
)

// codeRepoStubResolver implements gitpool.Resolver against a fixed
// catalog snapshot for scenario tests. The primary entry returns the
// workspace's RepoPath; code entries return the bare-clone path the
// scenario set up. Production wires this to the real
// repository.Registry; the stub keeps scenarios independent of the
// EPIC-001 catalog/binding store wiring.
type codeRepoStubResolver struct {
	primary    *repository.Repository
	codeRepos  map[string]*repository.Repository // id -> repo
	codeActive map[string]bool                   // id -> active
}

func (r *codeRepoStubResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	if id == repository.PrimaryRepositoryID {
		return r.primary, nil
	}
	repo, ok := r.codeRepos[id]
	if !ok {
		// Wrap the typed sentinel in a SpineError so the gateway's
		// WriteError path maps it to the right HTTP status. The
		// production registry returns the same shape.
		return nil, domain.NewErrorWithCause(domain.ErrNotFound,
			fmt.Sprintf("repository %q not found in catalog", id),
			repository.ErrRepositoryNotFound)
	}
	if !r.codeActive[id] {
		return nil, domain.NewErrorWithCause(domain.ErrPrecondition,
			fmt.Sprintf("repository %q binding is inactive", id),
			repository.ErrRepositoryInactive)
	}
	return repo, nil
}

func (r *codeRepoStubResolver) ListActive(_ context.Context) ([]repository.Repository, error) {
	out := []repository.Repository{*r.primary}
	for id, repo := range r.codeRepos {
		if r.codeActive[id] {
			out = append(out, *repo)
		}
	}
	return out, nil
}

// codeRepoSetup configures a scenario gateway with a code repository
// already cloned at a sibling path, exposed through gitpool with
// WithCloner so the routing path that materialises a code repo is
// exercised end-to-end.
//
// State keys set:
//   - "gw_url"           — gateway base URL
//   - "code_repo_id"     — the repository ID the scenario expects to clone
//   - "code_repo_path"   — filesystem path to the materialised code repo
type codeRepoSetup struct {
	repositoryID string
	// codeAlreadyCloned, when true, uses the workspace primary repo
	// as the "code" target so the local path already contains a real
	// .git tree (the scenario then exercises the cache-hit / no-clone
	// branch). When false, the binding points at an empty directory
	// so the first request must materialise it.
	codeAlreadyCloned bool
	// inactive marks the code repo as inactive so the registry
	// returns ErrRepositoryInactive — the scenario asserts the
	// gateway maps that to a precondition error past the auth gate.
	inactive bool
}

func setupGitHTTPServerWithCodeRepo(opts codeRepoSetup) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-git-http-server-with-code-repo",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repoPath := sc.Repo.Dir

			// Use the seeded workspace repo as the "code" target by
			// default — it's already a real Git working tree the test
			// can clone from. For the missing-clone case, point at an
			// empty sibling path: the gateway's gitpool is wired with
			// WithCloner, so a real clone would normally materialise
			// it; in scenario tests we cannot actually clone over
			// the network, so we just verify the routing reaches
			// the registry and surfaces the right error.
			codePath := repoPath
			if !opts.codeAlreadyCloned {
				codePath = filepath.Join(filepath.Dir(repoPath), "missing-code-repo")
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
					opts.repositoryID: {
						ID:        opts.repositoryID,
						Kind:      repository.KindCode,
						LocalPath: codePath,
						CloneURL:  "https://example.invalid/code.git",
						Status:    "active",
					},
				},
				codeActive: map[string]bool{
					opts.repositoryID: !opts.inactive,
				},
			}

			primaryClient := git.NewCLIClient(repoPath)
			pool, err := gitpool.New(primaryClient, poolResolver,
				gitpool.NewCLIClientFactory(),
				gitpool.WithCloner(primaryClient),
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
			sc.Set("code_repo_id", opts.repositoryID)
			sc.Set("code_repo_path", codePath)
			return nil
		},
	}
}

// TestGitHTTP_PerRepo_PrimaryFallback asserts that omitting the
// repository segment in a per-workspace URL still resolves to the
// primary — the legacy single-repo HTTP shape must keep working
// alongside the new routing introduced by EPIC-003 TASK-004.
func TestGitHTTP_PerRepo_PrimaryFallback(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-per-repo-primary-fallback",
		Description: "/git/{ws}/info/refs (no repo segment) keeps resolving to the primary",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithCodeRepo(codeRepoSetup{
				repositoryID:      "code",
				codeAlreadyCloned: true,
			}),
			enableGitHTTPExport(),
			{
				Name: "primary-info-refs-still-200",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/ws-1/info/refs?service=git-upload-pack") //nolint:gosec // G107: test URL
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						return fmt.Errorf("expected 200 for primary fallback, got %d", resp.StatusCode)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PerRepo_CodeRepoCloneSucceeds is the headline scenario:
// a `/git/{ws}/{repo_id}/...` URL routes through the registry, the
// gitpool resolves the binding to a real local clone, and a full
// `git clone` over the gateway's HTTP endpoint succeeds against the
// code repo. This is the EPIC-003 acceptance evidence that the new
// per-repo routing actually serves bytes.
func TestGitHTTP_PerRepo_CodeRepoCloneSucceeds(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-per-repo-code-clone",
		Description: "git clone of /git/{ws}/{repo}/ resolves through the registry and serves the code repo",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithCodeRepo(codeRepoSetup{
				repositoryID:      "code",
				codeAlreadyCloned: true,
			}),
			enableGitHTTPExport(),
			{
				Name: "git-clone-per-repo-succeeds",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					cloneDir := filepath.Join(sc.T.TempDir(), "code-clone")
					url := base + "/git/ws-1/code"
					cmd := exec.Command("git", "clone", url, cloneDir)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("git clone %s: %w\n%s", url, err, out)
					}
					if _, statErr := exec.Command("test", "-d", filepath.Join(cloneDir, ".git")).Output(); statErr != nil {
						return fmt.Errorf("expected .git directory in clone")
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PerRepo_UnknownRepoIs404 asserts that an unknown
// repository_id is mapped to a 404 past the auth gate. Operators
// debugging a missing binding need a distinct status from a
// workspace-level 404.
func TestGitHTTP_PerRepo_UnknownRepoIs404(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-per-repo-unknown-repo",
		Description: "/git/{ws}/{unknown}/info/refs returns 404 past the auth gate",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithCodeRepo(codeRepoSetup{
				repositoryID:      "code",
				codeAlreadyCloned: true,
			}),
			{
				Name: "unknown-repo-returns-404",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/ws-1/missing/info/refs?service=git-upload-pack") //nolint:gosec // G107: test URL
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusNotFound {
						return fmt.Errorf("expected 404 for unknown repo, got %d", resp.StatusCode)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PerRepo_InactiveRepoIs412 asserts that an inactive
// binding is rejected with the registry's typed
// ErrRepositoryInactive — surfaced as a 412 (precondition) past the
// auth gate. A 200/404 here would let an inactive code repo serve
// HTTP traffic or hide its existence from a debugging operator.
func TestGitHTTP_PerRepo_InactiveRepoIs412(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-per-repo-inactive",
		Description: "/git/{ws}/{inactive}/info/refs returns 412 past the auth gate",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithCodeRepo(codeRepoSetup{
				repositoryID:      "code",
				codeAlreadyCloned: true,
				inactive:          true,
			}),
			{
				Name: "inactive-repo-returns-412",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/ws-1/code/info/refs?service=git-upload-pack") //nolint:gosec // G107: test URL
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusPreconditionFailed {
						return fmt.Errorf("expected 412 for inactive binding, got %d", resp.StatusCode)
					}
					return nil
				},
			},
		},
	})
}
