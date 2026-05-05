package checkrunner_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/checkrunner"
	"github.com/bszymi/spine/internal/domain"
)

// Coverage map for TASK-003 ACs:
//
//   "Local command checks can run against a cloned repository branch."
//     → TestLocalCommandRunner_Pass / Fail (real `sh` invoked in a tempdir
//       acting as the cloned working tree)
//   "Check results are normalized into the evidence schema."
//     → TestNormalize_* + TestNormalize_RoundTripIntoEvidence
//   "Timeouts and execution failures are classified."
//     → TestLocalCommandRunner_Timeout / Fail
//   "Unit tests cover pass, fail, timeout, and unavailable states."
//     → all four Outcome paths exercised below

// requireSh skips a test when the host has no /bin/sh (Windows CI).
// LocalCommandRunner is shell-based by design — there's no point
// shimming an alternate shell into every test when the package's
// production callers run in containers that ship `sh`.
func requireSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("LocalCommandRunner test requires POSIX shell")
	}
}

func TestLocalCommandRunner_Pass(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		CommitSHA:    "abc",
		WorkingDir:   dir,
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomePass {
		t.Fatalf("Outcome: got %q want pass", res.Outcome)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d want 0", res.ExitCode)
	}
	if res.StartedAt.IsZero() || res.CompletedAt.IsZero() {
		t.Fatalf("timestamps must be set: started=%v completed=%v", res.StartedAt, res.CompletedAt)
	}
	if res.CompletedAt.Before(res.StartedAt) {
		t.Fatalf("CompletedAt %v before StartedAt %v", res.CompletedAt, res.StartedAt)
	}
}

func TestLocalCommandRunner_Fail(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		WorkingDir:   t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "fails",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "exit 7",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeFail {
		t.Fatalf("Outcome: got %q want fail", res.Outcome)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode: got %d want 7", res.ExitCode)
	}
	if !strings.Contains(res.Reason, "exit 7") {
		t.Fatalf("Reason: got %q want substring %q", res.Reason, "exit 7")
	}
}

func TestLocalCommandRunner_Timeout(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	start := time.Now()
	res, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		WorkingDir:   t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "slow",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "sleep 30",
			TimeoutSeconds: 1,
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeTimeout {
		t.Fatalf("Outcome: got %q want timeout", res.Outcome)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took too long: %v — expected ~1s", elapsed)
	}
	if res.Reason != "context deadline exceeded" {
		t.Fatalf("Reason: got %q want %q", res.Reason, "context deadline exceeded")
	}
}

// TestLocalCommandRunner_TimeoutDoesNotMisreportSignalledExit guards
// classifyExit's first-rule precedence: when the policy timeout fires,
// the child is killed and exits with a signal-style status that
// exec.Cmd surfaces as a non-zero ExitError. Without the
// "ctx.Err() before exec.ExitError" ordering in classifyExit, this
// would report OutcomeFail with exit -1 / 137 — masking a real
// timeout as a policy violation. This test exists separately from
// TestLocalCommandRunner_Timeout because the previous test only
// covers the happy timeout path.
func TestLocalCommandRunner_TimeoutDoesNotMisreportSignalledExit(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "slow-shell",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "trap 'exit 0' TERM; sleep 30",
			TimeoutSeconds: 1,
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeTimeout {
		t.Fatalf("Outcome: got %q want timeout (regression: signalled child reported as fail)", res.Outcome)
	}
}

func TestLocalCommandRunner_UnavailableMissingWorkingDir(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: missing,
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if !strings.Contains(res.Reason, "working_dir does not exist") {
		t.Fatalf("Reason: got %q want substring 'working_dir does not exist'", res.Reason)
	}
}

func TestLocalCommandRunner_UnavailableEmptyWorkingDir(t *testing.T) {
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: "",
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if res.Reason != "working_dir is empty" {
		t.Fatalf("Reason: got %q want %q", res.Reason, "working_dir is empty")
	}
}

