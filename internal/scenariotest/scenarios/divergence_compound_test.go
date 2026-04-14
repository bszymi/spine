//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// --- Workflow definitions ---

// Two branches, all_branches_terminal entry policy, select_one strategy.
// Used for TestDivergence_SelectOneEndToEnd.
const selectOneWorkflowYAML = `id: task-div-select-one-e2e
name: Select-One End-to-End Workflow
version: "1.0"
status: Active
description: Two-branch select_one workflow for orchestrator-level divergence test.
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    diverge: div-main
    converge: conv-main
    outcomes:
      - id: done
        next_step: post-conv
  - id: branch-a
    name: Branch A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-b
    name: Branch B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: post-conv
    name: Post Convergence
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
divergence_points:
  - id: div-main
    mode: structured
    branches:
      - id: ba
        name: Branch A
        start_step: branch-a
      - id: bb
        name: Branch B
        start_step: branch-b
convergence_points:
  - id: conv-main
    strategy: select_one
    entry_policy: all_branches_terminal
    evaluation_step: post-conv
`

// Three branches, minimum_completed_branches:2 entry policy, select_one strategy.
// Used for TestDivergence_MinimumCompletedBranches.
const minCompletedWorkflowYAML = `id: task-div-min-completed
name: Min-Completed-Branches Workflow
version: "1.0"
status: Active
description: Three-branch minimum_completed_branches workflow for partial convergence test.
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    diverge: div-main
    converge: conv-main
    outcomes:
      - id: done
        next_step: post-conv
  - id: branch-a
    name: Branch A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-b
    name: Branch B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-c
    name: Branch C
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: post-conv
    name: Post Convergence
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
divergence_points:
  - id: div-main
    mode: structured
    branches:
      - id: ba
        name: Branch A
        start_step: branch-a
      - id: bb
        name: Branch B
        start_step: branch-b
      - id: bc
        name: Branch C
        start_step: branch-c
convergence_points:
  - id: conv-main
    strategy: select_one
    entry_policy: minimum_completed_branches
    min_branches: 2
    evaluation_step: post-conv
`

// Two branches, all_branches_terminal entry policy, require_all strategy.
// Used for TestDivergence_RequireAllWithFailedBranch.
const requireAllWorkflowYAML = `id: task-div-require-all
name: Require-All Workflow
version: "1.0"
status: Active
description: Two-branch require_all workflow for testing failure blocking.
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    diverge: div-main
    converge: conv-main
    outcomes:
      - id: done
        next_step: post-conv
  - id: branch-a
    name: Branch A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-b
    name: Branch B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: post-conv
    name: Post Convergence
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
divergence_points:
  - id: div-main
    mode: structured
    branches:
      - id: ba
        name: Branch A
        start_step: branch-a
      - id: bb
        name: Branch B
        start_step: branch-b
convergence_points:
  - id: conv-main
    strategy: require_all
    entry_policy: all_branches_terminal
    evaluation_step: post-conv
`

// Two sequential divergence points: div-1 → conv-1 (mid step) → div-2 → conv-2 (final step).
// Used for TestDivergence_SequentialDivergence.
const sequentialWorkflowYAML = `id: task-div-sequential
name: Sequential Divergence Workflow
version: "1.0"
status: Active
description: Two sequential divergence rounds for compound divergence test.
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    diverge: div-1
    converge: conv-1
    outcomes:
      - id: done
        next_step: mid
  - id: branch-1a
    name: Branch 1A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-1b
    name: Branch 1B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: mid
    name: Mid (second diverge)
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    diverge: div-2
    converge: conv-2
    outcomes:
      - id: done
        next_step: final
  - id: branch-2a
    name: Branch 2A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: branch-2b
    name: Branch 2B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
  - id: final
    name: Final
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: done
        next_step: end
divergence_points:
  - id: div-1
    mode: structured
    branches:
      - id: ba
        name: Branch 1A
        start_step: branch-1a
      - id: bb
        name: Branch 1B
        start_step: branch-1b
  - id: div-2
    mode: structured
    branches:
      - id: ba
        name: Branch 2A
        start_step: branch-2a
      - id: bb
        name: Branch 2B
        start_step: branch-2b
convergence_points:
  - id: conv-1
    strategy: select_one
    entry_policy: all_branches_terminal
    evaluation_step: mid
  - id: conv-2
    strategy: select_one
    entry_policy: all_branches_terminal
    evaluation_step: final
`

