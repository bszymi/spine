---
id: TASK-015
type: Task
title: "Extract CLI SendFile helper in cmd/spine subcommands"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-015 — Extract SendFile CLI Helper

---

## Purpose

Five sites across `cmd/spine/cmd_artifact.go` (L28-58 create, L87-117 update) and `cmd/spine/cmd_workflow.go` (L49-98 create/update/validate) share the same sequence:

```go
body, err := os.ReadFile(file); if err != nil { return fmt.Errorf("read file: %w", err) }
c := newAPIClient()
data, err := c.<Method>(ctx, "<endpoint>", map[string]string{<field>: string(body), ...extra})
if err != nil { return err }
return printResponse(data)
```

Small blocks individually, but five copies and every future similar CLI command will add a sixth. Note the sites vary along three axes and the helper must preserve each call shape:

| Site | Method | Endpoint | Body field | Extra fields |
| --- | --- | --- | --- | --- |
| artifact create | POST | `/api/v1/artifacts` | `content` | `path` |
| artifact update | PUT | `/api/v1/artifacts/{path}` | `content` | — |
| workflow create | POST | `/api/v1/workflows` | `body` | `id` |
| workflow update | PUT | `/api/v1/workflows/{id}` | `body` | — |
| workflow validate | POST | `/api/v1/workflows/{id}/validate` | `body` | — |

---

## Deliverable

1. Add `internal/cli/postfile.go` with a `SendFile` helper parameterised across the three axes, e.g.:
   ```go
   type SendFileOpts struct {
       Method    string // http.MethodPost or http.MethodPut
       Endpoint  string
       BodyField string // "content" or "body"
       Extra     map[string]string
   }
   func SendFile(ctx context.Context, client *APIClient, file string, opts SendFileOpts) ([]byte, error)
   ```
2. Reuse the existing `APIClient.Post` / `APIClient.Put` inside the helper; no new transport plumbing.
3. Replace all five sites with the helper while preserving method, body field name, and extra fields exactly.

---

## Acceptance Criteria

- All five call sites use the helper.
- CLI integration tests pass unchanged.
