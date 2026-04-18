---
id: TASK-002
type: Task
title: "Implement branchprotect.Policy with Request, Decision, and bootstrap defaults"
status: Pending
work_type: implementation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/tasks/TASK-001-config-schema-and-parser.md
---

# TASK-002 — Implement branchprotect.Policy with Request, Decision, and bootstrap defaults

---

## Purpose

Build the policy engine that both enforcement paths consult. All evaluation logic lives here; call sites in EPIC-003 and EPIC-004 construct a `Request` and read a `Decision` without re-implementing rule semantics.

---

## Context

[ADR-009 §3](/architecture/adr/ADR-009-branch-protection.md) fixes the surface:

```go
type Request struct {
    Branch   string
    Kind     OperationKind // Delete | DirectWrite | GovernedMerge
    Actor    ActorIdentity
    Override bool
    RunID    string
    TraceID  string
}

type Policy interface {
    Evaluate(ctx context.Context, req Request) (Decision, []Reason, error)
}
```

Bootstrap defaults ([§1](/architecture/adr/ADR-009-branch-protection.md)) apply when no config file is present:

```yaml
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
```

---

## Deliverable

1. **`internal/branchprotect` package** exposing:
   - `Decision` (Allow, Deny), `OperationKind` (Delete, DirectWrite, GovernedMerge), `Reason` (machine-readable code + human message).
   - `Request` struct matching ADR-009 §3.
   - `ActorIdentity` struct (at minimum: `ID`, `Role`) — reuse existing identity types if one already fits; otherwise define a small internal shim.
   - `Policy` interface + a concrete implementation that takes a rule source (a function or interface that returns the current ruleset — keeps the package decoupled from the runtime table in TASK-003).
   - `BootstrapDefaults() []config.Rule` exported for the projection layer and for consumers that need to evaluate against defaults when the source returns empty.

2. **Evaluation logic:**
   - `Kind == GovernedMerge` is always allowed regardless of rules (per ADR-009 §2 — governed merges are the intended write path).
   - `Kind == Delete` on a branch matching a `no-delete` rule: Deny unless `Override == true` and actor role is operator+.
   - `Kind == DirectWrite` on a branch matching a `no-direct-write` rule: Deny unless `Override == true` and actor role is operator+.
   - `Override == true` with insufficient role: Deny, with a `Reason` that distinguishes "no override authority" from "rule does not permit" (so UI layers can render the right error).
   - Branches not matching any rule: Allow.

3. **Unit tests** covering every branch of the decision matrix above, plus:
   - Branches matched by a glob (`release/1.0` matched by `release/*`).
   - Empty rule source → bootstrap defaults applied.
   - Overlap (a branch matched by two rules) — union of protections applies.

---

## Acceptance Criteria

- `branchprotect.Policy` exists and is importable; no call site has been wired yet (that happens in EPIC-003 / EPIC-004).
- Every decision-matrix case above is covered by a named test.
- The package has no import cycle with `internal/artifact`, `internal/engine`, or `internal/githttp` — those packages depend on `branchprotect`, not the other way around.
- Package documentation (top-of-file comment on `policy.go` or equivalent) points a reader at ADR-009 as the normative spec.
