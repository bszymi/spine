---
id: EPIC-001
type: Epic
title: Engine and Governance
status: Completed
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
owner: bszymi
created: 2026-10-23
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/initiative.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-016-governance-path-guard.md
---

# EPIC-001 — Engine and Governance

---

## 1. Purpose

The 2026-10-22 review found the INIT-023 governance controls sound. It also found three edges
where a control does not see what it should:

- **N-3.** Separation of duties (INIT-023 TASK-007) does not count the actor who started a
  planning run as an author, although that actor wrote the run's content. With the strict
  policy on, the starter can therefore review and accept their own artifact. This is the one
  finding SMP needs before its INIT-023 closing bump.
- **N-4 and N-7.** The workflow definitions are not as strict as the code that reads them:
  - three shipped validate steps pass on timeout;
  - a second YAML document in a workflow file is accepted and silently ignored.
- **N-5.** The governance-path guard (ADR-016) misses `<artifacts_dir>/workflows/` unless the
  embedder lists it.

---

## 2. Scope

### In scope

- `core/engine`: `run.go` (planning starters), `step_entry.go` (`runAuthor`) and
  `governance_guard.go` (starter record, guarded path set).
- `core/yamlsafe` (trailing documents) and the shipped `workflows/*.yaml` corpus.
- Tests, `KNOWN-LIMITATIONS.md`, `security-model.md` §4.4 and `CHANGELOG.md`.

### Out of scope

- Turning separation of duties on in SMP. That is SMP EPIC-004 TASK-011 owner question 2.
- Making `workflow.Validate` blocking (INIT-023 owner question 7 still stands).
- Propagating the N-4 change into existing workspace repositories that copied the shipped files.
  That is an operator action, announced in `CHANGELOG.md`.

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | PoCs → regression tests | SMP impact |
|---|---|---|---|---|---|---|
| TASK-001 | Separation of duties counts a planning run's starter as an author | N-3 | 1 | — | `scratch/governance/stepentry_test.go` `TestGV_SoD_PlanningStarterSelfReview`; `scratch/standalone/sa1` | yes (opt-in): `CreateEntry` must carry `domain.WithActor` once SMP turns SoD on |
| TASK-002 | Workflow definition strictness | N-4, N-5, N-7 | 2 | — | `scratch/governance/f3_guard_test.go` case `artifacts_dir_wf` | yes: N-7 is shared behaviour |

---

## 4. Acceptance Criteria

- **The zero value is today.** With no option set, every existing engine, gateway and scenario
  test passes unchanged. The one exception is TASK-002's argued default-on N-7 refusal, which is
  listed in initiative §7b.
- **N-3.** With separation of duties on, the actor who started a planning run cannot be bound to
  that run's review step, through either assign or claim. A planning run started with
  separation of duties on always records its starter.
- **N-4.** No shipped workflow step lets a timeout take its passing outcome into a review step.
- **N-5.** With the guard on, a governed merge that changes a workflow definition the projection
  would install is held, whatever `WorkflowDirs` the embedder passed.
- **N-7.** `workflow.Parse` refuses a file whose second YAML document is not empty.
- Both tasks' SMP compatibility gates pass with no SMP change.
