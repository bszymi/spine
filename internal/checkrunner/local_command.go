package checkrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

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
	logWriter, sinkErr := r.openLog(logRef)
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
	if req.Check.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Check.TimeoutSeconds)*time.Second)
		defer cancel()
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
	// cmd.Cancel signals the whole process group on context done so
	// `sh -c "sleep 30"` does not leak the orphan sleep after the
	// shell dies. Without this, the runner returns within WaitDelay
	// but descendants keep running in the workspace. Codex pass 1 P1.
	cmd.Cancel = func() error { return cancelProcessGroup(cmd) }

	runErr := cmd.Run()
	res.CompletedAt = r.now()
	res.LogReference = logRef
	// Snapshot the context state at the moment cmd.Run returned —
	// classification reads it later, after logWriter.Close, which can
	// block. Without the snapshot a slow sink Close that pushes the
	// deadline past TimeoutSeconds AFTER the command already exited
	// successfully would flip res.Outcome to OutcomeTimeout, mis-
	// labelling a completed verdict. Codex pass 2 P2.
	runCtxErr := runCtx.Err()

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
	res.Outcome = classifyExit(runCtxErr, runErr, &res)
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
// ctxErr is a snapshot of the run context's Err() taken at the moment
// cmd.Run returned. Using a snapshot rather than ctx.Err() at call
// time matters because the runner does post-Run work (sink close)
// that can block past the policy deadline; if the snapshot were
// dropped, slow sinks would re-classify completed verdicts as
// timeouts. Snapshot is taken in Run().
//
// Order matters:
//
//  1. Context deadline before any other check, so a child process that
//     happens to exit non-zero on signal is still reported as Timeout
//     (the operator's mental model of "we ran out of time" beats the
//     post-mortem detail of "and the child returned 137").
//  2. nil error → OutcomePass.
//  3. *exec.ExitError → OutcomeFail with the exit code.
//  4. Anything else (binary not found, permission denied, fork
//     failure) → OutcomeUnavailable so dashboards distinguish "config
//     issue, retry won't help" from "real verdict".
func classifyExit(ctxErr, runErr error, res *Result) Outcome {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		res.Reason = "context deadline exceeded"
		return OutcomeTimeout
	}
	if errors.Is(ctxErr, context.Canceled) {
		// Caller cancellation (e.g. orchestrator shutdown) is treated
		// as Unavailable — the verdict is unknown, the operator knows
		// why, and a retry by a fresh caller is the right next move.
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

func (r LocalCommandRunner) openLog(ref string) (io.WriteCloser, error) {
	if r.LogSink == nil {
		return nopWriteCloser{}, nil
	}
	w, err := r.LogSink.OpenLog(ref)
	if err != nil {
		return nil, err
	}
	if w == nil {
		// A LogSink that returns (nil, nil) would NPE inside the
		// command's stdout pipe. Treat this as a contract violation
		// rather than letting it fail later in obscure exec internals.
		return nil, fmt.Errorf("LogSink.OpenLog returned nil writer with nil error")
	}
	return w, nil
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
// in Result.LogReference and treat it as opaque. Including
// repository_id, branch, and a high-resolution start timestamp makes
// per-invocation references unique enough that two parallel Runs do
// not collide on a flat-file sink. A check_id keeps the same Run's
// multiple checks separable.
func buildLogReference(req Request, started time.Time) string {
	return fmt.Sprintf("checkrunner/%s/%s/%s/%s/%d",
		safeSegment(req.RepositoryID),
		safeSegment(req.BranchName),
		safeSegment(req.Check.CheckID),
		started.UTC().Format("20060102T150405.000000000Z"),
		started.UnixNano(),
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
