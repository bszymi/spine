---
id: TASK-008
type: Task
title: "Replace 13 Server xxxFrom accessors with a generic resolve helper"
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

# TASK-008 — Generic resolve Helper for Server Accessors

---

## Purpose

`internal/gateway/server.go` L313-412 defines 13 near-identical methods — `authFrom`, `storeFrom`, `artifactsFrom`, `workflowsFrom`, `projQueryFrom`, `projSyncFrom`, `gitFrom`, `validatorFrom`, `branchCreatorFrom`, `runStarterFrom`, `runCancellerFrom`, `planningRunStarterFrom`, `wfPlanningStarterFrom` — each checking `serviceSetFromContext(ctx)` for a matching non-nil field, falling back to the corresponding `s.field`. Adding a new service today requires editing three places (field, config, accessor).

---

## Deliverable

1. Add `func resolve[T any](ctx context.Context, pick func(*workspace.ServiceSet) T, fallback T) T` to `internal/gateway/server.go`.
2. Rewrite each `xxxFrom(ctx)` as a one-line wrapper (e.g. `return resolve(ctx, func(s *workspace.ServiceSet) auth.Service { return s.Auth }, s.auth)`).
3. For the four interface-cast variants (`runStarterFrom`, `runCancellerFrom`, `planningRunStarterFrom`, `wfPlanningStarterFrom`), keep the cast local to the wrapper.
4. Confirm Go version in `go.mod` supports generics (1.18+); today's module is modern enough.

---

## Acceptance Criteria

- Each `xxxFrom(ctx)` method is at most three lines.
- No behaviour change; gateway tests pass unchanged.
