---
type: Architecture
title: Docker and Local Runtime Environment
status: Living Document
version: "0.1"
---

# Docker and Local Runtime Environment

---

## 1. Purpose

This document defines how Spine is packaged and run using Docker and Docker Compose for local development, integration testing, and simple deployment environments.

The [Implementation Guide](/architecture/implementation-guide.md) defines the build process and binary distribution. This document extends that into containerized runtime — how the Spine binary, its dependencies, and the developer workflow come together in a consistent, reproducible environment.

This document covers **development and testing environments only**. Production deployment design is deferred.

---

## 2. Runtime Dependencies

Spine requires three runtime components:

| Component | Purpose | Container |
|-----------|---------|-----------|
| Spine application | Core runtime (API, Workflow Engine, Projection Service, etc.) | `spine` |
| PostgreSQL | Projection Store + Runtime Store | `spine-db` |
| Git repository | Authoritative artifact store | Mounted volume or initialized container |

---

## 3. Spine Application Container

### 3.1 Dockerfile

The Spine Dockerfile uses a multi-stage build to produce a minimal runtime image:

```dockerfile
# ── Build stage ──
FROM golang:1.22-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o spine ./cmd/spine

# ── Runtime stage ──
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /bin/bash spine
USER spine

COPY --from=builder /app/spine /usr/local/bin/spine
COPY --from=builder /app/migrations /app/migrations

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD ["spine", "health"]

ENTRYPOINT ["spine"]
CMD ["serve"]
```

### 3.2 Image Design Decisions

- **Debian slim** base (not Alpine) — Git CLI works more reliably on glibc-based images
- **Non-root user** — the `spine` user owns the process for security
- **Git + ca-certificates** — Git CLI for repository operations; ca-certificates for HTTPS Git remotes
- **Migrations bundled** — migration files are included so `spine migrate` works inside the container
- **Health check built-in** — Docker health check uses `spine health`, which performs an internal check (database connectivity + Git access) without depending on the HTTP server being ready. This avoids circular dependency during startup.

---

## 4. Git Repository Model (Containerized)

### 4.1 Repository Type

Spine operates against a **non-bare working repository** (not a bare repo). This is required because:

- The Artifact Service reads file content directly from the working tree
- Worktrees are created from the working repository for task/divergence branches
- Git CLI operations (commit, merge) require a working tree

### 4.2 Repository Structure

```
/repo/                          # SPINE_GIT_REPOSITORY_PATH
├── .git/                       # Git metadata
├── governance/                 # Governed artifacts
├── architecture/
├── product/
├── workflows/
├── initiatives/
└── ...

/var/spine/worktrees/           # SPINE_GIT_WORKTREE_PATH
├── run-abc123/                 # Worktree for active Run
│   └── implement-auth/        # Task branch checkout
└── run-def456/
    └── explore-designs/
        ├── branch-a/          # Divergence branch checkout
        └── branch-b/
```

### 4.3 Repository Initialization

The `spine init-repo` command:

1. Initializes a Git repository at `SPINE_GIT_REPOSITORY_PATH` (if not already initialized)
2. Creates the authoritative branch (`main`)
3. Sets Git config for the Spine system user (`Spine System <system@spine.local>`)
4. Creates an initial empty commit

If the repository already exists (e.g., cloned from a remote), `init-repo` is skipped.

### 4.4 Repository Ownership

- The `spine` user (non-root) must have read/write access to both `/repo` and `/var/spine/worktrees`
- Worktrees are created and deleted by the Artifact Service during Run lifecycle
- Worktrees are ephemeral — if lost, they can be recreated from the repository

---

## 5. Docker Compose

### 5.1 Development Compose File

