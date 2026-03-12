# ADR-002: Event Model — Derived Domain Events and Operational Events

**Status:** Accepted
**Date:** 2026-03-09
**Decision Makers:** Spine Architecture

---

## Context

Spine is an artifact-centric system where durable truth is stored in Git as versioned artifacts.

During execution, many things "happen" in the system:

- artifacts change
- workflow runs progress
- actors perform actions
- runtime systems emit operational signals

The architecture must define:

1. What an **Event** represents
2. Whether events are **stored as primary truth**
3. How events relate to **Git commits and artifact history**

Without a clear model, events risk becoming either:

- an uncontrolled telemetry stream, or
- a competing source of truth with Git.

---

## Decision

### 1. Events are derived signals, not primary system records

Spine events are **derived representations of system activity**.

They are generated from:

- Git artifact changes
- workflow runtime execution
- system observability signals

Events **do not replace Git artifacts as the source of truth**.

Durable system state must always be representable or reconstructible from Git artifacts.

---

### 2. Two categories of events exist

#### Durable Domain Events

Domain events represent **meaningful system state transitions**.

Examples:

- `artifact_created`
- `artifact_updated`
- `artifact_superseded`
- `run_started`
- `run_completed`
- `run_failed`
- `workflow_definition_changed`

Characteristics:

- represent meaningful lifecycle changes
- may be derived from Git commits
- must be reconstructible from artifact history
- can be published to integrations or automation systems

Domain events **do not need to be stored in Git**, because they can be derived from artifact changes.

---

#### Operational Runtime Events

Operational events describe **runtime execution behavior**.

Examples:

- `step_started`
- `step_assigned`
- `retry_attempted`
- `worker_heartbeat`
- `queue_enqueued`
- `queue_dequeued`

Characteristics:

- support observability and debugging
- generated during execution
- may exist only in runtime systems (logs, queues, telemetry)

Operational events are **not durable system state**.

---

### 3. Git changes may trigger events

Changes to artifacts may produce domain events.

Example flow:

```
Git Commit → Artifact State Change → Domain Event Emitted
```

For example:

- artifact status changes → `artifact_updated`
- workflow definition updated → `workflow_definition_changed`
- run completion recorded → `run_completed`

Events act as **notifications or projections of durable change**, not as the authoritative record.

---

### 4. Events do not represent governed actions

Intentional actions such as:

- approvals
- rejections
- convergence selections
- governance decisions

are **not modeled as events**.

These represent **governed decisions made by actors**, and must be stored as durable artifact changes.

Events may be emitted after such actions occur, but the action itself is recorded in Git.

---

### 5. Events support integration and observability

Events exist primarily to support:

- workflow orchestration
- external integrations
- system observability
- automation triggers

They allow systems to react to meaningful changes without treating events as the authoritative record.

---

## Consequences

### Positive

- Git remains the single source of truth
- Event streams remain lightweight
- Operational telemetry does not pollute repository history
- System integrations can subscribe to domain events

### Negative

- Domain events must be derived from artifact changes
- Event ordering depends on commit history and runtime systems

---

## Future Work

Future ADRs may define:

- Decision / Approval model
- Comment and discussion structures
- Event delivery mechanisms
- Event schema standards
