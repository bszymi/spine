---
id: EPIC-004
type: Epic
title: Standalone Service Hardening
status: Pending
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
owner: bszymi
created: 2026-10-06
last_updated: 2026-10-06
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/initiative.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: /architecture/actor-model.md
  - type: related_to
    target: /architecture/divergence-and-convergence.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: related_to
    target: smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md
---

# EPIC-004 — Standalone Service Hardening

---

## 1. Purpose

Close the findings that reach only standalone Spine-as-a-service:

- **Git state.** The convergence target and shared-working-tree safety, with bounded merge
  retries (F8, F9, P3-27).
- **Authority.** Skill and actor authority, with bootstrap-token hygiene (F6, P3-31, P3-33,
  P3-35).
- **Bypasses.** The reviewer workflow-update bypass and the foreign `write_context` bypass (F7,
  P3-6).
- **Remaining surfaces.** The remaining gateway and credential surfaces, including the latent
  gitpool token-host scoping (P3-28, P3-29, P3-30).

Changes live in `service/` or behind options wherever possible. The shared packages SMP embeds
stay compile- and behaviour-compatible.

None of these findings is reachable in embedded SMP:

- SMP does not import `service/` or `adapters/gitpool`.
- It maps skill and workflow operations to admin.
- It opens a fresh worktree per merge and serialises merge and push per workspace.
- Its divergence client resolves an empty target to the run branch.

They are still real defects for anyone who runs `spine serve`.

---

## 2. Scope

### In scope

- **`core/divergence`, `adapters/git` and `core/engine`:** an explicit convergence target, an
  empty `Target` refused, a per-repository working-tree guard with a home-branch restore, and
  bounded merge retries. `adapters/scheduler`: a bounded multi-workspace scan (TASK-012).
- **`core/auth`, `service/internal/gateway` and `service/cmd/spine`:**
  - the permission map;
  - actor-skill grants, with no self-grant below admin;
  - an `actor.create` role ceiling;
  - bootstrap re-activation, revocation on rotation and random token ids;
  - an `SMP_ADMIN_TOKEN` strength gate in production (TASK-013).
- **`core/workflow`, `core/engine` and the workflow and artifact handlers** (TASK-014):
  - a branch-protection policy on `workflow.Service`;
  - update-capable lifecycle planning runs;
  - reviewer update dispatch, with the empty-`write_context` and foreign-planning-run variants;
  - `write_context` limited to the run's bound assignee, with an API review freeze;
  - path and `task_path` guards.
- **`service/internal/gateway`, `core/observe`, `adapters/git` and `adapters/gitpool`**
  (TASK-015):
  - SSE field safety and a workflow identifier rule;
  - shared-mode scoping of metrics, the rebuild registry and the SSE limiter;
  - the git endpoint's real peer address and dev-mode limits;
  - the process push token scoped to the primary's push host.

### Out of scope

- Matching `required_skills` by id (initiative §7d). TASK-013 removes the vector instead.
- Wiring a code-repository catalog or a `RepositoryManager` into `spine serve`. The gitpool
  fix is a guard on a dormant path.
- Any SMP change. SMP picks this epic up only through its closing `.spine-revision` bump (SMP
  TASK-012), which re-runs the named compatibility gates.

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | Audit PoCs → regression tests |
|---|---|---|---|---|---|
| TASK-013 | Admin-only skill management, actor role ceiling, and bootstrap-admin token hygiene | F6, P3-31, P3-33, P3-35 | 2 | — | `skeptic-skills`, `verify-r1-4`, `crypto-secrets-auth`, `gap1-crypto-secrets-reaud` |
| TASK-014 | Reviewer workflow updates go through governance; `write_context` limited to the run's assignee | F7, P3-6 | 3 | EPIC-001 TASK-004 (soft: EPIC-001 TASK-016; EPIC-002 TASK-007 for its AC 19) | `standalone-service`, `verify-r1-5`, `verify-r1-12` |
| TASK-015 | SSE field safety, shared-mode tenant scoping, git-endpoint auth and gitpool token-host scoping | P3-28, P3-29, P3-30, latent gitpool | 3 | EPIC-001 TASK-016 | `gitpool_tokenleak`, `verify-r1-3`, `standalone-service` |
| TASK-012 | Standalone git-state safety: explicit convergence target, serialized working tree, bounded merge retries | F8, F9, P3-27 | 4 | EPIC-001 TASK-002, EPIC-001 TASK-004, EPIC-002 TASK-005 | `skeptic_conv`, `verify-r1-11`, `gap2-git-state-concurrenc`, `verify-r2-2` |

---

## 4. Key Work Areas

- **TASK-012 — one sequence at a time on a shared tree.**
  - **F8.** `CommitConvergence` names the parent run branch as its `Target`, and
    `CLIClient.Merge` refuses an empty `Target`. Divergence content then reaches only the run
    branch and its review.
  - **F9.** A per-repository guard (`WithWorkingTree`, `WithHomeBranch`) serialises every
    stateful git sequence:
    - between sequences, the tree rests on a clean checkout of `main`;
    - a poisoned tree self-heals on the next governed operation: tracked changes go to an
      unreferenced commit, and untracked files are never touched;
    - no lock is held while events are emitted or steps are routed;
    - git children stop with SIGTERM within a sequence bound.
  - **P3-27.** Merge retries are bounded using the existing `repository_merge_outcomes.attempts`,
    and the multi-workspace scheduler gets a per-workspace budget.
- **TASK-013 — authority.**
  - **Skills.** Skill create, update and deprecate, and actor-skill grant and remove, become
    admin-only, and a caller below admin cannot grant itself a skill.
  - **Actors.** `actor.create` cannot create an actor above a ceiling.
  - **Bootstrap.** `BootstrapInternalAdmin` never re-activates a suspended `smp-admin`. It
    revokes superseded bootstrap bearers on rotation and uses random token ids.
  - **Token strength.** `spine serve` refuses a weak `SMP_ADMIN_TOKEN` in production.
  - **Audit.** Every change writes an audit line.
