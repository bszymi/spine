---
id: TASK-014
type: Task
title: "Reviewer workflow updates go through governance; write_context limited to the run's assignee"
status: Completed
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
  - type: blocked_by
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-004-worktree-write-containment.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-016-run-branch-push-admission.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-006-governance-path-guard.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-005-merge-bound-reviewed-sha.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
  - type: related_to
    target: /architecture/api-operations.md
---

# TASK-014 — Reviewer workflow updates go through governance; write_context limited to the run's assignee

---

## Purpose

This task closes two findings from the 2026-10-06 Spine security audit (Spine `61994fe`,
SMP `80995559`). Both are **standalone-service only**: SMP does not import `service/`.

- **F7 (P2).** A reviewer's `workflow.update` without `write_context` commits straight to `main`.
  It skips both the ADR-008 lifecycle and branch protection.
- **P3-6 (P3).** `write_context` and `POST /artifacts/add` let any contributor commit into another
  actor's run branch, even while the run is in review.

The design also closes two variants of F7 that turned up while it was being checked. Both were
reproduced against `61994fe` on 2026-10-06 (`_work/task014/probe`):

- **V1. An empty `write_context` (`{}`) skips the reviewer dispatch on both create *and* update.**
  A reviewer `POST /workflows` or `PUT /workflows/{id}` with `"write_context": {}` returned
  201/200 `write_mode: authoritative`. `main` moved, with no planning run and no
  `Workflow-Bypass` trailer. The audit had recorded create as correctly routed.
- **V2. A workflow write may name any planning run.** A reviewer wrote `workflows/other-flow.yaml`
  onto the branch of a run that governs `workflows/a.yaml` (201 `write_mode: proposed`).
  - Code-traced, not run: the same route accepts an artifact-creation planning run.
    `resolveWriteContext` skips every `task_path` check for planning runs
    (`handlers_artifacts.go:440-444`).
  - It also accepts a standard task run when the caller echoes its `task_path`.
  - The workflow then reaches `main` under that run's review step, not the `workflow_review` gate
    of ADR-008.

> **Design-check note (2026-10-06): two more ways around I7, and one way the fix locks a
> workflow.** The probes are in `_work/task014-check/`. Each was run against `61994fe` and against
> the `_work/task014/proto` overlay.
>
> - **V3. Dot-segment paths get past the artifact-endpoint workflow guard.**
>   - `isWorkflowTarget` (`handlers_artifacts.go:18-21`) tests the raw request path.
>   - `artifact.Service` joins and cleans that path, and has no workflow guard of its own.
>   - The run's assignee sent `POST /artifacts` with `path: "./workflows/evil1.yaml"` or
>     `"governance/../workflows/evil2.yaml"` and a `write_context` naming their own run. Both
>     returned 201 `proposed`, and the files landed under `workflows/` on the run branch.
>   - `PUT /artifacts/./workflows/task-default.yaml` returned 200 and replaced the workflow file
>     on the branch with artifact Markdown.
>   - Results: at `61994fe` and on the prototype alike (`TestT014Check_ArtifactPathDotSegments_ReachWorkflowsDir`).
>   - Impact once merged under the *task's* review:
>     - The projection decodes the front matter as the workflow row (`adapters/projection/service.go:500-553`).
>     - No artifact status is `Active` (`core/domain/artifact.go:82-99`), so a replaced
>       `task-default` stops resolving, and every new Task run fails "no active workflow".
>     - An *executable* workflow cannot be planted this way: strict decoding rejects the artifact
>       keys `type` and `title` (`core/yamlsafe/decoder.go:63-73`).
>   - TASK-004's lexical `Clean` (TASK-004 §1a; already `blocked_by`) refuses these spellings at
>     the service. TASK-014 still canonicalises its own guard, and pins the spellings (§3.5,
>     AC 17).
> - **V4. `POST /runs` with `mode: planning` puts any `task_path` on a planning run, including
>   `workflows/<id>.yaml`.**
>   - `handleRunStart` (`handlers_workflow.go:43-101`) passes `req.TaskPath` straight to
>     `StartPlanningRun` (`:75`). `run.start` is `RoleContributor`.
>   - `StartPlanningRun` (`core/engine/run.go:203-285`) checks only the artifact content, and
>     resolves the governing workflow from the content's `type`.
>   - A contributor's `task_path` of `workflows/task-default.yaml`, `workflows/new-flow.yaml` or
>     `./workflows/new-flow.yaml` was forwarded verbatim, with 201
>     (`TestT014Check_RunStartPlanning_AcceptsWorkflowTaskPath`). The engine side is code-traced.
>   - The result is a non-lifecycle planning run (for example under `document-creation`) carrying
>     the exact key that §3.2 step 3 and §3.3 match on. Consequences:
>     1. Its own Markdown merges into `workflows/` under that workflow's review, not
>        `workflow_review`.
>     2. §3.3 answers 409 to every reviewer create or update of that id, so a contributor can
>        lock any workflow against governed edits.
>     3. A reviewer cannot write a real workflow onto such a run. `Create` returns
>        `already_exists`, and `Update` fails to parse the Markdown prior file.
>   - Fixed in §3.5.
> - **L1. A failed workflow-run start leaves a non-terminal run, which §3.3 then treats as open
>   forever.**
>   - `startRunCommon` persists the run (`run.go:61`) before `onBranch` (`:67-71`).
>   - A reviewer `POST /workflows` for an id that already exists fails inside `onBranch` with
>     `already_exists` and leaves a `pending` planning run with `TaskPath: workflows/<id>.yaml`.
>     Seen at `61994fe` and on the prototype (`TestT014Check_CreateExistingWorkflow_LeavesPendingRun`).
>   - `pending` is not terminal (`core/domain/run.go:37-39`).
>   - Scheduler recovery even promotes such runs to `active` with a waiting entry step
>     (`adapters/scheduler/recovery.go:71-138`), although their branch was deleted.
>   - Under §3.3 as designed, every later reviewer `PUT` or `POST` for that id returns 409 naming
>     the orphan, and the starter is never called (`TestT014Check_OrphanPendingRun_LocksWorkflow`).
>   - Only an operator can clear it (`run.cancel` is `RoleOperator`).
>   - The trigger can be an honest mistake (POST instead of PUT), a deliberate lock on
>     `workflow-lifecycle`, or V4 above.
>   - Fixed in §2 (design-check note) and AC 3 and AC 6.

Line numbers refer to Spine `61994fe` unless stated otherwise. "SMP" means
`spine-management-platform` at `80995559`.

### The defects

1. **`handleWorkflowUpdate` has no lifecycle dispatch (F7).**
   - `handleWorkflowCreate` routes a caller below operator, with no `write_context`, to
     `StartWorkflowPlanningRun` and returns 503 when no starter is wired
     (`service/internal/gateway/handlers_workflows.go:55-88`).
   - `handleWorkflowUpdate` (`:142-191`) has no such branch.
     - For a reviewer without `write_context`, it sets neither a branch nor bypass (`:162-182`).
     - It then calls `svc.Update` (`:184`).
   - The response is `write_mode: authoritative` (`:327-347`).
2. **`workflow.Service` never consults branch protection.**
   - `Service` holds only `git` and `repo` (`core/workflow/service.go:44-52`).
   - `writeAndCommit` (`:119-197`) writes and commits in the main working tree with no policy
     call, unlike `artifact.Service` (`core/artifact/service.go:187-194`, `:394-457`).
   - So the bootstrap `main: [no-delete, no-direct-write]` rule
     (`core/branchprotect/source.go:58-68`) never applies to workflow files.
   - The handler comment at `handlers_workflows.go:89-100` records this as known follow-up work.
3. **No update-capable planning run exists.**
   - `StartWorkflowPlanningRun` (`core/engine/run.go:287-377`) always writes through
     `workflowWriter.Create` (`:363`).
   - `engine.WorkflowWriter` has only `Create` (`core/engine/interfaces.go:146-148`).
   - For an existing workflow, the create on the branch fails with `already_exists`. A reviewer
     therefore has **no governed way to start an edit** of an existing workflow, even though
     `workflow-lifecycle.yaml` states that it "Governs creation and modification of workflow
     definitions".
4. **`write_context` resolution checks the run, not the caller (P3-6).**
   - `resolveWriteContext` (`handlers_artifacts.go:420-455`) checks only `active`, a branch, and
     (standard runs only) that the caller echoes the run's `task_path`. That path is readable by
     anyone through `GET /runs/{id}`.
   - Planning runs skip even that (`:440-445`).
   - Nothing checks who holds the current step, or whether the run is in review.
   - `handleArtifactAdd` (`handlers_artifact_entry.go:147-264`) requires only planning mode,
     `active` and the `draft` step (`:189-206`).
   - In contrast, `step.submit` *does* enforce ownership (`handlers_workflow.go:394-436`).
5. **The workflow endpoints treat a run-less `write_context` as authoritative (V1).**
   - `resolveWriteContext` returns `("", override, nil)` for an empty `run_id` (`:421-423`).
   - The workflow handlers then attach no branch (`:111-113`, `:176-178`) and no bypass, because
     the bypass is only on the `else` of `WriteContext != nil` (`:114`, `:179`).

### Attackers and reach

- **F7, V1 — A2 with reviewer role.** The standalone default role matrix gives `workflow.update`
  and `workflow.create` to `reviewer` (`core/auth/permissions.go:22-23`). A reviewer can:
  - rewrite `task-default` to drop the review gate or widen `eligible_actor_types`;
  - neuter `workflow-lifecycle` itself, so that every later workflow edit auto-merges.

  This needs no second reviewer and no planning run.
- **V2 — A2 with reviewer role** who holds the draft step of *any* planning run. Before this
  task, holding the step was not even needed: see P3-6.
- **P3-6 — A2 with contributor role.** The attacker can:
  - commit arbitrary artifacts (including over governed files such as `governance/charter.md`)
    into a colleague's run branch while it sits in `review`;
  - leave the reviewer's view stale.

  The governed merge publishes the branch tip (F2, TASK-005), so the injection reaches `main`
  under the trusted "Merge run …" commit.
- **Not reachable in SMP.**
  - SMP maps `workflow.create`/`workflow.update` to `admin`
    (`internal/customer/workspace/permissions.go:40-41`).
  - It always writes with `workflow.WithBypass` (`internal/customer/spineembedded/embedded_workflows.go:118-120`).
  - It rejects workflow `write_context` (`:176-182`).
  - It never calls the standalone gateway.
  - SMP's own API writes onto run branches are discussed in §9.

### Verified (2026-10-06, `go test -overlay` against `61994fe`)

- `poc/verify-r1-5`: reviewer `PUT /workflows/workflow-lifecycle` returned 200 `authoritative`,
  with planning hits=0. `main` moved, `workflow_review` was gone, and there was no
  `Workflow-Bypass` trailer. Branch protection on the bootstrap rules would have answered `deny`.
- `poc/standalone-service` `TestPOC_ReviewerDirectWorkflowUpdateOnMain`: 200 `authoritative`.
  `task-default`'s review step now needs `[execution]`.
- `poc/standalone-service` `TestPOC_ContributorWritesIntoForeignRunBranch`: 201 `proposed`.
  Alice's branch head is authored by mallory.
- `poc/verify-r1-12`: while the run sat in `review`, mallory's `PUT` returned 200. The real
  `MergeRunBranch` then put `MALLORY: reviews are optional.` on `main`.
- Probe `_work/task014/probe`: V1 and V2 as described above.

## Design

### 0. Invariants

- **I1 — One lifecycle for reviewers.** A caller below operator never advances the
  authoritative branch through a workflow endpoint. Create and update both go through a
  `workflow-lifecycle` planning run (ADR-008 §1), or the request is refused.
- **I2 — Bypass is operator-only and audited.**
  - `workflow.WithBypass` writes keep the `Workflow-Bypass: true` trailer
    (`core/workflow/service.go:328-334`).
  - When a policy is installed they require an operator+ actor in the context.
- **I3 — Policy on the write path.** A `workflow.Service` with a policy evaluates every
  non-bypass write as `OpDirectWrite` against its real target branch, before any filesystem or
  git change.
- **I4 — nil policy is today.** A `workflow.Service` with no policy behaves exactly as at
  `61994fe`: no rule evaluation and no role check on bypass. This is the SMP compatibility gate
  (§9).
- **I5 — Run branches have one writer.** An API write that lands on a run branch is accepted only
  in two cases:
  - the caller is the actor bound to the run-level execution of the run's current step, in state
    `assigned` or `in_progress`;
  - the caller is operator+.

  This is the same predicate TASK-016 applies to pushes (I5 there).
- **I6 — Review freeze.** While the run's current step is a `type: review` step, the API refuses
  every write to that run's branch, for every role.
- **I7 — Workflow edits ride only their own run.**
  - A workflow write with `write_context` must name the planning run whose `task_path` is
    `workflows/<id>.yaml`.
  - A run that governs a workflow accepts no artifact writes.
- **I8 — No schema change.** There is no migration, no new store method, no new environment
  variable and no new event type. The only reads used are existing store calls: `GetRun`,
  `ListStepExecutionsByRun` and `ListRunsByTask` (`adapters/store/interfaces.go:56`).

> **Design-check note (2026-10-06): invariant amendments.**
>
> - **I6 (amended).** The freeze applies while the current step's declared type is `review` *or
>   `internal`*. `internal` steps are engine-owned: for example task-default's `publish`
>   (`workflows/task-default.yaml:93-110`) sits between approval and the governed merge.
>   Rationale: §4 design-check note.
> - **I9 (new) — one producer of workflow task paths.** Only `StartWorkflowPlanningRun` /
>   `StartWorkflowUpdatePlanningRun` create a run whose `TaskPath` is under `workflows/`
>   (canonical form). Every artifact-surface producer refuses such a path (§3.5). This is what
>   makes the `run.TaskPath` key of §3.2, §3.3 and TASK-006 §3.3 sound.
> - **I8 (clarified).** The L1 fix uses the existing `RunStore.TransitionRunStatus`
>   (`core/engine/interfaces.go:26-29`). Still no new store method.

### 1. `core/workflow`: branch protection on the write path (F7)

**`Service`** (`service.go:44-52`) gains two fields:

- `policy branchprotect.Policy`
- `defaultRef string`

`NewService` sets `defaultRef: "main"`, matching `artifact.Service` (`core/artifact/service.go:45-53`).
`core/workflow` → `core/branchprotect` is a core→core import with no cycle
(`core/branchprotect` imports only `config`, `domain` and `yamlsafe`).

**New exported API** (all additive):

```go
// WithPolicy installs the ADR-009 guard. nil restores the no-policy behaviour (I4).
func (s *Service) WithPolicy(p branchprotect.Policy)
// WithDefaultRef sets the authoritative branch reported to the policy (default "main").
func (s *Service) WithDefaultRef(ref string)
// ValidateID / FilePath expose the existing idPattern check (:304-310) and path rule (:312-314)
// so planning-run starters refuse a bad id before any branch or run exists.
func ValidateID(id string) error
func FilePath(id string) string
```

**`writeAndCommit`.**

- `s.checkBranchProtection(ctx, op.operation)` runs after `Validate` (`:133-136`) and before
  `enterBranch` (`:138`), the same placement as `artifact.Service` (`:187-194`). A denied write
  therefore never creates a worktree or touches a file.
