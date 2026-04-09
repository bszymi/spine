---
id: TASK-009
type: Task
title: "Add body size limit to claim and release handlers"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-009 — Add Body Size Limit to Claim and Release Handlers

---

## Purpose

`handleExecutionClaim` (`/internal/gateway/handlers_claim.go:26`) and `handleExecutionRelease` (`/internal/gateway/handlers_release.go:27`) decode request bodies via `json.NewDecoder(r.Body).Decode()` directly, bypassing the `decodeJSON` helper's 1MB `io.LimitReader`. Arbitrarily large request bodies can consume server memory.

---

## Deliverable

Replace direct `json.NewDecoder(r.Body).Decode(&req)` with the `decodeJSON(r, &req)` helper.

---

## Acceptance Criteria

- Claim and release handlers enforce the 1MB body size limit
- Oversized requests return 400 Bad Request
- Existing tests pass
