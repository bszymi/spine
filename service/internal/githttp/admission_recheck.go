package githttp

// The apply-time admission re-check (INIT-024 TASK-005, review finding
// N-9).
//
// The pre-receive gate (prereceive) admits every ref update when the
// command section is parsed, before the pack is streamed. The run's step
// state was read at that moment only, so a pusher could announce an
// update while bound to a live step, hold the pack back until the step
// had moved on — to a review step, or to another actor — and the update
// still landed. (Merge binding still only merges the reviewed commit;
// this keeps the branch itself honest.)
//
// The checkout-budget pre-receive hook (budget_hook.go) is the one place
// that runs after git has received the pack and before any ref moves,
// with exactly the (old, new, ref) triples git is about to apply. It now
// has a second job: after the budget measure it asks the server to decide
// admission again, and refuses the push unless the server admits every
// update at that moment.
//
//   - Channel. The hook is a fresh process with only budget flags in a
//     script shared by every push to the repository, so per-push data
//     travels in the CGI environment of this push only: the path of a
//     Unix socket and a one-time token. The socket lives in a fresh 0700
//     directory (only this server's user can reach it); the token is 32
//     random bytes, compared in constant time, accepted once and only for
//     this push's socket; both disappear when the push's CGI returns.
//     Neither is ever logged, and the hook drops both from its own
//     environment before it starts any process. git passes the same
//     environment to the repository's other hooks for that push (update,
//     post-receive and the like, when an operator keeps them in the
//     pinned hooks directory): by then the token is spent and the socket
//     no longer listens, so it is of no use to them (review round 1).
//   - Decision. The server re-decides with the same evaluator the
//     pre-pack gate used (refadmit, one admission table), over the same
//     per-request run reader and step-definition lookup, the same actor
//     and repository, with nothing memoised from the pre-pack decision:
//     a run or divergence branch is admitted only while its bound
//     execution is still the actor's, still assigned or in progress, and
//     its step still not frozen (owner question 3: the admission rule at
//     apply time, with the review freeze exactly as INIT-023 recorded it
//     — review and internal steps, non-operators). Every update git is
//     about to apply must also be one the command section announced; one
//     that is not is refused.
//   - Fail closed. The hook refuses the push when the variables are
//     malformed, the socket cannot be reached, or no well-formed answer
//     arrives before its deadline. A hook started without the variables
//     is not serving a push of this endpoint (an operator's local push
//     into the repository): there is no command-section decision to
//     re-check, and it runs the budget measure only.
//
// Residual: the window between the server's answer and git's ref update
// (one process exit) remains; branch-protection rules are evaluated once,
// on the command section.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/refadmit"
)

// The CGI environment of a receive-pack request carries the re-check
// channel to the hook. Internal: set by ServeHTTP, read (and unset) by
// PreReceiveHookMain.
const (
	EnvRecheckSocket = "SPINE_INTERNAL_PUSH_RECHECK_SOCKET"
	EnvRecheckToken  = "SPINE_INTERNAL_PUSH_RECHECK_TOKEN"
)

const (
	// recheckHello opens a request; a different version is refused.
	recheckHello = "spine-push-recheck 1"
	// recheckTokenBytes is the token's length before hex encoding.
	recheckTokenBytes = 32
	// maxRecheckReplyBytes bounds the hook's read of the answer.
	maxRecheckReplyBytes = 64 << 10
	// maxRecheckLines bounds the lines one request may announce: the
	// hook's input cap over the shortest line ("<40 hex> <40 hex> r\n").
	maxRecheckLines = maxHookInputBytes / 84
	// maxRecheckSocketBase keeps "<base>/spine-push-<n>/s" inside the
	// 104-byte sun_path of macOS (108 on Linux).
	maxRecheckSocketBase = 64
)

// recheckDecider re-decides the updates git is about to apply and returns
// the refusal messages; none means every update is admitted.
type recheckDecider func(ctx context.Context, lines []RefUpdate) []string

