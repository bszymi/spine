# ADR-003: Discussion and Comment Model — Runtime Collaboration with Durable Resolution Artifacts

**Status:** Accepted
**Date:** 2026-03-09
**Decision Makers:** Spine Architecture

---

## Context

Spine is an artifact-centric system where durable truth is stored in Git as versioned artifacts.

However, real work involves discussion before durable truth is updated:

- product clarification discussions
- architecture exploration
- review comments
- objections and alternatives
- AI and human collaboration around artifacts and runs

These discussions are important to workflow and collaboration, but they are not always themselves durable truth.

The architecture must define:

1. Where discussion and comments live
2. Whether discussion is stored in Git
3. How discussion produces durable outcomes such as artifact updates, decisions, or ADRs

Without a clear model, the system risks either:

- storing too much conversational noise in Git, or
- losing the reasoning that led to durable changes.

---

## Decision

### 1. Discussion is runtime collaboration, not authoritative truth

Discussion and comments are part of the collaboration layer.

They may be stored in runtime systems such as a database or discussion service and attached to:

- artifacts
- runs
- decisions
- reviews

This rule applies equally to human and AI actors. AI-generated prompts, reasoning traces, or intermediate outputs may appear in discussion threads, but they follow the same model: they are runtime collaboration context and are not authoritative system state unless converted into durable artifacts or decisions.

Discussion is **not** the authoritative source of system truth.

Authoritative truth begins only when discussion is resolved into a durable artifact change or governed decision.

---

### 2. Discussion is not stored in Git by default

The full discussion history does not need to be committed to Git.

Reasons:

- most discussion is exploratory, partial, or abandoned
- storing all discussion in Git would create excessive repository noise
- Git should preserve durable meaning, not every conversational step

This does not violate the Spine principle of reproducibility from Git (Constitution §7), because discussion itself is not considered authoritative system state. Only the resolved outcomes of discussion — such as artifact updates, ADRs, or decision records — are required to be durable and reconstructible from Git history.

Discussion may remain entirely in runtime systems unless it produces a meaningful outcome that affects product, architecture, workflow, or governance.

---

### 3. Important discussion outcomes must be converted into durable artifacts

When discussion leads to a meaningful conclusion, the outcome must be recorded durably in Git through one or more of the following:

- update to an existing artifact
- creation of a new artifact
- creation of an ADR
- creation of a decision or review record
- structured clarification recorded in the governing artifact

Examples:

- product discussion → product artifact updated
- architecture discussion → ADR created
- review discussion → approval/rejection recorded
- convergence discussion → selected outcome recorded

The durable record is the source of truth, not the discussion thread itself.

---

### 4. The interface should support conversion from discussion to durable outputs

Spine should support UI actions that transform runtime discussion into governed artifacts.

Examples of supported actions:

- **Apply clarification to artifact**
- **Create ADR from discussion**
- **Create decision record**
- **Summarize discussion into review outcome**
- **Link discussion thread to resulting artifact change**

This allows discussion to remain fluid while making it easy to preserve the meaningful result.

---

### 5. Discussion may reference durable outcomes

A discussion thread may store references to the durable artifacts it produced.

Examples:

- `resolved_by_artifact_id`
- `resulting_adr_id`
- `resulting_decision_id`

This preserves traceability between conversation and governed outcome without making discussion itself the durable source of truth.

---

## Consequences

### Positive

- Git history remains focused on durable meaning
- Collaboration can be rich, threaded, and exploratory
- Important conclusions remain traceable through artifacts
- The interface can support smooth conversion from conversation to governed records

### Negative

- Full conversational history is not guaranteed to be reconstructible from Git
- Runtime discussion storage becomes an important collaboration capability
- Teams must ensure discussion systems have appropriate retention policies to avoid losing useful architectural or product context.
- Teams must decide which discussions require durable resolution

---

## Architectural Implications

The runtime collaboration layer may distinguish between several implementation concepts used to manage discussion and its outcomes:

### Discussion Thread
A runtime collaboration container attached to an artifact, run, decision, or review.

### Discussion Entry
An individual message or comment within a thread.

### Durable Resolution
A governed artifact change or new artifact that records the outcome of a discussion.

These concepts describe collaboration and resolution patterns in the runtime system. They are not necessarily first-class domain entities in the Spine core model unless later ADRs decide to formalize them.

---

## Out of Scope

This ADR does not define:

- the full Decision / Approval entity model
- retention policy for discussion threads
- discussion moderation rules
- notification or subscription mechanisms

These may be covered by later ADRs.

---

## Future Work

Future ADRs may define:

- Decision / Approval model
- Review model
- Comment and thread schema
- Discussion retention policy
- UX patterns for converting discussion into artifacts