- **TASK-014 — workflow governance.**
  - A reviewer's `PUT /workflows/{id}` opens an update-capable lifecycle planning run
    (`StartWorkflowUpdatePlanningRun`), like create does. An empty `write_context` no longer
    skips the dispatch.
  - `workflow.Service` consults branch protection, so a non-bypass write to `main` is refused.
    Operator bypass writes stay possible and carry the `Workflow-Bypass` trailer.
  - `write_context` and `/artifacts/add` write only onto a run the caller is bound to. A workflow
    write lands only on the lifecycle run that governs that workflow file.
  - The API path freezes a branch under review (owner question 3). Dot-segment paths and
    workflow `task_path`s on planning runs are refused.
- **TASK-015 — remaining surfaces.**
  - **P3-28.** The SSE writer refuses unsafe field values, and workflow identifiers follow a
    strict grammar.
  - **P3-29.** In shared mode, `/system/metrics` shows only the caller's workspace (with a new
    operator-token platform route for the full scrape). The rebuild registry and the SSE
    limiter are keyed by workspace.
  - **P3-30.** The trusted-CIDR bypass never applies to pushes, and never to a proxied request.
    Dev no-auth is a separate flag allowed only in development. The compose default requires
    tokens.
  - **Latent gitpool item.** The process push token goes only to the primary's push host; code
    clones elsewhere get none.

---

## 5. Primary Outputs

- **`adapters/git`:**
  - the working-tree guard;
  - the empty-`Target` refusal;
  - `WithoutPushToken`;
  - `RemoteURL(ctx, remote, push)`.
- **Engine and scheduler:** convergence-target, retry-bound and scheduler-budget changes.
- **`core/auth`:** permission-map and grant rules, and `BootstrapAdminConfig.SupersededBefore`,
  which is additive with a valid zero value.
- **`core/engine`:** `StartWorkflowUpdatePlanningRun`. **`core/workflow`:** `WithPolicy`.
- **`core/observe`:** `ExportPrometheusWorkspace`.
- **`service/`:** gateway handler, wiring and configuration changes. The CHANGELOG lists every
  standalone behaviour change: new 403, 409 and 503 answers, the metrics scope, the dev-mode
  limits and the compose defaults.
- **Documentation:**
  - `security-model.md` §4.1 and `actor-model.md` (skills are admin-managed);
  - ADR-008 §1/§5 and `api-operations.md` (reviewer workflow updates);
  - `docs/integration-guide.md` (git endpoint auth);
  - `KNOWN-LIMITATIONS.md`.
- Committed regression tests from the PoCs in §3.

---

## 6. Acceptance Criteria

- **F8.** Completing a branch step never moves `main`. Divergence content is merged only into
  the run branch, and an empty `Target` is an error (F8 PoC inverted).
- **F9, P3-27.**
  - Concurrent approvals no longer poison the tree.
  - A tree that is poisoned on purpose heals on the next governed operation, with no data loss
    beyond the documented aborted-merge state.
  - A new run branch never carries another run's content.
  - Merge retries stop at the configured bound (F9 PoC inverted).
- **F6, P3-33.** A contributor can no longer grant itself a skill, strip another actor's skill or
  deprecate a skill. `actor.create` cannot exceed its ceiling (F6 PoCs inverted).
- **P3-31, P3-35.** Rotating `SMP_ADMIN_TOKEN` revokes the superseded bootstrap bearer. A
  suspended `smp-admin` stays suspended across workspace loads. Token ids reveal nothing about
  the bearer.
- **F7, P3-6.**
  - A reviewer's workflow update, with or without an empty `write_context`, lands in a
    lifecycle planning run, never on `main`.
  - A contributor cannot write into another actor's run branch, or onto a branch under review,
    through the API (F7 PoCs inverted).
- **P3-28, P3-29, P3-30.**
  - No SSE frame can be injected through a step or workflow id.
  - A shared-mode tenant sees only its own workspace's metrics, rebuilds and limiter.
  - A token-less clone that arrives through a proxy (a declared proxy, or one that adds a
    forwarding header) is refused. A header-less TCP proxy is the documented residual, covered
    by the compose default that requires tokens.
  - Dev no-auth never applies to pushes or outside development.
- **Latent gitpool item.** A code-repository clone or push to a host other than the primary's
  push host never receives the process token (`gitpool_tokenleak` PoC inverted).
- **SMP compatibility.** SMP compiles and behaves unchanged; SMP TASK-012 re-runs each task's
  named SMP gate at the closing pin. TASK-012 changes only two things for SMP, and neither
  changes a successful result:
  - git children stop with SIGTERM on cancel;
  - the merge blocks re-check run status.

---

## 7. Cross-Repo Coordination

- **No SMP adaptation task.** The only SMP task is the closing bump, SMP TASK-012. It confirms:
  - SMP's admin workflow writes stay bypass writes, with the trailer (TASK-014);
  - SMP's convergence path still resolves to the run branch (TASK-012);
  - the bootstrap changes are invisible to SMP (TASK-013 AC 18);
  - SMP imports neither `service/` nor `adapters/gitpool` (TASK-015).
- **Shared semantics with SMP's gateway.** TASK-014's API-path review freeze must agree with the
  push-path rule in EPIC-001 TASK-016 and SMP TASK-007. Initiative owner question 3 answers
  this once for all three.
- **Partial close.** If a task here stalls, initiative owner question 12 decides whether SMP
  EPIC-004 waits for the INIT-023 closing commit.
