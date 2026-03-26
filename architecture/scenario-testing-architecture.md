---
type: Architecture
title: Scenario Testing Architecture
status: Living Document
version: "0.1"
---

# Scenario Testing Architecture

---

## 1. Purpose

This document defines the architecture for Spine's Product Scenario Testing System. The system validates Spine behaviour from a product perspective — not just code correctness — by executing end-to-end scenarios against isolated Spine environments.

The testing system complements existing unit tests (logic correctness) and integration tests (component interaction) with scenario-based validation of full Spine behaviour: artifact lifecycle, workflow execution, governance enforcement, and runtime resilience.

All architectural decisions must comply with the [Constitution](/governance/constitution.md), particularly the Reproducibility (§7) and Disposable Database (§8) principles — test scenarios must produce deterministic, reproducible outcomes.

---

## 2. System Overview

The scenario testing system consists of four layers:

```
┌─────────────────────────────────────────────────────┐
│                  Scenario Suites                     │
│   (artifact, workflow, governance, resilience)       │
├─────────────────────────────────────────────────────┤
│                  Scenario Engine                     │
│   (definition, execution, step composition)          │
├─────────────────────────────────────────────────────┤
│                Assertion Framework                   │
│   (git, artifact, workflow, governance assertions)   │
├─────────────────────────────────────────────────────┤
│                   Test Harness                       │
│   (git repo, database, runtime, API client)          │
└─────────────────────────────────────────────────────┘
```

Each layer has a single responsibility and communicates only with adjacent layers. Scenario suites use the engine, the engine uses assertions, and assertions use the harness.

---

## 3. Test Harness

### 3.1 Responsibility

The test harness creates and manages isolated Spine environments. Each test scenario receives a fresh environment with no shared state. The harness is the only layer that interacts with external systems (Git, database, runtime).

### 3.2 Components

#### 3.2.1 Temporary Git Repository

Builds on the existing `testutil.NewTempRepo(t)` helper. Each scenario gets a temporary Git repository initialized with:

- Standard Spine directory structure (`governance/`, `initiatives/`, `workflows/`, `architecture/`)
- Seed governance documents (Constitution, Charter, Guidelines)
- Default workflow definitions
- Clean main branch with initial commit

```go
type TestRepo struct {
    Dir      string           // path to temporary repository
    Git      git.GitClient    // Spine GitClient configured for this repo
}

func NewTestRepo(t *testing.T) *TestRepo
func (r *TestRepo) SeedGovernance(t *testing.T)
func (r *TestRepo) SeedWorkflows(t *testing.T)
func (r *TestRepo) WriteArtifact(t *testing.T, path, content string)
func (r *TestRepo) CommitAll(t *testing.T, message string)
func (r *TestRepo) HeadSHA(t *testing.T) string
```

Deterministic timestamps ensure reproducible Git history across runs.

#### 3.2.2 Test Database

Builds on the existing `store.NewTestStore(t)` and `testutil.NewTestConn(t)` helpers. Each scenario gets:

- A shared test PostgreSQL instance (via Docker Compose)
- Migrations applied automatically
- Data isolation via `CleanupTestData()` between scenarios
- Transaction rollback support for read-only scenario verification

```go
type TestDB struct {
    Store    *store.PostgresStore
    Conn     *pgx.Conn
}

func NewTestDB(t *testing.T) *TestDB
func (db *TestDB) Cleanup(t *testing.T)
```

Per Constitution §8 (Disposable Database), the test database is an accelerator — all durable test state exists in the temporary Git repository.

#### 3.2.3 Runtime Integration

The runtime layer wires Spine services for in-process execution. No HTTP server is started — scenarios call services directly for speed and determinism.

```go
type TestRuntime struct {
    Store       *store.PostgresStore
    Artifacts   *artifact.Service
    Projections *projection.Service
    Engine      *engine.Orchestrator
    Events      *event.QueueRouter
    Auth        *auth.Service
    Validator   *validation.Engine
}

func NewTestRuntime(t *testing.T, repo *TestRepo, db *TestDB) *TestRuntime
```

The runtime is constructed from the same packages used in production, ensuring test fidelity. Optional components (divergence, convergence) can be enabled per scenario.

#### 3.2.4 API Client

For scenarios that need to validate the HTTP layer, an optional test server can be started:

