---
id: TASK-003
type: Task
title: "Split internal/store/postgres.go along existing concern boundaries"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-003 — Split postgres.go

---

## Purpose

`internal/store/postgres.go` is 1966 lines and owns 14 concerns (runs, step-executions, actors, tokens, divergence, branches, assignments, artifact/workflow/execution projections, links, sync state, skills, subscriptions, delivery queue, event log, discussions, comments, migrations). Column constants and scan helpers are already co-located per concern (e.g. `runColumns`+`scanRun(s)` around L96, `stepExecColumns`+`scanStepExecution(s)` around L228), and the file carries `── X ──` dividers that mark the natural splits. Should land after TASK-002 so the helper extraction benefits land in one place first.

---

## Deliverable

Split into concern-per-file, keeping the `store` package:

- `postgres.go` — `Postgres` type, constructor, pool plumbing.
- `postgres_runs.go` — runs + step-executions.
- `postgres_actors.go` — actors + tokens.
- `postgres_divergence.go` — divergence + branches + convergence.
- `postgres_assignments.go` — assignments.
- `postgres_projections.go` — artifact/workflow/execution projections + sync state + links.
- `postgres_skills.go` — skills.
- `postgres_subscriptions.go` — subscriptions + delivery queue + event log.
- `postgres_discussions.go` — discussions + comments + threads.
- `postgres_migrations.go` — schema migrations.

No type changes, no interface changes, no method-signature changes.

---

## Acceptance Criteria

- Top-level `postgres.go` under 300 lines.
- No file over 700 lines.
- Store integration tests and scenario tests pass unchanged.
