# Spine Management Platform Integration Guide

This guide covers everything needed to build a management platform that integrates with Spine. It documents the credential helper protocol, workspace management API, environment variables, and deployment patterns.

---

## 1. Architecture Overview

### What Spine Owns

Spine is the **execution authority**. It owns:

- Git repository state (commits, branches, merges)
- Artifact lifecycle (create, validate, status transitions)
- Workflow execution (runs, steps, outcomes)
- Actor assignment and step delivery
- Projection sync (Git -> database cache)

### What the Management Platform Owns

The management platform owns:

- User accounts and platform-level auth
- Git hosting and credential provisioning
- Workspace lifecycle (create, provision, deactivate)
- Monitoring dashboards and alerting
- Container orchestration (Kubernetes, Docker Swarm, etc.)

### Deployment Modes

**Dedicated mode** (one Spine instance per workspace):
- Simplest setup. One container, one workspace, one database.
- Workspace identity set via `SMP_WORKSPACE_ID` env var at container creation.
- Credentials configured via `SPINE_GIT_CREDENTIAL_HELPER` (recommended) or `SPINE_GIT_PUSH_TOKEN` (fallback; scrubbed from process env at startup).

**Shared mode** (multiple workspaces per Spine instance):
- Single Spine instance serves multiple workspaces via a registry database.
- Workspace identity passed per-request via `X-Workspace-ID` header.
- `smp_workspace_id` set per workspace via `POST /workspaces`.
- Credentials resolved per-push from workspace config.

---

## 2. Git Push Credential Integration

### Credential Resolution Chain

Spine resolves push credentials in priority order:

1. **External credential helper** (`SPINE_GIT_CREDENTIAL_HELPER`) -- Git calls an external program (one of: `cache`, `store`, `osxkeychain`, `manager`, `pass`) to retrieve credentials. **Recommended for production.** The token is never resident in Spine's process environment.
2. **Built-in token** (`SPINE_GIT_PUSH_TOKEN`) -- Read once at startup, copied into an in-memory GIT\_ASKPASS helper, and then scrubbed from `os.Environ()` so the token is no longer visible to child processes, `/proc/<pid>/environ`, or core dumps. A step up from env-resident tokens but still inferior to a credential helper. When a credential helper is also configured, the token is ignored with a warning at startup.
3. **Git native** -- Whatever the user configured in their git config (SSH keys, credential store, etc.). Spine does nothing extra.
4. **None** -- Push skipped gracefully. Run completes without pushing.

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `SPINE_GIT_CREDENTIAL_HELPER` | Credential helper name (`cache`, `store`, `osxkeychain`, `manager`, `pass`). Recommended mode. | not set |
| `SMP_WORKSPACE_ID` | Workspace identifier for credential lookup (set by platform) | not set |
| `SPINE_GIT_PUSH_TOKEN` | Static PAT/deploy token for HTTPS push (standalone mode). Scrubbed from process env at startup; ignored when `SPINE_GIT_CREDENTIAL_HELPER` is also set. | not set |
| `SPINE_GIT_PUSH_USERNAME` | Username for token auth | `x-access-token` |
| `SPINE_GIT_PUSH_ENABLED` | Set to `false` to skip push entirely | `true` |

### Git Credential Helper Protocol

Spine configures the selected helper per-repo via `git config --local credential.helper <name>`. Only the short names in the allowlist are accepted: `cache`, `store`, `osxkeychain`, `manager`, `pass`. Free-form paths to custom helper scripts are refused at startup — git treats `credential.helper` values as "run this program," so allowing arbitrary strings is a remote-code-execution surface.

**Input** (from Git, via stdin):
```
protocol=https
host=github.com
```

**Output** (from the selected helper, via stdout):
```
protocol=https
host=github.com
username=x-access-token
password=<token>
```

### Populating the Helper's Backing Store

Since Spine no longer invokes a custom helper script, platform-side credential provisioning becomes a sidecar concern: populate the backing store the chosen helper reads from. Common patterns:

- **`store`** — write `~/.git-credentials` (one `https://user:token@host` line per entry) from a sidecar that fetches tokens from your platform API on workspace activation.
- **`cache`** — prime the in-memory cache by shell-invoking `git credential approve` on startup.
- **`osxkeychain` / `manager` / `pass`** — use the native tooling for the respective secret store.

### Security Requirements

