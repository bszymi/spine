---
type: Product
title: Branch Protection
status: Draft
version: "0.1"
---

# Branch Protection

---

## 1. Problem

Spine is its own Git server. Workspaces are served over Spine's `githttp` endpoint, and the Artifact Service mediates every write the engine performs on the authoritative branch. Push is enabled via the `SPINE_GIT_RECEIVE_PACK_ENABLED` flag (EPIC-004 TASK-001); when on, the pre-receive gate (TASK-002) evaluates every ref update against the same `branchprotect.Policy` the API-path uses. Without those enforcement points, a contributor would be able to:

1. **Delete** any branch — including long-lived ones like `staging` or release branches — with a single `git push --delete`.
2. **Advance** the authoritative branch (`main`) with a direct push, bypassing the planning-run / approval / merge machinery that every other write is required to go through.

The same two problems exist today on the server side, via the operator-bypass paths some `workflow.*` and `artifact.*` operations expose — a direct commit lands on `main` with no Run, no approval, and no workflow-level audit.

Forge-level protection (GitHub / GitLab branch rules) does not help: Spine is the Git server. Protection has to be a first-class Spine concept.

---

## 2. Users and Personas

### 2.1 Who Configures Protection

**Operators.** Protection rules declare which branches are load-bearing and how they are to be treated. They are an operator/admin concern — the same role that can already override protection on any given operation (§7) is the one who sets the rules. The intended path is for an operator to edit `/.spine/branch-protection.yaml` directly on the authoritative branch and push with the override surface (`git push -o spine.override=true`); an edit that arrives that way produces a `branch_protection.override` governance event. No lifecycle workflow governs the file; the enforcement boundary is branch-level rather than path-level (a run-branch governed merge that includes the file is allowed unconditionally — see [ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md)), so review of run branches touching `.spine/*` is the control for that path. Rationale in [ADR-009 §5](/architecture/adr/ADR-009-branch-protection.md).

### 2.2 Who Is Protected

**Contributors and automated actors.** The default rule set blocks the two operations above (`no-delete`, `no-direct-write`) for every actor below `operator`, regardless of authentication path — Git push, Spine API merge, or internal engine write.

### 2.3 Who Can Override

**Operators.** The operator role may override a protection rule on a single operation, not as a mode flag. Every override produces a distinct audit record (commit trailer + governance event) so the override is separable from routine commits. This mirrors ADR-008's operator bypass for the workflow lifecycle.

**Admins** inherit operator permissions (per the role hierarchy in the [Security Model](/architecture/security-model.md) §4).

---

## 3. What Is Protected

### 3.1 `no-delete`

A `no-delete` rule on a branch blocks:

- `git push --delete` on the Git endpoint.
- Any internal deletion path the Artifact Service exposes on that ref.

The rule does **not** block creation of the same branch name after a later override-delete; `no-delete` is a property of the live ref, not the name history.

### 3.2 `no-direct-write`

A `no-direct-write` rule on a branch blocks advances to that branch unless the advance is a Spine-governed merge. Concretely:

- `git push <branch>` from an external client: **rejected**.
- `artifact.create` / `workflow.create` without a `write_context` that would commit to the protected branch directly: **rejected**.
- `Orchestrator.MergeRunBranch` completing a planning-run on the protected branch: **allowed** (Spine-owned merge, marked as such by the caller).
- Operator override (`Branch-Protection-Override: true` on the operation): **allowed + audited**.

"Direct write" means *any* advance of the ref that is not mediated by a Spine governance flow with an approved outcome.

---

## 4. Non-Goals

The initial feature is deliberately narrow:

- **No status-check enforcement.** GitHub's "required checks before merge" are out of scope.
- **No required-reviewer counts.** Approval is owned by the workflow that produces the merge — not by the protection rule.
- **No path-scoped rules.** Protection applies to the whole ref; we do not gate which files a commit may touch.
- **No push-size or force-push limits** beyond the two blanket rules above. Force-push is covered by `no-direct-write` (it is an advance that is not a governed merge), and nothing else.
- **No signed-commit requirement.** Separable concern; if needed it becomes its own feature.
- **No parity with GitHub's full ruleset.** Expanding the ruleset requires a new decision — do not silently accrete rules.

---

## 5. User-Visible Behavior

### 5.1 Protected Authoritative Branch

A fresh repository, default config seeded by `spine init-repo`: `main` is protected with `no-direct-write` and `no-delete`.

