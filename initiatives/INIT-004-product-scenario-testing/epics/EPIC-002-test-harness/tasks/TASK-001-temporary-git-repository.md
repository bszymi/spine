---
id: TASK-001
type: Task
title: "Temporary Git Repository Management"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-002-test-harness/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-002-test-harness/epic.md
---

# TASK-001 — Temporary Git Repository Management

---

## Purpose

Implement utilities for creating, managing, and cleaning up temporary Git repositories for use in scenario tests. Each test must operate in an isolated repository to prevent interference.

## Deliverable

Test harness component providing:

- Create a new temporary Git repository with `git init`
- Seed the repository with baseline Spine structure (governance, templates)
- Commit helpers for staging and committing artifacts
- Automatic cleanup on test completion (including on failure)
- Support for parallel test execution with isolated repos

## Acceptance Criteria

- Temporary repositories are created in isolated temp directories
- Repositories are initialized with valid Git state
- Cleanup occurs automatically via `t.Cleanup()` or equivalent
- Multiple tests can create independent repositories concurrently
- Helper functions for committing files are available
