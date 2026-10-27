package githttp

// INIT-024 TASK-005: the apply-time admission re-check's channel and the
// storage probe, below the AC tests of push_gate_races_test.go.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

// recheckRequest writes one request to the session's socket and returns
// the raw answer ("" when the server closed without one).
func recheckRequest(t *testing.T, s *recheckSession, hello, token string, lines ...string) string {
	t.Helper()
	conn, err := net.Dial("unix", s.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "%s\n%s\n%d\n", hello, token, len(lines))
	for _, l := range lines {
		fmt.Fprintf(conn, "%s\n", l)
	}
	b, _ := io.ReadAll(conn)
	return string(b)
}

// TestRecheck_ChannelIsPrivateSingleUseAndTokenBound: the socket's
// directory is private, a request without the token (or with another
// push's) gets no answer and no decision, the token is accepted once,
// malformed requests are refused, and closing the session removes the
// directory.
func TestRecheck_ChannelIsPrivateSingleUseAndTokenBound(t *testing.T) {
	var mu sync.Mutex
	decided := 0
	s, err := startRecheck(context.Background(), 5*time.Second, func(_ context.Context, lines []RefUpdate) []string {
		mu.Lock()
		defer mu.Unlock()
		decided++
		if len(lines) == 1 && lines[0].Ref == "refs/heads/bad" {
			return []string{"refused\nwith a newline"}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := startRecheck(context.Background(), 5*time.Second, func(context.Context, []RefUpdate) []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()

	fi, err := os.Stat(s.dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
	}
	env := strings.Join(s.env(), "\n")
	if !strings.Contains(env, EnvRecheckSocket+"="+s.socket) || !strings.Contains(env, EnvRecheckToken+"="+s.token) || len(s.token) != 64 {
		t.Fatalf("env = %q", env)
	}
	line := strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " refs/heads/x"
	if got := recheckRequest(t, s, recheckHello, other.token, line); got != "" {
		t.Errorf("another push's token got an answer: %q", got)
	}
	if got := recheckRequest(t, s, recheckHello, strings.Repeat("0", 64), line); got != "" {
		t.Errorf("a wrong token got an answer: %q", got)
	}
	if got := recheckRequest(t, s, "spine-push-recheck 2", s.token, line); got != "" {
		t.Errorf("another protocol version got an answer: %q", got)
	}
	mu.Lock()
	if decided != 0 {
		t.Fatalf("decided %d times before the token was presented", decided)
	}
	mu.Unlock()
	if got := recheckRequest(t, s, recheckHello, s.token, line); got != "ok\n" {
		t.Fatalf("the push's own request: %q, want ok", got)
	}
	if got := recheckRequest(t, s, recheckHello, s.token, line); got != "" {
		t.Errorf("the token was accepted twice: %q", got)
	}

	// Malformed requests, each on a fresh session.
	for _, tc := range []struct{ name, line string }{
		{"two fields", strings.Repeat("a", 40) + " refs/heads/x"},
		{"short id", "abc " + strings.Repeat("b", 40) + " refs/heads/x"},
		{"mixed id lengths", strings.Repeat("a", 40) + " " + strings.Repeat("b", 64) + " refs/heads/x"},
		{"empty ref", strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " "},
	} {
		s2, err := startRecheck(context.Background(), 5*time.Second, func(context.Context, []RefUpdate) []string { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if got := recheckRequest(t, s2, recheckHello, s2.token, tc.line); !strings.HasPrefix(got, "refuse\n") {
			t.Errorf("%s: %q, want a refusal", tc.name, got)
		}
		s2.close()
	}
	s3, err := startRecheck(context.Background(), 5*time.Second, func(_ context.Context, lines []RefUpdate) []string {
		return []string{"refused\nwith a newline"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := recheckRequest(t, s3, recheckHello, s3.token, line); got != "refuse\nrefused with a newline\n" {
		t.Errorf("a message is one line: %q", got)
	}
	s3.close()

	dir := s.dir
	s.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the socket directory survived close: %v", err)
	}
}

// TestRecheck_HookFailsClosed: the hook refuses a push when its re-check
// variables are malformed, when the server cannot be reached, or when the
// answer is not one it understands — and drops the variables from its
// environment before it starts any process. Without the variables (a
// push not served by this endpoint) it runs the budget measure only.
func TestRecheck_HookFailsClosed(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	t.Chdir(repo)
	t.Setenv("GIT_DIR", ".")
	run := func(socket, token string) (string, error) {
		t.Setenv(EnvRecheckSocket, socket)
		t.Setenv(EnvRecheckToken, token)
		var stderr bytes.Buffer
		err := PreReceiveHookMain(context.Background(), []string{"--timeout=5s"}, strings.NewReader(""), &stderr)
		if os.Getenv(EnvRecheckSocket) != "" || os.Getenv(EnvRecheckToken) != "" {
			t.Errorf("the hook left the re-check variables in its environment")
		}
		return stderr.String(), err
	}
	tok := strings.Repeat("ab", 32)
	if out, err := run("", ""); err != nil {
		t.Fatalf("no channel: %v %s", err, out)
	}
	for _, tc := range []struct{ name, socket, token, want string }{
		{"socket without token", "/nonexistent/s", "", "malformed"},
		{"token without socket", "", tok, "malformed"},
		{"short token", "/nonexistent/s", "abcd", "malformed"},
		{"non-hex token", "/nonexistent/s", strings.Repeat("zz", 32), "malformed"},
		{"unreachable server", filepath.Join(t.TempDir(), "s"), tok, "could not be completed"},
	} {
		out, err := run(tc.socket, tc.token)
		if err == nil || !strings.Contains(out, "spine: push refused: ") || !strings.Contains(out, tc.want) {
			t.Errorf("%s: err=%v stderr=%q, want a refusal naming %q", tc.name, err, out, tc.want)
		}
		if strings.Contains(out, tok) {
			t.Errorf("%s: the token reached stderr", tc.name)
		}
	}

	// A server that answers garbage, or nothing.
	for _, answer := range []string{"maybe\n", "", "ok", "refuse"} {
		dir, err := os.MkdirTemp("", "spine-rc-test-")
		if err != nil {
			t.Fatal(err)
		}
		sock := filepath.Join(dir, "s")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		go func(answer string) {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(conn).ReadString('\n')
			_, _ = io.WriteString(conn, answer)
			_ = conn.Close()
		}(answer)
		out, err := run(sock, tok)
		if err == nil || !strings.Contains(out, "could not be completed") {
			t.Errorf("answer %q: err=%v stderr=%q, want a fail-closed refusal", answer, err, out)
		}
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	}
}

// TestRecheck_UnannouncedUpdateIsRefused: every update git is about to
// apply must be one the command section announced (object ids compared
// in either case, as git's own parser reads them), else the push is
// refused without an admission read.
func TestRecheck_UnannouncedUpdateIsRefused(t *testing.T) {
	h := &Handler{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	runs := &pgrRuns{r: boundRun("alice", pgrSlug, pgrHex)}
	p := &admittedPush{
		updates: []RefUpdate{{OldSHA: strings.ToUpper(a), NewSHA: b, Ref: "refs/heads/feature"}},
		actor:   pgrAlice, runs: runs, defs: &pgrDefs{typ: domain.StepTypeManual},
	}
	if msgs := h.recheckAdmission(context.Background(), log, p, []RefUpdate{{OldSHA: a, NewSHA: b, Ref: "refs/heads/feature"}}); len(msgs) != 0 {
		t.Fatalf("the announced update was refused: %v", msgs)
	}
	for _, lines := range [][]RefUpdate{
		{{OldSHA: a, NewSHA: b, Ref: "refs/heads/other"}},
		{{OldSHA: b, NewSHA: a, Ref: "refs/heads/feature"}},
		{{OldSHA: a, NewSHA: b, Ref: "refs/heads/feature"}, {OldSHA: a, NewSHA: b, Ref: "refs/heads/feature"}},
	} {
		msgs := h.recheckAdmission(context.Background(), log, p, lines)
		if len(msgs) != 1 || !strings.Contains(msgs[0], "did not announce") {
			t.Errorf("%v: %v, want the unannounced-update refusal", lines, msgs)
		}
	}
	if runs.r.calls != 0 {
		t.Errorf("an unannounced update cost %d store reads", runs.r.calls)
	}
}

// TestRecheck_RunsWithRenormalisationPending (TASK-003 e57a6741): a
// served work tree whose renormalisation is still pending lets a push
// through with a Warn — and the apply-time re-check still runs: the held
// push whose step became a review step is refused, the one whose step
// did not lands.
func TestRecheck_RunsWithRenormalisationPending(t *testing.T) {
	setup := func(t *testing.T) *pgrRunFixture {
		repo := t.TempDir()
		nulGit(t, repo, nil, "init", "-q", "-b", "main")
		if err := os.MkdirAll(filepath.Join(repo, "d"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "d", "x"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		nulGit(t, repo, nil, "add", "-A")
		nulGit(t, repo, nil, "commit", "-q", "-m", "base")
		nulGit(t, repo, nil, "branch", pgrRun)
		if err := os.RemoveAll(filepath.Join(repo, "d")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "d"), []byte("in the way\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return pgrRunSetup(t, repo)
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, review := range []bool{false, true} {
		f := setup(t)
		resp := pgrDemux(pgrHeldPush(t, f.h, f.repo, pgrAlice, pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun), f.pack,
			func() bool { return f.defs.lookups() >= 1 },
			func() {
				if review {
					f.defs.set(domain.StepTypeReview)
				}
			}))
		tip := f.runTip(t)
		if review && (tip != f.base || !strings.Contains(resp, "is frozen")) {
			t.Errorf("renormalisation pending: the frozen push was not refused at apply time (tip %s):\n%q", tip, resp)
		}
		if !review && tip != f.evil {
			t.Errorf("renormalisation pending: the push was refused (tip %s):\n%q", tip, resp)
		}
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logs.String(), "not yet brought to its stored bytes") {
		t.Fatalf("the renormalisation was not pending:\n%s", logs.String())
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestPrereceive_OverrideAuditOnlyOnceThePushReachesGit: the
// branch_protection.override event of an honoured operator override is
// written once the push holds its slot, hook and re-check — a push
// answered 503 is no override attempt and writes none, as when the slot
// was taken first.
func TestPrereceive_OverrideAuditOnlyOnceThePushReachesGit(t *testing.T) {
	repo, base, evil, pack := nulRepo(t, "")
	h := pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
		Policy: branchprotect.New(branchprotect.StaticRules([]config.Rule{
			{Branch: "main", Protections: []config.RuleKind{config.KindNoDirectWrite}},
		})),
	}, 0, 0, 1, 0)
	op := &domain.Actor{ActorID: "op", Role: domain.RoleOperator, Status: domain.ActorStatusActive}
	body := nulBody([]string{base + " " + evil + " refs/heads/main\x00report-status push-options\n"}, []string{"spine.override=true"}, pack)
	push := func(em *nulEmitter) int {
		req, _ := http.NewRequest(http.MethodPost, "/git-receive-pack", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req.WithContext(WithEvents(pgrCtx(req.Context(), repo, "ws-1", op), em)))
		return w.Code
	}
	release, _ := h.slots.acquire(slotPrincipal{workspace: "ws-other", id: "someone"})
	em := &nulEmitter{}
	if code := push(em); code != http.StatusServiceUnavailable {
		t.Fatalf("push with the only slot taken: %d, want 503", code)
	}
	if n := em.count(); n != 0 {
		t.Fatalf("a push answered 503 wrote %d override event(s)", n)
	}
	release()
	em = &nulEmitter{}
	if code := push(em); code != http.StatusOK {
		t.Fatalf("push: %d", code)
	}
	if n := em.count(); n != 1 {
		t.Fatalf("the honoured override wrote %d event(s), want 1", n)
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != evil {
		t.Fatalf("main = %s, want the overridden push %s", got, evil)
	}
}

// TestStorageProbe_MatchesTheHostFilesystem: the built-in probe answers
// what the filesystem does — checked against an independent test of the
// host's temporary directory (Linux: neither folds; macOS APFS: both;
// a case-sensitive APFS volume: Unicode normalization only) — and leaves
// nothing in the repository's refs directory.
func TestStorageProbe_MatchesTheHostFilesystem(t *testing.T) {
	oracle := t.TempDir()
	aliases := func(created, other string) bool {
		if err := os.WriteFile(filepath.Join(oracle, created), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(oracle, other))
		return err == nil
	}
	foldsCase := aliases("probe", "PROBE")
	foldsNorm := aliases("caf\u00e9", "cafe\u0301")
	hostFolds := foldsCase || foldsNorm
	t.Logf("host temporary directory folds case: %v, Unicode normalization: %v", foldsCase, foldsNorm)

	repo, _, _, _ := nulRepo(t, "")
	before, _ := os.ReadDir(filepath.Join(repo, "refs"))
	folds, err := probeRefStorage(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if folds != hostFolds {
		t.Fatalf("probe says folds=%v, the host's filesystem says %v", folds, hostFolds)
	}
	after, _ := os.ReadDir(filepath.Join(repo, "refs"))
	if len(after) != len(before) {
		t.Fatalf("the probe left entries in refs/: before %v after %v", before, after)
	}
	if out := nulGit(t, repo, nil, "for-each-ref", "--format=%(refname)"); strings.Contains(out, "probe") {
		t.Fatalf("a probe ref is visible: %s", out)
	}
	// A work tree's refs live in its .git directory.
	work := t.TempDir()
	nulGit(t, work, nil, "init", "-q")
	if folds, err := probeRefStorage(context.Background(), work); err != nil || folds != hostFolds {
		t.Fatalf("work tree: folds=%v err=%v, want %v", folds, err, hostFolds)
	}
	// Not a repository: an error, never "case-sensitive".
	if _, err := probeRefStorage(context.Background(), t.TempDir()); err == nil {
		t.Fatal("the probe of a non-repository answered")
	}
}