| Action | Actor | Outcome |
|---|---|---|
| `git push origin main` (fast-forward with a governed commit) | Contributor | **Rejected**: "main is protected; push through a workflow or use operator override." |
| `git push --delete origin main` | Any | **Rejected**: "main is protected from deletion." |
| `artifact.create` on a task branch → approved outcome → merge into `main` | Contributor + Reviewer | **Allowed**: Spine-owned merge. |
| `artifact.create` with no `write_context`, operator role | Operator | **Allowed**: direct commit with `Branch-Protection-Override: true` trailer. |

### 5.2 Protected Long-Lived Branch (e.g. `staging`)

A team adds `staging` to the config with `no-delete` only — direct writes are allowed so they can script deployments.

| Action | Actor | Outcome |
|---|---|---|
| `git push origin staging` (fast-forward) | Contributor | **Allowed**: no `no-direct-write` rule. |
| `git push --delete origin staging` | Contributor | **Rejected**: "staging is protected from deletion." |
| `git push --delete origin staging` with operator override | Operator | **Allowed + audited**. |

### 5.3 Spine-Internal Operations

Planning-run branch creation, divergence branch creation, task-branch merges into the authoritative branch, scheduler recovery writes — all continue to work. Protection does not ask "who is the actor"; it asks "is this a Spine-governed merge, a direct write, or a deletion?" Governed merges are unaffected.

---

## 6. Configuration UX

The intended author experience — the technical form of the config is resolved in ADR-009:

1. The config is a **versioned config file** in Git, at a well-known path. Editing it is a governance change, not a runtime toggle.
2. Each rule names a branch (or a branch pattern) and the rule types applied (`no-delete`, `no-direct-write`). There are no per-actor or per-role override allow-lists in v1 — operator override is the only escape hatch.
3. The intended path for changes is for an **operator** to commit directly on the authoritative branch and push with the override surface (`git push -o spine.override=true`). While the authoritative branch carries `no-direct-write` (the seeded default), that is the path a direct push to `main` has to take, and it produces a `branch_protection.override` governance event. There is no lifecycle workflow for this file; non-operators escalate out-of-band if they want a rule change. See [ADR-009 §5](/architecture/adr/ADR-009-branch-protection.md).
4. The enforcement boundary around that path is branch-level, not path-level. In particular: a governed merge of a run branch that happens to include a change to `.spine/branch-protection.yaml` is allowed by the policy module unconditionally (`OpGovernedMerge` is always permitted, see ADR-009 §3). The governing workflow's review step is the control on that path — the same as for any other governance file (workflows, ADRs, `.spine.yaml`). If a deployment relaxes `no-direct-write` on the authoritative branch, self-protection is off for the direct-push path as well. v1 accepts both limitations as consequences of the flat (branch + kind) ruleset; a future ADR can tighten them if dogfooding warrants. See [ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md) for the full reasoning.

Example (illustrative only, the exact shape is decided in the ADR):

```yaml
# spine/branch-protection.yaml
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: staging
    protections: [no-delete]
  - branch: "release/*"
    protections: [no-delete, no-direct-write]
```

---

## 7. Override Model

- Override is always **per-operation**, never a mode flag.
- The override surface is the same whether the operation arrives via Git push or Spine API — the caller signals "I know this is protected; override" on that one call.
- The authoritative audit record is a governance event, `branch_protection.override`, produced on every honored override. On the Spine API path, where Spine constructs the commit in-process, a `Branch-Protection-Override: true` commit trailer is also added as a convenience for `git log` inspection. On the Git push path there is no trailer — pushed commits are not rewritten.
- Only operator-role (and above) actors may invoke override. Lower roles see the request rejected with the same error whether or not they attempted the override flag — the flag is never silently ignored.

---

## 8. Resolutions (see ADR-009)

- **Branch patterns.** `release/*`-style globs are in scope for v1; regex and negative patterns are out.
- **Per-actor override allow-lists.** Not in v1. Operator override is the only escape hatch. Revisit via a new ADR if dogfooding surfaces a real need (e.g. a deployment bot that must force-push).
- **Config self-protection.** Branch-level, not path-level — the config lives on the authoritative branch, which is protected by `no-direct-write`. That invariant alone prevents silent relaxation.
- **Protected by default.** A fresh repository with no `branch-protection.yaml` evaluates against bootstrap defaults that protect `main` with `no-delete` + `no-direct-write`. `spine init-repo` seeds the file so the defaults become explicit and editable.
