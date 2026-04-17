---
id: EPIC-004
type: Epic
title: Security Audit 2026-04
status: Completed
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
owner: bszymi
created: 2026-04-16
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/initiative.md
---

# EPIC-004 — Security Audit 2026-04

---

## 1. Purpose

Address findings from a full-repo security audit conducted 2026-04-16 covering authentication, HTTP API, data layer, exec/filesystem, secrets/crypto, and container/supply chain. Broader in scope than EPIC-002 — covers areas previously marked out of scope (CORS, rate limiting, container hardening, SSE DoS).

---

## 2. Scope

### In Scope

- Supply chain: remove committed binaries, externalize credentials, enable SSL on DB connections
- CORS policy and cross-origin request hygiene
- DoS hardening: SSE connection caps, YAML decoder bounds, body-size enforcement on nested fields
- Secret handling: webhook signing secret hashing, git push token lifecycle
- Auth hardening: operator token length, workspace ↔ actor scope check, dev-mode prod guard
- Container/compose hygiene: resource limits, loopback port binding, gosec linter
- Misc polish: error message sanitization, refname validation, allowlists

### Out of Scope

- Multi-tenant re-architecture
- Workspace registry or runtime rewrites
- Rate-limiter rewrite beyond honoring X-Forwarded-For

---

## 3. Success Criteria

1. All Critical and High findings closed or explicitly accepted with a written rationale.
2. `gosec` passes cleanly in CI.
3. Dev-mode auth cannot be enabled in a production-marked environment.
4. No plaintext secrets in version-controlled compose files.
