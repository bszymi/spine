//go:build scenario

package scenarios_test

// INIT-023 TASK-017 AC 14: the declared artifact-status lifecycle end to
// end, on the scenario runtime built with harness.WithStatusLifecycle()
// (the lifecycle on the orchestrator, the workflow service and the
// validator, with merge binding).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// TestPlanningRun_StatusCascadesToChildren_StatusLifecycle is
// TestPlanningRun_StatusCascadesToChildren with the lifecycle on: the
// planning package cascade (parent plus every artifact the run adds,
// Draft -> Pending) is the shape the lifecycle must keep working.
func TestPlanningRun_StatusCascadesToChildren_StatusLifecycle(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-status-cascades-to-children-lifecycle",
		Description: "One approval rewrites Draft->Pending across the planned package with the status lifecycle on",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithStatusLifecycle(),
		},
		Steps: []engine.Step{
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun("initiatives/init-099/initiative.md", testInitiativeContent),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.CreateArtifactOnBranch("initiatives/init-099/epics/epic-001/epic.md", childEpicContent),
			engine.CreateArtifactOnBranch("initiatives/init-099/epics/epic-001/tasks/task-001.md", cascadeChildTaskDraftContent),
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.SubmitStepResult("valid"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),
			engine.AssertFileContains("initiatives/init-099/initiative.md", "status: Pending"),
			engine.AssertFileContains("initiatives/init-099/epics/epic-001/epic.md", "status: Pending"),
			engine.AssertFileContains("initiatives/init-099/epics/epic-001/tasks/task-001.md", "status: Pending"),
		},
	})
}

// headStep records main's head under key.
func headStep(key string) engine.Step {
	return engine.Step{
		Name: "record-main-head-" + key,
		Action: func(sc *engine.ScenarioContext) error {
			h, err := sc.Repo.Git.Head(sc.Ctx)
			if err != nil {
				return err
			}
			sc.Set(key, h)
			return nil
		},
	}
}

// TestStandardRun_StatusLifecycleRefusesNonOwnedStatusEdit: a standard run
// whose branch reopens a Completed sibling task is refused at the merge.
func TestStandardRun_StatusLifecycleRefusesNonOwnedStatusEdit(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const sibling = "initiatives/init-001/epics/epic-001/tasks/task-002.md"
	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-status-lifecycle-refuses-non-owned-edit",
		Description: "A run that changes another task's status is refused; main is unchanged",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithStatusLifecycle(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit("workflows/task-default.yaml", standardRunMergeWorkflowYAML, "seed task-default workflow"),
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			{
				Name: "seed-completed-sibling",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureTask(sc, sibling, engine.ArtifactOpts{ID: "TASK-002", Status: "Completed",
						Epic: "/initiatives/init-001/epics/epic-001/epic.md", Init: "/initiatives/init-001/initiative.md"})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.StartRun("initiatives/init-001/epics/epic-001/tasks/task-001.md"),
			{
				Name: "reopen-the-sibling-on-the-run-branch",
				Action: func(sc *engine.ScenarioContext) error {
					raw, err := os.ReadFile(filepath.Join(sc.Repo.Dir, sibling))
					if err != nil {
						return err
					}
					reopened := strings.Replace(string(raw), "status: Completed", "status: Pending", 1)
					if reopened == string(raw) {
						return fmt.Errorf("sibling fixture has no Completed status line")
					}
					return engine.WriteOnBranch(sibling, reopened, "reopen sibling").Action(sc)
				},
			},
			headStep("main_before"),
			engine.SubmitStepResult("completed", "deliverable"),
			engine.SubmitStepResult("accepted"),
			engine.AssertRunStatus(domain.RunStatusFailed),
			{
				Name: "assert-main-unchanged-and-classified",
				Action: func(sc *engine.ScenarioContext) error {
					h, err := sc.Repo.Git.Head(sc.Ctx)
					if err != nil {
						return err
					}
					if want := sc.MustGet("main_before").(string); h != want {
						return fmt.Errorf("main moved: %s -> %s", want, h)
					}
					raw, err := os.ReadFile(filepath.Join(sc.Repo.Dir, sibling))
					if err != nil {
						return err
					}
					if !strings.Contains(string(raw), "status: Completed") {
						return fmt.Errorf("the sibling's status changed on main:\n%s", raw)
					}
					execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, sc.MustGet("run_id").(string))
					if err != nil {
						return err
					}
					var last *domain.StepExecution
					for i := range execs {
						if execs[i].Status == domain.StepStatusCompleted {
							last = &execs[i]
						}
					}
					if last == nil || last.ErrorDetail == nil || last.ErrorDetail.Classification != domain.FailureStatusLifecycle {
						return fmt.Errorf("last completed step = %+v, want classification %s", last, domain.FailureStatusLifecycle)
					}
					return nil
				},
			},
		},
	})
}

// TestStandardRun_StatusLifecycleArtifactsDir: with artifacts_dir "spine"
// a task-default run's commit.status reaches the task under spine/ (today
// it is silently not applied: the cascade reads run.TaskPath raw).
func TestStandardRun_StatusLifecycleArtifactsDir(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-001.md"
	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-status-lifecycle-artifacts-dir",
		Description: "commit.status is applied to the task under artifacts_dir",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
			harness.WithStatusLifecycle(),
			harness.WithArtifactsDir("spine"),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit("workflows/task-default.yaml", standardRunMergeWorkflowYAML, "seed task-default workflow"),
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.AssertFileExists("spine/" + taskPath),
			engine.SyncProjections(),
			engine.StartRun(taskPath),
			engine.WriteOnBranch("spine/initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md",
				"# Deliverable\n", "Add task deliverable"),
			engine.SubmitStepResult("completed", "deliverable"),
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),
			engine.AssertFileContains("spine/"+taskPath, "status: Completed"),
		},
	})
}
