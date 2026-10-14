package gateway

// INIT-023 TASK-015 (audit P3-30) regression: the /git token-less
// trusted-CIDR bypass is granted only to a direct peer, never to a
// reverse proxy (declared or not) or to a forwarded address, and
// SPINE_DEV_MODE no longer switches git authentication off; the
// dedicated SPINE_GIT_HTTP_DEV_NO_AUTH flag opens reads only. Real
// git-http-backend behind a bare repository (AC 8, 9, 10, 20).

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

func t015GitServer(t *testing.T, trustedCIDRs []string, proxyCIDRs string, receivePack bool) *Server {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	h, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		TrustedCIDRs:       trustedCIDRs,
		ReceivePackEnabled: receivePack,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	proxies, err := ParseTrustedProxyCIDRs(proxyCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		gitHTTP:           h,
		wsResolver:        &poolStubResolver{cfg: workspace.Config{ID: "ws-1", RepoPath: repo, Status: workspace.StatusActive}},
		trustedProxyCIDRs: proxies,
	}
}

// t015GitRequest drives handleGit for /git/ws-1/<gitPath> from remoteAddr
// with the given extra headers (name, value pairs; repeated names add
// header lines) and returns the status.
func t015GitRequest(s *Server, method, gitPath, remoteAddr string, headers ...string) int {
	return t015GitResponse(s, method, gitPath, remoteAddr, headers...).Code
}

// t015GitResponse is t015GitRequest returning the whole response.
func t015GitResponse(s *Server, method, gitPath, remoteAddr string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/git/ws-1/"+gitPath, http.NoBody)
	req.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	wildcard, _, _ := strings.Cut(gitPath, "?")
	rctx.URLParams.Add("*", wildcard)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleGit(w, req)
	return w
}

const (
	t015InfoRefs     = "info/refs?service=git-upload-pack"
	t015PushInfoRefs = "info/refs?service=git-receive-pack"
	// t015SmuggledPushInfoRefs repeats `service`: url.Values.Get returns
	// the first value, git-http-backend keeps the last (review round 1).
	t015SmuggledPushInfoRefs = "info/refs?service=git-upload-pack&service=git-receive-pack"
)

// t015ReceivePackAdvertisement is the content type git-http-backend
// answers the push-side ref advertisement with.
const t015ReceivePackAdvertisement = "application/x-git-receive-pack-advertisement"

func TestP330_ProxyAddressIsNeverATrustedGitPeer(t *testing.T) {
	// 172.28.0.1 is a declared reverse proxy (or Docker's bridge gateway
	// fronting a published port) and inside the trusted runner CIDR, as
	// with the former compose default SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.0.0/16.
	s := t015GitServer(t, []string{"172.28.0.0/16"}, "172.28.0.1/32", false)
	for _, tc := range []struct {
		name    string
		headers []string
	}{
		{"internet client behind the proxy", []string{"X-Forwarded-For", "203.0.113.9"}},
		{"proxy hop without X-Forwarded-For", nil},
		{"client-forged trusted hop, proxy appended the real client", []string{"X-Forwarded-For", "172.28.0.5, 203.0.113.9"}},
		{"client-forged first header line, proxy added a second line", []string{"X-Forwarded-For", "172.28.0.5", "X-Forwarded-For", "203.0.113.9"}},
		{"unparsable hop", []string{"X-Forwarded-For", "not-an-ip"}},
	} {
		if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "172.28.0.1:40000", tc.headers...); code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401 (token-less clone granted)", tc.name, code)
		}
	}
}

// AC 8 as amended by the design check: a request from a declared proxy is
// never token-less, even when the forwarded address is a trusted runner —
// a proxy that passes the client's header through lets the client name
// any address.
func TestP330_ProxiedRequestIsNeverTokenless(t *testing.T) {
	s := t015GitServer(t, []string{"172.28.0.0/16"}, "10.0.0.0/8", false)
	for _, h := range [][]string{
		{"X-Forwarded-For", "172.28.0.5"},
		{"Forwarded", "for=172.28.0.5"},
		{"X-Real-IP", "172.28.0.5"},
	} {
		if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "10.0.0.2:40000", h...); code != http.StatusUnauthorized {
			t.Errorf("%s: %s via a declared proxy: status %d, want 401", h[0], h[1], code)
		}
	}
}

