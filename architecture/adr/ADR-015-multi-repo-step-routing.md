---
id: ADR-015
type: ADR
title: Multi-repository step routing model
status: Accepted
date: 2026-05-02
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: related_to
    target: /architecture/multi-repository-integration.md
  - type: related_to
    target: /architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md
---

# ADR-015 — Multi-repository step routing model

---

## Context

[ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md)
established that a workspace governs one primary Spine repository
and zero or more registered code repositories, identified through
the catalog at `/.spine/repositories.yaml` and bound to runtime
connection details. [Multi-Repository Integration](/architecture/multi-repository-integration.md)
extended that model with a multi-repo run lifecycle: when a task
declares `repositories: [<id>, ...]`, the run cuts a branch with
the same name in every affected code repo plus the primary Spine
repo, and merges them independently (per
[EPIC-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md)).

What that document does not pin down is **which repository each
individual step operates on**. Architecture §4.3 sketches three
candidate rules:

1. Explicit `repository` field on the step definition.
2. The task's `repositories` list, if it contains exactly one code
   repo (the "implicit single code repo" rule).
3. Default to the primary Spine repo.

[TASK-007](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-007-step-routing-decision-adr.md)
calls this out as an unresolved design point that must be settled
before [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-004-step-repository-routing.md)
is implemented. The ambiguity cascades into EPIC-005 merge ordering
(does each step write to one repo or many?) and EPIC-006 evidence
collection (is evidence keyed by execution, by repo, or by an
(execution, repo) pair?). It also shapes the assignment payload
contract: is `repository_id` a single field, a list, or a fan-out
implied by step expansion?

The decision space:

- **Routing model.** Three candidates. (a) **Explicit-only** — a
  step targets exactly one repo, determined by `step.repository`
  with a fixed default. (b) **Fan-out** — a step without
  `step.repository` is expanded by the runtime into N executions,
  one per affected repo. (c) **Hybrid** — some step kinds (build,
  test) auto-fan-out, others (review, governance) do not.
- **Default for unannotated steps.** Two candidates. (i)
  `spine` always — every step without `repository` operates on
  the primary repo. (ii) Conditional — `spine` when the task has
  zero code repos, the single code repo when the task has exactly
  one, fail otherwise.

These choices interact with the existing v0.x limitations stated
in [Multi-Repository Integration §9](/architecture/multi-repository-integration.md):
no cross-repo atomic merge, no cross-repo divergence, no per-repo
RBAC. Any model that creates implicit per-step fan-out reopens
those limitations because each fanned-out instance needs its own
position in merge ordering, divergence scope, and assignment
delivery.

This ADR commits to a single, deterministic routing model so
TASK-004 implementation has no remaining design choices, and so
EPIC-005 and EPIC-006 can be built against a stable per-step
single-repo contract.

---

## Decision

**Explicit-with-default-spine routing. One step targets one repo.
No runtime fan-out in v0.x.**

### Resolution rule

Every step execution targets exactly one repository, called the
step's **target repository**. The target is determined by exactly
two rules, applied in order:

1. If `step.repository` is set on the step definition, the target
   is that repository ID.
2. Otherwise the target is `spine` (the primary repository ID
   reserved by ADR-013).

The "implicit single code repo" rule sketched in
[Multi-Repository Integration §4.3 #2](/architecture/multi-repository-integration.md)
is **rejected**. A workflow author who wants a step to operate on
a code repository writes `repository: <id>` — the routing never
flips silently based on `task.repositories` cardinality.

### Workflow step schema

`StepDefinition` gains one new optional field:

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `repository` | optional | string | Workspace-scoped repository ID. Must match `^[a-z0-9]+(-[a-z0-9]+)*$` (the catalog ID format from ADR-013). When omitted, the step targets `spine`. |

Only literal repository IDs are allowed in v0.x. Template
references (e.g. `{{ task.repositories[0] }}`) are explicitly
**not** part of this ADR; they are reserved for a future ADR
should reusable multi-code-repo workflows justify the templating
surface.

### Validation

Validation runs in two layers:

**Workflow validation** (at workflow load and at every commit
that touches a workflow YAML):

- `repository`, if present, matches the catalog ID format
  (`^[a-z0-9]+(-[a-z0-9]+)*$`, max 64 chars).
- Unknown fields under `step` are rejected (catches typos like
  `repositorY:` or `repos:`).

**Run-start validation** (at the moment a run is created from a
task):

For every step in the workflow definition (including steps behind
divergences that may never execute), the resolved target
repository must satisfy all of:

- **Existence.** The repository ID is registered in the workspace.
  For code repos, this means an entry in
  `/.spine/repositories.yaml`. For `spine`, this is implicit: per
  [ADR-013 §2.1](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md)
  every workspace has exactly one primary repo whose ID is `spine`,
  and single-repo workspaces may omit the catalog file entirely
  (the primary entry is synthesized from workspace config).
  `spine` therefore always passes the existence check.
- **Active.** For code repos, the runtime binding (per
  [ADR-013 §2.2](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md))
  has `status: active`. For `spine`, this check is vacuous — the
  primary repo is always considered active for as long as the
  workspace itself is active; there is no separate deactivation
  signal.
- **Task opt-in.** It is a member of `task.repositories ∪ {spine}`
  — the workflow may only target repositories the task has opted
  into. `spine` is always implicitly in this set even when the
  task's `repositories` list is empty or omits it (per
  [Multi-Repository Integration §4.1](/architecture/multi-repository-integration.md)
  the primary Spine repo always participates). A step with
  `repository: api-gateway` in a workflow attached to a task that
  lists only `payments-service` is rejected.

