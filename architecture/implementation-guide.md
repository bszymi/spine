---
type: Architecture
title: Implementation Guide
status: Living Document
version: "0.1"
---

# Implementation Guide

---

## 1. Purpose

This document defines the concrete implementation structure for Spine v0.x — how the technology choices from [ADR-005](/architecture/adr/ADR-005-technology-selection.md) map to a buildable system.

The architecture documents define *what* the system does. This document defines *how* it is organized as code — package layout, module boundaries, internal interfaces, build tooling, testing strategy, and development workflow.

---

## 2. Go Module Structure

### 2.1 Repository Layout

```
spine/
├── cmd/
│   ├── spine/                  # Main binary entry point
│   │   └── main.go            # Server + CLI dispatch
│   └── spine-cli/             # Optional: standalone CLI (if separated later)
│
├── internal/                   # Private application code (not importable externally)
│   ├── artifact/              # Artifact Service
│   ├── workflow/              # Workflow Engine
│   ├── projection/            # Projection Service
│   ├── actor/                 # Actor Gateway
│   ├── event/                 # Event Router
│   ├── validation/            # Validation Service
│   ├── gateway/               # Access Gateway (HTTP handlers, auth)
│   ├── git/                   # Git client abstraction + CLI implementation
│   ├── queue/                 # Queue abstraction + in-process implementation
│   ├── store/                 # Database access (PostgreSQL via pgx)
│   └── domain/                # Shared domain types (Run, Step, Artifact, etc.)
│
├── api/
│   └── spec.yaml              # OpenAPI specification
│
├── architecture/              # Architecture documents (this file lives here)
├── governance/                # Governance documents
├── product/                   # Product documents
├── initiatives/               # Initiative/epic/task artifacts
├── workflows/                 # Workflow definition YAML files
├── migrations/                # Database migration SQL files
├── templates/                 # Artifact templates
│
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

### 2.2 Package Mapping to Components

Each architecture component maps to one or more Go packages:

| Component | Package | Responsibility |
|-----------|---------|---------------|
| Access Gateway | `internal/gateway` | HTTP routing, authentication, authorization, request normalization |
| Artifact Service | `internal/artifact` | Git read/write, schema validation, commit creation |
| Workflow Engine | `internal/workflow` | Run management, step execution, state machine, divergence/convergence |
| Projection Service | `internal/projection` | Git → DB sync, incremental updates, full rebuild |
| Actor Gateway | `internal/actor` | Step delivery, result collection, AI provider integration |
| Event Router | `internal/event` | Event emission, routing, consumer dispatch |
| Validation Service | `internal/validation` | Cross-artifact validation, rule evaluation |

### 2.3 Shared Packages

| Package | Purpose |
|---------|---------|
| `internal/domain` | Core domain types shared across packages (Run, StepExecution, Artifact, Event, etc.) |
| `internal/git` | Git client interface and CLI implementation |
| `internal/queue` | Queue interface and in-process implementation |
| `internal/store` | Database connection, transaction management, query helpers |

### 2.4 Package Dependency Rules

- Packages in `internal/` may import `internal/domain` and `internal/store`
- Component packages should not import each other directly — they communicate through interfaces or the event system
- `internal/gateway` is the only package that imports component packages to wire them together
- No package imports `cmd/` — dependency flows inward

---

## 3. Internal Interface Contracts

### 3.1 GitClient Interface

```go
type GitClient interface {
    Clone(ctx context.Context, url, path string) error
    Commit(ctx context.Context, opts CommitOpts) (CommitResult, error)
    Merge(ctx context.Context, opts MergeOpts) (MergeResult, error)
    CreateBranch(ctx context.Context, name, base string) error
    DeleteBranch(ctx context.Context, name string) error
    Diff(ctx context.Context, from, to string) ([]FileDiff, error)
    Log(ctx context.Context, opts LogOpts) ([]CommitInfo, error)
    ReadFile(ctx context.Context, ref, path string) ([]byte, error)
    ListFiles(ctx context.Context, ref, pattern string) ([]string, error)
    Head(ctx context.Context) (string, error)
}
```

The v0.x implementation (`internal/git/cli.go`) shells out to the `git` CLI. The interface allows future replacement with libgit2 or platform API.

### 3.2 Queue Interface

```go
type Queue interface {
    Publish(ctx context.Context, entry QueueEntry) error
    Subscribe(ctx context.Context, entryType string, handler EntryHandler) error
    Acknowledge(ctx context.Context, entryID string) error
}

