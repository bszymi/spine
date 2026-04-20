//go:build scenario

package scenarios_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/projection"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/workspace"
)

// fakeWSResolver resolves workspace IDs to configs for testing.
type fakeWSResolver struct {
	workspaces map[string]*workspace.Config
}

func (f *fakeWSResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	// Single-mode fallback: empty ID resolves to "default".
	if id == "" {
		if cfg, ok := f.workspaces["default"]; ok {
			return cfg, nil
		}
		return nil, workspace.ErrWorkspaceNotFound
	}
	cfg, ok := f.workspaces[id]
	if !ok {
		return nil, workspace.ErrWorkspaceNotFound
	}
	return cfg, nil
}

func (f *fakeWSResolver) List(_ context.Context) ([]workspace.Config, error) {
	var list []workspace.Config
	for _, cfg := range f.workspaces {
		list = append(list, *cfg)
	}
	return list, nil
}

// setupGitHTTPServer starts a gateway server with the git HTTP handler wired up.
// The test repo is served as the workspace's git repository.
// State keys set:
//   - "gw_url"    — base URL
//   - "gw_auth"   — *auth.Service
//   - "repo_path" — filesystem path to the test repo
func setupGitHTTPServer() scenarioEngine.Step {
	return setupGitHTTPServerWithOptions(false, false)
}

// setupGitHTTPServerWithOptions is setupGitHTTPServer with knobs that a
// handful of scenarios need (receive-pack gate, devMode for auth
// bypass). The default helper keeps the previous read-only shape.
func setupGitHTTPServerWithOptions(receivePackEnabled, devMode bool) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-git-http-server",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repoPath := sc.Repo.Dir

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

			gitHandler, err := githttp.NewHandler(githttp.Config{
				ResolveRepoPath: func(ctx context.Context, workspaceID string) (string, error) {
					cfg, err := wsResolver.Resolve(ctx, workspaceID)
					if err != nil {
						return "", err
					}
					return cfg.RepoPath, nil
				},
				TrustedCIDRs:       []string{"127.0.0.0/8"},
				MaxConcurrent:      5,
				ReceivePackEnabled: receivePackEnabled,
			})
			if err != nil {
				return fmt.Errorf("create git HTTP handler: %w", err)
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
				DevMode:           devMode,
			}

			srv := gateway.NewServer(":0", cfg)
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)

			sc.Set("gw_url", ts.URL)
			sc.Set("gw_auth", authSvc)
			sc.Set("repo_path", repoPath)
			return nil
		},
	}
}

// enableGitHTTPExport configures the test repo for HTTP export by ensuring
// git-daemon-export-ok exists and the repo is bare-enough for serving.
func enableGitHTTPExport() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "enable-git-http-export",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repoPath := sc.Repo.Dir

			// Update server info so dumb HTTP clients can discover refs.
			cmd := exec.Command("git", "update-server-info")
			cmd.Dir = repoPath
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("git update-server-info: %w\n%s", err, out)
			}
			return nil
		},
	}
}

// ── Scenarios ────────────────────────────────────────────────────────────────

// TestGitHTTP_InfoRefs verifies that the git smart HTTP endpoint serves
// ref advertisement for git-upload-pack, enabling clone/fetch.
//
// Scenario: GET /git/info/refs returns git protocol response
//
//	Given a running gateway with git HTTP handler and a seeded repo
//	When GET /git/info/refs?service=git-upload-pack is requested
//	Then the response is 200 with git smart protocol content
func TestGitHTTP_InfoRefs(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-info-refs",
		Description: "Git smart HTTP info/refs returns ref advertisement for upload-pack",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			enableGitHTTPExport(),
			{
				Name: "get-info-refs-returns-200",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/info/refs?service=git-upload-pack")
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					body, _ := io.ReadAll(resp.Body)

					if resp.StatusCode != http.StatusOK {
						return fmt.Errorf("expected 200, got %d (body: %s)", resp.StatusCode, body)
					}

					// Git smart protocol responses have Content-Type: application/x-git-upload-pack-advertisement
					ct := resp.Header.Get("Content-Type")
					if ct != "application/x-git-upload-pack-advertisement" {
						return fmt.Errorf("expected git upload-pack content-type, got %q", ct)
					}

					// Response must contain the service announcement line.
					if !bytes.Contains(body, []byte("git-upload-pack")) {
						return fmt.Errorf("response does not contain git-upload-pack announcement")
					}

					return nil
				},
			},
		},
	})
}

