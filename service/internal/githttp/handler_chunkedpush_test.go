package githttp

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// TestHandler_ChunkedPushBodyRefusedByCGI pins a transport limitation
// that predates INIT-023 TASK-001 and that TASK-001 records instead of
// fixing (its AC 11 keeps handler.go untouched; cross-task review, wave
// 1). git sends a receive-pack body larger than http.postBuffer (1 MiB
// by default) with Transfer-Encoding: chunked, and net/http/cgi refuses
// every chunked request body ("Chunked request bodies are not supported
// by CGI", HTTP 400) because CGI/1.1 needs CONTENT_LENGTH. ServeHTTP
// hands the stitched body to cgi.Handler as is, so a push of more than
// 1 MiB fails although no rule matches the branch. SMP's gateway spools
// such bodies to disk first and accepts the same push.
//
// The test also proves the client-side workaround that
// KNOWN-LIMITATIONS.md documents: raising http.postBuffer above the push
// size makes git send a Content-Length body, and the same push lands.
//
// It records the framing of every POST /git-receive-pack, so it cannot
// pass vacuously if git ever stops chunking. When the follow-up spools
// chunked bodies (TASK-001 "Parity with SMP"), the first push lands and
// this test fails: flip it to assert the push applies, and remove the
// KNOWN-LIMITATIONS.md entry and the TASK-001 parity bullet.
func TestHandler_ChunkedPushBodyRefusedByCGI(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := nulHandler(t, repo)
	actor := &domain.Actor{ActorID: "actor-contributor", Role: domain.RoleContributor}

	type framing struct {
		chunked bool
		length  int64
	}
	var (
		mu    sync.Mutex
		posts []framing
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			mu.Lock()
			posts = append(posts, framing{
				chunked: len(r.TransferEncoding) > 0 && r.TransferEncoding[0] == "chunked",
				length:  r.ContentLength,
			})
			mu.Unlock()
		}
		ctx := WithRepoPath(r.Context(), repo)
		ctx = domain.WithActor(ctx, actor)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()
	url := srv.URL + "/"
	takePosts := func() []framing {
		mu.Lock()
		defer mu.Unlock()
		out := posts
		posts = nil
		return out
	}

	work := t.TempDir()
	clone := exec.Command("git", "clone", "-q", url, work)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	// 2 MiB of random bytes: incompressible, so the pack stays above
	// the 1 MiB default http.postBuffer.
	big := make([]byte, 2<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	nulGit(t, work, nil, "add", "big.bin")
	nulGit(t, work, nil, "commit", "-q", "-m", "big")
	head := nulGit(t, work, nil, "rev-parse", "HEAD")
	push := func(ref string, gitConfig ...string) (string, error) {
		args := append(append([]string{}, gitConfig...), "-C", work, "push", "--porcelain", url, "HEAD:"+ref)
		cmd := exec.Command("git", args...)
		cmd.Env = nulGitEnv(t)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Default client: the pack goes chunked and CGI refuses it.
	out, err := push("refs/heads/feature-big")
	if err == nil {
		t.Fatalf("chunked push applied; the limitation is fixed, so flip this test and drop the KNOWN-LIMITATIONS.md entry:\n%s", out)
	}
	t.Logf("refused push, client output:\n%s", out)
	if !strings.Contains(out, "HTTP 400") {
		t.Errorf("expected the client to report HTTP 400, got:\n%s", out)
	}
	if nulRefExists(t, repo, "refs/heads/feature-big") {
		t.Error("feature-big was created by a refused push")
	}
	sawChunked := false
	for _, p := range takePosts() {
		sawChunked = sawChunked || p.chunked
	}
	if !sawChunked {
		t.Fatal("git sent no chunked POST /git-receive-pack; the test proves nothing about chunked bodies")
	}

	// Workaround: a postBuffer larger than the push gives a
	// Content-Length body, and the same push lands.
	if out, err := push("refs/heads/feature-big", "-c", "http.postBuffer=16777216"); err != nil {
		t.Fatalf("push with a raised http.postBuffer refused: %v\n%s", err, out)
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/feature-big"); got != head {
		t.Errorf("feature-big = %s, want %s", got, head)
	}
	sawLarge := false
	for _, p := range takePosts() {
		if p.chunked {
			t.Error("git still chunked the push with a raised http.postBuffer; the documented workaround is wrong")
		}
		sawLarge = sawLarge || p.length > 1<<20
	}
	if !sawLarge {
		t.Error("no Content-Length receive-pack body above 1 MiB was sent; the workaround push did not exercise a large body")
	}
}