- Never log tokens or credentials. Spine redacts URLs containing credentials in all log output.
- Never write tokens to disk. Remote URL rewriting is per-push, in-memory only.
- `SPINE_GIT_CREDENTIAL_HELPER` only accepts the allowlisted short names (`cache`, `store`, `osxkeychain`, `manager`, `pass`). Arbitrary paths or shell strings are refused at startup — git treats `credential.helper` values as "run this program," so the allowlist is the RCE gate.
- `SPINE_GIT_PUSH_TOKEN` is read once at startup, copied into an in-memory `GIT_ASKPASS` helper, and scrubbed from `os.Environ()`. It is not visible to child processes or `/proc/<pid>/environ`.

---

## 2.1 Secrets at Rest {#secrets-at-rest}

Spine encrypts sensitive fields before persisting them. Currently this applies to `event_subscriptions.signing_secret` (the HMAC key used to sign outbound webhooks); a DB compromise alone is not enough to forge webhook payloads.

### Key Management

- **Key source**: `SPINE_SECRET_ENCRYPTION_KEY` — base64-encoded 32 bytes (AES-256), supplied out-of-band from the database. Generate with `openssl rand -base64 32`.
- **Production gate**: Starting with `SPINE_ENV=production` and no key is refused at startup. Other environments log a warning and fall back to plaintext so local development does not require extra configuration.
- **Wire format**: Ciphertext rows carry an `enc:v1:` prefix followed by `base64(nonce || aes-gcm-ciphertext)`. Plaintext rows written before the key was deployed are returned untouched on read and transparently re-encrypted on their next update — no data migration is required.
- **Key rotation**: Losing the key means existing ciphertext cannot be decrypted. Back it up alongside other production secrets (e.g., your cloud KMS or 1Password/secrets-manager bag). Rotation is currently a manual procedure: decrypt with the old key, re-save subscriptions so they are re-encrypted under the new key.

### Webhook TLS Configuration

Per-subscription TLS config lives in the `metadata` JSONB column of `event_subscriptions` under the `tls` key. Set it when creating/updating a subscription to:

- **Pin the server's SPKI hash**: `{"tls":{"spki_sha256":"<base64>"}}` — the webhook dispatcher rejects any certificate whose Subject Public Key Info SHA-256 does not match.
- **Supply a custom CA bundle** (for internal CAs / private networks): `{"tls":{"root_cas_pem":"-----BEGIN CERTIFICATE-----..."}}`.

Both fields are optional; when neither is set, the dispatcher uses the system trust store. See `internal/delivery/tls.go` for the full validator.

---

## 3. Workspace Management API

All workspace endpoints require the operator token (`SPINE_OPERATOR_TOKEN`) as a Bearer token in the `Authorization` header.

Base path: `/api/v1`

### Create Workspace

```
POST /workspaces
Authorization: Bearer <operator-token>
Content-Type: application/json

{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "git_url": "https://github.com/acme/spine-repo.git",
  "smp_workspace_id": "ws-acme-prod-001"
}
```

Response (201):
```json
{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "status": "inactive",
  "message": "workspace created \u2014 run provisioning to activate"
}
```

The workspace is created as **inactive**. It must be provisioned (database created, repo cloned) before it can serve traffic.

### List Workspaces

```
GET /workspaces
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspaces": [
    {
      "workspace_id": "acme-prod",
      "display_name": "Acme Production",
      "status": "active"
    }
  ]
}
```

### Get Workspace

```
GET /workspaces/{workspace_id}
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "status": "active",
  "repo_path": "/var/spine/repos/acme-prod",
  "database_host": "spine-db:5432/spine_ws_acme_prod"
}
```

### Deactivate Workspace

```
POST /workspaces/{workspace_id}/deactivate
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspace_id": "acme-prod",
  "status": "inactive"
}
```

Deactivation immediately stops serving requests for this workspace and invalidates caches.

---

## 4. Actor and Auth Integration

### Token-Based Authentication

All API requests (except health/metrics and workspace management) require a bearer token.

**Create a token:**
```
POST /api/v1/tokens
Authorization: Bearer <existing-token-with-token.create>
Content-Type: application/json

{
  "actor_id": "actor-001",
  "name": "CI Integration",
  "expires_in": "720h"
}
```

Response:
```json
{
  "token_id": "tok-abc123",
  "token": "spine_<plaintext-token>",
  "expires_at": "2026-05-10T20:00:00Z"
}
```

The plaintext token is shown **once** at creation. Store it securely.

**Use the token:**
```
GET /api/v1/artifacts
Authorization: Bearer spine_<plaintext-token>
```

**Workspace-scoped requests** (shared mode): Include `X-Workspace-ID` header:
```
GET /api/v1/artifacts
Authorization: Bearer spine_<plaintext-token>
X-Workspace-ID: acme-prod
```

