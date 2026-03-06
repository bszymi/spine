# ADR-001: Workflow Definition Storage and Execution Recording Model

**Status:** Accepted
**Date:** 2026-03-06
**Decision Makers:** Spine Architecture

---

## Context

Spine is designed as an artifact-centric, governed execution system where durable truth lives in Git.
Execution of work is governed by **Workflow Definitions**, and workflows are executed through **Runs** composed of **Step Definitions**.

Two key architectural questions arise:

1. Where should workflow definitions be stored and versioned?
2. How should execution progress be recorded in Git?

Possible options considered:

### Workflow Definition Storage

**Option A — Runtime configuration (database)**
Workflows stored and modified in runtime configuration.

Pros:
- Easy to edit dynamically
- Operational flexibility

Cons:
- Weak reproducibility
- Difficult to reconstruct historical behavior
- Governance logic becomes opaque

**Option B — Git artifacts**
Workflow definitions stored as versioned artifacts in the repository.

Pros:
- Strong reproducibility
- Full audit history
- Aligns with artifact-centric architecture
- Workflow changes are explicit and reviewable

Cons:
- Requires commit workflow to change processes
- Slightly slower iteration during experimentation

---

### Execution Recording

**Option A — Commit every step transition**

Pros:
- Complete history stored in Git

Cons:
- Excessive commit noise
- Poor readability of repository history
- Git used as a runtime event store

---

**Option B — Commit durable outcomes only**

Pros:
- Git history remains meaningful
- Durable state changes preserved
- Operational noise kept outside repository

Cons:
- Requires clear definition of “durable outcome”

---

## Decision

### 1. Workflow Definitions

Workflow definitions **must be stored as versioned Git artifacts**.

The runtime system may maintain **database projections** of workflows for efficient execution, but Git remains the authoritative source.

Each Run references a specific workflow definition version.

---

### 2. Execution Recording

Spine **does not commit every runtime step execution**.

Instead, Git commits occur only when **durable execution outcomes** occur.

Examples of durable outcomes include:

- Artifact creation or modification
- Step outputs that produce governed artifacts
- Approval or rejection decisions
- Convergence selections between parallel outcomes
- Significant Run state transitions

Operational execution details remain outside Git.

Examples:

- Step start/assignment
- Retries or timeouts
- Queue events
- Worker telemetry
- Temporary execution state

These may exist in runtime event streams, logs, or projections.

---

## Consequences

### Positive

- Repository history remains readable and meaningful
- Execution remains reconstructible from Git artifacts
- Architecture aligns with Spine constitutional principles
- Runtime execution systems remain flexible

### Negative

- Runtime systems must maintain temporary execution state
- Durable outcomes must be clearly defined and enforced

---

## Architectural Implications

The system distinguishes between:

**Step Execution**
- Runtime event
- May occur multiple times
- Operational and ephemeral

**Step Outcome**
- Durable result of execution
- Must be represented in Git artifacts or derivable from them

---

## Future Work

The following topics may require additional ADRs:

- Precise definition of **durable outcomes**
- Modeling **Step Execution vs Step Outcome**
- Representation of **parallel execution (divergence)**
- Modeling **decision / review / approval entities**
