package githttp

// INIT-024 TASK-005: the standalone push-gate races (review findings
// N-8, N-9, N-13), driven through Handler.ServeHTTP with the real
// git-http-backend. Version-specific seams live in
// push_gate_races_seams_test.go.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// pgrRuns is a run reader the test can change while a push is in
// flight (the handler reads it from other goroutines). onRead, when set,
// runs on every read.
type pgrRuns struct {
	mu     sync.Mutex
	r      *admReader
	onRead func()
}

func (g *pgrRuns) read() {
	if g.onRead != nil {
		g.onRead()
	}
}

func (g *pgrRuns) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.read()
	return g.r.GetRun(ctx, id)
}

func (g *pgrRuns) ListStepExecutionsByRun(ctx context.Context, id string) ([]domain.StepExecution, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.read()
	return g.r.ListStepExecutionsByRun(ctx, id)
}

func (g *pgrRuns) GetBranch(ctx context.Context, id string) (*domain.Branch, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.read()
	return g.r.GetBranch(ctx, id)
}

func (g *pgrRuns) mutate(f func(*admReader)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g.r)
}

// pgrDefs resolves every step to a step of the current type, which the
// test can change; calls counts the lookups.
type pgrDefs struct {
	mu    sync.Mutex
	typ   domain.StepType
	calls int
}

func (d *pgrDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return &domain.StepDefinition{ID: stepID, Type: d.typ}, nil
}

func (d *pgrDefs) set(t domain.StepType) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typ = t
}

func (d *pgrDefs) lookups() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

const (
	pgrSlug = "task-x"
	pgrHex  = "1a2b3c4d"
	pgrRun  = "spine/run/" + pgrSlug + "-" + pgrHex
)

var (
	pgrAlice   = &domain.Actor{ActorID: "alice", Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	pgrMallory = &domain.Actor{ActorID: "mallory", Role: domain.RoleContributor, Status: domain.ActorStatusActive}
)

// pgrCtx is the context the gateway hands the handler: the repository,
// the resolved workspace and, unless nil, the authenticated actor.
func pgrCtx(ctx context.Context, repo, ws string, actor *domain.Actor) context.Context {
	ctx = WithRepoPath(ctx, repo)
	ctx = observe.WithWorkspaceID(ctx, ws)
	if actor != nil {
		ctx = domain.WithActor(ctx, actor)
	}
	return ctx
}

// pgrServe drives one request through h and returns the recorder.
func pgrServe(h *Handler, method, target, contentType string, body io.Reader, repo, ws string, actor *domain.Actor, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(pgrCtx(req.Context(), repo, ws, actor)))
	return w
}

// pgrCommand is a one-ref command section asking for report-status over
// side-band-64k, so the hook's "remote:" lines reach the response body.
func pgrCommand(old, nw, ref string) []byte {
	b := pktLine(old + " " + nw + " " + ref + "\x00report-status side-band-64k\n")
	return append(b, flushPkt...)
}

