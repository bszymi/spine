package checkrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// maxPolicyTimeoutSeconds is the largest TimeoutSeconds value we'll
// translate into a time.Duration. The product
// time.Duration(seconds)*time.Second is an int64 nanosecond count;
// values above MaxInt64/1e9 (~292 years) overflow into a negative
// duration, which context.WithTimeout interprets as an already-
// expired deadline — fast checks would then be reported as
// OutcomeTimeout (codex pass 12 P2). The policy validator only
// rejects negative TimeoutSeconds, so the runner has to defend the
// boundary itself.
const maxPolicyTimeoutSeconds = int64(math.MaxInt64) / int64(time.Second)

// POSIX shell exit-code conventions used to distinguish environment
// failures (the runner image lacks a tool, a binary is non-executable)
// from artifact policy verdicts. Mapping these to OutcomeUnavailable
// keeps the failure/error distinction clean in evidence rows: a
// missing `go` binary surfacing as CheckStatusFailed (not Error)
// would falsely accuse the policy author's artifact of not satisfying
// the check (codex pass 10 P2).
const (
	shellExitCommandNotExecutable = 126 // found but not executable / permission denied
	shellExitCommandNotFound      = 127 // not found in PATH
)

// logRefSequence is a process-wide monotonic counter appended to every
// LogReference. Wall-clock timestamps alone are not enough: two
// concurrent Runs with identical (repository, branch, check_id) can
// land in the same nanosecond on systems whose clock has lower
// effective resolution, producing colliding references that a
// flat-file / object-store LogSink would silently merge or
// overwrite. Codex pass 5 P2.
var logRefSequence atomic.Uint64

// LocalCommandRunner executes domain.PolicyCheckKindCommand checks
// against a working tree on the local filesystem. It is the first
// concrete Runner — TASK-003 AC #1 names the local-command shape
// explicitly. External-CI integrations are intentionally NOT this
// runner's concern; they go behind their own Runner implementation so
// the boundary stays CI-agnostic.
//
// The runner is deliberately stateless: every Run call captures its
// own start/end timestamps and routes its own log sink. Two concurrent
// Run calls on the same LocalCommandRunner instance MUST be safe.
type LocalCommandRunner struct {
	// Shell is the prefix used to invoke commands. Defaults to
	// {"sh", "-c"} when nil. Exposed as a field so tests on hosts
	// without /bin/sh (or with a non-POSIX default shell) can override
	// it deterministically — there is no global default the package
	// could "discover".
	Shell []string

	// LogSink, if non-nil, is invoked once per Run with a stable
	// reference string. The returned writer receives the merged
	// stdout+stderr of the executed command. The runner closes the
	// writer when the command completes (success, failure, or timeout).
	// LogSink may be nil — the runner then discards command output.
	// Discarding by default keeps the runner usable in tests and
	// embedded contexts that do not want to provision storage.
	LogSink LogSink

	// Now is the timestamp source for StartedAt / CompletedAt. Defaults
	// to time.Now when nil. Tests use a fixed clock so timing
	// assertions are deterministic; production code passes nil.
	Now func() time.Time

	// MaxLogBytes caps how many bytes the runner forwards to the log
	// sink per invocation. When zero, no cap applies. Exists because
	// runaway command output can otherwise OOM the process: a
	// `make test` rebuild loop or a `find /` typo can produce
	// gigabytes of stdout. Truncation is an integrity property, not a
	// performance optimization — the runner appends a single-line
	// "[truncated at N bytes]" footer when the cap fires so audit
	// readers know the log is incomplete.
	MaxLogBytes int64

	// WaitDelay bounds how long the runner waits for stdout/stderr
	// pipes to drain after the policy timeout fires. Without this,
	// `sh -c "sleep 30"` (or any check that spawns descendants) would
	// hang the runner for the full sleep: SIGKILL kills `sh`, but the
	// orphaned `sleep` keeps the pipe write end open for 30s, so
	// cmd.Wait stays blocked until the kernel reaps the descendant.
	// Defaults to 5 seconds when zero — long enough for legitimate
	// drain on slow I/O, short enough to bound runner latency.
	// Negative values disable the bound (matches exec.Cmd semantics).
	WaitDelay time.Duration
}

