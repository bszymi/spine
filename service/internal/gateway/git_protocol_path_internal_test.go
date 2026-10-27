package gateway

// INIT-024 TASK-004 (TASK-006 security review item 2): /git serves only the
// exact protocol paths of the repository a request resolved to. The
// handler's classification matched suffixes, so `/git/<ws>/spine/.git/
// info/refs` reached git-http-backend with `.git` as the repository — the
// primary repository is non-bare — and so would any other git directory
// inside the tree, for a trusted peer token-less. The gateway now
// classifies push and read on the git path it hands the handler, so the
// two decide on the same bytes; a push still never takes the trusted
// read bypass.

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

func protocolPathServer(t *testing.T, receivePack bool) (*Server, string) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	h, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath:         func(context.Context, string) (string, error) { return repo, nil },
		TrustedCIDRsByWorkspace: map[string][]string{"ws-1": {"10.1.0.0/16"}},
		ReceivePackEnabled:      receivePack,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return &Server{
		gitHTTP:    h,
		wsResolver: &poolStubResolver{cfg: workspace.Config{ID: "ws-1", RepoPath: repo, Status: workspace.StatusActive}},
	}, repo
}

func TestHandleGit_OnlyExactProtocolPathsReachTheRepository(t *testing.T) {
	const trusted = "10.1.0.5:40000"
	s, _ := protocolPathServer(t, false)

	// Controls: the primary repository, with and without its segment.
	for _, p := range []string{"info/refs?service=git-upload-pack", "spine/info/refs?service=git-upload-pack"} {
		w := t015GitResponse(s, http.MethodGet, p, trusted)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "refs/heads/main") {
			t.Fatalf("control %s: %d, want 200 with the primary's refs", p, w.Code)
		}
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "spine/.git/info/refs?service=git-upload-pack"},
		{http.MethodGet, "spine/.git/info/refs"},
		{http.MethodGet, "spine/.git/HEAD"},
		{http.MethodGet, "spine/.git/objects/info/packs"},
		{http.MethodPost, "spine/.git/git-upload-pack"},
		{http.MethodGet, "info/../.git/info/refs?service=git-upload-pack"},
		{http.MethodGet, "spine/./info/refs?service=git-upload-pack"},
		{http.MethodGet, "objects/../HEAD"},
	} {
		w := t015GitResponse(s, tc.method, tc.path, trusted)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "refs/heads/main") {
			t.Errorf("%s /git/ws-1/%s: %d, want no repository served", tc.method, tc.path, w.Code)
		}
	}
}

// A push is classified on the git path too: with receive-pack on, a
// trusted peer still needs a bearer for the push advertisement and the
// pack, under the primary's segment or none.
func TestHandleGit_PushOnTheGitPathNeedsABearer(t *testing.T) {
	const trusted = "10.1.0.5:40000"
	s, _ := protocolPathServer(t, true)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "info/refs?service=git-receive-pack"},
		{http.MethodGet, "spine/info/refs?service=git-receive-pack"},
		{http.MethodPost, "git-receive-pack"},
		{http.MethodPost, "spine/git-receive-pack"},
		{http.MethodGet, "info/refs?service&git-receive-pack"},
	} {
		if code := t015GitRequest(s, tc.method, tc.path, trusted); code != http.StatusUnauthorized {
			t.Errorf("%s /git/ws-1/%s from a trusted peer without a token: %d, want 401", tc.method, tc.path, code)
		}
	}
}