func TestLocalCommandRunner_UnavailableWorkingDirIsFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: filePath,
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if res.Reason != "working_dir is not a directory" {
		t.Fatalf("Reason: got %q want %q", res.Reason, "working_dir is not a directory")
	}
}

func TestLocalCommandRunner_UnavailableExternalKind(t *testing.T) {
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "external-scan",
			Kind:           domain.PolicyCheckKindExternal,
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if !strings.Contains(res.Reason, "external") {
		t.Fatalf("Reason should describe external kind: got %q", res.Reason)
	}
}

func TestLocalCommandRunner_UnavailableEmptyCommand(t *testing.T) {
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "blank",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "   \t   ",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if res.Reason != "policy check command is empty" {
		t.Fatalf("Reason: got %q want %q", res.Reason, "policy check command is empty")
	}
}

// TestLocalCommandRunner_UnavailableNoSuchBinary covers a subtle path
// inside classifyExit: a command that fails before producing an exit
// status (binary not found, fork failure) returns a generic exec error
// — NOT *exec.ExitError. We rely on the catch-all branch returning
// OutcomeUnavailable so dashboards can distinguish "your environment
// is broken" from "your code is broken".
func TestLocalCommandRunner_UnavailableNoSuchBinary(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{
		// Force a missing-binary error by pointing Shell at an absent
		// binary directly. classifyExit must still classify this as
		// Unavailable, not Fail.
		Shell: []string{"/no/such/binary/spine-test-shim", "-c"},
	}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "broken-env",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if res.Reason == "" {
		t.Fatalf("Reason should describe the underlying exec error")
	}
	if strings.ContainsAny(res.Reason, "\n\r") {
		t.Fatalf("Reason must be single-line for evidence Validate compatibility: got %q", res.Reason)
	}
}

func TestLocalCommandRunner_LogsRoutedToSink(t *testing.T) {
	requireSh(t)
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{LogSink: sink}
	_, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		WorkingDir:   t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "echoes",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "echo hello-from-stdout; echo hello-from-stderr 1>&2; exit 0",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sink.bytes(); !strings.Contains(got, "hello-from-stdout") || !strings.Contains(got, "hello-from-stderr") {
		t.Fatalf("log sink should receive merged stdout+stderr; got %q", got)
	}
}

// TestLocalCommandRunner_LogReferenceFlowsThrough confirms that the
// caller-visible Result.LogReference is the same string the LogSink
// saw. This is the audit handshake: Result.LogReference acts as a
// pointer the caller persists into evidence (or alongside it), and
// the same string lets a future reader find the bytes again.
func TestLocalCommandRunner_LogReferenceFlowsThrough(t *testing.T) {
	requireSh(t)
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{LogSink: sink}
	res, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		WorkingDir:   t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LogReference == "" {
		t.Fatalf("LogReference should be non-empty when a sink is configured")
	}
	if got := sink.refs(); len(got) != 1 || got[0] != res.LogReference {
		t.Fatalf("sink saw refs %v, Result.LogReference %q — must match", got, res.LogReference)
	}
}

