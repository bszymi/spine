---
id: ADR-008
type: ADR
title: Workflow Lifecycle Governance
status: Accepted
date: 2026-04-17
decision_makers: Spine Architecture
---

# ADR-008: Workflow Lifecycle Governance

---

## Context

[ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) made workflow definitions a first-class API resource with dedicated `workflow.*` operations. What that decision did **not** address is how changes to a workflow definition are governed. Today `workflow.create` and `workflow.update` commit directly to the authoritative branch:

- There is no draft state — a half-written workflow is either on `main` or not at all.
- There is no explicit reviewer/approver step beyond whatever a human chooses to do at the Git commit level.
- Audit reduces to `git log` on the workflow file plus whatever trailers happen to be set.

The structural validator ([Workflow Validation](/architecture/workflow-validation.md)) catches syntactic and reference-integrity errors at write time. It does not — and structurally cannot — catch domain-logic errors: wrong step sequence, incorrect actor type for a manual step, subtle retry/timeout policy that looks valid but is operationally wrong. These are "structurally valid but semantically wrong" errors, and they are the errors most expensive to discover at runtime: a malformed workflow blocks Runs, may corrupt step-assignment, and often requires manual Git recovery to unwind.

[ADR-006](/architecture/adr/ADR-006-planning-runs.md) established the pattern for governing *new artifact introduction* via planning-mode Runs — branch + approval + merge. Workflow edits are the same shape of problem: a structured artifact whose changes need draft state, human review, and an audit trail. The infrastructure already exists; it has not been extended to workflow changes.

Finally, [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) fixed the backwards-compatibility story for workflow changes: Runs pin to the workflow commit SHA captured at `run.start`. An in-flight Run does not follow the workflow forward when the workflow is edited. That means workflow edits can be treated as ordinary branch work — merging a new workflow version cannot break Runs already bound to an earlier version.

---

## Decision

### 1. Workflow Edits Flow Through a Planning-Mode Run

Every `workflow.create` / `workflow.update` from a reviewer flows through a planning-mode Run bound to a dedicated governing workflow (`workflow-lifecycle.yaml`, see §3). Concretely:

- `workflow.create` called without a `write_context` starts a planning-mode Run under `workflow-lifecycle`, opens a branch (`spine/run/{runID}`), writes the initial workflow body on that branch, and returns `{ run_id, branch_name, workflow_id, workflow_path, trace_id }` to the caller.
- `workflow.create` and `workflow.update` called with `write_context { run_id }` commit to the run's task branch instead of the authoritative branch. Repeated edits on the same `run_id` stack commits on the same branch.
- The authoritative branch is untouched until the approval step completes.

This reuses the planning-run orchestrator, `write_context` resolution, and merge-on-completion machinery already built for ADR-006. No new edit protocol is introduced.

### 2. Approval Merges the Branch

`workflow-lifecycle.yaml` has two steps:

- **draft** — the actor authors or edits the workflow body on the branch. Completing this step transitions the run to `review`.
- **review** — a reviewer evaluates the change. Two outcomes:
  - `approved` — merges the run's branch into the authoritative branch. The workflow becomes Active at the merge commit. The governing run transitions to `committing` → `completed` via the standard merge path (ADR-006 §6).
  - `needs_rework` — loops back to `draft`, keeps the branch alive, preserves the run for further `workflow.update` calls on the same `run_id`.

### 3. The Governing Workflow Is Seeded, Self-Hosted

`spine init-repo` seeds `workflows/workflow-lifecycle.yaml` as part of the initial commit. Teams that want stricter governance (additional reviewers, extra automated checks, different rework policies) edit that one file.

Because the governing workflow is itself a workflow definition, editing it goes through its own flow. This is deliberate — the governance of workflow changes is expressed in the same vocabulary as the governance of work. The price is a potential bootstrap deadlock if the lifecycle workflow is itself broken; §5 addresses that directly.

### 4. Run Pinning Preserves Backwards Compatibility

Per [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md), a Run captures the workflow commit SHA at `run.start` and resolves steps against that commit for the run's entire lifetime. Merging a new workflow version does **not** rebase in-flight Runs onto the new version.

Rebasing an Active Run onto a newer workflow version is explicitly out of scope here and would require its own decision. The default remains: edit freely, existing work is not disturbed.

### 5. Operator Bypass for Recovery

The reviewer-driven flow above is the default. The operator role retains a direct-commit path:

- `workflow.create` / `workflow.update` with **no** `write_context` and a caller role of `operator` (or higher) commits directly to the authoritative branch, bypassing the Run entirely.
- The bypass is audit-logged with a distinguishing commit trailer (`Workflow-Bypass: true`) so an auditor can separate bypass commits from governed merges.

The bypass exists specifically for bootstrap deadlocks (the `workflow-lifecycle` workflow is itself broken and the governance flow cannot complete) and incident recovery. It is not a general-purpose fast path — reviewer role calls without `write_context` still start a planning Run.

### 6. Audit

Two distinct audit trails result:

- Governed merge: branch history + planning-run record (step executions, outcomes, reviewer identity) + merge commit. The reviewer and approval rationale are answerable from the Run record.
- Operator bypass: direct commit on the authoritative branch, tagged with `Workflow-Bypass: true` and the operator's identity in trailers.

