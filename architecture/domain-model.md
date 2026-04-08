---
type: Architecture
title: Spine Domain Model
status: Living Document
version: "0.1"
---

# Spine Domain Model

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

All durable domain objects in Spine are represented as Artifacts or as runtime entities that operate on Artifacts.

### 3.1 Artifact

The fundamental unit of truth in Spine. A versioned Markdown document stored in Git that represents intent, definition, or outcome.


Artifact is the abstract root entity for all durable Spine records. Specific artifact types (such as Initiative, Epic, Task, ADR, Governance, etc.) are classifications of Artifact that may introduce additional conventions, metadata, or workflow behavior. Unless explicitly defined otherwise, capabilities described for Artifact apply to all artifact types.

Each artifact type is expected to define or inherit a schema/template that constrains its metadata and content structure. These schemas may be introduced incrementally over time, but the model assumes that artifact type is not merely a label — it is the basis for validation, rendering, and governed evolution.

**Attributes:**

- `id` — stable identifier unique within its governed scope (e.g. `INIT-001`, `EPIC-002`, `TASK-003`)
- `type` — artifact classification (Initiative, Epic, Task, ADR, Governance, etc.)
- `status` — lifecycle state (Pending, In Progress, Completed, Superseded)
- `path` — repository location
- `metadata` — structured fields stored in Markdown front matter (for example YAML) containing parent references, owner, version, dates, linkage data, and other machine-readable artifact attributes
- `content` — the body of the artifact

**Rules:**

- Every artifact must be versioned in Git
- Artifact IDs are immutable and never reused
- Artifacts are self-describing — they contain their own metadata
- Changes to artifacts produce Git commits (explicit, diffable history)

**ID Allocation:**

- Sequential IDs are allocated by scanning the parent directory for existing artifacts of the same type and incrementing the highest number found
- IDs are zero-padded per naming conventions (3 digits for Task/Epic/Initiative, 4 digits for ADR)
- Gaps are preserved — if TASK-001 and TASK-003 exist, the next is TASK-004, not TASK-002
- Follow-up IDs (900-series) are excluded from regular allocation
- Document types (Governance, Architecture, Product) use descriptive slugs instead of sequential IDs
- Collision detection at merge time: if two planning runs allocate the same ID, the second is renumbered automatically

Artifacts may include structured linkage information describing relationships to other artifacts. Linkage is intentionally general rather than limited to parent/child hierarchy so the model can express dependencies, follow-up work, related scope, blocking relationships, and other governed connections.

Artifact linkage is stored explicitly in artifact metadata (typically the Markdown front-matter block) so that both humans and automated agents can reliably discover relationships. All structured links for an artifact should appear together in this metadata block.

Artifacts may also include simple references to other artifacts. References are informational pointers intended to help readers navigate related material and do not represent governed relationships. Unlike typed links, references are not required to maintain bidirectional consistency and are not interpreted as workflow or dependency semantics.

Linkage is defined at the Artifact level so that any artifact may relate to any other artifact when appropriate. Specific artifact types may define conventions for particular link types (for example: Epic contains Tasks, Task blocks Task, ADR relates_to Architecture Document), but the core model does not restrict linkage to specific artifact types.

For relationships that have meaningful inverse semantics (for example blocked_by ↔ blocks or supersedes ↔ superseded_by), both artifacts may store the corresponding link entries so that each artifact remains self-describing when read directly. Tooling should validate that such bidirectional relationships remain consistent.

Link targets must use globally unambiguous references rather than relying on locally scoped identifiers alone. In practice, this means linkage should point to a full artifact reference (for example via canonical path, fully qualified hierarchical identifier, or another globally resolvable reference) rather than only a short local ID such as TASK-042.

---

### 3.2 Workflow Definition

A versioned artifact that describes how a type of work progresses through steps toward a terminal outcome.

**Attributes:**

- `id` — stable identifier
- `name` — human-readable workflow name
- `version` — semantic version of this definition
- `status` — lifecycle status (Active, Deprecated, Superseded)
- `description` — what this workflow governs
- `applies_to` — artifact types this workflow governs
- `entry_step` — step where execution begins
- `steps` — ordered sequence of workflow steps
- `divergence_points` — where parallel execution may begin
- `convergence_points` — where parallel results are evaluated

**Rules:**