// recheckSession is the server side of one push's re-check channel.
type recheckSession struct {
	dir, socket, token string
	ln                 net.Listener
	decide             recheckDecider
	ioTimeout          time.Duration
	// ctx is the decisions' context: the request's, cancelled by close
	// (codex review round 1). A decision also stops at its connection's
	// deadline, so a stalled store read can neither outlive the push nor
	// keep close — and the push's git slot — waiting.
	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup
	// used is set by the first request that presents the token: the
	// token is single-use, and the listener is closed then (review round
	// 1, P4-7).
	used atomic.Bool

	// conns are the accepted connections still being served; close
	// closes them, so a connection that stalls cannot hold close — and
	// the push's git slot — until its deadline.
	connMu sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// startRecheck opens the channel of one push. The decision's store reads
// run under ctx, cut at the connection's deadline (ioTimeout after it was
// accepted) and when the session is closed. The caller must close the
// session once the push's CGI has returned.
func startRecheck(parent context.Context, ioTimeout time.Duration, decide recheckDecider) (*recheckSession, error) {
	ctx, cancel := context.WithCancel(parent)
	s, err := openRecheck(ctx, ioTimeout, decide)
	if err != nil {
		cancel()
		return nil, err
	}
	s.ctx, s.cancel = ctx, cancel
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// openRecheck creates the session's directory, token and listener.
func openRecheck(ctx context.Context, ioTimeout time.Duration, decide recheckDecider) (*recheckSession, error) {
	base := os.TempDir()
	if len(base) > maxRecheckSocketBase {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "spine-push-")
	if err != nil {
		return nil, fmt.Errorf("push-admission re-check: %w", err)
	}
	tok := make([]byte, recheckTokenBytes)
	if _, err := rand.Read(tok); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("push-admission re-check: %w", err)
	}
	socket := filepath.Join(dir, "s")
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("push-admission re-check: %w", err)
	}
	return &recheckSession{
		dir: dir, socket: socket, token: hex.EncodeToString(tok),
		ln: ln, decide: decide, ioTimeout: ioTimeout,
		conns: map[net.Conn]struct{}{},
	}, nil
}

// env is the session's part of the CGI environment.
func (s *recheckSession) env() []string {
	return []string{EnvRecheckSocket + "=" + s.socket, EnvRecheckToken + "=" + s.token}
}

// close cancels any decision still running, stops the listener, waits
// for every connection handler and removes the socket's directory.
func (s *recheckSession) close() {
	s.cancel()
	_ = s.ln.Close()
	s.connMu.Lock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
	s.connMu.Unlock()
	s.wg.Wait()
	_ = os.RemoveAll(s.dir)
}

func (s *recheckSession) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // closed
		}
		s.connMu.Lock()
		if s.closed {
			s.connMu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.connMu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.connMu.Lock()
				delete(s.conns, conn)
				s.connMu.Unlock()
			}()
			s.handle(conn)
		}()
	}
}

// handle serves one connection: hello, token, a count and that many
// "<old> <new> <ref>" lines; then "ok" or "refuse" and one message per
// line. A connection without the token gets no answer.
func (s *recheckSession) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(s.ioTimeout)
	_ = conn.SetDeadline(deadline)
	sc := bufio.NewScanner(io.LimitReader(conn, maxHookInputBytes+4096))
	sc.Buffer(make([]byte, 0, 4096), maxHookLineBytes)
	next := func() (string, bool) {
		if !sc.Scan() {
			return "", false
		}
		return sc.Text(), true
	}
	if hello, ok := next(); !ok || hello != recheckHello {
		return
	}
	tok, ok := next()
	if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
		return
	}
	if !s.used.CompareAndSwap(false, true) {
		return
	}
	// Spent: nothing else is accepted on this push's socket.
	_ = s.ln.Close()
	reply := func(msgs []string) {
		var b strings.Builder
		if len(msgs) == 0 {
			b.WriteString("ok\n")
		} else {
			b.WriteString("refuse\n")
			for _, m := range msgs {
				b.WriteString(strings.NewReplacer("\n", " ", "\r", " ").Replace(m))
				b.WriteByte('\n')
			}
		}
		_, _ = io.WriteString(conn, b.String())
	}
	malformed := []string{"push refused: the pre-receive hook's re-check request was malformed (fail closed)"}
	countLine, ok := next()
	n, err := strconv.Atoi(countLine)
	if !ok || err != nil || n < 0 || n > maxRecheckLines {
		reply(malformed)
		return
	}
	lines := make([]RefUpdate, 0, n)
	for i := 0; i < n; i++ {
		l, ok := next()
		if !ok {
			reply(malformed)
			return
		}
		f := strings.Split(l, " ")
		if len(f) != 3 || !git.IsFullObjectName(f[0]) || !git.IsFullObjectName(f[1]) || len(f[0]) != len(f[1]) || f[2] == "" {
			reply(malformed)
			return
		}
		lines = append(lines, RefUpdate{OldSHA: f[0], NewSHA: f[1], Ref: f[2]})
	}
	// The decision ends with the connection. A decision that returns
	// after its context ended (the deadline passed, or close) gets no
	// answer, whatever it returned, so the hook refuses. The connection's
	// deadline alone does not stop the write: its timer is separate from
	// the context's, so an answer could still go out just after the
	// deadline (INIT-024 TASK-012).
	dctx, cancel := context.WithDeadline(s.ctx, deadline)
	defer cancel()
	msgs := s.decide(dctx, lines)
	if dctx.Err() != nil {
		return
	}
	reply(msgs)
}

