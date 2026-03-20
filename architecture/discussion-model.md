---
type: Architecture
title: Discussion and Comment Runtime Model
status: Living Document
version: "0.1"
---

# Discussion and Comment Runtime Model

---

## 1. Purpose

This document defines the runtime storage and interaction model for discussions and comments in Spine v0.x.

[ADR-003](/architecture/adr/ADR-003-discussion-and-comment-model.md) establishes that discussion is runtime collaboration, not authoritative truth. Discussion lives in runtime systems, not Git. Only when discussion produces a meaningful outcome is that outcome recorded durably as an artifact change or new artifact.

This document makes those decisions concrete — defining the data model, storage, lifecycle, access control, and conversion path from discussion to durable artifacts.

---

## 2. Core Concepts

### 2.1 Discussion Thread

A runtime collaboration container attached to a governed entity. Threads provide structured space for conversation, deliberation, and reasoning.

A thread is always attached to exactly one **anchor** — the entity the discussion is about:

| Anchor Type | Example | When Used |
|-------------|---------|-----------|
| Artifact | A task, ADR, or architecture document | Clarifying requirements, debating design decisions |
| Run | An active workflow execution | Discussing execution approach, flagging issues |
| Step Execution | A specific step within a Run | Review comments, rework guidance |
| Divergence Context | A divergence/convergence evaluation | Comparing branch outcomes, selection rationale |

### 2.2 Comment

An individual message within a thread. Comments are the atomic unit of discussion.

### 2.3 Durable Resolution

A governed artifact change that records the outcome of a discussion. The resolution is the source of truth — not the discussion thread itself (per ADR-003 §3).

---

## 3. Data Model

### 3.1 Discussion Thread Schema

```sql
CREATE TABLE runtime.discussion_threads (
    thread_id           text        PRIMARY KEY,
    anchor_type         text        NOT NULL,       -- artifact, run, step_execution, divergence_context
    anchor_id           text        NOT NULL,       -- path or ID of the anchored entity
    title               text,                       -- optional thread title
    status              text        NOT NULL DEFAULT 'open',
                                                    -- open, resolved, archived
    created_by          text        NOT NULL,       -- actor_id of thread creator
    created_at          timestamptz NOT NULL DEFAULT now(),
    resolved_at         timestamptz,
    resolution_type     text,                       -- artifact_updated, artifact_created, adr_created, decision_recorded, no_action
    resolution_ref      text,                       -- path to the artifact or commit that resolved this thread

    CONSTRAINT thread_status_check CHECK (status IN ('open', 'resolved', 'archived')),
    CONSTRAINT thread_anchor_check CHECK (anchor_type IN ('artifact', 'run', 'step_execution', 'divergence_context'))
);
```

**Indexes:**

```sql
CREATE INDEX idx_threads_anchor ON runtime.discussion_threads (anchor_type, anchor_id);
CREATE INDEX idx_threads_status ON runtime.discussion_threads (status);
CREATE INDEX idx_threads_created_by ON runtime.discussion_threads (created_by);
```

### 3.2 Comment Schema

```sql
CREATE TABLE runtime.comments (
    comment_id          text        PRIMARY KEY,
    thread_id           text        NOT NULL REFERENCES runtime.discussion_threads(thread_id),
    parent_comment_id   text        REFERENCES runtime.comments(comment_id),  -- for nested replies
    author_id           text        NOT NULL,       -- actor_id
    author_type         text        NOT NULL,       -- human, ai_agent, automated_system
    content             text        NOT NULL,       -- markdown content
    metadata            jsonb       DEFAULT '{}',   -- structured data (e.g., AI reasoning trace, code snippets)
    created_at          timestamptz NOT NULL DEFAULT now(),
    edited_at           timestamptz,
    deleted             boolean     NOT NULL DEFAULT false  -- soft delete

    -- No hard deletes — comments are soft-deleted to preserve thread coherence
);
```

**Indexes:**

```sql
CREATE INDEX idx_comments_thread ON runtime.comments (thread_id, created_at);
CREATE INDEX idx_comments_author ON runtime.comments (author_id);
CREATE INDEX idx_comments_parent ON runtime.comments (parent_comment_id);
```

---

## 4. Thread Lifecycle

### 4.1 States

| State | Description |
|-------|-------------|
| `open` | Active discussion; comments may be added |
| `resolved` | Discussion produced a durable outcome; thread is closed |
| `archived` | Discussion did not produce an outcome but is no longer active |

### 4.2 Transitions

| From | To | Trigger | Effects |
|------|-----|---------|---------|
| `open` | `resolved` | Actor resolves thread with a durable artifact reference | Set `resolved_at`, `resolution_type`, `resolution_ref` |
| `open` | `archived` | Actor or system archives thread (no outcome needed) | — |
| `resolved` | `open` | Actor reopens thread (outcome disputed or incomplete) | Clear resolution fields |
| `archived` | `open` | Actor reopens archived thread | — |

### 4.3 Resolution Types

When a thread is resolved, the resolution type records what kind of durable outcome was produced:

| Type | Meaning | Example |
|------|---------|---------|
| `artifact_updated` | An existing artifact was modified based on discussion | Task acceptance criteria clarified |
| `artifact_created` | A new artifact was created | Follow-up task created from discussion |
| `adr_created` | An ADR was created to record a decision | Architecture decision captured |
| `decision_recorded` | A governance decision was recorded in an artifact | Review outcome with rationale |
| `no_action` | Discussion concluded with no durable change needed | Question answered, no artifact impact |

---

## 5. Conversion to Durable Artifacts

### 5.1 Conversion Actions