- Workflow definitions are versioned artifacts stored in Git
- All execution must conform to a workflow definition
- Execution paths not defined by a workflow are prohibited
- Workflow changes are versioned and auditable
- Runtime systems may maintain database projections of workflow definitions for execution efficiency, but Git remains the authoritative source.

---

### 3.3 Step Definition

A configuration element within a Workflow Definition that specifies what must happen at a particular stage of execution. Step Definitions describe the intended structure of work, not the runtime execution itself.

**Attributes:**

- `id` — identifier within the workflow definition
- `name` — human-readable step name
- `type` — classification (manual, automated, review, convergence)
- `execution` — execution constraints (mode, eligible actor types, required skills)
- `preconditions` — what must be true before the step can begin
- `required_inputs` — artifacts or data required to execute
- `required_outputs` — artifacts or data that must be produced
- `validation` — conditions checked before accepting result
- `outcomes` — possible results, each routing to a next step or terminating the Run, with optional durable artifact mutations
- `retry` — retry configuration (limit and backoff strategy)
- `timeout` — maximum duration before escalation
- `timeout_outcome` — which outcome to apply on timeout

**Rules:**

- Step Definitions belong to a Workflow Definition and are not independent top-level entities
- Steps must either produce/reference a versioned artifact or produce a runtime step outcome that controls workflow progression
- Steps cannot be skipped unless the workflow definition permits it
- Automated steps must declare retry limits

At runtime, Step Definitions manifest as step executions within a Run. A step execution may occur multiple times due to retries or reassignment, but only its durable outcome (such as produced artifacts or governance decisions) must be persisted in Git.

---

### 3.4 Actor

An entity that executes workflow steps. Actors are interchangeable — the system does not distinguish between human and AI execution at the governance level.

**Attributes:**

- `id` — stable identifier
- `type` — classification (human, ai_agent, automated_system)
- `name` — human-readable name
- `skills` — what types of steps this actor can perform, managed through the Skill registry (see §3.4.1 and [Actor Model](/architecture/actor-model.md) §3.1)
- `permissions` — what artifacts and workflows the actor may access (derived from role, see [Security Model](/architecture/security-model.md) §4.1)

**Rules:**

- All actors operate under identical workflow constraints
- No actor has implicit authority — authority comes from workflow definitions
- AI agents are execution participants, not decision authorities
- Actors cannot mutate artifacts outside workflow definitions

---

### 3.4.1 Skill

A workspace-scoped skill entity that formalizes the skill matching system. Instead of treating skills as opaque strings, skills are first-class entities with metadata and lifecycle.

**Attributes:**

- `skill_id` — unique identifier (UUID)
- `name` — unique within workspace, used for matching against `required_skills`
- `description` — human-readable explanation of the skill
- `category` — grouping (e.g. "development", "review", "operations")
- `status` — lifecycle state (active, deprecated)

**Relationships:**

- Skills are assigned to Actors (many-to-many)
- Workflow steps declare required skills that resolve against the skill registry
- Skills are workspace-scoped — no cross-workspace visibility

---

### 3.5 Run

A single execution instance of a workflow associated with a Task artifact. A Run tracks the progress of work through a workflow from start to completion.

A Run represents the runtime execution governed by a specific version of a Workflow Definition. During execution, Step Definitions manifest as runtime step executions tracked within the Run.

Runs are initiated by Tasks and represent the execution of work required to complete that Task.

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
- Runs record durable execution outcomes as versioned artifacts in Git
- Not every runtime step transition must produce a Git commit; only durable outcomes are committed
- A Run's execution path must be reconstructible from its artifact trail
- Failed Runs preserve their state for diagnosis

Runtime execution may generate many operational events (step start, retries, assignments, telemetry). These events are not required to be persisted in Git. However, any durable outcome that affects artifact state, workflow governance, or execution traceability must be represented directly in Git artifacts or be derivable from Git history.

Step executions within a Run produce runtime step outcomes that determine workflow progression. The set of possible outcomes is defined by the governing Workflow Definition rather than fixed globally. These outcomes are part of execution state rather than standalone domain entities. Only outcomes that produce or modify artifacts create durable Git history.

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
- Projections should record the repository commit or revision they reflect so consumers can reason about freshness and reconstruction.

---

### 3.7 Event

A derived signal representing something that happened within the system. Events describe changes or runtime activity but are not the authoritative source of system truth.

