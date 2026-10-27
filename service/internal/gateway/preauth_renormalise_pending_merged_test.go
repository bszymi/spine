package gateway_test

// INIT-024 integration A (TASK-003 x TASK-007): a shared-mode workspace
// build brings a converted work tree to its stored bytes (TASK-003), and a
// renormalisation that cannot finish yet (ErrRenormalisePending) is logged
// while the build goes on; the git endpoint's push path warns and goes on
// too. Neither may be visible to a caller nobody authenticated: before the
// bearer is validated, the workspace with the pending renormalisation
// answers what a missing one answers (TASK-007's uniform 401), on /api/v1
// and on /git, pushes included, and an unauthenticated push never reaches
// the push path that would install the budget hook.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestPreauth_PendingRenormalisationIsInvisibleBeforeAuthentication(t *testing.T) {
	// A budget the converted tree's HEAD (two entries) is over, so the
	// build's renormalisation is refused and stays pending.
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1}})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })

	repo := testutil.NewTempRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	testutil.WriteFile(t, repo, "a.txt", "one\ntwo\n")
	run("add", "-A")
	run("commit", "-q", "-m", "files")
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	run("checkout", "--", "a.txt")
	marker := filepath.Join(repo, ".git", "spine-renormalise-pending")
	hook := filepath.Join(repo, ".git", "hooks", "pre-receive")

	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	valid, _, err := authSvc.CreateToken(context.Background(), "admin-1", "ok", nil)
	if err != nil {
		t.Fatal(err)
	}
	table := func() *countingResolver {
		return &countingResolver{calls: map[string]int{}, errs: map[string]error{}, known: map[string]workspace.Config{
			"ws-pending": {ID: "ws-pending", RepoPath: repo, Status: workspace.StatusActive},
		}}
	}
	pool := workspace.NewServicePool(context.Background(), table(), workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = fs
			ss.Auth = authSvc
			return nil
		},
	})
	t.Cleanup(pool.Close)
	gitHandler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{WorkspaceResolver: table(), ServicePool: pool, GitHTTP: gitHandler})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	do := func(method, path, ws, authz string) mergedPreauthResp {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if ws != "" {
			req.Header.Set(gateway.WorkspaceHeader, ws)
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		h := resp.Header.Clone()
		h.Del("Date")
		h.Del("X-Trace-Id")
		return mergedPreauthResp{status: resp.StatusCode, body: string(b), header: h}
	}
	same := func(label string, got, want mergedPreauthResp) {
		t.Helper()
		if got.status != want.status || got.body != want.body || !reflect.DeepEqual(got.header, want.header) {
			t.Errorf("%s: %d %q %v; want the missing workspace's %d %q %v", label, got.status, got.body, got.header, want.status, want.body, want.header)
		}
	}

	const api = "/api/v1/system/metrics"
	wantAPI := do(http.MethodGet, api, "ws-missing", "Bearer junk")
	if wantAPI.status != http.StatusUnauthorized {
		t.Fatalf("api, missing workspace: status %d %q; want 401", wantAPI.status, wantAPI.body)
	}
	same("api, pending, no header", do(http.MethodGet, api, "ws-pending", ""), wantAPI)
	// The first bearer builds the workspace; its renormalisation is refused.
	same("api, pending, junk bearer (the build)", do(http.MethodGet, api, "ws-pending", "Bearer junk"), wantAPI)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture: the build did not leave the renormalisation pending: %v", err)
	}
	same("api, pending, junk bearer (built)", do(http.MethodGet, api, "ws-pending", "Bearer junk"), wantAPI)
	// The build went on: the token valid there is served.
	if got := do(http.MethodGet, api, "ws-pending", "Bearer "+valid); got.status != http.StatusOK {
		t.Fatalf("api, pending, valid token: status %d %q; want 200 (the build is served while the renormalisation is pending)", got.status, got.body)
	}

	upload := "/info/refs?service=git-upload-pack"
	wantGit := do(http.MethodGet, "/git/ws-missing"+upload, "", "Bearer junk")
	if wantGit.status != http.StatusUnauthorized {
		t.Fatalf("git, missing workspace: status %d %q; want 401", wantGit.status, wantGit.body)
	}
	for _, c := range []struct{ name, method, path, authz string }{
		{"read, junk bearer", http.MethodGet, "/git/ws-pending" + upload, "Bearer junk"},
		{"push advertisement, no header", http.MethodGet, "/git/ws-pending/info/refs?service=git-receive-pack", ""},
		{"push advertisement, junk bearer", http.MethodGet, "/git/ws-pending/info/refs?service=git-receive-pack", "Bearer junk"},
		{"push, no header", http.MethodPost, "/git/ws-pending/git-receive-pack", ""},
		{"push, junk bearer", http.MethodPost, "/git/ws-pending/git-receive-pack", "Bearer junk"},
	} {
		same("git, pending, "+c.name, do(c.method, c.path, "", c.authz), wantGit)
	}
	// No unauthenticated push reached the push path: the budget hook is
	// installed (and the renormalisation retried) only past the auth gate.
	if _, err := os.Lstat(hook); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pre-receive hook after unauthenticated pushes: %v; want none installed", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("renormalise marker after unauthenticated requests: %v; want it still pending", err)
	}
}
