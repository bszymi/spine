# Spine Domain Model

**Project:** Spine
**Version:** 0.1
**Status:** Living Document

---

## 1. Purpose

This document defines the core entities and relationships that form the domain model of the Spine system.

The domain model provides the shared vocabulary for architecture, implementation, and governance. All components, APIs, and data models must be expressible in terms of these entities.

---

## 2. Design Principles

The domain model is shaped by constitutional constraints:

- **Artifact-centric** — entities exist as versioned artifacts in Git (Constitution §2, §3)
- **Governed execution** — execution proceeds through defined workflows (Constitution §4)
- **Actor neutrality** — all actors operate under identical constraints (Constitution §5)
- **Controlled divergence** — parallel paths are explicit with preserved outcomes (Constitution §6)
- **Reproducibility** — all state is reconstructible from repository artifacts (Constitution §7)
- **Disposable runtime** — databases are projections, not sources of truth (Constitution §8)

---

## 3. Core Entities

### 3.1 Artifact

The fundamental unit of truth in Spine. A versioned Markdown document stored in Git that represents intent, definition, or outcome.

**Attributes:**

- `id` — stable, unique identifier (e.g. `INIT-001`, `EPIC-002`, `TASK-003`)
- `type` — artifact classification (Initiative, Epic, Task, ADR, Governance, etc.)
- `status` — lifecycle state (Pending, In Progress, Complete, Superseded)
- `path` — repository location
- `metadata` — structured fields (parent references, owner, version, dates)
- `content` — the body of the artifact

**Rules:**

- Every artifact must be versioned in Git
- Artifact IDs are immutable and never reused
- Artifacts are self-describing — they contain their own metadata
- Changes to artifacts produce Git commits (explicit, diffable history)

---

### 3.2 Workflow Definition

A versioned artifact that describes how a type of work progresses through states.

**Attributes:**

- `id` — stable identifier
- `name` — human-readable workflow name
- `states` — ordered set of valid states
- `transitions` — rules governing movement between states
- `steps` — ordered sequence of workflow steps
- `divergence_points` — where parallel execution may begin
- `convergence_points` — where parallel results are evaluated

**Rules:**

- Workflow definitions are versioned artifacts stored in Git
- All execution must conform to a workflow definition
- Execution paths not defined by a workflow are prohibited
- Workflow changes are versioned and auditable

---

### 3.3 Step

A single unit of work within a workflow. Steps define what must happen at each stage of execution.

**Attributes:**

- `id` — identifier within the workflow
- `name` — human-readable step name
- `type` — classification (manual, automated, review, convergence)
- `actor_type` — who may execute this step (human, AI, automated, any)
- `preconditions` — what must be true before the step can begin
- `required_inputs` — artifacts or data required to execute
- `required_outputs` — artifacts or data that must be produced
- `validation` — conditions that must be met for the step to succeed
- `retry_limit` — maximum attempts for automated steps
- `timeout` — maximum duration before escalation

**Rules:**

- Every step must produce or reference a versioned artifact
- Steps cannot be skipped unless the workflow definition permits it
- Automated steps must declare retry limits

---

### 3.4 Actor

An entity that executes workflow steps. Actors are interchangeable — the system does not distinguish between human and AI execution at the governance level.

**Attributes:**

- `id` — stable identifier
- `type` — classification (human, ai_agent, automated_system)
- `name` — human-readable name
- `capabilities` — what types of steps this actor can perform
- `permissions` — what artifacts and workflows the actor may access

**Rules:**

- All actors operate under identical workflow constraints
- No actor has implicit authority — authority comes from workflow definitions
- AI agents are execution participants, not decision authorities
- Actors cannot mutate artifacts outside workflow definitions

---

### 3.5 Run

A single execution instance of a workflow. A Run tracks the progress of work through a workflow from start to completion.

**Attributes:**

- `id` — unique execution identifier
- `workflow_id` — reference to the governing workflow definition
- `status` — execution state (pending, active, completed, failed, cancelled)
- `current_step` — which step is currently active
- `actor_assignments` — which actor is assigned to each step
- `started_at` — when execution began
- `completed_at` — when execution finished
- `trace_id` — correlation identifier for observability
- `artifacts_produced` — list of artifacts created during execution
- `artifacts_consumed` — list of artifacts used as inputs

