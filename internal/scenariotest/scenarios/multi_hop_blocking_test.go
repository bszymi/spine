//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// mhbPendingTask returns content for a pending task with no blocked_by links.
func mhbPendingTask(id, initSlug, epicSlug string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Pending Task %s"
status: Pending
epic: /initiatives/%s/epics/%s/epic.md
initiative: /initiatives/%s/initiative.md
links:
  - type: parent
    target: /initiatives/%s/epics/%s/epic.md
---

# Pending Task %s
`, id, id, initSlug, epicSlug, initSlug, initSlug, epicSlug, id)
}

// mhbCompletedTask returns content for a completed task.
func mhbCompletedTask(id, initSlug, epicSlug string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Completed Task %s"
status: Completed
epic: /initiatives/%s/epics/%s/epic.md
initiative: /initiatives/%s/initiative.md
links:
  - type: parent
    target: /initiatives/%s/epics/%s/epic.md
---

# Completed Task %s
`, id, id, initSlug, epicSlug, initSlug, initSlug, epicSlug, id)
}

// mhbCancelledTask returns content for a cancelled task.
func mhbCancelledTask(id, initSlug, epicSlug string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Cancelled Task %s"
status: Cancelled
epic: /initiatives/%s/epics/%s/epic.md
initiative: /initiatives/%s/initiative.md
links:
  - type: parent
    target: /initiatives/%s/epics/%s/epic.md
---

# Cancelled Task %s
`, id, id, initSlug, epicSlug, initSlug, initSlug, epicSlug, id)
}

// mhbBlockedTask returns content for a task blocked by exactly one other task.
func mhbBlockedTask(id, initSlug, epicSlug, blockerTaskSlug string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Blocked Task %s"
status: Pending
epic: /initiatives/%s/epics/%s/epic.md
initiative: /initiatives/%s/initiative.md
links:
  - type: parent
    target: /initiatives/%s/epics/%s/epic.md
  - type: blocked_by
    target: /initiatives/%s/epics/%s/tasks/%s.md
---

# Blocked Task %s
`, id, id, initSlug, epicSlug, initSlug, initSlug, epicSlug, initSlug, epicSlug, blockerTaskSlug, id)
}