// pgrHeldPush posts cmd as actor, holds the pack back until decided()
// reports the pre-pack decision was made, runs between(), then sends the
// pack, and returns the response body. A push refused before its pack is
// read returns without the pack ever being consumed.
func pgrHeldPush(t *testing.T, h *Handler, repo string, actor *domain.Actor, cmd, pack []byte, decided func() bool, between func()) string {
	t.Helper()
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	done := make(chan string, 1)
	go func() {
		w := pgrServe(h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", pr, repo, "ws-1", actor, "")
		done <- w.Body.String()
	}()
	if _, err := pw.Write(cmd); err != nil {
		t.Fatalf("write the command section: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !decided() {
		if time.Now().After(deadline) {
			t.Fatal("the pre-pack decision was never made")
		}
		time.Sleep(5 * time.Millisecond)
	}
	between()
	go func() {
		_, _ = pw.Write(pack)
		_ = pw.Close()
	}()
	select {
	case resp := <-done:
		return resp
	case <-time.After(60 * time.Second):
		t.Fatal("the push did not answer")
		return ""
	}
}

// pgrDemux joins a side-band response's progress (band 2: the "remote:"
// lines) and data (band 1) streams; anything else is appended verbatim.
func pgrDemux(resp string) string {
	var out strings.Builder
	for len(resp) >= 4 {
		var n int
		if _, err := fmt.Sscanf(resp[:4], "%04x", &n); err != nil || (n != 0 && (n < 5 || n > len(resp))) {
			out.WriteString(resp)
			break
		}
		if n == 0 {
			resp = resp[4:]
			continue
		}
		switch resp[4] {
		case 1, 2:
			out.WriteString(resp[5:n])
		default:
			out.WriteString(resp[4:n])
		}
		resp = resp[n:]
	}
	return out.String()
}

// pgrRunFixture is a bare repository with main and the run branch at one
// base commit, an unreferenced unreviewed commit and its pack, and a
// handler whose run reader binds alice to the run's current step.
type pgrRunFixture struct {
	repo, base, evil string
	pack             []byte
	runs             *pgrRuns
	defs             *pgrDefs
	h                *Handler
}

func pgrRunSetup(t *testing.T, repo string) *pgrRunFixture {
	t.Helper()
	f := &pgrRunFixture{}
	if repo == "" {
		f.repo, f.base, f.evil, f.pack = nulRepo(t, "", pgrRun)
		// nulRepo writes the unreviewed objects into the repository; they
		// must reach it only through the push.
		nulGit(t, f.repo, nil, "prune", "--expire=now")
	} else {
		f.repo = repo
		f.base = nulGit(t, repo, nil, "rev-parse", "refs/heads/"+pgrRun)
		blob := nulGit(t, repo, []byte("pwned\n"), "hash-object", "-w", "--stdin")
		tree := nulGit(t, repo, []byte("100644 blob "+blob+"\tEVIL.md\n"), "mktree")
		f.evil = nulGit(t, repo, nil, "commit-tree", tree, "-p", f.base, "-m", "unreviewed")
		cmd := exec.Command("git", "-C", repo, "pack-objects", "--stdout", "-q", "--revs")
		cmd.Env = nulGitEnv(t)
		cmd.Stdin = strings.NewReader(f.evil + "\n")
		var err error
		if f.pack, err = cmd.Output(); err != nil {
			t.Fatalf("pack-objects: %v", err)
		}
		// The served work tree must not carry the unreviewed objects
		// before the push: they live in the pack only.
		nulGit(t, repo, nil, "prune", "--expire=now")
	}
	f.runs = &pgrRuns{r: boundRun("alice", pgrSlug, pgrHex)}
	f.defs = &pgrDefs{typ: domain.StepTypeManual}
	f.h = pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return f.repo, nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.New(branchprotect.StaticRules(nil)),
		RunRefs:            f.runs,
		StepDefs:           f.defs,
		OpTimeout:          30 * time.Second,
	}, 0, 0, 0, 0)
	return f
}

func (f *pgrRunFixture) runTip(t *testing.T) string {
	t.Helper()
	return nulGit(t, f.repo, nil, "rev-parse", "refs/heads/"+pgrRun)
}