```go
type TestServer struct {
    URL    string
    Client *cli.Client
}

func NewTestServer(t *testing.T, rt *TestRuntime) *TestServer
```

Most scenarios should use the runtime directly. The API client is reserved for gateway-level validation (auth, error codes, response format).

### 3.3 Environment Lifecycle

```
NewTestRepo(t)  ──→  SeedGovernance/Workflows  ──→  NewTestDB(t)  ──→  NewTestRuntime(t, repo, db)
                                                                              │
                                                                              ▼
                                                                    Scenario Execution
                                                                              │
                                                                              ▼
                                                               t.Cleanup() tears down all
```

All setup uses `t.Cleanup()` for automatic teardown. No manual cleanup is required.

### 3.4 Parallel Execution

Each scenario creates its own Git repository and cleans its own database state. No shared mutable state exists between scenarios. This enables `t.Parallel()` on all scenarios without interference.

The test database is shared at the PostgreSQL instance level, but data isolation is achieved through cleanup between tests and unique IDs per scenario.

---

## 4. Scenario Engine

### 4.1 Responsibility

The scenario engine provides a structured way to define and execute multi-step test scenarios. It handles step sequencing, context propagation between steps, and result collection.

### 4.2 Scenario Definition

A scenario is a named sequence of steps that operate on a test environment:

```go
type Scenario struct {
    Name        string
    Description string
    Steps       []Step
}

type Step struct {
    Name   string
    Action func(ctx *ScenarioContext) error
}
```

Steps execute sequentially. Each step receives a `ScenarioContext` that carries the test environment and accumulated state from previous steps.

### 4.3 Scenario Context

The context is the primary mechanism for passing state between steps:

```go
type ScenarioContext struct {
    T        *testing.T
    Repo     *TestRepo
    DB       *TestDB
    Runtime  *TestRuntime
    Server   *TestServer   // nil unless API testing is needed
    State    map[string]any // step-to-step state (run IDs, artifact paths, etc.)
}

func (sc *ScenarioContext) Get(key string) any
func (sc *ScenarioContext) Set(key string, value any)
func (sc *ScenarioContext) MustGet(key string) any  // fails test if missing
```

### 4.4 Execution Model

```go
func RunScenario(t *testing.T, scenario Scenario) {
    // 1. Create environment
    repo := NewTestRepo(t)
    db := NewTestDB(t)
    rt := NewTestRuntime(t, repo, db)

    ctx := &ScenarioContext{
        T: t, Repo: repo, DB: db, Runtime: rt,
        State: make(map[string]any),
    }

    // 2. Execute steps sequentially
    for _, step := range scenario.Steps {
        t.Run(step.Name, func(t *testing.T) {
            if err := step.Action(ctx); err != nil {
                t.Fatalf("step %q failed: %v", step.Name, err)
            }
        })
    }
}
```

Each step runs as a `t.Run()` subtest, providing:
- Per-step pass/fail reporting
- Step-level timeout support
- Early termination on fatal failures via `t.Fatalf`

### 4.5 Step Composition

Common operations are provided as reusable step builders:

```go
// Artifact steps
func CreateArtifact(path, content string) Step
func UpdateArtifact(path, content string) Step
func AcceptTask(path, rationale string) Step
func RejectTask(path string) Step

// Workflow steps
func StartRun(taskPath string) Step
func SubmitStepResult(outcomeID string) Step
func CancelRun() Step

// Assertion steps
func AssertArtifactStatus(path string, expected domain.ArtifactStatus) Step
func AssertRunCompleted() Step
func AssertValidationPasses(path string) Step
```

Step builders return `Step` values. They capture parameters at definition time and execute against the `ScenarioContext` at runtime.

---

## 5. Assertion Framework

### 5.1 Responsibility

The assertion framework provides domain-specific assertions for verifying Spine state. Assertions operate on the test harness layer and provide clear failure messages that reference Spine concepts (artifact paths, run statuses, validation rules).

### 5.2 Assertion Types

#### 5.2.1 Git Assertions

Verify the state of the Git repository:

```go
func AssertFileExists(t *testing.T, repo *TestRepo, path string)
func AssertFileContent(t *testing.T, repo *TestRepo, path, expected string)
func AssertFileContains(t *testing.T, repo *TestRepo, path, substring string)
func AssertCommitCount(t *testing.T, repo *TestRepo, expected int)
func AssertLastCommitMessage(t *testing.T, repo *TestRepo, contains string)
func AssertBranchExists(t *testing.T, repo *TestRepo, branch string)
```

#### 5.2.2 Artifact Assertions

Verify artifact state through the projection or Git layers:

```go
func AssertArtifactExists(t *testing.T, rt *TestRuntime, path string)
func AssertArtifactStatus(t *testing.T, rt *TestRuntime, path string, expected domain.ArtifactStatus)
func AssertArtifactType(t *testing.T, rt *TestRuntime, path string, expected domain.ArtifactType)
func AssertArtifactHasLink(t *testing.T, rt *TestRuntime, path string, linkType domain.LinkType, target string)
func AssertArtifactMetadata(t *testing.T, rt *TestRuntime, path, key, expected string)
```

#### 5.2.3 Workflow Assertions

Verify run and step execution state:

```go
func AssertRunStatus(t *testing.T, rt *TestRuntime, runID string, expected domain.RunStatus)
func AssertRunCompleted(t *testing.T, rt *TestRuntime, runID string)
func AssertStepStatus(t *testing.T, rt *TestRuntime, execID string, expected domain.StepExecutionStatus)
func AssertStepCount(t *testing.T, rt *TestRuntime, runID string, expected int)
func AssertCurrentStep(t *testing.T, rt *TestRuntime, runID, stepID string)
```

#### 5.2.4 Governance Assertions

Verify governance rule enforcement:

```go
func AssertValidationPasses(t *testing.T, rt *TestRuntime, path string)
func AssertValidationFails(t *testing.T, rt *TestRuntime, path string, ruleID string)
func AssertOperationForbidden(t *testing.T, err error)
func AssertOperationRequiresRole(t *testing.T, err error, role domain.ActorRole)
```

### 5.3 Assertion Composition

Assertions can be combined in scenario steps:

```go
Step{
    Name: "verify task completed",
    Action: func(ctx *ScenarioContext) error {
        runID := ctx.MustGet("run_id").(string)
        AssertRunCompleted(ctx.T, ctx.Runtime, runID)
        AssertArtifactStatus(ctx.T, ctx.Runtime, "tasks/TASK-001.md", domain.StatusCompleted)
        AssertLastCommitMessage(ctx.T, ctx.Repo, "Accepted")
        return nil
    },
}
```

All assertion functions call `t.Helper()` so failures report at the correct call site.

---

## 6. Integration Layer

### 6.1 Responsibility

The integration layer defines how scenarios interact with the three external systems: Git, the Spine runtime, and the database.

### 6.2 Git Integration

Scenarios interact with Git through two paths:

| Path | Use Case |
|------|----------|
| **Direct file operations** | Seeding content, verifying committed state |
| **Artifact service** | Creating/updating artifacts through the governed pipeline |

Direct file operations (`WriteArtifact`, `CommitAll`) bypass governance for setup. All scenario actions that represent product behaviour go through the artifact service.

### 6.3 Runtime Integration

The runtime is wired identically to production:

```
TestRepo.Git ──→ ArtifactService ──→ ProjectionService ──→ Engine/Orchestrator
                       │                    │
                       └────────────────────┴──→ TestDB.Store
```

Projection sync runs synchronously in tests (no polling interval). Events are delivered synchronously through the queue router.

### 6.4 Database Integration

The database serves two roles in testing:

| Role | What | Notes |
|------|------|-------|
| **Runtime state** | Runs, step executions, assignments | Operational; disposable per §8 |
| **Projections** | Artifact index, workflow cache | Rebuilt from Git on demand |

Scenarios can verify runtime state through the store or verify projection state through the query service. Both paths are valid — the choice depends on what the scenario is testing.

### 6.5 Actor Simulation

Scenarios simulate actors without real authentication:

```go
func WithActor(actorID string, role domain.ActorRole) context.Context
```

This sets the actor in context identically to the auth middleware, allowing scenarios to test permission boundaries without token management.

---

## 7. Package Structure

