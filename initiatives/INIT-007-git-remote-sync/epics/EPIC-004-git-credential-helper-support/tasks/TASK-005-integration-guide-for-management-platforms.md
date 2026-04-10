---
id: TASK-005
type: Task
title: "Write integration guide for management platform developers"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-005 — Write integration guide for management platform developers

---

## Purpose

Document everything a developer needs to know to build a management platform that integrates with Spine. This covers the credential helper protocol, workspace management API, environment variables, and deployment patterns. Without this guide, the only way to understand the integration surface is reading Spine's source code.

## Deliverable

`docs/integration-guide.md`

Content should define:

### 1. Architecture Overview

- Spine as execution authority — what it owns, what the platform owns
- Shared vs dedicated deployment modes
- Workspace lifecycle and provisioning flow

### 2. Git Push Credential Integration

- Credential resolution chain (helper → token → native → none)
- Git credential helper protocol (stdin/stdout format)
- Required environment variables:
  - `SPINE_GIT_CREDENTIAL_HELPER` — path to credential helper script
  - `SMP_WORKSPACE_ID` — workspace identifier for credential lookup
  - `SPINE_GIT_PUSH_TOKEN` — standalone token (no helper needed)
  - `SPINE_GIT_PUSH_USERNAME` — optional, defaults to `x-access-token`
  - `SPINE_GIT_PUSH_ENABLED` — set to `false` to skip push entirely
- Example credential helper script
- Security requirements: never log tokens, never write to disk

### 3. Workspace Management API

- `POST /workspaces` — create workspace (shared mode)
  - Request body: `workspace_id`, `display_name`, `smp_workspace_id`
- `GET /workspaces` — list workspaces
- `GET /workspaces/{id}` — get workspace details
- `POST /workspaces/{id}/deactivate` — deactivate workspace
- Authentication: operator token (`SPINE_OPERATOR_TOKEN`)

### 4. Actor and Auth Integration

- Token-based auth: `POST /tokens`, bearer tokens
- Actor model: creating actors, assigning skills
- Workspace-scoped auth headers (`X-Workspace-ID` in shared mode)

### 5. Projection Database Access

- Schema: `projection.artifacts`, `projection.workflows`, `projection.sync_state`
- Runtime schema: `runtime.runs`, `runtime.step_executions`
- Read-only access pattern
- Connection string configuration

### 6. Event Integration

- Polling-based state sync (no event stream yet)
- `projection.sync_state` table for change detection
- Recommended polling interval and backoff

### 7. Deployment Patterns

- Docker Compose example (dedicated mode)
- Kubernetes example (shared mode with multiple workspaces)
- Environment variable reference (complete list)

## Acceptance Criteria

- Guide covers all integration points between Spine and a management platform
- Includes working example credential helper script
- Includes environment variable reference with defaults and descriptions
- A developer can build a basic management platform using only this guide
- Reviewed for accuracy against current Spine API behavior
