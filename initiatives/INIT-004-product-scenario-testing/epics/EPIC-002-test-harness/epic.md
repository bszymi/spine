---
id: EPIC-002
type: Epic
title: "Test Harness"
status: Completed
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-001-architecture-design/epic.md
---

# EPIC-002 — Test Harness

---

## Purpose

Build the foundational test environment that enables product scenario testing. The harness must be capable of creating isolated Spine instances with temporary Git repositories, managing test databases, running the Spine runtime, and orchestrating the full environment lifecycle (setup, execute, teardown).

---

## Key Work Areas

- Temporary Git repository creation and cleanup
- Test database provisioning and teardown
- Spine runtime bootstrapping within test context
- Environment orchestration coordinating all components

---

## Primary Outputs

- Test harness package with stable API for scenario tests
- Temporary Git repository management utilities
- Test database lifecycle management
- Runtime integration layer

---

## Acceptance Criteria

- A scenario test can create an isolated Spine environment in a temporary Git repository
- Test database is provisioned and torn down automatically per test
- Spine runtime can be started and stopped within the test harness
- Multiple tests can run in parallel without interference
- Environment cleanup is guaranteed even on test failure