```
internal/
  scenariotest/           # Top-level package for scenario testing
    harness/              # Test harness: repo, database, runtime setup
      repo.go             # Temporary Git repository management
      db.go               # Test database setup
      runtime.go          # Spine runtime wiring
      server.go           # Optional HTTP test server
    engine/               # Scenario engine: definition, execution
      scenario.go         # Scenario and Step types
      context.go          # ScenarioContext
      runner.go           # RunScenario executor
      steps.go            # Reusable step builders
    assert/               # Assertion framework
      git.go              # Git assertions
      artifact.go         # Artifact assertions
      workflow.go         # Workflow assertions
      governance.go       # Governance assertions
    scenarios/            # Scenario suites (test files)
      artifact_test.go    # Artifact lifecycle scenarios
      workflow_test.go    # Workflow execution scenarios
      governance_test.go  # Governance enforcement scenarios
      resilience_test.go  # Recovery and rebuild scenarios
```

### 7.1 Dependency Direction

```
scenarios/ ──→ engine/ ──→ assert/ ──→ harness/
```

No reverse dependencies. Each layer imports only from layers below it. The harness has no Spine-specific assertions — it only provides environment setup.

---

## 8. Design Decisions

### 8.1 In-Process Over HTTP

Scenarios call Spine services directly rather than through the HTTP gateway. This provides:

- Faster execution (no network overhead)
- Synchronous projection sync (no polling delays)
- Direct access to internal state for verification
- Simpler error handling (Go errors, not HTTP status codes)

HTTP-level testing is reserved for gateway-specific scenarios (auth, error response format).

### 8.2 Sequential Steps Over Parallel

Scenario steps execute sequentially within a scenario. This reflects real Spine usage — a workflow progresses step-by-step. Parallelism exists between scenarios (via `t.Parallel()`), not within them.

### 8.3 Go Test Functions Over Custom DSL

Scenarios are standard Go test functions using the scenario engine. No custom DSL (Gherkin, YAML) is introduced in v0.1. Benefits:

- IDE support (navigation, refactoring, debugging)
- No parser or interpreter to maintain
- Full Go expression power for complex assertions
- Standard `go test` execution and reporting

A Gherkin layer is an extension point for future iterations if non-developer scenario authoring is needed.

### 8.4 Shared Database Instance

A single PostgreSQL instance is shared across parallel test runs. Data isolation is achieved through cleanup, not per-test databases. This balances speed (no per-test DB creation) with isolation (cleanup between scenarios).

---

## 9. Extension Points

The architecture is designed to support future capabilities without structural changes:

| Extension | Integration Point |
|-----------|------------------|
| Gherkin/BDD syntax | New parser layer above scenario engine |
| Performance testing | Harness with configurable load; timing assertions |
| CI/CD integration | `go test` output parsed by CI; no custom runner needed |
| Visual reporting | Result collector in scenario engine |
| External service mocks | Pluggable providers in test runtime |

---

## 10. Constitutional Alignment

| Principle | How the Testing System Supports It |
|-----------|-----------------------------------|
| Source of Truth (§2) | Test repos use Git as source of truth; projections rebuilt from Git |
| Explicit Intent (§3) | Scenarios create governing artifacts before execution |
| Governed Execution (§4) | Scenario workflow steps go through the engine, not bypassed |
| Actor Neutrality (§5) | Same scenarios run with human and AI actor types |
| Reproducibility (§7) | Deterministic timestamps, isolated environments, sequential steps |
| Disposable Database (§8) | Test DB is an accelerator; all durable state in test Git repo |
| Cross-Artifact Validation (§11) | Validation scenarios exercise the real validation engine |

---

## 11. Cross-References

- [INIT-004 — Product Scenario Testing](/initiatives/INIT-004-product-scenario-testing/initiative.md) — Parent initiative
- [Constitution](/governance/constitution.md) — Non-negotiable principles
- [Data Model](/architecture/data-model.md) — Storage layers
- [Runtime Schema](/architecture/runtime-schema.md) — Database schema
- [Discussion Model](/architecture/discussion-model.md) — Runtime collaboration patterns

---

## 12. Evolution Policy

This architecture is expected to evolve as scenario suites are implemented. Areas expected to require refinement:

- Step builder library expansion as new scenario patterns emerge
- Assertion message quality based on debugging experience
- Performance optimizations for parallel test execution
- Environment setup caching for frequently used configurations

Changes that alter the boundary between layers (harness, engine, assertions) or introduce new external dependencies should be captured as ADRs.
