---
id: TASK-015
type: Task
title: "Extract CLI PostFileAsBody helper in cmd/spine subcommands"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-015 — Extract PostFileAsBody CLI Helper

---

## Purpose

Five sites across `cmd/spine/cmd_artifact.go` (L28-58 create, L87-117 update) and `cmd/spine/cmd_workflow.go` (L49-98 create/update/validate) share the same sequence:

```go
content, err := os.ReadFile(file); if err != nil { return fmt.Errorf("read file: %w", err) }
c := newAPIClient()
data, err := c.Post(ctx, "...", map[string]string{"content": string(content)})
if err != nil { return err }
return printResponse(data)
```

Small blocks individually, but five copies and every future similar CLI command will add a sixth.

---

## Deliverable

1. Add `internal/cli/postfile.go` with `PostFileAsBody(ctx context.Context, client *APIClient, endpoint, path string) ([]byte, error)` that reads the file and POSTs `{ "content": "<contents>" }`.
2. Replace all five sites with the helper.

---

## Acceptance Criteria

- All five call sites use the helper.
- CLI integration tests pass unchanged.