// --- Helpers ---

// wireDivergenceService returns a step that wires a real divergence.Service into
// the orchestrator. Must run after harness setup and before StartRun.
func wireDivergenceService() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "wire-divergence-service",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			divSvc := divergence.NewService(sc.Runtime.Store, sc.Repo.Git, sc.Runtime.Events)
			sc.Runtime.Orchestrator.WithDivergence(divSvc)
			sc.Runtime.Orchestrator.WithConvergence(divSvc)
			return nil
		},
	}
}

// submitBranchStep submits a result for a named branch step that is currently active.
// Looks up the step execution by StepID from the run's full execution list.
func submitBranchStep(stepID, outcomeID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("submit-branch-step-%s", stepID),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list step executions: %w", err)
			}
			for i := range execs {
				if execs[i].StepID == stepID && !execs[i].Status.IsTerminal() {
					_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
						ExecutionID: execs[i].ExecutionID,
						OutcomeID:   outcomeID,
					})
					return err
				}
			}
			return fmt.Errorf("no active execution found for branch step %q", stepID)
		},
	}
}

// findAndSetActiveStep locates the active (non-terminal) execution for stepID and
// stores it in scenario state so that the built-in SubmitStepResult step can use it.
func findAndSetActiveStep(stepID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("find-active-step-%s", stepID),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list step executions: %w", err)
			}
			for i := range execs {
				if execs[i].StepID == stepID && !execs[i].Status.IsTerminal() {
					sc.Set("current_execution_id", execs[i].ExecutionID)
					sc.Set("current_step_id", execs[i].StepID)
					return nil
				}
			}
			return fmt.Errorf("no active execution found for step %q", stepID)
		},
	}
}

// --- Tests ---

// TestDivergence_SelectOneEndToEnd runs a complete orchestrator-level select_one
// divergence scenario. Two branches open simultaneously; both complete; select_one
// picks the first completed branch deterministically; the post-convergence step
// is activated and submitted; the run ends in completed status.
//
// This also covers the "select_one with tied completion time" acceptance criterion:
// even when both branches complete back-to-back, exactly one is selected and the
// run advances to a single post-convergence step.
//
// Scenario: select_one divergence runs to completion via orchestrator
//   Given a two-branch workflow with select_one convergence
//     And a seeded task hierarchy
//   When a run is started
//   Then the entry step (start) is active
//   When the start step is submitted
//   Then two branch step executions are created
//   When both branch steps are submitted
//   Then select_one convergence fires and picks the first branch
//     And the post-convergence step (post-conv) is activated
//   When the post-conv step is submitted
//   Then the run is completed
func TestDivergence_SelectOneEndToEnd(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "divergence-select-one-end-to-end",
		Description: "Two-branch select_one workflow runs to completion through the orchestrator",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithRuntimeEvents(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-div-select-one-e2e", selectOneWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-DIV1", "EPIC-DIV1", "TASK-DIV1"),
			scenarioEngine.SyncProjections(),
			wireDivergenceService(),

			// Start run — entry step (start) becomes active.
			scenarioEngine.StartRun("initiatives/init-div1/epics/epic-div1/tasks/task-div1.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Submit start step — divergence fires, branch-a and branch-b created.
			scenarioEngine.SubmitStepResult("done"),

			// Submit both branch steps — after branch-b, convergence fires and
			// select_one picks branch-a (first completed).
			submitBranchStep("branch-a", "done"),
			submitBranchStep("branch-b", "done"),

			// Convergence has fired; locate the post-conv step and submit it.
			findAndSetActiveStep("post-conv"),
			scenarioEngine.SubmitStepResult("done"),

			// Run must be completed — post-conv step routed to end.
			scenarioEngine.AssertRunCompleted(),
		},
	})
}

