---
type: Architecture
title: Spine System Components
status: Living Document
version: "0.1"
---

# Spine System Components

---

## 1. Purpose

This document defines the system components and services that make up the Spine runtime. It describes what each component does, what it owns, and how components interact.

All components must operate within the constraints defined by the [Constitution](/governance/constitution.md) and the boundaries defined in [Product Boundaries](/product/boundaries-and-constraints.md).

---

## 2. Design Principles

The component architecture follows from constitutional constraints:

- **Git is the source of truth** — all components that manage durable state must read from and write to Git. Runtime databases are projections.
- **Disposable runtime** — if all runtime infrastructure is lost, the system must be reconstructible from Git alone.
- **Governed execution** — the workflow engine enforces constraints; components do not bypass governance.
- **Actor neutrality** — the actor interface is uniform; no component has special paths for human vs AI execution.
- **Separation of durable and operational concerns** — durable outcomes go to Git; operational state lives in runtime systems.

---

## 3. Component Overview

```
┌─────────────────────────────────────────────────────┐
│                     Access Gateway                   │
│              (external access boundary)              │
└──────────┬──────────────────────────┬────────────────┘
           │                          │
           ▼                          ▼
┌─────────────────────┐   ┌─────────────────────────┐
│   Artifact Service   │   │    Workflow Engine       │
│  (Git read/write)    │   │  (execution governance)  │
└──────────┬──────────┘   └──────┬──────────┬────────┘
           │                     │          │
           ▼                     ▼          ▼
┌─────────────────────┐   ┌──────────┐ ┌──────────────┐
│  Projection Service  │   │  Queue   │ │ Actor Gateway │
│  (Git → DB sync)     │   │          │ │              │
└──────────┬──────────┘   └──────────┘ └──────────────┘
           │
           ▼
┌─────────────────────┐
│   Projection Store   │
│   (query database)   │
└─────────────────────┘
```

---

## 4. Components

### 4.1 Access Gateway

The external access boundary for all interactions with Spine.

The Access Gateway provides a unified entry layer for different connection modes such as HTTP APIs, CLI clients, graphical interfaces, automation tools, and future protocols. Regardless of transport, all requests enter Spine through this boundary and are normalized into a common internal request model.

**Responsibilities:**

- Accept and route requests from humans, AI agents, automated systems, and external tools
- Authenticate and authorize actors
- Expose a uniform interface regardless of actor type
- Route requests to the appropriate internal service

**Does not own:**

- Business logic or governance rules
- Artifact storage
- Workflow state

**Interactions:**

- Forwards artifact operations to the Artifact Service
- Forwards execution operations to the Workflow Engine
- Reads query data from the Projection Store

**Notes:**

- The Access Gateway is a logical boundary, not a commitment to a single transport protocol.
- Different access modes (API, CLI, GUI, MCP, or other integrations) may be implemented as adapters that connect through this gateway.

---

### 4.2 Artifact Service

Manages all read and write operations on Git-backed artifacts.

**Responsibilities:**

- Read artifacts from the Git repository
- Write artifact changes to Git (create, update, transition status)
- Validate artifact structure against schemas defined in [Artifact Schema](/governance/artifact-schema.md)
- Validate artifact front matter and linkage
- Enforce immutability rules (IDs never reused, history never rewritten)
- Manage task and divergence branches during workflow execution
- Perform all merges into the authoritative branch (sole merge authority for governed work)
- Emit domain events when artifacts change (artifact_created, artifact_updated, etc.)

For the full Git operational contract (authentication, commit format, branch strategy, merge rules), see [Git Integration](/architecture/git-integration.md).

**Does not own:**

- Workflow logic or execution state
- Query-optimized views (those belong to the Projection Service)
- Actor management

**Interactions:**

- Called by the API Gateway for direct artifact operations
- Called by the Workflow Engine when execution produces durable artifact changes
- Emits domain events consumed by the Projection Service and Event Router

**Constitutional alignment:**

- Source of Truth (§2) — Git is the authoritative store
- Explicit Intent (§3) — artifacts are validated before persistence
- Reproducibility (§7) — all changes produce Git commits

