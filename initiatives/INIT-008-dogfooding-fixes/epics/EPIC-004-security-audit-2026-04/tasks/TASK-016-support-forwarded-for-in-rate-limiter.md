---
id: TASK-016
type: Task
title: "Honor X-Forwarded-For in rate limiter behind a trusted proxy"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-016 — Honor X-Forwarded-For In Rate Limiter Behind A Trusted Proxy

---

## Purpose

`internal/gateway/ratelimit.go:70-92` keys per-IP buckets on `r.RemoteAddr`. Behind a reverse proxy, every request appears from the proxy IP and shares a single bucket — rate limiting effectively becomes a global cap. Conversely, naively trusting `X-Forwarded-For` lets a client fake any source IP.

---

## Deliverable

- Add `SPINE_TRUSTED_PROXY_CIDRS` env config.
- When `RemoteAddr` is inside a trusted CIDR, honor the right-most `X-Forwarded-For` entry that is not itself trusted.
- Default behavior (no trusted CIDRs) keeps current `RemoteAddr` logic.
- Document behavior and risk.

---

## Acceptance Criteria

- With trusted CIDR configured, rate-limit buckets are per-client IP.
- Without configuration, behavior is unchanged.
- Unit tests cover trusted and untrusted proxy scenarios.