The system supports explicit actions to convert discussion into governed artifacts (per ADR-003 §4):

| Action | Input | Output |
|--------|-------|--------|
| Apply clarification | Selected comments + target artifact | Updated artifact with changes committed to Git |
| Create ADR from discussion | Thread summary + decision | New ADR artifact committed to Git |
| Create follow-up task | Thread context + task definition | New Task artifact committed to Git |
| Record decision | Thread summary + rationale | Acceptance/rejection recorded on task artifact |
| Summarize into review | Thread context + evaluation | Step outcome submitted via `step.submit` |

### 5.2 Conversion Workflow

1. An actor identifies discussion content that should become durable
2. The actor initiates a conversion action (via CLI, API, or GUI)
3. The system creates or updates the target artifact through the normal governed path (`artifact.create`, `artifact.update`, `task.accept`, etc.)
4. The thread is resolved with `resolution_type` and `resolution_ref` pointing to the resulting commit or artifact
5. The discussion thread retains a link to the durable outcome for traceability

### 5.3 AI-Assisted Conversion

AI agents may assist with conversion by:

- Summarizing discussion threads into concise artifact content
- Drafting ADRs from architecture discussions
- Extracting action items into task definitions
- Proposing artifact updates based on review comments

AI-generated content follows the same governance rules — it must be submitted through workflow steps and validated before becoming durable.

---

## 6. Access Control

### 6.1 Thread Access

Discussion access follows the authorization model from the [Security Model](/architecture/security-model.md) §4:

| Action | Min Role |
|--------|----------|
| Read threads and comments | `reader` |
| Create thread | `contributor` |
| Add comment | `contributor` |
| Resolve thread | `contributor` (must have access to the anchor entity) |
| Archive thread | `reviewer` |
| Reopen resolved thread | `reviewer` |
| Delete comment (soft) | Author or `operator` |

### 6.2 Thread Visibility

Threads inherit visibility from their anchor:

- A thread on a task is visible to actors who can read the task
- A thread on a Run is visible to actors who can view the Run
- No separate permission model for threads — they follow the anchor's access rules

---

## 7. Retention and Durability

### 7.1 Retention Policy

Discussion threads are runtime data with limited durability (per [Data Model](/architecture/data-model.md) §2.3):

| Thread Status | Default Retention | Rationale |
|---------------|-------------------|-----------|
| `open` | Indefinite (while active) | Active discussions should not expire |
| `resolved` | Configurable (default: 180 days) | Outcome is in Git; thread is supplementary context |
| `archived` | Configurable (default: 90 days) | No durable outcome; lower retention |

Retention is operator-configured. Spine does not enforce retention — operators choose when to prune.

### 7.2 What Is Lost When Threads Are Deleted

- The conversational reasoning that led to a decision
- AI reasoning traces and intermediate outputs
- Review comments that were not converted to durable outcomes

### 7.3 What Is NOT Lost

- All durable outcomes (artifact changes, ADRs, decisions) remain in Git
- Resolution references in Git commits provide traceability even after threads are deleted
- The fact that a discussion occurred is traceable through commit messages and audit trail

---

## 8. Integration with Workflow Execution

### 8.1 Review Step Discussions

Review steps naturally produce discussion. When an actor reviews a deliverable:

1. The system creates (or reuses) a thread anchored to the step execution
2. The reviewer adds comments (feedback, questions, requests for changes)
3. The review outcome (`accepted`, `needs_rework`, `rejected`) is submitted as a step result
4. If resolved, the thread links to the step outcome

### 8.2 Convergence Discussions

During convergence evaluation, the evaluator may discuss branch outcomes:

1. A thread is anchored to the divergence context
2. The evaluator comments on strengths/weaknesses of each branch
3. The convergence decision is submitted as a step result
4. The thread is resolved with the convergence result as the resolution reference

### 8.3 Artifact Discussions

Standalone discussions about artifacts (outside of workflow execution):

1. An actor creates a thread anchored to an artifact
2. Discussion proceeds
3. If the discussion produces a change, the actor converts it to a durable outcome
4. If no change is needed, the thread is archived

---

## 9. Constitutional Alignment

| Principle | How the Discussion Model Supports It |
|-----------|-------------------------------------|
| Source of Truth (§2) | Discussion is runtime; only resolved outcomes are committed to Git |
| Explicit Intent (§3) | Conversion from discussion to artifact requires explicit action |
| Reproducibility (§7) | Durable outcomes are in Git; discussion is supplementary context, not required for reconstruction |
| Actor Neutrality (§5) | Both human and AI comments follow the same model |

---

## 10. Cross-References

- [ADR-003](/architecture/adr/ADR-003-discussion-and-comment-model.md) — Governance decision for discussion model
- [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md) — Evaluation and acceptance model
- [Security Model](/architecture/security-model.md) §4 — Authorization roles
- [Data Model](/architecture/data-model.md) §2.3 — Runtime layer properties
- [Runtime Schema](/architecture/runtime-schema.md) — Production database context
- [Actor Model](/architecture/actor-model.md) §6 — AI agent configuration for assisted conversion
- [API Operations](/architecture/api-operations.md) — Operations for artifact creation/update

---

## 11. Evolution Policy

This discussion model is expected to evolve as the system is implemented and collaboration patterns emerge.

Areas expected to require refinement:

- Notification and subscription mechanisms for thread activity
- Thread templates for common discussion types (review, architecture debate, incident)
- Discussion search and discovery
- Thread analytics (time-to-resolution, participation metrics)
- Integration with external collaboration tools (Slack, email)
- Moderation and content policies

Changes that alter the boundary between runtime discussion and durable artifacts should be captured as ADRs.
