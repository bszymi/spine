---
type: Architecture
title: Scenario Testing Strategy
status: Living Document
version: "0.1"
---

# Scenario Testing Strategy

---

## 1. Purpose

This document defines the testing strategy for Spine's Product Scenario Testing System. It establishes what is tested, how scenarios are structured, naming conventions, the execution model, and CI integration approach.

This strategy ensures that initiative-level success criteria can be expressed as executable scenarios, providing measurable validation of product behaviour.

This strategy complements the [Scenario Testing Architecture](/architecture/scenario-testing-architecture.md), which defines the technical design of the test harness, scenario engine, and assertion framework.

---

## 2. Test Layer Taxonomy

Spine uses three distinct test layers. Each layer validates different aspects of the system and has clear boundaries.

### 2.1 Unit Tests

| Aspect | Detail |
|--------|--------|
| **What** | Individual functions, methods, and types in isolation |
| **Where** | Colocated with source: `internal/<package>/*_test.go` |
| **Build tag** | None (runs with `go test ./...`) |
| **Dependencies** | No external systems; in-memory fakes or stubs only |
| **Speed** | Milliseconds per test |
| **Example** | `TestParseInitiative` verifies YAML frontmatter parsing for a single artifact |

### 2.2 Integration Tests

| Aspect | Detail |
|--------|--------|
| **What** | Component interactions with real external systems (PostgreSQL, Git) |
| **Where** | Colocated with source: `internal/<package>/*_integration_test.go` |
| **Build tag** | `//go:build integration` |
| **Dependencies** | Test PostgreSQL via `docker-compose.test.yaml`; temporary Git repositories |
| **Speed** | Seconds per test |
| **Example** | `TestPostgresRunCRUD` verifies run persistence through the real store |

### 2.3 Scenario Tests

| Aspect | Detail |
|--------|--------|
| **What** | Full product-flow behaviour across multiple lifecycle stages (may span multiple services) |
| **Where** | `internal/scenariotest/scenarios/*_test.go` |
| **Build tag** | `//go:build scenario` |
| **Dependencies** | Full Spine runtime (in-process), test PostgreSQL, temporary Git repositories |
| **Speed** | Seconds to tens of seconds per scenario |
| **Example** | A golden-path scenario that creates an initiative, binds a workflow, executes all steps, and verifies final artifact status |

### 2.4 Boundary Rules

- **Unit tests** must not require external services (PostgreSQL, network). Lightweight local resources (temporary directories via `t.TempDir()`, temporary Git repositories via `testutil.NewTempRepo`) are permitted and already used throughout the codebase.
- **Integration tests** validate a single component's interaction with a shared external service (e.g., PostgreSQL). They require the `integration` build tag and `docker-compose.test.yaml`.
- **Scenario tests** validate product behaviour across multiple components and lifecycle stages. They normally involve multiple Spine services (e.g., artifact creation followed by workflow execution), but the defining characteristic is cross-lifecycle behaviour, not the number of services.
- If a test validates only one function or one component's database interaction, it belongs in unit or integration tests, not scenarios.

---

## 3. Scenario Taxonomy

Scenarios are organized into four categories, each with a distinct purpose and scope.

These categories represent the primary validation intent. A scenario may exhibit characteristics of multiple categories, but should be classified according to its primary purpose.

### 3.1 Golden Path Scenarios

**Purpose:** Validate that the intended, successful product flow works correctly end-to-end.

**Characteristics:**
- All inputs are valid
- All governance rules are satisfied
- All workflow steps complete successfully
- Final state matches the expected product outcome

**When to use:** To verify that a complete lifecycle (create, execute, complete) produces the correct result. Golden path scenarios are the first scenarios written for any new capability.

**Examples:**
- Initiative creation through task acceptance
- Workflow execution from start to completion
- Artifact creation with valid schema and relationships

### 3.2 Negative Scenarios

**Purpose:** Validate that the system correctly rejects invalid states, inputs, or transitions.

**Characteristics:**
- At least one input or action is invalid
- The system must reject the operation with a clear error
- System state must remain consistent after rejection (no partial mutations)

**When to use:** To verify that guardrails work. Every golden path scenario should have at least one corresponding negative scenario.

**Examples:**
- Creating an artifact with missing required fields
- Attempting a workflow transition that violates step ordering
- Submitting a result for a step that is not active