// Guard (passes before and after): a direct runner inside the trusted
// CIDR still clones without a token; a direct outsider does not.
func TestP330_DirectRunnerStillTrusted(t *testing.T) {
	s := t015GitServer(t, []string{"172.28.0.0/16"}, "10.0.0.0/8", false)
	if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "172.28.0.5:40000"); code != http.StatusOK {
		t.Fatalf("direct trusted runner: status %d, want 200", code)
	}
	if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Fatalf("direct untrusted client: status %d, want 401", code)
	}
	// IPv6 direct peers are judged the same way.
	s6 := t015GitServer(t, []string{"fd00:28::/64"}, "", false)
	if code := t015GitRequest(s6, http.MethodGet, t015InfoRefs, "[fd00:28::5]:40000"); code != http.StatusOK {
		t.Fatalf("direct trusted IPv6 runner: status %d, want 200", code)
	}
	// Review round 1: the token-less runner read does not stretch to a
	// push advertisement smuggled in a repeated `service` parameter.
	sp := t015GitServer(t, []string{"172.28.0.0/16"}, "", true)
	if w := t015GitResponse(sp, http.MethodGet, t015SmuggledPushInfoRefs, "172.28.0.5:40000"); w.Code != http.StatusUnauthorized {
		t.Fatalf("direct trusted runner, repeated service parameter: status %d (%s), want 401", w.Code, w.Header().Get("Content-Type"))
	}
}

// Review round 1 (P3): git-http-backend keeps the LAST value of a
// repeated query parameter, url.Values.Get returns the FIRST. When the
// push/read classifier read one value,
// `info/refs?service=git-upload-pack&service=git-receive-pack` was a read
// for the token-less paths (trusted CIDR, SPINE_GIT_HTTP_DEV_NO_AUTH) and
// git-http-backend answered it with the receive-pack advertisement. A
// repeated `service` now counts as a push when any value names
// receive-pack (so it needs a bearer), and the handler refuses it (400)
// once past the auth gate, so git-http-backend never sees an ambiguous
// request.
func TestP330_RepeatedServiceParameterNeverOpensPushAdvertisement(t *testing.T) {
	devNoAuth := t015GitServer(t, nil, "", true)
	devNoAuth.gitDevNoAuth = true
	trustedPushOn := t015GitServer(t, []string{"172.28.0.0/16"}, "", true)
	trustedPushOff := t015GitServer(t, []string{"172.28.0.0/16"}, "", false)
	authed := t015GitServer(t, nil, "", true)
	authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
	authed.auth = authSvc

	const (
		uploadThenReceive = t015SmuggledPushInfoRefs
		receiveThenUpload = "info/refs?service=git-receive-pack&service=git-upload-pack"
		uploadTwice       = "info/refs?service=git-upload-pack&service=git-upload-pack"
	)
	for _, tc := range []struct {
		name    string
		s       *Server
		query   string
		peer    string
		headers []string
		want    int
	}{
		{"dev no-auth, upload then receive", devNoAuth, uploadThenReceive, "198.51.100.7:40000", nil, http.StatusUnauthorized},
		{"dev no-auth, receive then upload", devNoAuth, receiveThenUpload, "198.51.100.7:40000", nil, http.StatusUnauthorized},
		{"dev no-auth, upload twice", devNoAuth, uploadTwice, "198.51.100.7:40000", nil, http.StatusBadRequest},
		{"trusted runner, push on", trustedPushOn, uploadThenReceive, "172.28.0.5:40000", nil, http.StatusUnauthorized},
		{"trusted runner, upload twice", trustedPushOn, uploadTwice, "172.28.0.5:40000", nil, http.StatusBadRequest},
		{"trusted runner, push off", trustedPushOff, uploadThenReceive, "172.28.0.5:40000", nil, http.StatusBadRequest},
		{"authenticated pusher", authed, uploadThenReceive, "198.51.100.7:40000", []string{"Authorization", "Bearer " + bearer}, http.StatusBadRequest},
	} {
		w := t015GitResponse(tc.s, http.MethodGet, tc.query, tc.peer, tc.headers...)
		if ct := w.Header().Get("Content-Type"); ct == t015ReceivePackAdvertisement {
			t.Errorf("%s: git-http-backend served the receive-pack advertisement (status %d)", tc.name, w.Code)
		}
		if w.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	// Guard: a single service value is unchanged.
	if w := t015GitResponse(authed, http.MethodGet, t015PushInfoRefs, "198.51.100.7:40000", "Authorization", "Bearer "+bearer); w.Code != http.StatusOK ||
		w.Header().Get("Content-Type") != t015ReceivePackAdvertisement {
		t.Fatalf("authenticated push advertisement: status %d (%s), want 200 %s", w.Code, w.Header().Get("Content-Type"), t015ReceivePackAdvertisement)
	}
}

// AC 20: a peer inside the git CIDRs that is NOT declared as a proxy but
// sends a forwarding header is a proxy the operator did not declare (the
// audit's literal scenario: Docker's bridge gateway or a reverse proxy at
// 172.28.0.1 with SPINE_TRUSTED_PROXY_CIDRS unset).
func TestP330_ForwardingHeaderFromUndeclaredPeerIsNotTrusted(t *testing.T) {
	s := t015GitServer(t, []string{"172.28.0.0/16"}, "", false)
	for _, h := range [][]string{
		{"X-Forwarded-For", "203.0.113.9"},
		{"Forwarded", "for=203.0.113.9"},
		{"X-Real-IP", "203.0.113.9"},
		{"X-Forwarded-For", ""},
		{"Via", "1.1 proxy"},
	} {
		if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "172.28.0.1:40000", h...); code != http.StatusUnauthorized {
			t.Errorf("%s=%q from an undeclared proxy: status %d, want 401", h[0], h[1], code)
		}
	}
	// Guard: the same peer with no forwarding header is a direct peer.
	if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "172.28.0.1:40000"); code != http.StatusOK {
		t.Fatalf("direct trusted peer without forwarding headers: status %d, want 200", code)
	}
}