- After TASK-004 lands, the call stays immediately before `enterBranch`. TASK-004 rewrites only
  the file I/O below it (TASK-004 §3 "does not add the branch-protection check … that is
  TASK-014").

**`checkBranchProtection` rules, in order:**

1. `s.policy == nil` → return nil (I4).
2. The target is `GetWriteContext(ctx).Branch` when non-empty, otherwise `s.defaultRef`.
3. The actor is `domain.ActorFromContext(ctx)`. If that is absent, it falls back to
   `domain.Actor{ActorID: observe.ActorID(ctx)}`, which has a zero role. This is the same helper
   shape as `artifact.actorForRequest` (`core/artifact/service.go:476-481`).
4. **Bypass** (`IsBypass(ctx)`):
   - If the actor's role is below operator → `ErrForbidden`, "workflow bypass requires operator
     role (ADR-008 §5)". An actor-less bypass is refused too.
   - If the target is the authoritative branch → return nil. Rule evaluation is skipped, because
     the ADR-008 bypass *is* the sanctioned operator direct commit, audited by its trailer.
   - A bypass that carries a `WriteContext` branch falls through to rule 5. The gateway never
     builds one.
5. `policy.Evaluate(ctx, Request{Branch: target, Kind: OpDirectWrite, Actor, RunID, TraceID})`:
   - An evaluation error → `ErrInternal` (500), logged; fail closed. This matches
     `artifact.Service` (`:419-431`).
   - `DecisionDeny` → `ErrForbidden` (403) with detail `{branch, reasons[]}`. The shape is the
     artifact one (`:433-445`), so clients parse one deny format.

`Override` is never set, because workflow endpoints keep rejecting `write_context.override`
(§3.2).

### 2. `core/engine`: update-capable workflow planning runs (F7)

- **`engine.WorkflowWriter`** (`interfaces.go:146-148`) gains
  `Update(ctx, id, body string) (*workflow.WriteResult, error)`. `*workflow.Service` already
  implements it. Neither Spine nor SMP has another implementer. `WithWorkflowWriter` is called
  only at `service/cmd/spine/serve_resolver.go:126` and
  `service/scenariotest/harness/runtime.go:162`, and SMP has no caller (grep-verified).
- **New** `func (o *Orchestrator) StartWorkflowUpdatePlanningRun(ctx, workflowID, body string) (*StartRunResult, error)`.
  - `StartWorkflowPlanningRun` keeps its signature and behaviour.
  - Both delegate to a private `startWorkflowPlanningRun(ctx, id, body, update bool)`, which is
    the current body of `:291-377` with these changes:
    1. **Early id check.** `workflow.ValidateID(id)` runs right after the empty-argument checks.
       It runs for create too: today a bad id fails only inside `onBranch`, after `CreateRun`
       (`run.go:61-71`).
    2. **Path.** `path := workflow.FilePath(id)` replaces the inline join at `:302`.
    3. **Update pre-checks**, run before `ResolveWorkflowForMode` and therefore before any branch
       or run record exists:
       - `o.git.ReadFile(ctx, "HEAD", path)`. `HEAD` is the ref `createRunBranches` cuts the run
         branch from (`core/engine/multi_repo_branch.go:90`). If TASK-002 or TASK-012 qualify
         that base ref, this read follows it.
       - A `*git.GitError` of kind `ErrKindNotFound` → `ErrNotFound` "workflow not found:
         `<path>`". Any other error is wrapped and returned.
       - `workflow.Parse` of the prior file: an error → `ErrInternal`.
       - The prior version equals the submitted version → `ErrValidationFailed`, with the exact
         message `Service.Update` uses (`service.go:96-103`).
    4. **`onBranch`** calls `o.workflowWriter.Update` instead of `Create` when `update` is set. The
       error prefix becomes `"update workflow on branch: %w"`.
    5. **Unchanged:**
       - the run shape: `Mode: planning`, `TaskPath: workflows/<id>.yaml`, governing binding
         `Workflow`/`creation` → `workflow-lifecycle`;
       - branch naming (`spine/plan/…`, `core/engine/branchname.go:18-25`);
       - branch cleanup on writer failure (`:363-373`).
  - The lifecycle run's approval merges through the existing `MergeRunBranch` path. Its
    `approved` outcome already carries `commit: {merge: "true"}` (`workflows/workflow-lifecycle.yaml`).

> **Design-check note (2026-10-06): no orphan lifecycle runs (L1).** Two changes to
> `startWorkflowPlanningRun`, both covered by AC 3 and AC 6.
>
> - **3b. Create pre-check, symmetric with update's.** When `update` is false, run
>   `o.git.ReadFile(ctx, "HEAD", path)` before `ResolveWorkflowForMode`:
>   - success → `ErrAlreadyExists` "workflow already exists: `<path>`; use PUT /workflows/{id} to
>     open an update run";
>   - `ErrKindNotFound` → proceed;
>   - any other error → wrapped and returned.
>
>   No branch is cut and no run is persisted. The prototype explicitly kept create's old
>   behaviour ("the writer's Create on the branch reports already_exists"). That behaviour is
>   what leaves the orphan, so it is reversed here.
> - **6. Fail the record on an `onBranch` error.** After the existing branch cleanup (`:363-373`),
>   call `o.store.TransitionRunStatus(ctx, runID, RunStatusPending, RunStatusFailed)`.
>   - It is best effort: log a failure to transition, and still return the writer error.
>   - The record is then terminal, so neither §3.3 nor `recoverPendingRuns` picks it up.
>   - Scope: the two workflow starters only. The general fix for `StartPlanningRun` / `StartRun`
>     stays out of scope (see Out of scope), because those orphans block nothing.
> - SMP never calls either starter (grep-verified). There is no SMP impact.

### 3. Gateway: workflow dispatch (F7, V1, V2)

**3.1 `WorkflowPlanningRunStarter`** (`service/internal/gateway/server.go:97-102`) gains
`StartWorkflowUpdatePlanningRun(ctx, workflowID, body string) (*PlanningRunResult, error)`.

- The interface is in `service/internal/`, so it has no external implementers.
- `workflowPlanningRunAdapter` (`service/cmd/spine/serve_wiring.go:90-102`) gets the matching
  method, which calls the orchestrator.
- Widening the interface (rather than adding an optional one) means every wired starter can
  carry updates (Decision 6).

**3.2 `handleWorkflowUpdate`** (`handlers_workflows.go:142-191`) mirrors create:

```go
ctx := r.Context()
if req.WriteContext == nil && !callerHasOperatorPrivileges(ctx) {
    starter := s.wfPlanningStarterFrom(ctx)
    if starter == nil { 503 unavailable — same message as create (:67-68) }
    if err := s.refuseIfWorkflowRunOpen(ctx, id); err != nil { WriteError; return }   // §3.3
    planning, err := starter.StartWorkflowUpdatePlanningRun(ctx, id, req.Body)
    if err != nil { WriteError; return }                       // 400/404/422 from the engine
    WriteJSON(w, http.StatusCreated, workflowPlanningResponse(planning, id))
    return
}
if req.WriteContext != nil {
    branch, err := s.resolveWorkflowWriteContext(ctx, req.WriteContext, id)        // below
    if err != nil { WriteError; return }
    ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
} else if callerHasOperatorPrivileges(ctx) {
    ctx = workflow.WithBypass(ctx)
}
```

- `needWorkflows` stays *before* the dispatch, as in create, so `TestWorkflow_UpdateNotConfigured`
  keeps its 503.
- The create planning body (`:76-86`) moves into a shared `workflowPlanningResponse` helper.
- `handleWorkflowCreate` calls `refuseIfWorkflowRunOpen` before its starter call and uses
  `resolveWorkflowWriteContext` in place of `:101-113`. Its now-stale comment (`:89-100`) is
  rewritten.
- The override rejection moves into the new helper, unchanged in effect. `workflow.Service` now
  consults the policy, but it still emits neither an ADR-009 override event nor its trailer
  (out of scope).

**`resolveWorkflowWriteContext(ctx, wc, id) (branch string, err error)`** (new):

1. `wc.Override` → 400 (text unchanged apart from "not yet").
2. `wc.RunID == ""` → 400 `invalid_params`: "write_context.run_id is required on workflow
   endpoints; omit write_context to open a workflow-lifecycle planning run (operators: omit it for
   the ADR-008 bypass)". This closes V1. A workflow `write_context` never means an authoritative
   write.
3. `GetRun`, then:
   - `run.Mode != planning || run.TaskPath != workflow.FilePath(id)` → 400, with detail
     `{run_task_path, workflow_path}`. This closes V2 (I7).
   - Not `active` → 400 (message unchanged from `:431-434`).
   - No branch → 400.
4. `s.authorizeRunBranchWrite(ctx, run)` (§4) → 403/409.
5. Return `run.BranchName`.

> **Design-check note (2026-10-06).** Step 3 trusts `run.Mode == planning && run.TaskPath ==
> workflows/<id>.yaml` to mean "this workflow's lifecycle run". At `61994fe` that is false (V4):
> `POST /runs` can mint such a run under any creation workflow. §3.5 makes the key sound at the
> producers (I9). With §3.5 in place, an extra `run.WorkflowID` check here is not required. Runs
> created before this task are the only exception, and they cannot carry a parseable workflow
> body (V4, third consequence).

**3.3 `refuseIfWorkflowRunOpen(ctx, id)`** (new; this is the "actionable 409"):

- `ValidateID(id)` first.
- Then `storeFrom(ctx).ListRunsByTask(ctx, workflows/<id>.yaml)`. If any run has
  `Mode == planning` and a non-terminal status (`domain.RunStatus.IsTerminal`) → 409 `conflict`:
  - message: "workflow-lifecycle run `<run>` is already open for `<path>`; stack edits on it with
    write_context {run_id} as its draft assignee, or wait for it to finish";
  - detail: `{run_id, branch_name, status, current_step_id}`.
- If `ListRunsByTask` fails → 503 (fail closed).
- With no store, the check is skipped, because the starter cannot run without one either.
- Why the check exists: two lifecycle runs on one file both pass the version-bump check against
  `main`. If they edit different lines, git merges both cleanly into a combined definition that
  no reviewer saw, under one ambiguous version label. The check is best effort (a millisecond
  race between two first requests). The merge-conflict path stays as the backstop.

> **Design-check note (2026-10-06): what the 409 can be held by.**
>
> - **Orphans (L1).** As designed, a `pending` orphan from a failed start counts as open, and
>   blocks the id until an operator cancels it. The §2 note (3b, 6) removes the orphan.
> - **Non-lifecycle runs (V4).** A contributor could open a `document-creation` planning run on
>   `workflows/<id>.yaml` and hold the 409. §3.5 removes that producer.
> - **Residual, accepted: an idle legitimate run.** A reviewer who opens a lifecycle run and never
>   submits holds the id. Other reviewers get the 409, which names the run.
>   - Recovery is an operator cancel or the operator bypass.
>   - The draft step has no timeout in `workflow-lifecycle.yaml`; only `review` has `168h`.
>   - This is inherent to "one open run per workflow" (owner question 3).

**3.4 Resulting contract** (`POST /workflows` and `PUT /workflows/{id}`):

| Caller / request | Before (`61994fe`) | After |
|---|---|---|
| reader, contributor | 403 | 403 (unchanged) |
| reviewer, no `write_context`, starter wired | create 201 planning; **update 200 authoritative commit to `main`** | 201 `write_mode: planning` (create and update) |
| reviewer, no `write_context`, no starter | create 503; **update 200 commit** | 503 (both) |
| reviewer, no `write_context`, open lifecycle run for the id | a second run (merge-time conflict or silent combination) | 409 + `run_id` |
| reviewer update planning: missing workflow / no version bump / bad id | n/a | 404 / 422 / 400; no branch, no run |
| any role, `write_context: {}` | **commit to `main`, no trailer** | 400 |
| `write_context.run_id` of a run not governing `workflows/<id>.yaml` | 201/200 `proposed` on that branch | 400 |
| `write_context` on the lifecycle run, caller is the draft assignee | 201/200 `proposed` | 201/200 `proposed` (unchanged) |
| `write_context` on the lifecycle run, caller is not the assignee / step unassigned / run in review | 201/200 | 403 / 409 / 409 (§4) |
| operator/admin, no `write_context` | 201/200 `bypass` + trailer | unchanged; with a policy installed the service re-checks the role |
| `write_context.override` | 400 | 400 (unchanged) |

> **Design-check note (2026-10-06): extra rows.**
>
> | Caller / request | Before (`61994fe`) | After |
> |---|---|---|
> | reviewer `POST /workflows` for an id that already exists | 409 `already_exists` + orphan `pending` run (L1) | 409 `already_exists`; no branch, no run |
> | contributor `POST /runs` `{mode: planning, task_path: workflows/<id>.yaml}` | 201, non-lifecycle planning run on a workflow path (V4) | 400 `use_operation: workflow.create` (§3.5) |
> | assignee `POST`/`PUT /artifacts` with a dot-segment path into `workflows/` | 201/200 on the run branch (V3) | 400 (§3.5; TASK-004 also refuses it at the service) |

### 3.5 Workflow-path producers (design-check addition, V3/V4, I9)

> **Design-check note (2026-10-06).** This section is new. It is a few lines of code, and it makes
> I7 and I9 hold by construction rather than by every caller remembering the rule.

1. **Canonical `isWorkflowTarget`** (`handlers_artifacts.go:18-21`).
   - Compute `p := path.Clean("/" + strings.TrimPrefix(raw, "/"))`.
   - Return `p == "/workflows" || strings.HasPrefix(p, "/workflows/")`.
   - All existing call sites, plus the new I7 guard on `run.TaskPath`, then reject `./workflows/x`,
     `a/../workflows/x` and `workflows//x` alike.
   - `workflows-guide.md` and `docs/workflows/x.md` stay non-workflow paths.
   - This is independent of TASK-004. It keeps the gateway guard correct even if TASK-004's
     service-level `Clean` is later relaxed, refactored, or bypassed by a new writer.
2. **`handleRunStart`, planning branch** (`handlers_workflow.go:68-75`).
   - Before calling the starter, `isWorkflowTarget(req.TaskPath)` → `workflowRejection("create")`
     (400, `use_operation: workflow.create`, `adr: ADR-007`).
   - The starter is not called, and no branch or run is created.
3. **Engine `StartPlanningRun`** (`core/engine/run.go:203`), as defence in depth for every
   embedder.
   - After the empty-argument checks, clean the path (`path.Clean`, leading `/` trimmed). If it
     equals `workflows` or starts with `workflows/` → `ErrInvalidParams` "workflow definitions are
     created through the workflow-lifecycle planning run (ADR-007/ADR-008)".
   - This runs before `artifact.Parse`, so nothing is persisted.
   - SMP's only caller is `spineembedded.CreateEntry` (SMP `artifacts_createentry.go:186`). It
     builds the path with `artifact.BuildArtifactPath` / `BuildDocumentPath`: `.md` files under
     the type directories (Spine `core/artifact/id_allocator.go:197-203`), never `workflows/`. So
     SMP behaviour is unchanged (§9 note).
4. `POST /artifacts/entry` and `/artifacts/add` build their paths server-side
   (`artifact.BuildArtifactPath`), so they need no change.

**New file `service/internal/gateway/run_branch_write.go`**, with
`func (s *Server) authorizeRunBranchWrite(ctx context.Context, run *domain.Run) error`.

- `actor := actorFromContext(ctx)`.
- `operator := actor != nil && actor.Role.HasAtLeast(domain.RoleOperator)`.

1. **No run-level cursor.** This covers `run.CurrentStepID == ""` (legacy rows) and a
   `divergence:` prefix (the diverged-run cursor, `core/engine/step.go:482`).
   - Operator, or a nil actor → allow.
   - Otherwise → 409: "run has no run-level current step; its branch accepts writes only from
     operators".
   - This matches TASK-016's "the parent run branch is operator-only while the run is diverged".
2. **Binding (I5)**, for a non-nil actor below operator:
   - `storeFrom(ctx)` nil → 503.
   - `ListStepExecutionsByRun(run.RunID)`, taking the *last* entry with
     `StepID == run.CurrentStepID && BranchID == ""`. The store orders by `created_at`; this is
     the same "latest attempt wins" rule as `storeStepAssigner` (`step_assign_fallback.go:57-67`).
   - Then:
     - No such execution, or a status other than `waiting`/`assigned`/`in_progress` → 409 "run has
       no open execution for its current step".
     - `ActorID == ""` or status `waiting` → 409. The message tells the caller to claim the step
       (`POST /execution/claim`) or have an operator assign it
       (`POST /runs/{run_id}/steps/{step_id}/assign`). Detail
       `{run_id, step_id, execution_id}` gives the claim target.
     - `ActorID != actor.ActorID` → 403. The message never names the holder (TASK-016 wire-message
       rule).
   - A nil actor exists only in dev mode, where `authorize` admits everything
     (`middleware.go:253-267`). The binding is skipped there, because there is no identity to
     bind. This is the same stance as the submit ownership check (`handlers_workflow.go:394-405`).
3. **Review freeze (I6)**, for every role:
   - `s.assignerFor(ctx)` (`step_assign_fallback.go:28-43`) → `LookupStepDef(ctx, run.RunID,
     run.CurrentStepID)`.
     - With an orchestrator wired, that is `Orchestrator.LookupStepDef`, which loads the run's
       *pinned* workflow (`core/engine/assign.go:82-92`).
     - Otherwise it is the store fallback (`step_assign_fallback.go:99-118`).
   - No assigner → 503.
   - A nil step definition → 409 "cannot resolve the definition of step …; run-branch writes are
     refused" (fail closed).
   - `stepDef.Type == domain.StepTypeReview` → 409, detail `{run_id, step_id, step_type}`:
     "run is in review step `<id>`; its branch is frozen until the review completes (a rework
     outcome returns it to an editable step)".
   - The freeze is judged from the step's declared `type`, never from its id.

**If TASK-016 has merged first** (expected: it is wave 2), step 2 must call its `core/refadmit`
predicate instead of repeating it:

- `refadmit.NewEvaluator(st).Decide(ctx, refadmit.Request{Ref: "refs/heads/" + run.BranchName, Actor: actor})`.
- The `run_branch` class, operator rule, active-run rule and bound-execution table are the same
  as TASK-016 §1.3 steps 2-7.
- One predicate then governs both the push path and the API path.
- If TASK-016 has not merged, implement step 2 as written. TASK-016 swaps it for `refadmit` when
  it lands.

**Call sites:**

- `resolveWriteContext` (`handlers_artifacts.go:420-455`). This covers `POST /artifacts` and
  `PUT /artifacts/*`.
  - After the run lookup and the `active`/branch checks, add the I7 guard first:
    `isWorkflowTarget(run.TaskPath)` (`:18-21`) → 400 "run `<id>` governs a workflow definition;
    artifact writes are not accepted on it".
  - Then the existing standard-run `task_path` checks (planning runs still skip them), then
    `authorizeRunBranchWrite`.
  - The empty-`run_id` early return (`:421-423`) is untouched. An artifact `write_context`
    without a run is still an authoritative write gated by `artifact.Service`'s policy.
- `resolveWorkflowWriteContext` (§3.2, step 4).
- `handleArtifactAdd` (`handlers_artifact_entry.go:147-264`).
  - After `needArtifacts`/`needGit` (`:213-221`) and before `resolveParentFromRef` (`:226`):
    the same I7 guard, then `authorizeRunBranchWrite`.
  - The existing `draft` check (`:199-206`) stays.
  - Authorization comes before any branch read, so an unbound caller learns nothing about the
    run branch's tree.