---

### 4.3 Workflow Engine

Interprets workflow definitions and governs execution.

**Responsibilities:**

- Load workflow definitions from Git (via Artifact Service or Projection Store)
- Resolve workflow binding for artifacts using `(type, work_type)` resolution (per [Binding Model](/architecture/task-workflow-binding.md))
- Create and manage Runs
- Enforce state transitions, preconditions, and validation rules
- Assign steps to actors based on workflow definitions
- Track step execution progress and runtime step outcomes
- Handle retries, timeouts, and failure states
- Manage divergence (parallel steps) and convergence (evaluation steps)
- Determine when durable outcomes must be committed to Git

**Does not own:**

- Artifact storage (delegates to Artifact Service)
- Actor execution (delegates to actors via Actor Gateway)
- Long-term query state (delegates to Projection Service)

**Interactions:**

- Reads workflow definitions from Git (via Artifact Service or projections)
- Calls Artifact Service to commit durable outcomes
- Sends step assignments to actors via Actor Gateway
- Publishes execution events to the Queue / Event Router
- Reads and writes runtime execution state from its own operational store

**Constitutional alignment:**

- Governed Execution (§4) — enforces workflow constraints
- Controlled Divergence (§6) — manages parallel execution and convergence
- Reproducibility (§7) — ensures durable outcomes are committed

**Runtime state:**

The Workflow Engine maintains operational execution state (current step, actor assignments, retry counts) in a Runtime Store.

The Runtime Store is an operational component of the Spine Runtime and is not a source of truth. If lost, in‑progress runs may need to be restarted, but all durable outcomes remain preserved in Git.

---

### 4.4 Projection Service

Synchronizes Git artifact state into a query-optimized database.

**Responsibilities:**

- Watch for Git changes (via events or polling)
- Parse artifact front matter and content
- Build and maintain projection records in the Projection Store
- Track which Git commit each projection reflects
- Support full reconstruction from Git if the Projection Store is lost

**Does not own:**

- Authoritative artifact state (that lives in Git)
- Workflow execution logic
- Event routing

**Interactions:**

- Reads artifacts from Git (via Artifact Service or directly)
- Writes projections to the Projection Store
- Consumes domain events from the Event Router to trigger sync

**Constitutional alignment:**

- Disposable Database (§8) — projections are derived and reconstructible
- Source of Truth (§2) — projections never override Git

---

### 4.5 Projection Store

The query-optimized database that holds projected artifact state.

**Responsibilities:**

- Store projection records for fast querying
- Support queries by artifact type, status, linkage, and metadata fields
- Record the Git commit each projection was derived from

**Does not own:**

- Authoritative state (it is a disposable projection)
- Write access to Git

**Interactions:**

- Written to by the Projection Service
- Read by the API Gateway and Workflow Engine for queries

**Note:** The Projection Store is not a distinct service — it is a database used by the Projection Service. It is listed separately to make the data ownership boundary explicit.

---

### 4.6 Actor Gateway

Provides a uniform interface for assigning and receiving work from actors.

**Responsibilities:**

- Deliver step assignments to actors (human, AI, automated)
- Receive step results from actors
- Enforce that all actor interactions pass through governed workflows
- Abstract actor type differences behind a uniform interface

**Does not own:**

- Workflow logic (that belongs to the Workflow Engine)
- Actor intelligence or decision-making
- Artifact storage

**Interactions:**

- Receives step assignments from the Workflow Engine
- Returns step results to the Workflow Engine
- May integrate with external systems (LLM providers, CI/CD, human task interfaces)

For the concrete gateway protocol (assignment request/response schemas, delivery mechanisms, and response validation), see [Actor Model](/architecture/actor-model.md) §5.

**Constitutional alignment:**

- Actor Neutrality (§5) — uniform interface for all actor types

---

### 4.7 Event Router

Routes domain and operational events between components.

**Responsibilities:**

