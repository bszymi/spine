---
id: TASK-004
type: Task
title: "Fix missing body size limits and non-constant-time token comparison"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-004 — Fix Missing Body Size Limits and Non-Constant-Time Token Comparison

---

## Purpose

Three handlers bypass the `decodeJSON` helper that enforces a 1MB body size limit, allowing unbounded request payloads:

- `handleTokenCreate` (`handlers_tokens.go:35`)
- `handleCreateBranch` (`handlers_divergence.go:29`)
- `handleWorkspaceCreate` (`handlers_workspaces.go:55`)

Additionally, the operator token comparison in `handlers_workspaces.go:34-35` uses Go's `!=` which is not constant-time, potentially leaking token bytes via timing side-channel.

---

## Deliverable

1. Replace `json.NewDecoder(r.Body).Decode` with `decodeJSON(r, &req)` in the three handlers listed above
2. Use `subtle.ConstantTimeCompare` for the operator token comparison in `operatorTokenMiddleware`
3. Cap `handleQueryHistory` limit parameter (currently unbounded at `handlers_query.go:96-101`)

---

## Acceptance Criteria

- All handlers use `decodeJSON` for body parsing (no direct `json.NewDecoder` on `r.Body`)
- Operator token comparison uses `crypto/subtle.ConstantTimeCompare`
- Query history limit is capped at a reasonable maximum (e.g., 200)
- Existing tests pass