A run start that fails any of these checks returns a typed
`invalid_step_repository` error naming the offending step ID and
the unresolved repository, and the run is not created. No
in-flight rollback is required because the run never started.

The runtime context for the resolved target (the values the
assignment payload exposes; see *Assignment payload shape* below)
is sourced as follows. The assignment's `clone_url` is **always**
the workspace's git HTTP endpoint
(`<git-http-base>/git/{workspace_id}/{repository_id}`) for both
code repos and `spine` — runners get a uniform, in-workspace
fetch surface and the run branch is always reachable there. Code
repos additionally have a runtime binding holding `credentials_ref`
and a server-side `local_path` (per
[ADR-013 §2.2](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md));
`spine` does not require a binding row, and the server resolves
its filesystem path from the workspace's primary-repo
configuration (today's `cfg.RepoPath`, the same source the
existing single-repo path consumes). The runtime binding's
external `clone_url`, if set, is consulted only by server-side
clone/push paths and is never published into assignment payloads
in v0.x; likewise, server-side filesystem paths stay server-side.

### Assignment payload shape

Every step assignment payload includes the resolved target repo's
runtime connection context:

| Field | Type | Source |
|-------|------|--------|
| `repository_id` | string | Resolved per the rules above. |
| `clone_url` | string | The URL a runner uses to clone the resolved repository. **Always the workspace's git HTTP endpoint** per [Multi-Repository Integration §5.1](/architecture/multi-repository-integration.md): `<git-http-base>/git/{workspace_id}/{repository_id}`. The run branch is guaranteed to exist on the workspace's served copy (`createRunBranches` lands it locally before assignments are emitted; cf. EPIC-004 TASK-002), so the workspace endpoint is the only URL that consistently has `branch_name` reachable. The runtime binding's external `clone_url` (if set) is server-side metadata only and is **not** forwarded into assignment payloads in v0.x — bypassing the workspace's git HTTP would mean cloning a remote whose copy of the run branch is dependent on whether auto-push succeeded, which is best-effort. The server-side filesystem location for any repo (used internally to set `GIT_PROJECT_ROOT`) is likewise not exposed to runners. |
| `branch_name` | string | The run branch name; same in every affected repo per [Multi-Repository Integration §4.2](/architecture/multi-repository-integration.md). |

`repository_id` is a single value, not a list. The runner clones
exactly one repository, checks out exactly one branch, and writes
its outputs into that working tree.

### Step execution row shape

`StepExecution` gains one new field:

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `repository_id` | yes | string | Resolved at step activation; immutable for the lifetime of the row. Stored on the row so retries, audit queries, and recovery paths do not need to re-resolve from the workflow definition. |

Storing the resolved ID on the execution row matches the ADR-013
pattern of recording derived identity at the point of binding so a
later change in the upstream artifact does not retroactively
mutate execution history.

### Implications for EPIC-005 (merge ordering)

The merge ordering specified in
[Multi-Repository Integration §4.4](/architecture/multi-repository-integration.md)
operates on `Run.AffectedRepositories` (the task-derived list).
Step routing does not change that list — it only determines which
of those repos a given step's work lands in. Concretely:

- Multiple steps may target the same code repo. Their commits all
  land on the run branch in that repo. EPIC-005 merge ordering is
  unaffected: code repos merge first, primary Spine repo merges
  last.