### Actor Model

Actors represent entities (humans, AI agents, automated systems) that interact with Spine.

**Roles:** `reader`, `contributor`, `reviewer`, `operator`, `admin`

Each role grants specific permissions:
- `reader` -- read artifacts, query state
- `contributor` -- create/update artifacts, start runs, submit results
- `reviewer` -- all contributor permissions + accept/reject tasks
- `operator` -- all reviewer permissions + manage tokens, system operations
- `admin` -- all permissions

**Skills:** Actors can have skills assigned (e.g., "go-development", "code-review"). Workflows can require specific skills for step assignment, enabling skill-based routing of work.

---

## 5. Projection Database Access

Spine maintains a projection database that mirrors Git state into PostgreSQL tables for efficient querying. The management platform can connect to these tables read-only.

### Key Tables

**`projection.artifacts`** -- Cached artifact state
```sql
SELECT artifact_path, artifact_id, artifact_type, title, status, metadata, source_commit
FROM projection.artifacts
WHERE artifact_type = 'Task' AND status = 'Pending';
```

**`projection.workflows`** -- Parsed workflow definitions
```sql
SELECT workflow_path, workflow_id, name, version, definition
FROM projection.workflows
WHERE status = 'active';
```

**`projection.sync_state`** -- Sync cursor and status
```sql
SELECT last_synced_commit, last_synced_at, status
FROM projection.sync_state
WHERE id = 'global';
```

### Runtime Tables

**`runtime.runs`** -- Workflow execution state
```sql
SELECT run_id, task_path, status, current_step_id, started_at, completed_at
FROM runtime.runs
WHERE status = 'active';
```

**`runtime.step_executions`** -- Step-level execution state
```sql
SELECT execution_id, run_id, step_id, actor_id, status, outcome_id
FROM runtime.step_executions
WHERE run_id = 'run-abc123';
```

### Connection Configuration

The database URL is the same as `SPINE_DATABASE_URL`. For read-only access, create a separate PostgreSQL role with SELECT-only permissions.

---

## 6. Event Integration

Spine does not currently expose a real-time event stream. Use polling-based state sync.

### Change Detection

Poll `projection.sync_state` to detect when the projection has been updated:

```sql
SELECT last_synced_commit, last_synced_at
FROM projection.sync_state
WHERE id = 'global';
```

Compare `last_synced_commit` with your last-seen value. If changed, re-query the tables you care about.

### Recommended Polling

- **Normal operation:** Poll every 5-10 seconds
- **Backoff on no changes:** Increase interval to 30 seconds after 10 consecutive polls with no changes
- **Reset on change:** Return to 5-second interval when a change is detected

---

## 7. Deployment Patterns

### Docker Compose -- Dedicated Mode

```yaml
services:
  spine:
    build: .
    # Bind loopback-only; front with a reverse proxy (see
    # SPINE_TRUSTED_PROXY_CIDRS) if the service must be public.
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      - SPINE_ENV=production
      - SPINE_DATABASE_URL=postgres://spine:spine@spine-db:5432/spine?sslmode=require
      - SPINE_REPO_PATH=/repo
      - SPINE_SERVER_PORT=8080
      - SPINE_LOG_LEVEL=info
      - SPINE_OPERATOR_TOKEN=${SPINE_OPERATOR_TOKEN}      # 32+ chars
      - SPINE_SECRET_ENCRYPTION_KEY=${SECRET_ENC_KEY}     # openssl rand -base64 32
      # Recommended push-auth mode: short-name credential helper.
      # Falls back to SPINE_GIT_PUSH_TOKEN only if no helper is configured.
      - SPINE_GIT_CREDENTIAL_HELPER=store
      - SPINE_GIT_PUSH_ENABLED=true
    volumes:
      - repo:/repo
    depends_on:
      spine-db:
        condition: service_healthy

  spine-db:
    image: postgres:18-bookworm
    environment:
      - POSTGRES_USER=spine
      - POSTGRES_PASSWORD=spine
      - POSTGRES_DB=spine
    volumes:
      - pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spine"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
  repo:
```