**Rules:**

- Every Run is governed by a Workflow Definition
- Runs produce audit records as versioned artifacts
- A Run's execution path must be reconstructible from its artifact trail
- Failed Runs preserve their state for diagnosis

---

### 3.6 Projection

A runtime representation of repository artifact state, stored in a database for query performance. Projections are derived from Git and are disposable.

**Attributes:**

- `source_artifact` — reference to the Git artifact being projected
- `projection_type` — what kind of view this represents
- `last_synced_commit` — the Git commit this projection reflects
- `data` — the projected state

**Rules:**

- Projections are derived from Git artifacts — they are not sources of truth
- If projections are lost, they must be fully reconstructible from Git
- Projections may be stale — consumers must understand eventual consistency
- Projections must never be written back to Git as truth

---

### 3.7 Event

A record of something that happened within the system — a state change, an action taken, or a decision made.

**Attributes:**

- `id` — unique event identifier
- `type` — event classification (artifact_created, step_completed, run_started, etc.)
- `timestamp` — when the event occurred
- `actor_id` — who or what caused the event
- `run_id` — associated Run, if applicable
- `artifact_id` — associated Artifact, if applicable
- `payload` — event-specific data

**Rules:**

- Events are immutable once recorded
- Events provide the audit trail for reproducibility
- Durable events must be reconstructible from Git artifact history
- Runtime events may exist ephemerally in queues for operational purposes

---

## 4. Entity Relationships

```
Initiative (Artifact)
└── Epic (Artifact)
    └── Task (Artifact)
        └── Run
            ├── governed by → Workflow Definition (Artifact)
            ├── executed by → Actor
            ├── follows → Step sequence
            ├── produces → Artifact(s)
            └── emits → Event(s)

Projection ← derived from ← Artifact (via Git)

ADR (Artifact) — standalone, linked to related artifacts
```

### Key Relationships:

| From | To | Relationship |
|------|-----|-------------|
| Initiative | Epic | contains (1:many) |
| Epic | Task | contains (1:many) |
| Task | Run | triggers (1:many) |
| Run | Workflow Definition | governed by (many:1) |
| Run | Step | progresses through (1:many) |
| Run | Actor | executed by (many:many) |
| Run | Artifact | produces (1:many) |
| Run | Event | emits (1:many) |
| Projection | Artifact | derived from (many:1) |
| Artifact | Artifact | references (many:many) |

---

## 5. Entity Lifecycles

### 5.1 Artifact Lifecycle

```
Pending → In Progress → Complete
                      → Superseded (links to successor)
```

### 5.2 Run Lifecycle

```
Pending → Active → Completed
                 → Failed (preserves state)
                 → Cancelled
```

### 5.3 Step Lifecycle (within a Run)

```
Waiting → Assigned → In Progress → Completed
                                 → Failed (may retry up to limit)
                                 → Skipped (if workflow permits)
```

---

## 6. Divergence and Convergence

When a workflow introduces controlled divergence:

```
Step A → Divergence Point → Step B1 (Actor 1)
                          → Step B2 (Actor 2)
                          → Convergence Point → Step C (Reviewer)
```

- Divergence creates parallel Runs or parallel Steps within a Run
- Each path produces its own artifacts
- Convergence evaluates all outcomes and selects or synthesizes a result
- All outcomes — selected and rejected — are preserved as artifacts

---

## 7. Constitutional Alignment

| Entity | Constitutional Principle | How It Aligns |
|--------|------------------------|---------------|
| Artifact | Source of Truth (§2) | All truth lives in Git as artifacts |
| Workflow Definition | Governed Execution (§4) | Execution paths are explicit and enforceable |
| Step | Explicit Intent (§3) | Every action requires a governing definition |
| Actor | Actor Neutrality (§5) | Uniform interface, no implicit authority |
| Run | Reproducibility (§7) | Execution paths reconstructible from artifacts |
| Projection | Disposable Database (§8) | Runtime state is derived, not authoritative |
| Event | Reproducibility (§7) | Audit trail supports reconstruction |

---

## 8. Evolution Policy

This domain model is expected to evolve as architecture decisions are made and implementation begins.

New entities may be introduced. Existing entities may gain attributes. Changes must be versioned in Git and must not violate constitutional principles.

Changes that alter fundamental entity relationships should be captured as ADRs.
