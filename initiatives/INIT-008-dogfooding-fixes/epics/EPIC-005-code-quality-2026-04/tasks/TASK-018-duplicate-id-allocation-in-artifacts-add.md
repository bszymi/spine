---
id: TASK-018
type: Task
title: Fix duplicate ID allocation in POST /artifacts/add for siblings in the same run
status: Completed
created: 2026-04-21
last_updated: 2026-04-21
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-018 — Fix duplicate ID allocation in POST /artifacts/add for siblings in the same run

---

## Purpose

Found during the 2026-04-21 SMP end-to-end smoke test of the TASK-016
applyCommitStatus cascade: two consecutive `POST /artifacts/add` calls
in the same planning run, both targeting the newly-created `EPIC-053`
as parent, both returned `"artifact_id": "TASK-001"`. The ID allocator
does not consult artifacts already written on the branch within the
same run, so sibling children collide.

Reproducer (against a Spine built from `b3b96f3` or earlier):

```
POST /runs   {mode: planning, task_path: "…/epic.md", artifact_content: "<Epic Draft>"}
→ run-XXX

POST /artifacts/add   {run_id: run-XXX, artifact_type: Task,
                        parent: "EPIC-053", title: "Cascade child one"}
→ artifact_id: TASK-001, path: …/task-001-cascade-child-one.md

POST /artifacts/add   {run_id: run-XXX, artifact_type: Task,
                        parent: "EPIC-053", title: "Cascade child two"}
→ artifact_id: TASK-001   ← same ID again
                path: …/task-001-cascade-child-two.md   ← different path, same id
```

The returned path varies because slug → filename differs, but the
artifact frontmatter on the branch ends up with `id: TASK-001` in both
files. Downstream `validate-artifacts` then correctly flags this as an
ID-uniqueness violation and kicks the run back to `draft`. The only
workarounds are manual (edit the file on the branch) or serialising
adds across separate runs.

## Deliverable

- In the artifact service's ID allocator (`internal/artifact/id_allocator.go`),
  include artifacts already present on the run's planning branch —
  not just on `main` — when computing `NextID`. Two consecutive
  `POST /artifacts/add` calls on the same run must produce `TASK-001`
  then `TASK-002`.
- Unit test: fake git client with the first task committed on the
  planning branch, assert the second call returns `TASK-002`.
- Scenario test (behind `scenario` build tag): end-to-end
  `StartPlanningRun` + two `addArtifactToRun` calls for Task children
  of a new Epic, assert both children land with distinct IDs and
  distinct paths.

## Acceptance Criteria

- A planning run that adds N sibling artifacts of the same type via
  `POST /artifacts/add` produces IDs `TASK-001 … TASK-N` with no
  collisions.
- `validate-artifacts` is not the first thing to notice the collision
  — the allocator itself catches it.
- The unit + scenario tests above are green.

## Context

Surfaced while verifying SMP TASK-008 (verify-children gate) +
TASK-016 (applyCommitStatus cascade). The smoke test had to manually
rename the second child artifact on the plan branch before the
workflow could advance.
