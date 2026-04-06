//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Workflow for blocking tests — simple single-step task workflow.
const blockingWorkflowYAML = `id: task-blocking
name: Blocking Test Workflow
version: "1.0"
status: Active
description: Workflow for testing dependency blocking.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills:
        - execution
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

// Task artifact with blocked_by link.
const blockedTaskContent = `---
id: TASK-B
type: Task
title: Blocked Task
status: Pending
links:
  - type: parent
    target: /initiatives/init-blk/epics/epic-blk/epic.md
  - type: blocked_by
    target: /initiatives/init-blk/epics/epic-blk/tasks/task-a.md
---

# Blocked Task B

Blocked by Task A.
`

const blockerTaskContent = `---
id: TASK-A
type: Task
title: Blocker Task
status: Pending
links:
  - type: parent
    target: /initiatives/init-blk/epics/epic-blk/epic.md
---

# Blocker Task A
`

// TestBlocking_StartRunRejectedForBlockedTask verifies that StartRun
// returns an error when the task has unresolved blocked_by links.
func TestBlocking_StartRunRejectedForBlockedTask(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "blocking-start-run-rejected",
		Description: "StartRun fails for a task with unresolved blocked_by links",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedWorkflow("task-blocking", blockingWorkflowYAML),
			engine.SeedHierarchy("INIT-BLK", "EPIC-BLK", "TASK-A"),
			// Write the blocked task (TASK-B) with blocked_by link to TASK-A.
			engine.WriteAndCommit(
				"initiatives/init-blk/epics/epic-blk/tasks/task-b.md",
				blockedTaskContent,
				"add blocked task B",
			),
			engine.SyncProjections(),

			// Register skills and actors.
			registerSkill("sk-exec", "execution", "execution"),
			registerActor("dev-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("dev-1", "sk-exec"),

			// Attempt to start run on blocked task — should fail.
			engine.ExpectError("start-blocked-run", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx,
					"initiatives/init-blk/epics/epic-blk/tasks/task-b.md")
				return err
			}, "blocked_error"),
		},
	})
}

// TestBlocking_StartRunSucceedsAfterBlockerCompletes verifies that once
// the blocking task completes, the dependent task can start.
func TestBlocking_StartRunSucceedsAfterBlockerCompletes(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "blocking-start-after-blocker-completes",
		Description: "StartRun succeeds for a previously-blocked task after blocker completes",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedWorkflow("task-blocking", blockingWorkflowYAML),
			engine.SeedHierarchy("INIT-BL2", "EPIC-BL2", "TASK-A2"),

			// Write TASK-A2 (blocker) as Completed.
			engine.WriteAndCommit(
				"initiatives/init-bl2/epics/epic-bl2/tasks/task-a2.md",
				`---
id: TASK-A2
type: Task
title: Completed Blocker
status: Completed
links:
  - type: parent
    target: /initiatives/init-bl2/epics/epic-bl2/epic.md
---

# Completed Blocker Task
`,
				"add completed blocker",
			),

			// Write TASK-B2 blocked_by TASK-A2 (which is completed).
			engine.WriteAndCommit(
				"initiatives/init-bl2/epics/epic-bl2/tasks/task-b2.md",
				`---
id: TASK-B2
type: Task
title: Previously Blocked Task
status: Pending
links:
  - type: parent
    target: /initiatives/init-bl2/epics/epic-bl2/epic.md
  - type: blocked_by
    target: /initiatives/init-bl2/epics/epic-bl2/tasks/task-a2.md
---

# Task B2
`,
				"add previously blocked task",
			),
			engine.SyncProjections(),

			registerSkill("sk-exec2", "execution", "execution"),
			registerActor("dev-2", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("dev-2", "sk-exec2"),

			// Start run should succeed — blocker is already completed.
			engine.StartRun("initiatives/init-bl2/epics/epic-bl2/tasks/task-b2.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
		},
	})
}
