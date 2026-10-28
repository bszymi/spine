package githttp

// INIT-024 TASK-005 review round 1: the admission bound (P3-1), the
// probe's place in the request path (P4-6), the re-check session's
// connections (P4-7) and the joined command reader (P4-4, mutant MR5).
// Only the seams of push_gate_races_seams_test.go, so the red run at
// f4baae12 compiles them unchanged.

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// pgrStalledPush is a push whose command section stops in the middle of
// its first pkt-line; done is closed once ServeHTTP has returned, with
// the status in code.
type pgrStalledPush struct {
	pw   *io.PipeWriter
	done chan struct{}
	code *atomic.Int32
}

func pgrStartStalledPush(h *Handler, repo, base, evil, ws string, actor *domain.Actor) pgrStalledPush {
	pr, pw := io.Pipe()
	p := pgrStalledPush{pw: pw, done: make(chan struct{}), code: &atomic.Int32{}}
	go func() {
		w := pgrServe(h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", pr, repo, ws, actor, "")
		p.code.Store(int32(w.Code))
		close(p.done)
	}()
	line := pktLine(base + " " + evil + " refs/heads/feature\x00report-status\n")
	go func() { _, _ = pw.Write(line[:len(line)/2]) }()
	return p
}

// pgrAnswered waits (up to 3 s) for want of the pushes to be answered,
// then 200 ms more for any beyond it, and returns the statuses of those
// answered.
func pgrAnswered(pushes []pgrStalledPush, want int) []int {
	answered := func() []int {
		var codes []int
		for _, p := range pushes {
			select {
			case <-p.done:
				codes = append(codes, int(p.code.Load()))
			default:
			}
		}
		return codes
	}
	for deadline := time.Now().Add(3 * time.Second); len(answered()) < want && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return answered()
}

// TestGitHTTP_PreAdmissionWorkIsBounded (review round 1, P3-1; the
// reviewer's probe R3): the pushes of one principal that are inside their
// command section and admission at once are capped at 2x the per-actor
// slot cap, and those of one workspace at 2x the per-workspace cap; one
// more is answered 503 at once. Another workspace is not affected, no
// git slot is held, and the bound is released when the pushes end. At
// f4baae12 all of one principal's 10 pushes sat in their command read
// and none was refused.
func TestGitHTTP_PreAdmissionWorkIsBounded(t *testing.T) {
	repo, base, evil, _ := nulRepo(t, "")
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	}, 0, 0, 0, 20*time.Second)
	perActor, perWorkspace, _ := pgrDefaultCaps()
	var all []pgrStalledPush
	defer func() {
		for _, p := range all {
			_ = p.pw.Close()
		}
		for _, p := range all {
			<-p.done
		}
	}()
	batch := func(n int, ws string, actor *domain.Actor) []pgrStalledPush {
		var b []pgrStalledPush
		for i := 0; i < n; i++ {
			p := pgrStartStalledPush(h, repo, base, evil, ws, actor)
			b = append(b, p)
			all = append(all, p)
		}
		return b
	}
	check := func(label string, pushes []pgrStalledPush, inside int) {
		t.Helper()
		codes := pgrAnswered(pushes, len(pushes)-inside)
		if len(codes) != len(pushes)-inside {
			t.Fatalf("%s: %d of %d pushes answered, want %d (%d inside their command section at once)", label, len(codes), len(pushes), len(pushes)-inside, inside)
		}
		for _, c := range codes {
			if c != http.StatusServiceUnavailable {
				t.Fatalf("%s: a refused push got %d, want 503", label, c)
			}
		}
	}

	bob, carol := pgrActor("bob"), pgrActor("carol")
	check("alice of ws-1", batch(10, "ws-1", pgrAlice), 2*perActor)
	check("bob of ws-1", batch(10, "ws-1", bob), 2*perWorkspace-2*perActor)
	check("carol of ws-1 (workspace full)", batch(3, "ws-1", carol), 0)
	check("dave of ws-2", batch(3, "ws-2", pgrActor("dave")), 3)
	if held := pgrSlotsHeld(h); held != 0 {
		t.Errorf("pushes still in their command section hold %d git slot(s)", held)
	}

	for _, p := range all {
		_ = p.pw.Close()
	}
	for _, p := range all {
		<-p.done
	}
	all = nil
	check("alice again, after the pushes ended", batch(1, "ws-1", pgrAlice), 1)
}

// pgrBlockingProbe answers after release is closed and counts its calls.
type pgrBlockingProbe struct {
	calls   atomic.Int32
	release chan struct{}
	err     error
}

