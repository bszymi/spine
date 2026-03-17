---
type: Architecture
title: Spine Data Model
status: Living Document
version: "0.1"
---

# Spine Data Model

---

## 1. Purpose

This document defines the data model for Spine — where data lives, what form it takes, and how it moves between storage systems.

Spine operates across two fundamentally different storage layers: Git (durable truth) and runtime systems (operational state). This document makes the boundary between them concrete and defines how they interact.

All data model decisions must comply with the [Constitution](/governance/Constitution.md), particularly the Source of Truth (§2), Disposable Database (§8), and Reproducibility (§7) principles.

---

## 2. Storage Layers

### 2.1 Git Layer — Durable Truth

Git is the authoritative source of all durable system state.

**What lives in Git:**

- Artifact content and metadata (Initiatives, Epics, Tasks, ADRs, Governance, Architecture, Product documents)
- Artifact front matter (YAML metadata as defined in [Artifact Schema](/governance/artifact-schema.md))
- Artifact linkage (typed relationships between artifacts)
- Workflow definitions (versioned descriptions of how work progresses)
- Governance documents (Charter, Constitution, Guidelines)

**Format:**

- Markdown files with YAML front matter
- Organized by repository structure as defined in [Repository Structure](/governance/repository-structure.md)
- Every change produces a Git commit with explicit history

**Properties:**

- Immutable history — commits are never rewritten
- Globally versioned — every state is identified by a Git commit SHA
- Self-describing — artifacts contain their own metadata
- Diffable — all changes are explicit and reviewable

---

### 2.2 Projection Layer — Query-Optimized Views

The Projection Store holds derived views of Git artifact state, optimized for querying.

**What lives in projections:**

- Parsed artifact metadata (id, type, status, owner, dates, links)
- Artifact content indexed for search
- Relationship graphs derived from artifact linkage
- Aggregated views (e.g., epic progress, initiative status)

**Properties:**

- Derived — all projection data is computable from Git
- Disposable — if lost, projections can be fully rebuilt from Git
- Eventually consistent — projections may lag behind the latest Git commit
- Commit-tagged — each projection record tracks the Git commit it was derived from

Projection fields are derived from artifact front matter (YAML metadata) and file content. In practice, the projection layer parses the full front matter structure and may store it as a JSON document while also extracting commonly queried fields (such as id, type, status, title) into indexed columns for efficient querying.

**Schema (conceptual):**

```
projections
├── artifact_id        (string, from front matter)
├── artifact_type      (string, from front matter)
├── artifact_path      (string, repository-relative path)
├── status             (string, from front matter)
├── title              (string, from front matter)
├── metadata           (jsonb, full parsed front matter)
├── content            (text, markdown body)
├── links              (jsonb, parsed link entries)
├── source_commit      (string, Git commit SHA)
├── synced_at          (timestamp, when projection was last updated)
└── content_hash       (string, hash of raw file content for change detection)
```

---

### 2.3 Runtime Layer — Operational State

Runtime systems maintain operational state that supports execution but is not durable truth.

**What lives in runtime state:**

- Run execution state (current step, actor assignments, retry counts, timestamps)
- Step execution progress (waiting, assigned, in progress, completed, failed)
- Queue entries (pending step assignments, event delivery)
- Actor session state (authentication tokens, active assignments)

**Properties:**

- Operational — supports execution in progress
- Not authoritative — Git artifacts are the source of truth for completed outcomes
- Recoverable with limitations — if lost, in-progress Runs may need to be restarted, but all durable outcomes remain in Git
- Ephemeral by design — runtime state is expected to be transient

**Schema (conceptual):**