// TestLocalCommandRunner_LogReferenceIsSafePathSegment guards against
// a regression where the log reference embeds raw caller-supplied
// repo / branch names. Audit log sinks (filesystem trees, S3 keys)
// will choke on path-traversal characters in those names. Hardening
// at the runner means downstream sinks do not have to defend
// independently.
func TestLocalCommandRunner_LogReferenceIsSafePathSegment(t *testing.T) {
	requireSh(t)
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{LogSink: sink}
	res, err := r.Run(context.Background(), checkrunner.Request{
		RepositoryID: "billing/../etc",
		BranchName:   "spine/run/with spaces",
		WorkingDir:   t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, bad := range []string{"..", " ", "/etc"} {
		if strings.Contains(res.LogReference, bad) {
			t.Fatalf("LogReference must not contain %q — got %q", bad, res.LogReference)
		}
	}
}

// TestLocalCommandRunner_NoSinkDiscardsOutput exists because dropping
// command output silently is a contract: callers that have no sink
// configured (embedded mode, tests) must still get a clean Result.
// Without explicit coverage a future refactor could regress to
// "panic on nil sink" — this test traps that.
func TestLocalCommandRunner_NoSinkDiscardsOutput(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noisy",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "yes spam-output | head -n 1000",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomePass {
		t.Fatalf("Outcome: got %q want pass", res.Outcome)
	}
}

// TestLocalCommandRunner_LogSinkOpenError surfaces sink I/O failures
// as Run errors (not as OutcomeUnavailable). The runner's contract
// reserves the error return for runner-internal problems the operator
// must debug.
func TestLocalCommandRunner_LogSinkOpenError(t *testing.T) {
	wantErr := errors.New("sink-storage offline")
	r := checkrunner.LocalCommandRunner{
		LogSink: checkrunner.LogSinkFunc(func(string) (io.WriteCloser, error) { return nil, wantErr }),
	}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err == nil {
		t.Fatalf("expected sink-open error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error should wrap sink error: got %v", err)
	}
	if res.StartedAt.IsZero() {
		t.Fatalf("StartedAt should be set even when sink open fails")
	}
}

// TestLocalCommandRunner_LogSinkCloseError pins the documented
// behaviour that a close failure surfaces as a Run error AND still
// returns a Result with the verdict — a half-state where the verdict
// would be lost would be much worse than reporting a possibly-truncated
// log.
func TestLocalCommandRunner_LogSinkCloseError(t *testing.T) {
	requireSh(t)
	wantErr := errors.New("sink-close write failed")
	r := checkrunner.LocalCommandRunner{
		LogSink: checkrunner.LogSinkFunc(func(string) (io.WriteCloser, error) {
			return &erroringCloser{err: wantErr}, nil
		}),
	}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err == nil {
		t.Fatalf("expected close error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error must wrap close error: %v", err)
	}
	if res.Outcome != checkrunner.OutcomePass {
		t.Fatalf("verdict must be preserved despite close error: got %q", res.Outcome)
	}
}

// TestLocalCommandRunner_LogSinkNilWriter guards against a sneaky
// contract violation — a sink returning (nil, nil) — that would
// otherwise NPE inside exec.Cmd's pipe goroutine. The runner has to
// catch this BEFORE the process starts.
func TestLocalCommandRunner_LogSinkNilWriter(t *testing.T) {
	r := checkrunner.LocalCommandRunner{
		LogSink: checkrunner.LogSinkFunc(func(string) (io.WriteCloser, error) { return nil, nil }),
	}
	_, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noop",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err == nil {
		t.Fatalf("expected error when sink returns nil writer")
	}
}

