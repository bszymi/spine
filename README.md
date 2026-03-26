# Spine

Spine is a Git-native Product-to-Execution System.

It transforms explicit product intent into governed, observable, and reproducible execution across hybrid teams of humans and AI agents.

Instead of managing work through tickets scattered across tools, Spine treats work as versioned artifacts inside a repository, where intent, architecture, and implementation are structurally connected.

The repository is a shared cognitive model — a single contextual source of truth that enables humans and AI agents to reason about the system as a whole.

---

## Quick Start

```bash
# Initialize a new Spine repository
spine init-repo my-project

# Start the runtime server (requires PostgreSQL)
export SPINE_DATABASE_URL=postgres://localhost:5432/spine
spine migrate
spine serve

# CLI operations
spine artifact list
spine query artifacts --type Task --status Pending
spine workflow list
spine workflow resolve initiatives/INIT-001/tasks/TASK-001.md
spine run start --task initiatives/INIT-001/tasks/TASK-001.md
spine run inspect <run-id>
spine validate --all
```

---

## Start Here

If you're new to the project:

1. Read the [Charter](/governance/charter.md) to understand the philosophy
2. Review the [Product Definition](/product/product-definition.md)
3. Explore the [Architecture](/architecture/domain-model.md)
4. Check [Known Limitations](/KNOWN-LIMITATIONS.md) for current gaps

---

## Why Spine Exists

Modern software teams suffer from structural drift:

- Product intent becomes vague or outdated
- Tickets detach from the original purpose
- Automation runs without governance
- AI produces outputs without structural alignment
- Decisions become invisible over time
- Knowledge fragments across disconnected tools

Spine addresses this by introducing structural integrity between intent and execution, and by maintaining alignment across all project knowledge layers as the system evolves.

---

## Core Idea

Spine is built on a simple but powerful model.

Artifacts define truth.
Workflows define execution.
Actors perform actions.

This creates three structural layers.

---

## Artifact Layer

Git-versioned product and execution artifacts.

Examples:
- Product specifications
- Architecture documents and ADRs
- Initiative, Epic, and Task definitions
- Governance documents (Charter, Constitution, Guidelines)

Git is the source of truth. All artifacts are self-describing, versioned, and diffable.

---

## Execution Layer

A workflow engine governs how work progresses.

Workflows define:

- Valid step transitions and outcome routing
- Preconditions and required outputs
- Cross-artifact validation conditions
- Retry limits with backoff strategies
- Timeout handling at step and run levels
- Divergence and convergence points for parallel execution

Execution produces new artifacts. Only durable outcomes are committed to Git.

---

## Actor Layer

Actors execute workflow steps.

Actors may be:

- Humans
- AI agents
- Automated systems

All actors operate under the same governance rules. AI is treated as an execution actor, not a decision authority.

---

## Implementation

Spine is implemented in Go with PostgreSQL for runtime state.

### System Components

| Component | Package | Description |
|-----------|---------|-------------|
| Engine Orchestrator | `internal/engine` | Run/step lifecycle, divergence, retry, merge |
| Workflow Engine | `internal/workflow` | YAML parsing, state machines, binding resolution |
| Artifact Service | `internal/artifact` | Git-backed CRUD, validation, acceptance |
| Validation Service | `internal/validation` | 20 cross-artifact rules, 5 violation categories |
| Actor Gateway | `internal/actor` | Assignment delivery, selection, mock providers |
| Scheduler | `internal/scheduler` | Timeouts, orphan detection, recovery |
| Access Gateway | `internal/gateway` | HTTP API with auth, all endpoints |
| Projection Service | `internal/projection` | Git-to-PostgreSQL sync |
| Event Router | `internal/event` | In-memory event dispatch |
| Observability | `internal/observe` | Logging, tracing, Prometheus metrics, audit |
| CLI | `internal/cli` | Commands for all operations |

### CLI Commands

```
spine serve              Start runtime server
spine health             System health check
spine migrate            Run database migrations
spine init-repo [path]   Initialize Spine repository

spine artifact create|read|update|list|validate|links
spine run start|status|cancel|inspect
spine task accept|reject|cancel|abandon|supersede
spine query artifacts|graph|history|runs
spine workflow list|show|resolve
spine validate [path] [--all]
```

