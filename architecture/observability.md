---
type: Architecture
title: Observability and Audit Model
status: Living Document
version: "0.1"
---

# Observability and Audit Model

---

## 1. Purpose

This document defines the minimum observability and audit model for Spine at v0.x.

The [Constitution](/governance/constitution.md) (§7) mandates that execution paths must be reconstructible from repository state, outcomes must be traceable to governing artifacts, and reproducibility is mandatory. This document specifies how the system achieves traceability, how operators monitor execution, and how the audit trail is constructed.

Spine's observability model builds on existing architectural primitives:

- **Git history** — the authoritative, durable audit trail for all governed outcomes
- **Events** — derived signals for real-time observability (per [ADR-002](/architecture/adr/ADR-002-events.md) and [Event Schemas](/architecture/event-schemas.md))
- **Runtime state** — operational execution data in the Runtime Store (per [Data Model](/architecture/data-model.md))

This document ties these together into a coherent observability strategy.

---

## 2. Observability Layers

Spine observability operates across three layers with different durability guarantees:

| Layer | Durability | Content | Use |
|-------|-----------|---------|-----|
| Git history | Permanent | Artifact changes, governance decisions, durable outcomes | Audit trail, compliance, reconstruction |
| Runtime Store | Operational | Run state, step executions, error details | Execution monitoring, debugging |
| Event stream | Transient | Domain and operational events | Real-time monitoring, alerting, integration |

**Key principle:** The audit trail for governed outcomes is always reconstructible from Git alone. Runtime and event layers enhance observability but are not required for auditability.

---

## 3. Trace ID Strategy

### 3.1 Purpose

The `trace_id` is a correlation identifier that links all activity related to a single execution context. It enables end-to-end tracing across components, steps, actors, and events.

### 3.2 Generation

- A `trace_id` is generated when a Run is created
- The trace ID is a UUID v4 string
- The trace ID is stored on the Run record in the Runtime Store (per [Data Model](/architecture/data-model.md) §2.3)

### 3.3 Propagation

The trace ID propagates through the execution lifecycle:

```
Run created (trace_id generated)
  → Step assignments carry trace_id
  → Actor Gateway includes trace_id in actor requests
  → Actor responses include trace_id
  → Step outcomes carry trace_id
  → Events emitted include run_id (which maps to trace_id)
  → Durable outcome commits include trace_id in commit metadata
```

**Propagation rules:**

- All operational events emitted during a Run must include the `run_id`, which maps to the `trace_id`
- All step execution records include `run_id` as a foreign key
- Actor Gateway requests must include the trace ID so that external actor systems (LLM providers, CI/CD) can correlate their own logs
- Git commits for durable outcomes should include the trace ID in the commit message or a structured trailer

### 3.4 Cross-Run Tracing

When a task has multiple Runs (e.g., after failure and restart), each Run has its own trace ID. The task's artifact path serves as the correlation key across Runs:

```
Task artifact path → all Runs for this task → each Run's trace_id → all activity within that Run
```

### 3.5 Divergence Tracing

During divergence, the trace ID applies to the entire Run. Branch-specific tracing uses the `branch_id` field on events and step executions. The combination of `trace_id` + `branch_id` uniquely identifies activity within a specific branch of a specific Run.

---

## 4. Audit Trail

### 4.1 Durable Audit Trail (Git)

The authoritative audit trail is Git commit history. For any governed outcome, an auditor can answer:

- **What changed?** — diff the commit
- **When?** — commit timestamp
- **Who?** — commit author (maps to actor)
- **Why?** — commit message, linked workflow definition, task acceptance criteria
- **Under what governance?** — workflow definition pinned at Run creation (Git SHA)

This audit trail is permanent, immutable (commits are never rewritten), and self-contained.

### 4.2 What the Git Audit Trail Contains

- Artifact creation and status changes
- Acceptance and rejection decisions
- Convergence results (selected branch, evaluation rationale)
- Workflow definition changes
- Governance document changes

### 4.3 What the Git Audit Trail Does Not Contain

- Step-level execution details (which actor, how many retries, how long)
- Runtime operational activity
- Failed Runs that produced no durable outcomes
- Actor assignment history

These details exist in the Runtime Store and event stream for operational purposes but are not part of the permanent audit trail.

### 4.4 Runtime Audit Trail

The Runtime Store provides a supplementary audit trail for execution details:

- Step execution records with actor assignments, attempt counts, durations, and error details
- Run history with status transitions and timing
- Divergence context with branch status and convergence results

This audit trail has limited durability — if the Runtime Store is lost, execution details are lost (per [Data Model](/architecture/data-model.md) §5.2). However, all governed outcomes remain in Git.

### 4.5 Audit Reconstruction

To reconstruct the full execution history for a task:

1. **Git history** — read the task artifact's commit log for all governed state changes
2. **Runtime Store** — query Runs by `task_path` for execution details
3. **Event stream** — if retained, query events by `run_id` for fine-grained activity

If only Git is available, the governed outcome history is complete. Execution details (steps, retries, timing) require the Runtime Store.

---

## 5. Logging Model

### 5.1 Structured Logging

