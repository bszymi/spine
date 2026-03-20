# Spine

Spine is a Git-native Product-to-Execution System.

It transforms explicit product intent into governed, observable, and reproducible execution across hybrid teams of humans and AI agents.

Instead of managing work through tickets scattered across tools, Spine treats work as versioned artifacts inside a repository, where intent, architecture, and implementation are structurally connected.

The repository is a shared cognitive model — a single contextual source of truth that enables humans and AI agents to reason about the system as a whole.

---

## Start Here

If you're new to the project:

1. Read the [Charter](/governance/charter.md) to understand the philosophy
2. Review the [Product Definition](/product/product-definition.md)
3. Explore the [Architecture](/architecture/domain-model.md)

---

## Why Spine Exists

Modern software teams suffer from structural drift:

- Product intent becomes vague or outdated
- Tickets detach from the original purpose
- Automation runs without governance
- AI produces outputs without structural alignment
- Decisions become invisible over time
- Knowledge fragments across disconnected tools

Spine addresses this by introducing structural integrity between intent and execution, and by maintaining alignment across all project knowledge layers as the system evolves.

---

## Core Idea

Spine is built on a simple but powerful model.

Artifacts define truth.
Workflows define execution.
Actors perform actions.

This creates three structural layers.

---

## Artifact Layer

Git-versioned product and execution artifacts.

Examples:
- Product specifications
- Architecture documents and ADRs
- Initiative, Epic, and Task definitions
- Governance documents (Charter, Constitution, Guidelines)

Git is the source of truth. All artifacts are self-describing, versioned, and diffable.

---

## Execution Layer

A workflow engine governs how work progresses.

Workflows define:

- Valid state transitions
- Preconditions and required outputs
- Validation conditions
- Retry limits for automated steps
- Divergence and convergence points

Execution produces new artifacts. Only durable outcomes are committed to Git.

---

## Actor Layer

Actors execute workflow steps.

Actors may be:

- Humans
- AI agents
- Automated systems

All actors operate under the same governance rules. AI is treated as an execution actor, not a decision authority.

---

## Repository Structure

```
/
├── README.md
├── CONTRIBUTING.md
│
├── governance/
│   ├── Charter.md
│   ├── Constitution.md
│   ├── guidelines.md
│   ├── style-guide.md
│   ├── repository-structure.md
│   ├── naming-conventions.md
│   └── artifact-schema.md
│
├── product/
│   ├── product-definition.md
│   ├── users-and-use-cases.md
│   ├── non-goals.md
│   ├── success-metrics.md
│   └── boundaries-and-constraints.md
│
├── architecture/
│   ├── domain-model.md
│   ├── components.md
│   └── adr/
│       ├── ADR-001-workflow-definition-storage-and-execution-recording.md
│       ├── ADR-002-events.md
│       ├── ADR-003-discussion-and-comment-model.md
│       └── ADR-004-evaluation-and-acceptance-model.md
│
├── initiatives/
│   └── INIT-001-foundations/
│       ├── initiative.md
│       └── epics/
│           ├── EPIC-001-governance-baseline/
│           ├── EPIC-002-product-definition/
│           ├── EPIC-003-architecture-v0.1/
│           └── EPIC-004-governance-refinement/
│
└── templates/
    ├── initiative-template.md
    ├── epic-template.md
    ├── task-template.md
    └── adr-template.md
```

---

## Key Documents

### Governance

- [Charter](/governance/charter.md) — Purpose, philosophy, and structural model
- [Constitution](/governance/constitution.md) — Non-negotiable system constraints and invariants
- [Guidelines](/governance/guidelines.md) — Recommended practices and evolving standards
- [Artifact Schema](/governance/artifact-schema.md) — YAML front matter schema per artifact type
- [Repository Structure](/governance/repository-structure.md) — Folder layout and artifact taxonomy
- [Naming Conventions](/governance/naming-conventions.md) — Artifact ID and naming rules
- [Style Guide](/governance/style-guide.md) — Markdown formatting and metadata conventions

### Product

- [Product Definition](/product/product-definition.md) — What Spine is and how it differs from existing tools
- [Users and Use Cases](/product/users-and-use-cases.md) — Target personas and concrete use cases
- [Non-Goals](/product/non-goals.md) — What Spine explicitly does not aim to do
- [Success Metrics](/product/success-metrics.md) — Structural and adoption metrics
- [Boundaries and Constraints](/product/boundaries-and-constraints.md) — What Spine owns vs delegates

### Architecture

- [Domain Model](/architecture/domain-model.md) — Core entities and relationships
- [System Components](/architecture/components.md) — Runtime components, boundaries, and interaction flows
- [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) — Workflow definition storage and execution recording
- [ADR-002](/architecture/adr/ADR-002-events.md) — Event model (derived domain events and operational events)
- [ADR-003](/architecture/adr/ADR-003-discussion-and-comment-model.md) — Discussion and comment model
- [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md) — Evaluation and acceptance model

### Contributing

- [Contributing Guide](/CONTRIBUTING.md) — How to contribute to the project

---

## Philosophy

Most tools are actor-centric. They focus on people performing tasks.

Spine is artifact-centric. Work is defined through versioned intent. Execution derives from artifacts. Actors operate within governed workflows.

In a world where AI can generate enormous amounts of output, structure becomes the limiting reagent.

Spine provides that structure.

---

## Status

Foundations phase (INIT-001).

### Completed

- Governance baseline — Charter, Constitution, Guidelines, repository conventions, artifact templates
- Product definition — users, use cases, non-goals, success metrics, boundaries, product concept
- Domain model — core entities, relationships, lifecycles, constitutional alignment
- Architectural decision records — workflow storage, event model, discussion model, evaluation model
- System components — Access Gateway, Artifact Service, Workflow Engine, Projection Service, Actor Gateway, Event Router, Validation Service
- Governance refinement — artifact schema, Charter alignment, Constitution validation rules, Guidelines validation guidance

### In Progress

- Architecture v0.1 — data model, API surface, observability model

### Next

- Implementation planning based on completed architecture
