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

[INIT-018](/initiatives/INIT-018-branch-protection/initiative.md) and the companion [product description](/product/features/branch-protection.md) establish the feature scope: two protection types (`no-delete`, `no-direct-write`), operator-edited configuration, operator override with audit. This ADR decides the architectural questions: storage, enforcement point, override surface, and how Spine-owned operations interact with the rules.

---

## Decision

### 1. Configuration Is a Git-Tracked Operator Config

The branch-protection ruleset lives at `/.spine/branch-protection.yaml` on the authoritative branch. It is an operator-controlled config file (edit flow in §5), stored in Git as the source of truth, with front-matter-less YAML content (workflows are YAML without front matter; the protection config follows the same convention).

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

**Self-protection is branch-level, not path-level.** The config file lives on the authoritative branch, and the authoritative branch is covered by `no-direct-write` (either via explicit config or via the bootstrap defaults below). The ruleset is explicitly flat (branch + kind); there is no separate path-scoped rule on `/.spine/branch-protection.yaml`, and adding one would contradict §6 and push v1 into path-rule territory.

The enforcement boundary that follows from branch-level protection is narrower than "no one can change the bytes of the file":

- **Direct advance of the authoritative branch (`git push main`, `artifact.create` without `write_context`).** While the authoritative branch carries `no-direct-write` (the bootstrap default), these are blocked unless the caller invokes the §4 override path. Under that configuration, the operator-only edit property in §5 holds — every edit that arrives this way is an operator with `Override: true`, producing a `branch_protection.override` event.
- **Governed merge of a run branch that happens to include the config file.** `OpGovernedMerge` is allowed by `Policy.Evaluate` unconditionally (§3), and `Orchestrator.MergeRunBranch` merges the whole branch. A contributor with write access to a run branch can include a change to `/.spine/branch-protection.yaml` there; the governing workflow's review step is the only gate. v1 does **not** add a merge-time path guard that would force an override on this path. The reason is that path-scoped enforcement is explicitly out of scope (§6), and adding it would contradict the "flat (branch + kind)" invariant. Reviewers on run branches that touch `.spine/*` are expected to catch such changes the same way they would catch a surprise edit to a workflow definition or any other governance file.
- **Direct pushes while `no-direct-write` is not in effect.** If an operator later removes `no-direct-write` from the authoritative branch, self-protection is off — subsequent edits to `/.spine/branch-protection.yaml` are ordinary direct writes and do not emit a `branch_protection.override` event. That relaxation itself is an audited override (the act of removing the rule required one); further writes under the relaxed config are not. Deployments that want the stronger property keep `no-direct-write` on the authoritative branch.

This ADR does not force the self-protection invariant at the schema level, because (a) path-scoped self-protection is explicitly out of scope (§6), (b) imposing a hard-coded "authoritative branch cannot be relaxed" rule would foreclose legitimate configurations (e.g. workspaces that use a different ref as authoritative, or air-gapped repos that vouch for protection out-of-band), and (c) the merge-time guard for the run-branch path is a decision a future ADR can add once there is evidence it is warranted. Closing either gap would be a deliberate expansion of scope, not a bug fix.

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

### 5. Editing the Protection Config Is an Operator Direct Commit (Intended Path)

`/.spine/branch-protection.yaml` is treated as an operator-controlled governance config, in the same class as `.spine.yaml`, `.gitignore`, and the seed files produced by `spine init-repo` — **not** as a governed artifact with a lifecycle workflow.

No dedicated `branch-protection-lifecycle.yaml` exists, and the file is deliberately not bound to a `workflow-lifecycle.yaml`-style planning flow. The intended edit path is for an operator to land the change via the standard override surface defined in §4: a direct commit on the authoritative branch, pushed with the Git push option `-o spine.override=true`. `Policy.Evaluate` honors the override because the actor is `operator+`, and a `branch_protection.override` governance event is emitted in the usual way. No new signaling mechanism is introduced for config edits — this is the same surface every other honored override already uses.

The enforcement boundary around that path is exactly what §1 describes. Summarized:

- Direct advance of the authoritative branch that touches this file under `no-direct-write`: blocked unless an operator uses the §4 override. This is the case the "operator-only" framing applies to.
- Governed merge of a run branch that happens to modify the file: allowed unconditionally (§3); the governing workflow's review step is the control. v1 does not add a merge-time path guard for this path — see §1.

This ADR does not close the run-branch-slippage gap for the protection config specifically, because the same gap exists today for every other governance file (workflows, ADRs, `.spine.yaml`) that a contributor could slip onto a run branch. Addressing it uniformly would be a separate decision about path-scoped merge-time guards, not a decision about branch-protection specifically.

Why not a lifecycle workflow:

- **Who edits the rules is already constrained.** Branch-protection policy is an operator/admin concern by design — the override-to-edit path is gated on `operator+` anyway. Adding a reviewer-authored flow on top produces a review step that only operators are qualified to land.
- **Lifecycle workflows pay off when an artifact has production semantics.** Workflows, ADRs, Tasks are drafted, reviewed, iterated. Branch-protection rules are a short list of (branch, kinds) entries; there is no drafting effort that benefits from a separate review step beyond the commit review the operator's change receives at the Git/API boundary.
- **The self-protection recursion is avoided.** Under the previously considered design, a rule change would have to merge through a branch covered by the rule it is trying to change — every edit would require an override anyway for the bootstrap or self-disable case. Making the operator-commit path the only path removes the distinction between the "normal" and "recovery" edit paths: there is just one path.

Edit flow:

1. An operator edits `/.spine/branch-protection.yaml` on a local clone of the repo.
2. The commit is pushed to the authoritative branch with `git push -o spine.override=true` (the Git-path override surface from §4). Per §4 the Git push path does not rewrite commits, so no `Branch-Protection-Override` trailer is added — the governance event is the sole record.
3. `Policy.Evaluate` sees `Override: true` on the push and the caller's `operator+` role, and allows the advance.
4. A `branch_protection.override` governance event is emitted with the usual metadata (§4).
5. The `Projection Service` detects the advance via the usual sync path and refreshes the in-memory ruleset.

The "operator-only edit" property is a consequence of the authoritative branch carrying `no-direct-write`, not a separate invariant enforced by the branch-protection module. It constrains the direct-push path; it does not close the run-branch-slippage path (§1) or survive a relaxation of `no-direct-write` on the authoritative branch.

Non-operator authors who want a rule change propose it through the normal channels (file an issue, ask an operator) — the same escalation pattern for any other operator-controlled config. If dogfooding shows that operators routinely hand-edit complex rule changes and would genuinely benefit from a draft/review flow, or that run-branch slippage becomes a concrete problem, a future ADR can introduce a lifecycle workflow or a merge-time path guard; this ADR explicitly closes v1 without either.

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
- Protection rules live in Git and carry their edit audit trail in the event stream (§4, §5), so "who changed protection on `main` and when" is answered the same way as any other governance event.
- Override is auditable, attributable, and scoped to the single operation that requested it.

### Negative

- New dependency: the enforcement path adds a latency tax on every merge and every push. For the Git path this is unavoidable; for the API path the cost is an in-memory lookup against the projected ruleset.
- Bootstrap story is slightly more complex — repos without a config file evaluate against implicit defaults, and implementers must remember to keep the defaults and the seed file in sync.
- There is no API-level path for a non-operator to propose a rule change — non-operators must escalate out-of-band. This is the intended consequence of treating protection as operator-owned configuration (§5), and accepting it is what avoids the "lifecycle workflow reviewing an operator-only change" shape. Note: this bullet is about the intended edit path only. The branch-level enforcement boundary (§1) does not make "every edit requires an operator" a hard invariant — a run-branch governed merge can still carry a config change, and the governing workflow's review step is the control for that path.

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
- [Branch Protection Config Format](/architecture/branch-protection-config-format.md)
- [ADR-001 — Workflow Definition Storage and Execution Recording](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md)
- [ADR-006 — Planning Runs](/architecture/adr/ADR-006-planning-runs.md)
- [ADR-008 — Workflow Lifecycle Governance](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)
- [Git Integration Contract](/architecture/git-integration.md)
- [Security Model](/architecture/security-model.md)
- [Governance Charter](/governance/charter.md)
