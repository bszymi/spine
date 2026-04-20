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
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
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
	return setupGitHTTPServerWithOptions(false, false, nil, nil)
}

// setupGitHTTPServerWithOptions is setupGitHTTPServer with knobs that a
// handful of scenarios need (receive-pack gate, devMode for auth
// bypass, branch-protection policy). The default helper keeps the
// previous read-only shape.
//
// When pushEvents is non-nil, it is wired as the push-path event
// sink so tests can assert branch_protection.override emission from
// the Git path without subscribing through the full event router.
func setupGitHTTPServerWithOptions(receivePackEnabled, devMode bool, policy branchprotect.Policy, pushEvents event.Emitter) scenarioEngine.Step {
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
				Policy:             policy,
			})
			if err != nil {
				return fmt.Errorf("create git HTTP handler: %w", err)
			}

			authSvc := auth.NewService(sc.Runtime.Store)

			// Wire the pre-receive gate's per-push resources. In a
			// real deployment this comes from WSServicePool; for
			// scenarios the policy is fixed at setup time and the
			// events sink is whatever the caller wants to record
			// against.
			pushResolver := gateway.GitPushResolverFunc(func(_ context.Context, _ string) (gateway.GitPushResources, func(), error) {
				return gateway.GitPushResources{Policy: policy, Events: pushEvents}, func() {}, nil
			})

			cfg := gateway.ServerConfig{
				Store:             sc.Runtime.Store,
				Auth:              authSvc,
				Artifacts:         sc.Runtime.Artifacts,
				ProjQuery:         projection.NewQueryService(sc.Runtime.Store, sc.Repo.Git),
				ProjSync:          sc.Runtime.Projections,
				WorkspaceResolver: wsResolver,
				GitHTTP:           gitHandler,
				GitPushResolver:   pushResolver,
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
			setupGitHTTPServerWithOptions(true, true, nil, nil),
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

// TestGitHTTP_PushRejectedByPreReceivePolicy covers EPIC-004 TASK-002
// end-to-end: a push to a protected branch is rejected by the
// pre-receive gate before touching the server ref. The client sees
// the rejection as `remote: branch-protection: ...` lines on stderr
// and git exits non-zero.
func TestGitHTTP_PushRejectedByPreReceivePolicy(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-rejected-by-policy",
		Description: "Pre-receive policy rejects a direct push to a protected branch; ref unchanged on server",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			// release/* is protected with no-direct-write. The client
			// will try to push a new branch refs/heads/release/1.x; the
			// gate must reject it before git-http-backend advances the
			// ref.
			setupGitHTTPServerWithOptions(true, true, branchprotect.New(
				branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}}),
			), nil),
			enableGitHTTPExport(),
			{
				Name: "clone-and-attempt-protected-push",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					repoPath := sc.Repo.Dir
					cloneDir := filepath.Join(sc.T.TempDir(), "rejected-clone")

					if out, err := exec.Command("git", "clone", base+"/git", cloneDir).CombinedOutput(); err != nil {
						return fmt.Errorf("git clone failed: %w\n%s", err, out)
					}
					for _, kv := range [][2]string{
						{"user.email", "reject-test@spine.local"},
						{"user.name", "Reject Test"},
					} {
						c := exec.Command("git", "config", kv[0], kv[1])
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git config %s: %w\n%s", kv[0], err, out)
						}
					}
					// Create a protected branch and a commit on it.
					for _, args := range [][]string{
						{"checkout", "-b", "release/1.x"},
					} {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}
					if err := os.WriteFile(filepath.Join(cloneDir, "release.md"), []byte("# Release\n"), 0o644); err != nil {
						return fmt.Errorf("write file: %w", err)
					}
					for _, args := range [][]string{
						{"add", "release.md"},
						{"commit", "-m", "release bump"},
					} {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}

					// The push MUST fail.
					push := exec.Command("git", "push", "origin", "release/1.x")
					push.Dir = cloneDir
					out, err := push.CombinedOutput()
					if err == nil {
						return fmt.Errorf("expected git push to fail under no-direct-write policy, but it succeeded:\n%s", out)
					}
					// Client-side output must name the protection so
					// operators know why their push bounced.
					if !bytes.Contains(out, []byte("branch-protection")) {
						return fmt.Errorf("expected remote branch-protection message, got:\n%s", out)
					}
					if !bytes.Contains(out, []byte("no-direct-write")) {
						return fmt.Errorf("expected remote to name no-direct-write kind, got:\n%s", out)
					}

					// Server-side ref MUST NOT exist — pre-receive
					// rejects all-or-nothing (ADR-009 §3).
					show := exec.Command("git", "show-ref", "--verify", "refs/heads/release/1.x")
					show.Dir = repoPath
					if err := show.Run(); err == nil {
						return fmt.Errorf("server must not have refs/heads/release/1.x after pre-receive denial")
					}
					return nil
				},
			},
		},
	})
}