func (p *pgrBlockingProbe) probe(context.Context, string) (bool, error) {
	p.calls.Add(1)
	<-p.release
	return false, p.err
}

// TestStorageProbe_ReadsNeverWaitAndPushesProbeOnce (review round 1,
// P4-6): a read neither runs nor waits on the probe — reads are served
// while a push's probe is in flight — and concurrent first pushes to one
// repository run one probe (a per-repository singleflight).
func TestStorageProbe_ReadsNeverWaitAndPushesProbeOnce(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	}, 16, 16, 16, 0)
	p := &pgrBlockingProbe{release: make(chan struct{})}
	pgrSetStorageProbe(h, p.probe)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = pgrServe(h, http.MethodGet, "/info/refs?service=git-receive-pack", "", http.NoBody, repo, "ws-1", pgrActor("p"+string(rune('a'+i))), "").Code
		}(i)
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	reads := make(chan int, 3)
	go func() {
		for i := 0; i < 3; i++ {
			reads <- pgrTry(h, repo, "ws-1", pgrActor("reader"), "")
		}
	}()
wait:
	for i := 0; i < 3; i++ {
		select {
		case code := <-reads:
			if code != http.StatusOK {
				t.Errorf("read during a push's probe: %d, want 200", code)
			}
		case <-time.After(5 * time.Second):
			t.Error("a read waited on a push's probe")
			break wait
		}
	}
	close(p.release)
	for len(reads) > 0 {
		<-reads
	}
	wg.Wait()
	if n := p.calls.Load(); n != 1 {
		t.Errorf("6 concurrent first pushes ran %d probes, want 1", n)
	}
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("push advertisement %d: %d, want 200", i, c)
		}
	}
}

// TestRecheck_CloseDoesNotWaitForStalledConnections (review round 1,
// P4-7): a same-user connection that never sends its request does not
// keep close — which ServeHTTP runs before it gives the git slot back —
// waiting until its deadline; and once the token has been used the
// socket no longer accepts connections.
func TestRecheck_CloseDoesNotWaitForStalledConnections(t *testing.T) {
	s, err := startRecheck(context.Background(), time.Minute, func(context.Context, []RefUpdate) []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	stalled, err := net.Dial("unix", s.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stalled.Close() }()
	time.Sleep(50 * time.Millisecond) // accepted
	closed := make(chan struct{})
	go func() { s.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close waited for a stalled connection (until its 1-minute deadline)")
	}

	s2, err := startRecheck(context.Background(), time.Minute, func(context.Context, []RefUpdate) []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.close)
	line := strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " refs/heads/x"
	if got := recheckRequest(t, s2, recheckHello, s2.token, line); got != "ok\n" {
		t.Fatalf("the push's request: %q", got)
	}
	if conn, err := net.Dial("unix", s2.socket); err == nil {
		_ = conn.Close()
		t.Fatal("the socket still accepts connections after its token was used")
	}
}

// pgrLateBody records reads that return after ServeHTTP has returned.
// Each read takes a moment to come back, as a read woken by a closing
// body does.
type pgrLateBody struct {
	r        *io.PipeReader
	mu       sync.Mutex
	returned bool
	late     int
}

func (b *pgrLateBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	time.Sleep(50 * time.Millisecond)
	b.mu.Lock()
	if b.returned {
		b.late++
	}
	b.mu.Unlock()
	return n, err
}

func (b *pgrLateBody) Close() error { return b.r.Close() }

// TestPrereceive_CommandReaderEndsBeforeServeHTTPReturns (review round 1,
// P4-4, mutant MR5): when the command-read deadline cuts a stalled read,
// ServeHTTP waits for the reading goroutine, so nothing reads the request
// body after the handler has returned.
func TestPrereceive_CommandReaderEndsBeforeServeHTTPReturns(t *testing.T) {
	repo, base, evil, _ := nulRepo(t, "")
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	}, 0, 0, 0, 200*time.Millisecond)
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	body := &pgrLateBody{r: pr}
	line := pktLine(base + " " + evil + " refs/heads/feature\x00report-status\n")
	go func() { _, _ = pw.Write(line[:len(line)/2]) }()
	w := pgrServe(h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", body, repo, "ws-1", pgrAlice, "")
	body.mu.Lock()
	body.returned = true
	body.mu.Unlock()
	if !strings.Contains(w.Body.String(), "the command section was not received within") {
		t.Fatalf("response: %q", w.Body.String())
	}
	time.Sleep(300 * time.Millisecond)
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.late != 0 {
		t.Fatalf("%d read(s) of the request body returned after ServeHTTP had returned", body.late)
	}
}