```yaml
# docker-compose.yaml
version: "3.8"

services:
  spine:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      - SPINE_DATABASE_URL=postgres://spine:spine@spine-db:5432/spine?sslmode=disable
      - SPINE_GIT_REPOSITORY_PATH=/repo
      - SPINE_GIT_AUTHORITATIVE_BRANCH=main
      - SPINE_GIT_WORKTREE_PATH=/var/spine/worktrees
      - SPINE_SERVER_PORT=8080
      - SPINE_PROJECTION_POLLING_INTERVAL=5s
      - SPINE_LOG_LEVEL=debug
    volumes:
      - repo:/repo
      - worktrees:/var/spine/worktrees
    depends_on:
      spine-db:
        condition: service_healthy
    command: ["serve"]
    healthcheck:
      test: ["CMD", "spine", "health"]
      interval: 10s
      timeout: 3s
      start_period: 15s
      retries: 3

  spine-db:
    image: postgres:16-bookworm
    ports:
      - "5432:5432"
    environment:
      - POSTGRES_USER=spine
      - POSTGRES_PASSWORD=spine
      - POSTGRES_DB=spine
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spine"]
      interval: 5s
      timeout: 3s
      retries: 5

  spine-migrate:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      - SPINE_DATABASE_URL=postgres://spine:spine@spine-db:5432/spine?sslmode=disable
    depends_on:
      spine-db:
        condition: service_healthy
    command: ["migrate"]
    profiles: ["setup"]

  spine-init-repo:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      - SPINE_GIT_REPOSITORY_PATH=/repo
    volumes:
      - repo:/repo
    command: ["init-repo"]
    profiles: ["setup"]

volumes:
  pgdata:
  repo:
  worktrees:
```

### 5.2 Service Roles

| Service | Role | Lifecycle |
|---------|------|-----------|
| `spine` | Main application (API + engine + projections) | Long-running |
| `spine-db` | PostgreSQL database | Long-running |
| `spine-migrate` | Runs database migrations then exits | One-shot (setup profile) |
| `spine-init-repo` | Initializes a bare Git repository then exits | One-shot (setup profile) |

---

## 6. Environment Variable Mapping

### 6.1 Convention

Environment variables follow the pattern `SPINE_<SECTION>_<KEY>`:

| Environment Variable | Maps To (spine.yaml) | Required |
|---------------------|---------------------|----------|
| `SPINE_DATABASE_URL` | `database.url` | Yes |
| `SPINE_GIT_REPOSITORY_PATH` | `git.repository_path` | Yes |
| `SPINE_GIT_AUTHORITATIVE_BRANCH` | `git.authoritative_branch` | No (default: `main`) |
| `SPINE_GIT_WORKTREE_PATH` | `git.worktree_path` | No (default: `/var/spine/worktrees`) |
| `SPINE_GIT_MERGE_STRATEGY` | `git.merge_strategy` | No (default: `fast-forward`) |
| `SPINE_SERVER_PORT` | `server.port` | No (default: `8080`) |
| `SPINE_SERVER_HOST` | `server.host` | No (default: `0.0.0.0`) |
| `SPINE_PROJECTION_POLLING_INTERVAL` | `projection.polling_interval` | No (default: `30s`) |
| `SPINE_ORPHAN_THRESHOLD` | `scheduler.orphan_threshold` | No (default: `720h` / 30 days) |
| `SPINE_PROJECTION_WEBHOOK_ENABLED` | `projection.webhook_enabled` | No (default: `false`) |
| `SPINE_LOG_LEVEL` | `observability.log_level` | No (default: `info`) |
| `SPINE_LOG_FORMAT` | `observability.log_format` | No (default: `json`) |

### 6.2 Precedence

Environment variables override config file values (per [Implementation Guide](/architecture/implementation-guide.md) §12.1):

```
Environment variable > Config file (spine.yaml) > Default value
```

### 6.3 Secrets

Secrets are always provided via environment variables and never written to config files or container images (per [Security Model](/architecture/security-model.md) §5):

| Secret | Environment Variable |
|--------|---------------------|
| Database password | Embedded in `SPINE_DATABASE_URL` |
| Git credentials | `SPINE_GIT_TOKEN` or SSH key mounted as volume |
| AI provider API keys | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc. |

---

## 7. Startup Flow

### 7.1 First-Time Setup

```bash
# 1. Build containers
docker compose build

# 2. Start database
docker compose up -d spine-db

# 3. Run migrations
docker compose run --rm spine-migrate

# 4. Initialize Git repository (if starting fresh)
docker compose run --rm spine-init-repo

# 5. Start Spine
docker compose up -d spine
```

Or as a single command with the setup profile:

```bash
docker compose --profile setup up -d
# Wait for setup services to complete, then:
docker compose up -d spine
```

### 7.2 Application Boot Sequence (Inside Container)

When `spine serve` starts:

```
1. Load configuration (env vars + spine.yaml + defaults)
2. Verify Git repository is accessible at SPINE_GIT_REPOSITORY_PATH
3. Connect to PostgreSQL (retry with backoff if not yet ready)
4. Verify database schema (check migrations are applied)
5. Initialize internal services (per Implementation Guide §7.1)
6. Start projection sync loop
7. Start queue consumers
8. Start workflow engine scheduler
9. Start HTTP server on configured port
10. Report healthy
```