All Spine components must emit structured logs (JSON format) with the following standard fields:

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | ISO 8601 | When the log entry was produced |
| `level` | enum | `debug`, `info`, `warn`, `error` |
| `component` | string | Source component (e.g., `workflow_engine`, `artifact_service`) |
| `message` | string | Human-readable log message |
| `run_id` | string (nullable) | Associated Run for correlation |
| `trace_id` | string (nullable) | Trace ID for end-to-end correlation |
| `step_id` | string (nullable) | Associated step (if applicable) |
| `actor_id` | string (nullable) | Associated actor (if applicable) |
| `artifact_path` | string (nullable) | Associated artifact (if applicable) |
| `error` | object (nullable) | Structured error information |

### 5.2 Log Levels

| Level | Use |
|-------|-----|
| `debug` | Detailed execution information for development and troubleshooting |
| `info` | Normal operational activity (Run started, step completed, artifact committed) |
| `warn` | Degraded operation (retry triggered, timeout approaching, stale projection) |
| `error` | Failure requiring attention (step failed permanently, Git commit failed, orphaned Run detected) |

### 5.3 Log Retention

Log retention is operator-configured. Spine does not define retention policies. Logs are operational data, not governed artifacts — they may be rotated, compressed, or discarded based on operational needs.

### 5.4 Logs vs Events

Logs and events serve different purposes:

| Concern | Logs | Events |
|---------|------|--------|
| Audience | Operators, developers | System components, integrations |
| Format | Free-form structured text | Typed schemas with payloads |
| Delivery | Written locally | Routed via Event Router |
| Retention | Operator-configured | Transient (domain events reconstructible from Git) |
| Purpose | Debugging, troubleshooting | Coordination, observability, integration |

Components emit both logs and events. A step failure produces a log entry (for debugging) and a `step_failed` event (for the Event Router and consumers).

---

## 6. Run History

### 6.1 Run History Model

Run history exists in the Runtime Store as a queryable record of execution:

- All Runs for a given task (by `task_path`)
- Step execution timeline (ordered by `started_at`)
- Actor assignment history
- Retry and failure records
- Divergence and convergence details

### 6.2 Run History Queries

The system should support the following queries for operational and audit purposes:

| Query | Source |
|-------|--------|
| All Runs for a task | Runtime Store (by `task_path`) |
| Current status of a Run | Runtime Store (by `run_id`) |
| Step execution history for a Run | Runtime Store (step_executions by `run_id`) |
| Failed steps with error details | Runtime Store (step_executions where status = `failed`) |
| Active Runs across all tasks | Runtime Store (runs where status = `active`) |
| Governed outcome history for a task | Git history (commits touching the task artifact) |

### 6.3 Run History Durability

Run history in the Runtime Store is operational and disposable (per [Data Model](/architecture/data-model.md) §2.3). If the Runtime Store is lost:

- Governed outcomes remain in Git
- Execution details for completed Runs can be partially inferred from Git (e.g., a `Completed` status implies a successful Run occurred)
- Execution details for in-progress or failed Runs are lost

---

## 7. Metrics

### 7.1 Recommended Metrics (v0.x)

The following metrics are recommended for operational monitoring. Spine does not require a specific metrics system — these may be derived from events, logs, or direct instrumentation.

**Execution metrics:**

- Runs started / completed / failed / cancelled (counters)
- Run duration (histogram)
- Step execution duration (histogram, by step type)
- Retry count per step (histogram)
- Active Runs (gauge)

**System health metrics:**

- Git commit latency (histogram)
- Projection sync lag (gauge, seconds behind HEAD)
- Event delivery latency (histogram)
- Queue depth (gauge)

**Error metrics:**

- Step failures by error classification (counter, transient vs permanent)
- Git commit failures (counter)
- Orphaned Runs detected (counter)

### 7.2 Metrics and Events

Most metrics can be derived from operational events:

- `run_started` → increment run counter
- `run_completed` → increment completion counter, record duration from payload
- `step_failed` → increment failure counter by classification
- `retry_attempted` → increment retry counter

This means a metrics consumer can be implemented as an event consumer without additional instrumentation.

---

## 8. Constitutional Alignment

| Requirement (Constitution §7) | How Spine Satisfies It |
|-------------------------------|----------------------|
| Execution paths must be reconstructible from repository state | Git history contains all governed outcomes; domain events are reconstructible from Git commits |
| Outcomes must be traceable to governing artifacts | Every durable outcome commit references the task and workflow; trace_id links execution to commits |
| Non-deterministic systems must declare variability boundaries | AI actor steps declare their type; outcomes are evaluated through explicit governance steps |

---

## 9. Cross-References

- [Constitution](/governance/constitution.md) §7 — Reproducibility mandate
- [Domain Model](/architecture/domain-model.md) §3.5 — Run entity with `trace_id`, §3.7 — Event entity
- [Data Model](/architecture/data-model.md) §2.3 — Runtime Store schema, §2.4 — Event layer, §5 — Reconciliation
- [Event Schemas](/architecture/event-schemas.md) — Concrete event type definitions
- [ADR-002](/architecture/adr/ADR-002-events.md) — Event model (domain vs operational, derived signals)
- [System Components](/architecture/components.md) §4.7 — Event Router
- [Error Handling](/architecture/error-handling-and-recovery.md) §9 — Operator visibility and alerting

---

## 10. Evolution Policy

This observability model is expected to evolve as the system is implemented and operational experience is gained.

Areas expected to require refinement:

- Distributed tracing integration (OpenTelemetry or similar)
- Structured audit log export for compliance reporting
- Dashboard and alerting templates
- Log aggregation and search infrastructure guidance
- SLO definitions and error budget tracking

Changes that alter the audit trail guarantees or traceability model should be captured as ADRs.
