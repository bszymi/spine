---
id: ADR-007
type: ADR
title: Workflow Definitions as a Separate API Resource
status: Accepted
date: 2026-04-17
decision_makers: Spine Architecture
---

# ADR-007: Workflow Definitions as a Separate API Resource

---

## Context

[ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) establishes that workflow definitions are stored as versioned Git artifacts. In the current API surface ([api-operations.md](/architecture/api-operations.md) §3.1), workflow files are reachable through the same generic artifact operations (`artifact.create`, `artifact.update`, `artifact.add`) used for Initiatives, Epics, Tasks, ADRs, and free-form documents.

Workflow definitions differ materially from other artifacts:

- They are **executable schemas**, not narrative documents. The [Workflow Definition Format](/architecture/workflow-definition-format.md) specifies strict structural rules (entry_step, step_ids, divergence/convergence points, actor/skill requirements) that the runtime depends on.
- Their validation is substantially richer than artifact front-matter validation — see [Workflow Validation](/architecture/workflow-validation.md). It includes step-reference integrity, cycle detection, divergence/convergence balance, and actor/skill reference resolution.
- Errors in a workflow artifact surface at **run time**, during execution of governed work, not at read time. A silently-malformed workflow can block Runs, corrupt step-assignment, or require manual Git recovery.
- Workflow files are pure YAML (no Markdown front matter), which already makes them structurally disjoint from every other artifact type documented in [Artifact Schema](/governance/artifact-schema.md).

Routing workflow writes through the generic artifact endpoints means the strict-structure invariants are expressed as special-case branches inside a general-purpose code path, increasing the risk that validation gaps or future changes to generic artifact handling silently weaken workflow governance.

---

## Decision

### 1. Dedicated Workflow Operations

Workflow definitions become a first-class operation category alongside Artifact, Workflow Execution (Run), Query, System, Skill, and Divergence operations.

The following operations are introduced:

| Operation | Effect |
|-----------|--------|
| `workflow.create` | Creates a new workflow definition, running the full workflow-specific validation suite before commit |
| `workflow.update` | Updates an existing workflow definition; version bump is enforced |
| `workflow.read` | Reads a workflow definition by ID (and optionally version) |
| `workflow.list` | Lists workflow definitions, filterable by `applies_to`, `status`, `mode` |
| `workflow.validate` | Validates a candidate workflow body without persisting |

These operations are the only write path for workflow definitions.

### 2. Generic Artifact Endpoints Reject Workflows

`artifact.create`, `artifact.update`, and `artifact.add` reject any request whose target would be a workflow definition (by path prefix `/workflows/` or by declared type).

- Response: `400 invalid_params`
- Error payload points callers to the corresponding `workflow.*` operation.

### 3. Read and Discovery Boundary

- `workflow.read` is the only way to fetch workflow definition content.
- `query.artifacts` and `query.graph` may expose workflow definitions as list entries (id, path, version, status, applies_to) for discoverability, but do not return executable bodies.
- `artifact.read` against a workflow path returns the same summary projection used by `query.artifacts` — it does not return the executable body. Callers that want the body must use `workflow.read`.

### 4. Validation Ownership

The Validation Service exposes a dedicated workflow validation suite (per [Workflow Validation](/architecture/workflow-validation.md)). `workflow.create`, `workflow.update`, and `workflow.validate` invoke this suite directly. The generic artifact validation path does not need to be aware of workflow-specific rules.

### 5. Governance Ownership

Workflow definition changes are owned by the Spine Architecture group. Reviewer role is required for `workflow.create` / `workflow.update`. Backwards-compatibility considerations between workflow versions are recorded in commit trailers and surfaced in Run binding resolution.

Backwards compatibility with existing callers of the generic artifact endpoints is explicitly **not** a consideration: workflow writes through the generic endpoints are deprecated and removed in the same change.

---

## Consequences

### Positive

- Workflow structural invariants are enforced in one code path, not as a branch inside the generic artifact path.
- The generic artifact endpoint stays narrow — Initiative/Epic/Task/ADR/Document semantics only.
- Failure mode shifts from "runtime Run failure" to "write-time 4xx", which is cheaper and more observable.
- Authorization and governance requirements for workflow changes can be tightened without affecting other artifact types.

### Negative

- New operation surface to implement, document, and keep in sync with the OpenAPI spec.
- Callers that currently create or edit workflows through generic artifact endpoints must migrate.
- Two concepts ("artifact" and "workflow definition") now have overlapping-but-distinct create/update semantics, which must be explained clearly in [api-operations.md](/architecture/api-operations.md).

---

## Architectural Implications

- [api-operations.md](/architecture/api-operations.md) §3 gains a new operation category (Workflow Definition Operations) and the Artifact Operations table is annotated to exclude workflow definitions.
- [Access Surface](/architecture/access-surface.md) is updated so CLI and GUI surfaces route workflow writes to the new operations.
- [Validation Service](/architecture/validation-service.md) exposes the workflow validation suite as an operation callable by `workflow.validate`.
- [Artifact Schema](/governance/artifact-schema.md) is updated to state explicitly that workflow definitions are **not** covered by the artifact front-matter schema and are governed by [Workflow Definition Format](/architecture/workflow-definition-format.md).
- The OpenAPI specification gains `/workflows`, `/workflows/{id}`, `/workflows/{id}/validate`, and removes the implicit ability to POST workflow payloads through `/artifacts`.

---

## Future Work

- Governance of workflow edits themselves — **superseded by [ADR-008 — Workflow Lifecycle Governance](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)**, which routes `workflow.create`/`workflow.update` through a planning-mode Run with approval-gated merge and defines the operator bypass for recovery.
- Versioning and deprecation semantics for workflow definitions (interaction with Run binding resolution).
- Whether `workflow.update` should require the superseded-version link, mirroring ADR supersession.
- Migration tooling to convert any existing workflow artifacts written through the generic path (if any exist in repository history) to the new operation's commit-trailer format.
