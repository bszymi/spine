---
id: ADR-009
type: ADR
title: Branch Protection
status: Accepted
date: 2026-04-18
decision_makers: Spine Architecture
---

# ADR-009: Branch Protection

---

## Context

Spine is its own Git server. Workspaces are served via the `internal/githttp` endpoint (today read-only; enabling push is on the roadmap), and the `Artifact Service` plus `engine.Orchestrator` are the only internal components that advance refs on the authoritative branch. Forge-level protection rules (GitHub, GitLab) do not apply — there is no forge, there is Spine.

Two governance hazards result, each of which already exists today and becomes acute once the Git endpoint accepts writes:

1. **Deletion of load-bearing branches.** `git push --delete` on any branch, including long-lived refs like `staging` or `release/*`, removes the ref without any review.
2. **Direct writes to the authoritative branch.** A push to `main`, or a `workflow.create` / `artifact.create` call that commits without a `write_context`, advances `main` with no Run, no approval, and no workflow-level audit.

The rest of Spine goes to some length to prevent exactly this: [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) establishes Git as the source of truth, [ADR-006](/architecture/adr/ADR-006-planning-runs.md) introduces planning-run branches so that new artifacts land only via governed merge, and [ADR-008](/architecture/adr/ADR-008-workflow-lifecycle-governance.md) requires workflow edits to flow through their own lifecycle workflow with an operator bypass. Yet nothing enforces the invariants at the Git layer. The `Git Integration Contract` [§6.3](/architecture/git-integration.md) states that direct manual merges of `spine/*` branches are "not allowed" — but that is documentation, not enforcement.

The product-level prerequisite for this ADR is that Spine owns the Git host for governed repositories and the governance state machine around change proposals (PRs, reviews, approvals, merge authority). Both are now reflected in [Boundaries §2.1-§2.3](/product/boundaries-and-constraints.md): Spine hosts the governed repository via the `githttp` endpoint and owns the PR-shaped governance layer, delegating only the presentation surfaces that render it. External forges, when present, integrate as *clients* of Spine's governance engine, not as authorities over it. Without that boundary, branch protection would have to push rules to an external forge's API — which cannot see Spine's Run state and therefore cannot distinguish a governed merge from a direct write.

[INIT-018](/initiatives/INIT-018-branch-protection/initiative.md) and the companion [product description](/product/features/branch-protection.md) establish the feature scope: two protection types (`no-delete`, `no-direct-write`), reviewer-authored configuration, operator override with audit. This ADR decides the architectural questions: storage, enforcement point, override surface, and how Spine-owned operations interact with the rules.

---

## Decision

### 1. Configuration Is a Governed Artifact in Git

The branch-protection ruleset lives at `/.spine/branch-protection.yaml` on the authoritative branch. It is a governed artifact — the same class as a workflow definition — with front-matter-less YAML content (workflows are YAML without front matter; the protection config follows the same convention).

Shape:

```yaml
# .spine/branch-protection.yaml
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: staging
    protections: [no-delete]
  - branch: "release/*"
    protections: [no-delete, no-direct-write]
```

Git is the source of truth; the Projection Service mirrors the parsed ruleset into a runtime table (`branch_protection_rules`) the same way it does for workflow projections, and the enforcement path reads the projection (not the file) so evaluation is an in-memory lookup.

**Self-protection is branch-level, not path-level.** The config file lives on the authoritative branch, and the authoritative branch is covered by `no-direct-write` (either via explicit config or via the bootstrap defaults below). That alone ensures config edits can only land through a governed merge — there is no separate path-scoped rule on `/.spine/branch-protection.yaml`. The ruleset is explicitly flat (branch + kind); adding a path-scoped check would contradict §6 and push v1 into path-rule territory.

The only way to relax protection on the authoritative branch is either (a) a governed merge approved through the normal flow, or (b) an operator override on a direct commit. Both produce audit records (§4). An attacker with commit access but no operator role cannot disable self-protection, because every path that lands the edit goes through `Policy.Evaluate` for the authoritative branch.

**Bootstrap.** A fresh repository without `branch-protection.yaml` evaluates as if the following defaults were present:

```yaml
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
```

`spine init-repo` seeds the file so the defaults become explicit and editable, but the enforcement layer is correct even when the file is missing (newly-imported repositories, partial rollouts).

### 2. Protection Types — `no-delete` and `no-direct-write`

- **`no-delete`** blocks any operation that removes a ref. Evaluated on deletion attempts at both enforcement points (§3).
- **`no-direct-write`** blocks any advance of a ref that is not a Spine-governed merge. "Governed merge" means a commit produced by the Artifact Service's merge path in the context of a Run whose workflow reached an outcome authorizing the merge. Everything else — `git push` from an external client, direct `artifact.create` / `workflow.create` without `write_context`, internal helpers that advance refs outside the Run-scoped merge path — is a direct write.