// mhbDoubleBlockedTask returns content for a task blocked by two tasks.
func mhbDoubleBlockedTask(id, initSlug, epicSlug, blocker1Slug, blocker2Slug string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Double Blocked Task %s"
status: Pending
epic: /initiatives/%s/epics/%s/epic.md
initiative: /initiatives/%s/initiative.md
links:
  - type: parent
    target: /initiatives/%s/epics/%s/epic.md
  - type: blocked_by
    target: /initiatives/%s/epics/%s/tasks/%s.md
  - type: blocked_by
    target: /initiatives/%s/epics/%s/tasks/%s.md
---

# Double Blocked Task %s
`, id, id, initSlug, epicSlug, initSlug, initSlug, epicSlug, initSlug, epicSlug, blocker1Slug, initSlug, epicSlug, blocker2Slug, id)
}

// TestBlocking_TwoHopChain verifies that a three-task chain (A blocks B, B
// blocks C) requires both hops to be completed before C can start: completing
// A alone leaves C blocked (B is still pending); completing B then unblocks C.
//
// Scenario: two-hop blocking chain — C startable only after A and B complete
//
//	Given Task A with no blockers
//	  And Task B blocked_by Task A
//	  And Task C blocked_by Task B
//	When StartRun is attempted for C
//	Then it fails (B is pending)
//	When Task A is completed
//	  And StartRun is attempted for C again
//	Then it still fails (B is still pending)
//	When Task B is also completed
//	  And StartRun is attempted for C
//	Then it succeeds and the run is Active
func TestBlocking_TwoHopChain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const (
		initID   = "INIT-701"
		epicID   = "EPIC-701"
		initSlug = "init-701"
		epicSlug = "epic-701"
		pathA    = "initiatives/init-701/epics/epic-701/tasks/task-711.md"
		pathB    = "initiatives/init-701/epics/epic-701/tasks/task-712.md"
		pathC    = "initiatives/init-701/epics/epic-701/tasks/task-713.md"
	)

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "blocking-two-hop-chain",
		Description: "C startable only after both A and B complete in A→B→C chain",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-blocking", blockingWorkflowYAML),

			// Task A: no blockers — seeded through FixtureHierarchy so the
			// directory structure exists and the workflow resolver can operate.
			scenarioEngine.SeedHierarchy(initID, epicID, "TASK-711"),

			// Task B: blocked_by A.
			scenarioEngine.WriteAndCommit(pathB,
				mhbBlockedTask("TASK-712", initSlug, epicSlug, "task-711"),
				"add task-712 (blocked by task-711)",
			),

			// Task C: blocked_by B (the mid-chain hop).
			scenarioEngine.WriteAndCommit(pathC,
				mhbBlockedTask("TASK-713", initSlug, epicSlug, "task-712"),
				"add task-713 (blocked by task-712)",
			),

			scenarioEngine.SyncProjections(),

			// C is blocked — B is pending, which is itself blocked by A.
			scenarioEngine.ExpectError("start-c-while-b-pending",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathC)
					return err
				},
				"",
			),

			// Complete A — B is still pending, so C remains blocked.
			scenarioEngine.WriteAndCommit(pathA,
				mhbCompletedTask("TASK-711", initSlug, epicSlug),
				"mark task-711 completed",
			),
			scenarioEngine.SyncProjections(),

			scenarioEngine.ExpectError("start-c-while-b-still-pending",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathC)
					return err
				},
				"",
			),

			// Complete B — the last hop is resolved, C can now start.
			scenarioEngine.WriteAndCommit(pathB,
				mhbCompletedTask("TASK-712", initSlug, epicSlug),
				"mark task-712 completed",
			),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun(pathC),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
		},
	})
}

// TestBlocking_AutomatedBlockerResolution verifies that a blocker resolved
// through the workflow engine — rather than a direct status write — correctly
// unblocks a dependent task. The blocker (B) is completed by submitting its
// workflow steps programmatically (no human claim), and A is verified to be
// startable once the merge has landed on main.
//
// Scenario: automated step completion of blocker unblocks dependent task
//
//	Given Task A blocked_by Task B
//	  And Task B has no blockers
//	When StartRun is attempted for A
//	Then it fails (B is pending)
//	When B is started and its workflow steps are submitted (automated)
//	  And projections are rebuilt
//	Then StartRun for A succeeds and the run is Active
func TestBlocking_AutomatedBlockerResolution(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const (
		initID   = "INIT-702"
		epicID   = "EPIC-702"
		initSlug = "init-702"
		epicSlug = "epic-702"
		// B is the blocker; A is the blocked task.
		pathB = "initiatives/init-702/epics/epic-702/tasks/task-721.md"
		pathA = "initiatives/init-702/epics/epic-702/tasks/task-722.md"
	)

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "blocking-automated-blocker-resolution",
		Description: "Automated step submission completing blocker unblocks dependent task",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			// Mutation workflow has commit.status: Completed on its terminal outcome,
			// so the artifact status on main is updated to Completed when the run
			// merges — enabling IsBlocked to detect the resolved state via projection.
			seedMutationWorkflow(),

			// B: no blockers — seeded so the full hierarchy exists for StartRun.
			scenarioEngine.SeedHierarchy(initID, epicID, "TASK-721"),

			// A: blocked_by B.
			scenarioEngine.WriteAndCommit(pathA,
				mhbBlockedTask("TASK-722", initSlug, epicSlug, "task-721"),
				"add task-722 (blocked by task-721)",
			),

			scenarioEngine.SyncProjections(),

			// A cannot start while B is still pending.
			scenarioEngine.ExpectError("start-a-while-b-pending",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathA)
					return err
				},
				"",
			),

			// Run B's workflow to completion via programmatic step submission —
			// no human actor claims any step.
			scenarioEngine.StartRun(pathB),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			// Accepting the review triggers commit.status: Completed on the branch,
			// which applyCommitStatus writes before merging to main.
			scenarioEngine.SubmitStepResult("accepted"),
			scenarioEngine.AssertRunCompleted(),

			// Rebuild projections — B is now Completed on main.
			scenarioEngine.SyncProjections(),

			// A is no longer blocked: B reached a terminal status through the
			// workflow engine, not a direct file write.
			scenarioEngine.StartRun(pathA),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
		},
	})
}

// TestBlocking_MultipleBlockers verifies that a task with two separate
// blocked_by links cannot start until BOTH blockers are resolved. Completing
// only one blocker is not sufficient.
//
// Scenario: all blockers must complete before the dependent task can start
//
//	Given Task C blocked_by Task A and Task B
//	  And Task A is pending
//	  And Task B is pending
//	When StartRun is attempted for C
//	Then it fails (both A and B are pending)
//	When Task A is completed
//	  And StartRun is attempted for C
//	Then it still fails (B is still pending)
//	When Task B is also completed
//	  And StartRun is attempted for C
//	Then it succeeds and the run is Active
func TestBlocking_MultipleBlockers(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const (
		initID   = "INIT-703"
		epicID   = "EPIC-703"
		initSlug = "init-703"
		epicSlug = "epic-703"
		pathA    = "initiatives/init-703/epics/epic-703/tasks/task-731.md"
		pathB    = "initiatives/init-703/epics/epic-703/tasks/task-732.md"
		pathC    = "initiatives/init-703/epics/epic-703/tasks/task-733.md"
	)

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "blocking-multiple-blockers",
		Description: "C startable only when all of its blocked_by links are resolved",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-blocking", blockingWorkflowYAML),

			// A and B: independent tasks, no blockers.
			scenarioEngine.SeedHierarchy(initID, epicID, "TASK-731"),
			scenarioEngine.WriteAndCommit(pathB,
				mhbPendingTask("TASK-732", initSlug, epicSlug),
				"add task-732 (no blockers)",
			),

			// C: blocked by both A and B.
			scenarioEngine.WriteAndCommit(pathC,
				mhbDoubleBlockedTask("TASK-733", initSlug, epicSlug, "task-731", "task-732"),
				"add task-733 (blocked by task-731 and task-732)",
			),

			scenarioEngine.SyncProjections(),

			// Both blockers pending — start rejected.
			scenarioEngine.ExpectError("start-c-both-pending",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathC)
					return err
				},
				"",
			),

			// Complete A only — B is still pending, so C is still blocked.
			scenarioEngine.WriteAndCommit(pathA,
				mhbCompletedTask("TASK-731", initSlug, epicSlug),
				"mark task-731 completed",
			),
			scenarioEngine.SyncProjections(),

			scenarioEngine.ExpectError("start-c-one-blocker-remaining",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathC)
					return err
				},
				"",
			),

			// Complete B — all blockers resolved, C can start.
			scenarioEngine.WriteAndCommit(pathB,
				mhbCompletedTask("TASK-732", initSlug, epicSlug),
				"mark task-732 completed",
			),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun(pathC),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
		},
	})
}

// TestBlocking_CancelledBlockerStaysBlocking verifies that cancelling a blocker
// does NOT unblock the dependent task — cancelled ≠ completed.
//
// NOTE: This test currently FAILS because isTerminalArtifactStatus in
// internal/engine/blocking.go treats StatusCancelled as terminal, which
// causes IsBlocked to return false (not blocked) when the blocker is
// cancelled. The implementation must be corrected to exclude Cancelled from
// the terminal set used for blocking resolution, or to introduce a separate
// "resolved" terminal set that requires Completed status.
//
// Scenario: cancelled blocker does not unblock dependent task
//
//	Given Task A blocked_by Task B
//	  And Task B is pending
//	When StartRun is attempted for A
//	Then it fails (B is pending)
//	When Task B is cancelled
//	  And projections are rebuilt
//	Then StartRun for A still fails (cancelled ≠ completed: A remains blocked)
func TestBlocking_CancelledBlockerStaysBlocking(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const (
		initID   = "INIT-704"
		epicID   = "EPIC-704"
		initSlug = "init-704"
		epicSlug = "epic-704"
		pathB    = "initiatives/init-704/epics/epic-704/tasks/task-741.md"
		pathA    = "initiatives/init-704/epics/epic-704/tasks/task-742.md"
	)

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "blocking-cancelled-blocker-stays-blocking",
		Description: "A cancelled blocker does not unblock the dependent task (cancelled ≠ completed)",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-blocking", blockingWorkflowYAML),

			// B: the blocker (initially pending).
			scenarioEngine.SeedHierarchy(initID, epicID, "TASK-741"),

			// A: blocked_by B.
			scenarioEngine.WriteAndCommit(pathA,
				mhbBlockedTask("TASK-742", initSlug, epicSlug, "task-741"),
				"add task-742 (blocked by task-741)",
			),

			scenarioEngine.SyncProjections(),

			// A cannot start while B is pending.
			scenarioEngine.ExpectError("start-a-while-b-pending",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathA)
					return err
				},
				"",
			),

			// Cancel B — cancellation is not resolution: A must remain blocked.
			scenarioEngine.WriteAndCommit(pathB,
				mhbCancelledTask("TASK-741", initSlug, epicSlug),
				"mark task-741 cancelled",
			),
			scenarioEngine.SyncProjections(),

			// A must still be blocked: a cancelled blocker has not completed its
			// work and does not satisfy the dependency.
			// TODO: this assertion currently fails — fix isTerminalArtifactStatus
			// in internal/engine/blocking.go to exclude Cancelled from the set of
			// statuses that resolve a blocked_by link.
			scenarioEngine.ExpectError("start-a-after-b-cancelled",
				func(sc *scenarioEngine.ScenarioContext) error {
					_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, pathA)
					return err
				},
				"",
			),
		},
	})
}
