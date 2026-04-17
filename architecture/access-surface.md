---
type: Architecture
title: Access Surface
status: Living Document
version: "0.1"
---

# Access Surface

---

## 1. Purpose

This document defines the external access surface for Spine v0.x — how actors interact with the system from outside the runtime boundary.

Spine is not a single-protocol system. Different actors and use cases require different interaction modes. This document defines the supported access modes, the operations they expose, the authentication and authorization model, and the boundary between external interfaces and the internal engine.

All access modes converge on the [Access Gateway](/architecture/components.md) (§4.1), which normalizes requests into a common internal model. The access surface is the external-facing contract; the Access Gateway is the internal boundary.

---

## 2. Access Modes

### 2.1 Overview

Spine v0.x supports three access modes:

| Mode | Primary Actors | Transport | Purpose |
|------|---------------|-----------|---------|
| CLI | Developers, operators, CI/CD automation | Local process / shell | Direct artifact and workflow operations |
| API | External integrations, automation, AI agents | HTTP/REST | Programmatic access to Spine operations |
| GUI | Product managers, reviewers, non-technical users | Web browser | Visual exploration, review, and governance actions |

All three modes are adapters that connect through the Access Gateway. They expose the same underlying operations with different ergonomics.

### 2.2 CLI

The command-line interface provides direct, scriptable access to Spine operations.

**Capabilities:**

- Create artifacts through governed workflows (`spine artifact entry`)
- Create, read, update artifact content and metadata
- Add artifacts to planning run branches incrementally
- Trigger workflow actions (start run, submit step result, approve/reject)
- Query artifact state and relationships
- Validate artifacts against schemas and cross-artifact rules
- View run status and execution history

**Characteristics:**

- May read artifact state directly from the local Git repository for convenience; authoritative project state remains defined by the repository history
- Communicates with the Spine runtime for workflow and execution operations
- Suitable for automation (CI/CD pipelines, scripted workflows)
- Supports piped input/output for composability

**v0.x scope:**

- Primary interaction mode during foundation phase
- May operate as a standalone tool against Git before the full runtime is available

### 2.3 API

The HTTP API provides programmatic access for external systems and automation.

**Capabilities:**

- All operations available through CLI
- Webhook endpoints for external event ingestion
- Structured JSON responses for integration consumption

**Characteristics:**

- HTTP-based interface exposing domain operations rather than a strict REST resource model
- Stateless request handling
- Supports authentication via API tokens

The Spine API is not intended to be a pure REST API. While HTTP is used as the transport, the API primarily exposes domain operations (e.g., workflow transitions or artifact lifecycle actions). Read-heavy endpoints may resemble REST resources for convenience, but state-changing actions are modeled as governed operations rather than CRUD-style resource manipulation.

**v0.x scope:**

- Minimal surface — expose only operations required by active integrations
- May be introduced after CLI is stable

### 2.4 GUI

The graphical interface provides visual access for exploration, review, and governance.

**Capabilities:**

- Browse artifacts by type, status, and relationships
- Visualize artifact linkage and dependency graphs
- Participate in review and approval workflows
- View run progress and execution history
- Perform governance actions (approve, reject, create follow-up)

**Characteristics:**

- Read-heavy — most interactions are exploration and review
- Writes are primarily governance actions (approvals, status transitions)
- Consumes projected state from the Projection Store for fast rendering

**v0.x scope:**

- Not required for initial foundation phase
- May be introduced as a read-only dashboard first, with write capabilities added incrementally

---

## 3. External Operations

### 3.1 Operation Categories

Operations exposed through the access surface fall into five categories:

| Category | Description | Examples |
|----------|-------------|---------|
| Artifact operations | Create, read, update, query governed artifacts (Initiative/Epic/Task/ADR/Document) | Create task, update status, search by type |
| Workflow definition operations | Create, read, update, validate workflow definitions (per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md)) | Create workflow, update to new version, pre-commit validate |
| Workflow execution operations | Interact with workflow execution | Start run, submit step result, approve/reject |
| Query operations | Retrieve projected state | List artifacts by status, view relationship graph |
| System operations | Administrative and operational | Validate schema, trigger projection rebuild, health check |

### 3.2 Artifact Operations

These operations govern Initiative, Epic, Task, ADR, and Document artifacts. Workflow definitions are managed through a separate category (§3.2.2) — generic artifact write operations reject workflow paths with `400 invalid_params` per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md).

| Operation | Description | Modifies Git |
|-----------|-------------|-------------|
| `artifact.create` | Create a new artifact with validated front matter (not workflow definitions) | Yes |
| `artifact.read` | Read artifact content and metadata (workflow paths return summary only) | No |
| `artifact.update` | Update artifact content or metadata (not workflow definitions) | Yes |
| `artifact.validate` | Validate artifact against schema and cross-artifact rules | No |
| `artifact.list` | List artifacts by type, status, or parent | No |
| `artifact.links` | Query artifact relationships | No |

