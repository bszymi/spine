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
| `chi` | HTTP router | Lightweight, idiomatic, stdlib-compatible router with middleware support |
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

## 7. Runtime Composition

### 7.1 Boot Sequence

The `main.go` entry point wires all services together in a deterministic order:

```
1. Load configuration (YAML + env vars)
2. Connect to PostgreSQL (store)
3. Initialize GitClient (CLI implementation)
4. Initialize Queue (in-process)
5. Initialize EventRouter (backed by Queue)
6. Initialize Artifact Service (depends on: GitClient, Store, EventRouter)
7. Initialize Projection Service (depends on: GitClient, Store, EventRouter)
8. Initialize Validation Service (depends on: Store)
9. Initialize Actor Gateway (depends on: Store, EventRouter)
10. Initialize Workflow Engine (depends on: Artifact Service, Actor Gateway, Validation Service, Store, EventRouter, Queue)
11. Initialize Access Gateway (depends on: Artifact Service, Workflow Engine, Projection Service, Store)
12. Start Projection Service sync loop
13. Start Queue consumer loop
14. Start Workflow Engine scheduler (timeouts, orphan detection)
15. Start HTTP server
```

All dependencies are injected through constructors — no global state.

### 7.2 Service Wiring

```go
func main() {
    cfg := config.Load()
    db := store.Connect(cfg.Database)
    gitClient := git.NewCLIClient(cfg.Git)
    queue := queue.NewMemory()
    events := event.NewRouter(queue)

    artifactSvc := artifact.NewService(gitClient, db, events)
    projectionSvc := projection.NewService(gitClient, db, events)
    validationSvc := validation.NewService(db)
    actorGw := actor.NewGateway(db, events, cfg.ActorGateway)
    workflowEngine := workflow.NewEngine(artifactSvc, actorGw, validationSvc, db, events, queue)
    accessGw := gateway.NewAccessGateway(artifactSvc, workflowEngine, projectionSvc, db)

    // Start background services
    go projectionSvc.StartSyncLoop(ctx)
    go queue.StartConsumers(ctx)
    go workflowEngine.StartScheduler(ctx)

    // Start HTTP server
    accessGw.Serve(cfg.Server)
}
```

Component packages communicate through interfaces, not concrete types. The `main` function is the only place where implementations are bound to interfaces.

### 7.3 Cross-Component Communication

| Pattern | When Used | Mechanism |
|---------|-----------|-----------|
| Direct call | Synchronous, in-request | Interface method call (e.g., Workflow Engine calls Artifact Service) |
| Event | Asynchronous, decoupled | EventRouter (e.g., artifact change → Projection Service sync) |
| Queue | Async work items | Queue entry (e.g., step assignment delivery) |

Direct calls are used when the caller needs the result immediately (e.g., committing a Git change). Events and queue entries are used for follow-up work that can happen after the primary operation completes.

---

## 8. Write Transaction and Commit Flow

### 8.1 Canonical Write Sequence

Every governed write follows this sequence:

```
1. Access Gateway receives request
2. Authentication + authorization check
3. Workflow Engine validates governance (preconditions, step state, actor eligibility)
4. Artifact Service validates artifact schema + cross-artifact rules
5. Artifact Service commits to Git (atomic commit with trailers)
6. Runtime Store updated (Run/Step status) — in same DB transaction where possible
7. Event emitted (domain event for artifact change)
8. Projection Service notified (via event or polling)
9. Response returned to caller
```

**Critical invariant:** Step 5 (Git commit) must succeed before step 6 (runtime update) is considered final. If Git fails, the operation fails — runtime state is not updated.

### 8.2 Git Commit as Durable Boundary

The Git commit is the point at which an operation becomes durable truth. The sequence around it:

```
Runtime: step.completed (persisted, status = committing)
    ↓
Git: atomic commit with trailers (Trace-ID, Actor-ID, Run-ID, Operation)
    ↓  success?
    ├── yes → Runtime: run.completed (persisted) + event emitted
    └── no  → Retry (transient) or fail (permanent) — runtime stays in committing
```

The `committing` state (per [Engine State Machine](/architecture/engine-state-machine.md) §2) ensures that a crash between Git commit and runtime update results in a recoverable state — the engine retries the commit on recovery.

### 8.3 Transaction Boundaries

| Boundary | Atomicity | Recovery |
|----------|-----------|---------|
| Runtime Store writes | Atomic (single DB transaction) | Rollback on failure |
| Git commit | Atomic (single commit) | Retry on transient failure |
| Runtime + Git combined | NOT atomic | State machine handles gap: `committing` state bridges the two |
| Event emission | After runtime write | If lost, events are reconstructible from Git |

---

## 9. Recovery and Idempotency Rules

### 9.1 What Must Be Idempotent

| Operation | Idempotency Key | Behavior on Duplicate |
|-----------|----------------|----------------------|
| Git commit | Trace-ID in commit trailer | Check if commit with same Trace-ID exists; skip if so |
| Step result submission | Assignment ID + outcome | Check if execution already completed; reject if so |
| Queue entry processing | Idempotency key on entry | Check if entry already processed; skip if so |
| Run creation | Task path + deterministic run ID | Check if Run exists; reject if so |
| Event emission | Event ID | Consumers must handle at-least-once delivery |

### 9.2 Recovery Sequence (Engine Restart)

```
1. Scan runtime.runs WHERE status IN ('active', 'paused', 'committing')
2. For each run:
   a. committing → re-attempt Git commit (idempotent)
   b. active → inspect current step execution status:
      - waiting → re-attempt assignment
      - assigned → check actor availability
      - in_progress → check for timeout
      - blocked → check if blocking condition resolved
   c. paused → leave paused (operator decision)
3. Scan runtime.queue_entries WHERE status = 'processing'
   - Re-queue as 'pending' (the processor may have crashed)
4. Emit engine_recovered event
```