- Receive events from producers (Artifact Service, Workflow Engine)
- Route events to consumers (Projection Service, external integrations, observability)
- Support both domain events (artifact_created, run_completed) and operational events (step_started, retry_attempted)
- Provide at-least-once delivery for domain events

**Does not own:**

- Event interpretation or business logic
- Durable event storage (events are derived signals per ADR-002)

**Interactions:**

- Receives events from Artifact Service and Workflow Engine
- Delivers events to Projection Service, external integrations, and observability systems

**Note:** The Event Router may be implemented as a message queue, event bus, or pub/sub system. The architectural requirement is that events flow between components without tight coupling.

---

### 4.8 Validation Service

Performs cross-artifact validation during workflow progression.

**Responsibilities:**

- Execute cross-artifact consistency checks as required by Constitution §11
- Compare artifacts against governed context (Charter, Constitution, Architecture, Product, implementation)
- Classify mismatches (scope conflict, architectural conflict, implementation drift, missing prerequisite)
- Return validation results to the Workflow Engine for governance decisions

**Does not own:**

- Artifact storage
- Workflow progression decisions (those belong to the Workflow Engine based on validation results)
- Mismatch resolution (that requires actor action)

**Interactions:**

- Called by the Workflow Engine during validation steps
- Reads artifacts from the Projection Store or Artifact Service
- Returns validation results (pass, fail with classification)

For concrete validation rules, mismatch classifications, and the validation contract, see [Validation Service Specification](/architecture/validation-service.md).

**Constitutional alignment:**

- Cross-Artifact Validation (§11) — validation is a governed workflow activity
- Structural Integrity (§12) — ensures layers remain consistent

---

## 5. Component Interactions

### 5.1 Artifact Creation Flow

```
Actor → API Gateway → Workflow Engine (validates step) → Artifact Service (writes to Git) → Event Router → Projection Service (updates projections)
```

### 5.2 Query Flow

```
Actor → API Gateway → Projection Store (reads projected state)
```

### 5.3 Workflow Execution Flow

```
Workflow Engine → Actor Gateway (assigns step) → Actor (executes) → Actor Gateway (returns result) → Workflow Engine (evaluates outcome) → Artifact Service (commits durable outcome if needed)
```

### 5.4 Validation Flow

```
Workflow Engine → Validation Service (cross-artifact check) → Workflow Engine (decides based on result)
```

### 5.5 Projection Rebuild Flow

```
Projection Service → Artifact Service (reads all artifacts from Git) → Projection Store (rebuilds all projections)
```

---

## 6. Deployment Considerations (v0.x)

### 6.1 Minimal Deployment

For v0.x, the system may be deployed as a single process with components running as in-process modules rather than separate services. The architectural boundaries remain the same, but network overhead is eliminated.

### 6.2 Required Infrastructure

- **Git repository** — the authoritative artifact store (hosted on GitHub, GitLab, etc.)
- **Database** — for projection storage (PostgreSQL or similar)
- **Queue** — for event routing (may be in-process for v0.x)

### 6.3 Optional Infrastructure

- **External LLM providers** — for AI actor execution
- **CI/CD systems** — for automated actor execution
- **Notification systems** — for human actor step delivery

### 6.4 Scaling Path

Components are designed with clear boundaries so they can be extracted into separate services as load requires. The Projection Service and Event Router are the most likely candidates for early extraction.

---

## 7. Constitutional Alignment Summary

| Component | Primary Constitutional Principle |
|-----------|-------------------------------|
| Artifact Service | Source of Truth (§2), Explicit Intent (§3) |
| Workflow Engine | Governed Execution (§4), Controlled Divergence (§6) |
| Projection Service | Disposable Database (§8) |
| Actor Gateway | Actor Neutrality (§5) |
| Event Router | Observability support (ADR-002) |
| Validation Service | Cross-Artifact Validation (§11), Structural Integrity (§12) |

---

## 8. Evolution Policy

This component architecture is expected to evolve as implementation progresses and operational experience is gained.

New components may be introduced. Existing components may be split or merged. Changes must be versioned in Git and must not violate constitutional principles.

Changes that alter fundamental component boundaries or responsibilities should be captured as ADRs.