> **Design-check note (2026-10-06): four amendments to §4.**
>
> 1. **Freeze `internal` steps too (I6 amended; owner question 6).**
>    - In task-default, `review --accepted--> publish` (`type: internal`, `mode: spine_only`)
>      precedes the governed merge. During `publish` the run is `active`.
>    - Rule 2 then 409s contributors, because the publish execution has no actor. Operators,
>      however, pass rule 2 and are not frozen by rule 3 as written. An operator API write could
>      therefore land after approval and before the merge.
>    - Once TASK-005 binds the merge to the reviewed SHA, that becomes a stuck run rather than an
>      integrity break. It is still exactly what I6 exists to refuse.
>    - Rule 3 therefore tests `stepDef.Type == StepTypeReview || stepDef.Type == StepTypeInternal`.
>    - No legitimate API writer exists during an `internal` step: the engine runs those through
>      `activateInternalStep` and never through the API.
>    - `convergence` steps stay unfrozen. The binding still applies, and a diverged run is
>      operator-only under rule 1.
>    - The 409 detail keeps `step_type`, so a client can tell "in review" from "publishing".
> 2. **Residual, not closed: a claim takeover (P3-6, owner question 7).**
>    - I5 binds to *whoever holds* the current step. `hybrid` and `human_only` steps stay
>      `waiting` after activation (`core/engine/step.go:22-35`, `:70-83`). That covers every new
>      planning run, every `needs_rework` loop, every `execution.release` and every restart
>      recovery.
>    - `execution.claim` is `RoleContributor` (`core/auth/permissions.go:47`). `ClaimStep` checks
>      only the allow-list, the actor type and the skills (`core/engine/claim.go:52-142`), and in
>      standalone a contributor can self-grant skills until TASK-013.
>    - So mallory can claim alice's waiting `draft` and then write.
>    - After this task, that is an **attributed** takeover, not a silent injection:
>      - it is recorded in `step_executions.actor_id` and `actor_assignments`, plus a
>        `step_assigned` event;
>      - alice gets 409 or 403;
>      - the content still has to pass a review that is now frozen;
>      - TASK-007's separation of duties stops mallory reviewing it.
>    - Closing it fully is a rule about *who may claim a run's step*, which is TASK-007's
>      step-entry policy (for example, "rework is re-offered only to the prior draft actor
>      unless an operator assigns it"). It is not a rule about who may write.
> 3. **Time-of-check gap (accepted).** `authorizeRunBranchWrite` reads the run and its executions
>    once, before the write.
>    - The only non-operator who passes the binding is the assignee. So the gap is the assignee
>      racing their own `step.submit` with a write: a single request's duration.
>    - TASK-005's reviewed-SHA binding is the backstop.
>    - No AC: the race is not deterministic in a unit test.
> 4. **The store fallback is not version-pinned.** `storeStepAssigner.LookupStepDef` reads the
>    *current* workflow projection (`step_assign_fallback.go:104`), not the run's pinned version.
>    - It is reached only when no orchestrator is wired (`assignerFor`, `:28-43`), a degenerate
>      configuration with no run engine.
>    - A missing step still fails closed (409).
>    - No change; recorded so the "pinned" claim above is read as orchestrator-only.

### 5. Standalone wiring

- **`service/cmd/spine/serve_builders.go`.** New `buildWorkflowService(gitClient, repoPath, st)`.
  It builds `workflow.NewService` and installs `buildBranchProtectPolicy(st)` (`:68-77`), exactly
  as `buildArtifactService` does (`:44-56`). That means the projection-backed policy with a Store,
  and the permissive policy without one (early bootstrap).

  > **Design-check note (2026-10-06).** `buildBranchProtectPolicy` returns
  > `(branchprotect.Policy, error)`, so `buildWorkflowService` returns
  > `(*workflow.Service, error)`, and `buildServerConfig` fails startup on that error, the same as
  > the artifact path (`serve_wiring.go:277-280`). It must not swallow the error into a nil
  > policy, because a nil policy is the I4 "no protection" mode.
- **`service/cmd/spine/serve_wiring.go`:**
  - `:281` uses `buildWorkflowService`.
  - In the `if orch != nil` block (`:403-410`), add `orch.WithWorkflowWriter(workflowSvc)` and
    `wfPlanningStarter = &workflowPlanningRunAdapter{orch: orch}`.
  - Add `WFPlanningStarter: wfPlanningStarter` to the `ServerConfig` literal (`:424-461`).
  - Without this, single-workspace (`WORKSPACE_RESOLVER=file`) deployments have no workflow
    planning starter. Pooled workspaces wire one in `serve_resolver.go:125-140`. Reviewers would
    then get 503 for every update, with no governed path (Decision 9).
- **`service/internal/workspace/pool.go:948`.** After `workflow.NewService`, install
  `branchprotect.New(bpprojection.New(st))` when `st != nil`, otherwise
  `branchprotect.NewPermissive()`. This mirrors the artifact service at `:934-943`.
- **Not touched:**
  - `service/scenariotest/harness/runtime.go:115` keeps a policy-less `workflow.Service`
    (I4, unchanged behaviour);
  - `cmd/embed-smoketest` (no `workflow.Service`).

### 6. Behaviour changes for standalone callers

These are intentional. Each is covered by an acceptance criterion and documented in §7.

1. Reviewer `PUT /workflows/{id}` without `write_context` now returns **201** with a planning-run
   body instead of 200 with a commit. The `spine workflow update` CLI accepts any status below
   400 and prints the body (`service/internal/cli/client.go:108`), so it needs no change.
2. Writes to a run branch through `write_context` or `/artifacts/add` now need the caller to hold
   the current step.
   - After `POST /artifacts/entry` or a workflow planning run starts, the draft step is `waiting`
     with no actor (`core/engine/step.go:24-34`, `:73-84`).
   - The author claims it (`POST /execution/claim`) before stacking edits.
   - The same claim is already required before the draft can be submitted
     (`handlers_workflow.go:418-424`), so the flow gains no new step, only an earlier one.
   - After `needs_rework`, the reworked step must be claimed again.
3. No API write lands on a run branch during a review step. This includes operators. Recovery is
   `needs_rework`, cancel, or an operator push (TASK-016).
4. `write_context: {}` on workflow endpoints is a 400 for every role. Operators who sent it
   previously got an *untrailered* direct commit. They now omit `write_context` and get the
   audited bypass.
5. With a store configured, a non-bypass `workflow.Service` write to a protected `main` is
   refused at the service (403) even if a future handler forgets the dispatch.

> **Design-check note (2026-10-06): further intentional changes** (documented as in §7).
>
> 6. `POST /runs` with `mode: planning` and a `task_path` under `workflows/` returns 400
>    `use_operation: workflow.create`. `StartPlanningRun` refuses the same path. No shipped
>    client sends one: the CLI and `POST /artifacts/entry` build `.md` paths.
> 7. A reviewer `POST /workflows` for an existing id still returns 409 `already_exists`, but now
>    before any branch or run exists. An injected writer failure leaves the run `failed`, not
>    `pending`.
> 8. API writes are also refused while the current step is `type: internal` (for example
>    task-default `publish`), for every role.
> 9. Dot-segment artifact paths that resolve into `workflows/` return 400 at the gateway.

### 7. Documentation

- **`architecture/api-operations.md`:**
  - §3.1 (`:63-67`, `:93`): `write_context` with a `run_id`, and `artifact.add`, are accepted only
    from the current step's assignee or an operator, never during a review step (409). A run that
    governs a workflow definition accepts no artifact writes.
  - Workflow-lifecycle planning runs are the documented exception to "creating new artifacts
    only" (`:67`). They carry exactly one workflow definition, new or updated.
  - §3.2 (`:114`, `:121-124`):
    - reviewer `workflow.update` opens an update planning run (201), or returns 503 / 409;
    - a workflow `write_context` requires a `run_id` that names that workflow's lifecycle run, and
      the caller must be its draft assignee;
    - `workflow.Service` enforces `no-direct-write` for non-bypass writes;
    - the bypass requires operator+.
- **`api/spec.yaml`:**
  - `POST /workflows` (`:258-290`) and `PUT /workflows/{id}` (`:354-388`) gain the 201 planning
    response schema (`WorkflowPlanningResponse`, today undocumented even for create), and the
    `409` and `503` responses.
  - The `WriteContext` description (`:1056-…`) states the binding, the review freeze and the
    workflow `run_id` rule.
  - `/artifacts/add` (`:110-140`) gains 403 and 409.
- **`architecture/access-surface.md:167`.** Update is now routed like create; the assignee rule.
- **`architecture/security-model.md:307`, `:312`.** Workflow writes are now evaluated by
  branch protection. Override on workflow endpoints is still not supported.
- **ADR-008.** Add an "Amended by INIT-023 TASK-014" note to §1:
  - updates open their own lifecycle run;
  - a workflow `write_context` binds to the run that governs that workflow and to its draft
    assignee;
  - the branch is frozen during review.
> **Design-check note (2026-10-06): documentation additions.**
>
> - **`api/spec.yaml` `POST /runs`.** A planning-mode `task_path` under `workflows/` returns 400
>   (`use_operation: workflow.create`).
> - **`api/spec.yaml` `WriteContext`.** The freeze covers `internal` steps too.
> - **`api-operations.md` §3.1.** Workflow definitions are never artifact-surface paths,
>   whatever the path spelling.
> - **ADR-008 amendment note.** Add "one open lifecycle run per workflow; a failed start leaves
>   no open run".

- **`CHANGELOG.md` staging area.**
  - Add a "Security (INIT-023 TASK-014)" entry.
  - Rewrite the "Not yet implemented" bullet (`:83-88`). Its "`workflow.Service` does not consult
    `branchprotect.Policy` yet" is no longer true. Override stays unsupported.

### 8. Regression tests

The audit PoCs are inverted into committed tests that fail on `61994fe`. Drafts live in
`_work/task014/regression/`. They are a starting point, not a mandate.

- **`service/internal/gateway/security_f7_p36_regression_test.go`** uses only symbols that exist
  at `61994fe`, so it compiles against the pre-fix tree.
  - **F7 tests:**
    - `TestF7_ReviewerUpdate_RoutedToPlanningRun_MainUntouched` (from
      `poc/standalone-service/poc_reviewer_workflow_update`);
    - `TestF7_ReviewerCannotRewriteLifecycleOnMain` (from `poc/verify-r1-5`, keeping its create
      control);
    - `TestF7_EmptyWriteContext_RefusedOnWorkflowEndpoints` (V1);
    - `TestF7_WorkflowWriteContext_OtherWorkflowsRun_Refused` (V2).
  - **P3-6 tests:**
    - `TestP36_UnassignedContributor_CannotWriteForeignRunBranch` (from
      `poc/standalone-service/poc_writecontext_foreign_run`);
    - `TestP36_R12_ReviewFreeze_InjectionNeverReachesMain` (from `poc/verify-r1-12`, keeping the
      real `engine.MergeRunBranch`);
    - `TestP36_ArtifactsAdd_RequiresDraftAssignee`.
  - **Guards** (pass before and after): `TestF7_OperatorUpdate_KeepsBypassTrailer` and
    `TestP36_Assignee_CanWriteOwnRunBranch`.
  - The planner double implements both starter methods, so it satisfies the interface before and
    after the change.
  - Seeded workflow bodies are read relative to the test file (`runtime.Caller`), not from an
    absolute path.
- **`core/workflow/policy_test.go`** (new API):
  - deny of a non-bypass write to `main`;
  - bypass requires operator (reviewer and actor-less are refused; operator writes with the
    trailer);
  - a run-branch write is allowed;
  - an evaluation error fails closed;
  - `TestWorkflowService_NilPolicy_KeepsPreTask014Behaviour`.
- **`core/engine`:**
  - `TestStartWorkflowUpdatePlanningRun_WritesUpdateOnRunBranch`;
  - `TestStartWorkflowUpdatePlanningRun_RefusesBeforeAnyBranch` (missing → 404, same version →
    422, bad id → 400; no branch created, no writer call);
  - `mockWorkflowWriter` (`run_test.go:1229-1250`) gains `Update`.
- **Gateway unit tests:**
  - `TestWorkflowUpdate_RoleDispatchTable`, mirroring `TestWorkflowCreate_RoleDispatchTable`
    (`handlers_workflows_test.go:587-639`);
  - `TestWorkflowUpdate_ReviewerNoPlanner_Returns503`;
  - `TestWorkflow{Create,Update}_OpenLifecycleRun_Returns409`;
  - `TestAuthorizeRunBranchWrite_Matrix` (§4 rules 1-3 × roles × step states).
- **Scenario** (`//go:build scenario`):
  - `TestWorkflowLifecycle_UpdateExisting` in
    `service/scenariotest/scenarios/workflow_lifecycle_test.go`. It covers
    `StartWorkflowUpdatePlanningRun("task-spike", bumped)` → draft → review → approved → `main`
    has the new version. `task-spike` is seeded by the harness (`harness/repo.go:359`).
  - A new step, `engine.StartWorkflowUpdatePlanningRun`, goes into
    `service/scenariotest/engine/workflow_steps.go` next to `:66-93`.

**Existing tests that change.** All are in `service/internal/gateway`. Running `go test ./...`
with the design prototype overlay on 2026-10-06 showed that these nine are the only failures in
the whole module. Each changes only by contract or fixture.

- `TestWorkflowUpdate_Success` (`handlers_workflows_test.go:212-227`). It encodes the F7 bypass
  as success, so it is rewritten into two tests:
  - `TestWorkflowUpdate_OperatorNoWriteContext_Bypass` (200, `write_mode: bypass`,
    `lastUpdateBypass`);
  - `TestWorkflowUpdate_ReviewerNoWriteContext_StartsPlanningRun` (201 planning, `svc.Update`
    not called).
- `TestWorkflowUpdate_NotFound` (`:229-241`). The reviewer path now dispatches, so the test
  either:
  - uses the operator role (bypass → `svc.Update` → 404); or
  - adds a reviewer variant whose starter returns `ErrNotFound` → 404.
- `TestWorkflowCreate_WithWriteContext_RoutesToBranch` (`:641-664`) and
  `TestWorkflowUpdate_WithWriteContext_RoutesToBranch` (`:672-694`). The `workflowPlanningStore`
  fixture (`:447-462`) gains:
  - `TaskPath: workflows/task-default.yaml`;
  - `CurrentStepID: "draft"`;
  - a draft execution bound to `actor-1`;
  - a `StepAssigner` double reporting `draft` as `manual`.
- `fakeWFPlanningStarter` (`:419-445`) gains `StartWorkflowUpdatePlanningRun`.
- `TestArtifactUpdate_WithPlanningWriteContext` (`handlers_coverage2_test.go:945-987`), plus four
  `/artifacts/add` tests:
  - `TestHandleArtifactAdd_PlanningMode_Success` and `…_ParentNotFound`
    (`handlers_artifact_entry_deep_test.go`);
  - `…_EpicWithInitiativeParent` and `…_WrongParentForEpic` (`handlers_coverage_test.go`).

  These bind `contrib-1` to the `draft` execution in their store fixture and provide a step
  definition. Their assertions are unchanged.
- Unchanged and green: `TestWorkflowCreate_RoleDispatchTable`,
  `TestResolveWriteContext_PlanningRunNoTaskPath` (admin, empty cursor), every
  `override_e2e_test.go` case, and the `serve_smoke_test.go` probes.

> **Design-check note (2026-10-06): verification and added tests.**
>
> - **Re-run on 2026-10-06, all confirmed:**
>   - head overlay: 7 FAIL and 2 guards PASS;
>   - fix overlay: 9/9 PASS;
>   - full `go test ./...` on the fix overlay: exactly the nine listed gateway tests fail;
>   - mutations m1-m4 each turn the named tests red;
>   - `go vet -tags integration ./service/...`: clean.
>   - Scenario tests call the services directly, not through the gateway, so the new binding
>     never applies to them.
> - **Added tests** (AC 3, AC 6, AC 8 and AC 17):
>   - `core/engine`:
>     - `TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun` (no `CreateRun`, no branch);
>     - `TestStartWorkflowPlanningRun_WriterFailure_MarksRunFailed` (create and update);
>     - `TestStartPlanningRun_RefusesWorkflowPath` (`workflows/x.yaml`, `./workflows/x.yaml`,
>       `/workflows/x.yaml`; no branch, no run record).
>   - gateway:
>     - `TestIsWorkflowTarget_Canonical` (table);
>     - `TestArtifactWrite_DotSegmentWorkflowPath_Refused` (POST and PUT, from
>       `_work/task014-check/zz_t014check_probe_test.go`);
>     - `TestRunStart_Planning_WorkflowTaskPath_Refused` (from `zz_t014check_runstart_test.go`);
>     - `TestWorkflowUpdate_FailedRunDoesNotBlock` (a `failed` run on the file → the reviewer PUT
>       reaches the starter);
>     - `TestAuthorizeRunBranchWrite_Matrix` gains `internal`-step rows.

### 9. SMP: compatibility gate and dependent work

**Gate: SMP's workflow write path keeps working unchanged.**

- `spinecore.BuildWorkflowService` builds `workflow.NewService(gitClient, scope.RepoDir)` with no
  policy (`internal/customer/spinecore/workflowwriter.go:343`).
- Every embedded write goes through `embeddedWorkflowWriteContext`, which is
  `workflow.WithBypass(bridgeActorContext(ctx))`
  (`internal/customer/spineembedded/embedded_workflows.go:118-120`).
  `bridgeActorContext` binds only `observe` actor and trace IDs, not a `domain.Actor`
  (`embedded_tasks.go:191-199`).
- Under I4 (nil policy means no evaluation and no role check), these writes behave byte-for-byte
  as at `61994fe`. `TestWorkflowService_NilPolicy_KeepsPreTask014Behaviour` pins exactly this
  shape: an actor-less bypass and a non-bypass write.
- **Verified on 2026-10-06** with the design prototype as a `-overlay` over `../spine`:
  - SMP `go build -mod=readonly ./cmd/... ./internal/...` succeeded;
  - `go vet ./internal/customer/spinecore/... ./internal/customer/spineembedded/...` succeeded;
  - `go test -run Workflow` in both packages passed.

**API impact on SMP.** Additive only:

- `workflow.Service.WithPolicy`, `WithDefaultRef`, `workflow.ValidateID` and `workflow.FilePath`;
- `Orchestrator.StartWorkflowUpdatePlanningRun`;
- `engine.WorkflowWriter.Update`. SMP neither implements this interface nor calls
  `WithWorkflowWriter`.

> **Design-check note (2026-10-06): SMP re-verified, plus one engine refusal SMP reaches.**
>
> - **Re-verified against the prototype overlay:**
>   - `go build -mod=readonly ./cmd/... ./internal/...`: OK;
>   - `go test -run Workflow ./internal/customer/spinecore/ ./internal/customer/spineembedded/
>     ./internal/customer/workflow/`: OK;
>   - the SMP tree stayed clean.
> - **No promoted methods.** SMP's `spinecore.WorkflowWriter` holds the service as a *named* field
>   (`Service *workflow.Service`, SMP `workflowwriter.go:51-56`), not an embedding. So the new
>   `WithPolicy` and `WithDefaultRef` add no methods to SMP's type, and nothing calls them.
> - **The §3.5 item 3 refusal is reached by SMP.** `StartPlanningRun` gains a refusal that SMP's
>   `spineembedded.CreateEntry` does call. Its paths come from `BuildArtifactPath` /
>   `BuildDocumentPath` and are never under `workflows/`, so behaviour is unchanged.
> - **SMP TASK-012 gate.** It must include the `spineembedded` CreateEntry tests, which are
>   already inside `./internal/customer/spineembedded/...`.

`service/internal/gateway` is not importable by SMP.

**Contract SMP must respect from now on.** SMP must not call `WithPolicy` on its
`workflow.Service` unless it also binds a `domain.Actor` of role ≥ operator into the write
context. Otherwise every admin bypass write would be refused (I2). No SMP task needs to do that.
SMP's workflow surface is admin-only by its own permission map.

**What the dependent SMP task must do.**

- **SMP TASK-012** (the `.spine-revision` bump to the INIT-023 closing commit) is the only SMP
  consumer. No dedicated SMP adaptation task is needed. TASK-012 must:
  - run the full gates;
  - re-run `go test ./internal/customer/spinecore/... ./internal/customer/spineembedded/...`
    (workflow writer and embedded workflows);
  - confirm the admin `PUT /workflows/{id}` still returns 200 `write_mode: bypass`, with the
    `Workflow-Bypass: true` trailer on origin.
- **Flagged for the orchestrator** (outside TASK-014, which changes no SMP code): SMP's own API
  writes onto planning-run branches have the same shape P3-6 closes here.
  - `embeddedArtifacts.AddToRun` (`internal/customer/spineembedded/artifacts_addtorun.go:37-81`)
    and `WriteContent` (`artifacts_content.go:57`) check planning, active and the `draft` step,
    but not that the caller is the draft assignee.
  - `artifact.add` is `member` (`internal/customer/workspace/permissions.go:34`).
  - SMP auto-assigns the entry step to the creator
    (`internal/customer/artifact/handler.go:263-294`), so the predicate is available.
  - Recommendation: SMP TASK-007 (run ownership) should apply its run-branch rule to these two API
    writes as well as to pushes, or a new SMP task should.

### 10. Coordination and ordering

- **TASK-004 (hard; `blocked_by`).** It rewrites `workflow.Service.writeAndCommit`'s file I/O
  (`:144-194`). This task inserts the policy call just above that, at `:137`. Rebase onto TASK-004
  and keep its `worktreefs` sinks untouched.
- **TASK-016 (soft, recommended).**
  - Reuse its `core/refadmit` bound-actor predicate (§4 step 2), so that push admission and API
    admission are one rule.
  - TASK-016's default run-branch rule does **not** freeze review steps (its owner question 1,
    alternative (b)). See owner question 5 here.
