---
type: Product
title: Spine Product Definition
status: Living Document
version: "0.1"
---

# Spine Product Definition

---

## 1. What Spine Is

Spine is a Git-native Product-to-Execution System.

It transforms explicit product intent into governed, observable, and reproducible execution across hybrid teams of humans and AI agents.

Spine is the structural backbone that connects what a team intends to build with how that work is executed, by whom, and under what constraints.

Spine does not manage work — it governs the structural integrity between intent and execution.

Spine maintains a small, strict core responsible for governance and coordination. Additional capabilities may be implemented through integrations or extensions without expanding the responsibilities of the core system.

---

## 2. The Problem

Modern software teams suffer from structural drift between intent and execution.

- **Specifications drift.** Product intent is written once and becomes disconnected from the work it governs.
- **Tickets detach from purpose.** Work items lose their connection to the original goal as they multiply and fragment across tools.
- **Decisions disappear.** Architectural and product decisions are made in conversations, Slack threads, and meetings — then forgotten.
- **Automation operates without governance.** CI/CD pipelines, scripts, and bots execute without structural oversight.
- **AI produces unaligned output.** AI agents generate code and content without connection to product intent or architectural constraints.

The result is chaos disguised as productivity. Teams ship output without structural confidence that it aligns with what was intended.

---

## 3. How Spine Solves It

Spine introduces structural integrity between intent and execution by treating work as versioned artifacts governed by explicit workflows.

Instead of managing work through tickets scattered across tools, Spine treats work as versioned artifacts governed by explicit workflows and stored in a Git repository.

Instead of relying on implicit processes and tribal knowledge, Spine defines explicit workflows that govern how work progresses — including what actors may do, what validation is required, and what happens when execution diverges.

Instead of treating AI as a black box, Spine treats AI agents as first-class execution actors operating under the same governance rules as humans.

---

## 4. Core Product Model

Spine operates through three interdependent layers:

### 4.1 Artifact Layer — Versioned Truth

All product and execution artifacts are versioned in Git.

Artifacts define:
- Product intent (initiatives, epics, specifications)
- Execution definitions (tasks, workflow definitions)
- Outcomes (deliverables, ADRs, audit records)

Git repositories are the authoritative source of truth. Runtime systems and databases exist only as projections of repository artifacts. Change is explicit. History is immutable. Truth is diffable.

Spine hosts the governed Git repository itself and owns the governance structures around change: PR state, review discussions, approval outcomes, and merge authority live in Spine's Run and discussion model. External forges integrate as *clients* of that governance engine — they can surface and forward, but they cannot authorize. See [Boundaries §2.1-§2.3](/product/boundaries-and-constraints.md) and [Git Integration Contract](/architecture/git-integration.md).

### 4.2 Execution Layer — Workflow Governance

A workflow engine interprets and enforces how work progresses.

Workflows define:
- Valid state transitions
- Required inputs and outputs
- Validation conditions
- Retry limits for automated steps
- Divergence and convergence points

Execution derives from artifacts and produces new artifacts.

### 4.3 Actor Layer — Hybrid Contributors

Actors execute workflow steps. Actors may be:
- Humans
- AI agents
- Automated systems

All actors operate under identical governance constraints. No actor has implicit authority. AI is an execution participant, not a decision authority.

---

## 5. Workspace Model — Isolation Boundary

A **workspace** is Spine's fundamental isolation boundary. Every governed repository, runtime state, projection state, and actor scope exists within exactly one workspace. Each workspace has a unique identifier used to address it in API requests and CLI commands.

### 5.1 What a Workspace Contains

A workspace is a named, isolated context that encapsulates:

- **A governed Git repository** — the authoritative source of truth for all artifacts within the workspace
- **Runtime state** — run executions, step progress, queue entries, actor assignments
- **Projection state** — query-optimized views derived from the repository
- **Actor scope** — the set of actors (humans, AI agents, and automated systems) authorized to operate within the workspace

All existing product concepts — artifacts, workflows, runs, actors — exist within a workspace. A workspace is the boundary within which governance applies.

### 5.2 Isolation Guarantee

Workspaces are isolated from each other:

- One workspace cannot see or query another workspace's artifacts, runs, or projections
- One workspace's actors cannot operate in or authenticate against another workspace
- One workspace's Git history is independent of another workspace's history
- No operation can span multiple workspaces

This isolation is a product invariant, not an implementation detail. It holds regardless of how workspaces are deployed.

### 5.3 Relationship to Other Concepts

| Concept | Relationship to Workspace |
|---------|--------------------------|
| Artifacts | Exist within a workspace's governed repository |
| Workflows | Defined within a workspace; govern execution within that workspace |
| Runs | Execute within a workspace against that workspace's artifacts and actors |
| Actors | Registered within a workspace; scoped to that workspace's operations |
| Projections | Derived from a workspace's repository into that workspace's database |

A workspace is not a team, an organization, or a permission group. It is the structural boundary within which Spine's governance model operates.

### 5.4 Hosting Modes

