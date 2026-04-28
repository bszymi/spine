---
id: EPIC-002
type: Epic
title: "Task Schema and Repository Validation"
status: Pending
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
---

# EPIC-002 - Task Schema and Repository Validation

---

## Purpose

Let Task artifacts declare which code repositories they affect, then validate those references before a run starts.

Repository binding belongs in the governing Task because it is part of explicit intent: the task author is saying where implementation work is expected to occur.

---

## Scope

### In Scope

- `repositories` field in Task front matter
- Artifact schema and validation updates
- Query/projection support for repository metadata
- Run-start precondition checks for repository existence and activity
- Backward-compatible default behavior when `repositories` is absent

### Out of Scope

- Step-level repository routing
- Merge coordination
- Code repo source scanning

---

## Primary Outputs

- Updated artifact schema
- Repository validation rules
- Projection/query support for task repository bindings
- Unit and scenario tests

---

## Acceptance Criteria

1. A Task may declare `repositories: [repo-a, repo-b]`.
2. Missing or empty `repositories` defaults to primary-repo-only execution.
3. Unknown repository IDs fail validation with actionable errors.
4. Inactive repository IDs block run start.
5. Existing tasks without repository metadata remain valid.

