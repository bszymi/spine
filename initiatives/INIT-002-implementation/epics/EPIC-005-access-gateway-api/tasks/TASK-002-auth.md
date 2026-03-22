---
id: TASK-002
type: Task
title: Authentication and Authorization
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
---

# TASK-002 — Authentication and Authorization

## Purpose

Implement bearer token authentication and role-based authorization per Security Model §3-4.

## Deliverable

- Bearer token validation middleware
- Actor identity resolution from token
- Role-based authorization check per operation (per API Operations §8 authorization summary)
- Token creation and revocation (admin operations)
- Hashed token storage
- Authorization error responses (401 unauthorized, 403 forbidden)

## Acceptance Criteria

- Requests without token return 401
- Requests with invalid token return 401
- Requests with insufficient role return 403
- Each operation enforces its minimum role from the authorization summary
- Token creation returns plaintext only once; storage is hashed
- Revoked tokens are immediately rejected
- Unit tests for token validation, integration tests for role enforcement