Spine supports two hosting modes. Both provide identical workspace isolation guarantees — the difference is operational, not functional.

**Single mode** — one workspace per Spine instance. This is the default. Each workspace runs in its own process with its own database and Git repository. It is the simplest deployment model and provides the strongest operational isolation. A failure or misbehavior in one workspace cannot affect another.

**Shared mode** — multiple workspaces in one Spine instance. A single Spine process serves requests for multiple workspaces, resolving the correct workspace context at the request boundary. Each workspace still has its own database and Git repository — isolation is at the resource level, not the query level. Shared mode reduces operational overhead when workspace count grows.

Hosting mode is an operator decision, not a user-facing distinction. Users interact with workspaces the same way regardless of how they are deployed.

### 5.5 User Interaction with Workspaces

Users address a workspace by its unique identifier:

- **API** — workspace ID is included in each request (e.g., via request header). In single mode, workspace ID is optional — the runtime falls back to the single configured workspace for backward compatibility.
- **CLI** — workspace ID is set via the `--workspace` flag or persisted through `spine config set workspace <id>`.

System-level operations (health checks, metrics) are not scoped to a workspace and do not require a workspace identifier.

### 5.6 Repository Topology — Primary and Code Repositories

A workspace contains exactly one **primary repository** and zero-to-many **code repositories**. Single-repository workspaces remain the default; a workspace with no registered code repositories operates exactly as it did before multi-repo support and requires no migration.

| Type | Count | `kind` | Contains | Managed By |
|------|-------|--------|----------|------------|
| Primary | Exactly 1 | `spine` | Governance artifacts: initiatives, epics, tasks, ADRs, workflows, product and architecture documents | Spine (authoritative) |
| Code | 0 to N | `code` | Implementation code, configs, infrastructure | Teams (Spine creates branches during execution) |

The primary repository is the governance authority and the coordination ledger. Code repositories are execution targets. Governance artifacts only live in the primary repo; Spine does not scan code repositories for initiatives, tasks, or ADRs. This split is enforced by the system, not by convention.

Repository identity is workspace-scoped and immutable. The same upstream repository may be registered in different workspaces under different IDs, but a registered ID cannot be renamed within a workspace — only deregister and re-register changes it.

Detailed contracts: [Multi-Repository Workspaces](/product/multi-repository-workspaces.md), [Multi-Repository Integration](/architecture/multi-repository-integration.md), [Git Integration Contract](/architecture/git-integration.md).

### 5.7 Multi-Repository Runs

Tasks may declare which code repositories they affect via a `repositories` field. When the field is omitted, the task operates against the primary repo only and the run is single-repo.

When a task declares affected code repositories, a run spans those repositories alongside the primary repo:

1. The run creates a `spine/run/<artifact-id>-<slug>-<run-hex>` branch in the primary repo and in each affected code repo.
2. Step actors clone the relevant repository over Spine's git endpoint and commit work to its run branch.
3. On run completion, Spine merges the run branch in each code repository independently, then merges the primary repo's run branch last so the governance outcome reflects the code-side result.
4. If a merge succeeds in repository A but fails in repository B, the run transitions to the `partially-merged` state. Repository A's merge stands; repository B's branch is preserved for manual resolution; the run remains open until B is resolved or the task is explicitly closed.

The `partially-merged` state is a first-class run state surfaced by `run inspect` and the run API, with per-repo outcomes recorded in the primary repo. Operators see which repositories merged, which failed, and what conflicts remain.

### 5.8 Multi-Repo Constraints

These constraints are product invariants of the multi-repo model:

- **No cross-repo atomic transactions.** Merges are per-repo. There is no distributed two-phase commit and no rollback of a successful merge if a sibling repo fails. The `partially-merged` state is the explicit outcome of this trade-off.
- **Governance lives only in the primary repo.** Code repositories never become governance authorities. Initiatives, epics, tasks, ADRs, and workflow definitions are scanned only from the primary repo.
- **Workspace-level RBAC.** Authorization is scoped to the workspace, not per-repo. An actor authorized to operate in a workspace operates against all repositories registered in it. Per-repository permissions are not part of the v0.x model.
- **Workspace isolation extends to code repositories.** A code repo registered in workspace A is not visible to workspace B; the isolation guarantee in §5.2 holds for the full repository topology.

---

## 6. Polyrepo Use Case — Payments Platform

Consider a platform team that operates three repositories in production:

- `api-gateway` — public API layer
- `payments-service` — core payment processing
- `notification-service` — email and webhook delivery

Each repository has its own CI pipeline and release cadence. The team wants to govern product intent, execution traceability, and review through a single Spine workspace without consolidating into a monorepo.

**Setup.** A platform engineer registers the three code repositories against a Spine workspace whose primary repo holds governance artifacts. The repository catalog gains three entries with `kind: code` alongside the primary `kind: spine` entry.

**Intent.** A product owner authors an initiative ("Add rate limiting to checkout") and an epic with two tasks. One task — *Implement the rate limiter* — declares `repositories: [api-gateway, payments-service]`. A second task — *Notify on rate-limit rejections* — declares `repositories: [notification-service]`. Both tasks live as governed artifacts in the primary repo.

