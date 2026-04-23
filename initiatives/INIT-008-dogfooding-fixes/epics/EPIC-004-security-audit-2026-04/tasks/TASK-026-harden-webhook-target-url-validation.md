---
id: TASK-026
type: Task
title: Harden webhook target URL validation against SSRF
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: security
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-026 — Harden webhook target URL validation against SSRF

---

## Purpose

`target_url` is accepted on subscription create/update with only a non-empty check, then used directly by the subscription test endpoint and webhook dispatcher. In multi-workspace deployments, any admin-level actor can make the Spine server initiate HTTP requests to arbitrary network locations, including loopback, link-local, private network services, or cloud metadata endpoints.

## Deliverable

- Add a shared webhook target validator used by `handleSubscriptionCreate`, `handleSubscriptionUpdate`, `handleSubscriptionTest`, and `delivery.WebhookDispatcher`.
- Default to `https://` targets only; reject userinfo, empty host, unsupported schemes, and malformed URLs.
- Reject loopback, link-local, multicast, unspecified, and private IP ranges by default after DNS resolution.
- Add an explicit operator-controlled allowlist for private webhook destinations if local/internal webhooks are required.
- Use a transport or dialer that re-validates the resolved address at connection time so DNS rebinding cannot bypass create-time validation.

## Acceptance Criteria

- Creating or updating a subscription with `http://169.254.169.254/`, `http://127.0.0.1/`, `http://localhost/`, `file://...`, or `https://user:pass@example.com/` is rejected with `invalid_params`.
- The subscription test endpoint and dispatcher refuse persisted unsafe targets even if old database rows predate this task.
- Safe public HTTPS webhook URLs still deliver successfully and preserve existing HMAC signing behavior.
- Unit tests cover URL parsing, DNS/IP rejection, allowlist behavior, and dispatcher/test-endpoint enforcement.