- **TASK-005.** It binds the governed merge to the reviewed SHA. The API review freeze is the
  front-door complement: the reviewer can no longer amend what they are about to approve through
  the API.
- **TASK-006.**
  - Its lifecycle exemption keys on `run.TaskPath` (TASK-006 §1, item 5). I7 makes that key sound: an
    API workflow write lands only on the run that governs that file.
  - When TASK-006's guard is enabled without the exemption, approved reviewer update runs will
    hold for operator approval at merge. That is TASK-006's intended behaviour.
- **TASK-007.** It tightens *who may be bound* to a step (eligibility and separation of duties).
  This task consumes the binding and does not change it.

  > **Design-check note (2026-10-06): F7's "no second reviewer" clause needs TASK-007 and
  > TASK-013.**
  >
  > - **What TASK-014 alone still allows.** One reviewer can take a workflow edit to `main` with
  >   no second person:
  >   1. open the update run (201);
  >   2. claim `draft`, which needs `workflow_authoring`;
  >   3. submit;
  >   4. claim `review`, which needs `workflow_review`;
  >   5. approve, and the run merges.
  > - **Why.** Both skills can be self-granted in standalone until TASK-013 (F6;
  >   `skill.create`/`skill.update` are `RoleContributor`, `core/auth/permissions.go:80-83`).
  >   `workflow-lifecycle.yaml` has no separation of duties, and `ClaimStep` has none either
  >   (`core/engine/claim.go:52-142`).
  > - **What TASK-014 still adds.** It turns the audit's silent direct commit into an audited
  >   lifecycle run with one actor on both steps.
  > - **What closes the clause.** TASK-007 §7: separation of duties on `review`-type steps,
  >   strict by default in `spine serve`, so a draft author cannot claim the review. TASK-013
  >   then makes the skill gates meaningful.
  > - **Coordination.** AC 19 pins the end-to-end property once TASK-007 has merged. If TASK-014
  >   merges first, its Implementation Notes record the clause as open pending TASK-007, and the
  >   TASK-007 PR adds the AC 19 scenario.
  > - **TASK-007 also owns the claim-takeover residual** (§4 design-check note, item 2).
- **TASK-012.** The standalone shared working tree means a bypass write lands on whatever branch
  the tree has checked out. Serialising and pinning the tree is TASK-012. This task changes
  nothing there.
- **TASK-002.** If it qualifies the base ref `createRunBranches` uses, the update pre-check read
  (§2, step 3) must use the same ref.
- **Wave 3**, risk medium. There is no migration and no SMP code change.

## Decisions

1. **Extend the planning-run machinery for updates, instead of refusing reviewer updates.**
   ADR-008 §1 promises reviewers a governed edit path, and `workflow-lifecycle.yaml` already
   describes itself as governing "creation and modification".
   - *Rejected: 409 or 503 for every reviewer update.* There is no API to open a lifecycle run
     for an existing workflow (defect 3), so reviewers would lose the only edit path they have,
     and operators would become the de facto editors.
2. **Mirror create's role dispatch exactly** (reviewer → planning, operator+ → bypass), using the
   same `callerHasOperatorPrivileges` (`HasAtLeast`).
   - *Rejected: forcing operators through the lifecycle too.* That would remove the ADR-008 §5
     recovery hatch for a broken `workflow-lifecycle`.
3. **nil policy keeps today's behaviour (I4).**
   - *Rejected: fail closed on a nil policy, like `artifact.Service`* (`core/artifact/service.go:394-398`).
     SMP builds `workflow.Service` with no policy and an actor-less, bypass-only context, so every
     SMP admin workflow write would fail.
   - Standalone gets protection by wiring the policy (§5), not by changing the zero value.
4. **A bypass skips rule evaluation but is role-gated.**
   - *Rejected: evaluating the bypass as an ADR-009 `Override`.* That would emit a
     `branch_protection.override` event and trailer for every bypass, mixing two audit mechanisms.
     Unifying them is the open scope decision in `CHANGELOG.md:86-88`.
5. **P3-6 lives in the gateway, in one helper.**
   - The artifact and workflow services do not know runs.
   - The standalone gateway is the only surface that turns a `run_id` into a branch write.
   - *Rejected: an engine API.* It would widen `engine` for a standalone-only HTTP concern.
6. **Widen `WorkflowPlanningRunStarter`, rather than adding an optional update interface.** It is
   internal, has one production implementer, and a compile-time guarantee beats a runtime
   type-assertion.
   - Consequence: the original F7 PoCs' create-only planner no longer compiles. Their inverted
     copies (§8) are the parity evidence.
7. **The binding is the run-level execution of the current step, in `assigned` or `in_progress`.**
   This is TASK-016's predicate.
   - *Rejected: "any non-terminal execution of the run".* It is looser, and lets a finished
     executor keep writing during later steps.
   - *Rejected: "the run's creator".* `domain.Run` records no creator (`core/domain/run.go:51-91`).
8. **The review freeze is judged from the step definition's `type`, loaded at the run's pinned
   version, and fails closed when it is unknown.**
   - *Rejected: matching the step id `"review"`.* Planning workflows name review steps freely.
9. **Wire the workflow planning starter in single-workspace mode.**
   - *Rejected: leaving it pooled-only.* After this task, file-resolver deployments would have no
     governed workflow edit path at all.
10. **An empty workflow `write_context` is a 400, not "treat as absent".** A client that sent
    `write_context` meant a branch write. Silently opening a planning run would hide the client
    bug.
11. **One open lifecycle run per workflow (409).**
    - *Rejected: relying on merge conflicts.* Edits to disjoint lines merge cleanly into a
      definition no reviewer saw.
    - *Rejected: putting the check in the engine.* `engine.RunStore` has no `ListRunsByTask`
      (`core/engine/interfaces.go:20-53`), and widening it would touch SMP's store contract.
12. **(Design check) One producer of workflow task paths (I9), enforced at every producer.**
    - The gateway `isWorkflowTarget` is canonicalised, `handleRunStart` refuses workflow paths
      for planning runs, and the engine `StartPlanningRun` refuses them.
    - *Rejected: only adding a `run.WorkflowID` check in §3.2.* It leaves V4's junk merge and the
      contributor-held 409 lock in place.
    - *Rejected: relying on TASK-004's `Clean`.* It closes V3 but not V4, since
      `workflows/x.yaml` is canonical.
13. **(Design check) Workflow starters never leave a non-terminal orphan.**
    - Create gets the same pre-check as update.
    - A writer failure after `CreateRun` marks the record `failed` through the existing
      `TransitionRunStatus`.
    - *Rejected: §3.3 ignoring `pending` runs.* `recoverPendingRuns` promotes orphans to `active`.
    - *Rejected: the general orphan fix for every starter.* Only the workflow starters gained a
      reader (§3.3) that orphans can block.
14. **(Design check) Freeze `internal` steps as well as `review` steps.**
    - It closes the post-approval operator window at no cost: no API writer exists during
      engine-owned steps.
15. **(Design check) Leave the claim-takeover residual to TASK-007.**
    - Who may *claim* a run's step is step-entry policy.
    - Who may *write* the run branch, given a holder, is this task.
    - Duplicating a claim rule in the gateway would make two policies disagree.

## Owner questions

The design works without answers, using the stated defaults.

1. **Should operators be exempt from the API review freeze?**
   - *Default:* no. Everyone is frozen, and operators recover through `needs_rework`, cancel, or
     an operator push (TASK-016).
   - *Alternative:* exempt operators, and emit a governance event per write.
2. **Should a workflow `write_context` be limited to the workflow's own lifecycle run (I7)?**
   - *Default:* yes. That refuses writing a workflow onto a standard task run, which `61994fe`
     allows when `task_path` matches.
   - *Alternative:* also allow the task run whose `task_path` matches, as today. This reopens V2
     for task runs reviewed without `workflow_review`.
3. **Should there be one open lifecycle run per workflow, with a 409, for create as well as
   update?**
   - *Default:* yes, for both.
   - *Alternative:* update only, keeping create's current "second run fails at merge" behaviour.
4. **Should starting a planning run auto-bind its entry step to the caller**, as SMP does
   (`internal/customer/artifact/handler.go:263-294`)?
   - *Default:* no change here. The author claims the draft step, with `ClaimStep`'s eligibility
     checks.
   - If yes, it belongs with TASK-007's step-entry policy.
5. **Should TASK-016's push admission also freeze run branches during review steps** (its
   alternative (b)), so the API and push paths agree?
   - *Default:* yes. This is a recommendation to TASK-016's owner question 1. Without it, the
     review assignee can still push to the branch under review.

> **Design-check note (2026-10-06) on questions 3 and 4.**
>
> - **Q3.** With the L1 fix, only a *live* lifecycle run holds the 409. An idle reviewer can
>   still hold one indefinitely; recovery is an operator cancel or the operator bypass (§3.3
>   note). Choosing "update only" would not remove that.
> - **Q4.** Auto-binding the entry step removes the claim-takeover window only on run start,
>   not after `needs_rework`, `release` or restart recovery (§4 note, item 2).
>   - It would also stop pull-based agents from claiming `hybrid` draft steps.
>   - The default stays "no", and the residual goes to question 7.

6. **(Design check) Should the API freeze cover `internal` steps as well as `review` steps?**
   - *Default:* yes (Decision 14). It closes the operator window between approval and the
     governed merge (task-default `publish`).
   - *Alternative:* review only, relying on TASK-005 to make such writes harmless (a stuck run).
7. **(Design check) Accept the claim-takeover residual of P3-6 here, and hand "who may claim a
   run's waiting step" to TASK-007?**
   - The residual: any eligible contributor can claim another actor's *waiting* step, then
     write. After this task that is attributed, and the content is still reviewed.
   - *Default:* yes, accept it here. TASK-007 adds a "rework and released steps are re-offered
     to the prior holder unless an operator assigns" option, strict in `spine serve`.
   - *Alternative:* have TASK-014 refuse run-branch writes from an actor who did not hold the
     run's *entry* step. This is cheaper but wrong for multi-actor runs, for example task-default
     `draft` (ai_only) followed by `execute` (human).

## Acceptance Criteria

1. **Policy on `workflow.Service`.** `core/workflow` exposes `(*Service).WithPolicy`,
   `(*Service).WithDefaultRef`, `ValidateID` and `FilePath`. `core/workflow/policy_test.go` shows,
   with `branchprotect.StaticRules(nil)` (bootstrap: `main` `no-direct-write`):
   - **(a)** A reviewer actor's non-bypass `Update` to `main` returns `ErrForbidden` with detail
     `branch`/`reasons`, and `main`'s SHA is unchanged.
   - **(b)** A write with a `WriteContext{Branch: "spine/plan/…"}` succeeds.
   - **(c)** A policy evaluation error returns `ErrInternal`, and nothing is written.
   - **(d)** A bypass write by a reviewer actor, or with no actor, is refused. An operator's
     bypass write succeeds, and its commit carries `Workflow-Bypass: true`.
   - *Mutation checks:*
     - removing the `checkBranchProtection` call fails (a);
     - removing the bypass role check fails (d) (mutation m3 in `_work/task014/mut`).
2. **The nil-policy path is unchanged (SMP gate).**
   `TestWorkflowService_NilPolicy_KeepsPreTask014Behaviour`: with no policy, an actor-less
   `WithBypass` write and an actor-less non-bypass write both commit to `main`.
   - *Mutation check:* making nil fail closed fails it (m4).
3. **Update-capable planning runs.**
   - `engine.WorkflowWriter` declares `Update`.
   - `StartWorkflowUpdatePlanningRun` writes through `Update` on the new run branch. The run has
     `Mode: planning`, `TaskPath: workflows/<id>.yaml` and the `workflow-lifecycle` binding.
   - Each of these refuses with **no branch created and no writer call**:
     - a missing workflow → `ErrNotFound`;
     - an unchanged version → `ErrValidationFailed`;
     - an invalid id (`../x`) → `ErrInvalidParams`.
   - Every existing `TestStartWorkflowPlanningRun_*` passes unmodified.
   - *Design-check addition (2026-10-06), L1:*
     - **Create pre-check.** `StartWorkflowPlanningRun` for an id that exists at `HEAD` returns
       `ErrAlreadyExists` with no `CreateRun` call, no branch and no writer call
       (`TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun`).
     - **Writer failure.** For create and for update, a writer error after `CreateRun` leaves the
       run record `failed`, never `pending`. The branch-cleanup calls are unchanged
       (`TestStartWorkflowPlanningRun_WriterFailure_MarksRunFailed`).
     - *Mutation checks:* removing the create pre-check, or the `failed` transition, fails the
       corresponding test.
     - The design-check probe `TestT014Check_CreateExistingWorkflow_LeavesPendingRun` must fail
       against the task branch, because no run is left behind.
4. **F7 dispatch.** `TestWorkflowUpdate_RoleDispatchTable` covers every `ActorRole`:
   - reader and contributor → 403;
   - reviewer → 201 `write_mode: planning`, with the update starter called and `svc.Update` not
     called;
   - operator and admin → 200 `write_mode: bypass`, with the bypass context set and the starter
     not called.
   - A reviewer with no starter → 503, and `svc.Update` is not called.
   - A reviewer whose starter returns `ErrNotFound` → 404.
   - *Mutation check:* removing the new dispatch branch fails the reviewer row.
5. **`TestWorkflowUpdate_Success` is rewritten** into the operator-bypass and reviewer-planning
   tests (§8), and `TestWorkflowUpdate_NotFound` is adapted. Both are listed in the PR description
   as intentional contract changes.
6. **Actionable 409.** If `ListRunsByTask` returns a `planning` run in a non-terminal state for
   `workflows/<id>.yaml`:
   - a reviewer `PUT` without `write_context` returns 409 with `detail.run_id`, and the starter is
     not called;
   - a reviewer `POST` does the same (owner question 3 default).
   - A `completed`/`failed`/`cancelled` run does not block.
   - A `ListRunsByTask` error returns 503.
   - *Design-check addition (2026-10-06):* after AC 3's writer-failure case, a reviewer `PUT` for
     the same id reaches the update starter, not 409 (`TestWorkflowUpdate_FailedRunDoesNotBlock`).
     This is end-to-end evidence that a failed start never locks a workflow.
7. **Workflow `write_context` (V1, V2).**
   - `{}` returns 400 on both `POST /workflows` and `PUT /workflows/{id}`, for reviewer and
     operator alike, and `main` is unchanged.
   - A `run_id` whose run is not `planning` or whose `TaskPath` is not `workflows/<id>.yaml`
     returns 400, and that run's branch is unchanged.
   - `override: true` still returns 400.
   - The draft assignee on the right lifecycle run gets 201/200 `write_mode: proposed`, with the
     write on that branch.
8. **P3-6 binding and freeze.** `TestAuthorizeRunBranchWrite_Matrix` drives each rule through
   `POST /artifacts`, `PUT /artifacts/*`, `POST`/`PUT /workflows` with `write_context`, and
   `POST /artifacts/add`:
   - A foreign contributor gets 403. The body does not contain the holder's actor id.
   - The current step is `waiting` or unassigned: 409, with `detail.execution_id`.
   - There is no open execution: 409.
   - The assignee in `assigned` or `in_progress` is allowed.
   - An unbound operator or admin is allowed on a non-review step.
   - A `review`-type current step gets 409 for the assignee and for an operator. A foreign
     contributor still gets 403, because the binding check runs first.
   - The `divergence:` cursor or an empty cursor: operator allowed, contributor 409.
   - An unresolvable step definition: 409 for every role.
   - Dev mode with a nil actor: the binding is skipped, the freeze still applies.
   - *Mutation checks:* removing the freeze (m1) or the binding (m2) turns the corresponding rows
     and the §8 regression tests red.
   - *Design-check addition (2026-10-06), I6 amended:*
     - **Internal-step freeze.** A current step of `type: internal` (task-default `publish`) gets
       409 for an operator and for an admin, with `detail.step_type: internal`. A contributor gets
       409 too, because the engine-owned execution has no actor.
     - **Convergence not frozen.** A `type: convergence` current step is not frozen; the binding
       still applies.
     - *Mutation check:* dropping `StepTypeInternal` from the freeze fails the operator row.
9. **Workflow runs carry no artifacts.** An artifact `write_context` naming a run whose
   `TaskPath` is under `workflows/`, or `/artifacts/add` on such a run, returns 400, and the
   branch is unchanged.
10. **The audit PoCs are committed regression tests that fail on `61994fe`.**
    - `service/internal/gateway/security_f7_p36_regression_test.go` contains the seven F7/P3-6
      tests and the two guards from §8.
    - The Implementation Notes record a `go test -overlay` run of that file against `61994fe`:
      the seven tests fail and the two guards pass.
    - The design-phase draft was run this way on 2026-10-06 with exactly that result. On the
      prototype fix, all nine passed.
11. **Existing tests change only as listed in §8.**
    - Fixture or contract edits only; no assertion is removed or weakened.
    - `TestWorkflowCreate_RoleDispatchTable` and `TestResolveWriteContext_PlanningRunNoTaskPath`
      are unmodified and green.
    - The PR diff for those nine tests is reviewable as fixture-only, with the two contract
      rewrites called out.
12. **Wiring.**
    - `buildWorkflowService` installs the same policy as `buildArtifactService`.
    - A test in `service/cmd/spine` asserts that `buildServerConfig` with a store yields:
      - a non-nil `Config.WFPlanningStarter`;
      - a `Workflows` service that refuses a reviewer non-bypass write to `main` under
        bootstrap rules.
    - `pool.go`'s per-workspace `workflow.Service` gets a policy (projection-backed, or permissive
      without a store), asserted in `service/internal/workspace`.
    - `TestServerStartup_NoUnwiredServices` stays green.
13. **Scenario.** `TestWorkflowLifecycle_UpdateExisting` (`-tags scenario`) passes:
    - a seeded `task-spike` updated through `StartWorkflowUpdatePlanningRun`;
    - `submitted` → `approved`;
    - the run is completed and `main:workflows/task-spike.yaml` carries the bumped version.
14. **Documentation.** §7 is done:
    - `api-operations.md` §3.1 and §3.2;
    - `api/spec.yaml` (planning 201 schema, 409/503 on both workflow writes, `WriteContext`
      text, `/artifacts/add` 403/409);
    - `access-surface.md:167`;
    - `security-model.md:307`/`:312`;
    - the ADR-008 amendment note;
    - the `CHANGELOG.md` Security entry, with the stale `:83-88` bullet corrected.