- A run with two code repos in `AffectedRepositories` always
  merges both, even if every step targeted only one of them. The
  unused-code-repo case yields an empty branch (no commits beyond
  the cut). EPIC-005's per-repo outcome model decides how that
  empty-branch merge is recorded (likely as a no-op fast-forward,
  but the precise outcome name is EPIC-005's to name); this ADR
  does not constrain the choice.

### Implications for EPIC-006 (per-repo evidence)

Evidence is keyed by `(execution_id, repository_id)`. Because
this ADR makes `repository_id` a single value per execution row,
every piece of evidence has exactly one repo to attribute to —
the execution row's `repository_id`. EPIC-006 does not need an
N-way evidence aggregation across step instances.

### Implications for runner clone context

The runner receives one `clone_url` and one `branch_name` per
assignment. The clone command in
[Multi-Repository Integration §4.3](/architecture/multi-repository-integration.md)
remains as documented: clone exactly the resolved repo at the
run branch.

### Single-repo migration impact

Zero behavior change for existing single-repo workspaces and
existing workflows:

- Single-repo tasks have `repositories: []`. Workflow steps that
  omit `repository` resolve to `spine`. Runs proceed exactly as
  they do today.
- No existing workflow YAML needs to change. The new field is
  optional and absent from every committed workflow definition
  on `main` as of this ADR's date.
- Single-repo workspaces continue to operate without
  `/.spine/repositories.yaml` (per ADR-013 §2.1's catalog-presence
  rules). Run-start validation only consults the catalog when one
  exists; in its absence, the workspace behaves as if a single
  `kind: spine` entry existed and every step's resolved
  `repository` (always `spine`) trivially passes the catalog
  membership check.

### Schema versioning

The workflow schema gains an additive optional field. The
canonical workflow schema documents — [Workflow Definition
Format](/architecture/workflow-definition-format.md) §3.2 and
[Workflow Validation](/architecture/workflow-validation.md) §3.3
— are updated alongside this ADR to declare the `repository`
field, its format constraint, and its run-start validation rules.

**Compatibility envelope.** The workflow schema version is **not**
bumped. The pre-TASK-004 workflow parser
(`internal/workflow/parser.go` → `yamlsafe.DecodeInto`) calls
`yaml.Node.Decode` without `KnownFields(true)`, so unknown YAML
keys are **silently dropped**. That is benign for old workflows
(which never set `repository`) but it creates a misrouting risk
during the TASK-004 rollout: a pre-TASK-004 binary parsing a new
workflow YAML that uses `repository: payments-service` would
silently drop the field and route the step to `spine` instead of
`payments-service`. The fix lives in TASK-004, not in this ADR;
this ADR specifies the requirement so the implementation cannot
overlook it.

TASK-004 MUST therefore satisfy three rollout invariants:

1. **Reconcile `StepDefinition` with the existing on-disk
   corpus before flipping strictness.** Several committed
   workflow YAMLs (e.g., `workflows/adr-creation.yaml`,
   `workflows/document-creation.yaml`) already use step-level
   keys that the current `domain.StepDefinition` struct does not
   declare — at minimum `description:`. Turning on
   `KnownFields(true)` against today's struct would reject those
   workflows even though they do not use `repository`. TASK-004
   therefore audits every committed workflow YAML, lifts every
   used-but-undeclared step field into `StepDefinition` (most
   plausibly as `Description string \`json:"description,omitempty"
   yaml:"description,omitempty"\`` and similar), and only then
   enables strict decoding. Stripping the fields from the YAMLs
   instead is acceptable but loses authoring intent; declaring
   them is preferred.
2. **Parser strictness ships before any workflow uses
   `repository`.** With the corpus reconciled, the workflow parser
   is upgraded to reject unknown step fields
   (`yaml.NewDecoder(...).KnownFields(true)` on the typed
   `StepDefinition` decode, mirroring the
   [ADR-014](/architecture/adr/ADR-014-validation-policy-as-governed-artifact.md)
   pattern for validation policies). This must land and roll out
   to every environment that parses workflows — git server, run
   starter, validation service, CLI — before any workflow YAML
   commits a `repository:` value. While in transit, the field is
   reserved but unused; the strict parser tolerates omission.
3. **Forward compatibility for the existing corpus is preserved.**
   Old workflow YAMLs (no `repository` field anywhere) continue
   to parse cleanly against the post-TASK-004 binary. This is the
   entire existing on-`main` corpus as of this ADR's date, so the
   rollout is a no-op for already-committed workflows.

The artifact front-matter `repositories` list on Task continues to
use its existing schema as adopted in EPIC-002.

---

## Consequences

### Positive

- **Deterministic routing.** Two rules, applied in order, produce
  exactly one repository per step. No reasoning about
  `task.repositories` cardinality, no reasoning about step type.
- **No invisible defaults.** A workflow author looking at a step
  definition can answer "which repo does this run on?" by reading
  the `repository` field (or noting its absence and reading
  `spine`). The answer never depends on the task the workflow is
  attached to.
- **EPIC-005 and EPIC-006 stay simple.** Per-repo merge outcomes
  and per-(execution, repo) evidence are the natural fit for a
  one-execution-per-step-per-repo model. No fan-out aggregation
  layer is needed.
- **Zero migration cost** for single-repo workspaces and the
  existing `task-default` workflow.
- **Future fan-out is non-breaking.** Adding `step.fan_out:
  <strategy>` later expands the rule without invalidating existing
  workflows: the resolution rule becomes "if `fan_out` is set,
  expand; else apply rule 1 then rule 2".

### Negative

- **Annotation cost in multi-code-repo workflows.** A workflow
  used by a task with multiple code repos must annotate each
  code-repo step with `repository: <id>`. Until template
  references are added (future ADR), one workflow file pins to a
  fixed set of code repo IDs. Workspaces that need the same step
  shape across different code repo sets must duplicate the
  workflow.
- **Validation surface grows.** Run-start validation must
  enumerate every step in the workflow (including divergence
  branches that may not execute) and resolve each one's target
  repo against the catalog and `task.repositories`. This is
  cheap — workflow definitions are small — but it adds a step to
  run-start beyond the existing single-repo path.
- **`task.repositories ∪ {spine}` is the only opt-in surface.**
  A workflow cannot target a repo the task has not declared. This
  is the right default (it makes `task.repositories` an honest
  declaration of blast radius), but it means a misconfigured task
  produces a run-start failure rather than a silent routing
  decision. Operators must understand the error message names the
  step and the missing repo.

### Neutral

- The architecture document
  [Multi-Repository Integration §4.3](/architecture/multi-repository-integration.md)
  is updated alongside this ADR to remove the "implicit single
  code repo" rule. The rule was a sketch, never implemented; this
  ADR's resolution rule supersedes it.
- `step.repository` accepts `spine` as an explicit value, not just
  catalog-defined code repo IDs. This is the same identifier
  reserved by ADR-013 for the primary repo, so a workflow author
  who prefers explicit-everywhere can write `repository: spine`
  on every governance step. It is exactly equivalent to omitting
  the field.
- The new field on `StepExecution` (`repository_id`) is required
  going forward but carries a trivial backfill path for in-flight
  v0.x rows: every existing execution has implicit
  `repository_id = "spine"`. Backfill is not a migration concern
  in practice because v0.x runs never persist beyond a runtime
  rebuild (Constitution §8) and INIT-014 has not yet shipped any
  multi-repo runs.

---

## Alternatives Considered

### Fan-out — runtime expands a step into one execution per repo

A step without `repository` is expanded at activation time into N
executions, one per `task.repositories` entry (and optionally one
for `spine`). Each execution gets its own assignment, its own
runner, and its own evidence row.

**Rejected.** The model cascades complexity into every downstream
contract:

- **Merge ordering (EPIC-005)** — fan-out introduces N parallel
  executions per step. The next step in the workflow only proceeds
  once "the step" completes, but "the step" is now an aggregate
  over N executions with potentially divergent outcomes. Merge
  ordering must reason about whether per-instance failures retry
  individually or fail the group, and the ledger must record N
  outcomes per step rather than one.
- **Evidence aggregation (EPIC-006)** — a single step "outcome"
  now means "all N fanned-out instances completed with the same
  outcome", which requires a parent/child execution model and an
  N-way reducer. Evidence per-repo is no longer a one-to-one
  mapping.
- **Step state machine** — `StepExecution.Status` is per-row;
  fan-out introduces a "step group" status. Failure modes
  multiply: one instance fails, do we retry just that one or all?
  Does the step's overall status reflect the worst-case instance
  or the best-case?
- **Divergence (Constitution §6)** — a fanned-out step inside a
  divergence branch raises "which fanout instance owns the
  divergence?" with no clean answer.

The complexity is real and the demand is hypothetical: no current
or near-term task in INIT-014 needs the same step run on multiple
repos. When the demand materializes, fan-out can be added as a
non-breaking superset by introducing `step.fan_out: <strategy>`
opt-in. Today's decision keeps the surface small.

### Hybrid by step kind — auto-fan-out for build/test, explicit elsewhere

Some step kinds (build, test, lint, deploy) auto-expand to N
executions; others (review, approval, governance) target a single
repo. The mapping lives in a fixed `step.type → routing` table
defined in this ADR.

**Rejected.** v0.x step types are operational in spirit — they
describe `manual` vs `automated` vs `review` to drive the
execution mode and assignment shape, not the cross-repo blast
radius. Coupling routing semantics to the type taxonomy mixes two
unrelated concerns and forces the type vocabulary to grow whenever
a new cross-repo behavior appears. It also makes routing
non-orthogonal to step type: an "automated build" step that
should target only one repo (e.g., a release build of a single
service) cannot opt out of fan-out without a special-case escape
hatch. The escape hatch is `step.repository`, which means we end
up with both the auto-fan-out table and the explicit field —
two ways to spell the same intent.

### Implicit single code repo default — `task.repositories` cardinality drives default

When a task has exactly one code repo and a step has no
`repository` field, the step targets that code repo. When the
task has zero code repos, the step targets `spine`. When the task
has 2+ code repos and the step has no `repository`, run start
fails with `ambiguous_step_repository`.

**Rejected.** This is what
[Multi-Repository Integration §4.3 #2](/architecture/multi-repository-integration.md)
sketches. Two problems:

- **Surprise for governance steps in single-code-repo tasks.** A
  task with `repositories: [payments-service]` typically still
  has a final review step that updates governance YAML in
  `spine`. Under the implicit rule, that step silently routes to
  `payments-service` unless the author writes `repository: spine`.
  The rule punishes the common case (governance step in a
  single-code-repo task) to save typing in the rare case (every
  step is code-repo work).
- **Routing depends on task shape, not workflow shape.** A
  workflow's behavior changes based on which task it is attached
  to. The same workflow run against `task.repositories: []` vs
  `task.repositories: [a]` routes the same step to two different
  repos. That is exactly the kind of implicit dependency the
  Constitution's Explicit Intent principle (§3) tries to prevent.

The cost of rejecting this rule is one extra `repository: <id>`
annotation per code-repo step in workflows used with
single-code-repo tasks. That is a one-time author cost, paid
once per workflow rather than once per run.

### Multi-repo step — single execution clones N repos

A step targets a list of repos; the runner clones all of them
into sibling directories under a workspace root and runs against
the bundle. Evidence is one outcome per (execution, repo).

**Rejected.** The runner working directory becomes ambiguous
(which repo's branch is "the" current branch?), the assignment
payload becomes a list rather than a value, and the
one-execution-per-step invariant breaks at the storage layer
(evidence rows are now keyed by a tuple, but `StepExecution.Status`
is still per-row). It also conflates "this step needs to read N
repos" (a clone-context concern) with "this step writes to N
repos" (a merge concern). The clone-context need is real but is
better solved by a runner-side multi-clone helper than by reshaping
the step model.

---

## Migration

This ADR is design-only. The full implementation is
[TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-004-step-repository-routing.md).

For workflows already on `main` as of this ADR's date:

- No file changes are required. Every existing workflow's steps
  omit `repository`; every step resolves to `spine`; every run
  proceeds as today.
- The schema gains an optional field; existing YAML remains valid.

For new multi-code-repo workflows authored after this ADR
**and after TASK-004 has shipped to every parser-bearing
environment**:

- Each code-repo step declares `repository: <id>` explicitly.
- Governance/review steps may omit `repository` (defaulting to
  `spine`) or write `repository: spine` for clarity.
- Workflow validation rejects unknown step fields, so typos
  surface at the editor / CI level rather than at run time.

**Do not commit `repository:` in any workflow YAML before
TASK-004 has rolled out everywhere that parses workflows.** Per
*Schema versioning* above, today's parser silently drops the
field and routes the step to `spine` — committing the field
during the rollout window creates the exact misrouting this ADR
is designed to prevent. The
[Workflow Definition Format](/architecture/workflow-definition-format.md)
schema doc marks the field as reserved for the same reason.

---

## Links

- Initiative: [INIT-014](/initiatives/INIT-014-multi-repository-workspaces/initiative.md)
- Epic: [EPIC-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md)
- Implementation task: [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-004-step-repository-routing.md)
- Decision task: [TASK-007](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-007-step-routing-decision-adr.md)
- Companion ADR: [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) (catalog and runtime binding)
- Architecture: [Multi-Repository Integration](/architecture/multi-repository-integration.md) (specifically §4.3, updated alongside this ADR)
- Architecture: [Git Integration Contract](/architecture/git-integration.md) (single-repo base contract)
- Governed schema: [Artifact Front Matter Schema](/governance/artifact-schema.md) (Task `repositories` field)
- Downstream epics: [EPIC-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md), [EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md)
