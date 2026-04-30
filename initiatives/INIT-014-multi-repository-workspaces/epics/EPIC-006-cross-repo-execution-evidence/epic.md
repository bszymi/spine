---
id: EPIC-006
type: Epic
title: "Cross-Repo Execution Evidence"
status: In Progress
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
---

# EPIC-006 - Cross-Repo Execution Evidence

---

## Purpose

Prove that code repository changes satisfy governed intent without turning code repositories into governance authorities.

The primary repo remains the ledger. Code repos produce deterministic evidence: changed commits, check results, policy results, and ADR-linked validation outcomes.

---

## Scope

### In Scope

- Execution evidence model recorded in the primary repo
- Validation policy artifacts linked from ADRs or architecture docs
- Per-repo check execution and result capture
- Workflow preconditions for required evidence
- Reporting and query support for evidence status

### Out of Scope

- AI-only semantic validation as a blocking rule
- Full source-code indexing
- Build or deployment orchestration beyond invoking declared checks

---

## Primary Outputs

- Evidence schema and storage location
- ADR-linked validation policy format
- Check runner integration boundary
- Validation rules that consume evidence
- End-to-end evidence scenario tests

---

## Acceptance Criteria

1. A task can require evidence for each affected repository.
2. ADRs can link to deterministic validation policies.
3. Required checks produce structured results tied to repo, branch, and commit.
4. Missing or failed required evidence blocks publication.
5. Evidence is auditable from primary-repo history.