// pushEventRecorder captures branch_protection.override events so
// the scenario can assert on emission without subscribing through
// the full event router.
type pushEventRecorder struct {
	mu     sync.Mutex
	events []domain.Event
}

func (r *pushEventRecorder) Emit(_ context.Context, e domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *pushEventRecorder) snapshot() []domain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Event, len(r.events))
	copy(out, r.events)
	return out
}

// TestGitHTTP_PushOverrideHonouredForOperator covers TASK-003
// end-to-end: an operator pushes to a protected branch with
// `-o spine.override=true`, the pre-receive gate honours the
// override, the push lands on the server, and exactly one
// branch_protection.override event is emitted with the ADR-009 §4
// payload (including pre_receive_ref).
func TestGitHTTP_PushOverrideHonouredForOperator(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	recorder := &pushEventRecorder{}
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-override-operator",
		Description: "Operator push with spine.override=true bypasses protection and emits one governance event",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			// release/* is protected with no-direct-write. The
			// push below includes spine.override=true; devMode
			// bypasses bearer auth, and the actor the gateway
			// attaches in devMode has operator role so the
			// role gate lets the override through.
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}})),
				recorder,
				&domain.Actor{ActorID: "op-scenario", Role: domain.RoleOperator},
			),
			enableGitHTTPExport(),
			{
				Name: "operator-overrides-protected-push",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					base := sc.MustGet("gw_url").(string)
					repoPath := sc.Repo.Dir
					cloneDir := filepath.Join(sc.T.TempDir(), "override-clone")

					if out, err := exec.Command("git", "clone", base+"/git", cloneDir).CombinedOutput(); err != nil {
						return fmt.Errorf("git clone: %w\n%s", err, out)
					}
					for _, kv := range [][2]string{
						{"user.email", "op-scenario@spine.local"},
						{"user.name", "Op Scenario"},
					} {
						c := exec.Command("git", "config", kv[0], kv[1])
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git config %s: %w\n%s", kv[0], err, out)
						}
					}
					// Create a protected branch + commit.
					for _, args := range [][]string{{"checkout", "-b", "release/2.x"}} {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}
					if err := os.WriteFile(filepath.Join(cloneDir, "release.md"), []byte("# Release 2.x\n"), 0o644); err != nil {
						return fmt.Errorf("write file: %w", err)
					}
					for _, args := range [][]string{
						{"add", "release.md"},
						{"commit", "-m", "release 2.x bump"},
					} {
						c := exec.Command("git", args...)
						c.Dir = cloneDir
						if out, err := c.CombinedOutput(); err != nil {
							return fmt.Errorf("git %v: %w\n%s", args, err, out)
						}
					}

					// Push with the override option.
					push := exec.Command("git", "push",
						"-o", "spine.override=true",
						"origin", "release/2.x",
					)
					push.Dir = cloneDir
					if out, err := push.CombinedOutput(); err != nil {
						return fmt.Errorf("override push failed (should succeed): %w\n%s", err, out)
					}

					// Server ref must exist.
					show := exec.Command("git", "show-ref", "--verify", "refs/heads/release/2.x")
					show.Dir = repoPath
					if out, err := show.CombinedOutput(); err != nil {
						return fmt.Errorf("server should have refs/heads/release/2.x after override push: %w\n%s", err, out)
					}

					events := recorder.snapshot()
					if len(events) != 1 {
						return fmt.Errorf("expected exactly 1 branch_protection.override event, got %d", len(events))
					}
					ev := events[0]
					if ev.Type != domain.EventBranchProtectionOverride {
						return fmt.Errorf("wrong event type: %s", ev.Type)
					}
					payload := string(ev.Payload)
					for _, substr := range []string{
						`"branch":"refs/heads/release/2.x"`,
						`"rule_kinds":["no-direct-write"]`,
						`"operation":"direct_write"`,
						`"pre_receive_ref"`,
					} {
						if !bytes.Contains([]byte(payload), []byte(substr)) {
							return fmt.Errorf("payload missing %q, got: %s", substr, payload)
						}
					}
					return nil
				},
			},
		},
	})
	_ = event.Emitter(nil) // keep import referenced on all platforms
}