Both are discoverable from `git log` on the workflow file; the Run record adds structured "who approved this and why" for the governed path.

---

## Consequences

### Positive

- Draft state for workflows — incomplete edits never touch the authoritative branch.
- Structural + domain review on every change. Domain errors ("wrong actor type for a step") are caught at review time, not at Run time.
- Audit trail for every governed change is answerable directly from the Run record.
- Self-hosted governance — teams extend lifecycle policy by editing one file, not by patching the gateway.
- No new machinery: reuses the planning-run orchestrator, `write_context` resolution, merge-on-completion, and authorization model.

### Negative

- Every workflow edit by a reviewer now costs a branch + approval. This is the intended cost but is a material change from "commit and go."
- Bootstrap concern: a fresh repo must have `workflow-lifecycle.yaml` before any workflow edit can be governed by it. The seed at `spine init-repo` addresses this; the operator bypass addresses the degenerate case where the seed is itself broken.
- Two distinct commit paths on workflow files (governed merge and operator bypass). Auditors must understand the distinction.
- Branch sprawl risk if planning runs are opened and abandoned. Deferred to standard run cancellation; revisited if it shows up in practice.

### Neutral

- No change to how workflow definitions are stored (ADR-001) or how they are routed in the API (ADR-007).
- No change to Run pinning behavior. Existing Runs stay bound to their captured workflow commit SHA regardless of subsequent workflow edits.

---

## Architectural Implications

- **`internal/workflow/service.go`** gains `WriteContext` support mirroring `internal/artifact/service.go` — writes target a run's task branch when a branch is supplied.
- **`internal/gateway/handlers_workflows.go`** accepts `write_context { run_id }` on create/update, resolves branch via the existing `resolveWriteContext` helper, and distinguishes reviewer vs operator behavior on absent `write_context`.
- **`internal/engine/` / planning-run orchestrator** extends `PlanningRunStarter` so `workflow-lifecycle` runs produce a branch, return it to the caller, and merge on approval like an artifact-creation run does.
- **`internal/workflow/binding.go`** resolves `applies_to: [Workflow]` so the `workflow-lifecycle` workflow binds to `workflow.*` operations.
- **`internal/auth/permissions.go`** documents the operator-bypass rule explicitly (no `write_context` + operator role → direct commit; reviewer role → planning-run path).
- **`spine init-repo`** seeds `workflows/workflow-lifecycle.yaml` so fresh repos can edit workflows through the governed path from day one.
- **Docs**: `api-operations.md` §3.2, `access-surface.md` §3.2.2, `validation-service.md`, `README.md`, and `docs/integration-guide.md` reflect the new default flow and the bypass. ADR-007 §Future Work gains a pointer to this ADR as the follow-up on edit governance.

---

## Alternatives Considered

### A. Commit Directly, Rely on Post-Commit Review

Leave `workflow.create/update` as direct commits and rely on downstream review (PR, post-hoc audit).

Rejected. This is the current state. Its failure mode is exactly what motivates this ADR: structurally valid but semantically wrong workflows land before review, and the "who approved this" question has no answer inside the system.

### B. A New Edit-Protocol Machinery Separate From Planning Runs

Build a purpose-built "workflow edit session" entity with its own lifecycle, review, and merge.

Rejected. It duplicates what planning runs already do. The cost of forking the machinery exceeds the cost of extending it, and the two models would inevitably drift. Treating workflow edits as a planning run lets them share state-machine, merge path, authorization, and observability with the existing artifact-creation flow.

### C. Per-Workflow Custom Lifecycle at Create Time

Let the caller specify a lifecycle workflow at `workflow.create` time.

Rejected for the default path. Defaulting to a seed workflow and letting teams edit that seed achieves the same flexibility with far less API surface. Teams that want stricter policies already get them for free — they edit `workflow-lifecycle.yaml`, and the change itself flows through the lifecycle workflow (except for the bootstrap case, which the operator bypass handles).

### D. No Operator Bypass

Require every workflow edit to go through the governed flow, with no escape hatch.

Rejected. The self-hosted lifecycle has a known failure mode: if `workflow-lifecycle.yaml` itself is broken, no governed edit can succeed, including the edit that would fix it. The bypass is the minimum escape hatch needed to recover, scoped to operator role and loudly audited.

---

## Links

- [ADR-001 — Workflow Definition Storage and Execution Recording](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md)
- [ADR-006 — Planning Runs for Governed Artifact Creation](/architecture/adr/ADR-006-planning-runs.md)
- [ADR-007 — Workflow Definitions as a Separate API Resource](/architecture/adr/ADR-007-workflow-resource-separation.md)
- [Workflow Definition Format](/architecture/workflow-definition-format.md)
- [Workflow Validation](/architecture/workflow-validation.md)
- [api-operations.md](/architecture/api-operations.md)
- [access-surface.md](/architecture/access-surface.md)
- Initiative: [INIT-017 — Workflow Lifecycle Governance](/initiatives/INIT-017-workflow-lifecycle/initiative.md)