15. **No regression.**
    - Spine passes `go build ./...`, `go vet ./...`, `go test ./...`, `make lint`,
      `make lint-boundaries` and the CI integration and scenario suites.
    - No migration, environment variable or event type is added.
    - SMP builds and passes `go test ./internal/customer/spinecore/... ./internal/customer/spineembedded/...`
      against the task branch, through `replace ../spine`. The result is recorded in the
      Implementation Notes.
16. **Audit PoC parity**, run via `-overlay` against the merge commit:
    - The original `poc/verify-r1-12` reports `injection did not reach main`. Its attack `PUT` is
      refused with 409/403.
    - The original `poc/standalone-service` `TestPOC_ContributorWritesIntoForeignRunBranch` logs
      a 4xx for mallory's write.
    - The F7 PoCs (`verify-r1-5`, `poc_reviewer_workflow_update`) no longer compile, because their
      planner lacks the widened method (Decision 6). AC10's inverted copies stand in for them.
    - Recorded in the Implementation Notes.
    - *Design-check addition (2026-10-06):* the `_work/task014-check` probes, run the same way
      against the merge commit, must show:
      - `ArtifactPathDotSegments`: every dot-segment write returns 400, and the run branch has no
        new file under `workflows/`;
      - `RunStartPlanning`: 400, with no path forwarded to the starter;
      - `CreateExistingWorkflow`: no run record is left behind.
17. **(Design check) Workflow paths have one producer (I9, V3, V4).**
    - **`TestIsWorkflowTarget_Canonical`.**
      - True for `workflows/x.yaml`, `/workflows/x.yaml`, `./workflows/x.yaml`,
        `governance/../workflows/x.yaml`, `workflows//x.yaml` and `workflows`.
      - False for `workflows-guide.md`, `docs/workflows/x.md` and `governance/charter.md`.
    - **`TestArtifactWrite_DotSegmentWorkflowPath_Refused`.** The run's own assignee sends:
      - `POST /artifacts` with `./workflows/x.yaml` or `governance/../workflows/x.yaml`;
      - `PUT /artifacts/./workflows/task-default.yaml`;

      all with `write_context`. Each returns 400 `use_operation: workflow.*`, and the run branch's
      `workflows/` tree is byte-identical before and after.
    - **The gateway guard stands on its own.** A second variant wires a recording fake
      `ArtifactService`. For every row the fake sees **zero** `Create`/`Update` calls. So the
      refusal does not depend on TASK-004's service-level `Clean`.
    - **`TestRunStart_Planning_WorkflowTaskPath_Refused`.** `POST /runs` with `{mode: planning}`
      and `task_path` of `workflows/x.yaml` or `./workflows/x.yaml` returns 400, and the planning
      starter is not called. A `.md` task path still returns 201.
    - **`TestStartPlanningRun_RefusesWorkflowPath`** (engine). It returns `ErrInvalidParams` with
      no `CreateRun`, no branch and no writer call.
    - *Mutation checks:*
      - reverting `isWorkflowTarget` to the raw prefix test fails the dot-segment rows;
      - removing the `handleRunStart` guard fails the run-start test.
    - **SMP.** `go test ./internal/customer/spineembedded/...` (CreateEntry) passes against the
      task branch, through `replace ../spine` (AC 15).
18. **(Design check) The documented residuals are recorded.**
    - Out of scope and the Implementation Notes name three residuals, each with its owner:
      - the claim-takeover residual (§4 note, item 2) → TASK-007;
      - the time-of-check gap (§4 note, item 3) → TASK-005;
      - the idle-run 409 hold (§3.3 note) → operator recovery.
    - `CHANGELOG.md` "Security (INIT-023 TASK-014)" states that P3-6 is closed for unheld writes,
      and that step claiming is governed by TASK-007.
19. **(Design check) F7 end to end: a second person approves.**
    - **Applies once TASK-007 has merged** (§10 note). A scenario test
      `TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview` runs under `spine serve`'s
      default step-entry policy:
      - the reviewer who claimed and submitted `draft` on an update run gets 403 when claiming
        `review`;
      - a different reviewer can claim and approve;
      - `main:workflows/task-spike.yaml` then carries the bumped version.
    - **If TASK-014 merges first,** its Implementation Notes record "F7 second-reviewer clause
      open pending TASK-007 §7". The TASK-007 PR adds this scenario, and the INIT-023 closing
      checklist does not mark F7 closed until it is green.

## Out of scope / follow-ups

- **ADR-009 override on workflow endpoints** (event and trailer through `workflow.Service`), and
  unifying it with the ADR-008 bypass. This is the existing scope decision (`CHANGELOG.md:86-88`).
  The 400 stays.
- **Orphan `pending` run after an `onBranch` failure.**
  - `startRunCommon` calls `CreateRun` (`core/engine/run.go:61`) before `onBranch` (`:67-71`), so
    a writer failure leaves a pending run with a deleted branch.
  - This already exists for `StartPlanningRun` and `StartWorkflowPlanningRun`. This task's update
    pre-checks avoid it for the predictable failures.
  - A general fix (fail the run record) is a separate engine task.
- **Push-path parity** (who may push a run branch, and the review freeze on pushes). That is
  TASK-016; see owner question 5.
- **SMP's API writes onto planning branches** (`AddToRun`, `WriteContent`). Flagged in §9 for
  SMP TASK-007 or a new SMP task.
- **Auto-claiming the entry step on planning-run start.** Owner question 4; TASK-007 territory.
- **The standalone shared working tree under bypass writes.** TASK-012.

> **Design-check note (2026-10-06).**
>
> - **The orphan bullet above is narrowed.** The two workflow starters now pre-check and mark
>   their orphans `failed` (§2 note). `StartPlanningRun` and `StartRun` keep the general gap,
>   which blocks nothing.
> - **Claim takeover of a waiting step (P3-6 residual).** Owner question 7; owned by TASK-007.
> - **Time-of-check gap between `authorizeRunBranchWrite` and the commit.** Backstop is TASK-005.
> - **A workflow-lifecycle `draft` step has no timeout,** so an idle run holds the §3.3 409 until
>   an operator acts. Changing `workflow-lifecycle.yaml` is a governed workflow edit, not code.
> - **Divergence branches.** Creating an exploratory branch writes no run-branch content
>   (`core/divergence/service.go:152-190`). Content reaches the run branch by push, which is
>   TASK-016's divergence-ref class, and by convergence, whose target is fixed by the F8 task.

## Design check

An adversarial review on 2026-10-06 against Spine `61994fe` and SMP `80995559`.

- **Read-only.** Both trees were verified clean afterwards. Every Go command used
  `-mod=readonly`.
- **Where things are.** Probes are under `_work/task014-check/`; its `README.txt` has the
  commands.
- **Where the changes are.** The designer's text is kept. Every change is one of:
  - a marked "Design-check note (2026-10-06)";
  - a new §3.5, the invariant amendments (I6, I9) or Decisions 12-15;
  - owner questions 6-7;
  - AC additions: notes on AC 3, 6, 8 and 16, and new AC 17-19.

**What held up.**

- **Line references and current-behaviour claims.** Every designer file and line reference
  checked against `61994fe` and SMP `80995559` is correct. That covers the handlers, the
  services, the engine, the wiring and the test files.
- **V1, V2 and defects 1-5** reproduce as described.
- **Prototype results reproduced.**
  - head overlay: 7 FAIL and 2 guards PASS;
  - fix overlay: 9/9 PASS;
  - full `go test ./...` on the fix: exactly the nine listed gateway tests fail;
  - mutations m1-m4: each turns the named tests red;
  - `go vet -tags integration`: clean.
- **SMP.**
  - `go build ./cmd/... ./internal/...` and the workflow tests in `spinecore`, `spineembedded` and
    `workflow` pass against the prototype.
  - SMP neither implements `engine.WorkflowWriter` nor calls `WithWorkflowWriter`, and it holds
    `*workflow.Service` as a named field, so no methods are promoted.
  - The nil-policy (I4) gate is sound for SMP's actor-less bypass writes.
- **The F7 dispatch, the V1/V2 closures and the P3-6 binding/freeze** are correct for every
  gateway sink. There are five: artifact create and update, workflow create and update, and
  `/artifacts/add`. These are the only `WithWriteContext` constructions outside the engine and
  the scenario harness.

**Bypasses found (fixed in this artifact).**

- **V3. Dot-segment paths defeat `isWorkflowTarget`.**
  - At head and on the prototype, the run's assignee wrote `./workflows/x.yaml` and replaced
    `workflows/task-default.yaml` on a task run branch through the artifact endpoints.
  - TASK-004's `Clean` would refuse these at the service. TASK-014 now canonicalises its own
    guard (§3.5.1) and pins the spellings (AC 17).
- **V4. `POST /runs` with `mode: planning` mints non-lifecycle runs on `workflows/<id>.yaml`.**
  - That is the key §3.2 and §3.3 trust. It allows a junk merge into `workflows/` and lets a
    *contributor* hold the 409 lock on any workflow.
  - Fixed at the producers: `handleRunStart` and the engine `StartPlanningRun` (§3.5.2-3, I9,
    Decision 12, AC 17).
  - SMP's only `StartPlanningRun` caller builds `.md` paths, so it is unaffected.

**A regression the design would have introduced (fixed).**

- **L1. A failed workflow start leaves a `pending` run.** Examples are a reviewer create of an
  existing id, a git error, or a policy denial on the branch.
- Recovery would then promote it to `active`, and §3.3 would 409 every later reviewer edit of
  that workflow until an operator cancelled it.
- Fixed with a create pre-check and by marking the run `failed` on writer failure (§2 note,
  Decision 13, AC 3 and AC 6).

**Gap tightened.**

- **The I6 freeze now covers `internal` steps.** That closes the operator window between
  approval and the governed merge (task-default `publish`). See Decision 14, owner question 6
  and AC 8.

**Residuals made explicit, not fixed here.**

- **The claim takeover of a waiting step.** P3-6 is narrowed to an attributed takeover. Owned by
  TASK-007 (owner question 7, Decision 15).
- **The time-of-check gap** between authorization and the commit. Backstop is TASK-005.
- **An idle lifecycle run holding the 409.** Recovery is an operator.
- **F7's "no second reviewer" clause** is closed only together with TASK-007 §7 (separation of
  duties) and TASK-013 (skills). The §10 note and AC 19 pin it.

**Other notes.**

- `buildWorkflowService` must propagate the policy-builder error (§5 note).
- The store-fallback `LookupStepDef` is not version-pinned (§4 note, item 4).

## Implementation Notes

Implemented 2026-10-08 … 2026-10-13 on branch
`init/INIT-023-TASK-014-workflow-and-write-context-governance`, cut from Spine local `main`
`d23b897` (origin `61994fe` + the INIT-023 scaffold + waves 1 and 2, including TASK-004's confined
writers, TASK-007's step-entry policy, TASK-011's write-path round-trip check and TASK-016's
`core/refadmit`). Diff base: `git diff main...HEAD`. No migration, store method, environment
variable or event type is added (I8).

### Orchestrator rulings (recorded as defaults pending the owner)

- **Owner question 1 — default: operators are not exempt from the API freeze.** Rule 3 of
  `authorizeRunBranchWrite` applies to operators and admins (matrix rows `review freeze/op-1`,
  `/admin-1`). The push path (TASK-016) exempts operators; operators recover through
  `needs_rework`, cancel, or a push.
- **Owner question 2 — default: a workflow `write_context` is limited to the workflow's own
  lifecycle run** (`resolveWorkflowWriteContext`: `Mode == planning && TaskPath ==
  workflows/<id>.yaml`).
- **Owner question 3 — default: one open lifecycle run per workflow (409), for create and update**
  (`refuseIfWorkflowRunOpen`, called by both dispatch paths).
- **Owner question 4 — default: no auto-bind of a planning run's entry step.** Unchanged; the
  first attempt stays claimable (residual below).
- **Owner question 5 — already done by TASK-016.** The push path freezes run branches during
  `review`/`internal` steps for non-operators. The API freeze uses the same step-type set,
  `refadmit.FrozenStepType`, so the two paths cannot drift; for non-operators the API freeze *is*
  refadmit's (the binding decision is `refadmit.Decide`, which applies the freeze to the bound
  actor), and rule 3 adds it for operators and the dev-mode nil actor.
- **Owner question 6 — default: the API freeze also covers `type: internal` steps** (matrix rows
  `internal-step freeze`, `detail.step_type: internal`).
- **Owner question 7 — default: the claim-takeover residual is accepted here, and the
  re-offer-to-prior-holder rule is added to TASK-007's policy** as the opt-in
  `StepEntryPolicy.ReofferToPriorHolder` (zero value off; on in `StrictStepEntryPolicy`, which
  `spine serve` installs unless `SPINE_STEP_ENTRY_POLICY=legacy`). Details under Deviations 6.

### What was built (commits)

| Commit | Content |
|---|---|
| `3ba1300` | `core/workflow`: `WithPolicy`, `WithDefaultRef`, `ValidateID`, `FilePath`; `checkBranchProtection` before `enterBranch` (I2–I4); `policy_test.go` |
| `11d0091` | `core/engine`: `WorkflowWriter.Update`, `StartWorkflowUpdatePlanningRun`, create pre-check (3b), failed transition on a branch-write failure (L1), `StartPlanningRun` refuses `workflows/` (I9); `workflow.InWorkflowsDir`; engine tests |
| `57bbbcf` | `StepEntryPolicy.ReofferToPriorHolder` (Q7): claim check 6b, candidate-listing filter, strict default; tests |
| `022c2d9` | gateway: `authorizeRunBranchWrite` (via `core/refadmit`), canonical `isWorkflowTarget`, I7 guards, `/artifacts/add` gate, `POST /runs` planning guard, update dispatch, `resolveWorkflowWriteContext`, `refuseIfWorkflowRunOpen`, widened starter + serve adapter (compile-time assertion); matrix, regression and table tests; shared fixtures |
| `91bd17b` | wiring: `buildWorkflowService`, single-mode starter + workflow writer, pooled workflow policy; tests |
| `e4a2996` | scenario step `StartWorkflowUpdatePlanningRun`; AC 13 and AC 19 scenarios |
| `c4de89c` | docs (§7) |
| `7f82e70` | codex round 1 fix (P2): a retry after a failed attempt is not re-offered |
| `fcad5bb` | codex round 2 fix (P2): a released rework attempt is a hand-off |
| `68c12ec` | lint: no new golangci-lint issues (test-only) |
| `8607765` | the pooled-policy integration test migrates its database first |

### Acceptance criteria — evidence

1. **Policy on `workflow.Service`.** `core/workflow/policy_test.go`: (a)
   `TestWorkflowService_Policy_DeniesNonBypassDirectWriteToMain` (update and create; 403, detail
   `branch`/`reasons`, `main` unchanged, no worktree left); (b)
   `TestWorkflowService_Policy_RunBranchWriteAllowed`; (c)
   `TestWorkflowService_Policy_EvalErrorFailsClosed` (500, nothing written); (d)
   `TestWorkflowService_Policy_BypassRequiresOperator` (reviewer, contributor, actor-less refused;
   operator and admin land with `Workflow-Bypass: true`). Also `…_RequestShape` (bypass on main not
   rule-evaluated; a bypass naming another branch is) and `…_DefaultRef`. Mutations: removing the
   call fails (a) and four more; removing the bypass role check (m3) fails (d).
2. **nil policy unchanged.** `TestWorkflowService_NilPolicy_KeepsPreTask014Behaviour` (actor-less
   bypass create/update and actor-less non-bypass update all commit to `main`; also after
   `WithPolicy(nil)`). Mutation m4 (nil fails closed) fails it.
3. **Update-capable planning runs.** `TestStartWorkflowUpdatePlanningRun_WritesUpdateOnRunBranch`;
   `…_RefusesBeforeAnyBranch` (missing → 404, same version → 422 with `Service.Update`'s exact
   message, `../x` → 400, unparsable prior → 500, read failure, invalid body, id mismatch — no
   branch, no run, no writer call). Every existing `TestStartWorkflowPlanningRun_*` passes with an
   unmodified body (see Deviations 4). L1:
   `TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun`,
   `TestStartWorkflowPlanningRun_WriterFailure_MarksRunFailed` (create and update; branch cleanup
   unchanged). Mutations: removing the create pre-check / the failed transition / update using
   `Create` each fail their test.
4. **F7 dispatch.** `TestWorkflowUpdate_RoleDispatchTable` (every role),
   `TestWorkflowUpdate_ReviewerNoPlanner_Returns503`,
   `TestWorkflowUpdate_ReviewerStarterNotFound_Returns404`.
5. **Rewritten tests.** `TestWorkflowUpdate_Success` → `TestWorkflowUpdate_OperatorNoWriteContext_Bypass`
   + `TestWorkflowUpdate_ReviewerNoWriteContext_StartsPlanningRun`; `TestWorkflowUpdate_NotFound`
   now uses the operator role (reviewer variant above). These are the two intentional contract
   changes.
6. **Actionable 409.** `TestWorkflow_OpenLifecycleRun_Returns409` (POST and PUT × active, pending,
   paused, committing; `detail.run_id`/`branch_name`/`status`; starter not called),
   `TestWorkflow_TerminalOrForeignRunsDoNotBlock`, `TestWorkflowUpdate_FailedRunDoesNotBlock`,
   `TestWorkflow_OpenRunCheckFailure_Returns503` (the store error text is not echoed).
7. **Workflow `write_context`.** `TestWorkflowWriteContext_Rules` (`{}`/`task_path` only → 400 for
   reviewer and operator; another workflow's run, an artifact planning run, a standard run on the
   workflow path → 400; `override` → 400; the draft assignee → 201/200 `proposed` on the run
   branch), plus `TestF7_EmptyWriteContext_RefusedOnWorkflowEndpoints` and
   `TestF7_WorkflowWriteContext_OtherWorkflowsRun_Refused` against a real repository.
8. **Binding and freeze.** `TestAuthorizeRunBranchWrite_Matrix`: 14 run states × callers × all five
   sinks (POST/PUT `/artifacts`, POST/PUT `/workflows` with `write_context`, `/artifacts/add`), 209
   subtests; dev-mode nil actor in `TestAuthorizeRunBranchWrite_DevModeNilActor` (internal: no HTTP
   request reaches a handler without a bearer); `…_NoAssigner503`,
   `…_NonEngineBranchOperatorOnly`. Mutations: m1 (no operator freeze) and m2 (no binding, 33
   foreign-caller rows) fail; dropping `StepTypeInternal` from `refadmit.FrozenStepType` fails the
   operator/admin internal rows.
