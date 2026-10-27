package githttp

// INIT-024 TASK-004 (TASK-006 security review item 2): the handler serves
// exactly the protocol paths of the repository it was handed. The
// classification used to match path SUFFIXES (".../info/refs", any GET),
// and git-http-backend takes everything in PATH_INFO before the protocol
// part as a directory under GIT_PROJECT_ROOT and serves it as a
// repository: in the primary (non-bare) repository "/.git/info/refs"
// reached git-http-backend as the repository's own git directory, and any
// other git directory inside the tree (a submodule's under .git/modules,
// a nested clone) was reachable the same way — with a trusted peer's
// token-less read, too. Dot segments reached git-http-backend, whose alias
// check refused them with a 500 (TASK-006's residual). Now only the paths
// git-http-backend serves at the repository root are reads or pushes;
// every other path — a prefix, a dot segment, a doubled or trailing slash —
// is neither, and ServeHTTP refuses it (403) before CGI.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestProtocolPaths_OnlyTheServedRepositoryRoot(t *testing.T) {
	const (
		oid1 = "0123456789abcdef0123456789abcdef01234567"
		oid2 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	reads := []struct{ method, path string }{
		{"GET", "/info/refs"},
		{"GET", "/HEAD"},
		{"GET", "/objects/info/packs"},
		{"GET", "/objects/info/alternates"},
		{"GET", "/objects/info/http-alternates"},
		{"GET", "/objects/01/" + oid1[2:]},
		{"GET", "/objects/01/" + oid2[2:]},
		{"GET", "/objects/pack/pack-" + oid1 + ".pack"},
		{"GET", "/objects/pack/pack-" + oid1 + ".idx"},
		{"GET", "/objects/pack/pack-" + oid2 + ".pack"},
		{"GET", "/objects/pack/pack-" + oid2 + ".idx"},
		{"POST", "/git-upload-pack"},
	}
	for _, tc := range reads {
		req := httptest.NewRequest(tc.method, tc.path, http.NoBody)
		if !isReadOnly(req) || IsReceivePack(req) {
			t.Errorf("%s %s: read=%v push=%v, want a read", tc.method, tc.path, isReadOnly(req), IsReceivePack(req))
		}
	}
	for _, tc := range []struct{ method, path, query string }{
		{"GET", "/info/refs", "service=git-receive-pack"},
		{"POST", "/git-receive-pack", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path+"?"+tc.query, http.NoBody)
		if isReadOnly(req) || !IsReceivePack(req) {
			t.Errorf("%s %s?%s: want a push", tc.method, tc.path, tc.query)
		}
	}

	neither := []struct{ method, path string }{
		// A prefix: another directory under GIT_PROJECT_ROOT.
		{"GET", "/.git/info/refs"},
		{"GET", "/.git/HEAD"},
		{"GET", "/.git/objects/info/packs"},
		{"POST", "/.git/git-upload-pack"},
		{"POST", "/.git/git-receive-pack"},
		{"GET", "/.git/modules/sub/info/refs"},
		{"GET", "/sub/info/refs"},
		{"POST", "/sub/git-upload-pack"},
		{"POST", "/sub/git-receive-pack"},
		{"GET", "/foo/HEAD"},
		// Dot segments, doubled and trailing slashes.
		{"GET", "/./info/refs"},
		{"GET", "/info/./refs"},
		{"GET", "/info/../info/refs"},
		{"GET", "/objects/../HEAD"},
		{"GET", "/../HEAD"},
		{"POST", "/./git-upload-pack"},
		{"POST", "/../git-receive-pack"},
		{"GET", "//info/refs"},
		{"GET", "/info//refs"},
		{"GET", "/info/refs/"},
		{"GET", "/HEAD/"},
		{"POST", "/git-upload-pack/"},
		// Names git-http-backend does not serve, or not with that case.
		{"GET", "/objects/pack/pack-abc.pack"},
		{"GET", "/objects/01/" + strings.ToUpper(oid1[2:])},
		{"GET", "/objects/0/" + oid1[1:]},
		{"GET", "/objects/info/refs"},
		{"GET", "/config"},
		{"GET", "/description"},
		{"GET", "/"},
		{"GET", ""},
		{"POST", "/git-upload-archive"},
		{"POST", "/info/refs"},
		{"GET", "/git-upload-pack"},
		{"GET", "/git-receive-pack"},
		{"HEAD", "/HEAD"},
	}
	for _, tc := range neither {
		req := httptest.NewRequest(tc.method, "/", http.NoBody)
		req.URL.Path = tc.path // verbatim: httptest would clean some of these
		for _, q := range []string{"", "service=git-upload-pack", "service=git-receive-pack", "service&git-receive-pack"} {
			req.URL.RawQuery = q
			if isReadOnly(req) || IsReceivePack(req) || IsNonCanonicalInfoRefs(req) {
				t.Errorf("%s %q?%s: read=%v push=%v noncanonical=%v, want none", tc.method, tc.path, q,
					isReadOnly(req), IsReceivePack(req), IsNonCanonicalInfoRefs(req))
			}
		}
	}
}

// TestServeHTTP_OnlyTheServedRepositoryIsReachable drives the real
// git-http-backend behind ServeHTTP for a non-bare repository — the
// primary repository's shape — with receive-pack on: the repository root
// is served, and no other directory of the tree reaches git-http-backend.
func TestServeHTTP_OnlyTheServedRepositoryIsReachable(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"},
		{"init", "-q", "--bare", repo + "/nested.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(method, path, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", http.NoBody)
		req.URL.Path, req.URL.RawQuery = path, query
		req = req.WithContext(WithRepoPath(req.Context(), repo))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// The repository root is served.
	if w := serve("GET", "/info/refs", "service=git-upload-pack"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "refs/heads/main") {
		t.Fatalf("GET /info/refs: %d %q, want the root repository's refs", w.Code, w.Body.String())
	}

	for _, tc := range []struct{ method, path, query string }{
		{"GET", "/.git/info/refs", "service=git-upload-pack"},
		{"GET", "/.git/info/refs", ""},
		{"GET", "/.git/HEAD", ""},
		{"POST", "/.git/git-upload-pack", ""},
		{"GET", "/nested.git/info/refs", "service=git-upload-pack"},
		{"GET", "/nested.git/HEAD", ""},
		{"GET", "/./info/refs", "service=git-upload-pack"},
		{"GET", "/info/../.git/info/refs", "service=git-upload-pack"},
		{"GET", "/.git/info/refs", "service=git-receive-pack"},
		{"POST", "/.git/git-receive-pack", ""},
	} {
		w := serve(tc.method, tc.path, tc.query)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "unsupported git operation") {
			t.Errorf("%s %s?%s: %d %q, want 403 unsupported git operation (before git-http-backend)",
				tc.method, tc.path, tc.query, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}