### 3.3 Governance Scenarios

**Purpose:** Validate that Constitution principles and governance rules are enforced.

**Characteristics:**
- Focus on permission boundaries, actor neutrality, and governance constraints
- Must test both human and AI actors with identical expectations
- Verify that governance violations produce actionable error messages

**When to use:** To verify that non-negotiable rules from the Constitution cannot be bypassed. Governance scenarios are mandatory for any feature that involves permissions, actor roles, or constitutional principles.

**Examples:**
- Constitution enforcement (governed execution, explicit intent)
- Permission validation (role-based access to operations)
- AI actor governance (same rules apply as for human actors)
- Divergence and convergence handling

### 3.4 Resilience Scenarios

**Purpose:** Validate that the system recovers correctly from failures, restarts, and state loss.

**Characteristics:**
- Simulate runtime loss, database wipe, or partial state corruption
- Verify that Git-based reconstruction produces consistent state
- Confirm that projections can be rebuilt from Git

**When to use:** To verify Constitutional principle §8 (Disposable Database) — that runtime state can always be reconstructed from Git.

**Examples:**
- Runtime recovery after simulated crash
- Projection rebuild from Git history
- Git-based state reconstruction

---

## 4. Naming and Organization Conventions

### 4.1 File Organization

```
internal/scenariotest/
  scenarios/
    artifact_test.go         # Artifact lifecycle scenarios (golden + negative)
    workflow_test.go          # Workflow execution scenarios (golden + negative)
    governance_test.go        # Governance enforcement scenarios
    resilience_test.go        # Recovery and rebuild scenarios
```

As scenario suites grow, files may be split by subcategory:

```
    artifact_golden_test.go
    artifact_negative_test.go
    workflow_golden_test.go
    workflow_negative_test.go
```

### 4.2 Test Function Naming

Test functions follow the pattern:

```
Test<Category>_<Behaviour>
```

**Category** matches the scenario taxonomy: `Artifact`, `Workflow`, `Governance`, `Resilience`.

**Behaviour** describes the product outcome being validated, not the implementation detail.

Examples:

```go
// Golden path
func TestArtifact_InitiativeLifecycle(t *testing.T)
func TestWorkflow_TaskExecutionToCompletion(t *testing.T)

// Negative
func TestArtifact_RejectsMissingRequiredFields(t *testing.T)
func TestWorkflow_RejectsInvalidTransition(t *testing.T)

// Governance
func TestGovernance_ConstitutionEnforcesGoverningArtifact(t *testing.T)
func TestGovernance_AIActorSameRulesAsHuman(t *testing.T)

// Resilience
func TestResilience_ProjectionRebuildFromGit(t *testing.T)
func TestResilience_RuntimeRecoveryAfterCrash(t *testing.T)
```

### 4.3 Scenario Names

The `Scenario.Name` field uses lowercase-with-hyphens, describing the product flow:

```go
Scenario{
    Name: "initiative-to-task-acceptance",
    // ...
}
```

### 4.4 Step Names

Step names use imperative, lowercase-with-hyphens:

```go
Step{Name: "create-initiative"}
Step{Name: "bind-workflow"}
Step{Name: "submit-step-result"}
Step{Name: "verify-task-accepted"}
```

---

## 5. Execution Model

### 5.1 Build Tag Isolation

Scenario tests use the `scenario` build tag:

```go
//go:build scenario
```

This ensures scenarios never run during `go test ./...` or `go test -tags integration ./...`. They must be explicitly invoked.

### 5.2 Makefile Targets

The following targets are planned additions for EPIC-002 onwards. They may not yet exist in the current Makefile.

The following targets should be added to the Makefile when the scenario test package is created (EPIC-002 onwards):

```makefile
# Planned additions — not yet in Makefile
test-scenario:
	go test -tags scenario ./internal/scenariotest/...

test-scenario-v:
	go test -tags scenario -v ./internal/scenariotest/...

test-all:
	go test ./...
	go test -tags integration ./...
	go test -tags scenario ./internal/scenariotest/...
```

### 5.3 Prerequisites

Scenario tests require:

1. **Test PostgreSQL running** — via `make test-db-up` (uses `docker-compose.test.yaml`)
2. **Migrations applied** — the test harness applies migrations automatically via `NewTestDB(t)`
3. **Git available** — standard `git` CLI on `$PATH`

