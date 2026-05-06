package checkrunner

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// classifyExit lives in package checkrunner; this internal test
// exercises its decision matrix directly so we can exhaustively cover
// the both-deadlines-fired case without orchestrating a real
// process-group escape (which depends on platform-specific tools
// like setsid). The black-box tests in local_command_test.go cover
// the integration end-to-end; this file pins the policy-vs-caller
// timing rule that codex pass 13 flagged.

func TestClassifyExit_PolicyTimeoutFirst_BothDeadlinesFired(t *testing.T) {
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded, // runCtx fired
		context.DeadlineExceeded, // parent ctx fired
		context.DeadlineExceeded, // runErr surfaces ctx err
		-1,                       // leader signalled (no clean exit)
		true,                     // policy fired first
		&res,
	)
	if got != OutcomeTimeout {
		t.Fatalf("Outcome: got %q want timeout", got)
	}
	if res.Reason != "context deadline exceeded" {
		t.Fatalf("Reason: got %q", res.Reason)
	}
}

func TestClassifyExit_CallerDeadlineFirst_BothDeadlinesFired(t *testing.T) {
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		-1,
		false, // policy did NOT fire first
		&res,
	)
	if got != OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", got)
	}
	if res.Reason != "caller deadline exceeded" {
		t.Fatalf("Reason: got %q", res.Reason)
	}
}

func TestClassifyExit_OnlyPolicyDeadlineFired(t *testing.T) {
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		nil,
		context.DeadlineExceeded,
		-1,
		true,
		&res,
	)
	if got != OutcomeTimeout {
		t.Fatalf("Outcome: got %q want timeout", got)
	}
}

func TestClassifyExit_OnlyCallerDeadlineFired(t *testing.T) {
	// Without a policy timeout, runCtx == ctx, so both errs are the
	// same. policyTimeoutFiredFirst is false.
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		-1,
		false,
		&res,
	)
	if got != OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", got)
	}
	if res.Reason != "caller deadline exceeded" {
		t.Fatalf("Reason: got %q", res.Reason)
	}
}

func TestClassifyExit_CallerCancellationWinsEvenIfPolicyFiredFirst(t *testing.T) {
	// Explicit cancellation by the caller takes precedence over a
	// policy deadline — the caller actively chose to stop, regardless
	// of which timer would have fired first. (Note: we only get here
	// if the leader did NOT exit cleanly; a clean exit is honoured by
	// the leaderExitCode>=0 branch above.)
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		context.Canceled,
		context.Canceled,
		-1,
		true,
		&res,
	)
	if got != OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", got)
	}
	if res.Reason != "context canceled" {
		t.Fatalf("Reason: got %q", res.Reason)
	}
}

func TestClassifyExit_LeaderCleanExitWinsOverDeadlines(t *testing.T) {
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		0, // leader exited 0 cleanly
		true,
		&res,
	)
	if got != OutcomePass {
		t.Fatalf("Outcome: got %q want pass — clean leader exit beats deadline", got)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d want 0", res.ExitCode)
	}
}

func TestClassifyExit_LeaderNonZeroExitWithPolicyFired(t *testing.T) {
	// If the leader exited non-zero before the deadline fired during
	// post-run drain, the verdict is Fail (the leader actually
	// reported failure). The deadline annotates the suffix.
	res := Result{}
	got := classifyExit(
		context.DeadlineExceeded,
		nil,
		context.DeadlineExceeded,
		3,
		true,
		&res,
	)
	if got != OutcomeFail {
		t.Fatalf("Outcome: got %q want fail", got)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode: got %d want 3", res.ExitCode)
	}
}

func TestClassifyExit_ExecExitError(t *testing.T) {
	// Signalled child (no clean exit) with no context fire → fall
	// through to *exec.ExitError handling. We can't easily construct
	// a real *exec.ExitError without spawning a process; this case is
	// covered by TestLocalCommandRunner_TimeoutDoesNotMisreportSignalledExit
	// in the black-box suite, which forces the SIGKILL path.
	_ = (*exec.ExitError)(nil) // keep the import live
}

func TestClassifyExit_GenericExecErrorIsUnavailable(t *testing.T) {
	res := Result{}
	got := classifyExit(
		nil,
		nil,
		errors.New("fork/exec /no/such/binary: no such file or directory"),
		-1,
		false,
		&res,
	)
	if got != OutcomeUnavailable {
		t.Fatalf("Outcome: got %q want unavailable", got)
	}
}
