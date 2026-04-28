---
id: TASK-001
type: Task
title: Add Task repositories frontmatter schema
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-001-governed-repository-catalog-format.md
---

# TASK-001 - Add Task Repositories Frontmatter Schema

---

## Purpose

Make affected code repositories part of Task intent.

## Deliverable

Update the artifact schema, parser, and examples to support:

```yaml
repositories:
  - payments-service
  - api-gateway
```

Rules:

- Field is optional.
- Empty or missing field means primary-repo-only execution.
- Values are repository IDs, not URLs or paths.
- `spine` may be omitted because the primary repo always participates.

## Acceptance Criteria

- Artifact schema documents `repositories` on Task.
- Task parser preserves repository IDs in metadata.
- Invalid YAML shapes fail artifact validation.
- Existing task fixtures remain valid.
- Example tasks show single-repo and multi-repo usage.