type EntryHandler func(ctx context.Context, entry QueueEntry) error
```

The v0.x implementation (`internal/queue/memory.go`) uses Go channels. The interface allows future extraction to Redis, NATS, or RabbitMQ.

### 3.3 EventRouter Interface

```go
type EventRouter interface {
    Emit(ctx context.Context, event Event) error
    Subscribe(ctx context.Context, eventType string, handler EventHandler) error
}

type EventHandler func(ctx context.Context, event Event) error
```

In v0.x, the EventRouter is backed by the in-process Queue. It wraps events as queue entries for delivery.

### 3.4 Store Interface

```go
type Store interface {
    // Transactions
    WithTx(ctx context.Context, fn func(tx Tx) error) error

    // Runs
    CreateRun(ctx context.Context, run *Run) error
    GetRun(ctx context.Context, runID string) (*Run, error)
    UpdateRunStatus(ctx context.Context, runID, status string) error

    // Step Executions
    CreateStepExecution(ctx context.Context, exec *StepExecution) error
    UpdateStepExecution(ctx context.Context, exec *StepExecution) error

    // Projections
    UpsertArtifactProjection(ctx context.Context, proj *ArtifactProjection) error
    QueryArtifacts(ctx context.Context, query ArtifactQuery) ([]ArtifactProjection, error)

    // ... additional methods per table
}
```

Implemented with `pgx` against PostgreSQL. All methods accept `context.Context` for cancellation and timeout propagation.

---

## 4. Build and Distribution

### 4.1 Single Binary

The primary build artifact is a single Go binary that includes both server and CLI modes:

```
spine serve        # Start the runtime server
spine cli <cmd>    # Execute CLI commands
spine migrate      # Run database migrations
spine rebuild      # Trigger projection rebuild
```

### 4.2 Build Tooling

```makefile
# Makefile targets
build:          # go build -o bin/spine ./cmd/spine
test:           # go test ./...
test-integration: # Integration tests (requires PostgreSQL + Git)
lint:           # golangci-lint run
migrate:        # Apply database migrations
spec-validate:  # Validate OpenAPI spec
```

### 4.3 Distribution

- **Binary releases** via GoReleaser (cross-compiled for Linux, macOS, Windows)
- **Container image** — minimal Docker image with the binary + Git CLI
- **No runtime dependencies** beyond Git CLI and PostgreSQL connection

### 4.4 Container Image

```dockerfile
FROM golang:1.22 AS builder
WORKDIR /app
COPY . .
RUN go build -o spine ./cmd/spine

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*
COPY --from=builder /app/spine /usr/local/bin/spine
ENTRYPOINT ["spine"]
```

---

## 5. Dependency Policy

### 5.1 Minimal External Dependencies

Spine minimizes external Go dependencies to reduce supply chain risk and maintenance burden.

**Approved dependencies (v0.x):**

| Dependency | Purpose | Rationale |
|------------|---------|-----------|
| `pgx` | PostgreSQL driver | Standard, high-performance Go Postgres driver |
| `chi` or `gorilla/mux` | HTTP router | Lightweight routing without full framework |
| `cobra` | CLI framework | Standard Go CLI library |
| `slog` (stdlib) | Structured logging | Standard library, no external dependency |

### 5.2 Dependency Approval

New dependencies require explicit justification:

- What problem does it solve?
- Can it be solved with the standard library?
- What is the maintenance status and security posture?
- Does it introduce transitive dependencies?

Dependencies are reviewed before addition and tracked in `go.mod`.

---

## 6. Testing Strategy

### 6.1 Test Levels

| Level | Scope | Infrastructure | Location |
|-------|-------|---------------|----------|
| Unit | Single function/method | None | `*_test.go` alongside source |
| Integration | Package + database | PostgreSQL (test instance) | `*_integration_test.go` |
| Git integration | Artifact Service + Git | Temp Git repos | `internal/git/*_test.go` |
| End-to-end | Full request flow | PostgreSQL + Git | `test/e2e/` |

### 6.2 Git Test Fixtures

Tests that interact with Git use temporary repositories:

```go
func TestArtifactCreate(t *testing.T) {
    repo := testutil.NewTempRepo(t)  // Creates temp Git repo, cleaned up on test end
    client := git.NewCLIClient(repo.Path)
    // ... test artifact operations
}
```

### 6.3 Database Test Strategy

Integration tests use a dedicated test database:

- Migrations are applied before the test suite
- Each test runs in a transaction that is rolled back after the test
- No shared state between tests

### 6.4 State Machine Tests

The workflow engine state machine (per [Engine State Machine](/architecture/engine-state-machine.md)) is tested by:

- Verifying every valid transition produces the expected state and effects
- Verifying every invalid transition is rejected
- Testing recovery from every persisted state
- Testing concurrent transitions (divergence branches)

---

## 7. Configuration

### 7.1 Configuration Sources

| Source | Priority | Use |
|--------|----------|-----|
| Environment variables | Highest | Runtime secrets, deployment-specific config |
| Config file (YAML) | Medium | Server settings, feature flags |
| Defaults | Lowest | Sensible defaults for all settings |

### 7.2 Core Configuration

```yaml
# spine.yaml
server:
  port: 8080
  host: "0.0.0.0"

database:
  url: "postgres://user:pass@localhost:5432/spine"
  max_connections: 20

git:
  repository_path: "/path/to/repo"
  authoritative_branch: "main"
  author_email_domain: "spine.local"

projection:
  polling_interval: "30s"
  webhook_enabled: true

queue:
  type: "memory"  # memory (v0.x) or external broker later

logging:
  level: "info"
  format: "json"
```

### 7.3 Secrets

Secrets (database password, API tokens, Git credentials) are provided via environment variables, never in config files or Git (per [Security Model](/architecture/security-model.md) §5).

---

## 8. Development Workflow

### 8.1 Local Development

```bash
# Setup
git clone <repo>
make setup          # Install tools, create test DB

# Development cycle
make build          # Build binary
make test           # Run unit tests
make test-integration # Run integration tests (requires DB)
make lint           # Run linter

# Run locally
./bin/spine migrate  # Apply migrations
./bin/spine serve    # Start server
```

### 8.2 Code Organization Conventions

- Each component package has a `service.go` with the main service struct and constructor
- Interfaces are defined in the package that uses them, not the package that implements them
- Domain types live in `internal/domain` and are imported by all packages
- Database queries are co-located with the store methods that use them
- Error types are defined in the package where they originate

---

## 9. Cross-References

- [ADR-005](/architecture/adr/ADR-005-technology-selection.md) — Technology selection decision
- [System Components](/architecture/components.md) — Component responsibilities
- [Runtime Schema](/architecture/runtime-schema.md) — Database table definitions
- [Git Integration](/architecture/git-integration.md) — Git operational contract
- [API Operations](/architecture/api-operations.md) — API design
- [OpenAPI Specification](/api/spec.yaml) — Machine-readable API contract
- [Engine State Machine](/architecture/engine-state-machine.md) — State machine to implement
- [Security Model](/architecture/security-model.md) §5 — Secret management

---

## 10. Evolution Policy

This implementation guide evolves with the codebase. As patterns emerge during development:

- Package structure may be refined (packages split or merged)
- New interfaces may be introduced for testability
- Build tooling may expand (CI/CD pipeline, release automation)
- Configuration schema will grow as features are added

Changes that alter the package dependency rules or internal interface contracts should be discussed before implementation.