### 3.2.1 Artifact Discovery from Git

Artifacts may be introduced through normal Git activity rather than explicit API or CLI creation commands. For example, during task execution a contributor may create new artifact files (tasks, documents, ADRs, etc.) directly in a task branch.

When workflow checkpoints occur (for example during step submission or review), the system performs **artifact discovery** by inspecting the branch contents or commit range associated with the Run.

Discovered artifacts are treated as **proposed artifacts** until the governing workflow step is accepted and the branch is merged into the authoritative branch.

This model ensures that:

- Git remains the authoritative source of artifact content
- branch-local artifacts can participate in review workflows
- newly introduced artifacts are validated before becoming part of the authoritative project state

Artifact creation operations such as `artifact.create` are therefore helpers rather than the only mechanism through which artifacts may appear in the system.

### 3.2.2 Workflow Definition Operations

Per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md), workflow definitions are a first-class API resource with dedicated operations. Their structural invariants (step-reference integrity, cycle detection, divergence/convergence balance, actor/skill resolution — see [Workflow Validation](/architecture/workflow-validation.md)) are enforced by the workflow validation suite at write time, so malformed definitions are rejected before commit rather than at Run execution time.

| Operation | Description | Modifies Git |
|-----------|-------------|-------------|
| `workflow.create` | Create a new workflow definition; runs full workflow validation suite before commit | Yes |
| `workflow.update` | Update an existing workflow definition; version bump required | Yes |
| `workflow.read` | Return the executable workflow definition body (only endpoint that does) | No |
| `workflow.list` | List workflow definition summaries, filterable by `applies_to`, `status`, `mode` | No |
| `workflow.validate` | Validate a candidate workflow body without persisting | No |

CLI and GUI surfaces route workflow create/update/validate actions to this category, not to `artifact.*`.

**Lifecycle governance ([ADR-008](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)):** workflow edits flow through a planning-mode Run under the seeded `workflow-lifecycle` workflow (draft → review). Reviewer callers invoking `workflow.create`/`workflow.update` without a `write_context` open a planning run and receive `run_id` + `branch_name`; approval merges the branch to the authoritative branch. Operators (and admins) retain a direct-commit path — commits on that path are tagged with a `Workflow-Bypass: true` trailer and reported with `write_mode: bypass` in the response. Existing Runs stay pinned to their captured workflow commit SHA and are not rebased by later edits.

### 3.3 Workflow Execution Operations

| Operation | Description | Modifies Git |
|-----------|-------------|-------------|
| `run.start` | Start a new Run for a task | No (runtime only) |
| `run.status` | Query Run execution state | No |
| `run.cancel` | Cancel an in-progress Run | No (runtime only) |
| `step.submit` | Submit step result for evaluation | Depends on outcome |
| `step.assign` | Assign actor to a step | No (runtime only) |
| `task.accept` | Record task-level acceptance | Yes |
| `task.reject` | Record task-level rejection | Yes |
| `task.cancel` | Cancel a task with rationale | Yes |
| `task.abandon` | Abandon a task by governance decision | Yes |
| `task.supersede` | Supersede a task with successor work | Yes |

### 3.4 Query Operations

| Operation | Description |
|-----------|-------------|
| `query.artifacts` | Search artifacts by type, status, metadata fields |
| `query.graph` | Retrieve relationship graph for an artifact |
| `query.history` | View artifact change history from Git |
| `query.runs` | List runs for a task with execution state |
| `execution.candidates` | List tasks ready for execution, filtered by skills, actor type, and blocking status |

### 3.5 System Operations

| Operation | Description |
|-----------|-------------|
| `system.health` | Runtime health check |
| `system.rebuild` | Trigger full projection rebuild from Git |
| `system.validate_all` | Run schema validation across all artifacts |

---

## 4. Authentication and Authorization

### 4.1 Actor Identity

Every request to Spine must be associated with an identified actor. Anonymous access is not permitted.

**Actor types:**

| Type | Identity Mechanism | Examples |
|------|-------------------|---------|
| Human | User account with credentials | Developer, product manager, reviewer |
| AI Agent | Service account with API token | LLM-based agent, automated assistant |
| Automated System | Service account with API token | CI/CD pipeline, integration webhook |

Actor identity is established at the Access Gateway and propagated to all internal components. The internal system does not distinguish between actor types for governance purposes (per Constitution §5 — Actor Neutrality).

### 4.2 Authentication

**v0.x authentication model:**

| Access Mode | Authentication Method |
|-------------|---------------------|
| CLI | Local identity (Git config) or API token |
| API | API token (Bearer token in Authorization header) |
| GUI | Session-based authentication (login with credentials) |