### 9.3 "Already Committed" Detection

The Artifact Service detects already-committed changes by:

1. Checking if a commit with the same `Trace-ID` trailer exists in the repository
2. If found, the operation is a duplicate — return the existing commit SHA
3. If not found, proceed with the commit

This makes Git commit retries safe after crashes.

---

## 10. Branch and Merge Execution Model

### 10.1 Task Branch Lifecycle (Implementation)

```
1. run.start → Artifact Service creates branch: spine/<run-id>/<task-slug>
2. Step execution produces work → commits land on the task branch
3. Review/validation steps operate on the task branch content
4. Terminal outcome reached → Artifact Service merges task branch to authoritative branch
5. Merge commit includes all standard trailers
6. Task branch is deleted after successful merge
```

The task branch is created from the authoritative branch HEAD at Run creation time. This ensures the Run operates against the governed baseline.

### 10.2 Divergence Branch Lifecycle (Implementation)

```
1. Divergence triggered → Artifact Service creates branches:
   spine/<run-id>/<divergence-id>/branch-a
   spine/<run-id>/<divergence-id>/branch-b
   (each forked from the task branch, not from authoritative)
2. Each branch executes independently with isolated commits
3. Convergence → evaluation step reads all branch content
4. Selected branch merged to task branch
5. Non-selected branches preserved (never deleted)
6. Task branch eventually merged to authoritative (at Run completion)
```

### 10.3 Merge Authority

All merges to the authoritative branch are performed exclusively by the Artifact Service (per [Git Integration](/architecture/git-integration.md) §6.3):

- The Workflow Engine tells the Artifact Service *when* to merge (terminal outcome reached, validation passed)
- The Artifact Service performs the merge operation and handles conflicts
- No actor or external system may merge `spine/*` branches directly

### 10.4 Worktree Management

For concurrent Runs, the Artifact Service uses Git worktrees to allow multiple branches to be checked out simultaneously:

- Each active Run's task branch gets its own worktree at `<worktree_path>/<run-id>/`
- Worktrees are created at Run start and cleaned up after branch merge or Run failure
- This avoids checkout-switching on the main repository clone

---

## 11. Implementation Invariants

These rules are non-negotiable in the implementation. Violating any of them is a bug.

1. **No durable success without confirmed Git commit** — a Run is not `completed` until its Git commit is confirmed. The `committing` state exists specifically to enforce this.

2. **No projection write treated as truth** — code must never read from the Projection Store and treat it as authoritative. Projections are derived, stale-tolerant, and disposable.

3. **No direct artifact mutation outside governed service path** — all artifact changes flow through the Artifact Service, which validates schema, checks governance, and commits to Git. No package may write to Git directly.

4. **No merge without Spine validation** — the Artifact Service must not merge a branch to authoritative without the Workflow Engine confirming that all governance checks have passed.

5. **No runtime state without persistence** — state transitions must be persisted to the database *before* their effects (events, Git commits) are executed. This ensures crash recovery is deterministic.

6. **No queue entry treated as committed state** — queue entries are transient. If the process crashes, queue state is lost. All critical state must be in the Runtime Store or Git.

7. **No actor response trusted without validation** — all actor responses pass through the Workflow Engine for outcome validation and artifact schema checking before any state change.

---

## 12. Configuration

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
  worktree_path: "/var/spine/worktrees"    # Location for task/divergence branch checkouts
  merge_strategy: "fast-forward"            # fast-forward or merge-commit
  commit_retry_limit: 3
  commit_retry_backoff: "exponential"

projection:
  polling_interval: "30s"
  webhook_enabled: true
  sync_mode: "incremental"                  # incremental or full-rebuild

queue:
  type: "memory"                            # memory (v0.x) or external broker later

actor_gateway:
  providers:                                # Actor provider configuration
    anthropic:
      type: "ai_agent"
      api_key_env: "ANTHROPIC_API_KEY"      # Environment variable name (never stored in config)
    ci:
      type: "automated_system"
      webhook_url: "https://ci.example.com/spine"

observability:
  trace_id_header: "X-Trace-Id"
  log_level: "info"
  log_format: "json"
  metrics_enabled: false                    # v0.x: optional
```

### 7.3 Secrets

Secrets (database password, API tokens, Git credentials) are provided via environment variables, never in config files or Git (per [Security Model](/architecture/security-model.md) §5).

---

## 13. Development Workflow

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

## 14. Cross-References

- [ADR-005](/architecture/adr/ADR-005-technology-selection.md) — Technology selection decision
- [System Components](/architecture/components.md) — Component responsibilities
- [Runtime Schema](/architecture/runtime-schema.md) — Database table definitions
- [Git Integration](/architecture/git-integration.md) — Git operational contract
- [API Operations](/architecture/api-operations.md) — API design
- [OpenAPI Specification](/api/spec.yaml) — Machine-readable API contract
- [Engine State Machine](/architecture/engine-state-machine.md) — State machine to implement
- [Security Model](/architecture/security-model.md) §5 — Secret management

---

## 15. Evolution Policy

This implementation guide evolves with the codebase. As patterns emerge during development:

- Package structure may be refined (packages split or merged)
- New interfaces may be introduced for testability
- Build tooling may expand (CI/CD pipeline, release automation)
- Configuration schema will grow as features are added

Changes that alter the package dependency rules or internal interface contracts should be discussed before implementation.
