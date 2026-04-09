---
id: TASK-010
type: Task
title: "Validate and sanitize X-Trace-Id header"
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

# TASK-010 — Validate and Sanitize X-Trace-Id Header

---

## Purpose

The trace ID middleware in `/internal/gateway/middleware.go` (lines 156-168) accepts `X-Trace-Id` headers from clients verbatim without validation. Crafted trace IDs containing newlines, JSON metacharacters, or ANSI escape codes cause log injection. Additionally, short trace IDs cause panics in handlers that slice them (e.g., `handlers_system.go:99` does `TraceID[:8]`).

---

## Deliverable

1. Validate trace ID format in the middleware (alphanumeric + hyphens, minimum 8 chars, maximum 64 chars)
2. Reject or replace invalid values with a generated fallback
3. Guard all `TraceID` slicing with length checks

---

## Acceptance Criteria

- Invalid trace IDs are rejected or sanitized
- Short trace IDs do not cause panics
- Log injection via trace ID is prevented
- Existing tests pass