// TestPrereceive_FreezeRaceRefusedAtApply (N-9; ports the review's
// TestRV_PushAdmission_FreezeRace): a bound author announces a push of
// its run branch, holds the pack back, and the run's step moves on before
// the pack arrives. The update was admitted when the command section was
// parsed; the pre-receive hook's re-check, which runs once the pack is
// in and before any ref moves, refuses it. Without the change in state
// the same held push lands. At a560c210 (and at the TASK-005 base) the
// frozen push lands.
func TestPrereceive_FreezeRaceRefusedAtApply(t *testing.T) {
	cases := []struct {
		name   string
		change func(f *pgrRunFixture)
		want   string // "" = the push lands
	}{
		{name: "no change lands", change: func(*pgrRunFixture) {}},
		{
			name:   "step became a review step",
			change: func(f *pgrRunFixture) { f.defs.set(domain.StepTypeReview) },
			want:   "is frozen while its current step is a review or engine-internal step",
		},
		{
			name:   "step became an engine-internal step",
			change: func(f *pgrRunFixture) { f.defs.set(domain.StepTypeInternal) },
			want:   "is frozen while its current step is a review or engine-internal step",
		},
		{
			// Owner question 3's floor: whatever the freeze answer, the
			// bound execution must still be assigned or in progress.
			name: "execution was submitted",
			change: func(f *pgrRunFixture) {
				f.runs.mutate(func(r *admReader) {
					x := r.execs["run-"+pgrHex]
					x[0].Status = domain.StepStatusCompleted
				})
			},
			want: "only the actor assigned to the run's current step, or an operator, may push it",
		},
		{
			name: "step was reassigned",
			change: func(f *pgrRunFixture) {
				f.runs.mutate(func(r *admReader) {
					r.execs["run-"+pgrHex] = append(r.execs["run-"+pgrHex], domain.StepExecution{
						ExecutionID: "exec-2", RunID: "run-" + pgrHex, StepID: "execute", ActorID: "bob",
						Status: domain.StepStatusAssigned, RepositoryID: domain.PrimaryRepositoryID,
					})
				})
			},
			want: "only the actor assigned to the run's current step, or an operator, may push it",
		},
		{
			name: "run was paused",
			change: func(f *pgrRunFixture) {
				f.runs.mutate(func(r *admReader) { r.runs["run-"+pgrHex].Status = domain.RunStatusPaused })
			},
			want: "only the actor assigned to the run's current step, or an operator, may push it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := pgrRunSetup(t, "")
			cmd := pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun)
			resp := pgrDemux(pgrHeldPush(t, f.h, f.repo, pgrAlice, cmd, f.pack,
				func() bool { return f.defs.lookups() >= 1 },
				func() { tc.change(f) }))
			tip := f.runTip(t)
			if tc.want == "" {
				if tip != f.evil {
					t.Fatalf("the held push with no change did not land (tip %s):\n%q", tip, resp)
				}
				return
			}
			if tip != f.base {
				t.Fatalf("the run branch moved to %s after its step changed during the push:\n%q", tip, resp)
			}
			for _, want := range []string{"spine: push refused: ref-admission: ", tc.want, "pre-receive hook declined"} {
				if !strings.Contains(resp, want) {
					t.Errorf("response lacks %q:\n%q", want, resp)
				}
			}
			if objectExists(t, f.repo, f.evil) {
				t.Error("the refused push's commit is in the repository (quarantine not discarded)")
			}
			// Control: the same push announced now is refused before its
			// pack is read.
			if tc.name == "step became a review step" {
				w := pgrServe(f.h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request",
					bytes.NewReader(append(append([]byte{}, cmd...), f.pack...)), f.repo, "ws-1", pgrAlice, "")
				if !strings.Contains(w.Body.String(), "is frozen") {
					t.Errorf("a push announced during the review step was not refused up front:\n%q", w.Body.String())
				}
			}
		})
	}
}

// TestPrereceive_EarlyRejectStillFires (N-9): a ref the pre-pack
// decision refuses is still refused before the pack is read — the
// response comes back while the pack is never sent — and (N-13) the
// refused push never held a slot, nor reached the hook install.
func TestPrereceive_EarlyRejectStillFires(t *testing.T) {
	f := pgrRunSetup(t, "")
	heldDuringAdmission := -1
	f.runs.onRead = func() {
		if heldDuringAdmission < 0 {
			heldDuringAdmission = pgrSlotsHeld(f.h)
		}
	}
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan string, 1)
	go func() {
		w := pgrServe(f.h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", pr, f.repo, "ws-1", pgrMallory, "")
		done <- w.Body.String()
	}()
	if _, err := pw.Write(pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun)); err != nil {
		t.Fatal(err)
	}
	var resp string
	select {
	case resp = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the refused push waited for its pack")
	}
	for _, want := range []string{"ref-admission: " + pgrRun + " is a run branch", "ng refs/heads/" + pgrRun + " pre-receive hook declined"} {
		if !strings.Contains(resp, want) {
			t.Errorf("response lacks %q:\n%q", want, resp)
		}
	}
	if tip := f.runTip(t); tip != f.base {
		t.Fatalf("run branch moved to %s", tip)
	}
	f.runs.mutate(func(*admReader) {}) // synchronise with the handler's reads
	if heldDuringAdmission != 0 {
		t.Errorf("the push held %d git slot(s) while its command section was admitted, want 0", heldDuringAdmission)
	}
	if _, err := os.Lstat(filepath.Join(f.repo, "hooks", "pre-receive")); err == nil {
		t.Error("a push refused before its pack still installed the pre-receive hook")
	}
}

