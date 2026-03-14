---
id: TASK-006
type: Task
title: Architectural Decision Records
status: Completed
epic: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
---

# TASK-006 — Architectural Decision Records

---

## Purpose

Capture core architectural decisions as ADRs so that design rationale is explicit, versioned, and auditable.

## Deliverable

`/architecture/adrs/`

Required ADRs (at minimum):

- ADR format and conventions
- Git as source of truth (operational implications)
- Database as projection (disposable DB principle)
- Workflow engine approach
- Actor security model at v0.x

## Acceptance Criteria

- ADR directory and format are established
- at least three core ADRs are written
- each ADR documents context, decision, and consequences

---

## Completion Note

All acceptance criteria met:

- **ADR directory established:** `/architecture/adr/` with 4 ADRs
- **Format established by convention:** consistent structure across ADR-001 through ADR-004 (Status, Date, Decision Makers, Context, Decision, Consequences)
- **4 ADRs written:**
  - ADR-001 — Workflow Definition Storage and Execution Recording
  - ADR-002 — Event Model (Derived Domain Events and Operational Events)
  - ADR-003 — Discussion and Comment Model
  - ADR-004 — Evaluation and Acceptance Model

Remaining items from the original required list (ADR format convention, Git as source of truth, disposable DB principle) are already covered by governance and architecture documents (Constitution §2/§8, Data Model). Actor security model will be addressed as part of TASK-008 (Access Surface v0.x). Future ADRs should emerge from implementation decisions rather than a pre-planned list.