**Principles:**

- Authentication is the responsibility of the Access Gateway
- Internal components trust the identity established by the Access Gateway
- API tokens are scoped to specific actors and may have expiration
- No implicit identity — every operation must have an explicit actor

### 4.3 Authorization

Authorization determines what an authenticated actor may do. Spine v0.x uses a role-based model:

**Roles:**

| Role | Permissions |
|------|------------|
| `reader` | Read artifacts, query projected state, view Runs and execution history |
| `contributor` | Reader + create/update artifacts, submit step results, start Runs |
| `reviewer` | Contributor + approve/reject tasks, execute governance steps |
| `operator` | Reviewer + system operations (projection rebuild, health checks, Run cancellation) |
| `admin` | Full access including actor management, token management, and configuration |

For the full authorization model including role hierarchy, capabilities, and enforcement points, see [Security Model](/architecture/security-model.md) §4.

**Authorization rules:**

- Roles are assigned to actors, not to access modes
- An actor has the same permissions regardless of whether they access via CLI, API, or GUI
- Workflow definitions may impose additional step-level constraints via skills (e.g., "only actors with `architecture_review` skill may execute this step")
- Authorization is enforced at the Access Gateway before requests reach internal components

### 4.4 Secrets and Credentials

- Actor credentials (passwords, API tokens) are never stored in Git
- Credentials are managed by the Access Gateway's authentication layer
- Integration credentials (external API keys, webhook secrets) are stored in runtime configuration, not in artifacts
- No artifact may contain or reference credentials directly

---

## 5. Boundary: External Access vs Internal Engine

### 5.1 The Access Gateway Boundary

The Access Gateway is the boundary between external access and internal engine:

```
External World                    │  Spine Internal
                                  │
  CLI ──────┐                     │
  API ──────┤── Access Gateway ───┤── Artifact Service
  GUI ──────┘   (auth, routing)   │── Workflow Engine
                                  │── Projection Store
                                  │── Event Router
                                  │── Validation Service
```

**What happens outside the boundary:**

- Transport protocol handling (HTTP, CLI parsing, WebSocket)
- Authentication (credential validation, token verification)
- Authorization (permission checks before requests reach internal services)
- Request format normalization (CLI args → internal model, JSON → internal model)
- Response format adaptation (internal model → JSON, internal model → CLI output)

**What happens inside the boundary:**

- Artifact validation and persistence
- Workflow governance and execution
- Projection management
- Event routing
- Cross-artifact validation

### 5.2 Access Mode Adapters

Each access mode is implemented as an adapter that:

1. Accepts requests in its native format (CLI args, HTTP request, GUI action)
2. Authenticates the actor
3. Translates the request into the common internal operation model
4. Forwards the request to the Access Gateway
5. Translates the response back to its native format

Adapters do not contain business logic. They are transport-specific translators.

### 5.3 Internal Operation Model

All access modes converge on a common internal operation model:

```
InternalRequest
├── operation     (string, e.g., "artifact.create", "run.start")
├── actor_id      (string, authenticated actor)
├── actor_role    (string, authorization role)
├── params        (map, operation-specific parameters)
└── trace_id      (string, observability correlation)
```

This ensures that the internal engine never knows or cares how a request arrived.

Operations executed through this model represent governed domain transitions rather than generic CRUD actions. Interfaces (CLI, API, GUI) translate user intent into these operations, while the internal engine evaluates workflow rules, artifact validation, and governance constraints before state changes occur.

---

## 6. Cross-References

- [System Components](/architecture/components.md) — Access Gateway (§4.1), Actor Gateway (§4.6)
- [Domain Model](/architecture/domain-model.md) — Actor entity (§3.4)
- [Data Model](/architecture/data-model.md) — Runtime state vs Git truth boundary
- [Product Boundaries](/product/boundaries-and-constraints.md) — System boundaries and integration model
- [Constitution](/governance/constitution.md) — Actor Neutrality (§5), Governed Execution (§4)
- [Security Model](/architecture/security-model.md) — Full authorization model, credential management, trust boundaries
- [Task Lifecycle](/governance/task-lifecycle.md) — Which operations modify Git vs runtime only
- [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) — Workflow definitions as a separate API resource
- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Schema for workflow definitions governed by §3.2.2
- [Workflow Validation](/architecture/workflow-validation.md) — Workflow-specific validation suite

---

## 7. Evolution Policy

This document defines the v0.x access surface. It is expected to evolve as implementation progresses and usage patterns emerge.

Changes that add new access modes, alter the authorization model, or change the boundary between external and internal responsibilities should be captured as ADRs.

The access surface should remain minimal — new operations are added only when justified by concrete use cases, not speculatively.