Events are produced from either Git artifact changes or runtime execution activity. They exist primarily for observability, integration, and automation. Durable system state must always be represented in Git artifacts rather than relying on events themselves.

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
- Events may contribute to the audit trail for observability and integration, but authoritative history comes from Git artifact changes
- Durable events must be reconstructible from Git artifact history
- Runtime events may exist ephemerally in queues for operational purposes
- Events do not represent governed decisions such as approvals or rejections; those must be recorded as durable artifact changes

Operational runtime events (such as step_started, retry_attempted, or worker_heartbeat) may exist only in runtime systems such as queues, logs, or telemetry streams. These events support execution observability but are not considered durable system state.

Events may be categorized into two conceptual groups: durable domain events derived from artifact history (e.g., artifact_updated, run_completed) and operational runtime events generated during execution (e.g., step_started, retry_attempted). Both categories are derived signals rather than primary records of truth.

Events must not be used to represent approval or rejection decisions; such outcomes are represented either as runtime step outcomes within Runs or as durable acceptance state within Task artifacts.

---

## 4. Entity Relationships

The following hierarchy represents a common organizational convention rather than a mandatory structure.

```
Initiative (Artifact)
└── Epic (Artifact)
    └── Task (Artifact)
        └── Run
            ├── governed by → Workflow Definition (Artifact)
            ├── executed by → Actor
            ├── follows → Step Definition sequence (from Workflow Definition)
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
| Run | Step Definition | executes according to (1:many, via workflow definition) |
| Run | Actor | executed by (many:many) |
| Run | Artifact | produces (1:many) |
| Run | Event | emits (1:many) |
| Projection | Artifact | derived from (many:1) |
| Artifact | Artifact | references (many:many) |
| Artifact | Artifact | linked_to (many:many, typed relationship) |
| Task | Task | blocked_by (many:many, via `blocked_by` link type) |

---

## 5. Entity Lifecycles

### 5.1 Artifact Lifecycle

```
Pending → In Progress → Completed
                      → Superseded (may link to related successor or replacement work)
```

Artifacts may optionally include governed acceptance or sign-off information recorded in the artifact itself.

For Task artifacts, this commonly represents whether the deliverable was approved, rejected with follow-up required, or rejected and closed. Other artifact types (such as ADRs or governance documents) may also record acceptance or approval metadata when appropriate. These outcomes are durable artifact state rather than workflow step results.

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

- Divergence typically creates parallel Steps within a single Run
- Each path produces its own artifacts
- Convergence evaluates all outcomes and selects or synthesizes a result
- All outcomes — selected and rejected — are preserved as artifacts

Note: In most cases divergence occurs as parallel step executions within a single Run. Creating multiple Runs for the same Task generally represents retrying or restarting execution after failure rather than planned divergence. If future architectural decisions require alternative representations, they should be captured as ADRs.

---

## 7. Constitutional Alignment

| Entity | Constitutional Principle | How It Aligns |
|--------|------------------------|---------------|
| Artifact | Source of Truth (§2) | All truth lives in Git as artifacts |
| Workflow Definition | Governed Execution (§4) | Execution paths are explicit and enforceable |
| Step Definition | Explicit Intent (§3) | Every action requires a governing definition |
| Actor | Actor Neutrality (§5) | Uniform interface, no implicit authority |
| Run | Reproducibility (§7) | Execution paths and final outcomes reconstructible from artifacts |
| Projection | Disposable Database (§8) | Runtime state is derived, not authoritative |
| Event | Observability Support | Derived signals allow systems to react to artifact and execution changes |

---

## 8. Evolution Policy

This domain model is expected to evolve as architecture decisions are made and implementation begins.

New entities may be introduced. Existing entities may gain attributes. Changes must be versioned in Git and must not violate constitutional principles.

The exact taxonomy of typed artifact links (for example: blocks, blocked_by, follow_up_to, related_to, supersedes, replaced_by) may evolve over time. The model requires only that links are explicit, typed, and stored durably in the governing artifact.

Artifacts should remain self-describing when viewed directly in their Markdown form. Structured metadata such as links should therefore be stored in the artifact itself rather than relying solely on derived projections. Tooling may validate and assist with maintaining bidirectional link consistency across artifacts.

The exact representation of artifact references and link targets may also evolve. Whatever representation is chosen, it must remain stable, globally resolvable, and suitable for durable storage inside artifacts.

Changes that alter fundamental entity relationships should be captured as ADRs.