// LogSink receives captured stdout+stderr from a single Run. The
// reference is the runner's stable identifier for this invocation
// (carried back to the caller as Result.LogReference). The sink
// returns a writer the runner pipes process output into; the runner
// closes the writer before returning the Result.
//
// Implementations are free to drop, redact, or persist the bytes —
// the runner does not interpret them. This indirection is what makes
// "preserve raw logs as references, not inline evidence" enforceable:
// the runner can't accidentally embed log content in its return value
// because it never holds the bytes itself.
type LogSink interface {
	OpenLog(ref string) (io.WriteCloser, error)
}

// LogSinkFunc adapts a plain function to the LogSink interface. Useful
// for tests and for sinks that don't need any state.
type LogSinkFunc func(ref string) (io.WriteCloser, error)

// OpenLog calls f.
func (f LogSinkFunc) OpenLog(ref string) (io.WriteCloser, error) { return f(ref) }

// Run implements Runner. The lifecycle:
//
//  1. Validate the request — kind must be command, working dir must
//     exist, command must be non-empty after trimming.
//  2. Open the log sink for this invocation (single reference per Run).
//  3. Build a context with the policy's TimeoutSeconds, if non-zero.
//  4. exec.CommandContext + Dir + merged stdout/stderr → log writer.
//  5. Classify the exit:
//     - context deadline before exit → OutcomeTimeout
//     - exit 0 → OutcomePass
//     - exit non-zero, deterministic → OutcomeFail
//     - other exec errors → OutcomeUnavailable
//
// Errors returned from Run are reserved for runner-internal I/O
// failures — opening the log sink, closing the log writer. A check
// that ran and returned a non-zero exit is a successful Run with
// OutcomeFail; the error channel stays nil. This split keeps caller
// code from special-casing routine outcomes as errors.
func (r LocalCommandRunner) Run(ctx context.Context, req Request) (Result, error) {
	now := r.now()
	res := Result{StartedAt: now}

	if req.Check.Kind != domain.PolicyCheckKindCommand {
		res.Outcome = OutcomeUnavailable
		res.Reason = fmt.Sprintf("kind=%q not supported by local command runner", req.Check.Kind)
		res.CompletedAt = r.now()
		return res, nil
	}
	command := strings.TrimSpace(req.Check.Command)
	if command == "" {
		res.Outcome = OutcomeUnavailable
		res.Reason = "policy check command is empty"
		res.CompletedAt = r.now()
		return res, nil
	}
	if req.WorkingDir == "" {
		res.Outcome = OutcomeUnavailable
		res.Reason = "working_dir is empty"
		res.CompletedAt = r.now()
		return res, nil
	}
	// Resolve the working tree to an absolute path before stat'ing so
	// a relative path resolved against the runner's CWD does not race
	// a chdir from another goroutine. exec.CommandContext also accepts
	// relative Dir, but consistent absolute paths show up cleanly in
	// ProcessState debug output if a Run hangs.
	workingDir, absErr := filepath.Abs(req.WorkingDir)
	if absErr != nil {
		res.Outcome = OutcomeUnavailable
		res.Reason = fmt.Sprintf("working_dir resolve failed: %s", sanitizeReason(absErr.Error()))
		res.CompletedAt = r.now()
		return res, nil
	}
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		res.Outcome = OutcomeUnavailable
		switch {
		case errors.Is(err, os.ErrNotExist):
			res.Reason = "working_dir does not exist"
		case err != nil:
			res.Reason = fmt.Sprintf("working_dir stat failed: %s", sanitizeReason(err.Error()))
		default:
			res.Reason = "working_dir is not a directory"
		}
		res.CompletedAt = r.now()
		return res, nil
	}

	logRef := buildLogReference(req, now)
	logWriter, hasRealSink, sinkErr := r.openLog(logRef)
	if sinkErr != nil {
		// Sink-open failure is a runner-internal problem the operator
		// must see (the runner has nowhere to put the bytes); surface
		// as Result error rather than as OutcomeUnavailable so callers
		// don't conflate "I couldn't run" with "I couldn't log what I
		// ran". StartedAt is left set so traces still show when the
		// attempt began.
		res.CompletedAt = r.now()
		return res, fmt.Errorf("checkrunner: open log sink %q: %w", logRef, sinkErr)
	}

	runCtx := ctx
	var cancel context.CancelFunc
	// policyTimeoutFiredFirst is true when the runner-created
	// timeout (from TimeoutSeconds) has an earlier deadline than the
	// caller's parent context (or the caller has no deadline at all).
	// classifyExit consults this so that when BOTH context errors
	// have already settled to DeadlineExceeded by the time we
	// classify, the proximate cause attributes correctly: the runner
	// timeout if its deadline was earlier, the caller deadline
	// otherwise. Without this, a slow-drain command (e.g. one that
	// escaped the process group) could mask a real policy timeout
	// as caller cancellation. Codex pass 13 P2.
	var policyTimeoutFiredFirst bool
	if req.Check.TimeoutSeconds > 0 {
		seconds := req.Check.TimeoutSeconds
		if seconds > maxPolicyTimeoutSeconds {
			seconds = maxPolicyTimeoutSeconds
		}
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		defer cancel()
		runnerDeadline, _ := runCtx.Deadline()
		parentDeadline, parentHasDeadline := ctx.Deadline()
		if !parentHasDeadline || runnerDeadline.Before(parentDeadline) {
			policyTimeoutFiredFirst = true
		}
	}

	shell := r.shell()
	args := append(append([]string{}, shell[1:]...), command)
	cmd := exec.CommandContext(runCtx, shell[0], args...)
	cmd.Dir = workingDir
	// Wrap the sink writer so a sink-side Write failure is
	// distinguishable from a normal exec error. Without this, a full
	// disk or broken upload during stdout copy surfaces from cmd.Run
	// as a generic error which classifyExit then collapses to
	// OutcomeUnavailable + nil Run error — silently labelling an
	// infrastructure logging outage as a routine check verdict.
	// Codex pass 1 P2.
	captured := &errCapturingWriter{w: newCappedWriter(logWriter, r.MaxLogBytes)}
	cmd.Stdout = captured
	cmd.Stderr = captured
	cmd.WaitDelay = r.waitDelay()
	configureProcessGroup(cmd)
	// runnerKilledLeader flips when the context-watching goroutine
	// calls cmd.Cancel — i.e. the leader was still alive when the
	// deadline / cancellation fired and we killed it. Used by
	// classifyExit to override the leader-exit branch on Windows,
	// where Process.Kill produces a non-negative ExitCode that
	// otherwise looks like a clean check verdict (codex pass 15 P2).
	// On POSIX, killed processes get exit code -1 so the leader-exit
	// branch isn't entered at all and this flag is informational; on
	// Windows it is load-bearing.
	var runnerKilledLeader atomic.Bool
	// cmd.Cancel signals the whole process group on context done so
	// `sh -c "sleep 30"` does not leak the orphan sleep after the
	// shell dies. Without this, the runner returns within WaitDelay
	// but descendants keep running in the workspace. Codex pass 1 P1.
	cmd.Cancel = func() error {
		runnerKilledLeader.Store(true)
		return cancelProcessGroup(cmd)
	}

	runErr := cmd.Run()
	res.CompletedAt = r.now()
	// Sweep the process group regardless of how cmd.Run returned. A
	// check that backgrounds work (e.g. `(while true; ...) &`) leaves
	// orphaned descendants holding pipe FDs even on the success path;
	// the deadline-driven cmd.Cancel only fires when the timeout
	// fires, so a successful-but-leaks scenario would otherwise leak
	// past Run. Best-effort: ESRCH on an already-empty group is a
	// no-op (codex pass 4 P2).
	_ = cancelProcessGroup(cmd)
	// LogReference is set only when a real LogSink received the bytes.
	// With a nil LogSink the runner discards output via nopWriteCloser
	// and exposing a generated reference would leave audit consumers
	// holding an unresolvable pointer (codex pass 3 P2).
	if hasRealSink {
		res.LogReference = logRef
	}
	// Snapshot the context state at the moment cmd.Run returned —
	// classification reads it later, after logWriter.Close, which can
	// block. Without the snapshot a slow sink Close that pushes the
	// deadline past TimeoutSeconds AFTER the command already exited
	// successfully would flip res.Outcome to OutcomeTimeout, mis-
	// labelling a completed verdict. Codex pass 2 P2.
	//
	// Snapshot the parent context separately so the runner can tell
	// "policy timeout fired" (only runCtx done) from "caller deadline
	// fired" (parent ctx done). If we collapsed them, a caller
	// passing context.WithTimeout that happened to be shorter than
	// the policy budget would have its requests reported as
	// OutcomeTimeout (a check verdict) instead of OutcomeUnavailable
	// (an orchestrator decision). Codex pass 4 P2.
	runCtxErr := runCtx.Err()
	parentCtxErr := ctx.Err()
	// Capture the leader's exit code BEFORE classifyExit. Needed for
	// the cmd.WaitDelay path: when a background child holds pipes
	// past leader exit, cmd.Run surfaces exec.ErrWaitDelay even
	// though cmd.ProcessState carries the leader's real exit code.
	// Without this snapshot classifyExit's catch-all would report
	// the clean exit as OutcomeUnavailable. Codex pass 8 P2.
	leaderExitCode := -1
	if cmd.ProcessState != nil {
		leaderExitCode = cmd.ProcessState.ExitCode()
	}

	// A sink write failure during stdout/stderr copy is a runner-
	// internal problem (logs are unreliable), not a check verdict.
	// Surface it via the error channel; preserve the LogReference so
	// the caller can still identify the partial file. We do NOT call
	// classifyExit here because runErr in this case IS the sink
	// error, and reporting it as OutcomeFail / OutcomeUnavailable
	// would conflate "log storage broken" with "check came back".
	if sinkErr := captured.firstErr(); sinkErr != nil {
		res.Outcome = OutcomeUnavailable
		res.Reason = "log sink write failed"
		_ = logWriter.Close()
		return res, fmt.Errorf("checkrunner: log sink write %q: %w", logRef, sinkErr)
	}

	closeErr := logWriter.Close()
	leaderKilled := leaderClearlyKilled(cmd, runnerKilledLeader.Load())
	res.Outcome = classifyExit(runCtxErr, parentCtxErr, runErr, leaderExitCode, policyTimeoutFiredFirst, leaderKilled, &res)
	if closeErr != nil {
		// Verdict survives the close failure (the process already
		// produced its exit status); surface the close error so
		// operators know the log may be truncated.
		return res, fmt.Errorf("checkrunner: close log sink %q: %w", logRef, closeErr)
	}
	return res, nil
}

