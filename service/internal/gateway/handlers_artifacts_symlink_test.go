package gateway_test

// INIT-023 TASK-004 (audit F5) gateway pins: a write through a symlink
// committed on the run branch is refused 400 invalid_params with nothing
// written outside the worktree (it was a 500 git_error after the outside
// write on 61994fe), and an oversized worktree file on Update is 413.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/testutil"
)

const f5GatewayDoc = `---
type: Governance
title: Attacker Controlled
status: Living Document
version: "0.1"
---

# Attacker Controlled
`

// f5RunBranch is the branch planningFakeStore.GetRun reports.
const f5RunBranch = "spine/run/plan-branch"

func f5GatewayGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@x", "-c", "user.name=a"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// f5GatewayServer serves a real artifact.Service over repo, with
// write_context resolving to f5RunBranch (a planning run).
func f5GatewayServer(t *testing.T, repo string) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	base := newFakeStore()
	base.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(base)
	token, _, err := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	artSvc := artifact.NewService(git.NewCLIClient(repo), nil, repo)
	artSvc.WithPolicy(branchprotect.NewPermissive())
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     &planningFakeStore{fakeStore: base},
		Auth:      authSvc,
		Artifacts: artSvc,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, token
}

// f5PlantOnRunBranch commits files and links on f5RunBranch only.
func f5PlantOnRunBranch(t *testing.T, repo string, plant func()) {
	t.Helper()
	f5GatewayGit(t, repo, "checkout", "-q", "-b", f5RunBranch)
	plant()
	f5GatewayGit(t, repo, "add", "-A")
	f5GatewayGit(t, repo, "commit", "-q", "-m", "plant")
	f5GatewayGit(t, repo, "checkout", "-q", "main")
}

func f5Do(t *testing.T, ts *httptest.Server, token, method, path, body string) (int, gateway.ErrorResponse) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var er gateway.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	return resp.StatusCode, er
}

func TestF5_Gateway_CreateThroughDanglingSymlink_400(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "pwned.md")
	f5PlantOnRunBranch(t, repo, func() {
		if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(repo, "governance", "evil.md")); err != nil {
			t.Fatal(err)
		}
	})
	ts, token := f5GatewayServer(t, repo)

	body, _ := json.Marshal(map[string]any{
		"path":          "governance/evil.md",
		"content":       f5GatewayDoc,
		"write_context": map[string]string{"run_id": "run-1"},
	})
	status, er := f5Do(t, ts, token, http.MethodPost, "/api/v1/artifacts", string(body))
	if status != http.StatusBadRequest || len(er.Errors) == 0 || er.Errors[0].Code != string(domain.ErrInvalidParams) {
		t.Fatalf("want 400 invalid_params, got %d %+v", status, er)
	}
	if strings.Contains(er.Errors[0].Message, outside) || strings.Contains(er.Errors[0].Message, os.TempDir()) {
		t.Fatalf("error message leaks a filesystem path: %q", er.Errors[0].Message)
	}
	if _, err := os.Lstat(victim); err == nil {
		t.Fatalf("file written outside the worktree at %s", victim)
	}
}

func TestF5_Gateway_UpdateOversizedWorktreeFile_413(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	f5PlantOnRunBranch(t, repo, func() {
		p := filepath.Join(repo, "governance", "big.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		big := f5GatewayDoc + strings.Repeat("x", int(worktreefs.DefaultMaxReadBytes))
		if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	ts, token := f5GatewayServer(t, repo)

	body, _ := json.Marshal(map[string]any{
		"content":       f5GatewayDoc,
		"write_context": map[string]string{"run_id": "run-1"},
	})
	status, er := f5Do(t, ts, token, http.MethodPut, "/api/v1/artifacts/governance/big.md", string(body))
	if status != http.StatusRequestEntityTooLarge || len(er.Errors) == 0 || er.Errors[0].Code != string(domain.ErrPayloadTooLarge) {
		t.Fatalf("want 413 payload_too_large, got %d %+v", status, er)
	}
}