func TestP330_GitPeerIP(t *testing.T) {
	proxies, err := ParseTrustedProxyCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, remote string
		headers      map[string]string
		want         string
	}{
		{"direct", "172.28.0.5:1", nil, "172.28.0.5"},
		{"direct ipv6", "[fd00::5]:1", nil, "fd00::5"},
		{"declared proxy", "10.0.0.2:1", nil, ""},
		{"declared proxy with xff", "10.0.0.2:1", map[string]string{"X-Forwarded-For": "172.28.0.5"}, ""},
		{"xff", "172.28.0.5:1", map[string]string{"X-Forwarded-For": "172.28.0.5"}, ""},
		{"forwarded", "172.28.0.5:1", map[string]string{"Forwarded": "for=1.2.3.4"}, ""},
		{"x-real-ip", "172.28.0.5:1", map[string]string{"X-Real-IP": "1.2.3.4"}, ""},
		{"x-forwarded-host", "172.28.0.5:1", map[string]string{"X-Forwarded-Host": "spine"}, ""},
		{"x-forwarded-proto", "172.28.0.5:1", map[string]string{"X-Forwarded-Proto": "https"}, ""},
		{"via", "172.28.0.5:1", map[string]string{"Via": "1.1 p"}, ""},
		{"unparsable remote", "not-an-addr", nil, ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/git/x", http.NoBody)
		req.RemoteAddr = tc.remote
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		got := gitPeerIP(req, proxies)
		if (got == nil && tc.want != "") || (got != nil && got.String() != tc.want) {
			t.Errorf("%s: gitPeerIP = %v, want %q", tc.name, got, tc.want)
		}
	}
	// A header map built in-process with a non-canonical key counts too.
	req := httptest.NewRequest(http.MethodGet, "/git/x", http.NoBody)
	req.RemoteAddr = "172.28.0.5:1"
	req.Header["x-forwarded-for"] = []string{"1.2.3.4"}
	if got := gitPeerIP(req, nil); got != nil {
		t.Errorf("non-canonical forwarding header ignored: %v", got)
	}
}

// AC 10: SPINE_DEV_MODE no longer drops git auth, for clone or push.
func TestP330_DevModeNoLongerDropsGitAuth(t *testing.T) {
	s := t015GitServer(t, nil, "", true)
	s.devMode = true
	if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("clone under SPINE_DEV_MODE: status %d, want 401", code)
	}
	if code := t015GitRequest(s, http.MethodGet, t015PushInfoRefs, "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("push advertisement under SPINE_DEV_MODE: status %d, want 401", code)
	}
	if code := t015GitRequest(s, http.MethodPost, "git-receive-pack", "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("push under SPINE_DEV_MODE: status %d, want 401", code)
	}
}

