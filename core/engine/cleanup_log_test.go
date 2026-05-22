package engine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// errorDeleteGit is a stubGitOperator whose DeleteBranch returns a
// configured error. Used to drive CleanupRunBranch into its non-nil
// return path so the run.go call sites have something to log.
type errorDeleteGit struct {
	stubGitOperator
	err error
}

func (g *errorDeleteGit) DeleteBranch(_ context.Context, _ string) error { return g.err }

// captureSlog redirects the global slog default to a buffer for the
// duration of t. Restores the previous default on cleanup. Used to
// assert the warn shape of cleanup-failure logs at the run.go call
// sites — INIT-022 EPIC-001 TASK-012.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestFailRun_CleanupErrorLogged forces the primary git delete to
// fail, then asserts FailRun emits the documented warn line carrying
// op name + run_id + branch + error. Locks the AC for TASK-012.
func TestFailRun_CleanupErrorLogged(t *testing.T) {
	logBuf := captureSlog(t)

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-cleanup-fail": {
				RunID:      "run-cleanup-fail",
				Status:     domain.RunStatusActive,
				BranchName: "spine/run/run-cleanup-fail",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    events,
		git:       &errorDeleteGit{err: errors.New("ref locked: branch checked out")},
		wfLoader:  &stubWorkflowLoader{},
	}

	if err := orch.FailRun(context.Background(), "run-cleanup-fail", "step timed out"); err != nil {
		t.Fatalf("FailRun: %v", err)
	}

	logged := logBuf.String()
	for _, want := range []string{
		`level=WARN`,
		`msg="cleanup_run_branch failed"`,
		`run_id=run-cleanup-fail`,
		`branch=spine/run/run-cleanup-fail`,
		`error="delete branch spine/run/run-cleanup-fail: ref locked: branch checked out"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("warn log missing %q\nfull log:\n%s", want, logged)
		}
	}
}

// TestCancelRun_CleanupErrorLogged is the parallel of FailRun — same
// plumbing in CancelRun must emit the same warn shape so operators
// can correlate orphan branches to whichever transition produced them.
func TestCancelRun_CleanupErrorLogged(t *testing.T) {
	logBuf := captureSlog(t)

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-cancel-fail": {
				RunID:      "run-cancel-fail",
				Status:     domain.RunStatusActive,
				BranchName: "spine/run/run-cancel-fail",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    events,
		git:       &errorDeleteGit{err: errors.New("permission denied")},
		wfLoader:  &stubWorkflowLoader{},
	}

	if err := orch.CancelRun(context.Background(), "run-cancel-fail"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	logged := logBuf.String()
	for _, want := range []string{
		`level=WARN`,
		`msg="cleanup_run_branch failed"`,
		`run_id=run-cancel-fail`,
		`branch=spine/run/run-cancel-fail`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("warn log missing %q\nfull log:\n%s", want, logged)
		}
	}
}