9. **Workflow runs carry no artifacts.** `TestWorkflowRun_AcceptsNoArtifactWrites` (artifact
   `write_context` and `/artifacts/add`, every role, also a dot-segment task path).
10. **Regression file.** `service/internal/gateway/security_f7_p36_regression_test.go` (seven F7/P3-6
    tests, two guards). Against a `git archive` export of `61994fe` with the file copied in
    (`GOFLAGS=-mod=mod GOPROXY=off go test -count=1 -run 'TestF7_|TestP36_'
    ./service/internal/gateway/`): the seven FAIL (reviewer update 200 `authoritative`; empty
    `write_context` 200/201 committing to `main`; another workflow's run 201 `proposed`; mallory 201
    into alice's branch; mallory 200 during review; mallory's `/artifacts/add` 201) and both guards
    PASS. On the task branch all nine pass. The draft was adjusted: engine-shaped branch names
    (Deviations 1) and a seeded parent for the `/artifacts/add` test, so it fails on `61994fe`
    because mallory's add succeeds (not on a missing parent), plus an assignee control.
11. **Existing tests.** Only `handlers_workflows_test.go` and `handlers_artifact_entry_deep_test.go`
    change among existing gateway tests: the shared fixtures (`workflowPlanningStore`,
    `planningFakeStore` model a run whose draft step is held by the test actor, with an
    engine-shaped run id and a workflow projection) and the two rewrites of AC 5.
    `TestArtifactUpdate_WithPlanningWriteContext`, the four `/artifacts/add` tests,
    `TestWorkflow{Create,Update}_WithWriteContext_RoutesToBranch` and TASK-004's
    `TestF5_Gateway_*` (which postdate the design) pass through the fixtures with no body change.
    `TestWorkflowCreate_RoleDispatchTable` and `TestResolveWriteContext_PlanningRunNoTaskPath` are
    untouched and green.
12. **Wiring.** `TestBuildServerConfig_WiresWorkflowGovernance` (non-nil orchestrator-backed
    `WFPlanningStarter`; reviewer non-bypass write to `main` 403 under the bootstrap rule; reviewer
    bypass refused; operator bypass lands), `TestBuildWorkflowService_InstallsPolicy`,
    `TestBuildServiceSet_WorkflowServiceHasPolicy_NoStore` and (integration)
    `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy`; `TestServerStartup_NoUnwiredServices`
    green. Mutations (policy not installed; single-mode starter not wired; pooled permissive policy
    removed) each fail.
13. **Scenario.** `TestWorkflowLifecycle_UpdateExisting` passes (`main` untouched until approval,
    then `version: "1.1"`).
14. **Documentation.** `api-operations.md` §3.1/§3.2/§3.3, `api/spec.yaml` (WorkflowPlanningResponse,
    409/503, `write_mode`, WriteContext, `/artifacts/add` 403/409, `POST /runs`),
    `access-surface.md`, `security-model.md` §7.3, ADR-008 §1 amendment note, `CHANGELOG.md`
    section (stale "does not consult `branchprotect.Policy` yet" bullet corrected),
    `KNOWN-LIMITATIONS.md`.
15. **No regression.** Gates below. No migration, environment variable or event type added. SMP
    gate below.