### API Endpoints

| Method | Path | Operation |
|--------|------|-----------|
| GET | /api/v1/system/health | Health check |
| GET | /api/v1/system/metrics | Prometheus metrics |
| POST | /api/v1/system/rebuild | Projection rebuild |
| POST | /api/v1/system/validate | Validate all artifacts |
| POST | /api/v1/artifacts | Create artifact |
| GET | /api/v1/artifacts | List artifacts |
| GET/PUT | /api/v1/artifacts/* | Read/update artifact |
| POST | /api/v1/runs | Start workflow run |
| GET | /api/v1/runs/{id} | Run status |
| POST | /api/v1/runs/{id}/cancel | Cancel run |
| POST | /api/v1/steps/{id}/submit | Submit step result |
| GET | /api/v1/assignments | List assignments |
| GET | /api/v1/query/artifacts | Query artifacts |
| GET | /api/v1/query/graph | Artifact graph |
| GET | /api/v1/query/history | Change history |
| GET | /api/v1/query/runs | List runs |

### Docker Development

```bash
# Build and run
docker compose build
docker compose up -d

# Run tests (cached Go modules)
make docker-test
make docker-lint
make docker-cover
make docker-vet
```

---

## Repository Structure

```
/
├── cmd/spine/           Go binary entry point
├── internal/            Go packages (18 packages, ~170 files)
├── migrations/          PostgreSQL migrations (7)
├── workflows/           Workflow YAML definitions (4)
├── templates/           Artifact templates
├── governance/          Governance documents
├── product/             Product definition
├── architecture/        Architecture documentation
├── initiatives/         Work tracking (3 initiatives, 23 epics, 113 tasks)
├── Dockerfile           Multi-stage build
├── docker-compose.yaml  Dev environment
└── Makefile             Build and test targets
```

---

## Key Documents

### Governance

- [Charter](/governance/charter.md) — Purpose, philosophy, and structural model
- [Constitution](/governance/constitution.md) — Non-negotiable system constraints and invariants
- [Guidelines](/governance/guidelines.md) — Recommended practices and evolving standards
- [Artifact Schema](/governance/artifact-schema.md) — YAML front matter schema per artifact type
- [Task Lifecycle](/governance/task-lifecycle.md) — Governed vs runtime states, terminal outcomes
- [Go Coding Guidelines](/governance/go-coding-guidelines.md) — Error handling, context, events, store patterns

### Architecture

- [Domain Model](/architecture/domain-model.md) — Core entities and relationships
- [System Components](/architecture/components.md) — Runtime components and interactions
- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Step-graph execution model
- [Engine State Machine](/architecture/engine-state-machine.md) — Run, step, and branch state transitions
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Parallel execution model
- [Validation Service](/architecture/validation-service.md) — Cross-artifact validation rules
- [Error Handling](/architecture/error-handling-and-recovery.md) — Failure classification, retry, recovery
- [API Operations](/architecture/api-operations.md) — Operation semantics and governance rules
- [Observability](/architecture/observability.md) — Metrics, tracing, audit, permissions
- [Runtime Schema](/architecture/runtime-schema.md) — Database tables and indexes

---

## Philosophy

Most tools are actor-centric. They focus on people performing tasks.

Spine is artifact-centric. Work is defined through versioned intent. Execution derives from artifacts. Actors operate within governed workflows.

In a world where AI can generate enormous amounts of output, structure becomes the limiting reagent.

Spine provides that structure.

---

## Status

**All three initiatives complete.** 111 of 113 tasks done (98.2%).

### INIT-001 — Foundations (Completed)
Governance baseline, product definition, architecture v0.1, governance and architecture refinement.

### INIT-002 — Implementation (Completed)
Core foundation, artifact service, projection service, workflow engine, access gateway, validation service, actor gateway, divergence/convergence.

### INIT-003 — Execution System (Completed)
Execution core, actor delivery, workflow definitions, Git orchestration, evaluation outcomes, validation integration, execution reliability, divergence/convergence integration, event observability, developer experience, production wiring.

### Remaining Work
- Documentation alignment (in progress)
- Discussion and comments runtime (planned)
- Known limitations cleanup (WriteContext, idempotency, queue delivery)

See [Known Limitations](/KNOWN-LIMITATIONS.md) for details.