// TestLocalCommandRunner_MaxLogBytesTruncates checks the runaway-
// output guard. A check that prints more than MaxLogBytes still
// completes; the sink receives at most cap+marker bytes; the
// truncation is surfaced via the marker line.
func TestLocalCommandRunner_MaxLogBytesTruncates(t *testing.T) {
	requireSh(t)
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{
		LogSink:     sink,
		MaxLogBytes: 64,
	}
	res, err := r.Run(context.Background(), checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "noisy",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "yes A | head -c 10000",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomePass {
		t.Fatalf("Outcome: got %q want pass", res.Outcome)
	}
	got := sink.bytes()
	if len(got) > 64+128 { // cap + room for the truncation marker
		t.Fatalf("sink received %d bytes, expected <= cap + marker", len(got))
	}
	if !strings.Contains(got, "log truncated") {
		t.Fatalf("truncation marker missing from sink output:\n%s", got)
	}
}

// TestLocalCommandRunner_RunsInWorkingDir is a behavioural anchor:
// a `pwd` check should report the WorkingDir we passed in, not the
// runner's own CWD. Without this assertion a regression where Dir is
// dropped would silently pass every other test.
func TestLocalCommandRunner_RunsInWorkingDir(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		canonicalDir = dir
	}
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{LogSink: sink}
	_, err = r.Run(context.Background(), checkrunner.Request{
		WorkingDir: dir,
		Check: domain.PolicyCheck{
			CheckID:        "where-am-i",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "pwd -P",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(sink.bytes())
	if got != canonicalDir {
		t.Fatalf("pwd: got %q want %q", got, canonicalDir)
	}
}

// TestLocalCommandRunner_ConcurrentRuns exercises the documented
// stateless invariant. Two concurrent Run calls on the same instance
// must not interfere with each other's outcomes or log sinks.
func TestLocalCommandRunner_ConcurrentRuns(t *testing.T) {
	requireSh(t)
	sink := newFakeSink()
	r := checkrunner.LocalCommandRunner{LogSink: sink}

	const n = 10
	var wg sync.WaitGroup
	results := make([]checkrunner.Result, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = r.Run(context.Background(), checkrunner.Request{
				RepositoryID: "billing",
				BranchName:   fmt.Sprintf("spine/run/run-%d", i),
				WorkingDir:   t.TempDir(),
				Check: domain.PolicyCheck{
					CheckID:        fmt.Sprintf("check-%d", i),
					Kind:           domain.PolicyCheckKindCommand,
					Command:        fmt.Sprintf("echo %d", i),
					Interpretation: domain.PolicyCheckInterpretationDeterministic,
					Severity:       domain.PolicySeverityBlocking,
				},
			})
		}()
	}
	wg.Wait()
	seenRefs := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Run #%d: %v", i, errs[i])
		}
		if results[i].Outcome != checkrunner.OutcomePass {
			t.Fatalf("Run #%d: outcome %q", i, results[i].Outcome)
		}
		if _, dup := seenRefs[results[i].LogReference]; dup {
			t.Fatalf("duplicate LogReference across concurrent runs: %q", results[i].LogReference)
		}
		seenRefs[results[i].LogReference] = struct{}{}
	}
}

// TestLocalCommandRunner_CancelledContext checks that an external
// cancellation (e.g. orchestrator shutdown) yields OutcomeUnavailable,
// not OutcomeFail. Distinguishing canceled-by-caller from real verdict
// is the difference between "retry on next boot" and "the check
// actually said no".
func TestLocalCommandRunner_CancelledContext(t *testing.T) {
	requireSh(t)
	r := checkrunner.LocalCommandRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res, err := r.Run(ctx, checkrunner.Request{
		WorkingDir: t.TempDir(),
		Check: domain.PolicyCheck{
			CheckID:        "slow",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "sleep 30",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != checkrunner.OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", res.Outcome)
	}
	if res.Reason != "context canceled" {
		t.Fatalf("Reason: got %q want %q", res.Reason, "context canceled")
	}
}

// TestOutcome_IsTerminal locks the contract that every Outcome value
// is terminal at the runner boundary. If a future revision adds a
// non-terminal outcome (e.g. "running"), this test forces a deliberate
// update.
func TestOutcome_IsTerminal(t *testing.T) {
	for _, o := range []checkrunner.Outcome{
		checkrunner.OutcomePass,
		checkrunner.OutcomeFail,
		checkrunner.OutcomeTimeout,
		checkrunner.OutcomeUnavailable,
	} {
		if !o.IsTerminal() {
			t.Errorf("Outcome %q must be terminal", o)
		}
	}
}

// fakeSink is a thread-safe in-memory LogSink for tests.
type fakeSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	openErr error
	calls   []string
}

func newFakeSink() *fakeSink { return &fakeSink{} }

func (s *fakeSink) OpenLog(ref string) (io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	s.calls = append(s.calls, ref)
	return &fakeSinkWriter{s: s}, nil
}

func (s *fakeSink) bytes() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *fakeSink) refs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

type fakeSinkWriter struct {
	s *fakeSink
}

func (w *fakeSinkWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	return w.s.buf.Write(p)
}

func (w *fakeSinkWriter) Close() error { return nil }

// erroringCloser implements io.WriteCloser; writes succeed but Close
// returns the configured error. Used to exercise the close-error
// branch in LocalCommandRunner.Run.
type erroringCloser struct {
	err error
}

func (e *erroringCloser) Write(p []byte) (int, error) { return len(p), nil }
func (e *erroringCloser) Close() error                { return e.err }
