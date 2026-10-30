package gateway

// INIT-024 integration A, phase 5 (coordination e): TASK-005 keys the git
// slots by (workspace, principal) from the request's context, TASK-004
// classifies a request on the exact git path the gateway hands the
// handler, and TASK-006/007 decide the /git auth gate per workspace. In
// shared mode, through the real /git routes: a trusted runner's token-less
// read is classified a read on its exact path, takes a slot keyed to its
// own workspace (the gateway sets it before the handler), and holding it
// does not cost another workspace's runner its slot.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/service/internal/githttp"
)

func TestHandleGit_SlotsAreKeyedToTheRequestsWorkspace(t *testing.T) {
	f := t006Server(t, t006Bindings, true, false)
	h, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath:         func(context.Context, string) (string, error) { return "", nil },
		TrustedCIDRsByWorkspace: t006Bindings,
		// Only the per-workspace cap can refuse here.
		MaxConcurrentPerWorkspace: 1,
		MaxConcurrentPerActor:     8,
		MaxConcurrent:             8,
		OpTimeout:                 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.srv.gitHTTP = h
	runnerA, runnerB := t006RunnerA+":40000", t006RunnerB+":40000"

	// ws-a's runner opens an upload-pack whose body it never finishes:
	// its slot is held while git waits for the request. The body signals
	// when git starts reading it, which is past the slot's acquisition.
	pr, pw := io.Pipe()
	body := &firstReadSignal{r: pr, ch: make(chan struct{})}
	held := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		held <- f.doBody(http.MethodPost, "/git/ws-a/git-upload-pack", runnerA, body)
	}()
	select {
	case <-body.ch:
	case w := <-held:
		t.Fatalf("the held read ended at once: %d %q", w.Code, w.Body.String())
	case <-time.After(20 * time.Second):
		t.Fatal("git never started reading the held read's body")
	}
	if w := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, runnerA); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "too many concurrent git operations") {
		t.Fatalf("ws-a's second request while its slot is held: %d %q; want 503", w.Code, w.Body.String())
	}
	// Another workspace's runner is not counted against ws-a.
	if w := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, runnerB); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), t006MarkerB) {
		t.Fatalf("ws-b's read while ws-a's slot is held: %d %q; want 200 with ws-b's refs", w.Code, w.Body.String())
	}
	// A push is still a push on its exact path: no token-less bypass for
	// ws-a's runner, and no slot taken by the refusal.
	if w := f.do(http.MethodPost, "/git/ws-a/git-receive-pack", runnerA); w.Code != http.StatusUnauthorized {
		t.Fatalf("ws-a's runner's token-less push: %d %q; want 401", w.Code, w.Body.String())
	}

	_ = pw.Close()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the held read did not end once its body did")
	}
	if w := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, runnerA); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), t006MarkerA) {
		t.Fatalf("ws-a's read once its slot is free: %d %q", w.Code, w.Body.String())
	}
}

// doBody is do with a request body.
func (f *t006Fixture) doBody(method, target, remoteAddr string, body io.Reader) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	f.srv.mountGitRoutes(r)
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	// A declared length git-http-backend waits for (the body never sends
	// it all): the CGI, and with it the slot, stays busy.
	req.ContentLength = 4096
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// firstReadSignal closes ch on the first Read.
type firstReadSignal struct {
	r    io.Reader
	ch   chan struct{}
	once sync.Once
}

func (f *firstReadSignal) Read(p []byte) (int, error) {
	f.once.Do(func() { close(f.ch) })
	return f.r.Read(p)
}