// recheckAdmission is the server's apply-time decision for a push the
// pre-pack gate admitted (p): every update git is about to apply (lines)
// must be one the command section announced, and must be admitted again,
// now, by a fresh evaluator over the same inputs.
func (h *Handler) recheckAdmission(ctx context.Context, log *slog.Logger, p *admittedPush, lines []RefUpdate) []string {
	key := func(u RefUpdate) [3]string {
		return [3]string{strings.ToLower(u.OldSHA), strings.ToLower(u.NewSHA), u.Ref}
	}
	announced := map[[3]string]int{}
	for _, u := range p.updates {
		announced[key(u)]++
	}
	for _, l := range lines {
		k := key(l)
		if announced[k] == 0 {
			log.Error("apply-time ref admission: git is about to apply an update the command section did not announce; push refused",
				"ref", l.Ref)
			return []string{"ref-admission: git is about to apply a ref update this push's command section did not announce; push refused"}
		}
		announced[k]--
	}
	actorID := ""
	if p.actor != nil {
		actorID = p.actor.ActorID
	}
	adm := refadmit.NewEvaluator(p.runs).WithStepDefs(p.defs)
	var msgs []string
	for _, l := range lines {
		dec, err := adm.Decide(ctx, refadmit.Request{
			Ref:          l.Ref,
			Delete:       l.IsDelete(),
			Actor:        p.actor,
			RepositoryID: p.repositoryID,
		})
		if err != nil {
			log.Error("apply-time ref admission evaluation failed, rejecting push",
				"ref", l.Ref, "actor_id", actorID, "error", err)
			msgs = append(msgs, refadmit.EvaluationErrorMessage(l.Ref))
			continue
		}
		if !dec.Allow {
			log.Warn("apply-time ref admission denied: the update was admitted when the push was announced, not when it was applied",
				"ref", l.Ref, "class", dec.Class, "reason", dec.Reason,
				"actor_id", actorID, "run_id", dec.RunID)
			msgs = append(msgs, dec.Message())
		}
	}
	return msgs
}

// recheckClient is the hook's side of the channel, from its environment.
type recheckClient struct{ socket, token string }

// errRecheckMisconfigured: the variables are present but not usable.
var errRecheckMisconfigured = errors.New("push-admission re-check variables are malformed")

// recheckFromEnv reads the channel from the hook's environment and
// removes it, so no process the hook starts inherits the token. (nil,
// nil): no channel — the hook is not serving a push of this endpoint.
func recheckFromEnv() (*recheckClient, error) {
	socket, token := os.Getenv(EnvRecheckSocket), os.Getenv(EnvRecheckToken)
	_ = os.Unsetenv(EnvRecheckSocket)
	_ = os.Unsetenv(EnvRecheckToken)
	if socket == "" && token == "" {
		return nil, nil
	}
	if socket == "" || len(token) != 2*recheckTokenBytes {
		return nil, errRecheckMisconfigured
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, errRecheckMisconfigured
	}
	return &recheckClient{socket: socket, token: token}, nil
}

// errRecheckFailed: the exchange with the server did not complete.
var errRecheckFailed = errors.New("push-admission re-check did not complete")

// check sends the "<old> <new> <ref>" lines git is about to apply and
// returns the server's refusal messages (none: admitted). Any failure to
// get a well-formed answer before ctx's deadline is errRecheckFailed.
func (c *recheckClient) check(ctx context.Context, lines []string) ([]string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, errRecheckFailed
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n%d\n", recheckHello, c.token, len(lines))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, errRecheckFailed
	}
	reply, err := io.ReadAll(io.LimitReader(conn, maxRecheckReplyBytes+1))
	if err != nil || len(reply) > maxRecheckReplyBytes {
		return nil, errRecheckFailed
	}
	text := string(reply)
	switch {
	case text == "ok\n":
		return nil, nil
	case strings.HasPrefix(text, "refuse\n") && strings.HasSuffix(text, "\n"):
		var msgs []string
		for _, m := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, "refuse\n"), "\n"), "\n") {
			msgs = append(msgs, strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, m))
		}
		if len(msgs) == 0 || (len(msgs) == 1 && msgs[0] == "") {
			msgs = []string{"push refused at apply time"}
		}
		return msgs, nil
	}
	return nil, errRecheckFailed
}