// TestGitHTTP_InfoRefs_WithWorkspaceID verifies workspace-scoped git access.
//
// Scenario: GET /git/ws-1/info/refs works with explicit workspace ID
//
//	Given a running gateway with workspace "ws-1" registered
//	When GET /git/ws-1/info/refs?service=git-upload-pack is requested
//	Then the response is 200
func TestGitHTTP_InfoRefs_WithWorkspaceID(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-info-refs-workspace",
		Description: "Git info/refs with explicit workspace ID resolves correctly",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			enableGitHTTPExport(),
			{
				Name: "get-info-refs-with-workspace-returns-200",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/ws-1/info/refs?service=git-upload-pack")
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					body, _ := io.ReadAll(resp.Body)

					if resp.StatusCode != http.StatusOK {
						return fmt.Errorf("expected 200, got %d (body: %s)", resp.StatusCode, body)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_InvalidWorkspace verifies 404 for unknown workspace IDs.
//
// Scenario: GET /git/nonexistent/info/refs returns 404
//
//	Given a running gateway
//	When GET /git/nonexistent/info/refs is requested
//	Then the response is 404
func TestGitHTTP_InvalidWorkspace(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-invalid-workspace",
		Description: "Git HTTP returns 404 for nonexistent workspace",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			{
				Name: "nonexistent-workspace-returns-404",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/nonexistent/info/refs?service=git-upload-pack")
					if err != nil {
						return err
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusNotFound {
						body, _ := io.ReadAll(resp.Body)
						return fmt.Errorf("expected 404, got %d (body: %s)", resp.StatusCode, body)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PushRejected verifies that push attempts are blocked.
//
// Scenario: POST /git/git-receive-pack returns 403
//
//	Given a running gateway
//	When receive-pack is advertised or posted
//	Then the response is 403 Forbidden
func TestGitHTTP_PushRejected(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-rejected",
		Description: "Git HTTP rejects push (receive-pack) operations",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			enableGitHTTPExport(),
			{
				Name: "receive-pack-info-refs-rejected",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Get(base + "/git/info/refs?service=git-receive-pack")
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					body, _ := io.ReadAll(resp.Body)

					if resp.StatusCode != http.StatusForbidden {
						return fmt.Errorf("expected 403, got %d (body: %s)", resp.StatusCode, body)
					}
					// The message must name the flag so an operator
					// hitting this in the wild can find the switch.
					if !bytes.Contains(body, []byte("SPINE_GIT_RECEIVE_PACK_ENABLED")) {
						return fmt.Errorf("rejection should mention SPINE_GIT_RECEIVE_PACK_ENABLED, got: %s", body)
					}
					return nil
				},
			},
			{
				Name: "receive-pack-post-rejected",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					resp, err := http.Post(base+"/git/git-receive-pack", "application/x-git-receive-pack-request", nil)
					if err != nil {
						return err
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusForbidden {
						body, _ := io.ReadAll(resp.Body)
						return fmt.Errorf("expected 403, got %d (body: %s)", resp.StatusCode, body)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_Clone verifies that a full git clone works via the HTTP endpoint.
//
// Scenario: git clone via Spine HTTP endpoint succeeds
//
//	Given a seeded repo served via git HTTP
//	When git clone is run against the endpoint
//	Then the clone succeeds and contains the expected files
func TestGitHTTP_Clone(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-clone",
		Description: "Full git clone via the smart HTTP endpoint succeeds",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			enableGitHTTPExport(),
			{
				Name: "git-clone-succeeds",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					cloneDir := filepath.Join(sc.T.TempDir(), "cloned")

					cmd := exec.Command("git", "clone", base+"/git", cloneDir)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("git clone failed: %w\n%s", err, out)
					}

					// Verify the clone contains governance files.
					constitutionPath := filepath.Join(cloneDir, "governance", "constitution.md")
					cmd = exec.Command("test", "-f", constitutionPath)
					if err := cmd.Run(); err != nil {
						return fmt.Errorf("expected governance/constitution.md in clone, not found")
					}

					return nil
				},
			},
		},
	})
}

// TestGitHTTP_ShallowClone verifies that --depth 1 clone works.
//
// Scenario: Shallow clone via Spine HTTP endpoint succeeds
//
//	Given a seeded repo
//	When git clone --depth 1 is run
//	Then the clone succeeds with minimal history
func TestGitHTTP_ShallowClone(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-shallow-clone",
		Description: "Shallow clone (--depth 1) via the smart HTTP endpoint succeeds",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			enableGitHTTPExport(),
			{
				Name: "git-shallow-clone-succeeds",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					cloneDir := filepath.Join(sc.T.TempDir(), "shallow")

					cmd := exec.Command("git", "clone", "--depth", "1", base+"/git", cloneDir)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("git clone --depth 1 failed: %w\n%s", err, out)
					}

					// Verify shallow clone — commit count should be 1.
					cmd = exec.Command("git", "rev-list", "--count", "HEAD")
					cmd.Dir = cloneDir
					countOut, err := cmd.Output()
					if err != nil {
						return fmt.Errorf("git rev-list --count failed: %w", err)
					}

					count := bytes.TrimSpace(countOut)
					if string(count) != "1" {
						return fmt.Errorf("expected 1 commit in shallow clone, got %s", count)
					}

					return nil
				},
			},
		},
	})
}