// pgrHold starts a request that holds a git slot — an upload-pack POST
// whose body never ends, so git-http-backend waits in it — as actor (nil:
// a token-less read from remoteAddr) in workspace ws. It returns ok=false
// and the status when the request was refused instead; stop ends the
// request and waits for it.
func pgrHold(t *testing.T, h *Handler, repo, ws string, actor *domain.Actor, remoteAddr string) (stop func(), status int, ok bool) {
	t.Helper()
	before := pgrSlotsHeld(h)
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		w := pgrServe(h, http.MethodPost, "/git-upload-pack", "application/x-git-upload-pack-request", pr, repo, ws, actor, remoteAddr)
		done <- w.Code
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case code := <-done:
			_ = pw.Close()
			return func() {}, code, false
		default:
		}
		if pgrSlotsHeld(h) > before {
			return func() {
				_ = pw.Close()
				<-done
			}, 0, true
		}
		if time.Now().After(deadline) {
			t.Fatal("a holding request neither took a slot nor answered")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// pgrTry is a quick read (the upload-pack ref advertisement) as actor in
// ws; it returns the status.
func pgrTry(h *Handler, repo, ws string, actor *domain.Actor, remoteAddr string) int {
	return pgrServe(h, http.MethodGet, "/info/refs?service=git-upload-pack", "", http.NoBody, repo, ws, actor, remoteAddr).Code
}

func pgrActor(id string) *domain.Actor {
	return &domain.Actor{ActorID: id, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
}

// TestGitHTTP_SlotsFairPerActorAndWorkspace (N-13): with the default
// caps, one actor holds at most its per-actor cap, the members of one
// workspace at most the per-workspace cap, and neither blocks an actor of
// another workspace; the process-wide ceiling still applies; a
// token-less read is keyed on its peer address, never on a shared empty
// actor. At a560c210 one actor held every slot (5) and every other
// tenant was answered 503.
func TestGitHTTP_SlotsFairPerActorAndWorkspace(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	}, 0, 0, 0, 0)
	perActor, perWorkspace, global := pgrDefaultCaps()
	var stops []func()
	defer func() {
		for _, s := range stops {
			s()
		}
	}()
	hold := func(ws string, actor *domain.Actor, addr string) bool {
		stop, _, ok := pgrHold(t, h, repo, ws, actor, addr)
		if ok {
			stops = append(stops, stop)
		}
		return ok
	}

	// 1. Actor A of ws-1 takes slots until refused.
	a := pgrActor("actor-a")
	held := 0
	for held < 10 && hold("ws-1", a, "") {
		held++
	}
	if held != perActor {
		t.Fatalf("actor A of ws-1 held %d git slots, want its per-actor cap %d", held, perActor)
	}
	// 2. ... and actor B of ws-2 is not blocked by it.
	if code := pgrTry(h, repo, "ws-2", pgrActor("actor-b"), ""); code != http.StatusOK {
		t.Fatalf("actor B of ws-2 got %d while actor A of ws-1 held its slots, want 200", code)
	}
	// 3. A second member of ws-1 fills the workspace's cap ...
	c := pgrActor("actor-c")
	for i := 0; i < perWorkspace-perActor; i++ {
		if !hold("ws-1", c, "") {
			t.Fatalf("actor C of ws-1 refused its slot %d", i+1)
		}
	}
	// ... so a third member of ws-1 is refused, and ws-2 is not.
	if code := pgrTry(h, repo, "ws-1", pgrActor("actor-d"), ""); code != http.StatusServiceUnavailable {
		t.Fatalf("actor D of ws-1 got %d with ws-1 at its per-workspace cap, want 503", code)
	}
	if code := pgrTry(h, repo, "ws-2", pgrActor("actor-b"), ""); code != http.StatusOK {
		t.Fatalf("actor B of ws-2 got %d with ws-1 at its cap, want 200", code)
	}
	// 4. The process-wide ceiling: other workspaces fill the rest.
	n := 0
	for ws := 2; n < global-perWorkspace; ws++ {
		for i := 0; i < perWorkspace/perActor && n < global-perWorkspace; i++ {
			if !hold(fmt.Sprintf("ws-%d", ws), pgrActor(fmt.Sprintf("actor-%d-%d", ws, i)), "") {
				t.Fatalf("ws-%d actor %d refused below the ceiling", ws, i)
			}
			n++
		}
	}
	if code := pgrTry(h, repo, "ws-9", pgrActor("actor-z"), ""); code != http.StatusServiceUnavailable {
		t.Fatalf("a request at the process-wide ceiling got %d, want 503", code)
	}
	for _, s := range stops {
		s()
	}
	stops = nil
	if got := pgrSlotsHeld(h); got != 0 {
		t.Fatalf("%d slot(s) still held after every request ended", got)
	}

	// 5. Token-less reads (a trusted-CIDR runner): keyed on the peer.
	held = 0
	for held < 10 && hold("ws-1", nil, "10.0.0.1:4000") {
		held++
	}
	if held != perActor {
		t.Fatalf("a token-less peer held %d slots, want the per-principal cap %d", held, perActor)
	}
	if code := pgrTry(h, repo, "ws-1", nil, "10.0.0.2:4000"); code != http.StatusOK {
		t.Fatalf("another token-less peer got %d, want 200 (peers must not share one empty-actor key)", code)
	}
	if code := pgrTry(h, repo, "ws-1", nil, "10.0.0.1:4001"); code != http.StatusServiceUnavailable {
		t.Fatalf("the same peer from another port got %d, want 503", code)
	}
	// An actor with an empty ID is keyed on its peer too.
	if code := pgrTry(h, repo, "ws-1", &domain.Actor{Role: domain.RoleContributor}, "10.0.0.3:4000"); code != http.StatusOK {
		t.Fatalf("an empty-ID actor from a fresh peer got %d, want 200", code)
	}
	if code := pgrTry(h, repo, "ws-1", &domain.Actor{Role: domain.RoleContributor}, "10.0.0.1:4002"); code != http.StatusServiceUnavailable {
		t.Fatalf("an empty-ID actor from the full peer got %d, want 503", code)
	}
}

