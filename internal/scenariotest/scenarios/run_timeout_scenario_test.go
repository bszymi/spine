//go:build scenario

package scenarios_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/scheduler"
)

// runTimeoutWorkflowYAML is a minimal single-step workflow with a
// workflow-level `timeout` so StartRun populates `runtime.runs.timeout_at`.
// The 1h value paired with harness.Clock.Advance(2h) is what drives the
// scenario: at construction the harness clock matches wall time, so each
// run's natural timeout_at sits 1h ahead of the clock; advancing by 2h
// crosses that deadline for any run still on the workflow-derived value.
// The fresh-run stamp below pushes that run's timeout_at past the
// advance window, so only the expired run is selected by
// `ListTimedOutRuns` after the tick.
const runTimeoutWorkflowYAML = `id: task-runtime-timeout
name: Run Timeout Test Workflow
version: "1.0"
status: Active
description: Minimal single-step workflow for run-timeout scenarios. Workflow-level timeout drives Run.TimeoutAt population.
applies_to:
  - Task
entry_step: execute
timeout: "1h"
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "168h"
`

// TestRunTimeout_ScannerCancelsExpiredRunAndPreservesFresh is the
// scenario-level AC anchor for INIT-022 EPIC-001 TASK-016. It pins the
// run-timeout cancellation contract for `Scheduler.ScanRunTimeouts`
// against real Postgres + the event router.
//
// Coverage map:
//
//   - Unit-level coverage at `internal/scheduler/run_timeout_test.go`
//     proves `handleRunTimeout` against a fake store. It does NOT
//     exercise `ListTimedOutRuns` against real Postgres or assert that
//     the timeout predicate isolates timed-out runs from healthy ones.
//   - This scenario seeds two active runs side-by-side: one carrying
//     the workflow-derived `timeout_at = now + 1h`, one stamped to
//     `timeout_at = now + 168h` so it sits past the advance window.
//     `harness.Clock.Advance(2h)` then drives the scheduler tick (via
//     an `OnAdvance` handler registered on the clock — see TASK-028).
//     After the tick, the 1h-deadline run must be cancelled with a
//     `run_timeout` event, and the future-stamped run must remain
//     active with no event — a regression that drops the
//     `AND timeout_at <= $1` predicate from `ListTimedOutRuns` would
//     return both rows, cancelling the fresh run too, and this
//     scenario fails on the "fresh run still active" assertion
//     (mutation-test target per AC bullet 2).
//   - The branch-cleanup contract for the timeout path is the
//     intentional gap documented in
//     `architecture/multi-repository-integration.md §4.5`:
//     "Scheduler-driven run timeouts (`Scheduler.handleRunTimeout`)
//     flip a run to cancelled but do not call `CleanupRunBranch`."
//     The scenario asserts the run branch is still on disk after the
//     scheduler tick, pinning that contract until a future change
//     wires cleanup into the timeout path.
//
// Note on AC wording: TASK-016's purpose section refers to
// `started_at`, but `ListTimedOutRuns`' implementation in
// `internal/store/postgres_runs.go` keys on `timeout_at <= $1`. The
// scenario's mutation target therefore is the `timeout_at` predicate,
// matching the implementation that ships today.
//
// TASK-028 update: the scenario now drives the tick via
// `harness.Clock.Advance` instead of stamping the expired run's
// `timeout_at` to the past + calling `ScanRunTimeouts` manually. The
// scheduler is constructed with `scheduler.WithClock(clock.Now)` and
// registered as an `OnAdvance` handler, so advancing the clock both
// (a) moves the scheduler's observed `now` past the workflow-derived
// deadline and (b) fires the scan synchronously in the same call. The
// fresh run is stamped to a far-future `timeout_at` to keep the
// predicate-mutation contract intact across the advance.
func TestRunTimeout_ScannerCancelsExpiredRunAndPreservesFresh(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "run-timeout-cancellation",
		Description: "harness.Clock.Advance drives ScanRunTimeouts to cancel timed-out runs, emit run_timeout, preserve the run branch (per §4.5 documented gap), and leave fresh runs untouched",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-runtime-timeout", runTimeoutWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-916", "EPIC-916", "TASK-916"),
			scenarioEngine.SeedHierarchy("INIT-917", "EPIC-917", "TASK-917"),
			scenarioEngine.SyncProjections(),

			startRunNamed("expired_run", "initiatives/init-916/epics/epic-916/tasks/task-916.md"),
			startRunNamed("fresh_run", "initiatives/init-917/epics/epic-917/tasks/task-917.md"),

			// Push the fresh run's deadline past the advance window so
			// the predicate-isolation assertion below still has bite.
			stampRunTimeoutAt("fresh_run", 168*time.Hour),

			// Wire a clock-driven scheduler + recorder; subsequent
			// Advance calls both bump the observed time and fire
			// ScanRunTimeouts in the same call.
			registerRunTimeoutScannerOnClock(),

			advanceHarnessClock(2 * time.Hour),

			assertRunStatusByKey("expired_run", domain.RunStatusCancelled),
			assertRunStatusByKey("fresh_run", domain.RunStatusActive),

			assertRunTimeoutEventEmittedFor("expired_run"),
			assertRunTimeoutEventNotEmittedFor("fresh_run"),

			assertRunBranchPreserved("expired_run"),
		},
	})
}