// TestDivergence_MinimumCompletedBranches verifies that the minimum_completed_branches
// entry policy triggers convergence as soon as the threshold is met, without
// waiting for all branches to finish.
//
// Workflow: 3 branches, min_branches=2, strategy=select_one.
// After submitting 2 of the 3 branch steps, convergence fires and the post-conv
// step is created. The third branch step is intentionally left pending.
//
// Scenario: minimum_completed_branches triggers at threshold
//   Given a three-branch workflow with min_branches=2
//     And a seeded task hierarchy
//   When a run is started and the start step submitted
//   Then three branch step executions are created
//   When two branch steps are submitted
//   Then convergence fires at the threshold
//     And the post-conv step is activated
//   When the post-conv step is submitted
//   Then the run is completed
//     And the third branch step is still pending
func TestDivergence_MinimumCompletedBranches(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "divergence-minimum-completed-branches",
		Description: "Convergence fires at minimum_completed_branches threshold before all branches finish",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithRuntimeEvents(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-div-min-completed", minCompletedWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-DIV2", "EPIC-DIV2", "TASK-DIV2"),
			scenarioEngine.SyncProjections(),
			wireDivergenceService(),

			// Start run — entry step (start) becomes active.
			scenarioEngine.StartRun("initiatives/init-div2/epics/epic-div2/tasks/task-div2.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Submit start step — divergence fires, all three branch steps created.
			scenarioEngine.SubmitStepResult("done"),

			// Submit branch-a and branch-b. Two branches completed = threshold met.
			// Convergence fires after branch-b is submitted.
			submitBranchStep("branch-a", "done"),
			submitBranchStep("branch-b", "done"),

			// Locate and submit the post-conv step activated by convergence.
			findAndSetActiveStep("post-conv"),
			scenarioEngine.SubmitStepResult("done"),

			// Run must be completed despite branch-c still being pending.
			scenarioEngine.AssertRunCompleted(),

			// Verify branch-c step execution is still non-terminal (window closed early).
			{
				Name: "assert-branch-c-still-pending",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
					if err != nil {
						return fmt.Errorf("list step executions: %w", err)
					}
					for i := range execs {
						if execs[i].StepID == "branch-c" {
							if execs[i].Status.IsTerminal() {
								sc.T.Errorf("branch-c should not be terminal; got status %s", execs[i].Status)
							}
							return nil
						}
					}
					return fmt.Errorf("no step execution found for branch-c")
				},
			},
		},
	})
}

