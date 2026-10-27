package gateway

// INIT-024 integration A: TASK-006 and TASK-007 each made part of the /git
// auth gate uniform. TASK-006 answers every 401 with the one message and
// sends a push with receive-pack off through the gate; TASK-007 validates a
// shared-mode bearer against the workspace's own ServiceSet only, so a
// build failure or a ServiceSet without an auth service no longer falls
// back to the (nil) process-level service and answers 503. With both
// merged, every refusal of a caller not authenticated for the workspace is
// one response in shared mode, and the 503 "authentication not configured"
// that KNOWN-LIMITATIONS keeps on purpose is left to single mode.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/service/internal/workspace"
)

type t024iaResp struct {
	code   int
	body   string
	header http.Header
}

func t024iaSnap(w *httptest.ResponseRecorder) t024iaResp {
	return t024iaResp{code: w.Code, body: w.Body.String(), header: w.Header().Clone()}
}

func TestHandleGit_MergedGateIsUniformInSharedMode(t *testing.T) {
	// Shared mode: ws-a builds a ServiceSet without an auth service (no
	// runtime database), ws-b's build fails, receive-pack is off.
	f := t006Server(t, t006Bindings, false, false)
	f.pool = workspace.NewServicePool(context.Background(), f.resolver, workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			if ss.Config.ID == "ws-b" {
				return errors.New("connect to workspace database: connection refused")
			}
			return nil
		},
	})
	t.Cleanup(f.pool.Close)
	f.srv.servicePool = f.pool

	outsider := t006Outsider + ":40000"
	want := t024iaSnap(f.do(http.MethodGet, "/git/ws-missing/"+t006UploadInfoRefs, outsider, "Authorization", "Bearer junk"))
	if want.code != http.StatusUnauthorized {
		t.Fatalf("missing workspace: status %d %q; want 401", want.code, want.body)
	}

	bearer := []string{"Authorization", "Bearer junk-token-0123456789"}
	cases := []struct {
		name, method, target string
		headers              []string
	}{
		{"no header, read", http.MethodGet, "/git/ws-a/" + t006UploadInfoRefs, nil},
		{"basic header", http.MethodGet, "/git/ws-a/" + t006UploadInfoRefs, []string{"Authorization", "Basic eDp5"}},
		{"bare scheme", http.MethodGet, "/git/ws-a/" + t006UploadInfoRefs, []string{"Authorization", "Bearer "}},
		{"bearer, no auth service, read", http.MethodGet, "/git/ws-a/" + t006UploadInfoRefs, bearer},
		{"bearer, build failure, read", http.MethodGet, "/git/ws-b/" + t006UploadInfoRefs, bearer},
		{"no header, push off, advertisement", http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", nil},
		{"bearer, no auth service, push off, advertisement", http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", bearer},
		{"bearer, no auth service, push off, receive-pack", http.MethodPost, "/git/ws-a/git-receive-pack", bearer},
		{"bearer, build failure, push off, receive-pack", http.MethodPost, "/git/ws-b/git-receive-pack", bearer},
		{"bearer, inactive", http.MethodGet, "/git/ws-gone/" + t006UploadInfoRefs, bearer},
		{"bearer, unavailable", http.MethodGet, "/git/ws-down/" + t006UploadInfoRefs, bearer},
	}
	for _, tc := range cases {
		got := t024iaSnap(f.do(tc.method, tc.target, outsider, tc.headers...))
		if got.code != want.code || got.body != want.body || !reflect.DeepEqual(got.header, want.header) {
			t.Errorf("%s: %d %q %v; want the missing workspace's %d %q %v",
				tc.name, got.code, got.body, got.header, want.code, want.body, want.header)
		}
	}
}

// The documented residual stays, in single mode only: a server without an
// auth service answers 503 to a bearer for its workspace, where a missing
// workspace gets the uniform 401 (KNOWN-LIMITATIONS, "a single-mode server
// without an auth service tells workspaces apart").
func TestHandleGit_NoAuthService503IsSingleModeOnly(t *testing.T) {
	f := t006Server(t, t006Bindings, false, false) // no pool, no auth service
	outsider := t006Outsider + ":40000"

	missing := f.do(http.MethodGet, "/git/ws-missing/"+t006UploadInfoRefs, outsider, "Authorization", "Bearer junk")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing workspace: status %d %q; want 401", missing.Code, missing.Body.String())
	}
	resolved := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, outsider, "Authorization", "Bearer junk")
	if resolved.Code != http.StatusServiceUnavailable || !strings.Contains(resolved.Body.String(), "authentication not configured") {
		t.Errorf("single mode, resolved workspace without an auth service: status %d %q; want 503 \"authentication not configured\"",
			resolved.Code, resolved.Body.String())
	}
	// Without a bearer it is the uniform 401: the 503 needs a bearer.
	noHeader := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, outsider)
	if noHeader.Code != missing.Code || noHeader.Body.String() != missing.Body.String() {
		t.Errorf("single mode, no header: %d %q; want the uniform %d %q",
			noHeader.Code, noHeader.Body.String(), missing.Code, missing.Body.String())
	}
}