const stateRecorderRouter = "run_timeout_recorder"

// startRunNamed mirrors scenarioEngine.StartRun but stores the resulting
// run ID and branch name under a caller-supplied prefix so the scenario
// can keep two distinct runs in flight without overwriting the default
// `run_id` slot.
func startRunNamed(stateKey, taskPath string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "start-run-" + stateKey,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("start run for %s: %w", taskPath, err)
			}
			sc.Set(stateKey+"_id", result.Run.RunID)
			sc.Set(stateKey+"_branch", result.Run.BranchName)
			return nil
		},
	}
}

// stampRunTimeoutAt overwrites the named run's `timeout_at` to
// `time.Now() + offset` so the harness can pin a deadline that survives
// a known clock advance. Uses `Store.ExecRaw` (the existing test-only
// escape hatch in `internal/store/testutil.go`) rather than introducing
// a new store method — the scenario harness already relies on this
// surface. Used here to push the fresh run beyond the advance window
// without changing the workflow's natural 1h deadline that drives the
// expired run.
func stampRunTimeoutAt(stateKey string, offset time.Duration) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "stamp-timeout-at-" + stateKey,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet(stateKey + "_id").(string)
			target := time.Now().Add(offset)
			if err := sc.Runtime.Store.ExecRaw(sc.Ctx,
				`UPDATE runtime.runs SET timeout_at = $1 WHERE run_id = $2`,
				target, runID,
			); err != nil {
				return fmt.Errorf("stamp timeout_at for %s: %w", runID, err)
			}
			return nil
		},
	}
}

// registerRunTimeoutScannerOnClock constructs a clock-aware scheduler
// + recording event router and hooks `ScanRunTimeouts` onto the
// harness clock's `OnAdvance` callback. The recorder (rather than the
// runtime's QueueRouter) keeps event assertions synchronous, avoiding
// a race against the MemoryQueue's background dispatch. After this
// step runs, every subsequent `clock.Advance(d)` both bumps the
// observed time and fires the scanner.
func registerRunTimeoutScannerOnClock() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "register-run-timeout-scanner-on-clock",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			recorder := &recordingEventRouter{}
			sched := scheduler.New(
				sc.Runtime.Store,
				recorder,
				scheduler.WithClock(sc.Runtime.Clock.Now),
			)
			sc.Set(stateRecorderRouter, recorder)
			sc.Runtime.Clock.OnAdvance("scan-run-timeouts", func() error {
				return sched.ScanRunTimeouts(sc.Ctx)
			})
			return nil
		},
	}
}

// advanceHarnessClock bumps the harness clock by d and fires every
// registered tick handler synchronously. The signal handlers see is
// the new `clock.Now()` value, so the scheduler's predicate compares
// against the advanced time without sleeping.
func advanceHarnessClock(d time.Duration) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("advance-clock-%s", d),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			if err := sc.Runtime.Clock.Advance(d); err != nil {
				return fmt.Errorf("advance harness clock by %s: %w", d, err)
			}
			return nil
		},
	}
}

// assertRunStatusByKey is the multi-run analogue of
// scenarioEngine.AssertRunStatus: it reads a run ID from a caller-named
// state slot rather than the default `run_id` one, so the scenario can
// pin both runs' statuses after the same scheduler tick.
func assertRunStatusByKey(stateKey string, want domain.RunStatus) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-status-" + stateKey + "-" + string(want),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet(stateKey + "_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run %s: %w", runID, err)
			}
			if run.Status != want {
				return fmt.Errorf("run %s status: got %s, want %s", runID, run.Status, want)
			}
			return nil
		},
	}
}