// TestGitHTTP_SlowCommandSectionDoesNotHoldSlot (N-13): a client that
// stalls inside its command section holds no slot — with one slot in
// all, the same actor's other requests are served meanwhile — and is cut
// at the command-read deadline, long before the server's whole-request
// ReadTimeout (none, here). At a560c210 the stalled push held the slot
// until the client gave up.
func TestGitHTTP_SlowCommandSectionDoesNotHoldSlot(t *testing.T) {
	repo, base, evil, _ := nulRepo(t, "")
	const deadline = 300 * time.Millisecond
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
	}, 1, 1, 1, deadline)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(pgrCtx(r.Context(), repo, "ws-1", pgrAlice)))
	}))
	defer srv.Close()

	t.Run("stalled command section", func(t *testing.T) {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		line := pktLine(base + " " + evil + " refs/heads/feature\x00report-status\n")
		partial := line[:len(line)/2] // a pkt-line cut in the middle
		start := time.Now()
		fmt.Fprintf(conn, "POST /git-receive-pack HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n", len(line)+4+64)
		if _, err := conn.Write(partial); err != nil {
			t.Fatal(err)
		}
		time.Sleep(deadline / 3)
		if got := pgrSlotsHeld(h); got != 0 {
			t.Errorf("the stalled push holds %d slot(s), want 0", got)
		}
		res, err := http.Get(srv.URL + "/info/refs?service=git-upload-pack")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("the actor's read during the stalled push got %d, want 200 (the stalled push must hold no slot)", res.StatusCode)
		}

		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("the stalled push was not cut within 3s (deadline %s): %v", deadline, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if elapsed := time.Since(start); elapsed > deadline+2*time.Second {
			t.Errorf("the stalled push was answered after %s, want about %s", elapsed, deadline)
		}
		if !strings.Contains(string(body), "the command section was not received within "+deadline.String()) ||
			!strings.Contains(string(body), EnvCommandReadTimeout) {
			t.Errorf("refusal does not name the command-read deadline: %q", body)
		}
	})

	t.Run("stalled push-options section", func(t *testing.T) {
		// Through a ResponseWriter that cannot move the read deadline
		// (the recorder): the body is closed instead.
		pr, pw := io.Pipe()
		defer func() { _ = pw.Close() }()
		done := make(chan string, 1)
		start := time.Now()
		go func() {
			w := pgrServe(h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", pr, repo, "ws-1", pgrAlice, "")
			done <- w.Body.String()
		}()
		cmd := pktLine(base + " " + evil + " refs/heads/feature\x00report-status push-options\n")
		if _, err := pw.Write(append(cmd, flushPkt...)); err != nil {
			t.Fatal(err)
		}
		select {
		case resp := <-done:
			if !strings.Contains(resp, "the command section was not received within") {
				t.Errorf("response: %q", resp)
			}
			if elapsed := time.Since(start); elapsed > deadline+2*time.Second {
				t.Errorf("answered after %s", elapsed)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a push stalled in its push-options section was not cut")
		}
	})
}

// pgrFoldingProbe reports folding storage and counts its calls.
type pgrFoldingProbe struct {
	mu    sync.Mutex
	folds bool
	err   error
	calls int
}

func (p *pgrFoldingProbe) probe(context.Context, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.folds, p.err
}

func (p *pgrFoldingProbe) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *pgrFoldingProbe) set(folds bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.folds, p.err = folds, err
}