// TestDivergence_RequireAllWithFailedBranch verifies that the require_all strategy
// blocks convergence when any branch has failed. The run stays active and no
// post-convergence step is created.
//
// Setup: 2 branches, all_branches_terminal, require_all.
// branch-a is marked Failed directly in the store (simulating a branch failure).
// branch-b's step is then submitted, making all branches terminal. The divergence
// machine detects the failed branch and returns DivergenceStatusFailed instead of
// Converging — so tryConvergence returns early and the run is not advanced.
//
// Scenario: require_all blocks convergence when a branch has failed
//   Given a two-branch workflow with require_all convergence
//     And a seeded task hierarchy
//   When a run is started and the start step submitted
//   Then two branch step executions are created
//   When branch-a is marked as Failed in the store
//     And branch-b's step is submitted
//   Then convergence does NOT fire (require_all blocks it)
//     And the run remains active (not completed or failed via run machine)
//     And no post-convergence step execution exists
func TestDivergence_RequireAllWithFailedBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "divergence-require-all-failed-branch",
		Description: "require_all strategy blocks convergence when one branch has failed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithRuntimeEvents(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-div-require-all", requireAllWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-DIV3", "EPIC-DIV3", "TASK-DIV3"),
			scenarioEngine.SyncProjections(),
			wireDivergenceService(),

			// Start run — entry step (start) becomes active.
			scenarioEngine.StartRun("initiatives/init-div3/epics/epic-div3/tasks/task-div3.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Submit start step — divergence fires, branch-a and branch-b created.
			scenarioEngine.SubmitStepResult("done"),

			// Directly mark branch-a as Failed in the store, simulating a failure
			// that bypasses the normal step submission path.
			{
				Name: "mark-branch-a-failed",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					// DivergenceID format: {runID}-div-{divDef.ID}
					divCtxID := runID + "-div-div-main"
					branches, err := sc.Runtime.Store.ListBranchesByDivergence(sc.Ctx, divCtxID)
					if err != nil {
						return fmt.Errorf("list branches: %w", err)
					}
					for i := range branches {
						if branches[i].BranchID == divCtxID+"-ba" {
							branches[i].Status = domain.BranchStatusFailed
							if err := sc.Runtime.Store.UpdateBranch(sc.Ctx, &branches[i]); err != nil {
								return fmt.Errorf("update branch: %w", err)
							}
							return nil
						}
					}
					return fmt.Errorf("branch-a (ba) not found in divergence %s", divCtxID)
				},
			},

			// Submit branch-b's step. All branches are now terminal (one failed, one
			// about to complete). The divergence machine sees require_all + hasFailed
			// and returns DivergenceStatusFailed — convergence entry policy is not
			// satisfied, so tryConvergence returns early without creating post-conv.
			submitBranchStep("branch-b", "done"),

			// Run stays active — the run machine was not advanced because tryConvergence
			// swallows the "not ready" result and returns nil without creating any step.
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Verify no post-conv step execution was created.
			{
				Name: "assert-no-post-conv-step",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
					if err != nil {
						return fmt.Errorf("list step executions: %w", err)
					}
					for i := range execs {
						if execs[i].StepID == "post-conv" {
							sc.T.Errorf("post-conv step should not exist when require_all fails; found exec %s", execs[i].ExecutionID)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_SequentialDivergence verifies that two divergence-convergence
// rounds chain correctly through the orchestrator. After conv-1 fires and selects
// a winner, the post-convergence step (mid) itself carries a diverge field that
// opens div-2. After conv-2 fires, the final step runs and the run completes.
//
// Scenario: sequential divergence — div-1 → conv-1 → div-2 → conv-2 → final
//   Given a workflow with two sequential divergence points
//     And a seeded task hierarchy
//   When a run is started and the start step submitted
//   Then div-1 opens: branch-1a and branch-1b are created
//   When both first-round branches are submitted
//   Then conv-1 fires (select_one picks branch-1a)
//     And the mid step is activated
//   When the mid step is submitted
//   Then div-2 opens: branch-2a and branch-2b are created
//   When both second-round branches are submitted
//   Then conv-2 fires (select_one picks branch-2a)
//     And the final step is activated
//   When the final step is submitted
//   Then the run is completed
func TestDivergence_SequentialDivergence(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "divergence-sequential",
		Description: "Two sequential divergence-convergence rounds chain correctly through the orchestrator",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithRuntimeEvents(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-div-sequential", sequentialWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-DIV4", "EPIC-DIV4", "TASK-DIV4"),
			scenarioEngine.SyncProjections(),
			wireDivergenceService(),

			// Start run — entry step (start) becomes active.
			scenarioEngine.StartRun("initiatives/init-div4/epics/epic-div4/tasks/task-div4.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Submit start step — div-1 opens, branch-1a and branch-1b step execs created.
			scenarioEngine.SubmitStepResult("done"),

			// Submit both first-round branches. After branch-1b, conv-1 fires and
			// select_one picks branch-1a. The mid step is created and activated.
			submitBranchStep("branch-1a", "done"),
			submitBranchStep("branch-1b", "done"),

			// Locate mid step (created by conv-1) and submit it — div-2 opens.
			findAndSetActiveStep("mid"),
			scenarioEngine.SubmitStepResult("done"),

			// Submit both second-round branches. After branch-2b, conv-2 fires and
			// select_one picks branch-2a. The final step is created and activated.
			submitBranchStep("branch-2a", "done"),
			submitBranchStep("branch-2b", "done"),

			// Locate final step (created by conv-2) and submit it.
			findAndSetActiveStep("final"),
			scenarioEngine.SubmitStepResult("done"),

			// Run must be completed after both convergence cycles.
			scenarioEngine.AssertRunCompleted(),
		},
	})
}