### Kubernetes -- Shared Mode with Credential Helper

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spine
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: spine
          image: spine:latest
          ports:
            - containerPort: 8080
          env:
            - name: SPINE_ENV
              value: "production"
            - name: SPINE_WORKSPACE_MODE
              value: "shared"
            - name: SPINE_REGISTRY_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: registry-db-url  # must use sslmode=require or stronger
            - name: SPINE_OPERATOR_TOKEN
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: operator-token   # 32+ chars
            - name: SPINE_SECRET_ENCRYPTION_KEY
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: secret-encryption-key  # base64 32 bytes
            - name: SPINE_GIT_CREDENTIAL_HELPER
              value: "store"            # allowlisted helper name; no path/script needed
            - name: SPINE_SERVER_PORT
              value: "8080"
            - name: SPINE_LOG_LEVEL
              value: "info"
          volumeMounts:
            - name: repos
              mountPath: /var/spine/repos
      volumes:
        - name: repos
          persistentVolumeClaim:
            claimName: spine-repos
```

> The helper allowlist (TASK-022) replaced the previous free-form path model. If your platform needs to talk to a remote secret store, run a sidecar that populates the appropriate backing store for `cache`/`store`/`osxkeychain`/`manager`/`pass` — Spine will call that helper by name only.

### Environment Variable Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SPINE_DATABASE_URL` | Yes (single mode) | - | PostgreSQL connection string for workspace database. Must use `sslmode=require` or stronger in production; `sslmode=disable` is refused at startup unless `SPINE_INSECURE_LOCAL=1`. |
| `SPINE_WORKSPACE_MODE` | No | `single` | `single` or `shared` |
| `SPINE_WORKSPACE_ID` | No | `default` | Workspace ID in single mode |
| `SPINE_REGISTRY_DATABASE_URL` | Yes (shared mode) | - | PostgreSQL URL for workspace registry. Same TLS requirements as `SPINE_DATABASE_URL`. |
| `SPINE_REPO_PATH` | No | `.` | Path to Git repository |
| `SPINE_SERVER_PORT` | No | `8080` | HTTP server port |
| `SPINE_LOG_LEVEL` | No | `info` | Log level: debug, info, warn, error |
| `SPINE_LOG_FORMAT` | No | `json` | Log format: json or text |
| `SPINE_ENV` | No | - | Runtime environment: `production`, `staging`, `development`. When `production`, `SPINE_DEV_MODE` is refused and `SPINE_SECRET_ENCRYPTION_KEY` is required. |
| `SPINE_DEV_MODE` | No | - | `1` or `true` lets unauthenticated requests through the auth gate. Dev-only; refused at startup when `SPINE_ENV=production`. |
| `SPINE_SECRET_ENCRYPTION_KEY` | Yes (production) | - | Base64-encoded 32-byte AES-256 key for at-rest secret encryption (webhook signing secrets). Generate with `openssl rand -base64 32`. See [Secrets at Rest](#secrets-at-rest). |
| `SPINE_OPERATOR_TOKEN` | No | - | Static bearer token for system-level endpoints (workspace CRUD). Minimum 32 characters; shorter values are refused at startup. Unset: operator endpoints return 503. |
| `SPINE_INSECURE_LOCAL` | No | - | Set to `1` only for local development with `sslmode=disable` DB URLs. |
| `SPINE_TRUSTED_PROXY_CIDRS` | No | - | Comma-separated CIDRs of reverse proxies Spine should trust for `X-Forwarded-For`. Leave unset if directly internet-facing. |
| `SPINE_GIT_HTTP_TRUSTED_CIDRS` | No | - | Comma-separated CIDRs allowed to clone/fetch over the internal git-HTTP endpoint without a bearer token (e.g. runner container network). Unset = require tokens from every caller. |
| `SPINE_GIT_CREDENTIAL_HELPER` | No | - | Credential helper name, one of: `cache`, `store`, `osxkeychain`, `manager`, `pass`. Recommended production mode. Non-allowlisted values are refused at startup. |
| `SPINE_GIT_PUSH_TOKEN` | No | - | Static PAT/deploy token for HTTPS push. Scrubbed from process env after startup; ignored when `SPINE_GIT_CREDENTIAL_HELPER` is also set. |
| `SPINE_GIT_PUSH_USERNAME` | No | `x-access-token` | Username for token-based push auth |
| `SPINE_GIT_PUSH_ENABLED` | No | `true` | Set to `false` to disable git push |
| `SMP_WORKSPACE_ID` | No | - | Management platform workspace ID (dedicated mode) |
| `SPINE_ORPHAN_THRESHOLD` | No | - | Duration before recovering orphaned steps |
| `SPINE_MIGRATIONS_DIR` | No | `migrations` | Path to schema migrations |
| `SPINE_SSE_MAX_CONN_PER_ACTOR` | No | - | Per-actor cap on concurrent SSE stream connections |
