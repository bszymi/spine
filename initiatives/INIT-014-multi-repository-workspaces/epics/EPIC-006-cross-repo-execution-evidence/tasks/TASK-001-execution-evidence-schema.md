---
id: TASK-001
type: Task
title: Define execution evidence schema
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-001-run-affected-repositories-model.md
---

# TASK-001 - Define Execution Evidence Schema

---

## Purpose

Define the structured record that proves what happened in each affected code repository.

## Deliverable

Document and implement an evidence schema that can be stored in the primary repo or generated into an artifact on the run branch.

Fields should include:

- Run ID
- Task path
- Repository ID
- Branch name
- Base commit
- Head commit
- Changed paths summary
- Required checks
- Check results
- Validation policy references
- Actor and trace metadata

## Acceptance Criteria

- Evidence is tied to repository, branch, and commit.
- Evidence is serializable as deterministic YAML or JSON.
- Evidence can be committed to the primary repo ledger.
- Evidence excludes secrets and raw logs by default.
- Schema supports both human and automated check producers.