// setupGitHTTPServerWithOptionsAndActor is a variant of
// setupGitHTTPServerWithOptions that also attaches a fixed actor to
// each push request via a small gateway middleware. devMode alone
// would accept the request but leaves the actor blank, which makes
// the branch-protection override role-gate fail; the scenario tests
// explicitly exercising override need an operator on the context.
func setupGitHTTPServerWithOptionsAndActor(receivePackEnabled, devMode bool, policy branchprotect.Policy, pushEvents event.Emitter, actor *domain.Actor) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-git-http-server-with-actor",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repoPath := sc.Repo.Dir

			wsResolver := &fakeWSResolver{
				workspaces: map[string]*workspace.Config{
					"default": {
						ID:       "default",
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
				Policy:             policy,
			})
			if err != nil {
				return fmt.Errorf("create git HTTP handler: %w", err)
			}

			authSvc := auth.NewService(sc.Runtime.Store)

			pushResolver := gateway.GitPushResolverFunc(func(_ context.Context, _ string) (gateway.GitPushResources, func(), error) {
				return gateway.GitPushResources{Policy: policy, Events: pushEvents}, func() {}, nil
			})

			cfg := gateway.ServerConfig{
				Store:             sc.Runtime.Store,
				Auth:              authSvc,
				Artifacts:         sc.Runtime.Artifacts,
				ProjQuery:         projection.NewQueryService(sc.Runtime.Store, sc.Repo.Git),
				ProjSync:          sc.Runtime.Projections,
				WorkspaceResolver: wsResolver,
				GitHTTP:           gitHandler,
				GitPushResolver:   pushResolver,
				DevMode:           devMode,
			}

			srv := gateway.NewServer(":0", cfg)
			// Wrap the handler with an actor-injection middleware so
			// the pre-receive gate sees the scenario's chosen role.
			inner := srv.Handler()
			wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if actor != nil {
					r = r.WithContext(domain.WithActor(r.Context(), actor))
				}
				inner.ServeHTTP(w, r)
			})
			ts := httptest.NewServer(wrapped)
			sc.ParentT.Cleanup(ts.Close)

			sc.Set("gw_url", ts.URL)
			sc.Set("gw_auth", authSvc)
			sc.Set("repo_path", repoPath)
			return nil
		},
	}
}

// pushClientClone clones the test repo into a fresh directory and
// configures a committer identity on the client side. Returns the
// clone dir. Shared setup across the EPIC-004 push scenarios so
// each test body focuses on what it is actually exercising.
func pushClientClone(sc *scenarioEngine.ScenarioContext, label string) (string, error) {
	base := sc.MustGet("gw_url").(string)
	cloneDir := filepath.Join(sc.T.TempDir(), label)
	if out, err := exec.Command("git", "clone", base+"/git", cloneDir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone: %w\n%s", err, out)
	}
	for _, kv := range [][2]string{
		{"user.email", label + "@spine.local"},
		{"user.name", "Push Test " + label},
	} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = cloneDir
		if out, err := c.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git config %s: %w\n%s", kv[0], err, out)
		}
	}
	return cloneDir, nil
}

func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}