If any step fails during boot, the process exits with a non-zero code and a structured error log.

### 7.3 Subsequent Starts

```bash
docker compose up -d
```

No setup needed — database is persisted in the `pgdata` volume, Git repository in the `repo` volume.

---

## 8. Health Checks

### 8.1 Application Health

The `spine health` command (and `GET /api/v1/system/health` endpoint) checks:

| Check | What It Verifies | Failure Behavior |
|-------|-----------------|-----------------|
| Database connectivity | PostgreSQL is reachable and responding | `unhealthy` |
| Git repository access | Repository path exists and is a valid Git repo | `unhealthy` |
| Projection freshness | Projection sync lag is within acceptable threshold | `degraded` |
| Workflow engine | Engine scheduler is running | `degraded` |

### 8.2 Container Health

Docker health checks use the `spine health` CLI command:

- **Interval:** 10 seconds
- **Timeout:** 3 seconds
- **Start period:** 15 seconds (allows time for boot)
- **Retries:** 3 (container marked unhealthy after 3 consecutive failures)

### 8.3 Dependency Health

The `spine-db` container has its own health check (`pg_isready`). The `spine` service uses `depends_on: condition: service_healthy` to wait for the database before starting.

---

## 9. Volume Strategy

### 9.1 Named Volumes

| Volume | Mount Point | Purpose | Persistent |
|--------|-------------|---------|-----------|
| `pgdata` | `/var/lib/postgresql/data` | PostgreSQL data | Yes (survives restart/rebuild) |
| `repo` | `/repo` | Git repository (authoritative) | Yes |
| `worktrees` | `/var/spine/worktrees` | Git worktrees for task/divergence branches | Ephemeral (can be recreated) |

### 9.2 Development Overrides

For development, the repository volume can be replaced with a bind mount to the host filesystem:

```yaml
# docker-compose.override.yaml
services:
  spine:
    volumes:
      - ./:/repo              # Mount current directory as read-write repo
      - worktrees:/var/spine/worktrees
```

The bind mount must be **read-write** — Spine needs write access for commits, branch creation, and merges. This allows developers to edit artifacts locally and have Spine detect changes via polling, while Spine's commits are also visible on the host.

**Caution:** With a bind mount, both the developer and Spine can modify the repository simultaneously. Developers should avoid modifying files that Spine is actively committing to (e.g., artifacts involved in an active Run).

### 9.3 Git Credentials

For Git operations requiring authentication (push to remote):

```yaml
services:
  spine:
    volumes:
      - ~/.ssh/id_ed25519:/home/spine/.ssh/id_ed25519:ro
    environment:
      - GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no
```

Or using a token:

```yaml
services:
  spine:
    environment:
      - SPINE_GIT_TOKEN=ghp_xxx
```

---

## 10. Integration Testing

### 10.1 Test Compose File

```yaml
# docker-compose.test.yaml
version: "3.8"

services:
  spine-test-db:
    image: postgres:16-bookworm
    environment:
      - POSTGRES_USER=spine_test
      - POSTGRES_PASSWORD=spine_test
      - POSTGRES_DB=spine_test
    tmpfs:
      - /var/lib/postgresql/data    # Ephemeral — no persistence needed
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spine_test"]
      interval: 2s
      timeout: 2s
      retries: 10
```

### 10.2 Running Integration Tests

```bash
# Start test database
docker compose -f docker-compose.test.yaml up -d

# Run integration tests against containerized DB
SPINE_DATABASE_URL=postgres://spine_test:spine_test@localhost:5432/spine_test \
    make test-integration

# Tear down
docker compose -f docker-compose.test.yaml down
```

### 10.3 CI Environment

In CI, the same compose file provides infrastructure:

```yaml
# Example GitHub Actions step
- name: Start test infrastructure
  run: docker compose -f docker-compose.test.yaml up -d

- name: Run tests
  run: make test-integration
  env:
    SPINE_DATABASE_URL: postgres://spine_test:spine_test@localhost:5432/spine_test
```

---

## 11. Developer Workflow

### 11.1 Common Commands

| Command | What It Does |
|---------|-------------|
| `docker compose build` | Build Spine container |
| `docker compose up -d` | Start all services |
| `docker compose down` | Stop all services |
| `docker compose logs -f spine` | Follow Spine logs |
| `docker compose run --rm spine-migrate` | Run database migrations |
| `docker compose down -v` | Stop and delete all data (full reset) |
| `docker compose exec spine spine cli <cmd>` | Run CLI commands inside the container |

