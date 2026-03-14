---
type: Product
title: Success Metrics
status: Living Document
version: "0.1"
---

# Success Metrics

---

## 1. Purpose

This document defines the criteria by which Spine's success is evaluated.

Spine's primary goal is structural integrity between product intent and execution — not adoption volume or feature count. Metrics must reflect this distinction.

---

## 2. Metric Categories

Success metrics are organized into two categories:

- **Structural metrics** — measure whether Spine achieves its core purpose (integrity, traceability, reproducibility)
- **Adoption metrics** — measure whether Spine is being used effectively by teams

Structural metrics take precedence. A system that is widely adopted but structurally unsound is failing.

---

## 3. Structural Metrics

### 3.1 Intent Traceability

Can product intent be traced from initiative through epic, task, and into delivered outcome?

**Indicators:**

- Every task links back to an epic and initiative
- Every deliverable artifact references its governing task
- No orphaned work exists (tasks without parent epics, deliverables without tasks)

**Evaluation:** Traverse the artifact graph from any outcome back to product intent. If the chain is complete, traceability is achieved.

---

### 3.2 Reproducibility

Can a past execution path be reconstructed from repository state alone?

**Indicators:**

- Workflow steps are recorded as versioned artifacts
- Decision rationale is captured in ADRs
- Actor contributions (human and AI) are identifiable in commit history
- No critical execution state exists only in runtime systems

**Evaluation:** Given a point in Git history, reconstruct what was intended, what was executed, and what was decided. If this is possible without external context, reproducibility is achieved.

---

### 3.3 Governance Compliance

Do all actors — human and AI — operate within defined workflow constraints?

**Indicators:**

- No artifacts are created outside governed workflows
- AI agent output conforms to allowed paths and types
- Workflow violations are detected and logged
- Exceptions are documented with rationale

**Evaluation:** Audit a sample of recent workflow executions. If all steps followed defined constraints (or documented exceptions), governance compliance is achieved.

---

### 3.4 Drift Detection

Can the system detect when execution diverges from intent?

**Indicators:**

- Changes to upstream artifacts (initiatives, epics) flag affected downstream work
- Stale tasks (tasks whose parent intent has changed) are identifiable
- No silent drift accumulates between intent and implementation

**Evaluation:** Modify an initiative's scope and verify that affected artifacts are surfaced for review.

---

### 3.5 Artifact Completeness

Are all durable system decisions and outcomes captured as versioned artifacts?

**Indicators:**

- Significant design decisions have corresponding ADRs
- Workflow definitions are versioned, not ad hoc
- Governance documents are current and internally consistent
- No critical knowledge exists only in conversations or tribal memory

**Evaluation:** Ask a new contributor to answer "What is Spine?", "How does it work?", and "What decisions were made?" using only repository artifacts. If they can, artifact completeness is achieved.

---

## 4. Adoption Metrics

### 4.1 Contributor Coverage

Are all team members (human and AI) contributing through governed workflows?

**Indicators:**

- All active contributors produce versioned artifacts
- AI agents operate within defined workflow constraints
- No significant work bypasses the artifact system

---

### 4.2 Artifact Freshness

Are artifacts kept current as the system evolves?

**Indicators:**

- Governance documents reflect current practices
- Task statuses are updated as work progresses
- Superseded artifacts link to their successors
- No stale artifacts remain marked as active

---

### 4.3 Workflow Utilization

Are defined workflows being followed rather than bypassed?

**Indicators:**

- Work follows the standard contribution workflow (branch, execute, commit, PR, merge)
- Exceptions to workflow are rare and documented
- New work types trigger creation of appropriate workflow definitions

---

## 5. Maturity Stages

Success is evaluated differently at each stage of Spine's maturity.

### 5.1 Foundation (Current)

**Focus:** Establish governance, product definition, and architecture.

**Key metrics:**

- Artifact completeness — governance and product documents exist and are consistent
- Intent traceability — initiative > epic > task chain is complete
- Contributor coverage — all work is captured as artifacts

### 5.2 Engine v0.x

**Focus:** Implement workflow engine and basic runtime.

**Key metrics:**

- Governance compliance — workflows are enforced, not just documented
- Reproducibility — execution paths are reconstructible from artifacts
- Drift detection — upstream changes surface affected downstream work

### 5.3 Production

**Focus:** Real teams using Spine for governed execution.

**Key metrics:**

- All structural metrics are continuously satisfied
- Adoption metrics show consistent usage patterns
- New contributors can onboard from artifacts alone
- AI agent contributions are bounded and auditable

---

## 6. Anti-Metrics

These are metrics that Spine explicitly does not optimize for:

- **Velocity** — number of tasks completed per unit time is not a success indicator
- **Output volume** — quantity of artifacts produced does not indicate structural integrity
- **Feature count** — more features does not mean better governance

If these metrics improve as a side effect of structural clarity, that is welcome. But optimizing for them directly would compromise Spine's purpose.

---

## 7. Evolution Policy

This document is expected to evolve as the system matures and new evaluation methods become available.

Changes must be versioned in Git and must not contradict the [Charter](/governance/Charter.md) or [Constitution](/governance/Constitution.md).
