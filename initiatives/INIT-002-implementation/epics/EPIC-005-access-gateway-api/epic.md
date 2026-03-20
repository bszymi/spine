---
id: EPIC-005
type: Epic
title: Access Gateway and API
status: Pending
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# EPIC-005 — Access Gateway and API

---

## Purpose

Build the Access Gateway (HTTP server + authentication + authorization) and CLI. After this epic, Spine is accessible externally through API and command line.

---

## Validates

- [Access Surface](/architecture/access-surface.md) — Operations and access modes
- [API Operations](/architecture/api-operations.md) — Operation semantics
- [OpenAPI Specification](/api/spec.yaml) — Concrete endpoints
- [Security Model](/architecture/security-model.md) — Authentication, authorization, roles

---

## Acceptance Criteria

- HTTP API serves all operations from api-spec.yaml
- Bearer token authentication works
- Role-based authorization enforces correct permissions per operation
- CLI commands map to API operations
- Trace ID propagation (X-Trace-Id header) works end-to-end
- Idempotency-Key header prevents duplicate writes
- Error responses match the error model from API Operations §4
- End-to-end tests: CLI → API → Artifact Service → Git