If PostgreSQL is unavailable, scenario tests skip with `t.Skip("test database not available")`.

### 5.4 Parallelism

- **Between scenarios:** All scenarios call `t.Parallel()` by default. Each scenario creates its own Git repository and uses scenario-scoped ID prefixes for database isolation.
- **Within scenarios:** Steps execute sequentially. A scenario represents a linear product flow — parallel step execution would violate the real usage model.
- **CI parallelism:** Standard `go test -parallel N` controls the degree of parallelism. Default: `GOMAXPROCS`.

### 5.5 Timeouts

| Level | Default | Rationale |
|-------|---------|-----------|
| **Per scenario** | 60 seconds | Scenarios involve Git operations and database I/O |
| **Per step** | 10 seconds | Individual steps should be fast; slow steps indicate a problem |
| **CI run** | 5 minutes | Total scenario suite timeout; prevents hung CI |

Timeouts are enforced via `go test -timeout` and can be overridden per-scenario using `context.WithTimeout` in the scenario runner.

Developers may override these defaults locally during debugging or investigation. CI pipelines should use the stricter defaults to detect regressions and hung tests.

### 5.6 Failure Behaviour

- **Step failure:** The scenario aborts immediately. Remaining steps do not execute. The failing step is reported with its name and error.
- **Scenario failure:** Other parallel scenarios continue. Only the failed scenario is marked as failed.
- **Environment failure:** If the test database is unavailable, all scenarios skip (not fail).

---

## 6. CI Integration

### 6.1 Execution Triggers

| Trigger | What Runs |
|---------|-----------|
| Every push | `make test` (unit tests) |
| Every push | `make lint` |
| Every push with `integration` or `scenario` changes | `make test-integration` + `make test-scenario` |
| Pre-merge (PR) | All three: `make test`, `make test-integration`, `make test-scenario` |
| Nightly / scheduled | `make test-all` (full suite) |

Initial implementations may use path-based detection (e.g., changes under `internal/scenariotest/` or runtime components). Pre-merge execution remains the safety net to ensure full validation regardless of change scope.

### 6.2 CI Pipeline Structure

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  Unit Tests  │────→│ Integration Tests │────→│ Scenario Tests  │
│  (fast, no   │     │ (needs DB)        │     │ (needs DB + Git)│
│   deps)      │     │                   │     │                 │
└─────────────┘     └──────────────────┘     └─────────────────┘
      ~10s                ~30s                     ~2-3min
```

Unit tests run first as a fast gate. Integration and scenario tests require the test database service.

### 6.3 CI Database Setup

The CI pipeline starts the test database before integration and scenario tests:

```yaml
# GitHub Actions example
services:
  postgres:
    image: postgres:18-bookworm
    env:
      POSTGRES_USER: spine_test
      POSTGRES_PASSWORD: spine_test
      POSTGRES_DB: spine_test
    ports:
      - 5433:5432
    options: >-
      --tmpfs /var/lib/postgresql
      --health-cmd "pg_isready -U spine_test"
      --health-interval 2s
      --health-timeout 2s
      --health-retries 10
