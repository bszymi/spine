---
id: INIT-004
type: Initiative
title: "Product Scenario Testing System"
status: Completed
owner: _TBD_
created: 2026-03-26
---

# INIT-004 — Product Scenario Testing System

---

## 1. Intent

Establish a product-level testing capability that validates Spine behaviour from a product perspective — not just code correctness. This system ensures that product intent (Charter, Constitution, artifacts) is enforced, workflows execute correctly, governance rules are respected, and the system behaves consistently end-to-end.

This complements existing unit tests (logic correctness) and integration tests (component interaction) by introducing scenario-based validation of full Spine behaviour.

---

## 2. Scope

### In scope

- End-to-end scenario execution
- Workflow validation (step transitions, approvals, rejections)
- Artifact validation (schemas, relationships, lifecycle)
- Governance enforcement testing
- Runtime and Git interaction testing
- Positive (golden path) and negative scenarios
- Resilience and recovery scenarios

### Out of scope

- Low-level unit testing
- UI testing
- Performance and load testing (future capability)

---

## 3. Success Criteria

This initiative is successful when:

1. A test harness exists that can spin up isolated Spine environments with temporary Git repositories
2. Golden path scenarios validate full lifecycle from init through approval
3. Negative scenarios detect and reject invalid states
4. Governance rules from the Constitution are enforceable and verified through tests
5. Runtime recovery from Git is proven through resilience scenarios
6. Same governance rules are validated for both human and AI actors

---

## 4. Primary Artifacts Produced

- Test harness package (`/internal/testharness/` or similar)
- Scenario definitions and execution engine
- Reusable assertion library for Spine validation
- Scenario test suites covering golden path, negative, governance, and resilience cases

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- Git as the single source of truth — all test state must be reconstructible from Git
- Governance is non-negotiable — tests must enforce the same rules the system enforces
- Deterministic and reproducible — scenario outcomes must be identical across runs
- Same rules for humans and AI — no actor-specific exemptions

---

## 6. Risks

- **Scope creep:** Scenario testing can expand indefinitely; strict phase boundaries mitigate this
- **Test environment complexity:** Spinning up full Spine environments may be slow or fragile
- **Coupling to internals:** Scenarios that depend on implementation details will break during refactors

Mitigations:

- Phase-gated delivery with clear exit criteria per epic
- Test harness abstracts environment setup behind a stable API
- Scenarios test product behaviour, not implementation details

---

## 7. Work Breakdown

### Epics

```
/initiatives/INIT-004-product-scenario-testing/
  /epics/
    /EPIC-001-architecture-design/
    /EPIC-002-test-harness/
    /EPIC-003-scenario-engine/
    /EPIC-004-artifact-validation/
    /EPIC-005-workflow-validation/
    /EPIC-006-governance-validation/
    /EPIC-007-resilience-testing/
```

### EPIC-001 — Architecture and Design

Purpose: Establish the architectural foundation before implementation. Define test harness architecture, scenario engine design, assertion patterns, and integration strategy.

Key work areas:

- Architecture specification
- Test strategy document
- Proof-of-concept spike

### EPIC-002 — Test Harness

Purpose: Build the foundational test environment capable of creating isolated Spine instances with temporary Git repositories, runtime, and database.

Key work areas:

- Temporary Git repository management
- Test database setup and teardown
- Spine runtime integration
- Environment orchestration

### EPIC-003 — Scenario Engine

Purpose: Implement the scenario definition format, step-by-step execution runner, assertion framework, and result reporting.

Key work areas:

- Scenario definition format
- Execution runner
- Assertion framework
- Result reporting

### EPIC-004 — Artifact Validation Scenarios

Purpose: Validate artifact creation, structure, relationships, and lifecycle through golden path and negative scenarios.

Key work areas:

- Artifact creation helpers
- Golden path scenarios
- Negative scenarios
- Relationship validation

### EPIC-005 — Workflow Validation Scenarios

Purpose: Validate workflow execution including step transitions, approvals, rejections, and invalid transition handling.

Key work areas:

- Workflow execution helpers
- Golden path workflow scenarios
- Invalid transition scenarios
- Approval and rejection scenarios

### EPIC-006 — Governance Validation Scenarios

Purpose: Validate Constitution enforcement, permission rules, AI actor governance, and divergence/convergence handling.

Key work areas:

- Constitution enforcement
- Permission validation
- AI actor governance
- Divergence and convergence

### EPIC-007 — Resilience Testing

Purpose: Validate system recovery from runtime loss, projection rebuild from Git, and state consistency after reconstruction.

Key work areas:

- Runtime recovery
- Projection rebuild
- Git-based reconstruction

---

## 8. Exit Criteria

INIT-004 may be marked complete when:

- All seven epics are completed
- Golden path, negative, governance, and resilience scenarios pass
- Test harness is stable and reusable for future scenario development
- Scenario results are deterministic and reproducible

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