// TestGitHTTP_BranchClone verifies that cloning a specific branch works.
//
// Scenario: Branch-specific clone via Spine HTTP endpoint
//
//	Given a repo with a feature branch
//	When git clone --branch feature is run
//	Then the clone is on the correct branch
func TestGitHTTP_BranchClone(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-branch-clone",
		Description: "Clone a specific branch via the smart HTTP endpoint",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServer(),
			{
				Name: "create-feature-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					repoPath := sc.Repo.Dir
					cmd := exec.Command("git", "branch", "spine/run/test-branch")
					cmd.Dir = repoPath
					if out, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("create branch: %w\n%s", err, out)
					}

					// Update server info for smart HTTP.
					cmd = exec.Command("git", "update-server-info")
					cmd.Dir = repoPath
					if out, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("update-server-info: %w\n%s", err, out)
					}
					return nil
				},
			},
			{
				Name: "clone-specific-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					cloneDir := filepath.Join(sc.T.TempDir(), "branch-clone")

					cmd := exec.Command("git", "clone",
						"--depth", "1",
						"--branch", "spine/run/test-branch",
						base+"/git",
						cloneDir,
					)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("git clone --branch failed: %w\n%s", err, out)
					}

					// Verify we're on the right branch.
					cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
					cmd.Dir = cloneDir
					branchOut, err := cmd.Output()
					if err != nil {
						return fmt.Errorf("get branch: %w", err)
					}

					branch := string(bytes.TrimSpace(branchOut))
					if branch != "spine/run/test-branch" {
						return fmt.Errorf("expected branch spine/run/test-branch, got %q", branch)
					}

					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PushAcceptedWhenFlagOn verifies that once ReceivePackEnabled
// is on, `git push` to a fresh branch succeeds end-to-end through the
// Spine git HTTP endpoint — the ref advertisement is served, the pack
// upload is accepted, and the server repo ends up with the new commit.
//
// This exercises the flag-on path of EPIC-004 TASK-001. Note: no branch
// protection runs here; that is EPIC-004 TASK-002. The scenario pushes
// to a new branch (rather than `main`) so the server's working-tree
// checkout does not block the update.
func TestGitHTTP_PushAcceptedWhenFlagOn(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-accepted",
		Description: "Git push is accepted end-to-end when ReceivePackEnabled is on",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			// Push is gated on a bearer token in production (the
			// trusted-CIDR bypass deliberately does NOT apply to
			// receive-pack). Minting a test token here would require
			// wiring the token store; for this scenario's scope —
			// does the flag actually let the CGI push through? —
			// devMode bypasses auth and keeps the test focused on
			// the receive-pack gate. A separate unit test covers
			// the push-requires-auth contract.
			setupGitHTTPServerWithOptions(true, true),
			enableGitHTTPExport(),
			{
				// Server is a non-bare repo; pushing to its
				// currently-checked-out branch would fail with
				// "refusing to update checked out branch". Pushing
				// to a fresh branch sidesteps that and still
				// exercises the CGI push path.
				Name: "clone-and-push-new-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					repoPath := sc.Repo.Dir
					cloneDir := filepath.Join(sc.T.TempDir(), "push-clone")

					if out, err := exec.Command("git", "clone", base+"/git", cloneDir).CombinedOutput(); err != nil {
						return fmt.Errorf("git clone failed: %w\n%s", err, out)
					}

					// Configure identity on the client side
					// so `git commit` has an author.
					for _, kv := range [][2]string{
						{"user.email", "push-test@spine.local"},
						{"user.name", "Push Test"},
					} {
						c := exec.Command("git", "config", kv[0], kv[1])
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git config %s: %w\n%s", kv[0], err, out)
						}
					}

					// Create a new branch, add a file, commit.
					branchCmds := [][]string{
						{"checkout", "-b", "spine/test/push"},
					}
					for _, args := range branchCmds {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}
					newFile := filepath.Join(cloneDir, "pushed-file.md")
					if err := os.WriteFile(newFile, []byte("# Pushed\n"), 0o644); err != nil {
						return fmt.Errorf("write pushed file: %w", err)
					}
					for _, args := range [][]string{
						{"add", "pushed-file.md"},
						{"commit", "-m", "add pushed file"},
					} {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}

					// Push to the HTTP endpoint.
					push := exec.Command("git", "push", "origin", "spine/test/push")
					push.Dir = cloneDir
					if out, err := push.CombinedOutput(); err != nil {
						return fmt.Errorf("git push failed: %w\n%s", err, out)
					}

					// Verify the server repo has the new branch.
					show := exec.Command("git", "show-ref", "--verify", "refs/heads/spine/test/push")
					show.Dir = repoPath
					if out, err := show.CombinedOutput(); err != nil {
						return fmt.Errorf("server should have refs/heads/spine/test/push after push, got: %w\n%s", err, out)
					}

					return nil
				},
			},
		},
	})
}
