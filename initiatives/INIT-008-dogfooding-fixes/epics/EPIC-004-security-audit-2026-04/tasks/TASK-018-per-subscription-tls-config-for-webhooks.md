---
id: TASK-018
type: Task
title: "Per-subscription TLS config for webhook deliveries"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: enhancement
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-018 — Per-Subscription TLS Config For Webhook Deliveries

---

## Purpose

`internal/delivery/webhook_dispatcher.go:64` and `internal/gateway/handlers_subscriptions.go:343` construct an `http.Client` with default TLS, no CA pinning, and no custom CA bundle support. For webhooks delivered to internal targets with private CAs or to high-value external targets, a compromised transit path (malicious CA, DNS spoofing) could intercept or forge events.

---

## Deliverable

- Add optional per-subscription fields for: pinned SPKI fingerprint and/or custom CA PEM.
- When set, construct a dedicated `http.Client` with the pin/CA for that subscription.
- Document the threat model and trade-offs (operational burden vs tamper resistance).

---

## Acceptance Criteria

- Subscription with a mismatched pin fails TLS handshake.
- Subscription with correct custom CA validates successfully.
- Default (no pin, no CA) behavior unchanged.
