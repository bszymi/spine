package githttp

// INIT-023 TASK-015 review round 2 (audit P3-30 companion): the push/read
// classification of GET .../info/refs must be the one git-http-backend
// acts on. url.ParseQuery and git-http-backend's own query parser disagree
// on queries no git client sends — a repeated `service` (first vs last
// value, round 1) and a name without '=' (git takes the NEXT segment as
// its value, so `service&git-receive-pack` is service=git-receive-pack to
// git and service="" to Go, round 2). These tests drive the real
// git-http-backend, with receive-pack enabled so it would serve the
// push-side advertisement, over every short query built from the bytes
// that matter, and check that nothing this package calls a read (or "not
// a push") is answered with the receive-pack advertisement.

import (
	"context"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

const receivePackAdvertisementType = "application/x-git-receive-pack-advertisement"

// infoRefsQueryCorpus returns every concatenation of up to four tokens
// from the bytes the two parsers treat differently, plus encoded and
// mixed forms, plus the three queries a git client sends.
func infoRefsQueryCorpus() []string {
	tokens := []string{"service", "=", "&", "git-receive-pack", "git-upload-pack", ";"}
	seen := map[string]bool{}
	var out []string
	add := func(q string) {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if depth == 0 {
			return
		}
		for _, tok := range tokens {
			q := prefix + tok
			add(q)
			walk(q, depth-1)
		}
	}
	walk("", 4)
	for _, q := range []string{
		"",
		"service=git-upload-pack",
		"service=git-receive-pack",
		"service&git-receive-pack&x=1",
		"x=1&service&git-receive-pack",
		"service=git-upload-pack&service&git-receive-pack",
		"%73ervice&git-receive-pack",
		"%73ervice=git-receive-pack",
		"service&git-%72eceive-pack",
		"service=git-%72eceive-pack",
		"service=git-receive-pack%00",
		"service%00&git-receive-pack",
		"service=git-receive-pack%zz",
		"service+&git-receive-pack",
		"service=git-receive-pack#x",
		"service=git-upload-pack&x=%zz",
		"Service=git-receive-pack",
	} {
		add(q)
	}
	return out
}

func newInfoRefsRequest(method, rawQuery string) *http.Request {
	req := httptest.NewRequest(method, "/info/refs", http.NoBody)
	// Set the raw bytes directly: they reach git-http-backend verbatim as
	// QUERY_STRING (net/http/cgi), whatever url.ParseQuery makes of them.
	req.URL.RawQuery = rawQuery
	return req
}

func TestInfoRefsClassifier_NeverAReadWhereGitHTTPBackendAdvertisesReceivePack(t *testing.T) {
	backend, err := findGitHTTPBackend()
	if err != nil {
		t.Fatalf("git-http-backend: %v", err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "--bare", "-q", repo},
		{"-C", repo, "config", "http.receivepack", "true"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	// advertisement asks git-http-backend itself, with no gate in front,
	// what it answers a query with.
	advertisement := func(rawQuery string) string {
		w := httptest.NewRecorder()
		(&cgi.Handler{
			Path:   backend,
			Dir:    repo,
			Env:    []string{"GIT_PROJECT_ROOT=" + repo, "GIT_HTTP_EXPORT_ALL=1"},
			Stderr: io.Discard,
		}).ServeHTTP(w, newInfoRefsRequest(http.MethodGet, rawQuery))
		return w.Header().Get("Content-Type")
	}

	// Positive controls: the probe sees the hole. git-http-backend answers
	// these with the receive-pack advertisement; url.ParseQuery reads the
	// last two as upload-pack (first value) and service="" respectively.
	for _, q := range []string{
		"service=git-receive-pack",
		"service=git-upload-pack&service=git-receive-pack",
		"service&git-receive-pack",
	} {
		if ct := advertisement(q); ct != receivePackAdvertisementType {
			t.Fatalf("positive control %q: git-http-backend answered %q, want %q", q, ct, receivePackAdvertisementType)
		}
	}

	corpus := infoRefsQueryCorpus()
	probed := 0
	for _, q := range corpus {
		req := newInfoRefsRequest(http.MethodGet, q)
		read, push := isReadOnly(req), IsReceivePack(req)
		// A GET info/refs is exactly one of the two: a read the token-less
		// paths may serve, or a (possible) push that needs a bearer.
		if read == push {
			t.Errorf("%q: isReadOnly=%v IsReceivePack=%v, want exactly one", q, read, push)
		}
		if push {
			// Needs a bearer at the gateway; a non-canonical one is then
			// refused by ServeHTTP (TestServeHTTP_RepeatedServiceParameterRefused,
			// TestInfoRefsQuery_OnlyClientQueriesReachGitHTTPBackend).
			continue
		}
		probed++
		if ct := advertisement(q); ct == receivePackAdvertisementType {
			t.Errorf("%q: classified as a read, but git-http-backend serves the receive-pack advertisement", q)
		}
	}
	// The empty query and service=git-upload-pack are reads.
	if probed < 2 {
		t.Fatalf("only %d of %d queries were probed as reads, want at least 2", probed, len(corpus))
	}
}

// Every info/refs query other than the three a git client sends is
// refused with 400 before any gate or CGI, whatever the receive-pack
// flag; the three are not refused by that check.
func TestInfoRefsQuery_OnlyClientQueriesReachGitHTTPBackend(t *testing.T) {
	canonical := map[string]bool{
		"":                         true,
		"service=git-upload-pack":  true,
		"service=git-receive-pack": true,
	}
	for _, enabled := range []bool{false, true} {
		h, err := NewHandler(Config{
			ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
			ReceivePackEnabled: enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range infoRefsQueryCorpus() {
			req := newInfoRefsRequest(http.MethodGet, q)
			// Not a repository: past the query check, the request answers
			// 403 (push off), or 500 from ensureReceivePackConfig.
			req = req.WithContext(WithRepoPath(req.Context(), "/nonexistent/definitely/not/a/repo"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			refused := w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "unsupported info/refs query")
			if refused == canonical[q] {
				t.Errorf("receive-pack enabled=%v, %q: status %d (%s), refused=%v, want refused=%v",
					enabled, q, w.Code, strings.TrimSpace(w.Body.String()), refused, !canonical[q])
			}
		}
	}
}
