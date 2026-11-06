package githttp

// INIT-024 TASK-013: chi v5.5.0 routes the HTTP QUERY method to
// Handle/HandleFunc routes, and the gateway's /git routes are HandleFunc
// routes, so a QUERY request now reaches ServeHTTP (chi v5.2.5 answered
// 405 first). It is neither a read nor a push and is refused before
// git-http-backend runs.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestServeHTTP_QueryMethodIsUnsupported(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, query string }{
		{"/info/refs", "service=git-upload-pack"},
		{"/info/refs", "service=git-receive-pack"},
		{"/info/refs", ""},
		{"/git-upload-pack", ""},
		{"/git-receive-pack", ""},
		{"/HEAD", ""},
	} {
		req := httptest.NewRequest("QUERY", "/", http.NoBody)
		req.URL.Path, req.URL.RawQuery = tc.path, tc.query
		if isReadOnly(req) || IsReceivePack(req) {
			t.Errorf("QUERY %s?%s: read=%v push=%v, want neither", tc.path, tc.query, isReadOnly(req), IsReceivePack(req))
		}
		req = req.WithContext(WithRepoPath(req.Context(), repo))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "unsupported git operation") {
			t.Errorf("QUERY %s?%s: %d %q, want 403 unsupported git operation", tc.path, tc.query, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}