// AC 10: the dedicated development flag opens reads only; a push still
// needs a bearer, and with one it is authenticated (an actor is bound).
func TestP330_GitDevNoAuth_ReadsOnly(t *testing.T) {
	s := t015GitServer(t, nil, "", true)
	s.gitDevNoAuth = true
	if code := t015GitRequest(s, http.MethodGet, t015InfoRefs, "198.51.100.7:40000"); code != http.StatusOK {
		t.Errorf("clone with SPINE_GIT_HTTP_DEV_NO_AUTH: status %d, want 200", code)
	}
	if code := t015GitRequest(s, http.MethodGet, t015PushInfoRefs, "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("push advertisement with SPINE_GIT_HTTP_DEV_NO_AUTH: status %d, want 401", code)
	}
	// Review round 1: nor through a repeated `service` parameter.
	if code := t015GitRequest(s, http.MethodGet, t015SmuggledPushInfoRefs, "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("push advertisement (repeated service) with SPINE_GIT_HTTP_DEV_NO_AUTH: status %d, want 401", code)
	}
	if code := t015GitRequest(s, http.MethodPost, "git-receive-pack", "198.51.100.7:40000"); code != http.StatusUnauthorized {
		t.Errorf("push with SPINE_GIT_HTTP_DEV_NO_AUTH: status %d, want 401", code)
	}
	authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
	s.auth = authSvc
	if code := t015GitRequest(s, http.MethodGet, t015PushInfoRefs, "198.51.100.7:40000", "Authorization", "Bearer "+bearer); code != http.StatusOK {
		t.Errorf("authenticated push advertisement with SPINE_GIT_HTTP_DEV_NO_AUTH: status %d, want 200", code)
	}
}

// AC 9: X-Forwarded-For is read across every header line, so the bucket
// is the proxy-observed client, not a client-forged first line.
func TestP330_RateLimitClientIP_ReadsAllForwardedLines(t *testing.T) {
	proxies, err := ParseTrustedProxyCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "10.0.0.2:5555"
	req.Header.Add("X-Forwarded-For", "1.2.3.4")     // client-forged
	req.Header.Add("X-Forwarded-For", "203.0.113.9") // added by the proxy
	if got := clientIPForRateLimit(req, proxies); got != "203.0.113.9" {
		t.Fatalf("bucket = %q, want the proxy-observed client 203.0.113.9", got)
	}
	// Unchanged cases: no header, an all-proxy chain and an untrusted
	// peer fall back to RemoteAddr.
	for _, tc := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"10.0.0.2:1", nil, "10.0.0.2"},
		{"10.0.0.2:1", []string{"10.0.0.7", "10.0.0.8"}, "10.0.0.2"},
		{"10.0.0.2:1", []string{""}, "10.0.0.2"},
		{"198.51.100.7:1", []string{"1.2.3.4"}, "198.51.100.7"},
		{"10.0.0.2:1", []string{"garbage, 203.0.113.9"}, "203.0.113.9"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		r.RemoteAddr = tc.remote
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := clientIPForRateLimit(r, proxies); got != tc.want {
			t.Errorf("remote %s xff %q: bucket %q, want %q", tc.remote, tc.xff, got, tc.want)
		}
	}
}

// The refused-proxied-peer Warn names the remedy, carries no header
// value, and is rate-limited.
// lockedBuffer is a bytes.Buffer safe for the concurrent writes of
// whatever else logs through slog.Default while a test captures it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestP330_ProxiedTrustedPeerWarnIsRateLimited(t *testing.T) {
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := t015GitServer(t, []string{"172.28.0.0/16"}, "", false)
	for i := 0; i < 3; i++ {
		_ = t015GitRequest(s, http.MethodGet, t015InfoRefs, "172.28.0.1:40000", "X-Forwarded-For", "203.0.113.9")
	}
	// An untrusted proxied peer never logs it.
	_ = t015GitRequest(s, http.MethodGet, t015InfoRefs, "198.51.100.7:40000", "X-Forwarded-For", "203.0.113.9")
	out := buf.String()
	if n := strings.Count(out, "token-less access refused for a trusted-CIDR peer"); n != 1 {
		t.Fatalf("warn logged %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "SPINE_TRUSTED_PROXY_CIDRS") || strings.Contains(out, "203.0.113.9") {
		t.Errorf("warn must name SPINE_TRUSTED_PROXY_CIDRS and no header value:\n%s", out)
	}
}