// errCapturingWriter wraps an io.Writer and records the first error
// returned from Write. exec.Cmd's I/O goroutines call Write on
// cmd.Stdout / cmd.Stderr; a sink-side failure there propagates as
// the cmd.Run() return value, indistinguishable from any other
// generic exec error. Capturing the original error lets Run discriminate
// "logs are broken" (runner-internal, surface as Run error) from
// "exec returned a strange status" (Unavailable verdict).
type errCapturingWriter struct {
	w   io.Writer
	err error
}

func (e *errCapturingWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

func (e *errCapturingWriter) firstErr() error { return e.err }

// classifyExit is the boundary between os/exec semantics and the
// runner's Outcome enum. Centralized so the rules are unit-testable
// against constructed errors without spawning processes for every
// classification edge.
//
// runCtxErr is a snapshot of the run-context Err() (the runner's
// timeout-wrapped context) taken at the moment cmd.Run returned.
// parentCtxErr is the snapshot of the caller-supplied context's
// Err(). Both are passed in because:
//   - Snapshotting protects against post-Run latency (sink close)
//     re-flipping context state by the time classifyExit runs
//     (codex pass 2 P2).
//   - Distinguishing parent vs runner context lets the runner tell
//     "policy timeout fired" (runCtx only) from "caller deadline /
//     cancellation" (parent ctx). Without the split a short caller
//     deadline would be reported as a policy timeout — a check
//     verdict where there really was none (codex pass 4 P2).
//
// Order matters:
//
//  1. Leader exited cleanly (exit code ≥ 0) → honour the verdict
//     regardless of subsequent context state. The leader's exit is
//     the authoritative signal; if it ran to completion, post-run
//     pipe drain crossing a deadline must not erase the verdict
//     (codex pass 9 P2). exit code -1 means "killed by signal" — no
//     verdict, fall through.
//  2. Parent context cancelled or deadline-exceeded → caller decided
//     to stop the run before the leader produced a verdict; report
//     as Unavailable. Caller signals normally take precedence over
//     policy timeouts because the caller didn't want this check to
//     run. EXCEPTION: policyTimeoutFiredFirst — when the runner's
//     own deadline was strictly earlier than the caller's, the
//     runner timeout was the proximate cause even if both deadlines
//     have settled to DeadlineExceeded by the time we classify.
//     Without this, a slow-drain command could mask a real policy
//     timeout as caller cancellation (codex pass 13 P2).
//  3. Run context (policy timeout) deadline-exceeded → real check
//     timeout (the leader was killed mid-execution, no verdict).
//     Reported BEFORE *exec.ExitError because a child killed by
//     SIGKILL on timeout surfaces as a signal-style status that
//     looks like a non-zero verdict — operators want "we ran out of
//     time", not "and the child returned 137".
//  4. nil error → OutcomePass (defensive; should be covered by #1).
//  5. *exec.ExitError → OutcomeFail with the exit code (signalled).
//  6. Anything else (binary not found, permission denied, fork
//     failure) → OutcomeUnavailable so dashboards distinguish "config
//     issue, retry won't help" from "real verdict".
func classifyExit(runCtxErr, parentCtxErr, runErr error, leaderExitCode int, policyTimeoutFiredFirst, leaderKilled bool, res *Result) Outcome {
	if leaderExitCode >= 0 && !leaderKilled {
		// Leader exited cleanly — verdict is authoritative even when
		// a deadline / WaitDelay / cancellation later shaped the
		// post-run state. ErrWaitDelay or post-run context fires can
		// happen after a successful leader exit; without honouring
		// the leader's status here, those would erase the verdict.
		//
		// leaderKilled gates this branch off when the runner actively
		// killed the leader: on POSIX a SIGKILL'd leader has
		// ProcessState.Exited()=false (so leaderKilled=true even when
		// ExitCode happens to be 0), and on Windows Process.Kill
		// would otherwise let a non-negative ExitCode masquerade as
		// a clean exit (codex pass 15 P2). Determination is platform-
		// specific via leaderClearlyKilled.
		res.ExitCode = leaderExitCode
		suffix := classifySuffix(runCtxErr, parentCtxErr, runErr)
		switch leaderExitCode {
		case 0:
			res.Reason = "exit 0" + suffix
			return OutcomePass
		case shellExitCommandNotFound:
			// Shell convention: command not found in PATH. The check
			// declared a tool the runner image doesn't have — that's
			// an environment problem, not an artifact verdict.
			res.Reason = "exit 127 (command not found)" + suffix
			return OutcomeUnavailable
		case shellExitCommandNotExecutable:
			// Shell convention: command found but not executable
			// (typically permission denied). Same environment-error
			// classification as 127.
			res.Reason = "exit 126 (command not executable)" + suffix
			return OutcomeUnavailable
		default:
			res.Reason = fmt.Sprintf("exit %d", leaderExitCode) + suffix
			return OutcomeFail
		}
	}
	// Caller cancellation always wins (it is an explicit "stop" the
	// caller initiated). Caller deadline only wins when the runner's
	// own deadline did NOT fire first.
	if errors.Is(parentCtxErr, context.Canceled) {
		res.Reason = "context canceled"
		return OutcomeUnavailable
	}
	if errors.Is(parentCtxErr, context.DeadlineExceeded) && !policyTimeoutFiredFirst {
		res.Reason = "caller deadline exceeded"
		return OutcomeUnavailable
	}
	if errors.Is(runCtxErr, context.DeadlineExceeded) {
		res.Reason = "context deadline exceeded"
		return OutcomeTimeout
	}
	if errors.Is(runCtxErr, context.Canceled) {
		res.Reason = "context canceled"
		return OutcomeUnavailable
	}
	if runErr == nil {
		res.ExitCode = 0
		res.Reason = "exit 0"
		return OutcomePass
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		res.Reason = fmt.Sprintf("exit %d", res.ExitCode)
		return OutcomeFail
	}
	res.Reason = sanitizeReason(runErr.Error())
	return OutcomeUnavailable
}

// classifySuffix annotates a leader-honoured Reason with the post-run
// signal that shaped the wait. Without the suffix, an evidence row
// for a clean `cmd & exit 0` would say "exit 0" with no hint that
// the wait was bounded by WaitDelay or a deadline — operators
// debugging slow runs would lose useful context. Suffix is empty in
// the common case (no post-run signal).
func classifySuffix(runCtxErr, parentCtxErr, runErr error) string {
	switch {
	case errors.Is(parentCtxErr, context.DeadlineExceeded):
		return " (caller deadline fired during post-run drain)"
	case errors.Is(parentCtxErr, context.Canceled):
		return " (context canceled during post-run drain)"
	case errors.Is(runCtxErr, context.DeadlineExceeded):
		return " (deadline fired during post-run drain)"
	case errors.Is(runErr, exec.ErrWaitDelay):
		return " (background pipes drained by wait delay)"
	}
	return ""
}

func (r LocalCommandRunner) shell() []string {
	if len(r.Shell) >= 1 {
		return r.Shell
	}
	return []string{"sh", "-c"}
}

func (r LocalCommandRunner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r LocalCommandRunner) waitDelay() time.Duration {
	if r.WaitDelay < 0 {
		return 0
	}
	if r.WaitDelay == 0 {
		return 5 * time.Second
	}
	return r.WaitDelay
}

// openLog returns the writer that command stdout/stderr will be piped
// into, plus a flag indicating whether a real sink received the bytes.
// hasRealSink == false means the runner is discarding output via a
// no-op writer; the caller uses this signal to keep Result.LogReference
// empty so audit consumers do not chase a pointer that was never
// resolved.
func (r LocalCommandRunner) openLog(ref string) (io.WriteCloser, bool, error) {
	if r.LogSink == nil {
		return nopWriteCloser{}, false, nil
	}
	w, err := r.LogSink.OpenLog(ref)
	if err != nil {
		return nil, false, err
	}
	if w == nil {
		// A LogSink that returns (nil, nil) would NPE inside the
		// command's stdout pipe. Treat this as a contract violation
		// rather than letting it fail later in obscure exec internals.
		return nil, false, fmt.Errorf("LogSink.OpenLog returned nil writer with nil error")
	}
	return w, true, nil
}

// nopWriteCloser drops everything written to it. Used when no LogSink
// is configured so the runner does not have to special-case nil
// writers throughout. io.Discard would do the read side but we need
// Close() too.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// cappedWriter forwards writes to an underlying writer up to a byte
// budget, then drops further bytes (returning len(p) so the producer
// — exec.Cmd's pipe goroutine — does not loop on a partial write).
// On the first overflow it appends a single-line marker so audit
// readers can tell the log was truncated, not just terminated by the
// process. Cap == 0 means "no cap".
type cappedWriter struct {
	w        io.Writer
	limit    int64
	written  int64
	overflow bool
}

func newCappedWriter(w io.Writer, limit int64) *cappedWriter {
	return &cappedWriter{w: w, limit: limit}
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.limit <= 0 {
		n, err := c.w.Write(p)
		c.written += int64(n)
		return n, err
	}
	remaining := c.limit - c.written
	if remaining >= int64(len(p)) {
		n, err := c.w.Write(p)
		c.written += int64(n)
		return n, err
	}
	if remaining > 0 {
		n, err := c.w.Write(p[:remaining])
		c.written += int64(n)
		if err != nil {
			return n, err
		}
	}
	if !c.overflow {
		c.overflow = true
		// Best-effort marker. We deliberately ignore the marker write
		// error — the cap is already a degraded mode, and a marker
		// failure must not stall the producer.
		_, _ = fmt.Fprintf(c.w, "\n[checkrunner: log truncated at %d bytes]\n", c.limit)
	}
	// Always claim full consumption so cmd.Run's pipe goroutine does
	// not stall on a "short write" loop.
	return len(p), nil
}

// buildLogReference produces the stable identifier the LogSink sees.
// Format is internal to the package; callers receive the same string
// in Result.LogReference and treat it as opaque.
//
// Uniqueness is layered:
//   - repository_id / branch / check_id keep concurrent Runs of
//     different checks distinguishable;
//   - start timestamp keeps sequential Runs of the same check
//     distinguishable;
//   - a process-wide atomic counter forces uniqueness even when two
//     concurrent Runs of the same (repo, branch, check_id) land in
//     the same wall-clock nanosecond — a flat-file / object-store
//     LogSink would otherwise overwrite one with the other.
func buildLogReference(req Request, started time.Time) string {
	return fmt.Sprintf("checkrunner/%s/%s/%s/%s/%d/%016x",
		safeSegment(req.RepositoryID),
		safeSegment(req.BranchName),
		safeSegment(req.Check.CheckID),
		started.UTC().Format("20060102T150405.000000000Z"),
		started.UnixNano(),
		logRefSequence.Add(1),
	)
}

// safeSegment renders an identifier safely into a path-style log ref.
// Replaces every byte that isn't an ASCII letter, digit, '-' or '_'
// with '_' so the resulting reference is safe to use as a file path
// segment on any of the sinks we expect (filesystem, S3-style key).
// Identifier-level escaping rather than full URL encoding because the
// reference is opaque to consumers — they don't have to round-trip the
// segments back to the original string.
func safeSegment(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sanitizeReason strips newlines from a free-form error message so the
// resulting Reason is safe to drop into CheckResult.Summary
// (Validate forbids \n / \r in summary). The message stays
// human-readable; only the line-separator chars are replaced with
// spaces. Whitespace runs are NOT collapsed — that would mangle paths
// that legitimately contain spaces in error messages.
func sanitizeReason(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	r := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
	return r.Replace(s)
}