```
runs
├── run_id             (string, unique identifier)
├── task_path          (string, path to governing Task artifact)
├── workflow_path      (string, path to governing Workflow Definition)
├── workflow_version   (string, Git commit SHA of workflow definition)
├── workflow_version_label (string, semantic version from workflow YAML, informational)
├── status             (enum: pending, active, completed, failed, cancelled)
├── current_step_id    (string, active step within workflow)
├── started_at         (timestamp)
├── completed_at       (timestamp, nullable)
└── trace_id           (string, observability correlation ID)

step_executions
├── execution_id       (string, unique identifier)
├── run_id             (string, foreign key to runs)
├── step_id            (string, step within workflow definition)
├── actor_id           (string, assigned actor)
├── status             (enum: waiting, assigned, in_progress, completed, failed, skipped)
├── attempt            (integer, retry count)
├── outcome            (string, workflow-defined outcome value)
├── started_at         (timestamp, nullable)
├── completed_at       (timestamp, nullable)
└── error_detail       (text, nullable, failure information)

queue_entries
├── entry_id           (string, unique identifier)
├── entry_type         (enum: step_assignment, event_delivery)
├── payload            (jsonb, entry-specific data)
├── status             (enum: pending, processing, completed, failed)
├── created_at         (timestamp)
└── processed_at       (timestamp, nullable)
```

---

### 2.4 Event Layer — Derived Signals

Events flow between components but are not stored as durable truth (per [ADR-002](/architecture/adr/ADR-002-events.md)).

**What lives in the event layer:**

- Domain events derived from Git changes (artifact_created, artifact_updated, run_completed)
- Operational events from runtime execution (step_started, step_assigned, retry_attempted)

**Properties:**

- Derived — domain events are computable from Git commit history
- Transient — events may exist only in queues or streams
- At-least-once delivery — domain events should be delivered reliably to consumers
- Not a source of truth — events are notifications, not records

Domain events must always be reconstructible from Git history, because Git is the authoritative source of durable system state. Operational events produced during runtime execution do not have the same guarantee and may be transient.

**Event envelope (conceptual):**

```
event
├── event_id           (string, unique identifier)
├── event_type         (string, e.g., artifact_updated, step_started)
├── timestamp          (ISO 8601 datetime)
├── source_component   (string, which component produced the event)
├── actor_id           (string, nullable, who caused it)
├── run_id             (string, nullable, associated Run)
├── artifact_path      (string, nullable, associated artifact)
├── source_commit      (string, nullable, Git commit that triggered the event)
└── payload            (jsonb, event-specific data)
```

---

## 3. Data Flow

### 3.1 Write Path (Artifact Changes)

```
Actor action
→ Workflow Engine (validates governance)
→ Artifact Service (validates schema, writes to Git)
→ Git commit created
→ Domain event emitted (artifact_created / artifact_updated)
→ Projection Service (parses commit, updates Projection Store)
```

All durable state changes flow through Git. Artifact changes may originate either through governed workflow actions or through direct repository commits by users or automation. In both cases, the Git commit remains the authoritative state change, and the Projection Store is updated asynchronously after the commit.

### 3.2 Read Path (Queries)

```
Actor query
→ Access Gateway
→ Projection Store (reads projected state)
→ Response returned
```

Queries read from projections, not from Git directly. This provides fast access without parsing Markdown on every request.

For cases where projection freshness is critical, consumers may compare the projection's `source_commit` against the repository HEAD to determine staleness.

### 3.3 Execution Path (Runs)

```
Task triggers Run
→ Workflow Engine creates Run record (runtime state)
→ Steps assigned to actors (via Actor Gateway)
→ Actors execute and return results
→ Workflow Engine evaluates step outcomes
→ Durable outcomes committed to Git (via Artifact Service)
→ Runtime Run record updated
→ Operational events emitted throughout
```

Runtime execution state lives in the Runtime Store. Runs are operational constructs and are not durable artifacts stored in Git. Only durable outcomes (artifact creation, status changes, acceptance decisions) are committed to Git.

---

## 4. Projection Rebuild

### 4.1 Full Rebuild

If the Projection Store is lost or corrupted, it can be fully rebuilt from Git:

1. Projection Service reads the repository tree at a chosen commit (typically HEAD)
2. Each artifact file is parsed (front matter + content)
3. Projection records are created with `source_commit` set to HEAD
4. Linkage graph is reconstructed from parsed front matter

**Guarantee:** Full rebuild produces a Projection Store identical to one that was incrementally maintained, because all projection data is derived from Git.

### 4.2 Incremental Sync

