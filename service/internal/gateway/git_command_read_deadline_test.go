package gateway_test

// INIT-024 TASK-005 review round 1 (P4-3): the git endpoint cuts a
// stalled push command section by moving the connection's read deadline
// (http.ResponseController). That works only through ResponseWriter
// wrappers with Unwrap; the fallback — closing the body — cannot
// interrupt a net/http server body. This drives the cut through the
// gateway's whole middleware chain (recovery, security headers, rate
// limit, CORS, trace, logging), so a wrapper without Unwrap is caught.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

// TestGitHTTP_GatewayCutsStalledCommandSection: an authenticated push
// that stops inside its command section is answered at the command-read
// deadline through the full router, with no server ReadTimeout to fall
// back on, and holds no slot meanwhile (one slot in all: the same
// actor's clone is served).
func TestGitHTTP_GatewayCutsStalledCommandSection(t *testing.T) {
	repo, head := admServerRepo(t)
	fs := newFakeStore()
	authSvc, tokens := admTokens(t, fs, map[string]domain.ActorRole{"alice": domain.RoleContributor})
	const deadline = 300 * time.Millisecond
	gh, err := githttp.NewHandler(githttp.Config{
		ReceivePackEnabled:        true,
		CommandReadTimeout:        deadline,
		MaxConcurrent:             1,
		MaxConcurrentPerWorkspace: 1,
		MaxConcurrentPerActor:     1,
	})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: fs, Auth: authSvc, GitHTTP: gh,
		WorkspaceResolver: admResolver{cfg: workspace.Config{ID: "ws1", RepoPath: repo, Status: workspace.StatusActive}},
	})
	ts := httptest.NewServer(srv.Handler()) // no ReadTimeout: only the cut can answer
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cmd := fmt.Sprintf("%s %s refs/heads/feature\x00report-status\n", head, strings.Repeat("a", 40))
	line := fmt.Sprintf("%04x%s", len(cmd)+4, cmd)
	start := time.Now()
	fmt.Fprintf(conn, "POST /git/ws1/git-receive-pack HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n"+
		"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n%s",
		tokens["alice"], len(line)+64, line[:len(line)/2])

	time.Sleep(deadline / 3)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/git/ws1/info/refs?service=git-upload-pack", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+tokens["alice"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the actor's read during its stalled push: %d, want 200 (the stalled push must hold no slot)", res.StatusCode)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the stalled push was not cut through the gateway within 3s (deadline %s): %v", deadline, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if elapsed := time.Since(start); elapsed > deadline+2*time.Second {
		t.Errorf("answered after %s, want about %s", elapsed, deadline)
	}
	if !strings.Contains(string(body), "the command section was not received within") {
		t.Errorf("response %d %q", resp.StatusCode, body)
	}
}