**Execution.** When the rate-limiter task is started, Spine creates `spine/run/...` branches in the primary repo, in `api-gateway`, and in `payments-service`. The runner for each step clones the relevant code repo through Spine's git endpoint and commits work to its run branch. An AI agent implements the gateway plumbing; a human implements the service-side enforcement. Each commit lands on the corresponding run branch under the same workflow governance.

**Convergence.** When the run completes, Spine merges the run branches in `api-gateway` and `payments-service` independently, then merges the primary repo's run branch to record the governance outcome. The primary repo's history references both code-side merges as evidence.

**Partial-merge handling.** Suppose `api-gateway`'s merge succeeds but `payments-service` has a conflict. The run transitions to `partially-merged`. The primary repo records `api-gateway: merged`, `payments-service: conflict`. The `api-gateway` run branch is deleted; the `payments-service` run branch is preserved. The task remains open. An operator resolves the conflict on the preserved branch, retries the merge, and the run advances to fully completed.

**Single-repo default still applies.** The notification task declared a single code repository; its run spans the primary repo and `notification-service` only. Tasks with no `repositories` field declared continue to operate against the primary repo alone, producing single-repo runs identical to v0.x behavior.

This walkthrough is normative for product behavior. Implementation contracts live in [Multi-Repository Workspaces](/product/multi-repository-workspaces.md) and [Multi-Repository Integration](/architecture/multi-repository-integration.md).

---

## 7. How Spine Differs from Existing Tools

| Tool Category | What It Does | How Spine Differs |
|--------------|-------------|-------------------|
| Issue trackers (Jira, Linear) | Actor-centric task management | Spine is artifact-centric — work is defined through versioned intent, not boards and sprints |
| CI/CD (GitHub Actions, Jenkins) | Build and deployment automation | Spine governs execution integrity — it does not build, test, or deploy |
| Project management (Asana, MS Project) | Scheduling and resource planning | Spine governs structural integrity between intent and execution, not timelines |
| Documentation (Confluence, Notion) | Knowledge management and publishing | Spine treats documents as governed artifacts, not content to browse |
| AI frameworks (LangChain, CrewAI) | LLM orchestration and agent tooling | Spine governs what agents may do, not how they reason internally |

Spine does not compete at the feature level with any of these tools. It operates at the coordination layer — governing intent, artifacts, and execution — and integrates with existing tools where needed.

See [Non-Goals](/product/non-goals.md) for explicit boundaries.

---

## 8. Key Principles

These principles are drawn from the [Charter](/governance/charter.md) and enforced by the [Constitution](/governance/constitution.md):

1. **Explicit intent before action** — no execution without versioned, reviewable intent
2. **Artifact-centric truth** — the repository is the source of truth, not runtime state
3. **Governed execution** — all work proceeds through defined workflows
4. **Actor neutrality** — humans and AI operate under identical constraints
5. **Controlled divergence** — parallel execution is intentional, with explicit convergence
6. **Reproducibility over speed** — execution paths must be reconstructible from artifacts

---

## 9. Who Spine Is For

Spine is designed for hybrid teams of humans and AI agents that need structural integrity between product intent and execution.

Primary personas:
- **Technical Leads** — governing execution and ensuring intent-to-delivery traceability
- **Product Owners** — authoring intent artifacts that remain connected to execution
- **Software Engineers** — executing tasks with clear context and governed workflows
- **Reviewers** — evaluating outcomes at governance checkpoints
- **Platform Engineers** — integrating Spine with external systems
- **AI Agents** — executing workflow steps under governed constraints

Spine is not designed for casual solo development workflows where governance is unwanted overhead.

See [Users and Use Cases](/product/users-and-use-cases.md) for full persona definitions.

---

## 10. Governance Hierarchy

Spine operates under a layered governance model:

1. **Charter** — defines purpose, philosophy, and structural model
2. **Constitution** — defines non-negotiable system constraints
3. **Guidelines** — define recommended practices and evolving standards

The Constitution must align with the Charter. Guidelines must align with both. No rule may contradict foundational principles.

See [Charter](/governance/charter.md), [Constitution](/governance/constitution.md), [Guidelines](/governance/guidelines.md).

---

## 11. Related Documents

This document is the authoritative product definition. It is supported by:

- [Users and Use Cases](/product/users-and-use-cases.md) — who Spine is for and how they use it
- [Non-Goals](/product/non-goals.md) — what Spine is not and will not do
- [Success Metrics](/product/success-metrics.md) — how Spine's success is evaluated
- [Boundaries and Constraints](/product/boundaries-and-constraints.md) — system boundaries and constitutional constraints
- [Multi-Repository Workspaces](/product/multi-repository-workspaces.md) — full product model for primary and code repository topology

---

## 12. Evolution Policy

This document is expected to evolve as the product matures.

Changes must be versioned in Git and must not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md).
