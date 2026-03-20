---
id: ADR-005
type: ADR
title: Technology Selection for Spine v0.x Core Runtime
status: Accepted
date: 2026-03-20
decision_makers: Spine Architecture
links:
  - type: related_to
    target: /architecture/data-model.md
  - type: related_to
    target: /architecture/components.md
---

# ADR-005: Technology Selection for Spine v0.x Core Runtime

---

## Context

Spine is a governed execution runtime — not a traditional web application. The core system combines:

- API handling (CLI, HTTP)
- Background workflow execution and step orchestration
- Queue and event processing
- Git subprocess orchestration (commits, branches, merges)
- Projection rebuilds from Git
- Actor Gateway integration (LLM providers, CI/CD, human interfaces)

This mixed execution model — API + background workers + queue consumers + orchestration — drives the technology selection. The choice must align with Spine's architectural constraints:

- Single-process modular monolith for v0.x (per [Components](/architecture/components.md) §6.1)
- PostgreSQL for projections and runtime state (per [Data Model](/architecture/data-model.md) §7.2)
- Git as source of truth, accessed via the Artifact Service
- In-process queue for v0.x event handling
- CLI as primary interface initially; API minimal

---

## Options Considered

### Option A: Go

**Strengths:**

- Natural fit for mixed API + background execution workloads
- Lightweight goroutines handle concurrent API calls, queue consumers, workflow execution, and rebuild tasks without external worker infrastructure
- Single binary deployment — no runtime dependency chain
- Clean internal package boundaries support modular monolith with future service extraction
- Predictable performance and resource usage
- Strong standard library for HTTP, process execution, and database access
- Well-suited for AI-assisted development (explicit flows, composable components)

**Weaknesses:**

- Less expressive type system than Rust or TypeScript
- Error handling is verbose
- Fewer high-level abstractions for complex domain modeling

### Option B: Ruby (Rails or lightweight framework)

**Strengths:**

- Faster initial development velocity
- Rich ecosystem for web-style patterns (ORM, migrations, testing)
- Team familiarity

**Weaknesses:**

- Rails is optimized for CRUD/web applications, not runtime engines
- More runtime complexity (gem dependencies, background worker infrastructure like Sidekiq)
- Less natural fit for long-lived orchestration + queue + system-level concerns
- Harder to maintain strict architectural boundaries (risk of drifting into app-style design)
- Single-threaded model requires external process management for concurrency

### Option C: TypeScript (Node.js)

**Strengths:**

- Good for CLI tools and API clients
- Large ecosystem
- Potential code sharing between CLI and core

**Weaknesses:**

- Node.js runtime less suited for execution engines with mixed workloads
- More coupling to ecosystem/runtime behavior (event loop constraints)
- Less clean for long-lived orchestration + queue + system-level concerns
- Single-threaded event loop requires careful design for CPU-bound operations

---

## Decision

**Spine v0.x core runtime will be implemented in Go.**

Go is selected due to its strong fit for Spine's mixed execution model (API + background workflows + queue + Git orchestration), operational simplicity (single binary, no runtime dependencies), and natural support for the single-process modular monolith architecture.

---

## Supporting Technology Choices

### Database: PostgreSQL

PostgreSQL is confirmed as the database for both projection and runtime schemas.

**Rationale:**

- JSONB support for flexible metadata and link storage (per [Runtime Schema](/architecture/runtime-schema.md))
- Mature Go driver ecosystem (`pgx`)
- Single database instance acceptable for v0.x
- Strong indexing capabilities for artifact queries

### Git Interaction: Git CLI via Abstraction Layer

Spine will interact with Git by invoking the Git CLI as a subprocess, wrapped behind an internal abstraction layer.

**Rationale:**

- Git CLI is the most reliable and complete Git implementation
- No dependency on libgit2 bindings (which can have compatibility issues)
- The abstraction layer allows future replacement with libgit2 or platform API if needed
- Git CLI is available in all deployment environments
- Subprocess execution is natural in Go

**Abstraction contract:**

The Artifact Service accesses Git through an internal `GitClient` interface. The v0.x implementation shells out to `git` CLI. The interface is designed so that alternative implementations (libgit2, GitHub API) can be substituted without changing callers.

### Queue: In-Process

Event handling and step assignment queuing will be in-process for v0.x.

**Rationale:**

- Eliminates external infrastructure dependency
- Go channels provide natural in-process queue semantics
- Sufficient for v0.x scale (single process, moderate throughput)
- The queue interface is designed for future extraction to an external broker (Redis, RabbitMQ, NATS) when scaling requires it

### API Framework: Standard Library + Lightweight Router

The HTTP API will use Go's standard `net/http` package with a lightweight router (e.g., `chi` or `gorilla/mux`).

**Rationale:**

- Spine's API is operation-oriented, not REST-resource-heavy — a full framework is unnecessary
- Standard library provides sufficient HTTP handling
- Lightweight router adds path parameters and middleware without framework lock-in

### CLI: Go (Cobra or similar)

The CLI will be implemented in Go as part of the same codebase.

**Rationale:**

- Shares domain types and validation logic with the core
- Single binary distribution includes both server and CLI modes
- `cobra` is the standard Go CLI framework with mature tooling

---

## Consequences

### Positive

- Single binary deployment simplifies operations — no runtime dependencies, no package managers, no worker processes
- Go's concurrency model (goroutines) naturally supports Spine's mixed workload without external infrastructure
- Clean package boundaries enforce the modular monolith architecture
- The technology choice aligns with Spine's identity as infrastructure, not a web application
- AI-assisted development works well with Go's explicit, composable style

### Negative

- Go is more verbose than Ruby or TypeScript for domain modeling — more boilerplate for complex types
- Team members without Go experience will need onboarding
- Go's error handling requires discipline to avoid swallowing errors
- Less flexibility for rapid prototyping compared to dynamic languages

### Risks

- **Learning curve** — if the team is primarily experienced in Ruby/TypeScript, Go adoption adds friction. Mitigated by Go's simplicity and strong tooling.
- **Ecosystem gaps** — Go has fewer high-level workflow/orchestration libraries than Node.js or Ruby. Mitigated by Spine building its own engine (which is the core product).
- **Over-engineering risk** — Go's explicitness can lead to premature abstraction. Mitigated by keeping v0.x focused on the modular monolith with minimal interfaces.

---

## Future Evolution

- **CLI could be reimplemented in TypeScript** if a separate lightweight CLI distribution is needed — but Go CLI is the default for v0.x
- **External queue extraction** when scaling requires it (the in-process queue interface is designed for this)
- **Service extraction** when individual components need independent scaling (Go's package boundaries make this straightforward)
- **libgit2 or platform API** may replace Git CLI if subprocess overhead becomes a bottleneck

---

## Architectural Alignment

| Principle | How This Choice Supports It |
|-----------|----------------------------|
| Source of Truth (§2) | Git CLI provides full Git capabilities for authoritative state management |
| Disposable Database (§8) | PostgreSQL with Go's `pgx` driver; clean separation of runtime and projection schemas |
| Governed Execution (§4) | Go's explicit control flow makes workflow engine state transitions unambiguous |
| Reproducibility (§7) | Single binary with no runtime dependencies ensures consistent deployment |

---

## References

- [Data Model](/architecture/data-model.md) §7 — Storage technology guidance
- [System Components](/architecture/components.md) §6 — Deployment considerations
- [Runtime Schema](/architecture/runtime-schema.md) — PostgreSQL table definitions
- [Git Integration](/architecture/git-integration.md) — Git operational contract
- [API Operations](/architecture/api-operations.md) — API design philosophy