// assertRunTimeoutEventEmittedFor pins the event-emission half of the
// timeout-cancellation contract. A regression that flipped the run to
// cancelled but skipped `event.EmitLogged(EventRunTimeout)` would still
// pass the status assertion above; this step is the load-bearing check
// for that gap.
func assertRunTimeoutEventEmittedFor(stateKey string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-timeout-event-emitted-" + stateKey,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet(stateKey + "_id").(string)
			recorder := sc.MustGet(stateRecorderRouter).(*recordingEventRouter)
			for _, ev := range recorder.snapshot() {
				if ev.Type == domain.EventRunTimeout && ev.RunID == runID {
					return nil
				}
			}
			return fmt.Errorf("expected run_timeout event for run %s; recorded events: %s",
				runID, summarizeEvents(recorder.snapshot()))
		},
	}
}

// assertRunTimeoutEventNotEmittedFor is the AC mutation-test partner.
// Removing `AND timeout_at <= $1` from `ListTimedOutRuns` would surface
// here: the fresh run would also be returned, cancelled, and emit
// run_timeout — failing this assertion before the status assertion has
// a chance to fire on a later tick.
func assertRunTimeoutEventNotEmittedFor(stateKey string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-timeout-event-not-emitted-" + stateKey,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet(stateKey + "_id").(string)
			recorder := sc.MustGet(stateRecorderRouter).(*recordingEventRouter)
			for _, ev := range recorder.snapshot() {
				if ev.Type == domain.EventRunTimeout && ev.RunID == runID {
					return fmt.Errorf("unexpected run_timeout event for non-expired run %s", runID)
				}
			}
			return nil
		},
	}
}

// assertRunBranchPreserved encodes the §4.5 documented intentional gap
// for the scheduler timeout path:
//
//	"Scheduler-driven run timeouts (Scheduler.handleRunTimeout) flip a
//	 run to cancelled but do not call CleanupRunBranch; their run
//	 branches stay behind until an operator deletes them or a future
//	 change adds cleanup to that path."
//
// A regression that quietly added `CleanupRunBranch` to handleRunTimeout
// without updating the doc would surface here as a deleted branch and
// fail this step. If a future change *intentionally* wires cleanup into
// the timeout path, both this scenario and §4.5 must change in lockstep.
func assertRunBranchPreserved(stateKey string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-branch-preserved-" + stateKey,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			branch := sc.MustGet(stateKey + "_branch").(string)
			if branch == "" {
				return fmt.Errorf("run %s has no branch", stateKey)
			}
			if err := assertLocalBranchExistsAt(sc.Repo.Dir, branch); err != nil {
				return fmt.Errorf("timeout path must preserve run branch per §4.5: %w", err)
			}
			return nil
		},
	}
}

// recordingEventRouter satisfies event.EventRouter and captures every
// emitted event for synchronous post-tick assertions. Subscribe is a
// no-op — the scheduler emits but never subscribes.
type recordingEventRouter struct {
	mu     sync.Mutex
	events []domain.Event
}

func (r *recordingEventRouter) Emit(_ context.Context, ev domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingEventRouter) Subscribe(_ context.Context, _ domain.EventType, _ event.EventHandler) error {
	return nil
}

func (r *recordingEventRouter) snapshot() []domain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Event, len(r.events))
	copy(out, r.events)
	return out
}

// summarizeEvents formats a recorded event slice for failure messages
// — type plus run_id is enough to disambiguate which run was missed.
func summarizeEvents(events []domain.Event) string {
	if len(events) == 0 {
		return "<none>"
	}
	out := ""
	for i, ev := range events {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("{type=%s run_id=%s}", ev.Type, ev.RunID)
	}
	return out
}

// assertLocalBranchExistsAt is the run-timeout scenario's branch
// presence check. It mirrors `assertBranchExistsAt` (in
// `multi_repo_run_lifecycle_test.go`) but is duplicated here to keep
// the timeout scenario file self-contained — `assertBranchExistsAt`
// pre-dates this scenario and is owned by the multi-repo lifecycle
// path; reusing it would couple the two scenarios' failure surfaces.
func assertLocalBranchExistsAt(repoDir, branchName string) error {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/"+branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
			return fmt.Errorf("branch %q missing in %s (run-timeout path must preserve it):\n%s",
				branchName, filepath.Base(repoDir), out)
		}
		return fmt.Errorf("git rev-parse --verify refs/heads/%s in %s: %w\n%s",
			branchName, filepath.Base(repoDir), err, out)
	}
	return nil
}