These are the only two types in v1. Additional rules (signed commits, required-reviewer counts, status checks, path-scoped rules) require a new ADR — the ruleset is explicitly closed for extension here.

### 3. Enforcement Is Centralized in `internal/branchprotect`

A new package `internal/branchprotect` owns the policy evaluation. Its surface:

```go
type Decision int
const (
    Allow Decision = iota
    Deny
)

type Request struct {
    Branch      string          // ref being mutated
    Kind        OperationKind   // Delete | DirectWrite | GovernedMerge
    Actor       ActorIdentity
    Override    bool            // true when the caller opts into override
    RunID       string          // present for GovernedMerge and write_context
    TraceID     string
}

type Policy interface {
    Evaluate(ctx context.Context, req Request) (Decision, []Reason, error)
}
```

Both enforcement points call the same `Policy.Evaluate`:

- **Git push path (`internal/githttp`).** A pre-receive check wraps `git-receive-pack` (once push is enabled). For each ref update in the push, the handler constructs a `Request` (delete vs advance inferred from the old/new SHA) and consults the policy. A `Deny` translates to a `pre-receive hook declined` error over the Git wire protocol.
- **Spine write path (`internal/artifact`, `internal/engine`).** Every code path that advances a ref goes through a small number of well-defined functions (the Artifact Service's commit helpers, `Orchestrator.MergeRunBranch`, divergence branch management). Those functions call `Policy.Evaluate` with `Kind: GovernedMerge` (for merge paths carrying a `RunID`) or `Kind: DirectWrite` (for direct commit paths). Governed merges are allowed by the `no-direct-write` rule; direct writes are not.

Spine-internal operations (planning-run branch creation on `spine/run/*`, divergence branches, scheduler recovery writes) are **out of scope of the rules by construction**: protection rules name branches users care about (`main`, `staging`, `release/*`), and `spine/*` branches are never listed. There is no implicit "system bypass" — the policy module does not know the actor is "internal" vs "external", it only knows the branch and the operation kind. That keeps the audit semantics clean: any mention of `Override: true` is an actual override, not a system exemption.

### 4. Override Is Per-Operation, Operator Role Only

- Override is opt-in on each operation. There is no repo-wide or time-bounded "protection off" mode.
- Git push: an operator signals override via a push option (`git push -o spine.override=true`). Spine's pre-receive handler reads push options via the Git wire protocol and surfaces them on the `Request`.
- Spine API: the existing `write_context` extension on artifact / workflow operations gains an `override: true` field, mirroring the `run_id` extension from ADR-008.
- The `branchprotect` module checks the actor role (`operator`+) before honoring `Override: true`. A contributor sending the override flag gets the same rejection as one who did not — the flag is never silently accepted or silently ignored.

**Audit.** The authoritative audit record for every honored override is a governance event, not a commit trailer. The Git path cannot retrofit trailers onto client-produced commits, and override-deletes have no resulting commit at all.

- **Primary (both paths): governance event.** Every honored override emits `branch_protection.override` with `{ actor_id, branch, rule_kinds, operation, trace_id, run_id, commit_sha (or `null` for deletions and pre-existing-SHA pushes), pre_receive_ref (old_sha, new_sha, ref) for Git-path events }`, persisted via the existing event bus.
- **Secondary (Spine write path only): commit trailer.** On the Spine API path, the caller's commit is constructed in-process by the Artifact Service, so the enforcement module adds a `Branch-Protection-Override: true` trailer on that commit. This is a convenience for `git log` inspection; it is not a replacement for the event.
- **Git push path: no trailer rewriting.** The pre-receive handler does not rewrite pushed commits to add a trailer — that would mutate client-produced objects and break expected push semantics. The governance event is the sole record on this path.

An auditor answering "who overrode protection on this branch" consults the event stream. An auditor answering "was this specific commit the product of an override" consults the event stream joined against `commit_sha`; the commit trailer is only present on API-path overrides and exists as a hint, not a source of truth.

Per-actor override allow-lists (a deployment bot that must force-push to `staging`) are **not** included in v1 — operator override is the only escape hatch. If dogfooding surfaces a real need, a new ADR adds them.

### 5. Editing the Protection Config Goes Through the Governance Flow

`/.spine/branch-protection.yaml` is a governed artifact. Editing it means:

1. The reviewer starts a Run whose workflow writes to the config path. (The specific governing workflow — a dedicated `branch-protection-lifecycle.yaml`, or just bracketing the file under the existing `workflow-lifecycle.yaml`-style scheme — is an implementation decision for the follow-up epic, not this ADR.)
2. The Run's branch is created, the edit is committed, a review step is required, and merging the branch updates the config.
3. The `Projection Service` detects the merge via the usual sync path and refreshes the in-memory ruleset.

Bootstrap deadlock — the config protects itself and the config is broken — is the same class of problem ADR-008 addresses for the lifecycle workflow. The resolution is the same: operator override. An operator may land a direct commit on `/.spine/branch-protection.yaml` via the override path, and that commit carries the audit trailer.

### 6. Scope of v1

In scope:

- `no-delete`, `no-direct-write` rule types.
- Exact branch names and glob-style patterns (`release/*`). Regex and negative patterns are out.
- Push-option-based override for Git; `write_context.override` for Spine API.
- Projection of config to a runtime table; hot-path reads from the runtime table.
- Bootstrap defaults for repositories without a config file.

Out of scope (explicitly — each requires its own ADR):

- Signed-commit requirement.
- Required status checks.
- Required-reviewer counts beyond what the governing workflow enforces.
- Per-path rules within a branch.
- Per-actor override allow-lists.
- Protection rules on tags.

---

## Consequences

### Positive

- Authoritative-branch invariants become enforceable, not aspirational. The `Git Integration Contract` §6.3 claim about Spine-managed branches becomes a runtime fact.
- Both push-layer and internal-API-layer writes are checked by a single policy module, eliminating the "one path forgot to check" failure mode.
- Protection rules are governed by the same machinery as any other artifact — reviewing a rule change looks like reviewing an ADR or a workflow.
- Override is auditable, attributable, and scoped to the single operation that requested it.

### Negative

- New dependency: the enforcement path adds a latency tax on every merge and every push. For the Git path this is unavoidable; for the API path the cost is an in-memory lookup against the projected ruleset.
- Bootstrap story is slightly more complex — repos without a config file evaluate against implicit defaults, and implementers must remember to keep the defaults and the seed file in sync.
- Self-protection creates a recursion: relaxing protection requires an operator override. This is the intended behavior but adds a support-surface edge case (an operator is always required to ship the first relaxation).

### Neutral

- No new actor roles are introduced. The existing hierarchy (`reader < contributor < reviewer < operator < admin`) carries the override authority.
- The decision deliberately defers every feature that would put Spine in parity with forge-level branch protection. Expanding the ruleset is a future ADR.

---

## Alternatives Considered

### A. Configuration in the Runtime Database

Branch-protection rules live only in the `branch_protection_rules` table and are mutated via a `/admin/branch-protection` API.

**Why not.** Loses the "Git is the source of truth" invariant for a piece of governance state that is explicitly about what lands on Git. Rule history lives in the database, not in `git log`, so a reviewer auditing "who relaxed protection on `main`?" has to query a separate store. And the same mechanism that governs every other artifact change (branch + review + merge) is inapplicable to the rules that govern merges. The projection-backed design keeps Git authoritative and still makes evaluation cheap.

### B. Enforcement Only in the Git Push Path

A pre-receive hook is the single enforcement point; the Spine API layer is not changed.

**Why not.** Direct commits produced by `workflow.create` / `artifact.create` without `write_context` never go through the Git wire protocol — the Artifact Service `git commit`s locally in the server process and only then pushes the ref forward. A push-only enforcement point would let these bypass protection entirely. Centralizing the policy module so both paths consult it is the only design that enforces a single invariant.

### C. Enforcement Only in the Spine API Layer

The Git push path stays read-only forever; all writes go through Spine API operations.

**Why not.** This is the status quo and it is on its way out — enabling push to the `githttp` endpoint is a committed roadmap item. Deferring enforcement until after push is enabled repeats the `Git Integration Contract` §6.3 mistake (documentation instead of enforcement). Better to design enforcement for both paths up front, even if the Git path only goes live later.

### D. Operator Bypass as a Session / Mode Flag

An operator enters "bypass mode" for some duration, during which all of their operations skip protection.

**Why not.** Bypass mode makes auditing much worse: a single mode-on event followed by N commits is materially less informative than N per-operation override records, because it does not identify which specific commits were intended to override protection and which just happened to land while the mode was open. Per-operation override is the form ADR-008 already uses for the workflow lifecycle, and the audit trailer convention generalizes.

---

## Links

- [INIT-018 — Branch Protection](/initiatives/INIT-018-branch-protection/initiative.md)
- [INIT-018 product description](/product/features/branch-protection.md)
- [ADR-001 — Workflow Definition Storage and Execution Recording](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md)
- [ADR-006 — Planning Runs](/architecture/adr/ADR-006-planning-runs.md)
- [ADR-008 — Workflow Lifecycle Governance](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)
- [Git Integration Contract](/architecture/git-integration.md)
- [Security Model](/architecture/security-model.md)
- [Governance Charter](/governance/charter.md)