```

### 6.4 Output and Reporting

Scenario tests produce standard `go test` output. No custom reporters are required in v0.1.

- **Verbose mode** (`-v`) prints each scenario name and step result
- **JSON mode** (`-json`) enables machine-readable output for CI dashboards
- Step-level granularity is provided by `t.Run()` subtests

---

## 7. Test Data Management

### 7.1 Seeding

Each scenario starts with a freshly seeded environment. The test harness provides seed functions:

| Function | What It Seeds |
|----------|--------------|
| `SeedGovernance(t)` | Constitution, Charter, Guidelines in `governance/` |
| `SeedWorkflows(t)` | Default workflow definitions in `workflows/` |
| `WriteArtifact(t, path, content)` | Arbitrary artifact for scenario-specific setup |

Seed data is deterministic — identical across runs. Governance seed documents are derived from the actual production governance files to ensure test fidelity.

### 7.2 Fixtures

Scenarios that need specific artifact content use inline Go constants rather than external fixture files:

```go
const taskYAML = `---
id: TASK-001
type: Task
title: "Test Task"
status: Pending
---
# TASK-001 — Test Task
`
```

This keeps test data colocated with test logic and avoids hidden dependencies on fixture files.

For larger or more complex artifacts, shared builders or helper functions should be preferred over large inline constants to maintain readability.

For commonly reused fixtures (e.g., a valid initiative with one epic and one task), shared fixture builders are provided:

```go
func FixtureInitiativeWithTask(t *testing.T, repo *TestRepo) string
```

### 7.3 Cleanup

Cleanup is automatic via `t.Cleanup()`:

- **Git repositories:** Temporary directories removed by `t.TempDir()` lifecycle
- **Database:** Scenario-scoped ID prefixes enable targeted cleanup without affecting parallel scenarios. Each scenario's `TestDB.Cleanup(t)` deletes only rows matching its prefix
- **Runtime:** In-process services require no explicit teardown

No manual cleanup code should appear in scenario test functions.

### 7.4 Determinism

All scenarios must produce identical results across runs. Sources of non-determinism and their mitigations:

| Source | Mitigation |
|--------|-----------|
| Timestamps | Deterministic clock injected via test harness |
| Random IDs | Scenario-scoped prefix with sequential counters |
| Git commit hashes | Deterministic author, date, and content |
| Database ordering | Explicit `ORDER BY` in all queries; assertions do not depend on insertion order |

---

## 8. Coverage Goals

Implementation should prioritize golden path scenarios first, followed by negative and governance scenarios, and finally resilience scenarios.

### 8.1 Mandatory Coverage

The following areas must have scenario test coverage before INIT-004 can be marked complete:

| Area | Minimum Scenarios | Categories |
|------|-------------------|------------|
| Artifact lifecycle | 3 golden, 3 negative | Golden path, Negative |
| Workflow execution | 2 golden, 2 negative | Golden path, Negative |
| Governance enforcement | 4 scenarios | Governance |
| Actor neutrality | 2 scenarios (human + AI) | Governance |
| Runtime resilience | 2 scenarios | Resilience |
| Projection rebuild | 1 scenario | Resilience |

Total minimum: **19 scenarios** across all categories.

### 8.2 Coverage Measurement

Scenario tests do not aim for line-level code coverage. Their value is measured by:

1. **Behaviour coverage:** Each success criterion from the [INIT-004 initiative](/initiatives/INIT-004-product-scenario-testing/initiative.md) has at least one corresponding scenario
2. **Constitutional coverage:** Each constitutional principle referenced in the [architecture spec](/architecture/scenario-testing-architecture.md#10-constitutional-alignment) has at least one enforcement scenario
3. **Category balance:** No single category (golden path, negative, governance, resilience) accounts for more than 50% of total scenarios

### 8.3 What Is Not Covered by Scenarios

Scenario tests intentionally do not cover:

- **Internal implementation details** — tested by unit tests
- **Single-component database interactions** — tested by integration tests
- **HTTP response format, status codes, headers** — tested by gateway-specific integration tests (except where a scenario explicitly validates the API layer)
- **Performance, load, or latency** — out of scope for INIT-004
- **UI behaviour** — Spine has no UI

---

## 9. Growth Policy

### 9.1 When to Add a New Scenario

Add a scenario when:

- A new product capability is introduced that involves multiple services
- A bug is found that scenario tests should have caught (regression scenario)
- A new constitutional principle or governance rule is added
- A resilience concern is identified that is not covered

### 9.2 When Not to Add a Scenario

Do not add a scenario when:

- The behaviour can be fully validated by a unit or integration test
- The scenario duplicates an existing one with only minor parameter variation (use table-driven subtests instead)
- The scenario tests implementation details rather than product behaviour

### 9.3 Scenario Maintenance

- Scenarios that break due to refactoring (not behaviour change) indicate coupling to implementation details — fix the scenario, not the code
- Scenarios that become flaky must be investigated immediately. Flaky scenarios are treated as bugs, not tolerated
- Scenario seed data must be updated when governance documents change

---

## 10. Cross-References

- [Scenario Testing Architecture](/architecture/scenario-testing-architecture.md) — Technical design
- [INIT-004 — Product Scenario Testing](/initiatives/INIT-004-product-scenario-testing/initiative.md) — Parent initiative
- [Constitution](/governance/constitution.md) — Non-negotiable principles
- [EPIC-001 — Architecture and Design](/initiatives/INIT-004-product-scenario-testing/epics/EPIC-001-architecture-design/epic.md) — Parent epic
