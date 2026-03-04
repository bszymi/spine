# Product Boundaries and Constraints

**Project:** Spine
**Version:** 0.1
**Status:** Living Document

---

## 1. Purpose

This document defines the structural and operational boundaries within which Spine operates, and the constraints that shape its design.

It serves as a bridge between the [Constitution](/governance/Constitution.md) (which defines invariants) and architecture (which must operate within them). Architecture decisions that violate boundaries or constraints defined here require formal amendment.

---

## 2. System Boundaries

### 2.1 What Spine Owns

Spine is responsible for:

- **Artifact governance** — defining, validating, and managing versioned artifacts in Git
- **Workflow execution** — interpreting workflow definitions and enforcing state transitions
- **Actor coordination** — assigning work to humans and AI agents under governed constraints
- **Intent-to-execution traceability** — maintaining structural links from product intent through to outcome
- **Drift detection** — identifying when execution diverges from intent
- **Audit trail** — recording what was done, by whom, and under what governance

### 2.2 What Spine Delegates

Spine explicitly does not own:

- **Code hosting** — Git repository hosting is delegated to GitHub, GitLab, or similar platforms
- **Build and deployment** — CI/CD pipelines are delegated to GitHub Actions, Jenkins, etc.
- **Model infrastructure** — LLM hosting and inference are delegated to external providers
- **Rich documentation** — publishing and knowledge discovery are delegated to documentation platforms
- **Project scheduling** — timelines, capacity, and resource planning are delegated to PM tools

See [Non-Goals](/product/non-goals.md) for the full boundary rationale.

### 2.3 Boundary Principle

When deciding whether Spine should own a capability or delegate it:

1. If the capability is about **governing intent, artifacts, or execution** — Spine owns it
2. If the capability is about **operating tools within a layer** — Spine delegates it
3. When in doubt, prefer integration over consolidation

---

## 3. Operational Constraints

These constraints are derived from the [Constitution](/governance/Constitution.md) and shape every architectural and product decision.

### 3.1 Git-Native (Constitution Section 2)

Spine is fundamentally dependent on Git.

- All durable execution artifacts must be versioned in Git
- The repository is the authoritative source of truth
- If runtime state conflicts with repository artifacts, the repository wins
- Spine must not require any storage system as a source of truth other than Git

**Architecture implication:** Any database, cache, or queue is an operational accelerator — not a source of truth. The system must be reconstructible from Git alone.

### 3.2 Artifact-Centric (Constitution Section 2, 3)

Work in Spine is defined through versioned artifacts, not through actor actions.

- No execution step may occur without a governing artifact
- Implicit or undocumented work is invalid
- Artifacts must be self-describing and reconstructable from repository history

**Architecture implication:** The system must always be able to answer "what artifact authorized this action?" for any execution step.

### 3.3 Governed Execution (Constitution Section 4)

All work must proceed through defined workflows.

- Workflow definitions must declare valid state transitions, required inputs/outputs, and validation conditions
- Execution paths not defined by a workflow are prohibited
- Automation must operate within defined governance

**Architecture implication:** The workflow engine must be able to reject actions that do not conform to workflow definitions.

### 3.4 Actor Neutrality (Constitution Section 5)

Humans, AI agents, and automated systems are interchangeable execution actors.

- All actors operate under identical workflow constraints
- No actor has implicit authority
- AI is an execution participant, not a decision authority

**Architecture implication:** The actor interface must be uniform. The system must not have special paths for human vs. AI execution.

### 3.5 Controlled Divergence (Constitution Section 6)

Parallel execution is permitted but must be explicit.

- Divergent results must be preserved and auditable
- Convergence must occur through explicit evaluation steps
- Silent overwriting of alternative outputs is prohibited

**Architecture implication:** The system must support branching execution paths and preserve all outcomes, even those not selected.

### 3.6 Reproducibility (Constitution Section 7)

Execution must be explainable from artifact history.

- Execution paths must be reconstructible from repository state
- Outcomes must be traceable to the artifacts that governed them
- Non-deterministic systems must declare their variability boundaries

**Architecture implication:** Every execution step must produce or reference a versioned artifact. Runtime-only state is insufficient.

### 3.7 Disposable Database (Constitution Section 8)

Runtime infrastructure (databases, caches, queues) is expendable.

- These systems are operational accelerators, not sources of truth
- If operational databases are lost, the system must be able to rebuild state from Git artifacts
- Operational state may be ephemeral; structural truth may not

**Architecture implication:** The data model must support full reconstruction from Git. Database state is a projection, not the source.

---

## 4. Integration Boundaries

### 4.1 Integration Model

Spine integrates with external systems through defined boundaries:

| Integration Type | Direction | Examples |
|-----------------|-----------|----------|
| Artifact import | External → Spine | Importing issues from Jira, specs from Confluence |
| Artifact export | Spine → External | Publishing status to Slack, syncing to issue trackers |
| Execution trigger | External → Spine | CI/CD webhook triggers workflow step |
| Execution delegation | Spine → External | Spine triggers a CI/CD pipeline or LLM call |
| Observation | External → Spine | Monitoring systems reading audit logs |

### 4.2 Integration Principles

1. **Spine is authoritative for artifacts** — external systems may mirror artifact state but must not override it
2. **External systems are authoritative for their domain** — Spine does not attempt to become the CI/CD engine or the code host
3. **Integration must be explicit** — no silent syncing or implicit data flow
4. **Integration failure must not corrupt artifacts** — if an external system is unavailable, Spine's artifact state remains valid

---

## 5. Constraints Summary

| Constraint | Source | Architecture Impact |
|-----------|--------|-------------------|
| Git is source of truth | Constitution §2 | DB is projection only; full rebuild from Git required |
| Explicit intent required | Constitution §3 | Every action needs a governing artifact |
| Governed execution | Constitution §4 | Workflow engine must enforce constraints |
| Actor neutrality | Constitution §5 | Uniform actor interface; no special paths |
| Controlled divergence | Constitution §6 | Preserve all outcomes; explicit convergence |
| Reproducibility | Constitution §7 | All steps produce versioned artifacts |
| Disposable database | Constitution §8 | State is reconstructible; DB is ephemeral |

---

## 6. Evolution Policy

This document is expected to evolve as architecture decisions are made and integration patterns are established.

Changes must be versioned in Git and must not contradict the [Charter](/governance/Charter.md) or [Constitution](/governance/Constitution.md).