### 11.2 Development Cycle

```bash
# 1. Make code changes locally
# 2. Rebuild and restart
docker compose build spine && docker compose up -d spine

# 3. Check logs
docker compose logs -f spine

# 4. Run tests
make test
make test-integration
```

### 11.3 Full Reset

To start from scratch (wipe database, repository, and all state):

```bash
docker compose down -v
docker compose --profile setup up -d
# Wait for setup services, then:
docker compose up -d spine
```

---

## 12. Async Execution

Spine v0.x uses an **in-process queue** for all asynchronous work (step assignment delivery, event routing, workflow scheduling). No external message broker (Kafka, RabbitMQ, Redis) is required.

This means:

- All async processing happens within the `spine` container
- No additional infrastructure services in Docker Compose
- Queue state is lost on container restart (by design — per [ADR-005](/architecture/adr/ADR-005-technology-selection.md), the queue is not a durable system of record)
- Recovery after restart reconstructs pending work from the Runtime Store

---

## 13. Migration Responsibility

### 13.1 Migration Rule

**Spine will not start if the database schema is outdated.** The boot sequence (§7.2) verifies that all migrations have been applied. If the schema is behind, the process exits with an error.

Migrations are **not auto-applied** on startup. They must be run explicitly:

```bash
docker compose run --rm spine-migrate
# or
spine migrate
```

This prevents accidental schema changes in shared environments and ensures migrations are a deliberate operator action.

### 13.2 Migration Source of Truth

Migration files in `/migrations/` (bundled in the container image) are the source of truth for the database schema. The `spine migrate` command applies them sequentially using version tracking in the database.

---

## 14. Logging

All logs are written to **stdout** in structured JSON format by default. This is the standard container-native logging pattern.

```json
{"timestamp":"2026-03-20T10:00:00Z","level":"info","component":"workflow_engine","message":"Run started","run_id":"run-abc123","trace_id":"..."}
```

- **stdout** is the only log destination — no log files inside the container
- Docker and container orchestrators capture stdout natively
- `SPINE_LOG_LEVEL` controls verbosity (`debug`, `info`, `warn`, `error`)
- `SPINE_LOG_FORMAT` can be set to `text` for human-readable output during local development

---

## 15. Document Boundary

This document and the [Implementation Guide](/architecture/implementation-guide.md) have complementary but distinct scopes:

| Concern | Implementation Guide | Docker Runtime |
|---------|---------------------|---------------|
| Package layout and code structure | Yes | No |
| Internal interfaces and dependency rules | Yes | No |
| Boot sequence (internal service wiring) | Yes (§7) | References it |
| Configuration schema and categories | Yes (§12) | Env var mapping to it |
| Dockerfile and container image | No | Yes |
| Docker Compose and service topology | No | Yes |
| Volume strategy and Git operational model | No | Yes |
| Developer workflow (containerized) | No | Yes |

When both documents describe the same concept (e.g., startup flow), the Implementation Guide defines the internal logic and this document defines the container-level execution.

---

## 16. Scope Boundary

### 16.1 What This Document Covers

- Local development environment
- Integration test infrastructure
- Simple single-host deployment (Docker Compose)

### 16.2 What This Document Does NOT Cover (Deferred)

- Production deployment (Kubernetes, cloud infrastructure)
- High availability and scaling
- Monitoring and alerting infrastructure
- Backup and disaster recovery
- TLS termination and network security
- Multi-node deployment

These concerns are deferred until the system is functionally complete and operational experience is gained.

---

## 17. Cross-References

- [Implementation Guide](/architecture/implementation-guide.md) §4 — Build and distribution, §12 — Configuration
- [Security Model](/architecture/security-model.md) §5 — Secret management
- [Data Model](/architecture/data-model.md) §7 — Storage technology guidance
- [Runtime Schema](/architecture/runtime-schema.md) — Database migration context
- [Git Integration](/architecture/git-integration.md) §3 — Authentication methods
- [Observability](/architecture/observability.md) §5 — Logging model

---

## 18. Evolution Policy

This document evolves as deployment patterns emerge.

Areas expected to require refinement:

- Production Dockerfile optimizations (distroless base, security scanning)
- Kubernetes deployment manifests and Helm charts
- Docker Compose profiles for different scenarios (minimal, full, debug)
- Hot-reload for development (file watching, auto-rebuild)
- Observability sidecar containers (log forwarding, metrics collection)

Changes that alter the container security model or runtime dependency requirements should be captured as ADRs.