// TestGitHTTP_PushDeleteRejectedByNoDelete drives a real `git push
// --delete` against a branch that `branch-protection.yaml` has marked
// `no-delete`. The pre-receive gate must reject with the rule kind
// on the wire and the server ref must still exist afterwards.
func TestGitHTTP_PushDeleteRejectedByNoDelete(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-delete-rejected",
		Description: "git push --delete against a no-delete ref is rejected and the server ref is preserved",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			{
				Name: "seed-release-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					// Server-side branch to try to delete. We do it
					// before the server starts so the ref is visible
					// to the clone.
					if err := runGit(t, sc.Repo.Dir, "branch", "release/prod"); err != nil {
						return err
					}
					return runGit(t, sc.Repo.Dir, "update-server-info")
				},
			},
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDelete},
				}})),
				nil,
				&domain.Actor{ActorID: "c-1", Role: domain.RoleContributor},
			),
			{
				Name: "attempt-delete",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					cloneDir, err := pushClientClone(sc, "delete-reject")
					if err != nil {
						return err
					}
					// git push --delete.
					out, err := exec.Command("git", "-C", cloneDir, "push", "origin", "--delete", "release/prod").CombinedOutput()
					if err == nil {
						return fmt.Errorf("expected delete push to fail under no-delete rule, succeeded:\n%s", out)
					}
					if !bytes.Contains(out, []byte("no-delete")) {
						return fmt.Errorf("expected remote message to name no-delete, got:\n%s", out)
					}
					// Server ref must still exist.
					if err := runGit(t, sc.Repo.Dir, "show-ref", "--verify", "refs/heads/release/prod"); err != nil {
						return fmt.Errorf("server-side ref must survive denied delete: %w", err)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PushOverrideRejectedForContributor drives the role-gate
// on the push override: a contributor pushes with `-o
// spine.override=true` to a protected branch and is rejected with
// the `override_not_authorised` reason on the wire. The server ref
// must be unchanged and no audit event must be emitted.
func TestGitHTTP_PushOverrideRejectedForContributor(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	recorder := &pushEventRecorder{}
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-push-override-contributor-rejected",
		Description: "Contributor with spine.override=true still hits the role gate and nothing is emitted",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}})),
				recorder,
				&domain.Actor{ActorID: "c-1", Role: domain.RoleContributor},
			),
			enableGitHTTPExport(),
			{
				Name: "contributor-override-rejected",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					cloneDir, err := pushClientClone(sc, "contributor-override")
					if err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "checkout", "-b", "release/contrib"); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(cloneDir, "release.md"), []byte("# C\n"), 0o644); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "add", "release.md"); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "commit", "-m", "c"); err != nil {
						return err
					}

					out, err := exec.Command("git", "-C", cloneDir, "push",
						"-o", "spine.override=true",
						"origin", "release/contrib").CombinedOutput()
					if err == nil {
						return fmt.Errorf("contributor push with override should fail, succeeded:\n%s", out)
					}
					if !bytes.Contains(out, []byte("override")) {
						return fmt.Errorf("expected remote message to mention override, got:\n%s", out)
					}
					if err := exec.Command("git", "-C", sc.Repo.Dir, "show-ref",
						"--verify", "refs/heads/release/contrib").Run(); err == nil {
						return fmt.Errorf("server ref must not exist after contributor override rejection")
					}
					if n := len(recorder.snapshot()); n != 0 {
						return fmt.Errorf("contributor override must emit 0 events, got %d", n)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_UnusedOverrideEmitsNoEvent covers the "silent on
// unused override" rule from ADR-009 §4: an operator sets the
// override flag on a push to an unprotected branch. The push is
// allowed (no rule matched) and NO event is emitted — otherwise
// every operator who habitually sets the flag would flood the audit
// log.
func TestGitHTTP_UnusedOverrideEmitsNoEvent(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	recorder := &pushEventRecorder{}
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-unused-override-silent",
		Description: "Override flag on a push to an unprotected branch emits no governance event",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}})),
				recorder,
				&domain.Actor{ActorID: "op-unused", Role: domain.RoleOperator},
			),
			enableGitHTTPExport(),
			{
				Name: "push-with-unused-override",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					cloneDir, err := pushClientClone(sc, "unused-override")
					if err != nil {
						return err
					}
					// feature/* is NOT covered by any rule.
					if err := runGit(t, cloneDir, "checkout", "-b", "feature/untouched"); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(cloneDir, "f.md"), []byte("f\n"), 0o644); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "add", "f.md"); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "commit", "-m", "f"); err != nil {
						return err
					}
					out, err := exec.Command("git", "-C", cloneDir, "push",
						"-o", "spine.override=true",
						"origin", "feature/untouched").CombinedOutput()
					if err != nil {
						return fmt.Errorf("push with unused override should succeed:\n%s", out)
					}
					if n := len(recorder.snapshot()); n != 0 {
						return fmt.Errorf("unused override must emit 0 events, got %d", n)
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_MixedPushRejectedAllOrNothing asserts ADR-009 §3's
// pre-receive semantics: a push that updates an allowed ref and a
// denied ref is rejected as a whole, and NEITHER server-side ref
// advances.
func TestGitHTTP_MixedPushRejectedAllOrNothing(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-mixed-push-rejected",
		Description: "Push with one denied ref is rejected in full; no partial application",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}})),
				nil,
				&domain.Actor{ActorID: "c-mixed", Role: domain.RoleContributor},
			),
			enableGitHTTPExport(),
			{
				Name: "mixed-push",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					cloneDir, err := pushClientClone(sc, "mixed")
					if err != nil {
						return err
					}
					// Two fresh branches: one allowed, one denied.
					for _, branch := range []string{"feature/a", "release/3.x"} {
						if err := runGit(t, cloneDir, "checkout", "-b", branch); err != nil {
							return err
						}
						if err := os.WriteFile(filepath.Join(cloneDir, branch+".md"), []byte(branch+"\n"), 0o644); err != nil {
							return err
						}
						if err := runGit(t, cloneDir, "add", "."); err != nil {
							return err
						}
						if err := runGit(t, cloneDir, "commit", "-m", branch); err != nil {
							return err
						}
						if err := runGit(t, cloneDir, "checkout", "main"); err != nil {
							return err
						}
					}

					out, err := exec.Command("git", "-C", cloneDir, "push", "origin",
						"feature/a", "release/3.x").CombinedOutput()
					if err == nil {
						return fmt.Errorf("mixed push should be rejected, got success:\n%s", out)
					}
					// Both refs must be absent on the server.
					for _, branch := range []string{"feature/a", "release/3.x"} {
						if err := exec.Command("git", "-C", sc.Repo.Dir, "show-ref",
							"--verify", "refs/heads/"+branch).Run(); err == nil {
							return fmt.Errorf("server must not have refs/heads/%s after all-or-nothing rejection", branch)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestGitHTTP_PushedCommitIsByteIdentical asserts that Spine does
// not rewrite client-produced commits — the operator override path
// uses the `branch_protection.override` event as the audit record,
// not a commit trailer rewrite. The commit SHA the client created
// must equal the ref SHA on the server after the override push.
func TestGitHTTP_PushedCommitIsByteIdentical(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "git-http-pushed-commit-byte-identical",
		Description: "Operator-override push lands on the server with the client's exact commit SHA — no trailer rewrite",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []scenarioEngine.Step{
			setupGitHTTPServerWithOptionsAndActor(
				true, true,
				branchprotect.New(branchprotect.StaticRules([]config.Rule{{
					Branch:      "release/*",
					Protections: []config.RuleKind{config.KindNoDirectWrite},
				}})),
				&pushEventRecorder{},
				&domain.Actor{ActorID: "op-bytes", Role: domain.RoleOperator},
			),
			enableGitHTTPExport(),
			{
				Name: "override-push-preserves-sha",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					cloneDir, err := pushClientClone(sc, "byte-identical")
					if err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "checkout", "-b", "release/bytes"); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(cloneDir, "b.md"), []byte("b\n"), 0o644); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "add", "b.md"); err != nil {
						return err
					}
					if err := runGit(t, cloneDir, "commit", "-m", "b"); err != nil {
						return err
					}
					// Capture the client-side SHA.
					clientSHAOut, err := exec.Command("git", "-C", cloneDir, "rev-parse", "HEAD").CombinedOutput()
					if err != nil {
						return fmt.Errorf("client rev-parse: %w\n%s", err, clientSHAOut)
					}
					clientSHA := strings.TrimSpace(string(clientSHAOut))

					out, err := exec.Command("git", "-C", cloneDir, "push",
						"-o", "spine.override=true",
						"origin", "release/bytes").CombinedOutput()
					if err != nil {
						return fmt.Errorf("override push failed:\n%s", out)
					}

					// Server SHA for the same ref must equal the
					// client's — if Spine rewrote the commit to add
					// a trailer, the SHAs would diverge.
					serverSHAOut, err := exec.Command("git", "-C", sc.Repo.Dir,
						"rev-parse", "refs/heads/release/bytes").CombinedOutput()
					if err != nil {
						return fmt.Errorf("server rev-parse: %w\n%s", err, serverSHAOut)
					}
					serverSHA := strings.TrimSpace(string(serverSHAOut))
					if serverSHA != clientSHA {
						return fmt.Errorf("server rewrote the commit: client=%s server=%s", clientSHA, serverSHA)
					}
					return nil
				},
			},
		},
	})
}