16. **PoC parity** (overlays in the scratchpad mapping the audit files into this worktree;
    nothing copied into the repository):
    - `poc/verify-r1-12`: logs `injection did not reach main`; the attack PUT is refused with 409.
    - `poc/standalone-service` `TestPOC_ContributorWritesIntoForeignRunBranch`: mallory's write gets
      409.
    - Both refusals read "branch is not an engine run branch": the PoC fixtures name the branch
      `spine/run/alice-task-001`, whose suffix resolves to `run-001`, not the run asked for
      (Deviations 1). With engine-shaped names the same attacks get 403 (foreign) / 409 (frozen)
      in the committed regression tests.
    - The F7 PoCs (`verify-r1-5`, `poc_reviewer_workflow_update`) no longer compile ("does not
      implement gateway.WorkflowPlanningRunStarter (missing method
      StartWorkflowUpdatePlanningRun)", Decision 6). With a scratch shim adding only that method
      they report "PoC did not reproduce": 201 planning, `main` unchanged, `workflow_review` gate
      kept.
    - `_work/task014-check`: `ArtifactPathDotSegments` — every write 400, the branch's
      `workflows/` holds only the seeded file; `RunStartPlanning` — 400, nothing forwarded;
      `CreateExistingWorkflow` — see Deviations 4.
17. **One producer.** `TestIsWorkflowTarget_Canonical` (internal), `TestInWorkflowsDir`,
    `TestArtifactWrite_DotSegmentWorkflowPath_Refused` (real artifact service; `workflows/` tree
    hash identical), `…_GuardStandsAlone` (recording fake: zero writes),
    `TestRunStart_Planning_WorkflowTaskPath_Refused`, `TestStartPlanningRun_RefusesWorkflowPath`.
    Mutations: the raw prefix test fails the dot-segment rows; removing the `handleRunStart` guard
    fails the run-start test; removing the engine guard fails its test. SMP CreateEntry tests pass
    (SMP gate).
18. **Residuals** — see "Residuals" below; also in Out of scope, `KNOWN-LIMITATIONS.md` and the
    CHANGELOG ("P3-6 is closed for writes by actors who do not hold the step; who may claim a step
    is governed by TASK-007's step-entry policy").
19. **F7 end to end.** TASK-007 is on `main`, so the scenario is in this task:
    `TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview` passes under
    `StrictStepEntryPolicy()` (the draft author's review claim is 403; a second reviewer approves;
    `main` carries 1.1). With separation of duties switched off the refusal step fails. The clause
    stays open under `SPINE_STEP_ENTRY_POLICY=legacy`.

### Deviations and additions (with reasons)

1. **The binding is TASK-016's predicate, called on the run's branch, and must resolve back to the
   same run.** `authorizeRunBranchWrite` calls `refadmit.NewEvaluator(st).WithStepDefs(assigner)
   .Decide(refs/heads/<run.BranchName>)` as TASK-016's notes ask. An allow counts only if it is
   `bound_actor` on a `run_branch` that resolves to the run the request named; anything else (an
   ordinary or reserved branch name, a name whose suffix names another run) is operator-only, 409
   — fail closed for legacy or inconsistent rows. Engine-made names always carry the run's suffix,
   so production is unaffected; test fixtures were made engine-shaped.
2. **Refusal detail comes from the execution list, the decision from refadmit.** refadmit returns
   one `not_bound` reason; `bindingRefusal` reads the latest run-level execution only to choose
   the wire answer (409 waiting/unassigned with `detail.execution_id`, 409 no open execution, 403
   otherwise; the holder is never named). A store error during authorization is 500 (as the
   TASK-016 divergence handler); `ListRunsByTask` failure is 503 as designed.
3. **`InWorkflowsDir` folds case** (Unicode `EqualFold` on the first component) in addition to
   `path.Clean`, because a case-insensitive server filesystem aliases `Workflows/` onto
   `workflows/` (the TASK-016 DC-1 reasoning). It only refuses more.
4. **Engine test helper and the L1 probe.** `stubGitOperator.ReadFile` answers `(nil, nil)` — "the
   file exists" — for every path, so the create pre-check (3b) would refuse every existing
   `TestStartWorkflowPlanningRun_*` while AC 3 requires them unmodified. The shared helper
   `workflowPlanningOrchestrator` now wraps the double (`absentWorkflowReads`: an empty read under
   `workflows/` is git's not-found); test bodies are unchanged. Consequence: the probe
   `TestT014Check_CreateExistingWorkflow_LeavesPendingRun` models "exists" only through the
   writer's error, so it now exercises the writer-failure path and logs the run left `failed`
   (terminal, which neither the 409 nor scheduler recovery counts), not `pending`; it no longer
   fails the way AC 3 anticipated. The pre-check itself is pinned by
   `TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun` with a git double that serves the
   file (no `CreateRun`, no branch).
5. **`POST /runs` guard is planning-only, as designed.** Standard `StartRun` is unchanged: the
   gateway's I7 guard refuses artifact writes on such a run, and the 409 / workflow `write_context`
   match only `Mode == planning`.
6. **Q7 rule shape.** Author step types only (`isAuthorStepType`, the types push admission lets the
   bound actor write). Prior holder: the actor still on the row (restart recovery), else the actor
   of the *immediately preceding* attempt of the same step and branch if it completed (rework) —
   codex round 1 — unless the execution itself was claimed and released since — codex round 2;
   detected through ledger rows with deterministic ids (`claim-<exec>-<actor>`,
   `auto-<exec>-<actor>`, `<exec>`) via the existing `GetAssignment` (no new store method). Retries
   after a failed attempt and released steps stay claimable by others; `AssignStep` (operator) is
   never restricted; the candidate listing hides refusals; ledger or list failures fail the claim
   closed (503). Residual: a claim row left by an actor other than the candidate holder (possible
   only before the policy was on) is not probed. `ReleaseStep` is unchanged.
7. **Scenario body.** The AC 13/19 update uses a fresh validator-clean `task-spike` body: the
   harness-seeded one fails `workflow.Validate` (status `Draft`, no required skill), which every
   write path enforces.
8. **Pooled-mode starter type assertion.** `ServiceSet.WFPlanningStarter` is untyped, so a missing
   adapter method would have compiled and answered 503 for every pooled reviewer workflow write; a
   compile-time assertion now pins the adapter to the widened interface.
9. **Commit attribution.** Seven commits (`022c2d9`, `91bd17b`, `e4a2996`, `c4de89c`, `7f82e70`,
   `fcad5bb`, `68c12ec`) end with only the `Co-Authored-By` line: a session-level attribution
   instruction in force at that time omitted the `Claude-Session` line. The others carry both
   lines. History was not rewritten (no rebase allowed).
10. **Environment.** The shared PostgreSQL at `localhost:55630` was not running at gate time, so
    every database gate ran on a private PostgreSQL 17.10 cluster in this task's scratchpad
    (`127.0.0.1:55714`, scram, TCP only; no pgvector, which neither Spine nor SMP's platform and
    Spine-runtime sets need), stopped and deleted afterwards; the shared server and other tasks'
    instances were not touched. The scratchpad golangci-lint binary named in the task was absent;
    `/opt/homebrew/bin/golangci-lint` 2.12.2 (the repo's v2 config) was used. The codex config's
    model is refused by the account; reviews ran with `-c model="gpt-6-astra"`.

### Residuals (AC 18), with owners

- **Claim takeover of a waiting step** (§4 note, item 2) → TASK-007's step-entry policy. Closed
  under the strict policy for reworked and restart-recovered author steps (`ReofferToPriorHolder`);
  still open for a never-held first attempt (owner question 4: no auto-bind), for a step its holder
  released (a deliberate hand-off), under `legacy`, and in the embedded zero value. Attributed in
  every case.
- **Time-of-check gap** (§4 note, item 3) → TASK-005's reviewed-SHA binding.
- **Idle-run 409 hold** (§3.3 note) → operator recovery (cancel or bypass).
- **F7 second-reviewer clause** → closed under `spine serve`'s strict policy (AC 19); open under
  `legacy`.
- **SMP's own API writes onto planning branches** (`AddToRun`, `WriteContent`) → SMP TASK-007 or a
  new SMP task (§9).

### Gates (commands and results, final code `8607765`)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | ok |
| `go build -o …/embed-smoketest ./cmd/embed-smoketest && …/embed-smoketest` | "library surface is sound" |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 ./...` | exit 0 (40 packages ok) |
| `go test -race -p 4 -count=1 ./core/workflow/ ./core/engine/ ./service/internal/gateway/ ./service/cmd/spine/ ./service/internal/workspace/` | all ok, no race |
| `SPINE_TEST_DATABASE_URL=…/s014_integ go test -tags integration -count=1 -p 1` on the same five packages | all ok (after `8607765`) |
| `go test -tags scenario -count=1 -p 1 -timeout 30m ./service/scenariotest/...` on HEAD (`s014_scen`) and on a `main` export (`s014_scenmain`) | 30 failures each, the identical set (the known pre-existing ones); none new |
| CI scenario-git-http job: `-run '^TestGitHTTP_' -v` and `-run '^TestBootstrapInternalAdmin_' -v` | 20 pass (8 `TestGitHTTP_Push*`), 0 fail, 0 skip; both bootstrap scenarios pass, 0 skip |
| `go test -tags scenario -run '^TestWorkflowLifecycle_' -v` | 4/4 pass |
| `golangci-lint run --build-tags=integration,scenario` on the touched packages, HEAD vs a `main` export, normalised without positions | **0 new issues** (5 fewer: import-order fixes in touched files) |
| `golangci-lint run --enable-only=gosec ./...` | HEAD 0 issues, `main` 0 issues |
| `gofmt -l` on every changed `.go` file | clean |
| regression file against `61994fe` (AC 10) | 7 FAIL, 2 guards PASS |
| `codex review --base main` (plain) | round 1: one P2 (fixed `7f82e70`); round 2: one P2 (fixed `fcad5bb`); round 3: "No actionable regressions"; round 4 on the final code (`8607765`): "No actionable regressions were identified" (codex's own run of the service tests was blocked by its sandbox's port restrictions; they pass here, rows above) |

### SMP compatibility gate

SMP checkout `f5c6fd6d` (detached, no `vendor/`, no `tmp/`, `../spine` → this worktree), never
modified (tree clean after every command):

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`: clean.
- SMP packages importing a changed Spine package (`core/workflow`, `core/engine`; `go list` with test
  imports, cross-checked by grep): `internal/customer/executionreadiness`, `spinecore`,
  `spineembedded`; plus `internal/customer/workflow` (named by the design check). `go test -p 4
  -count=1` on all four: ok.
- Platform migrations: `go run ./dev/apply-migrations -set platform -dir migrations` → 115 versions;
  Spine runtime: `go run ./dev/apply-migrations -set spine-runtime` → 26 versions.
- CI integration job's Spine-runtime step: `go run ./dev/testgate -- -race -count=1 -timeout 10m
  ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/
  ./internal/customer/executionreadiness/` with `TEST_SPINE_DB`, `TEST_PLATFORM_DB`,
  `TEST_PROVISIONING_DSN` (roles `platform`, `platform_audit_retention`, `smp_prov` provisioned as
  in CI): **1835 tests passed, 0 failed**. testgate exits 1 only for two skips that are
  macOS-specific (`TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch`:
  "ru_maxrss in KiB is the Linux contract"); they run on CI's Linux runner and touch nothing this
  task changes.
- API impact for SMP: additive only (`workflow.Service.WithPolicy`/`WithDefaultRef`,
  `workflow.ValidateID`/`FilePath`/`InWorkflowsDir`, `Orchestrator.StartWorkflowUpdatePlanningRun`,
  `engine.WorkflowWriter.Update`, `StepEntryPolicy.ReofferToPriorHolder`). SMP neither implements
  `engine.WorkflowWriter` nor calls `WithWorkflowWriter` or the step-entry policy; its
  `StartPlanningRun` caller builds `.md` paths, so the new refusal is never reached; its workflow
  service has no policy, so its actor-less bypass writes are byte-for-byte unchanged (I4).

### Not run (with the exact command)

- `make lint` over the whole repository: `golangci-lint run` was run on the touched packages
  against `main` (0 new) and gosec-only over `./...`; the full run carries pre-existing issues on
  `main`. *(Run in review round 1: 0 new issues against `main`; see below.)*
- `make test-scenario` with default package parallelism: run as `go test -tags scenario -count=1
  -p 1 ./service/scenariotest/...` (packages share one database).
- SMP's other gated suites (the platform-DB suites, faultinject, githttp, dockerexec): not named by
  this task's gate; they need MinIO/Docker images this host does not provide.
- SMP TASK-012 still owes the end-to-end check that the admin `PUT /workflows/{id}` returns 200
  `write_mode: bypass` with the trailer on origin (§9).

### Review round 1 (2026-10-14)

Three reviewers (correctness and compatibility, adversarial security, completeness and tests)
reviewed `main...47a1703`. Each finding was checked against the code before it was acted on. The
fixes are commits `8066fed` (gateway), `1deae3f` (engine re-offer), `3a5f033` (real-git
starter test), `1252756` (integration test and CI) and `5f165b1` (docs). Findings 1, 4 and 6
have one root cause, and so do 3 and 8.

**Findings.**

1. **P2 (finding 6), with P3 findings 1 and 4. The holder of a step routed to a code repository
   got a 403 saying they were not the assignee. FIXED.**
   - *Verified.* A new matrix row on the pre-fix code (an `in_progress` holder whose execution has
     `RepositoryID: payments`) got `403 "accepts writes only from the actor assigned to its
     current step"` on all five sinks. A `waiting` code-repository step answered "claim it
     first". A claim would not have made the write possible.
   - *Cause.* `authorizeBoundWriter` asks refadmit about the primary repository, because every
     API sink writes there. refadmit binds a holder only for the repository its execution
     targets (`core/refadmit/evaluator.go`, rule 7). `bindingRefusal` ignored the repository.
   - *Rule chosen: keep the same-repository binding.* The alternative was to drop the
     repository term for API writes. It was rejected for three reasons:
     - ADR-015 has a step write exactly one repository (its "one repository per step"
       decision, which rejects multi-repository steps).
     - TASK-016 refuses the same holder a push of the primary run branch. Admitting the API
       write would make the push path and the API path two rules again, against the design's
       "one predicate governs both" (§4).
     - The design's own call shape (§4: `Request{Ref, Actor}`, no repository) already means the
       primary repository.

     Before this task, *any* contributor could write that branch (P3-6). So no legitimate
     contract is lost: a code step that must also update the task artifact does so in a step
     routed to the primary repository.
   - *Change.* `bindingRefusal` returns `409` with `detail.repository_id` to the holder of an
     open code-repository step, and to anyone while that step is unclaimed. A caller who does not
     hold a claimed step still gets the 403, so the binding stays the first answer, as for the
     freeze. Operators are unaffected.
   - *Docs.* spec `WriteContext` and the `artifact.add` 409, `api-operations.md` §3.1, CHANGELOG
     "Changed", `KNOWN-LIMITATIONS.md`.
   - *Tests.* Four matrix row groups across the five sinks: held, assigned, waiting, and
     `repository: spine` by name (allowed).
   - Recorded as Deviation 11 below.
2. **P3. The re-offer stranded attempts. FIXED.** The problem was a prior holder who no longer
   resolves or is not `active`, and machine-pool steps.
   - *Verified.* Two cases stranded the attempt until an operator assigned it, and
     `ListStepExecutions` hid it from everyone else:
     - `priorHolder` re-offered to a suspended, deactivated or deleted actor, whom
       `EnforceEligibility` refuses;
     - it pinned `automated_only` / `ai_only` reworks to one pool identity.

     For machine modes the pin was also incoherent. `ActivateStep`'s auto-selection binds such a
     rework to *any* eligible actor of the type (`resolveAutoActorID`, `StrategyAnyEligible`), so
     the claim path, and with it the re-offer, applies only when no eligible actor was found.
   - *Change.*
     - `priorHolderCanClaim`: an inactive or unknown holder means no re-offer. A lookup failure
       fails the claim closed (503) with a message that does not name the holder. With no actor
       lookup wired, the re-offer stands.
     - `StepEntryPolicy.reofferExempt`: with `EnforceExecutionMode` on, `automated_only` and
       `ai_only` steps are not re-offered. Their claimers are machine actors, which only operators
       create (`actor.create`), so no contributor can claim them. Without `EnforceExecutionMode`
       the mode restricts nobody, so the re-offer stays.
   - *Tests.*
     - `TestStepEntry_ReofferToPriorHolder` gains suspended, deactivated, unknown, an inactive
       holder on a restart-recovered row, a lookup failure (no holder named) and no lookup wired.
       Its fixture now registers its actors as active.
     - New `TestStepEntry_ReofferExemptsMachinePoolSteps`.
   - *Mutations.* Dropping the holder check fails 4 rows. Dropping the exemption fails 2.
     Dropping its `EnforceExecutionMode` guard fails 1.
   - *Not changed.* A `hybrid` / `human_only` rework whose holder is still `active` but idle waits
     for that holder or an operator assign. Humans can claim those steps, so the re-offer is what
     closes the takeover. This is documented in `KNOWN-LIMITATIONS.md`.
3. **P3 (finding 3), with P3 finding 8. Runs left by a failed start before the upgrade lock their
   workflow. FIXED (docs and message).**
   - *Verified.* `refuseIfWorkflowRunOpen` counts any non-terminal planning run. Its message said
     "wait for it to finish", but such a run never finishes. `recoverPendingRuns` does not check
     the branch (`adapters/scheduler/recovery.go`).
   - *Change.*
     - The 409 now ends "…or, if it is stale, ask an operator to cancel it (`POST
       /runs/<id>/cancel`)". `TestWorkflow_OpenLifecycleRun_Returns409` asserts this.
     - A CHANGELOG upgrade note gives a `runtime.runs` query (non-terminal planning runs on
       `workflows/%`), a `git rev-parse --verify --quiet refs/heads/<branch_name>` check, and
       `spine run cancel` / `POST /runs/{id}/cancel`. `GET /query/runs?task_path=…` is the
       alternative without database access.
     - `KNOWN-LIMITATIONS.md` has the residual.
   - *Not done: skipping or annotating a branchless run in the 409 path.* That would need a ref
     read on every refusal. The gateway's `GitReader` has no ref-existence call, and a git error
     there is a new failure mode. Skipping would also let a second run open while the orphan stays
     non-terminal. A one-time operator clean-up is enough, and new failures already leave the run
     `failed`.
4. *(see 1)*
5. **P3. KNOWN-LIMITATIONS did not say I7/V2 hold only on the API path. FIXED (docs).**
   - *Verified.* refadmit admits the bound actor by binding alone, and
     `service/internal/githttp/prereceive.go` has no path checks. TASK-006 is `Pending`.
   - *Change.* A residual bullet in `KNOWN-LIMITATIONS.md` and a CHANGELOG Security bullet ("Not
     closed here: workflow edits by push"). Owner: TASK-006.
6. *(see 1)*
7. **P3. The AC 12 pooled-policy integration test could pass by skipping, and no CI job ran it.
   FIXED.**
   - *Verified.* After `DELETE FROM projection.branch_protection_rules`, the pre-fix file
     (`-overlay`) reported `SKIP … nothing to deny`.
   - *How the table can be empty.* `adapters/store`'s `TestBranchProtectionRulesProjection` ends
     on an empty ruleset before its cleanup reseeds, and the rule source has no bootstrap
     fallback.
   - *Change.* The test installs the bootstrap row through `UpsertBranchProtectionRules`,
     restores it in `t.Cleanup`, and turns a missing rule into `t.Fatalf`. On the emptied table
     the fixed test passes.
   - *CI.* The `scenario-git-http` job (Postgres) gains two must-pass, never-skip steps:
     - `TestWorkflowLifecycle_*` (AC 13 and AC 19 scenarios);
     - this integration test (AC 12).

     Both were run verbatim against a private database. The rest of the scenario suite stays
     out of CI, because it is red on `main` (the job's own comment).
8. *(see 3)*
9. **P3. The spec did not declare the new 403/409 on artifact create/update. FIXED.**
   `artifact.create` gains `403`, and its `409` now covers `conflict` as well as
   `already_exists`. `artifact.update` gains `403` and `409`. Each points to the `WriteContext`
   rule. The spec was parsed with `gopkg.in/yaml.v3` after the edit.

**Acceptance-criteria gaps.**

- **AC 16 / AC 3 probe (`CreateExistingWorkflow`). FIXED with new evidence.** The probe models
  "the id exists" only through the writer's error, so on this branch it reaches the
  writer-failure path and leaves a `failed` run (Deviation 4).
  - The new `TestWorkflowStarters_RealGit_PreChecks` (`core/engine`) uses a real `CLIClient` and a
    real `workflow.Service`:
    - a create of an existing id leaves **no run record, no ref and no commit**;
    - a create of a new id proceeds (real git's not-found counts as "absent");
    - an update of a missing id returns 404 with no run.
  - Mutation: removing the create pre-check fails the first row (a `failed` run left behind).
- **AC 3 "every existing `TestStartWorkflowPlanningRun_*` passes unmodified". NOT A DEFECT.**
  - The bodies are unmodified. The shared helper's wrapper (`absentWorkflowReads`) is needed
    because `stubGitOperator.ReadFile` answers "exists, empty" for every path, so the create
    pre-check would refuse every one of those tests.
  - This was disclosed as Deviation 4. The real-git test above now covers the pre-check without
    any double.
- **AC 8 "unresolvable step definition: 409 for every role". NOT A DEFECT; now pinned.**
  - Rule 2 (binding) runs before rule 3 (freeze, definition lookup), exactly as AC 8's own review
    row has a foreign contributor get 403. "Every role" is read as every role the binding admits:
    holder, operator, admin.
  - The matrix row now also asserts `403` for a foreign contributor.
- **AC 8 "the assignee in `assigned`/`in_progress` is allowed". FIXED** as finding 1. The
  assignee of a primary-repository step is allowed. The assignee of a code-repository step gets
  the 409, by the same rule as pushes. Both are now in the matrix.
- **AC 15 "CI integration and scenario suites". FIXED as far as it applies.** Spine CI has no
  integration job. The task's database evidence now runs in the Postgres job (finding 7).
- **AC 15 `make lint`. FIXED (run).**
  - `./dev/lint-boundaries.sh && golangci-lint run` (the scratchpad v2 binary) over the whole
    repository: HEAD 270 issues, `main` 275. The position-normalised diff shows **0 new**, and 5
    fewer: gofmt fixes in touched files.
- **AC 12 / 13 / 15 / 19 database gates. RE-RUN.** The shared server at `localhost:55630` was
  still down, and the named `psql` path was absent. So every database gate below ran on a private
  PostgreSQL 17.10 cluster in this task's scratchpad (`127.0.0.1:55742`, scram, TCP only, its own
  roles), using only `s014_*` databases plus SMP's own allocations. The cluster was stopped and
  deleted afterwards.
- **Re-verified, no gap.** The SMP gate and the AC 10 regression file need no change. The file is
  unchanged this round, and the gateway package passes.

**Deviations added in round 1.**

11. **Run-branch API writes follow the execution's repository (finding 1).** The design's I5 /
    AC 8 wording ("the actor bound to the run's current step") has no repository qualifier. The
    implemented binding is the design's own refadmit call (primary repository), so the holder of
    a step routed to a code repository is not the primary run branch's API writer. They get a
    distinct `409` (`detail.repository_id`), as for a push. Reasons are under finding 1.
12. **The re-offer is narrower (finding 2).** There is no re-offer to a holder who cannot claim
    (inactive or unknown), and none on `automated_only` / `ai_only` steps while
    `EnforceExecutionMode` is on. *(Amended in review round 2: "cannot claim" now covers every
    claimer-dependent refusal in `ClaimStep` — also the execution's `eligible_actor_ids`,
    `eligible_actor_types`, the mode-implied type and `required_skills` — and the host's
    authorization is recorded as the one residual. See "Review round 2", finding 1.)*

**Residuals added** (they extend AC 18's list; all are in `KNOWN-LIMITATIONS.md`):

- workflow edits by push are not confined to the lifecycle run → TASK-006;
- runs left by failed starts before the upgrade → one-time operator clean-up (CHANGELOG
  upgrade note);
- an idle but `active` prior holder of a human-claimable rework → that holder or an operator
  assign.

**Gates (round 1, final code `5f165b1`).**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o …/embed-smoketest ./cmd/embed-smoketest && …/embed-smoketest` | OK; ok; "library surface is sound" |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -p 4 ./...` | exit 0 (40 packages ok) |
| `go test -race -p 4 -count=1` on `core/workflow`, `core/engine`, `service/internal/gateway`, `service/cmd/spine`, `service/internal/workspace` | all ok |
| `go test -tags integration -count=1 -p 1` on the same five (`s014_integ`) | all ok; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS, also right after `adapters/store`'s projection test and on an emptied rule table |
| `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` on HEAD (`s014_scen`) and on a `main` export (`s014_scenmain`) | 30 failures each, the identical set (known pre-existing); none new |
| the two new CI steps, run verbatim | `TestWorkflowLifecycle_*` 4/4 PASS (incl. `_UpdateExisting`, `_UpdateExisting_DraftAuthorCannotReview`), no skip; the integration step PASS, no skip |
| `golangci-lint run` (whole repository, `make lint`'s second half) HEAD vs `main` export, position-normalised | **0 new** (270 vs 275) |
| `golangci-lint run --enable-only=gosec ./...` | 0 issues |
| `gofmt -l` on every changed `.go` file | clean |
| SMP gate (`f5c6fd6d`, `../spine` → this worktree, tree clean after): `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` | clean |
| SMP `go test -p 4 -count=1` on the direct importers of `core/engine` / `core/workflow` (`executionreadiness`, `spinecore`, `spineembedded`) and `internal/customer/workflow` | all ok; SMP uses no `StepEntryPolicy` (grep), so the re-offer change cannot reach it |
| SMP `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` | 115 / 26 versions |
| SMP CI Spine-runtime step: `go run ./dev/testgate -- -race -count=1 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` | **1835 passed, 0 failed**; the same two macOS-only `ru_maxrss` skips as before (Linux-only contract, run on CI) |
| `codex review --base main` (plain; `-c model="gpt-6-astra"`, the configured model is refused by the account) | "No actionable regressions were identified" (codex's own service-suite runs were blocked by its sandbox's socket restrictions; they pass here) |

### Review round 2 (2026-10-14)

A second-round reviewer examined `main...fcc7928` after the round-1 fixes. It reported one
finding and one evidence gap. Both were checked against the code before anything changed. The
fixes are commits `6b81ca9` (engine), `0c71b9d` (CHANGELOG and KNOWN-LIMITATIONS) and `74247ba`
(comment wording).

**Findings.**

1. **P3. The round-1 re-offer fix covered only inactive or unknown holders. FIXED.**
   - *Verified.* `priorHolderCanClaim` checked only `a.Status == active`. `ClaimStep` also
     refuses a claimer for other reasons. Those that depend on who claims are: the execution's
     `eligible_actor_ids`, `eligible_actor_types`, the mode-implied type (under
     `EnforceExecutionMode`) and `required_skills`. The reviewer's probe was ported as the first
     row of the new `TestStepEntry_ReofferSkipsHolderTheClaimWouldRefuse`
     (`StrictStepEntryPolicy()`, a `hybrid` `execute` that requires `go`, member-1 lost `go`). On
     `fcc7928` it failed as described: member-1's own claim got `409 actor missing required
     skills: [go]`, and member-2 got the re-offer `403`. Five more rows failed the same way: a
     deprecated skill, the holder off the allow-list, a holder type the step does not admit, a
     non-human holder of a `human_only` step, and a failing skill lookup that answered `403`
     instead of failing closed. The listing row failed too: the rework was hidden from member-2.
   - *Change.*
     - `priorHolderCanClaim(ctx, stepDef, exec, holder)` now mirrors `ClaimStep`'s
       claimer-dependent checks, in its order. If any of them would refuse the holder, there is
       no re-offer:
       1. `eligible_actor_ids`. This check is pure, so it also applies with no actor lookup
          wired.
       2. The holder resolves and is `active` (round 1).
       3. With `EnforceExecutionMode`, the mode-implied type. Only `human_only` is left here,
          because `reofferExempt` already covers the machine modes.
       4. `eligible_actor_types`.
       5. `required_skills`, read through the same skill lookup as the claim. A skill lookup
          failure is `503` with a message that does not name the holder. With no skill lookup
          wired, `ClaimStep` skips the skill check, so the holder passes and the re-offer stands.
     - `ClaimStep`, `RequireSkillsOnAssign` and the holder check now share one active-skill rule,
       `missingRequiredSkills`. The extraction does not change behaviour: same loop, same order,
       same message.
   - *Who can trigger the exception.* Each of these refusals follows an operator or admin
     action, never a contributor's, so a contributor cannot lift a colleague's re-offer:
     - `actor.remove_skill` and `skill.deprecate` are admin-only, and so is suspension;
     - `UpdateActor` never writes an actor's type;
     - only `step.assign` (operator) writes `eligible_actor_ids`.
   - *Deliberately not mirrored* (stated in the function comment):
     - *Checks that refuse every claimer alike* (run status, engine-owned steps,
       preconditions). The re-offer adds no strand there.
     - *Separation of duties.* Under that rule the holder of an author step can never be bound to
       a review of the run afterwards. A live review it held before (rows from before the policy)
       ends with that review, so the refusal is transient, not a strand.
     - *The host's authorization.* `spine serve`'s `execution.claim` needs contributor, but the
       engine cannot see that check. Judging it from `Actor.Role` would also silently lift every
       re-offer for an embedder whose resolver leaves `Role` empty, which is the unsafe
       direction. So a holder whose role an admin lowers below contributor keeps the rework
       pinned until the role is restored or an operator assigns it. This is recorded in
       `KNOWN-LIMITATIONS.md`.
   - *Tests.*
     - `TestStepEntry_ReofferSkipsHolderTheClaimWouldRefuse` has ten rows: seven where the
       re-offer is dropped and three controls where it stands. Each row whose holder cannot claim
       also asserts the holder's own claim code.
     - `TestStepEntry_CandidateListingOffersReworkWhenHolderCannotClaim` covers the listing
       mirror.
   - *Mutations* (`-overlay`, the worktree untouched). Each of these fails at least one row:
     - dropping the allow-list, status, implied-type, `eligible_actor_types` or skill check;
     - removing the `EnforceExecutionMode` guard (fails the "mode not enforced" control);
     - turning the skill-lookup failure into "cannot claim", or a missing skill lister into
       "cannot claim";
     - counting non-`active` skills in `missingRequiredSkills`.
   - *Docs.* The `ReofferToPriorHolder` field comment, the CHANGELOG "Security" bullet and
     `KNOWN-LIMITATIONS.md` now list every exception and the role residual. Deviation 12 is
     amended in place.
   - *SMP.* No change. SMP installs no `StepEntryPolicy` (grep over the SMP checkout), so
     `priorHolderCanClaim` and `requireSkillsOnAssign` are unreachable from it. Its
     `ClaimStep`'s skill check is the same rule, now extracted.

**Gap.**

- **AC 12, AC 13/19 and AC 15 database evidence was not re-checked by the reviewer. RE-RUN on
  the final code.** The shared server at `localhost:55630` was still down (port closed), and the
  named `psql` path was absent. So, as in round 1, a private PostgreSQL 17.10 cluster ran in this
  task's scratchpad: `127.0.0.1:55781`, scram, TCP only, no Unix socket. Only `s014_*` databases
  were used (`s014_integ`, `s014_scen`, `s014_scenmain`, `s014_smp_it`,
  `s014_spine_runtime_it`), plus the SMP roles its CI provisions and SMP's own test allocations.
  The cluster was stopped and deleted afterwards. Results are in the table below:
  - `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` passes with no skip, in the
    package suite and in the CI step run verbatim;
  - `TestWorkflowLifecycle_*` passes 4/4 with no skip;
  - the scenario-suite failure set is identical to `main`'s.

**Gates (round 2, final code).**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o …/embed-smoketest ./cmd/embed-smoketest && …/embed-smoketest` | OK; ok; "library surface is sound" |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -p 4 -count=1 ./...` | exit 0 (40 packages ok) |
| `go test -race -p 4 -count=1` on `core/workflow`, `core/engine`, `service/internal/gateway`, `service/cmd/spine`, `service/internal/workspace` | all ok |
| `SPINE_TEST_DATABASE_URL=…/s014_integ go test -tags integration -count=1 -p 1` on the same five | all ok; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS (`-v`, no skip) |
| CI `scenario-git-http` job, all four steps run verbatim with `bash -eo pipefail` (logs moved to the scratchpad) on `s014_scen` | exit 0 each: `TestGitHTTP_*` 20 PASS, `TestBootstrapInternalAdmin_*` 2 PASS, `TestWorkflowLifecycle_*` 4 PASS (incl. `_UpdateExisting`, `_UpdateExisting_DraftAuthorCannotReview`), the AC 12 integration step PASS; 0 FAIL, 0 SKIP |
| `go test -tags scenario -count=1 -p 1 -timeout 40m ./service/scenariotest/...` on HEAD (`s014_scen`, recreated) and on a fresh `git archive main` export (`s014_scenmain`) | 30 failures each, the identical set (the known pre-existing ones, also identical to round 1's list); none new |
| `golangci-lint run --build-tags=integration,scenario` on the touched packages (the five above plus `service/scenariotest/...`), HEAD vs the `main` export, normalised without positions | **0 new issues** (189 vs 194; the 5 fewer are the gofmt fixes in touched files) |
| `golangci-lint run --enable-only=gosec ./...` | 0 issues |
| `gofmt -l` on every changed `.go` file (36) | clean |
| SMP gate (`f5c6fd6d`, `../spine` → this worktree, tree clean after every command): `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` | clean |
| SMP `go test -p 4 -count=1` on the importers of changed Spine packages (`go list` with test imports: `executionreadiness`, `spinecore`, `spineembedded`) and `internal/customer/workflow` | all ok |
| SMP `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` | 115 / 26 versions |
| SMP CI Spine-runtime step: `go run ./dev/testgate -- -race -count=1 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` | **1835 passed, 0 failed**; the same two macOS-only `ru_maxrss` skips as rounds 0 and 1 (Linux-only contract, run on CI's Linux runner) |
| `codex review --base main` (plain; the configured model `gpt-6.1-sol` is refused by the account — "not supported when using Codex with a ChatGPT account" — so, as in round 1, `-c model="gpt-6-astra"`) on `0c71b9d`; `74247ba` after it is comment-only | "No actionable regressions were identified" (no P1/P2; codex's own gateway and server suite runs were blocked by its sandbox's listening-port restriction; they pass here, rows above) |

### Integration (wave 3 merge) (2026-10-14)

Integration branch `init/INIT-023-wave3-integration` (worktree `i23/s-int3/spine`), cut from Spine
local `main` `d23b897` (origin `61994fe` + the INIT-023 scaffold + waves 1 and 2). Merge order, each
`git merge --no-ff`, built and tested on its touched packages before the next: TASK-005 `06ccca2`
(merge `1f7af13`, clean), TASK-003 `b5ad640` (merge `88df8bb`; conflicts `CHANGELOG.md`, the
`adapters/git/cli.go` imports), TASK-014 `9c1778a` (merge `4ed5195`; `CHANGELOG.md`), TASK-015
`2096c1f` (merge `5029321`; `CHANGELOG.md`, the `CLIClient` fields in `adapters/git/cli.go`, the
`workflow.create` / `workflow.update` descriptions in `api/spec.yaml`). Every conflict kept both
sides: each task's CHANGELOG section (newest merge on top), both import sets, both field sets, and
TASK-015's identifier rule beside TASK-014's dispatch text. Coordination commits: `70d6c8e` (the
merge-binding plumbing runs under TASK-003's read bounds) with `f5b4347` (its lint), `94017aa`
(docs: review-time writes and the push window), `b911c04` (codex round 2: a captured binding is
stored before the step completes), `70bed62` (codex round 3: rows a pre-TASK-003 projection holds
at a refused path are retired). Nothing was pushed; `main` and the task branches are unchanged.

**This task in the integration.** Merge `4ed5195` (conflict: `CHANGELOG.md`, every section kept).

- *With TASK-015.* `checkWorkflowIdentifiers` runs right after the body checks, before the
  lifecycle dispatch, `refuseIfWorkflowRunOpen` and `resolveWorkflowWriteContext`, on create and on
  update (checked on the merged handlers, as TASK-015 §10 asks); the `workflow.create` /
  `workflow.update` spec descriptions keep both texts (the `api/spec.yaml` conflict of TASK-015's
  merge). `authorizeRunBranchWrite` and `refuseIfWorkflowRunOpen` resolve the workspace's store and
  step assigner from the request (`storeFrom`, `assignerFor`), so TASK-015's shared-mode scoping
  (P3-29) needs nothing more here: this task adds no process-global gateway state.
- *With TASK-005 — docs (`94017aa`).* This task's freeze (`409 detail.step_type` for
  `/artifacts/add` and `write_context` during a `review` or `internal` step, every role) makes
  TASK-005's "a reviewer's own fix-up during a review cannot be approved" impossible through the
  API; TASK-005's CHANGELOG UX bullet, `api/spec.yaml` (`step.submit` 409) and `api-operations.md`
  now say that in `spine serve` such a commit can only be an operator's push or a push started
  before the review. No code conflict: TASK-005's take-back (`takeBackRefusedMerge` →
  `ResetBranch`) moves the local target branch, never a run branch, and while a refused merge
  retries in `committing` the current step is `publish` (`internal`), so the run branch stays
  frozen and the retry merges the bound commit regardless. The lifecycle workflow's legacy terminal
  commit merges under binding in `spine serve`; `TestWorkflowLifecycle_*` (4/4) pass with the
  harness's binding on. The time-of-check residual (KNOWN-LIMITATIONS) is now backed by TASK-005's
  merged binding, and codex round 2's fix (`b911c04`) keeps a submit's binding even when the
  `commit_meta` write after completion would have failed.
- *Re-offer vs TASK-007 / TASK-016 / TASK-005.* No change. The re-offer is this task's opt-in part
  of TASK-007's policy (`StrictStepEntryPolicy`), and pushes follow TASK-016's binding, which the
  re-offer only narrows. A publish run that TASK-005 sends back to `execute` (`merge_failed`)
  creates a rework attempt like any loop-back; it is re-offered to the holder of the immediately
  preceding completed attempt, per Deviation 6.
- *With TASK-003.* `workflowPlanningPreCheck` reads at `HEAD` through TASK-003's bounded
  `ReadFile`; an oversized existing definition fails closed (not 404/409). TASK-003's
  `workflow.Service` changes (`Read` → 413, `List` without unsafe paths) sit in other functions of
  `core/workflow/service.go`; the auto-merge kept this task's policy check in front of
  `enterBranch`.
- The two CI steps this task added to the `scenario-git-http` job pass verbatim on the merged tree.

**Gates on the integration branch** (code at `70bed62`; only these artifact notes were committed
afterwards):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 17 non-scenario packages the four tasks and the coordination commits touched | all ok |
| `-tags integration -race -p 1` (`SPINE_TEST_DATABASE_URL` = `s3i_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3i_reg`, migrated from `migrations/registry`) on the touched packages with integration tests | `adapters/delivery` 315, `adapters/projection` 180, `service/cmd/spine` 184, `service/internal/workspace` 225 PASS (subtests counted), 0 FAIL, 0 SKIP |
| CI `scenario-git-http` job, its four steps run verbatim (`bash -eo pipefail`) on a fresh `s3i_ci3` | `TestGitHTTP_*` 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS; `TestWorkflowLifecycle_*` 4 PASS (incl. `_UpdateExisting`, `_UpdateExisting_DraftAuthorCannotReview`), with the harness's merge binding on; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS; 0 SKIP |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` (`s3i_scen3`, registry `s3i_reg3` migrated) vs a `git archive main` export on its own databases | 31 top-level FAIL, the **same set as `main`**: the 30 known pre-existing failures plus `TestWorkspace_ScopedServices` (pre-existing with the registry URL set). With an unmigrated registry database both trees also fail `TestWorkspace_BatchMigration` and `TestWorkspace_Lifecycle` (33 each, identical sorted `--- FAIL` lists, 70 lines with subtests); `main` passes those two with the registry migrated (checked) |
| `golangci-lint` v2.11.4 (`local-scratch/bin`) `run --new-from-rev main` on the touched packages: plain, `--build-tags=integration`, `--build-tags=scenario` (with `service/scenariotest/...`) | 0 issues each (one gocritic `appendAssign` on the first `runWith` change was fixed in `f5b4347`) |
| `--enable-only=gosec` on the touched packages (`integration,scenario` tags) and on `./...` | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| CI `git-floor` steps in the local `golang:1.26-bookworm` image (git 2.39.5; `--pull=never --rm`, read-only mounts, `GOPROXY=off`), plus `adapters/projection` | all ok; no container left |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, **no `-c model=` override**; each review session's `session_meta` has `source: {"subagent":"review"}` and `"model":"gpt-6.1-sol"`, the newest `rollout-2026-10-14T13-49-08-…`) | round 1 (`f5b4347`): "No actionable regressions were identified". Round 2 (`f5b4347`): one P2, TASK-005 — fixed in `b911c04`. Round 3 (`b911c04`): one P2, TASK-003 with TASK-011's sweep — fixed in `70bed62`. Three rounds is the cap, so `70bed62` itself was not reviewed by codex again (open question for the orchestrator). The earlier task rounds that ran on older models (`-c model="gpt-5.5"`, `gpt-5.6-sol`, `gpt-6-astra` because the default was refused then) are superseded by these gpt-6.1-sol rounds over the whole wave-3 diff |

**SMP compatibility** (`i23/s-int3/smp` = SMP `main` `f5c6fd6d`, detached, `../spine` = this
worktree, `GOFLAGS=-mod=readonly`; never modified, `git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 13 SMP packages importing a changed Spine package (`go list` with test imports) plus `internal/customer/workflow` | 11 ok, `projectionsvc` has no tests; `gitread` (`TestBounded_ErrorContractEqualsCLIClient`: `bad_ref`, `bad_sha`, `missing_path`, `path_beyond_a_symlinked_dir`) and `projectionwriter` (`TestBoundedProjection_IncrementalAddAndModifyOversized`, `…_OversizedWorkflowStalls`, a nil-pointer panic) fail — the **identical** `--- FAIL` list against a Spine `main` `d23b897` export through a scratch `-modfile`: pre-existing, SMP TASK-004 tests that SMP TASK-006 adapts at the bump |
| SMP TASK-006 tip `935852b5` (a temporary detached SMP worktree beside this one, removed afterwards) against this Spine | `gitread` and `projectionwriter` **pass**; `go build ./...`, `go vet` plain and `-tags integration` OK. TASK-006's adaptations fix the two pre-existing failures, also with wave 3 |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3c_smp_it` with `vector`, `s3c_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov` as in CI) | 1835 passed, 0 failed, 2 allowed skips (the Linux-only `ru_maxrss` tests; macOS host); `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |

Environment: the shared PostgreSQL 18.6 container on `localhost:55630`. Only `s3i_*` (Spine) and
`s3c_*` (SMP) databases were created, and all were dropped afterwards; the SMP suites' own `ws_*`
databases and roles were cleaned up by the suites (none left, checked). The only containers
started (the `git-floor` runs, `--rm`) were removed; the Docker VM kept at least 6.1 GB free. Logs:
`local-scratch/wave3-int/`.

### Cross-task review (wave 3) (2026-10-14)

The wave 3 cross-task review raised no finding against this task's code. One P3 (filed against
TASK-005's integration docs, `94017aa`) concerned this task's documented time-of-check gap
(`service/internal/gateway/run_branch_write.go`: the run is read once, before the write): the
CHANGELOG UX bullet and `api-operations.md` §3.3 said a commit landing during a review "can only be
an operator's push or a push started before the review". An `/artifacts/add` or `write_context`
write authorized during the producer step can land after that step's submit; together with an
`automated` producer step that re-binds (Governed Merge Binding 2) it is then merged. The docs now
say so (`3aa24b9`, on `init/INIT-023-wave3-integration`; dispositions and gates in TASK-005's
"Cross-task review (wave 3)"). No code change here.

### Acceptance (wave 3) (2026-10-14)

The final gate on `init/INIT-023-wave3-integration` (worktree `i23/s-int3/spine`). It ran after the
codex 6.1 catch-up of waves 1 and 2 was merged in (`41c70dd8`: the worktreefs FIFO `O_NONBLOCK`
open, exact integers in stored workflow definitions, the sweep's `SetSyncBaseline`). The code gates
ran at `41c70dd8`. The two commits after it change documentation only: `c6938efd` (CHANGELOG) and
the commit that adds this note. Nothing was pushed. Main, the task branches and the SMP checkout
are unchanged.

**This task.** In the CI job, `TestWorkflowLifecycle_UpdateExisting`,
`TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview` and
`TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` pass. The first two are the only
difference from `main` in the full scenario run (198 top-level PASS against 196). The catch-up now
reads integers in stored definitions exactly (`core/workflow/projection_definition.go`). That does
not reach this task's `core/workflow/policy.go` or `service.go`, which compare no stored
definitions.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 21 packages changed since `d23b897` | 19 ok. `core/engine/enginetest` has no tests. `service/scenariotest/scenarios` builds only with `-tags scenario`; the scenario rows below cover it |
| `-tags integration -p 1 -count=1 -v` on the changed packages that have integration suites: `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace`. `SPINE_TEST_DATABASE_URL` = fresh `s3g_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3g_reg`, migrated from `migrations/registry` | 1086 PASS, subtests counted (315 / 191 / 171 / 184 / 225); 0 SKIP; 1 FAIL. The failure is `adapters/store` `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, column `workflow_version_label`), and it was there before: a `git archive` export of `main` `d23b897` fails it the same way. `adapters/store` is in scope only because of the catch-up's `SetSyncBaseline`; its `TestSetSyncBaseline_*` tests pass |
| CI `scenario-git-http` job: its four `run:` steps, read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, repo root as cwd, `SPINE_TEST_DATABASE_URL` = fresh `s3g_ci`) | All four steps exit 0. `TestGitHTTP_*`: 20 PASS, 8 of them `TestGitHTTP_Push*`. `TestBootstrapInternalAdmin_*`: 2 PASS. `TestWorkflowLifecycle_*`: 4 PASS, including both required ones. `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy`: PASS. 0 SKIP |
| Full scenario suite, `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...`, run side by side on this branch and on a `git archive` export of `main` `d23b897` (fresh `s3g_scen_{b,m}`, registry `s3g_reg_{b,m}` migrated) | The failure sets are identical: 31 top-level `--- FAIL` on each (59 lines with subtests). They are the 30 known failures plus `TestWorkspace_ScopedServices`, the same sorted list as the wave 3 review's run. Top-level PASS: 198 on the branch, 196 on main; the two extra are TASK-014's `TestWorkflowLifecycle_UpdateExisting*`. 0 SKIP |
| CI `git-floor` job: its three `run:` steps, verbatim, in `golang:1.26-bookworm` (`--pull=never --rm`; worktree, host module cache and step scripts mounted read-only; `GOPROXY=off`, `GOTOOLCHAIN=local`) | git 2.39.5. All three steps ok: `adapters/git`, `adapters/git/refname`, `core/engine`, then `adapters/git/...`, `core/artifact`, `core/workflow`. The container was removed |
| `golangci-lint` v2.11.4 `run --new-from-rev d23b897` on the changed packages, plain, `--build-tags integration` and `--build-tags scenario` | 0 issues each |
| `golangci-lint --enable-only=gosec` on the changed packages (`scenario`; `integration`) and on `./...` (plain; `integration,scenario`) | 0 issues, apart from one finding with `integration`: G602 at `adapters/store/postgres_integration_test.go:1563`. It was there before: the file is unchanged since `main`, and the `main` export reports the same finding |
| `gofmt -l` (Go 1.26.8) on the 150 `.go` files changed since `d23b897` | empty |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, no `-c model=` override) | Round 1 at `41c70dd8`: session `rollout-2026-10-14T16-10-25-01a13af6-f3bb-…`, `source: {"subagent":"review"}`, `"model":"gpt-6.1-sol"`. Round 2 at `c6938efd`: session `rollout-2026-10-14T16-26-49-01a13b05-f6d9-…`, same source and model. Both rounds returned "No actionable regressions were identified." Nothing to fix, so no third round |

**Merge `41c70dd8`: semantic check.** In every auto-merged file (`adapters/projection/service.go`,
`adapters/projection/sweep.go`, `core/worktreefs/tree.go`, `KNOWN-LIMITATIONS.md`), the merge's
change against each parent matches the other side's change against `d23b897` hunk for hunk.
Neither side lost or altered a line.

- **Baseline reset and the sweep.** On the up-to-date path, `resetSyncBaseline` now replaces the
  `idle@head` write. The wave 3 projection changes do not get in its way:
  - the TASK-003 refusals are reported on the diff path (`recordRefused`), which the up-to-date
    return never reaches;
  - the sweep's `unsafe_path` retirement is a row write, so it sets the `changed` flag that drives
    the reset;
  - so after a retirement only the baseline moves, as the catch-up intends.
- **Stored-definition comparisons.** The exact-integer `sameJSON` and `VerifyProjectionDefinition`
  are the only places that compare stored definitions, and wave 3 adds none. The keep-or-remove
  decision for an unsafe-path row (`retireUnsafeWorkflowRow` → `applyRejection`, and the sweep's
  `unsafe_path` branch) goes through `VerifyProjectionDefinition`, so it reads integers exactly
  too.
- **`core/worktreefs` and TASK-003.**
  - `git.DefaultMaxBlobBytes` is still defined as `worktreefs.DefaultMaxReadBytes`, which is
    unchanged.
  - `ReadFileLimit` still refuses on the Lstat size and still reads through
    `LimitReader(limit+1)`.
  - The non-blocking open and the `IsRegular` check only add a refusal before the read, and
    `clearNoBlock` makes the descriptor blocking again before `readCapped`.
  - TASK-005's `commitbinder.go` entry in the `scan_test.go` allowlist and the new
    `nonblock_*.go` files coexist; `core/worktreefs` passes, including under `-race`.
- **SMP.** `projectionsvc.New` passes a `*store.PostgresStore` (`NewPostgresStoreWithQuerier`), so
  SMP gets `SetSyncBaseline` without any change.
- **Defect found.** The only one: the CHANGELOG conflict resolution dropped the `---` rule before
  the catch-up section. `c6938efd` restores it (docs only).

**SMP compatibility.** `i23/s-int3/smp` is SMP `main` `f5c6fd6d`, detached, with `../spine` =
this worktree and `GOFLAGS=-mod=readonly`. The checkout was never modified, and `git status` is
clean.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` with no DSNs, on the 13 SMP packages that import a changed Spine package (test imports included) plus `internal/customer/workflow` | 11 ok; `projectionsvc` has no tests. Two packages fail with the same list as the wave 3 integration and review runs: `gitread` (`TestBounded_ErrorContractEqualsCLIClient`: `bad_ref`, `bad_sha`, `missing_path`, `path_beyond_a_symlinked_dir`) and `projectionwriter` (`TestBoundedProjection_IncrementalAddAndModifyOversized`, `TestBoundedProjection_OversizedWorkflowStalls`). These are the known SMP TASK-004 contract tests: they also fail against waves 1 and 2 on SMP `main`, and SMP's wave 3 branch fixes them. Reported here, not fixed |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3g_smp_it` with `vector`, `s3g_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, and `TEST_PROVISIONING_DSN` = `smp_prov` as in CI) | 1835 passed, 0 failed, 2 allowed Linux-only skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |

**Not run.**
- The other steps of SMP's integration job: the platform-DB shared suites, fenced migrate,
  workspace-schema, faultinject and verify-children. They are not in this gate list.
- Image-based gates (images, RDS emulation, an isolated `make dev-up` smoke). They need images
  built from these branches.
- GitHub CI. It cannot run this branch until Spine is pushed.

**Environment.**
- PostgreSQL: the shared PostgreSQL container on `localhost:55630`. Only `s3g_*` databases were
  created, and all of them were dropped. There were no `ws_*` databases or roles before the SMP
  suites ran and none after.
- Docker: the only containers started were the `git-floor` run (`--rm`) and the `alpine:3 df`
  disk checks. Nothing was pulled. The Docker VM had 6.3 GB free.
- Logs: `local-scratch/w3final/`.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: Completed.** Spine `main` was pushed on 2026-11-04 with the owner's approval. GitHub
  CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802` plus INIT-024
  TASK-010's git-identity fix) passed all 8 jobs. Every AC is met, and the owner questions stay at
  their recorded defaults.