During normal operation, projections are updated incrementally:

1. Domain event signals that an artifact changed (or polling detects new commits)
2. Projection Service reads the changed files from the new commit
3. Affected projection records are updated
4. `source_commit` and `synced_at` are updated

### 4.3 Consistency Model

Projections are eventually consistent with Git.

- There is no guarantee that a projection reflects the latest commit at any given moment
- Consumers must tolerate staleness
- The `source_commit` field allows consumers to reason about projection freshness
- The Projection Service should minimize sync delay but is not required to be real-time

---

## 5. Reconciliation

### 5.1 Projection Reconciliation

To detect and fix projection drift:

1. Compare `source_commit` of each projection against the current repository HEAD
2. For any stale projections, re-parse the artifact from Git and update
3. For any projections with no corresponding Git artifact, mark as orphaned and remove
4. For any Git artifacts with no projection, create the missing projection

Reconciliation may be triggered manually, on a schedule, or when anomalies are detected.

### 5.2 Runtime State Reconciliation

Runtime execution state (Runs, step executions) does not have the same rebuild guarantees as projections.

If runtime state is lost:

- **Completed Runs** — durable outcomes exist in Git. Run records can be partially reconstructed from artifact history (which tasks were completed, what artifacts were produced).
- **In-progress Runs** — may need to be restarted. The Workflow Engine should detect orphaned Runs and allow operators to restart or cancel them.
- **Queue entries** — lost entries may result in missed step assignments or event deliveries. The system should support replaying events from Git commit history to recover.

### 5.3 Event Reconciliation

Domain events can be reconstructed by replaying Git commit history:

1. Walk the commit log from a known checkpoint
2. Diff each commit to identify artifact changes
3. Re-emit corresponding domain events

This allows consumers that missed events to catch up without the Event Router maintaining durable event storage.

---

## 6. Data Ownership Summary

| Data | Storage | Owner | Disposable? |
|------|---------|-------|------------|
| Artifact content and metadata | Git | Artifact Service | No — authoritative |
| Workflow definitions | Git | Artifact Service | No — authoritative |
| Governance documents | Git | Artifact Service | No — authoritative |
| Artifact projections | Database | Projection Service | Yes — rebuildable from Git |
| Run execution state | Database | Workflow Engine | Yes — operational only |
| Step execution state | Database | Workflow Engine | Yes — operational only |
| Queue entries | Queue / Database | Event Router | Yes — operational only |
| Events | Queue / Stream | Event Router | Yes — derived signals |
| Actor sessions | Memory / Database | Access Gateway | Yes — transient |

---

## 7. Storage Technology Guidance (v0.x)

This section provides guidance, not requirements. Technology choices may evolve.

### 7.1 Git

- Hosted on GitHub, GitLab, or similar
- Accessed via Git CLI, libgit2, or platform API
- The Artifact Service abstracts Git operations behind a service interface

### 7.2 Database

- PostgreSQL recommended for projections and runtime state
- JSONB columns for flexible metadata and linkage storage
- Single database instance acceptable for v0.x
- Projection tables and runtime tables may share the same database but should use separate schemas for clarity

### 7.3 Queue

- In-process queue acceptable for v0.x (e.g., in-memory channel)
- May be extracted to a dedicated message broker (Redis, RabbitMQ, etc.) when scaling requires it
- Must support at-least-once delivery for domain events

---

## 8. Constitutional Alignment

| Principle | How the Data Model Supports It |
|-----------|-------------------------------|
| Source of Truth (§2) | All durable state lives in Git; databases are projections |
| Explicit Intent (§3) | Artifacts in Git define all intent; runtime state derives from them |
| Reproducibility (§7) | Projections rebuildable from Git; events replayable from commit history |
| Disposable Database (§8) | All non-Git storage is explicitly disposable and reconstructible |

---

## 9. Evolution Policy

This data model is expected to evolve as implementation progresses.

Schema changes to projections require no migration guarantees — projections can always be rebuilt from Git. Schema changes to runtime state should be handled through standard database migration practices.

Changes that alter the boundary between Git truth and runtime state should be captured as ADRs.