// TestReceivePack_RefusedOnCaseFoldingStorage (N-8): a repository whose
// storage the probe finds folding (faked: CI runs on a case-sensitive
// filesystem) refuses receive-pack — the push advertisement and the push
// itself, before git sees it, with the storage requirement named, and
// git-http-backend's own http.receivepack set false — while clones and
// fetches are served. The review's TestRV_F1_CaseFoldRef push
// (refs/heads/MAIN carrying main's next commit) is refused at the gate. A
// probe that cannot answer refuses pushes and is asked again. On a
// case-sensitive store the push lands. At a560c210 receive-pack was
// served on any storage.
func TestReceivePack_RefusedOnCaseFoldingStorage(t *testing.T) {
	repo, base, evil, pack := nulRepo(t, "")
	nulGit(t, repo, nil, "prune", "--expire=now") // the objects travel in the pack only
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.New(branchprotect.StaticRules(nil)),
	}, 0, 0, 0, 0)
	probe := &pgrFoldingProbe{folds: true}
	pgrSetStorageProbe(h, probe.probe)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(pgrCtx(r.Context(), repo, "ws-1", pgrAlice)))
	}))
	defer srv.Close()

	adv := pgrServe(h, http.MethodGet, "/info/refs?service=git-receive-pack", "", http.NoBody, repo, "ws-1", pgrAlice, "")
	if adv.Code != http.StatusForbidden || !strings.Contains(adv.Body.String(), "case- or normalization-insensitive filesystem") {
		t.Fatalf("push advertisement on folding storage: %d %q, want 403 naming the storage requirement", adv.Code, adv.Body.String())
	}
	for _, ref := range []string{"refs/heads/MAIN", "refs/heads/Main"} {
		w := pgrServe(h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request",
			bytes.NewReader(append(pgrCommand(base, evil, ref), pack...)), repo, "ws-1", pgrAlice, "")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "KNOWN-LIMITATIONS") {
			t.Errorf("push of %s on folding storage: %d %q, want 403 naming the requirement", ref, w.Code, w.Body.String())
		}
		if nulRefExists(t, repo, ref) {
			t.Errorf("%s was created", ref)
		}
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("main moved to %s", got)
	}
	if objectExists(t, repo, evil) {
		t.Fatal("the refused push's objects reached the repository")
	}
	if got := nulGit(t, repo, nil, "config", "--local", "http.receivepack"); got != "false" {
		t.Errorf("http.receivepack = %q on folding storage, want false", got)
	}
	// Reads are served: the fetch advertisement and a real clone.
	if code := pgrTry(h, repo, "ws-1", pgrAlice, ""); code != http.StatusOK {
		t.Fatalf("fetch advertisement on folding storage: %d, want 200", code)
	}
	work := t.TempDir()
	clone := exec.Command("git", "clone", "-q", srv.URL+"/", work)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone from folding storage: %v %s", err, out)
	}
	if n := probe.n(); n != 1 {
		t.Errorf("the probe ran %d times for one repository, want once", n)
	}

	t.Run("the review's case-fold push against the real probe", func(t *testing.T) {
		// TestRV_F1_CaseFoldRef: refs/heads/MAIN (and Main) carrying
		// main's next commit, with main packed and loose. On folding
		// storage (a macOS host) the real probe refuses it before git
		// sees it; on case-sensitive storage (Linux CI) it is an
		// unrelated branch. main never moves.
		for _, loose := range []bool{false, true} {
			for _, ref := range []string{"refs/heads/MAIN", "refs/heads/Main"} {
				r, b, e, pk := nulRepo(t, "")
				if loose {
					other := nulGit(t, r, nil, "commit-tree", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "-p", b, "-m", "tmp")
					nulGit(t, r, nil, "update-ref", "refs/heads/main", other)
					nulGit(t, r, nil, "update-ref", "refs/heads/main", b)
				}
				hr := pgrHandler(t, Config{
					ResolveRepoPath:    func(context.Context, string) (string, error) { return r, nil },
					ReceivePackEnabled: true,
					Policy:             branchprotect.New(branchprotect.StaticRules(nil)),
				}, 0, 0, 0, 0)
				pgrSetStorageProbe(hr, probeRefStorage)
				folds, err := probeRefStorage(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				w := pgrServe(hr, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request",
					bytes.NewReader(append(pgrCommand(b, e, ref), pk...)), r, "ws-1", pgrAlice, "")
				if folds && w.Code != http.StatusForbidden {
					t.Errorf("loose=%v %s on folding storage: %d %q, want the 403 storage refusal", loose, ref, w.Code, w.Body.String())
				}
				if got := nulGit(t, r, nil, "rev-parse", "refs/heads/main"); got != b {
					t.Errorf("loose=%v %s (storage folds: %v): main moved to %s", loose, ref, folds, got)
				}
			}
		}
	})

	t.Run("probe failure refuses pushes and is retried", func(t *testing.T) {
		repo2, _, _, _ := nulRepo(t, "")
		h2 := pgrHandler(t, Config{
			ResolveRepoPath:    func(context.Context, string) (string, error) { return repo2, nil },
			ReceivePackEnabled: true,
		}, 0, 0, 0, 0)
		p := &pgrFoldingProbe{err: fmt.Errorf("disk on fire")}
		pgrSetStorageProbe(h2, p.probe)
		if w := pgrServe(h2, http.MethodGet, "/info/refs?service=git-receive-pack", "", http.NoBody, repo2, "ws-1", pgrAlice, ""); w.Code != http.StatusInternalServerError {
			t.Errorf("push advertisement after a failed probe: %d %q, want 500", w.Code, w.Body.String())
		}
		if code := pgrTry(h2, repo2, "ws-1", pgrAlice, ""); code != http.StatusOK {
			t.Errorf("fetch advertisement after a failed probe: %d, want 200", code)
		}
		p.set(false, nil)
		if w := pgrServe(h2, http.MethodGet, "/info/refs?service=git-receive-pack", "", http.NoBody, repo2, "ws-1", pgrAlice, ""); w.Code != http.StatusOK {
			t.Errorf("push advertisement once the probe answers: %d %q, want 200", w.Code, w.Body.String())
		}
		if n := p.n(); n != 3 {
			t.Errorf("probe ran %d times, want 3 (no failed answer is cached)", n)
		}
	})

	t.Run("case-sensitive storage admits the push", func(t *testing.T) {
		repo3, _, _, _ := nulRepo(t, "")
		h3 := pgrHandler(t, Config{
			ResolveRepoPath:    func(context.Context, string) (string, error) { return repo3, nil },
			ReceivePackEnabled: true,
			Policy:             branchprotect.New(branchprotect.StaticRules(nil)),
		}, 0, 0, 0, 0)
		pgrSetStorageProbe(h3, (&pgrFoldingProbe{}).probe)
		srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h3.ServeHTTP(w, r.WithContext(pgrCtx(r.Context(), repo3, "ws-1", pgrAlice)))
		}))
		defer srv3.Close()
		work := t.TempDir()
		clone := exec.Command("git", "clone", "-q", srv3.URL+"/", work)
		clone.Env = nulGitEnv(t)
		if out, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v %s", err, out)
		}
		nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "feature")
		push := exec.Command("git", "-C", work, "push", "-q", srv3.URL+"/", "HEAD:refs/heads/feature")
		push.Env = nulGitEnv(t)
		if out, err := push.CombinedOutput(); err != nil {
			t.Fatalf("push on case-sensitive storage: %v %s", err, out)
		}
		if !nulRefExists(t, repo3, "refs/heads/feature") {
			t.Fatal("feature did not land")
		}
		if got := nulGit(t, repo3, nil, "config", "--local", "http.receivepack"); got != "true" {
			t.Errorf("http.receivepack = %q, want true", got)
		}
	})
}
