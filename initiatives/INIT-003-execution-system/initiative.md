---
id: INIT-003
type: Initiative
title: Execution System
status: Completed
owner: bszymi
created: 2026-03-22
last_updated: 2026-03-22
links:
  - type: related_to
    target: /initiatives/INIT-002-implementation/initiative.md
---

# INIT-003 — Execution System

---

## 1. Intent

Build the Spine execution system — the orchestrator that wires INIT-002's isolated components into a functioning, governed workflow runtime.

INIT-002 delivered domain model, artifact service, workflow engine, actor service, projection service, validation engine, HTTP gateway, store, scheduler, event router, observability, CLI, and Docker setup. Each module works in isolation but the system cannot execute a workflow end-to-end.

This initiative delivers that end-to-end execution capability: a system that can start a run, progress through steps, assign actors, produce artifacts, commit to Git, and complete — all under governed workflow control.

---

## 2. Scope

### In scope

- Engine orchestrator (run lifecycle, step progression, outcome routing)
- Actor delivery pipeline (queue consumers, provider integration, result ingestion)
- Executable workflow definitions (reference workflows, binding integration)
- Git orchestration (branch-per-run, commit model, merge strategy)
- Evaluation and acceptance model (task-level outcomes, successor tasks)
- Validation integration into workflow progression
- Execution reliability (retry, timeout, failure classification, recovery)
- Divergence and convergence orchestration
- Event emission and observability
- CLI completion and developer experience

### Out of scope

- GUI / web interface
- External system integrations (GitHub, Jira, Slack)
- Multi-repository support
- AI autonomy / advanced AI orchestration
- CI/CD features
- Documentation rendering

---

## 3. Success Criteria

This initiative is successful when:

1. A task can execute end-to-end through a governed workflow
2. Outcomes are durably committed to Git
3. Validation blocks invalid workflow progression
4. Parallel execution (divergence/convergence) is governed
5. Execution is reproducible from artifacts
6. No work occurs outside workflows

---

## 4. Primary Artifacts Produced

- `internal/engine/` — Engine orchestrator package
- `workflows/` — Reference workflow definitions
- Git orchestration layer in artifact service
- Database migrations for discussion/reliability tables
- CLI query and workflow subcommands

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- §2 Source of Truth — All durable outcomes versioned in Git
- §3 Explicit Intent — No execution without governing artifacts
- §4 Governed Execution — Work proceeds through defined workflows
- §5 Actor Neutrality — All actor types operate identically
- §7 Disposable Database — Runtime state is operational, not authoritative

---

## 6. Risks

- **Integration complexity:** Components were built in isolation; wiring them may reveal interface mismatches
- **Git orchestration:** Branch-per-run with Spine-owned merges is non-standard Git usage and may surface edge cases
- **Actor delivery:** Real AI provider integration introduces external dependency and latency

Mitigations:

- Phase 0 (First Working Slice) validates integration early before expanding scope
- Git orchestration builds on existing GitClient with incremental complexity
- Mock actor provider enables development without external dependencies

---

## 7. Work Breakdown

### Epics

| Epic | Title | Phase | Dependencies |
|------|-------|-------|-------------|
| EPIC-001 | Execution Core | 1 | None |
| EPIC-002 | Actor Delivery | 1 | EPIC-001 |
| EPIC-003 | Workflow Definitions | 1 | EPIC-001 |
| EPIC-004 | Git Orchestration Layer | 2 | EPIC-001, EPIC-003 |
| EPIC-005 | Evaluation & Outcomes | 2 | EPIC-001, EPIC-004 |
| EPIC-006 | Validation Integration | 2 | EPIC-001, EPIC-003 |
| EPIC-007 | Execution Reliability | 2 | EPIC-001 |
| EPIC-008 | Divergence & Convergence | 3 | EPIC-001, EPIC-004 |
| EPIC-009 | Event & Observability | 3 | EPIC-001 |
| EPIC-010 | Developer Experience | 4 | EPIC-001, EPIC-003 |

---

## 8. Exit Criteria

INIT-003 may be marked complete when:

- All epics are completed or explicitly deferred with rationale
- A task can execute end-to-end with durable Git outcomes
- The system is usable via CLI for governed workflow execution

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- Plan: `/tmp/next-steps.md`
