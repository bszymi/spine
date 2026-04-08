---
id: TASK-002
type: Task
title: CLI artifact create command
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/tasks/TASK-001-api-endpoint.md
---

# TASK-002 — CLI Artifact Create Command

---

## Purpose

Add `spine artifact create` CLI command that calls the API endpoint to start governed artifact creation.

---

## Deliverable

Extend CLI (likely `internal/cli/cmd_artifact.go` or new file).

### Usage

```
spine artifact create --type Task --epic EPIC-003 --title "Implement validation"
spine artifact create --type Epic --initiative INIT-003 --title "New feature epic"
spine artifact create --type ADR --title "Use event sourcing for audit log"
```

### Flags

- `--type` (required): artifact type (Task, Epic, Initiative, ADR)
- `--title` (required): artifact title
- `--epic` (optional): parent epic ID (required for Task)
- `--initiative` (optional): parent initiative ID (required for Epic)

### Behavior

1. Validate required flags based on type (Task requires --epic, Epic requires --initiative)
2. Call `POST /artifacts/create` with the payload
3. Print the result:
   ```
   Created TASK-006 "Implement validation"
   Planning run: <run-id>
   Branch: INIT-003/EPIC-003/TASK-006-implement-validation
   Workflow: artifact-creation
   ```

### Error handling

- Missing required flags: print usage help
- API error: print the error message from the response
- Connection error: print connection failure message

---

## Acceptance Criteria

- `spine artifact create --type Task --epic EPIC-003 --title "..."` works end-to-end
- Missing `--epic` for Task type prints a clear error
- Missing `--title` prints a clear error
- Output includes artifact ID, run ID, branch name, and workflow
