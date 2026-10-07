---
id: TASK-016
type: Task
title: "Ref-class push admission in the standalone git gateway (run, plan, divergence, init and non-branch refs)"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-07
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /architecture/api-operations.md
  - type: related_to
    target: /architecture/security-model.md
---

# TASK-016 — Ref-class push admission in the standalone git gateway (run, plan, divergence, init and non-branch refs)

---

## Purpose

This task closes the **push halves** of two findings from the 2026-10-06 Spine security audit
(Spine `61994fe`, SMP `80995559`) on the standalone service:

- **F2.spine-push** (P2): run branches can be pushed by anyone who can authenticate.
- **F10.spine-gateway** (P2): tags and other non-branch refs can be pushed by anyone, and they
  shadow run branches and `main` in the engine's bare-name git calls.

It also closes the **divergence-ref push vector** behind **F8**: any actor can write the
deterministically named divergence branch that convergence later merges.

Line numbers refer to Spine `61994fe` unless stated. "SMP" means `spine-management-platform` at
`80995559`. The scope is standalone only (`service/`). SMP runs its own gateway and is covered by
SMP TASK-005 and SMP TASK-007.

### The defect

1. **`spine/*` refs skip every check.**
   - The pre-receive loop in `service/internal/githttp/handler.go:401-404` drops any ref with prefix
     `refs/heads/spine/` (or bare `spine/`) before the policy runs. Its comment says they are
     "out of scope for user-authored rules (ADR-009 §3)".
   - Nothing else gates them. Push auth (`service/internal/gateway/handlers_git.go:93-133`) only
     authenticates. There is no role check, and the permission map (`core/auth/permissions.go`) has
     no git-push operation.
   - So any authenticated actor, including a `reader`, can create, advance, force-push or delete any
     run branch (`spine/run/*`), planning branch (`spine/plan/*`), divergence branch, or
     `spine/init`.
2. **Non-branch refs are allowed for everyone.**
   - `core/branchprotect/policy.go:80-85` returns Allow (`no_matching_rule`) for every ref outside
     `refs/heads/`. This is ADR-009 §6 ("tags out of scope") and is correct for *branch protection*.
   - But nothing else gates these refs, so `refs/tags/*`, `refs/notes/*`, `refs/<anything>` and
     `refs/remotes/*` are writable by any authenticated actor.
3. **A nil policy disables the gate completely.**
   - The whole evaluation sits inside `if policy != nil` (`handler.go:398`). With no policy (an
     early-bootstrap or no-DB deployment, `serve_builders.go:348-353` warns about it), every ref
     lands unchecked.
4. **The engine trusts whatever the run branch and bare names resolve to.**
   - The governed merge publishes the run-branch tip (`core/engine/merge.go:168-169`,
     `Source: run.BranchName`).
   - It addresses branches by bare name: merge source at `:169`, `Push(ctx,"origin","main")` at
     `:229`. Git resolves a bare `<n>` as `refs/<n>`, then `refs/tags/<n>`, then `refs/heads/<n>`
     (audit F10).
   - Convergence merges the divergence ref `spine/<run_id>/<divergence_id>/<branch_id>`
     (`core/divergence/convergence.go:178`, created at `core/divergence/service.go:221`).

### Attackers and reach

- **Who:** A1/A2/A6 in the audit's shorthand. That is any actor holding a workspace bearer token,
  any role: an AI agent, a contributor, a reader, or a compromised runner token. In `SPINE_DEV_MODE`
  the gateway skips git auth entirely (`handlers_git.go:104`), so any network client counts too.
- **Precondition:** `SPINE_GIT_RECEIVE_PACK_ENABLED=true`. This is opt-in (default off,
  `handler.go:99-104`), but it is the documented way to work on run branches
  (`docs/git-push-guide.md`).

  > **Design-check note (2026-10-06).** `docs/git-push-guide.md` documents pushing in general
  > (§2 unprotected branches, §3 protected, §4 operator override). It never mentions run
  > branches. The only run-branch text is the runner *clone* example at
  > `docs/integration-guide.md:472-479`. So no documented client flow pushes `spine/**` today.
  > That lowers the compatibility risk of this task and is why §7 item 4 must *add* the
  > "Pushing a run branch" section rather than amend one.
- **Not reachable in SMP:** SMP never imports `service/`. Its own lint forbids it
  (`dev/lint-spine-internals/main.go:54`).

### Impact

1. **Unreviewed content reaches `main` (F2).**
   - A reader pushes or force-pushes onto a colleague's run branch while review is open, or after
     `accepted`.
   - The publish step merges the tip, so content nobody reviewed lands on `main` under the trusted
     "Merge run …" message. That content can include `.spine/branch-protection.yaml` and
     `workflows/**` (audit F3).
   - The audit PoC `poc/standalone-service/poc_git_push_runbranch_test.go` reproduces this: a
     reader's push and force-push both land.
2. **Shadow refs (F10).**
   - A push of `refs/tags/<victim-run-branch>` makes the victim's next governed merge publish the
     tag's commit. The reviewed `refs/heads` ref is left untouched.
   - A push of `refs/tags/main` makes `git push origin main` ambiguous. That error is classified
     permanent, so every later governed merge in the workspace fails.
   - PoC `poc/verify-r2-0/push_phase1_test.go`: both tag pushes are accepted at `61994fe`
     (re-run 2026-10-06).
3. **Divergence branches (F8 vector).**
   - The ref is deterministic, so any actor can push it.
   - The honest worker who completes the branch step then carries that content through
     convergence.
4. **`refs/heads/spine` (new, verified 2026-10-06).**
   - Pushing a branch named exactly `spine` while no `spine/*` ref exists makes every later
     `git branch spine/run/…` fail with "'refs/heads/spine' exists; cannot create
     'refs/heads/spine/run/…'".
   - That is a workspace-wide denial of service on run creation. It is reachable after all run
     branches have been cleaned up.
5. **Deletion.** Any actor can delete any run, planning or divergence branch, which destroys
   in-flight work.

---

## Design

### 0. Invariants

- **I1 — Gate on ref class first, and always.** Every ref update on every `POST /git-receive-pack`
  is classified and admitted before `branchprotect.Policy` runs. This holds whether or not a
  policy is attached.
- **I2 — Fail closed.** No actor, no resolver, an unresolvable run, or a store error all deny.
  Operators and admins are never blocked by store state.
- **I3 — Keep existing behaviour for ordinary branches.** Refs under `refs/heads/` outside the
  reserved `spine` namespace go to `branchprotect.Policy` exactly as today, with the same
  messages, the same override semantics and the same audit events.
- **I4 — No schema, API or event change.** The admission adds no table, column, migration,
  endpoint, event type or environment variable.
- **I5 — The bound rule mirrors SMP.** It matches the rule SMP TASK-007 adopts (owner question 1
  here equals SMP's owner question 3). It is ADR-032 §1/§5 restated for a gateway with no job
  tokens: only the actor holding the live execution may write the run branch, and nobody below
  operator may delete it.

### 1. New package `core/refadmit` (imports only `core/domain` + stdlib)

The package is pure and transport-agnostic, so SMP TASK-007 can call it. Section 8 covers that.

**Ref classes** (`type Class string`)

| Class | Ref | Who may create/update | Who may delete |
|---|---|---|---|
| `branch` | `refs/heads/<x>`, `<x>` not `spine` and not under `spine/` | defer to `branchprotect.Policy` (unchanged) | defer to policy |
| `run_branch` | `refs/heads/spine/run/<name>`, `refs/heads/spine/plan/<name>` (`core/engine/branchname.go:21-24`, `:61-65`) | bound actor (§1.3) or operator+ | operator+ |
| `divergence_branch` | `refs/heads/spine/<run-id>/<divergence-id>/<branch-id>` (`core/divergence/service.go:221`) | actor bound to that branch's current step, or operator+ | operator+ |
| `init_branch` | `refs/heads/spine/init` (`service/internal/cli/initrepo.go:145`, `:164`) | operator+ | operator+ |
| `reserved` | `refs/heads/spine`, and any other `refs/heads/spine/**` (including malformed run/plan names) | operator+ | operator+ |
| `non_branch` | anything not under `refs/heads/` (tags, notes, `refs/<x>`, `refs/remotes/*`) | operator+, then policy (which allows) | operator+ |
| `invalid` *(design-check DC-2)* | any ref containing a byte `< 0x20` or `0x7f` | nobody | nobody |

#### 1.1 `Classify(ref string) Ref`

`Ref` holds the fields `Full`, `Short`, `Class`, `RunID` and `DivergencePath`. Classification is
syntactic only. The store is never consulted.

1. If `ref` does not start with `refs/heads/`, the class is `non_branch`. That covers `HEAD`,
   `refs/HEADS/main` and every refname TASK-001 still admits.
2. Otherwise `short` is `ref` without the `refs/heads/` prefix.
   - Compare against `spine` **case-insensitively**: if `lower(short)` is neither `spine` nor
     prefixed `spine/`, the class is `branch`.
   - Folding case is defence in depth. Git 2.54 with `core.ignorecase=true` already refuses a
     colliding create ("reference already exists", checked 2026-10-06). But the reserved namespace
     must not depend on the server's filesystem.
3. Every other `short` is `reserved` unless one of these exact, case-sensitive shapes matches:
   - **`short == "spine/init"`** gives `init_branch`.
   - **`spine/run/<name>` or `spine/plan/<name>`** gives `run_branch` when `<name>` is non-empty,
     contains no `/`, and contains a `-` that is not its last byte.
     - `RunID = "run-" + <text after the last '-'>`. Run IDs are `run-<8 hex>`
       (`core/engine/run.go:179`, `:245`, `:337`), and the branch name ends in `-<that hex>`
       (`branchname.go:61-65`, `:107-113`).
   - **`spine/<a>/<b>/<c…>`** gives `divergence_branch` when `<a>` is not `run`, `plan` or
     `init` and `<a>`, `<b>` and `<c…>` are all non-empty.
     - `RunID = <a>` and `DivergencePath = "<b>/<c…>"`.
     - The divergence ID is `<run_id>-div-<def_id>` (`core/divergence/service.go:85`).
     - The store's branch ID is `<divergence_id>-<branch suffix>` (`service.go:212-221`;
       `convergence.go:178`, `:241-247`).

> **Design-check note (2026-10-06) — four changes to `Classify`.** Each was confirmed against the
> `_work/task016` prototype with an overlay probe (`_work/task016-check/zz_check_test.go`).
>
> - **DC-1: fold with Unicode simple folding, not only `ToLower`.**
>   - Compare the first path component of `short` with
>     `strings.EqualFold(c, "spine") || strings.ToLower(c) == "spine"`.
>   - Why: on a case-insensitive server filesystem, the namespace aliases more than ASCII case.
>     On APFS with git 2.54 (checked 2026-10-06), `SPINE`, `Spine` and `ſpine` (U+017F, LATIN
>     SMALL LETTER LONG S) all name the same directory as `spine`.
>   - git refuses only a colliding **create**, when the old id is zero. An **update** that
>     carries the ref's current object id lands on the real `refs/heads/spine/...` ref.
>   - `strings.ToLower` leaves U+017F alone, so the prototype classifies
>     `refs/heads/ſpine/run/<victim>` as `branch`. That is a full bypass of this task on such a
>     filesystem. `strings.EqualFold` catches all three variants.
>   - Reach: not Linux production (ext4 is case-sensitive). It is reachable on macOS/Windows
>     hosts and through the documented dev bind mount (`docker-compose.override.yaml.example`:
>     `./:/repo`) on Docker Desktop for Mac.
>   - Other probed variants (U+0130, U+0131, fullwidth `ｓ`, zero-width joiners) did **not**
>     alias on APFS. HFS+-style ignorable code points are out of scope: HFS+ is not a supported
>     server filesystem.
> - **DC-2: a new class, `invalid`, denied for every actor.**
>   - Any ref that contains a byte `< 0x20` or `0x7f` classifies as `invalid`. This includes an
>     operator's push.
>   - Why: the prototype classifies `refs/heads/spine\x00` as `branch`. git ends the command at
>     the NUL (audit F1), so it would create `refs/heads/spine`, which is the D/F
>     run-creation DoS in Impact 4.
>   - TASK-001 rejects such refs in the parser first, so this is defence in depth. It mirrors
>     TASK-001 §3, which adds the same byte loop to `branchprotect.Policy`. `core/refadmit`
>     cannot import TASK-001's `adapters/git/refname.ValidateQualified` (core must not import
>     adapters), so it uses its own byte loop.
>   - Effect: this task's guarantees no longer depend on TASK-001 landing first.
> - **DC-3: `spine/init/...` is `reserved`.**
>   - The divergence shape requires `<a>` to be other than `run`, `plan` and `init`, as written
>     above. The prototype does not exclude `init`: it classifies `spine/init/x/y` as
>     `divergence_branch` with `RunID = "init"`.
>   - That is fail-closed, since the store lookup misses. But it costs a lookup and reports
>     `unresolved` instead of `operator_required`. AC 2 now pins the row.
> - **DC-4: length bound.**
>   - A `short` longer than 1024 bytes under the `spine` namespace is `reserved`, before any
>     shape match. Engine-made names are at most about 80 bytes (`branchname.go:13`,
>     `maxSlugLength = 60`).
>   - This is the classifier half of the §1.3 split cap (DC-5).

#### 1.2 `RunReader`: the read surface, with no store-interface change

```go
type RunReader interface {
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)
	GetBranch(ctx context.Context, branchID string) (*domain.Branch, error)
}
```

- `*store.PostgresStore` and the union `store.Store` (`adapters/store/interfaces.go:40-86`)
  satisfy it already.
- So does SMP's `spinecore.RunStore`, at SMP `internal/customer/spinecore/runstore.go:113`,
  `:383` and `:449`. All three were verified by an overlay compile on 2026-10-06.
- **Contract:** a missing run or branch is reported as a `*domain.SpineError` with
  `Code == domain.ErrNotFound`, which is what `notFoundOr` produces
  (`adapters/store/pgutil.go:16-21`). Any other error counts as an evaluation failure, and the push
  is denied.

#### 1.3 `Evaluator` and `Decide`

`NewEvaluator(rr RunReader) *Evaluator` is created once per push. Its signature:

```go
func (e *Evaluator) Decide(ctx context.Context, req Request) (Decision, error)
```

- `Request` holds `Ref string`, `Delete bool`, `Actor *domain.Actor` (nil means unauthenticated)
  and `RepositoryID string` (empty means primary).
- `Decision` holds `Allow`, `Class`, `Reason`, `RunID` and `ExecutionID`, plus a `Message()`
  method that returns wire text.

Rules, in order:

1. **Ordinary branch.** For class `branch`, return Allow (`ordinary_branch`). The caller continues
   to `branchprotect.Policy` as today.
2. **Operator.** If `Actor != nil` and `Actor.Role.HasAtLeast(domain.RoleOperator)`
   (`core/domain/actor.go:44`), return Allow (`operator`). This path never touches the store.
3. **Operator-only classes.** Classes `init_branch`, `reserved` and `non_branch` return Deny
   (`operator_required`).
4. **Run and divergence pre-checks.** Each check below denies:
   - `Delete` gives `delete_requires_operator`. This is parity with ADR-032 §5.
   - These give `not_bound`: `Actor == nil`, an empty `ActorID`, or a role below `contributor`.
     The role floor matches `step.submit` and `execution.claim`
     (`core/auth/permissions.go:35`, `:47`).
   - A nil reader gives `resolver_unavailable` (I2).
   - A store-backed ref already denied in this push gives `not_evaluated`. This bounds lookups to
     about two per push for an unbound actor, whatever the number of refs.
5. **Resolve the run.**
   - `GetRun(RunID)`: `ErrNotFound` gives `unresolved`; any other error is returned.
   - `run.Status != active` gives `not_bound`. That covers `pending`, `paused`, `committing` (the
     F2 merge window), `partially-merged` and every terminal state.
6. **Resolve the bound execution's target.**
   - For a **run branch:**
     - The expected name is `run.BranchName`. For a non-primary repository it is
       `run.RepositoryBranches[repo]` when that is set (`core/domain/run.go:71-75`).
     - Anything other than an exact, case-sensitive match gives `unresolved`. This rejects a
       forged name that reuses a real run's suffix.
     - The binding target is `(branch_id = "", step_id = run.CurrentStepID)`.
     - During divergence `CurrentStepID` is `divergence:<id>` (`core/engine/step.go:482`), so no
       run-level execution can match. The parent run branch is therefore operator-only while the
       run is diverged.
   - For a **divergence branch:**
     - Try each `/` split of `DivergencePath` into `<divID>/<suffix>`. Only splits where `divID`
       has the prefix `RunID + "-div-"` count.
     - For each, call `GetBranch(divID + "-" + suffix)`. `ErrNotFound` moves to the next split;
       any other error is returned.
     - A match needs `b.RunID == RunID && b.DivergenceID == divID`. No match gives `unresolved`.
     - A branch status other than `pending` or `in_progress` (`core/domain/divergence.go:25-28`)
       gives `not_bound`.
     - The binding target is `(b.BranchID, b.CurrentStepID)`. The cursor is kept by
       `core/engine/step.go:562-566`, and branch executions carry `BranchID`
       (`step.go:497-506`).

     > **Design-check note (2026-10-06) — DC-5: cap the split loop.**
     >
     > - Try at most **8** splits per ref, in order. Past the cap, return `unresolved` with no
     >   further reads.
     > - Why: every `/` after a segment that starts with `<RunID>-div-` passes the prefix test,
     >   so the loop issues one `GetBranch` per `/`. One ~60 KB ref (it fits a single pkt-line,
     >   inside the 1 MiB command cap at `handler.go:156`) drove **30,000** `GetBranch` calls in
     >   the prototype probe.
     > - The `not_evaluated` short-circuit does not help here, because it bounds refs only
     >   *after* the first denial.
     > - Who: any contributor who names any *active* run (run IDs are readable at `reader`).
     > - Cost: one push holds one of the 5 pack slots (`handler.go:122-124`) and a ServicePool
     >   ref while it issues tens of thousands of sequential queries.
     > - Legitimate suffixes *can* contain `/`. Exploratory branch IDs come from the request
     >   body unvalidated (`service/internal/gateway/handlers_divergence.go:35-46`). So the loop
     >   stays, but bounded. Eight splits covers a divergence definition ID and a suffix with
     >   several `/` each.
     > - Together with DC-4's 1024-byte bound, the worst case is 8 reads per push.
     > - SMP TASK-007's local copy (§2 rule 9 there) has the same loop and needs the same cap.
7. **Find the bound execution.** Call `ListStepExecutionsByRun(run.RunID)`; an error is returned.
   - The result is Allow (`bound_actor`, with `ExecutionID`) only if some execution `x` matches
     every field below. Otherwise it is Deny (`not_bound`).

     | Field | Required value |
     |---|---|
     | `x.BranchID` | the target branch ID |
     | `x.StepID` | the target step |
     | `x.ActorID` | `Actor.ActorID` |
     | `x.Status` | `assigned` or `in_progress` |
     | `repoOrPrimary(x.RepositoryID)` | `repoOrPrimary(req.RepositoryID)` |

   - `assigned` and `in_progress` with an actor are the bound states: `claim.go:166-167`,
     `assign.go:66`, `acknowledge.go:68`.
   - The open-slot state, `assigned` with an empty `ActorID` (`assign.go:55`), cannot match.
   - The repository check is a cheap guard for ADR-015 multi-repo runs. A primary-repo step's actor
     cannot push the run branch of a code repo. Code-repo push routing is unwired at HEAD (audit
     §4).

   > **Design-check note (2026-10-06) — DC-6: "latest attempt wins", not "some execution".**
   >
   > - Among the executions with `x.BranchID == target branch` and `x.StepID == target step`,
   >   take the **last** one in store order (`ORDER BY created_at`,
   >   `adapters/store/postgres_steps.go:111`). Allow only if *that* execution matches
   >   `ActorID`, `Status` and repository.
   > - Why:
   >   - This is the rule TASK-014 §4 step 2 already specifies for the API write path, and the
   >     rule `storeStepAssigner` uses (`service/internal/gateway/step_assign_fallback.go:57-67`).
   >   - TASK-014 says it will *call this predicate* once this task merges. With "some
   >     execution", that swap silently loosens TASK-014.
   >   - I5's "one rule" therefore needs the stricter form.
   > - Effect: an older attempt that a crash or race left `assigned`/`in_progress` can no
   >   longer authorize a push once a newer attempt exists. That is fail-closed. Normal flows
   >   always end the older attempt (`completed`/`failed`) before creating a new one.
   > - SMP TASK-007 (§1 "some step execution", §2 rule 10) needs the same wording.
   >
   > **Design-check note (2026-10-06) — DC-7: what `repoOrPrimary` means.**
   >
   > - It maps `""` to `domain.PrimaryRepositoryID` (`"spine"`, `core/domain/run.go:97`) and
   >   leaves every other value alone.
   > - Both sides need it:
   >   - Step executions store `"spine"` for primary steps (`core/engine/step_routing.go:53-63`);
   >     legacy rows may hold `""`.
   >   - The gateway passes `parseGitPath`'s `repositoryID`. That is `""` for `/git/{ws}/...`,
   >     but the literal `"spine"` for `/git/{ws}/spine/...`. `resolveGitRepoPath` treats both as
   >     primary (`handlers_git.go:347`).
   > - In the run-branch expected-name step above, "non-primary" means `repoOrPrimary(repo) !=
   >   "spine"`. That is what the prototype does.
   > - AC 3 now pins both spellings.
8. **Memoize.** Cache `GetRun` and `ListStepExecutionsByRun` by run ID inside the evaluator, for
   the life of one push.

**Wire messages (`Decision.Message()`).** No message names who is bound. The precise `Reason` goes
to the server log only.

| Case | Message |
|---|---|
| run branch denied | `ref-admission: <short> is a run branch; only the actor assigned to the run's current step, or an operator, may push it` |
| divergence branch denied | `ref-admission: <short> is a divergence branch; only the actor assigned to that branch's current step, or an operator, may push it` |
| delete | `ref-admission: deleting <short> requires the operator role` |
| `init_branch` | `ref-admission: spine/init is reserved for 'spine init-repo'; operator role required` |
| `reserved` | `ref-admission: <short> is in the reserved spine namespace; operator role required` |
| `non_branch` | `ref-admission: <ref> is not a branch; only operators may push tags or other non-branch refs` |
| `resolver_unavailable` | `ref-admission: run-branch admission is not configured on this server; operator role required for <short>` |
| `not_evaluated` | `ref-admission: <short> not evaluated (push already refused)` |
| caller-side store error | `ref-admission: evaluation error on <short>` |

The run-branch message covers `not_bound` and `unresolved` alike. Echoing the ref is safe because
TASK-001 has already rejected control bytes on every command line.

> **Design-check note (2026-10-06) — the `invalid` rule and its message (DC-2).**
>
> - **Rule 0, before rule 1:** class `invalid` returns Deny (`invalid_ref`) for every actor,
>   operators and admins included. It never touches the store.
> - **Message:** `ref-admission: ref name contains a control byte; push refused`. The message
>   does **not** echo the ref, so it stays safe even if TASK-001 has not landed.
> - **Log:** the server log quotes the ref with `%q`.
> - **Ordering:** rule 0 runs before the operator short-circuit, so an operator cannot be
>   tricked into creating a truncated `refs/heads/spine` by a client bug either.
> - **Message length:** wire messages echo `<short>`, which DC-4 bounds to 1024 bytes in the
>   reserved namespace. Ordinary `branch` refs do not reach a `ref-admission:` message at all.

### 2. `service/internal/githttp`: wire the admission into pre-receive

**`Config` and `Handler`** (`handler.go:82-112`, `:39-79`, `:140-150`)

- Add `RunRefs refadmit.RunReader` to `Config`, with a doc comment: a per-request value from
  `WithRunRefs` wins; with both nil, run, plan and divergence refs are operator-only.
- Store it in `Handler`.

**Context keys** (beside `:772-811`)

- Add `WithRunRefs(ctx, refadmit.RunReader)` and `runRefsFromContext`.
- Add `WithRepositoryID(ctx, id string)` and `repositoryIDFromContext`.
- The pattern is the same as `WithPolicy`.

**`prereceive`: replace the loop at `handler.go:398-448`** with one unconditional loop.

1. `actor := domain.ActorFromContext(ctx)` (a pointer; nil when unauthenticated).
2. `runs := runRefsFromContext(ctx)`, falling back to `h.runRefs`.
3. `adm := refadmit.NewEvaluator(runs)`.
4. For each update:
   1. Call `adm.Decide(ctx, Request{Ref: u.Ref, Delete: u.IsDelete(), Actor: actor,
      RepositoryID: repositoryIDFromContext(ctx)})`.
   2. On an error, log at Error and append the evaluation-error message.
   3. On a Deny, log at Warn (`ref`, `class`, `reason`, `actor_id`) and append `dec.Message()`.
   4. On an Allow for a `run_branch`, `divergence_branch`, `init_branch` or `reserved` ref:
      - Log at Info `pre-receive admitted reserved ref` (`ref`, `class`, `reason`, `actor_id`,
        `run_id`, `execution_id`).
      - `continue`. `spine/**` refs still never reach `Policy.Evaluate`, which keeps
        ADR-009 §3's "rules never name spine/*".
   5. On an Allow for a `branch` or `non_branch` ref: if `policy != nil`, run the existing
      `Policy.Evaluate` body unchanged, from `:405` to `:437`. That covers the override handling
      and `honoredOverrides`.
5. If any message was collected, deny the whole push as today (`:439-447`). Pre-receive stays
   all-or-nothing.

`actorForPush` (`:622-627`) is kept for `branchprotect.Request.Actor`. The override-event emission
at `:463-471` is unchanged.

> **Design-check note (2026-10-06) — DC-8: admission is decided when the push starts.**
>
> - `Decide` runs when the command section arrives. git updates the ref only after the PACK has
>   been uploaded and unpacked. `cgi.Handler` does not honour the request context, so the
>   window is bounded only by the HTTP server's `ReadTimeout`: 30 s by default
>   (`service/internal/gateway/server.go:384`), and configurable.
> - Consequence: a push *started* while the actor is bound can *land* after that actor has
>   submitted the step, while review is open. "Only the bound actor writes the run branch" is a
>   check-time property, not a write-time one.
> - It cannot be closed at this layer without a real post-unpack hook (Decision 5). TASK-005's
>   SHA-bound merge is the content-integrity control: what lands late is never what gets merged
>   unless someone validates and approves it again.
> - **Required:** document the window in `docs/git-push-guide.md`'s new run-branch section
>   (§7 item 4), and list it in Out of scope. **Not required:** any code change. AC 16 now
>   checks the documentation.
>
> > **Review round 1 correction (2026-10-07): the window is not bounded by `ReadTimeout`.**
> > `ReadTimeout` limits only the upload of the request body. After it, `git receive-pack`
> > indexes the pack, resolves deltas and checks connectivity, then updates the ref; nothing
> > bounds that phase (`net/http/cgi` starts `git-http-backend` with no context and kills it
> > only when copying output to the client fails, which does not reach `receive-pack`), and
> > the pusher influences its length through the pack it sends. The review freeze (OQ4) is a
> > push-start check for the same reason. The docs now say the window has no fixed bound; see
> > Implementation Notes → Review round 1, finding R1-2/R1-4.

### 3. `service/internal/gateway/handlers_git.go`

1. Add `Runs refadmit.RunReader` to `GitPushResources` (`:27-30`), and extend the doc comment at
   `:22-26`.
2. After `observe.WithWorkspaceID` (`:196`), set
   `ctx = githttp.WithRepositoryID(ctx, repositoryID)`.
   - `repositoryID` comes from `parseGitPath`, `:64`.
   - Empty means primary.
   - Set it on every request; it is only read on push.
3. In the resolver block (`:223-228`): `if res.Runs != nil { ctx = githttp.WithRunRefs(ctx,
   res.Runs) }`.
   - The resolver's `release` is already deferred past `ServeHTTP` (`:216`, `:231`), so the
     per-workspace store outlives the push.
4. Do not touch the auth gate (`:93-133`).
   - devMode still skips git auth, so devMode pushes reach admission with a nil actor. They keep
     working for ordinary branches and are refused for `spine/**` and non-branch refs.
   - Git-endpoint auth belongs to TASK-015 (P3-30). See Decision 8.

### 4. `service/cmd/spine`: wiring

1. **`buildGitPushResolver`** (`serve_builders.go:138-180`):
   - Single mode returns `Runs: runReaderOrNil(fallbackStore)`.
   - Shared mode returns `Runs: runReaderOrNil(ss.Store)`. That is nil when the workspace has no
     database yet (`:157-165`), which fails closed.
   - Add the helper `runReaderOrNil(st store.Store) refadmit.RunReader`, which returns a nil
     interface for a nil store.
2. **`buildGitHTTPHandler`** (`serve_builders.go:323-354`):
   - Add a `runs refadmit.RunReader` parameter and set `Config.RunRefs`.
   - When `receivePackEnabled && runs == nil`, log at Warn: "run-branch pushes are operator-only:
     no run store wired".
3. **`serve_wiring.go:421`:** pass `runReaderOrNil(deps.Store)`.

### 5. `adapters/store/interfaces_assertions.go`

Add `_ refadmit.RunReader = (*PostgresStore)(nil)`. `adapters` may import `core`. `core/refadmit`
imports no adapter, so `dev/lint-boundaries.sh` needs no allowlist entry.

### 6. Existing tests that change on purpose

1. **`service/internal/githttp/handler_test.go:447-486`, `TestServeHTTP_PreReceiveAllowsSpineRefsWithoutPolicy`.**
   - It asserts that an anonymous push of `refs/heads/spine/run/abc` passes the gate. After this
     task it is refused (`reserved`, because `abc` has no run suffix).
   - Replace it with the two tests in AC 7.
   - The 2026-10-06 overlay probe confirmed this is the only `githttp`, `gateway` or `cmd/spine`
     unit test the change breaks.
2. **`service/scenariotest/scenarios/git_http_serve_test.go:538-631`, `TestGitHTTP_PushAcceptedWhenFlagOn`** (build tag `scenario`, test DB).
   - It pushes `spine/test/push` anonymously in devMode (`:555`, `:588`, `:613`, `:620-623`).
   - Change the pushed branch to `feature/push`. The scenario's purpose ("does the flag let CGI
     push through?") is unchanged.
   - Add the reserved-ref scenario from AC 13.

### 7. Documentation

1. **`architecture/api-operations.md` §6:** add a "Git push (receive-pack) admission" subsection
   with the class table in §1. It documents `refs/heads/spine/init` as a reserved bootstrap branch,
   operator-only, written by `spine init-repo`.
2. **`architecture/git-integration.md`:**
   - §6.4: `spine/*` is reserved and enforced, not only a convention.
   - §6.5: add the enforcement step "ref-class admission" between steps 2 and 3, and correct
     step 3's "`spine/*` refs bypass the policy". They still bypass the *policy*, but they no
     longer bypass the gate.
3. **`docs/integration-guide.md:461`:** correct "Refs under `spine/*` … bypass policy by design".
4. **`docs/git-push-guide.md` §5:** add the `ref-admission:` messages to the catalogue. Add a
   "Pushing a run branch" section: claim the step first; delete needs an operator; tags need an
   operator.
5. **`architecture/security-model.md` §7.3:** replace the stale "Git push path (planned)" bullet
   and add the admission paragraph.
6. **`architecture/adr/ADR-009-branch-protection.md`:** add an "Amendment — 2026-10 (INIT-023
   TASK-016)" note.
   - §3 and §6 still hold for branch-protection *rules*.
   - A separate ref-class admission now gates `spine/**` and non-branch refs at the git gateway.
7. **`CHANGELOG.md`:** add a security entry with the upgrade note in §9.

### 8. SMP: effect, and what the dependent SMP task must do

- **No SMP change is required by this task.**
  - SMP does not import `service/` (`dev/lint-spine-internals/main.go:54`).
  - The only exported additions are the new package `core/refadmit` and one compile-time
    assertion in `adapters/store`. Both are additive.
  - At the `.spine-revision` bump (SMP TASK-012), SMP compiles unchanged.
  - SMP's local `vendor/` is gitignored and CI builds through `replace => ../spine`, so no vendor
    refresh is needed unless SMP imports the new package.
- **SMP TASK-007 (run ownership in SMP's own gateway) must enforce the same rule.**
  - Owner question 1 here and SMP's owner question 3 must have one answer.
  - Recommended: SMP's `internal/customer/githttp` pre-receive calls `refadmit.Classify` and
    `NewEvaluator(spinecore.RunStore).Decide`. The call goes after its job-token ref-scope check
    (SMP `internal/customer/githttp/handler.go:503-530`) and replaces its `spine/*` skip (`:567`).
  - Each SMP principal is mapped to a `domain.Actor` the same way it already is for
    branchprotect. Both gateways then share one implementation.
- **Adopting the package adds a cross-repo order.**
  - The order is spine:TASK-016 → `.spine-revision` bump → smp:TASK-007, and it turns smp:TASK-007
    into a Spine-dependent task.
  - If smp:TASK-007 must land first, it implements the same table and rules locally. SMP TASK-012
    then records the convergence as a follow-up.
  - Note for SMP: `spinecore.RunStore.GetRun` returns its own `ErrRunNotFound`
    (`runstore.go:15-19`), not `domain.ErrNotFound`. Adopting `Decide` needs a thin wrapper that
    maps it. Without the wrapper the push is still denied, but with the evaluation-error message.

> **Design-check note (2026-10-06) — DC-9: SMP already chose the local copy, so it must mirror
> these amendments.**
>
> - SMP TASK-007 (`batch7/E4-TASK-007-run-ownership.md`, Decision 1 and §2) takes the
>   "implements locally" branch: it adds `internal/customer/githttp/refadmit.go`, which copies
>   this task's grammar and binding verbatim. So there is **no** cross-repo ordering, and the
>   "Recommended" bullet above is now the follow-up path.
> - **Swap later.** After SMP TASK-012's `.spine-revision` bump, SMP replaces its copy with
>   `core/refadmit`. Before that, SMP's lint allows `core/...` imports
>   (`dev/lint-spine-internals/main.go:8-10`), so no allowlist change is needed.
> - **Mirror now.** Because SMP copied *this* text, DC-1 (`EqualFold`), DC-2 (`invalid`),
>   DC-3, DC-4, DC-5 (split cap) and DC-6 (latest attempt) must be mirrored in SMP TASK-007
>   before it is built. Otherwise the two gateways drift on day one, which I5 forbids.
> - **SMP impact of DC-1.** It matters *more* in SMP than in standalone: customer-app's repo
>   volume on a macOS developer machine is case-insensitive whenever it is a host bind mount.
>   Production is Linux.
> - **SMP impact of DC-5.** It matters *more* too: customer-app is a shared multi-tenant
>   process, and the 30,000-read amplification would run against one workspace's DB pool while
>   holding a gateway slot.
> - **Compile and lint, verified.** `adapters/store` gaining an import of `core/refadmit` is
>   invisible to SMP's lint, which checks SMP's own imports only. `.gitignore:3` lists `vendor/`,
>   which confirms the vendor claim above.

### 9. Coordination, ordering and upgrade note

- **spine:TASK-001 lands first.** Every command line is validated, so `Classify` sees only valid
  refnames and the echoed refs carry no control bytes.
  - *Design-check note (2026-10-06):* with DC-2 this is a preference, not a correctness
    dependency. If the order slips, this task still refuses control-byte refs on its own.
- **spine:TASK-014 (P3-6) consumes this task's predicate** for API-path run-branch writes
  (TASK-014 §4 step 2).
  - *Design-check note (2026-10-06):* DC-6 makes the two rules identical ("latest attempt
    wins").
  - Owner question 4 must be answered together with TASK-014's owner question 5 (review
    freeze). Otherwise the API path freezes a run branch under review while the push path lets
    the review assignee write it.
- **spine:TASK-015 depends on this task.** It edits the same `handleGit` auth gate (P3-30 trusted
  CIDR and devMode).
- **spine:TASK-002** qualifies refs in the CLI client. It is the root fix for F10 shadowing; this
  task closes the gateway half.
- **spine:TASK-005** binds the merge to the validated SHA. A bound actor (including a reviewer
  bound to the review step) can still advance the tip until that lands.
- **spine:TASK-012** fixes the convergence target (F8 merge half). This task only closes who can
  write the divergence ref.
- **Upgrade note (CHANGELOG and integration guide):**
  - Shadow refs pushed before the upgrade persist.
  - Operators should list the non-branch refs on each workspace repo with
    `git -C <repo> for-each-ref --format='%(refname)' | grep -v '^refs/heads/'`.
  - Delete any ref whose short name equals a branch name. TASK-002 then makes any that remain
    inert.

---

## Decisions

1. **DECIDED — Put the classifier and decision in `core/refadmit`, not inline in
   `service/internal/githttp`.**
   - *Rejected: inline in githttp.* SMP cannot import `service/internal/...`, so the "same rule in
     both gateways" (I5) would be two copies that drift.
   - The package is pure. It imports `core/domain` only, needs no lint allowlist, and
     `cmd/embed-smoketest` still links.
2. **DECIDED — Find the run from the branch's run-ID suffix, then require exact `BranchName`
   equality.**
   - *Rejected: a new `GetRunByBranchName` store method.* It would change `store.RunStore`,
     which has 11 implementers in Spine plus SMP's `spinecore.RunStore`. It would also want an
     index on `runtime.runs.branch_name`, which breaks I4.
   - *Rejected: scanning `ListRunsByStatus(active)`.* That is O(runs) per push.
   - A forged name that reuses a real suffix fails the equality check.
   - A run whose ID is not `run-<suffix>` cannot be pushed by a bound actor and falls back to
     operator-only. No production code creates such runs.
3. **DECIDED — "Bound" means the step execution's `ActorID`, not the run's starter.**
   - `domain.Run` has no initiator field (`core/domain/run.go:51-89`), so using the starter would
     need a schema change.
   - The step execution is also what SMP's job token binds to (ADR-032 §1).
4. **DECIDED — An unwired resolver fails closed (operator-only).**
   - *Rejected: permissive-when-nil, like `Config.Policy` (`handler.go:305-308`).* That is the
     defect class this task closes.
   - Operators keep working with no store at all, so bootstrap and recovery are unaffected.
5. **DECIDED — A bound actor may create or update the run branch, including force-push, but may
   not delete it.**
   - Delete-refusal is ADR-032 §5 parity.
   - A fast-forward-only rule cannot be enforced at this layer. The pre-receive gate runs before
     `git-http-backend` unpacks the PACK, so the new commits are not in the repo yet.
   - `receive.denyNonFastForwards` would be repo-wide and would also block operators.
   - TASK-005's SHA binding makes post-validation rewrites harmless. See Out of scope.
6. **DECIDED — Do not restrict by step type** (for example, refuse the reviewer bound to a review
   step).
   - Detecting step type needs the run's pinned workflow definition on the push path: a workflow
     load and a projection dependency.
   - SMP's job token makes no such distinction.
   - TASK-005 covers the risk this would address. Owner question 1 offers the stricter variant.
7. **DECIDED — Fold case for the reserved namespace and reserve `refs/heads/spine` itself.**
   - Both are cheap guards. The second closes the D/F denial of service verified 2026-10-06.
   - *Design-check note (2026-10-06):* the fold is **not** only defence in depth. §1.1 step 2
     says git 2.54 "already refuses a colliding create", and that is true only for a create.
     On a case-insensitive filesystem, an update whose old id equals the ref's current id lands
     on the real ref (checked on APFS). So on such hosts the fold is the control itself, and it
     must use Unicode simple folding (DC-1), not ASCII-only `ToLower`.
8. **DECIDED — Leave git auth and devMode alone here.**
   - devMode pushes are anonymous, so they are refused for reserved and non-branch refs. That is
     fail-closed, and the shipped compose file sets neither `SPINE_DEV_MODE` nor receive-pack.
   - Validating a presented bearer in devMode, so bound pushes work in dev, belongs to TASK-015
     (P3-30), which depends on this task.
9. **DECIDED — Bound the per-push store cost by denying the rest without lookups once one
   store-backed ref is refused, and by memoizing per run.**
   - *Rejected: a hard cap on refs per push.* It would also hit legitimate operator pushes.
10. **DECIDED — Log operator admissions of reserved refs; emit no event.**
    - A new governance event would change `event-schemas.md` and break I4.
    - Owner question 3 offers the event-based variant.

---

## Owner questions

The design works without answers, using the stated defaults.

1. **Who may push a run or planning branch?** The answer must be identical to SMP TASK-007's owner
   question 3.
   - *Default:*
     - **Operator or admin:** may always push.
     - **Anyone else:** must meet every condition below.
       - The actor's `ActorID` matches the actor of the run-level step execution for the run's
         `current_step_id`.
       - That execution is `assigned` or `in_progress`.
       - The run is `active`.
       - The actor's role is `contributor` or higher.
       - The execution's repository is the target repository.
       - The push creates or updates the branch. Deletion is refused.
   - *Alternatives:*
     - (a) Any non-terminal execution of the run, not only the current step. This is looser.
     - (b) As the default, but excluding executions of `review`-type steps. This is stricter and
       needs the pinned workflow.
     - (c) Also allow `paused` runs.
   - *Design-check note (2026-10-06):* the default's first condition now reads "the actor
     matches the **latest** run-level execution of the current step" (DC-6). The answer must
     now be identical across three tasks: this one, SMP TASK-007 owner question 3, and the
     binding half of TASK-014 §4.
   - *Ruling (orchestrator, 2026-10-07; the default, pending the owner):* the default as amended
     by DC-6 — operator/admin always; otherwise the actor holds the **latest** run-level
     execution of `current_step_id` (`assigned` or `in_progress`) on an `active` run, role
     `contributor`+, same repository, create/update but never delete. Implemented as such.
2. **Non-branch refs (tags, notes) are operator-only.** Is there a legitimate non-operator tag
   pusher, such as CI release tagging with a contributor token?
   - *Default:* operator-only. CI must use an operator token.
   - If an exception is wanted, it is a follow-up: a narrow allow such as `refs/tags/release/*`
     that never admits a tag whose short name equals an existing branch name or `main`.
   - *Ruling (orchestrator, 2026-10-07; the default, pending the owner):* operator-only.
3. **Audit for operator pushes to reserved refs.**
   - *Default:* an Info log line only.
   - *Alternative:* require `-o spine.override=true` and emit a `branch_protection.override`-style
     governance event. That needs an event-schema change and would make operator remediation
     pushes two-step.
   - *Ruling (orchestrator, 2026-10-07; the default, pending the owner):* an Info log line only.

*Added by the design check (2026-10-06):*

4. **Should a run branch be frozen while its current step is a `review` step?** This must be
   answered once, for the push path (this task, alternative (b) of question 1) and the API path
   (TASK-014 owner question 5).
   - *Today's defaults disagree:*
     - TASK-014 freezes the branch on the API path, for every role.
     - This task does not freeze it on the push path.
     - So the review assignee is refused through `write_context` but admitted through
       `git push`, and an operator is refused by the API but admitted by push.
   - *Recommendation:* adopt the freeze in both for non-operators.
     - `refadmit` gains an optional `StepDefLookup` hook. The standalone gateway wires
       `assignerFor(ctx).LookupStepDef`, the same source TASK-014 uses.
     - A nil hook, or an unresolvable step definition, denies non-operators (I2).
     - Operators stay exempt on the push path, which keeps it the remediation route.
   - This closes the "reviewer moves the tip" gap before TASK-005 lands, and it keeps I5.
   - If the owner keeps "no freeze", TASK-014 must drop its freeze, or document the divergence
     as accepted.
   - *Ruling (orchestrator, 2026-10-07; pending the owner):* adopt the freeze for non-operators
     through an optional `StepDefLookup` hook in `refadmit` that fails closed when nil; the API
     freeze stays TASK-014's and the hook is reusable by it. Implementation Notes record the
     exact shape (it also freezes `internal` steps, for parity with TASK-014's amended I6).
5. **Who owns the case-insensitive-filesystem gap in `branchprotect`?**
   - On a case-insensitive server filesystem, a case-variant *update* of a protected branch (for
     example `refs/heads/MAIN`, old id = `main`'s tip) is evaluated against rule `main`. The rule
     does not match (`path.Match` is case-sensitive), so the push is allowed, and git then
     advances the real `refs/heads/main`. Checked 2026-10-06, git 2.54 on APFS.
   - This task only fixes the `spine` namespace. Its Out-of-scope bullet called the
     `branchprotect` case "not exploitable on git 2.54", which is wrong for updates.
   - *Recommendation:* add it to TASK-001 §3 (the same `Policy.Evaluate` defence-in-depth spot):
     - normalize the branch for matching with `EqualFold` semantics; or
     - deny any `refs/heads/` ref whose `EqualFold` class collides with a protected literal and
       whose spelling differs from it.
   - Severity is dev-only, because Linux production is case-sensitive. Reach: macOS/Windows
     hosts and the documented `./:/repo` bind mount on Docker Desktop for Mac.
   - *Disposition (TASK-001, 2026-10-07):* not folded into TASK-001 §3. TASK-001 owner question 1
     (default, pending the owner) documents the storage requirement and routes fail-closed
     handling to a new INIT-023 EPIC-001 follow-up task (see the corrected Out-of-scope bullet).
   - *Ruling (orchestrator, 2026-10-07; pending the owner):* stays with the TASK-001 follow-up;
     documentation only here.
6. **Exploratory divergence branches are a binding entry point that no task owns.**
   - `divergence.create_branch` is `contributor` (`core/auth/permissions.go:65`). Its
     `branch_id` is unvalidated (`handlers_divergence.go:35-46`), and nothing ties the caller to
     the run.
   - So any contributor can add a branch to another actor's open exploratory divergence, claim
     its step, and thereby become "bound" under this task's divergence rule.
   - TASK-014 explicitly sets divergence branch creation aside (its line 1300).
   - *Recommendation:* route it to TASK-007 (step-entry guards):
     - the caller must be bound to the run, or be an operator;
     - `branch_id` must match the workflow ID grammar (`core/workflow/service.go:23`).
   - Until then, this task's divergence binding is only as strong as that endpoint.
   - *Addition (TASK-001 cross-task review, wave 1, 2026-10-07): the same `branch_id` reaches the
     control-byte guard.*
     - `createBranchRecord` builds `spine/<run>/<div>/<branch_id>` (`core/divergence/service.go:221`)
       and passes it to `Policy.Evaluate` (`:224`) before anything checks its bytes.
     - So `{"branch_id":"a\u0001"}` is refused fail-closed by TASK-001's guard (Design §3), but it is
       reported as an evaluation failure, not as bad input:
       - an ERROR log, `branch-protection evaluation failed on divergence` (`:64`);
       - HTTP 500 `internal_error`, `branch-protection evaluation failed: branchprotect: branch
         "spine/…/a\x01" contains control byte 0x01 (malformed ref from caller)`.
     - Reproduced through the gateway route with the real divergence service and policy. Before
       TASK-001 the same request also returned 500, from the git branch failure, so the status did not
       get worse.
     - **Whichever task takes this entry point** (recommended above: TASK-007) must validate
       `branch_id` before `Evaluate` and return `ErrInvalidParams` (HTTP 400). The workflow ID grammar
       recommended above refuses every control byte, so it covers this.
     - TASK-001's `TestCreateExploratoryBranch_ControlByteBranchIDFailsClosed`
       (`core/divergence/service_controlbyte_test.go`) asserts only that the refusal is fail-closed,
       so it stays valid after that change.
   - *Ruling (orchestrator, 2026-10-07; pending the owner):* **this task owns it.** `branch_id` is
     validated against the workflow ID grammar (`ErrInvalidParams`, 400) and the caller must be
     bound to the run or an operator, in a way that is identical for every input SMP produces
     (SMP never calls `CreateExploratoryBranch`) — see Implementation Notes.

---

## Acceptance Criteria

1. **`core/refadmit` exists** as specified in §1 and imports only `core/domain` and stdlib.
   - `./dev/lint-boundaries.sh` passes with no new allowlist line.
   - `go build ./cmd/embed-smoketest` passes.
   - `adapters/store/interfaces_assertions.go` asserts `_ refadmit.RunReader =
     (*PostgresStore)(nil)`.
2. **`TestClassify`** (table-driven). Each row asserts the class, plus `RunID` and
   `DivergencePath` where applicable.

   | Ref | Expected class |
   |---|---|
   | `refs/heads/main` | `branch` |
   | `refs/heads/feature/spine/run/x` | `branch` |
   | `refs/heads/spinefoo` | `branch` |
   | `refs/heads/spine` | `reserved` |
   | `refs/heads/SPINE/run/x-0a1b2c3d` | `reserved` |
   | `refs/heads/Spine/init` | `reserved` |
   | `refs/heads/spine/run/` | `reserved` |
   | `refs/heads/spine/run/abc` | `reserved` |
   | `refs/heads/spine/run/a/b-0a1b2c3d` | `reserved` |
   | `refs/heads/spine/other` | `reserved` |
   | `refs/heads/spine/init/x` | `reserved` |
   | `refs/heads/spine/run/task-001-x-0a1b2c3d` | `run_branch`, `RunID = run-0a1b2c3d` |
   | `refs/heads/spine/plan/task-001-x-0a1b2c3d` | `run_branch` |
   | `refs/heads/spine/init` | `init_branch` |
   | `refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b1` | `divergence_branch` |
   | `refs/tags/spine/run/task-001-x-0a1b2c3d` | `non_branch` |
   | `refs/tags/main` | `non_branch` |
   | `refs/notes/commits` | `non_branch` |
   | `refs/spine/run/x` | `non_branch` |
   | `refs/remotes/origin/main` | `non_branch` |
   | `refs/HEADS/main` | `non_branch` |
   | `HEAD` | `non_branch` |

   *Design-check rows (2026-10-06).* The `Spine/…` and D/F rows already pass at the
   `_work/task016` prototype and pin existing behaviour. Every other row fails there and must
   pass after the fix.

   | Ref | Expected class | Pins |
   |---|---|---|
   | `refs/heads/ſpine/run/task-001-x-0a1b2c3d` (U+017F) | `reserved` | DC-1 |
   | `refs/heads/Spine/run/task-001-x-0a1b2c3d` | `reserved` | DC-1 |
   | `refs/heads/spine\x00` | `invalid` | DC-2 |
   | `refs/heads/spine/run/task-001-x-0a1b2c3d\x00` | `invalid` | DC-2 |
   | `refs/heads/main\x7f` | `invalid` | DC-2 |
   | `refs/heads/spine/init/x/y` | `reserved` | DC-3 |
   | `refs/heads/spine/run`, `refs/heads/spine/plan` | `reserved` | D/F blockers |
   | `refs/heads/spine/run-0a1b2c3d` | `reserved` | D/F blocker for divergence refs |
   | `refs/heads/spine/run/` + 1020 × `a` + `-0a1b2c3d` | `reserved` | DC-4 (otherwise a `run_branch` shape) |

   - *Mutation checks:*
     - replacing the fold with plain `strings.ToLower` fails the U+017F row;
     - removing the byte loop fails all three `invalid` rows.

3. **`TestDecide_RunBranch`** (fake `RunReader`).
   - The run fixture is `run-0a1b2c3d`: `active`, current step `execute`, execution `e1` held by
     `alice` (`in_progress`, repository `spine`).
   - **Allowed:**
     - `alice` as contributor, with the execution in each of `assigned` and `in_progress`.
     - An operator and an admin, including a delete.
   - **Denied:**
     - another contributor;
     - `alice` with role `reader`;
     - a nil actor, and an actor with an empty ID;
     - an actor holding an `assigned` execution of a *non-current* step;
     - an actor holding the current step ID on a *branch* execution (`BranchID` set);
     - the execution in each of `waiting`, `blocked`, `completed`, `failed`, `skipped`;
     - the run in each of `pending`, `paused`, `committing`, `partially-merged`, `completed`,
       `failed`, `cancelled`;
     - a branch name with the same suffix but a different slug;
     - an unknown run;
     - a repository mismatch (`RepositoryID: "payments"`);
     - `alice` deleting.
   - Operator rows assert that the fake recorded **zero** reader calls.
   - *Mutation check:* each change below fails at least one row.
     - dropping the `ActorID` equality;
     - dropping the status filter;
     - dropping the `CurrentStepID` match;
     - dropping the `BranchID == ""` match;
     - dropping the run-`active` check;
     - dropping the `BranchName` equality;
     - dropping the delete guard;
     - dropping the contributor floor;
     - dropping the repository match;
     - moving the operator check after the lookup.
   - *Design-check rows (2026-10-06):*
     - **Allowed (DC-7):**
       - `alice` with `Request.RepositoryID: "spine"`, the literal primary spelling;
       - `alice` with execution `RepositoryID: ""`, a legacy row, and `Request.RepositoryID: ""`.
     - **Denied (DC-6):** two run-level executions of `execute`. `e1` (older) is `in_progress`
       for `alice`; `e2` (newer) is `assigned` to `bob`.
       - `alice` is denied.
       - `bob` is allowed.
     - *Mutation checks:*
       - replacing "latest attempt" with "any matching execution" fails the `alice` row;
       - a `repoOrPrimary` that maps only `""` fails the `"spine"` row.
4. **`TestDecide_Divergence`.**
   - **Allowed:** the actor holding the branch's current-step execution (`BranchID` set,
     `assigned`).
   - **Denied:**
     - the run-level actor of the same run;
     - an actor bound to a sibling branch;
     - the branch in `completed` or `failed`;
     - a `Branch` whose `RunID` or `DivergenceID` differs from the ref;
     - a divergence segment not prefixed `<run>-div-`;
     - an unknown branch.
   - A `DivergencePath` with an extra `/` in the suffix resolves when a matching `Branch` exists.
5. **`TestDecide_FailClosed`.**
   - A nil reader denies the run and divergence classes for a contributor (`resolver_unavailable`)
     and still allows an operator.
   - A non-NotFound reader error is returned as an error.
   - After the first denial in an evaluator, a further 50 distinct foreign run refs cause no reader
     calls: the total stays ≤ 2.
   - *Mutation check:* removing the `not_evaluated` short-circuit fails the count.
   - *Design-check addition (2026-10-06, DC-5):*
     - **Setup:** an active run `run-0a1b2c3d`, and a contributor who pushes one divergence ref
       whose `DivergencePath` is `run-0a1b2c3d-div-d` followed by 10,000 `/a` segments, with
       every `GetBranch` returning NotFound.
     - **Expected:** at most **8** `GetBranch` calls, and `Deny` (`unresolved`).
     - At the prototype this probe made 30,000 calls for 30,000 segments.
     - *Mutation check:* removing the cap fails the count.
6. **`TestDecide_NonRunClasses`.**
   - `init_branch`, `reserved` and `non_branch` deny contributor, reviewer, nil and reader actors,
     and allow operator and admin.
   - `branch` allows every actor, including nil, with reason `ordinary_branch`.
   - *Design-check addition (2026-10-06, DC-2):*
     - `invalid` denies **every** actor, operator and admin included, with reason `invalid_ref`
       and zero reader calls.
     - `Decision.Message()` for `invalid` contains no byte `< 0x20`.
7. **githttp unit tests** (`service/internal/githttp/handler_test.go`, using the existing
   `buildPushBody`).
   - **(a) `TestServeHTTP_SpineRefsSkipPolicyButNotAdmission`** replaces `:447-486`.
     - It uses a deny-all `*` policy.
       *Review round 1 note: `*` never matches `spine/run/abc` (`path.Match`'s `*` stops at
       `/`), so this row was vacuous. The test now uses rules that match every `spine/**` shape
       and a counting policy that must see zero evaluations (finding R1-6).*
     - An operator push of `refs/heads/spine/run/abc` produces no denial body: the policy is still
       skipped for `spine/**`.
     - The same push with no actor returns `ng refs/heads/spine/run/abc` and an `ref-admission:`
       line.
   - **(b) `TestServeHTTP_RefAdmissionRunsWithoutPolicy`** uses `Config{Policy: nil}`.
     - A contributor push of `refs/tags/x` is refused.
     - *Mutation check:* re-wrapping the loop in `if policy != nil` fails it.
   - **(c) `TestServeHTTP_RefAdmissionMixedPushAllOrNothing`.**
     - A contributor push of `refs/heads/feature/x` and `refs/tags/x` together marks both refs
       `ng`.
   - **(d) Per-request reader beats the config reader.** The run reader comes from
     `WithRunRefs`, not `Config.RunRefs`.
     - The actor is bound in the context reader and unbound in the config reader, so the push is
       admitted.
     - With both readers nil, the bound push is refused.
8. **The run-branch push PoC becomes a committed regression test.**
   - `service/internal/gateway/git_push_admission_test.go`, test
     `TestGitPush_ReaderCannotPushForeignRunBranch`, is ported from
     `poc/standalone-service/poc_git_push_runbranch_test.go`.
   - It keeps the PoC's setup: real `gateway.NewServer`, real `githttp.NewHandler` with
     `git-http-backend` CGI (skip if unavailable), a reader token, `StaticRules(nil)`, and the
     literal `spine/run/victim-run`.
   - It inverts the PoC's assertions:
     - the push and the force-push both exit non-zero with `ref-admission:` on stderr;
     - `rev-parse spine/run/victim-run` equals the reviewed SHA;
     - `show spine/run/victim-run:task.md` is `reviewed content`.
   - *Fails before the fix:* at `61994fe` the PoC shows both pushes land.
     - *Mutation check:* making `Decide` always allow, with the types kept, fails this test and
       AC 9–12. The 2026-10-06 overlay probe confirmed this.
9. **Bound and operator pushes still work** (same file, real git, `GitPushResolver` returning
   `Runs`).
   - The fixture run `run-0a1b2c3d` (`BranchName spine/run/task-001-victim-0a1b2c3d`) has its
     current step held by contributor `alice`.
   - `alice` pushing `HEAD:refs/heads/spine/run/task-001-victim-0a1b2c3d` succeeds, and the server
     ref equals the pushed SHA.
   - `alice`'s `push --delete` of it is refused, and the ref still exists.
   - Contributor `bob` (unbound) is refused.
   - Operator `op` (unbound) push and delete both succeed.
   - *Design-check additions (2026-10-06):*
     - **Why AC 8 is not enough.** AC 8 keeps the PoC's *reader* and wires no `GitPushResolver`.
       Its pushes are therefore refused by the role floor or `resolver_unavailable` before any
       store read, so it would still pass with a broken binding. This AC is the real F2
       regression: a contributor, with a store wired.
     - `bob`'s plain push **and** force-push are both refused, and afterwards the server ref
       still equals the reviewed SHA.
     - After the fixture run moves to `committing` (the F2 merge window), `alice`'s push is
       refused too.
10. **The F10 gateway half becomes a regression test.**
    - `service/internal/githttp/ref_admission_tags_test.go` is ported from
      `poc/verify-r2-0/push_phase1_test.go`. It drops the `POC_DIR` gate and uses `t.TempDir()`.
    - **Refused, for anonymous and for contributor pushers:**
      - `<evil>:refs/tags/spine/run/task-001-victim-0a1b2c3d`;
      - `<evil>:refs/tags/main`;
      - `refs/notes/commits`;
      - `refs/spine/run/task-001-victim-0a1b2c3d`.
      - Afterwards `git for-each-ref refs/tags refs/notes refs/spine` on the server is empty.
    - **Admitted:** an operator push of `refs/tags/v1.0.0`.
    - The PoC itself fails at its "tag push denied" fatal once the fix is in (probe-confirmed), and
      passes at `61994fe`.
11. **Divergence refs** (gateway, real git).
    - A push to `refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b1` is refused for the run-level
      actor `alice`.
    - It is admitted for `dave`, who holds the branch's current-step execution.
    - It is admitted for the operator.
12. **`spine/init` and the reserved namespace.**
    - Operator flow:
      - Run `cli.InitRepo` on a clone whose `origin` is the test gateway, with an operator bearer
        in `http.extraHeader` (and with `SPINE_GIT_AUTO_PUSH` unset).
      - Afterwards the server has `refs/heads/spine/init`.
    - The same flow with a contributor bearer leaves the server without it.
    - A contributor push of `refs/heads/spine` and of `refs/heads/spine/other` is refused.
    - A contributor push of `refs/heads/feature/x` succeeds, and a contributor push to `main` is
      still refused with the existing `branch-protection: rule "no-direct-write"` message.
    - *Design-check addition (2026-10-06, DC-1).* Add
      `TestGitPush_CaseFoldVariantOfReservedRefRefused`, using the real handler and real git.
      - **Skip** unless the test's temp directory is case-insensitive. Probe it by creating a
        file and stat'ing its upper-case name.
      - **Setup:** contributor `bob`. The fixture run branch exists.
      - **Push:** an *update* of the `ſpine/…` (U+017F) and `SPINE/…` spellings of the fixture
        branch, with old id = the fixture's current id. Use a hand-built command section, since
        the stock client sends a zero old id for an unadvertised name.
      - **Expected:** both are refused with `ref-admission:`, and the fixture ref is unchanged.
      - It runs on a macOS dev host. On Linux CI it skips, and the AC 2 rows carry
        the guarantee.
13. **The scenario suite** (`go test -tags scenario ./service/scenariotest/...` against the test
    DB, i.e. `make test-scenario` / docker-test).
    - `TestGitHTTP_PushAcceptedWhenFlagOn` passes, pushing `feature/push`.
    - A new `TestGitHTTP_PushReservedRefRejectedAnonymously` passes: in devMode with no actor,
      `spine/test/push` is refused and the server lacks the ref.
    - Every other `TestGitHTTP_*` push scenario passes unmodified. This covers the override, delete,
      contributor-override, mixed and byte-identical scenarios.
14. **Wiring** (`service/cmd/spine`, new `serve_builders_test.go` cases).
    - `buildGitPushResolver(nil, st, ev)` returns `Runs` identical to `st`.
    - With a nil store it returns `Runs == nil` and a permissive policy.
    - `buildGitHTTPHandler` with a nil reader and receive-pack on logs the operator-only warning
      (asserted through a captured `slog` handler).
15. **Unchanged surfaces.** These pass with no edits:
    *Review round 1 note: two edits are sanctioned, both caused by rules this task adds and
    recorded under Deviations — TASK-001's shared `nulHandler` helper (wires a bound reader) and
    `TestHandleCreateBranch_Success` (operator token, OQ6). Their assertions are unchanged.*
    - every existing test in `service/internal/githttp` other than the one in §6;
    - every existing test in `service/internal/gateway` and `core/branchprotect`;
    - in particular `TestServeHTTP_OperatorOverrideAllowsAndEmitsEvent`,
      `TestServeHTTP_ContributorOverrideRejected`,
      `TestServeHTTP_MixedOverridePushEmitsEventOnlyForProtectedRef` and
      `TestHandleGit_PerWorkspacePolicyInvoked`.
16. **Documentation:** §7 items 1–7 are merged in the same PR, and
    `grep -n "bypass policy by design" docs/integration-guide.md` returns nothing.
    - *Design-check addition (2026-10-06):* the new "Pushing a run branch" section of
      `docs/git-push-guide.md` states both of these:
      - the admission-time window (DC-8): a push started while bound may land after the step is
        submitted, bounded by the server `ReadTimeout`;
        *Review round 1 note: that bound is wrong (it covers the upload only); the docs now state
        that the window has no fixed bound (finding R1-2/R1-4).*
      - that the `spine` namespace is reserved under any case-fold spelling (DC-1).
17. **Gates.**
    - `go build ./...`, `go vet ./...` and `go test ./...` are green.
    - `make build`, `./dev/lint-boundaries.sh` and the embed-smoketest are green.
    - Codex review P1/P2 findings are addressed.
    - The mutation checks in AC 3, 5, 7b and 8 are recorded with their outcome in Implementation
      Notes.
    - *Design-check addition (2026-10-06):* so are the AC 2, AC 3 and AC 5 design-check mutation
      checks: `ToLower`-only fold, removed byte loop, "any execution", `repoOrPrimary`, and the
      removed split cap.

---

## Out of scope / follow-ups

- **SMP's gateway.** SMP TASK-005 makes the runner bearer read-only for pushes. SMP TASK-007
  enforces the same ref-class rule, ideally by calling `core/refadmit` (§8).
- **Bare-name resolution in the git CLI client** (TASK-002, root F10 fix). Shadow refs already
  present in deployed repos are covered only by the upgrade note in §9.
- **Merging exactly the validated and reviewed SHA** (TASK-005). Until then, a bound actor,
  including a reviewer bound to a review step, can move the tip after validation.
- **The convergence merge target** (TASK-012, F8 merge half).
- **Git-endpoint auth: trusted-CIDR `RemoteAddr` and devMode** (TASK-015, P3-30).
  - Note for TASK-015: if devMode git access is kept, validate a presented bearer, so a bound actor
    can push in dev.
- **Step-entry controls** (claim eligibility, self-granted skills; TASK-007, TASK-013). A binding
  is only as strong as the rules that let an actor claim the step.
- **Fast-forward-only for bound actors, and a constitutional "no force push" rule.** Neither can be
  enforced per actor at this layer (Decision 5). Both would need a post-unpack hook.
- **Standalone per-execution, ref-scoped job credentials** (an ADR-032 equivalent) would replace
  bearer-plus-binding with a credential that dies at submit.
- **A governance event for operator admissions** (owner question 3).
- **A case-folding audit of `branchprotect` rule matching.** It was not exploitable on git 2.54,
  which has `core.ignorecase` collision refusal. Untested on other git versions.
  - *Design-check note (2026-10-06): this is incorrect for updates.*
    - git refuses only a colliding *create*.
    - On a case-insensitive server filesystem, a case-variant *update* of a protected literal
      branch carrying the current old id lands on the real branch (checked: git 2.54, APFS,
      `refs/heads/MAIN` advanced `main`).
    - Dev-only reach (macOS/Windows hosts, Docker Desktop bind mounts); Linux production is
      unaffected.
    - Not this task's surface, but it needs an owner: see owner question 5 (recommended: TASK-001
      §3).
  - *Correction by TASK-001 (2026-10-07):*
    - The gap is wider than case-variant updates: with `main` **packed** (any
      `git clone --bare` repository, or after `git gc`), even a *create* of `refs/heads/MAIN` lands
      on `main`, `core.ignorecase=true` notwithstanding. `refs/HEADS/main` takes the non-branch
      allow the same way, and the aliasing is not ASCII-only (Kelvin sign, NFD/NFC). See TASK-001
      Design §6.
    - It is **not** owned by TASK-001 §3. Under TASK-001 owner question 1 (default, pending the
      owner) TASK-001 only documents the requirement (`KNOWN-LIMITATIONS.md` → Security Notes:
      served repositories need case- and normalization-sensitive storage), and fail-closed
      handling is a **new INIT-023 EPIC-001 follow-up task**, paired with SMP's follow-up (epic
      Out of scope).
- *Added by the design check (2026-10-06):*
  - **The API write path to the same run branches** (P3-6: `write_context`,
    `POST /artifacts/add`) is TASK-014 §4. It must call this task's predicate (DC-6) so the two
    sinks admit the same actors.
  - **The admission-time window** (DC-8). Write-time enforcement needs a post-unpack hook. The
    content consequence is TASK-005's.
    - *Review round 1:* the window is not bounded by `ReadTimeout` (upload only); git's
      post-upload processing is unbounded. A time bound needs `git-http-backend` run in its own
      process group under a deadline, which would also cut legitimate large pushes and clones
      that outlive `OpTimeout` today. New INIT-023 EPIC-001 follow-up, together with the
      post-unpack re-check.
  - **Exploratory divergence branch creation by any contributor** (owner question 6). It is a
    way to become "bound" to another actor's run. Recommended home: TASK-007.
  - **SMP TASK-007's local `refadmit` copy must mirror DC-1 to DC-6** before it is built (§8
    DC-9). This is a cross-task follow-up for the SMP design, not a change in this repo.

---

## Design check (2026-10-06)

The design was checked adversarially against: the audit report (F2, F8 and F10 sections; P3-6,
P3-30), the PoCs `poc/standalone-service/poc_git_push_runbranch_test.go` and
`poc/verify-r2-0/push_phase1_test.go`, Spine `61994fe`, SMP `80995559`, and the designer's
prototype `_work/task016/`. The probes are in `_work/task016-check/`: `zz_check_test.go`, run as
an external test of `core/domain` via `overlay_check.json`, plus real-git checks on APFS with
git 2.54. Neither repo was modified.

**Verified as stated:**
- every `handler.go`, `handlers_git.go`, `serve_builders.go` and `serve_wiring.go` line reference;
- the engine, domain and store references (`branchname.go`, `run.go:179/245/337`,
  `step.go:482/497-506/562-566`, `claim.go:166-167`, `assign.go:55/66`, `acknowledge.go:68`,
  `actor.go:44`, `permissions.go:35/47`, `pgutil.go:16-21`, `interfaces.go`);
- the divergence ref shapes (`service.go:85/212-221`, `convergence.go:178/241-247`);
- SMP `runstore.go:15-19/113/383/449`, and the lint deny-list (`main.go:54`);
- that `ss.Store` is a true nil interface when a workspace has no DB, so `runReaderOrNil` has no
  typed-nil trap;
- with the prototype overlaid, the only existing unit test that breaks is
  `TestServeHTTP_PreReceiveAllowsSpineRefsWithoutPolicy`. `gateway` and `cmd/spine` stay
  green. The only scenario that changes is `TestGitHTTP_PushAcceptedWhenFlagOn` (`spine/test/push`);
- no Spine-internal component pushes `spine/**` through the gateway: the engine writes the repo
  directly, and `init-repo` pushes to `origin`. So the engine is unaffected;
- devMode reaches admission with a nil actor in production. The scenario tests inject an actor
  through middleware, and that still works.

**Changed, and why:**

| # | Change | Why (evidence) |
|---|---|---|
| DC-1 | Fold the namespace with `strings.EqualFold` as well as `ToLower` | U+017F aliases `spine` on APFS, and updates (not creates) land. The prototype classified it as `branch`, a full bypass on such hosts. |
| DC-2 | New `invalid` class (control bytes), denied for all actors, with a non-echoing message | The prototype classified `refs/heads/spine\x00` as `branch` (D/F DoS through F1 truncation). This removes the hard dependency on TASK-001's ordering. |
| DC-3 | `spine/init/…` is `reserved`, as the text already says | The prototype classified `spine/init/x/y` as `divergence_branch`. |
| DC-4 | `spine/**` short names over 1024 bytes are `reserved` | Bounds lookups and echoed messages. |
| DC-5 | At most 8 `GetBranch` splits per divergence ref | One ~60 KB ref drove 30,000 reads in the prototype (any contributor, any active run). |
| DC-6 | Latest attempt wins, not "some execution" | Parity with TASK-014 §4 (which will call this predicate) and `storeStepAssigner`. |
| DC-7 | Define `repoOrPrimary`, including the gateway's literal `"spine"` | `parseGitPath` yields `"spine"` for `/git/{ws}/spine/…`; executions store `"spine"`. |
| DC-8 | Document the admission-time window (≤ `ReadTimeout`) | `cgi.Handler` ignores the context; TASK-005 is the content control. |
| DC-9 | §8 reflects SMP TASK-007's local-copy choice, which must mirror DC-1 to DC-6 | SMP Decision 1 already took that branch. |
| — | Corrected the "documented way" claim, Decision 7 and the case-fold Out-of-scope bullet | The push guide never mentions run branches. git 2.54 refuses colliding creates only. |
| — | Added owner questions 4 to 6; strengthened AC 2, 3, 5, 6, 9, 12, 16 and 17 | AC 8 alone passes with a broken binding (reader + no resolver). The new rows each fail at the prototype. |

**Not changed (checked, holds):**
- A bypass through a pushed ordinary branch `refs/heads/origin/<x>` has no sink: the standalone
  engine never resolves a bare `origin/<x>`.
- Push cannot create symrefs, and no Spine code creates one under `refs/heads/`.
- Ambiguity between divergence branch IDs is disambiguated by the `DivergenceID` equality.
- D/F blockers (`spine`, `spine/run`, `spine/plan`, `spine/<run-id>`) are all `reserved`.

---

## Implementation Notes

*(Implementation, 2026-10-07. Branch `init/INIT-023-TASK-016-run-branch-push-admission`, cut from
Spine local `main` `ee3c249` (`61994fe` + the INIT-023 scaffold + the merged TASK-001). The
orchestrator named the branch. Run on macOS (APFS, case-insensitive), git 2.54.0, go 1.26.5;
the local PostgreSQL is 17.10, CI uses 18.)*

### Orchestrator rulings (recorded as defaults, pending the owner)

| Question | Ruling | Where |
|---|---|---|
| OQ1 run-branch pushers | Operator/admin always; otherwise the holder of the **latest** run-level execution of `current_step_id` (`assigned`/`in_progress`) on an `active` run, `contributor`+, same repository, create/update only | `core/refadmit` rules 4–7 |
| OQ2 non-branch refs | Operator-only | class `non_branch` |
| OQ3 operator audit | Info log only (`pre-receive admitted reserved ref` / `… non-branch ref`) | `githttp.prereceive` |
| OQ4 review freeze | Adopted for non-operators via an optional `StepDefLookup` hook that fails closed when nil | `refadmit.StepDefLookup`, `FrozenStepType` |
| OQ5 case-insensitive storage in `branchprotect` | Stays with the TASK-001 follow-up; docs only (`KNOWN-LIMITATIONS.md` cross-reference) | — |
| OQ6 divergence `create_branch` | Owned here: workflow-ID grammar (400) and run binding or operator (403) | `divergence.ValidateExploratoryBranchID`, `refadmit.DecideBoundToRun`, `handleCreateBranch` |
| TASK-002 DC-3 / D12 | Created/updated refs with a non-first `refs` or any `HEAD` component are refused for every principal; deletes stay allowed | `Ref.Shadowing`, rule 0b |

### What was built

| Commit | Change |
|---|---|
| `4c414cf` | `core/refadmit` (new; imports `core/domain` + stdlib): `Classify` (7 classes, DC-1…DC-4, `Shadowing` flag), `Evaluator.Decide` (rules 0, 0b, 1–7, DC-5 cap, DC-6, DC-7, memoization, `not_evaluated`), the review freeze, `DecideBoundToRun`, wire messages. `adapters/store` asserts `*PostgresStore` and `Store` satisfy `refadmit.RunReader` |
| `d112201` | `service/internal/githttp`: unconditional admission loop before the policy; `Config.RunRefs`/`StepDefs`; `WithRunRefs`/`WithStepDefs`/`WithRepositoryID`. AC 7 tests; the F10 PoC port (`ref_admission_tags_test.go`); TASK-001's `nulHandler` wires a bound reader |
| `0094d59` | `gateway.GitPushResources.Runs`/`StepDefs`, `handleGit` attaches the repository ID and the per-push pair; `spine serve` wiring (`buildGitPushResolver`, `buildGitHTTPHandler`, `runReaderOrNil`, `stepDefLookupOrNil`). AC 8, 9, 11, 12 (`git_push_admission_test.go`) and AC 14 (`serve_builders_test.go`) |
| `2e68589` | OQ6: `divergence.ValidateExploratoryBranchID` (new, exported, not called by `CreateExploratoryBranch`) and the gateway's grammar + binding checks, with tests |
| `8195fe5` | AC 13: `TestGitHTTP_PushAcceptedWhenFlagOn` pushes `feature/push`; new `TestGitHTTP_PushReservedRefRejectedAnonymously` |
| `08c6c35` | §7 documentation, `KNOWN-LIMITATIONS.md`, `CHANGELOG.md` |
| `ec57c0b`, `9826cdc` | Test fixture order (makes the BranchID-match mutation fail an AC 3 row), and two gocritic fixes |
| `a49d811` | Self-review fix: a nil per-request run reader is a decision. `WithRunRefs` always attaches (nil included), `handleGit` attaches the resolver's pair unconditionally, and githttp never falls back to its `Config` pair once one is attached — otherwise a shared-mode workspace with no database would be admitted against the handler's process-level store (another workspace's). Latent in `spine serve` (its shared-mode process store is nil). Tests: githttp AC 7(d) extension and `TestGitPush_WorkspaceWithoutStoreDoesNotFallBack` |

No migration, table, column, endpoint, event type or environment variable is added (I4). Nothing in
`core/` or `adapters/` calls the new package, so embedded behaviour is unchanged.

### Per-AC evidence

1. **Package.** `go list -deps ./core/refadmit` → only `core/domain` (+ `core/yamlsafe`, a
   `core/domain` dependency) and stdlib. `./dev/lint-boundaries.sh` OK with no allowlist line;
   `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` OK;
   `adapters/store/interfaces_assertions.go` holds `_ refadmit.RunReader = (*PostgresStore)(nil)`
   (and `= Store(nil)`).
2. **`TestClassify`**: every AC 2 row, every design-check row, plus U+0130 (`spİne`, caught by the
   lower-case half of the fold), shadowing rows and further shapes. `TestClassify_ReservedLengthBoundary`
   pins 1024 vs 1025 bytes. Mutations: `ToLower`-only fold, removed byte loop, removed length bound
   all fail (table below).
3. **`TestDecide_RunBranch`**: all allowed/denied rows of AC 3, DC-6 (alice older `in_progress`,
   bob newer `assigned`: alice denied, bob allowed), DC-7 (`"spine"` request; legacy `""` row),
   operators allowed with **zero** reader and step-definition calls (also with a nil reader), the
   `RepositoryBranches` path, the diverged-run row, and the freeze rows. Every AC 3 mutation fails
   at least one row.
4. **`TestDecide_Divergence`**: dave allowed on `x1`; run-level actor, sibling-branch actor,
   completed/failed branch, foreign `RunID`/`DivergenceID`, non-`-div-` segment (0 `GetBranch`),
   unknown branch, moved cursor and delete denied; a `DivergencePath` with an extra `/` resolves.
5. **`TestDecide_FailClosed`**: nil reader (contributor `resolver_unavailable`, operator allowed);
   non-NotFound errors returned (GetRun, ListStepExecutionsByRun, GetBranch) and the evaluator then
   answers `not_evaluated`; 1 denial + 50 foreign refs = 2 reader calls; bound refs memoized (1
   read each). DC-5: a 400-segment divergence ref (under the DC-4 bound) → `unresolved` with ≤ 8
   `GetBranch`; the literal 10,000-segment path → ≤ 8 calls through `findBranch` (see Deviations).
6. **`TestDecide_NonRunClasses`**: init/reserved/non-branch deny contributor, reviewer, reader and
   nil (0 reads) and allow operator and admin, deletes included; `branch` allows everyone
   (`ordinary_branch`); `invalid` denies everyone including operator/admin with 0 reads and a
   message with no byte < 0x20; shadowing names refused for every principal, deletes fall through.
   `TestDecision_Messages` pins the wire catalogue and the 1024-byte echo bound.
7. **githttp**: (a) `TestServeHTTP_SpineRefsSkipPolicyButNotAdmission` replaces
   `TestServeHTTP_PreReceiveAllowsSpineRefsWithoutPolicy`; (b) `…RunsWithoutPolicy` (mutation
   "re-wrap in `if policy != nil`" fails it); (c) `…MixedPushAllOrNothing`; (d)
   `…PerRequestReaderWins` (context reader binds alice, config reader binds bob → admitted, config
   reader not called; per-request reader without per-request lookup → refused; config only →
   refused; a per-request **nil** reader with a binding `Config` reader → refused, no fallback;
   both nil → refused).
8. **`TestGitPush_ReaderCannotPushForeignRunBranch`**: PoC setup kept (reader token,
   `StaticRules(nil)`, no resolver, `spine/run/victim-run`); push and force-push exit non-zero with
   `ref-admission:`; ref = reviewed SHA; `task.md` = `reviewed content`. The original PoC, overlaid
   on this branch, now fails with `push did not land`. "Decide always allows" fails this test and
   AC 9–12 (table below).
9. **`TestGitPush_BoundAndOperatorPushes`**: bob's push and force-push refused, ref unchanged;
   alice's push lands at her SHA; alice's delete refused, ref kept; run `committing` → alice
   refused; operator force-push and delete succeed.
10. **`TestRefAdmission_ShadowTagsRefused`** (`t.TempDir()`, no `POC_DIR`): `refs/tags/<run>`,
    `refs/tags/main`, `refs/notes/commits`, `refs/<run>` refused for anonymous and contributor;
    `for-each-ref refs/tags refs/notes refs/spine` empty; operator `refs/tags/v1.0.0` lands. The
    original PoC, overlaid, fails at its `tag push denied` fatal. Extra:
    `TestRefAdmission_ShadowingNamesRefusedForOperators` (D12, real git; cleanup delete works).
11. **`TestGitPush_DivergenceRefAdmission`**: alice refused, dave admitted (ref = his SHA), operator
    admitted.
12. **`TestGitPush_InitAndReservedNamespace`**: `cli.InitRepo` with a contributor bearer leaves no
    `spine/init`, with an operator bearer creates it; contributor `spine` and `spine/other` refused
    with the reserved-namespace line; `feature/x` lands; `main` refused with `rule
    "no-direct-write"`. **`TestGitPush_CaseFoldVariantOfReservedRefRefused` ran (not skipped) on
    this APFS host**: hand-built updates of `SPINE/…` and `ſpine/…` with the fixture's old id are
    refused for bob and for the bound alice, ref unchanged. With the fold mutated to `ToLower`
    only, the `ſpine` update **moved the real run branch** (`ok refs/heads/ſpine/run/…`), so the
    test is the real DC-1 regression on case-insensitive hosts.
13. **Scenarios** (fresh `s016_scenario`): `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v
    ./service/scenariotest/scenarios/` → 20 `--- PASS: TestGitHTTP_*`, 0 skips, including
    `TestGitHTTP_PushAcceptedWhenFlagOn` (`feature/push`) and the new
    `TestGitHTTP_PushReservedRefRejectedAnonymously`; the other push scenarios unmodified.
14. **Wiring**: `TestBuildGitPushResolver_RunReaderWiring` (single mode: `Runs` identical to the
    store, `StepDefs` the fallback; nil store: `Runs == nil`, permissive policy; shared mode without
    a DB: nil pair, never the process fallback; shared mode with a store and engine: the
    workspace's own pair), `TestStepDefLookupOrNil` (typed-nil orchestrator → nil),
    `TestGitPush_WorkspaceWithoutStoreDoesNotFallBack` (real gateway: a resolver with no `Runs` and a
    handler whose fallback reader would bind alice → alice refused, operator admitted), and
    `TestBuildGitHTTPHandler_WarnsWhenRunBranchesAreOperatorOnly` (captured `slog`).
15. **Unchanged surfaces**: all `core/branchprotect` tests and every listed `githttp`/`gateway` test
    pass unedited, except the edits listed under Deviations (TASK-001's two multi-ref tests via
    their shared helper, and `TestHandleCreateBranch_Success`, both caused by rules this task adds).
16. **Docs**: §7 items 1–7 are in `08c6c35`; `grep -n "bypass policy by design"
    docs/integration-guide.md` returns nothing; `docs/git-push-guide.md` §6 states the
    admission-time window (DC-8, `ReadTimeout`, 30 s in `spine serve` — corrected in review
    round 1: the window has no fixed bound, R1-2/R1-4) and the case-fold reservation (DC-1).
17. **Gates**: below. Codex: see "Codex review".

### Mutation checks (AC 3, 5, 7b, 8, and the design-check mutations)

Run in a scratch copy of the tree (never in the worktree), each mutation applied alone:

| Mutation | Result (first failing test) |
|---|---|
| drop `ActorID` equality | killed — `TestDecide_RunBranch/denied/another_contributor` |
| drop the status filter | killed — `…/denied/execution_waiting` |
| drop the `CurrentStepID` match | killed — `…/denied/run_diverged` |
| drop the `BranchID == ""` match | killed — `…/denied/holder_of_the_current_step_on_a_branch_execution` (after `ec57c0b`) |
| drop the run-`active` check | killed — `…/denied/run_pending` (and every run status) |
| drop the `BranchName` equality | killed — `…/denied/same_suffix,_different_slug` |
| drop the delete guard | killed — `…/denied/alice_deleting` |
| drop the contributor floor | killed — `…/denied/alice_as_reader` |
| drop the repository match | killed — `…/denied/repository_mismatch` |
| move the operator check after the lookup | killed — `…/operators_never_read_the_store` |
| "any execution" instead of latest (DC-6) | killed — `…/DC-6_latest_attempt_wins` |
| `repoOrPrimary` identity / request side unnormalized (DC-7) | killed — `…/allowed` |
| remove `not_evaluated` | killed — `TestDecide_FailClosed/not_evaluated_bounds_reads_after_the_first_denial` |
| remove the split cap (DC-5) | killed — `TestDecide_FailClosed/DC-5_split_cap` |
| `ToLower`-only fold (DC-1) | killed — `TestClassify`; and the real-git `TestGitPush_CaseFoldVariantOfReservedRefRefused` (ref moved) |
| remove the control-byte loop (DC-2) | killed — `TestClassify`, `TestDecide_NonRunClasses/invalid_…` |
| remove the length bound (DC-4) | killed — `TestClassify_ReservedLengthBoundary` |
| rule 0 after the operator check | killed — `TestDecide_NonRunClasses/invalid_refuses_every_principal` |
| shadowing rule removed / applied to deletes | killed — `TestDecide_NonRunClasses/shadowing_…` |
| freeze removed / nil hook not fail-closed (OQ4) | killed — `TestDecide_RunBranch/review_freeze_…` |
| re-wrap admission in `if policy != nil` (AC 7b) | killed — `TestServeHTTP_RefAdmissionRunsWithoutPolicy` |
| `Decide` always allows, types kept (AC 8) | killed — all AC 7 tests, both F10 tests, all five `TestGitPush_*` (AC 8–12), and `TestGitHTTP_PushReservedRefRejectedAnonymously` |
| gateway drops the per-request reader | killed — `TestGitPush_BoundAndOperatorPushes`, `…DivergenceRefAdmission` |
| githttp prefers the config reader | killed — `TestServeHTTP_RefAdmissionPerRequestReaderWins` |
| a nil per-request reader falls back to the config pair (`a49d811` reverted) | killed — `TestServeHTTP_RefAdmissionPerRequestReaderWins`, `TestGitPush_WorkspaceWithoutStoreDoesNotFallBack` |
| OQ6 binding / grammar check removed | killed — `TestHandleCreateBranch_UnboundContributorForbidden` / `…InvalidBranchIDRejected` |
| resolver drops `Runs` (AC 14) | killed — `TestBuildGitPushResolver_RunReaderWiring/single_mode_with_a_store` |

### Deviations and additions (with reasons)

- **The review-freeze hook (OQ4).** `refadmit.StepDefLookup` is an interface with the method
  `LookupStepDef(ctx, runID, stepID) (*domain.StepDefinition, *domain.Run)` — the exact method
  `*engine.Orchestrator` (pinned workflow version) and the gateway's `StepAssigner` already have,
  so TASK-014 can pass `s.assignerFor(ctx)` as is. Installed with `Evaluator.WithStepDefs`. A nil
  hook is a configuration gap and reports `resolver_unavailable` (checked before any store read); a
  nil definition reports the new `step_unresolved`; a frozen step reports the new `review_frozen`.
  The lookup runs only after the binding passes, so an unbound pusher never triggers a workflow
  load. **Additions beyond the ruling's "review step":** (1) `internal` steps freeze too
  (`FrozenStepType`), for parity with TASK-014's amended I6 — `ClaimStep` ignores `execution.mode`
  (P3-15), so a contributor could otherwise claim task-default's `publish` and push between
  approval and merge; (2) the freeze also applies to a divergence branch's current step, for one
  rule across both store-backed classes. Both only refuse more, never less.
- **The DC-3 / D12 rule is a flag plus rule 0b, not a class.** `Classify` keeps the AC 2 classes
  (`refs/heads/refs/heads/x` is still `branch`) and sets `Ref.Shadowing`; `Decide` refuses
  `shadowing_ref` before the operator check unless the update is a delete, which then falls
  through to the class rules (so a contributor may delete `refs/heads/refs/heads/main`, an
  operator `refs/tags/refs/heads/main`). Matching is exact-case, as git's refspec and
  abbreviation matching are, and as SMP E4-TASK-005 §7 specifies.
- **Wiring signatures.** `buildGitPushResolver` gains a fourth parameter (the process-level step
  definition lookup, `orch`), and `buildGitHTTPHandler` gains `runs` and `stepDefs`: the hook needs
  a source in both modes (shared mode uses `ServiceSet.StepAssigner`). `githttp` takes the reader
  and the lookup as a **pair**: once a per-request reader is attached, the per-request lookup (or
  none) is used, never `Config.StepDefs`, so a shared-mode push cannot mix workspaces. A
  per-request reader is "attached" even when nil (`a49d811`): the resolver's "no store for this
  workspace" then means operator-only, never the handler's fallback. `WithStepDefs` ignores nil;
  `runReaderOrNil`/`stepDefLookupOrNil` avoid typed nils.
- **DC-5 test shape.** A 10,000-segment ref is ~20 KB, so DC-4 classifies it `reserved` and `Decide`
  answers `operator_required` with no read at all (asserted). The cap is therefore tested through
  `Decide` with a 400-segment path that stays under 1024 bytes (`unresolved`, ≤ 8 reads) and on the
  literal 10,000-segment path directly against `findBranch` (≤ 8 reads). The cap counts `GetBranch`
  calls (qualifying splits), as AC 5 measures.
- **Echo bound.** Messages echo at most 1024 bytes of a name (rune-safe, `...` appended); this also
  bounds the non-branch and shadowing messages, whose refs DC-4 does not bound.
- **OQ6 details.** The grammar is the workflow ID regex plus a 128-byte cap and the ref-component
  rules git enforces (`..`, trailing `.`, `.lock`), because the regex alone admits names git then
  refuses with a 500. "Bound to the run" is `refadmit.DecideBoundToRun`: operator, or a `contributor`+
  actor holding the latest live execution of the run-level current step or of an open divergence
  branch's current step, on an `active` run (≤ 8 branch reads). A nil actor (dev mode only) skips the
  binding, as the step-submit ownership check does. Consequence, documented in
  `KNOWN-LIMITATIONS.md` and `CHANGELOG.md`: an exploratory divergence leaves its run with no live
  cursor (and the API creates no step execution for the new branch), so creating exploratory
  branches is effectively operator-only. *Superseded in review round 1 (finding R1-1):
  `DecideBoundToRun` also binds the actor who submitted the step that triggered the divergence,
  so the rule is reachable for a contributor.* Validation lives in the gateway (`service/`); the new
  `divergence.ValidateExploratoryBranchID` is exported but `CreateExploratoryBranch` does not call
  it, so embedders and SMP (which never calls it) are unchanged.
- **Existing tests edited (AC 15).** (1) TASK-001's `nulHandler` (shared by its tests) now wires a
  reader binding `actor-contributor` to `run-abcd1234`, because
  `TestPreReceive_WellFormedMultiRefPushStillApplies` and `TestPreReceive_RealGitClientPushesUnchanged`
  push `spine/run/task-001-abcd1234` as a contributor and expect it to land; their assertions are
  unchanged (two doc comments updated). These tests did not exist at `61994fe`, which is why §6
  missed them. (2) `TestHandleCreateBranch_Success` now uses the operator token: its unbound
  contributor is refused by OQ6 on purpose (new `…UnboundContributorForbidden` pins that).
- **Logging.** Refused refs are logged at Warn with `class`, `reason`, `actor_id` and `run_id`;
  slog quotes values with control bytes, so the ref is passed raw rather than pre-`%q`-quoted
  (double quoting otherwise). Operator admissions of **non-branch** refs are also logged at Info
  (OQ3 extension), and they still reach the policy.
- **Docs.** `architecture/git-integration.md`'s divergence examples used a divergence ID without the
  engine's `<run-id>-div-` prefix; corrected, since admission resolves exactly that shape. The
  upgrade note covers doubly-qualified and `HEAD`-named refs (TASK-002 cross-task requirement).

### Notes for dependent tasks

- **TASK-014:** call `refadmit.NewEvaluator(st).WithStepDefs(s.assignerFor(ctx)).Decide(ctx,
  refadmit.Request{Ref: "refs/heads/" + run.BranchName, Actor: actor})` — without `WithStepDefs` every
  non-operator is refused. The push path exempts operators from the freeze; TASK-014's API freeze
  (every role) stays its own. `refadmit.FrozenStepType` is the shared step-type set.
- **SMP TASK-007:** mirror DC-1…DC-7, D12 (rule 0b), the freeze (review + internal, fail closed) and
  `DecideBoundToRun` if it adopts OQ6. `spinecore.RunStore` satisfies `refadmit.RunReader` as is
  (overlay compile, below); its `ErrRunNotFound` needs mapping to `domain.ErrNotFound` to read as
  `unresolved` instead of an evaluation error.
- **TASK-015:** devMode pushes reach admission with no actor; validating a presented bearer there
  would let bound actors push in dev.

### Gates (commands and results; final code at `a49d811`)

All rows below were (re-)run on `a49d811`, except the full-scenario, store-integration and mutation
rows, which ran on `9826cdc`; `a49d811` changes only `githttp`/`gateway` admission wiring and
tests, so those rows are unaffected (its own mutation row is in the table above).

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | all packages `ok`, no failures |
| `go test -race -p 4 -count=1 ./core/refadmit ./adapters/store ./core/divergence ./service/internal/githttp ./service/internal/gateway ./service/cmd/spine` | all `ok`, no races |
| `SPINE_TEST_DATABASE_URL=…/s016_scenario go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (fresh DB) | 20 PASS, 0 SKIP |
| `SPINE_TEST_DATABASE_URL=…/s016_integration go test -tags integration -count=1 ./adapters/store` (fresh DB) | 1 failure, `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, `workflow_version_label`) — **identical on `main`** (`git archive main`, fresh DB); `adapters/store` changed only by a compile-time assertion |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` (fresh DB) | 30 failures, the **same 30 as `main`** on its own fresh DB (`TestSchemaMatchesProduction`, fixture validation, …: the pre-existing set TASK-001 recorded); every `TestGitHTTP_*` passes |
| `golangci-lint run --build-tags scenario --new-from-rev main <each touched package>` | `0 issues` for each of `core/refadmit`, `adapters/store`, `core/divergence`, `githttp`, `gateway`, `cmd/spine`, `scenarios` |
| `golangci-lint run --build-tags scenario <package>` per package, branch vs `main` | `core/refadmit` 0 (new); store 5 = 5; divergence 4 = 4; githttp 19 < 20; gateway 89 < 90; cmd/spine 10 < 11; scenarios 60 = 60 (no new issue anywhere) |
| `golangci-lint run --enable-only=gosec` on the same packages | 0 issues, branch and `main` |
| `git diff --name-only main...HEAD \| grep '\.go$' \| xargs gofmt -l` | empty |
| Mutation checks | 31 mutant runs, all killed (table above; two first drafts that did not compile were redone as compiling mutants) |
| PoCs overlaid (`go test -overlay`) | `poc_git_push_runbranch_test.go`: fails with `push did not land`; `push_phase1_test.go` (`POC_DIR` set): fails at `tag push denied` |

### Codex review

| Round | Code | Command | Result |
|---|---|---|---|
| 1 | `08c6c35` | `codex review --base main` | "No actionable regressions were identified." (its sandbox could not bind sockets, so it ran only the socket-free tests) |
| 2 | `9826cdc` | same | first attempt aborted ("Selected model is at capacity"); the retry: "No actionable regressions were identified in the admission logic or gateway wiring." |
| 3 | `a49d811` | same | "No actionable defects were identified in the changes." |

No P1/P2 was reported, so none was fixed or rejected. The one defect fixed after review (`a49d811`)
came from self-review.

### SMP compatibility gate

Re-run on `a49d811` with the same results.

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T016/smp`, detached at `7d4e7cc9`, no `vendor/`
or `tmp/`; `go list -m` resolves `github.com/bszymi/spine => …/i23/s-T016/spine`. Not modified
(`git status` clean after every step).

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...` | OK |
| `go vet ./...`; `go vet -tags integration ./...` | clean |
| `go test -p 4 -count=1 <31 packages>` — every SMP package whose dependency graph contains `adapters/store` or `core/divergence` (`go list -f '{{.ImportPath}} {{join .Deps " "}}' ./...`; direct importers: `cmd/customer-app`, `eventdelivery`, `projectionwriter`, `spinecore`, `stepevents`, `workspace`) | 30 `ok`, 1 `[no test files]` (`workspace/faultharness`) |
| `./dev/lint-spine-internals.sh` | `lint-spine-internals: OK` |
| `go vet -overlay` with `var _ refadmit.RunReader = (*RunStore)(nil)` in `spinecore` | compiles: SMP's run store satisfies the reader unchanged |

Every Spine change reaching SMP is provably identical for SMP: `core/refadmit` is new and called by
nothing in `core/`/`adapters/`; `adapters/store` gains a compile-time assertion only;
`core/divergence` gains an exported function nothing calls (SMP never calls
`CreateExploratoryBranch`); everything else is under `service/`, which SMP cannot import.

### Not run

- **SMP's gated DB suites** (`go run ./dev/testgate -- -race -count=1 … ./internal/customer/workspace/
  ./internal/customer/eventdelivery/ ./internal/customer/projectionwriter/ …`, as in SMP `ci.yml`'s
  `integration` job): the artifact names none (§8: no SMP change), and the job needs its
  provisioned roles, `smp_prov` and MinIO. The changed Spine code is not on any path those suites
  execute (see above).
- **CI on GitHub** (including the `scenario-git-http` job on PostgreSQL 18): needs a push, which the
  orchestrator owns.
- **The Linux skip path of `TestGitPush_CaseFoldVariantOfReservedRefRefused`**: this host is
  case-insensitive, so the test ran in full; on ext4 it skips by design and the `TestClassify` rows
  carry the guarantee.

### Review round 1 (2026-10-07)

Three reviewers (correctness and SMP compatibility, adversarial security, completeness and tests)
examined `3cbcbae`. Each finding was checked against the code before acting on it. Findings are
numbered R1-1 … R1-9 in the order they were reported.

**Per-finding disposition**

| # | Sev. | Finding | Disposition |
|---|---|---|---|
| R1-1 | P3 | The OQ6 binding (`DecideBoundToRun`) can never be met by a contributor on an exploratory divergence, the only kind `create_branch` accepts; `TestHandleCreateBranch_BoundContributorAllowed` tested a state the engine never produces | **Fixed** (`4d23317`). Verified: `startDivergence` completes the triggering execution, sets the cursor to `divergence:<id>` (`core/engine/step.go:482`), and creates executions only for structured branches; `CreateExploratoryBranch` creates none. Of the reviewer's two options, the one closer to the ruling ("bound to the run, or an operator") was taken: while the active run's cursor is `divergence:<id>`, the actor of the **latest run-level execution, which must be `completed`** (the submitted trigger) is bound (`ReasonDivergenceTrigger`). A live, failed or newer attempt in that slot refuses. `Decide` still requires a live execution, so the rule admits no push. Gateway test rewritten to the engine's real state (`TestHandleCreateBranch_DivergenceTriggerHolderAllowed`); new `TestDecideBoundToRun_DivergenceTrigger`. KNOWN-LIMITATIONS, CHANGELOG and api-operations updated. SMP never calls the endpoint or the predicate. *Pending the owner, like the OQ6 ruling itself: the alternative is to accept operator-only.* |
| R1-2 | P3 | The DC-8 window is documented as bounded by `ReadTimeout`; that covers the upload only | **Fixed** (`aa4ab61`), with R1-4. |
| R1-3 | P3 | `spine init-repo` with a non-operator bearer is refused by admission, but prints only "remote may not be configured" and exits 0; AC 12 locked that in | **Fixed** (`35dcb38`). `pushFailureNote` quotes only the server's own `remote: ref-admission:` / `remote: branch-protection:` lines (never the rest of git's output, which can name the remote URL; non-printable bytes dropped) and the operator-token remedy. Other failures keep the old note; exit code unchanged (0), as for any push failure. `TestPushFailureNote`; the AC 12 gateway test now asserts the contributor's stderr and that the operator's run prints no note. Push-guide row extended. |
| R1-4 | P2 | Same window: nothing bounds git's post-upload processing (index-pack, connectivity check), so the binding and the OQ4 freeze are push-start properties only | **Docs fixed** (`aa4ab61`): git-push-guide §6, KNOWN-LIMITATIONS and security-model §7.3 now say the window has no fixed bound, that the freeze is checked at the same moment, and that TASK-005 is the content control. **Code bound not done — closest safe alternative, recorded:** `net/http/cgi` exposes no context or process-group control, and its `Process.Kill` of `git-http-backend` does not reach the `receive-pack` child (git cleans up children on catchable signals only), so a real bound needs a CGI host that runs `git-http-backend` in its own process group under a deadline. Enforcing `opTimeout` (30 s) that way would also cut legitimate large pushes **and clones**, which outlive it today. `receive.maxInputSize` would cap pack size repo-wide, operators included. All three are behaviour changes for an owner decision, so they are a new INIT-023 EPIC-001 follow-up (with the post-unpack re-check, Decision 5); DC-8 itself said "not required: any code change". No test pins a bound because none is claimed. |
| R1-5 | P3 | `findBranch` was not memoized and the parser does not dedupe, so repeating an admitted divergence ref cost one `GetBranch` each (1,000 repeats → 1,000 reads, reproduced) | **Fixed** (`cb8c8b8`). `findBranch` memoizes found and not-found by (run ID, divergence path); store errors are not cached. New subtest: 1,000 repeats → 1 `GetBranch` (and 1 of each other read), plus a two-split hit and a miss looked up once each. |
| R1-6 | P2 | AC 7(a) was vacuous: `*` never matches `spine/run/abc`, so deleting the `continue` at `handler.go:488` went unnoticed | **Fixed** (`891450d`). Rules `spine` … `spine/*/*/*/*` (no-direct-write + no-delete), asserted to deny each ref directly; a counting policy must see zero evaluations for operator writes and deletes of run, plan, divergence, init and reserved refs; `main` is the live control. The `continue` mutation is now killed. |
| R1-7 | P3 | No test covered the gateway passing the URL repository ID (§3 item 2, DC-7) | **Fixed** (`020cc4a`). `TestGitPush_RepositoryScopedBinding`: a code repository `payments` through a `gitpool` with a stub registry; carol (current step on `payments`) admitted at `/git/ws1/payments`, refused at `/git/ws1` and `/git/ws1/spine`; retargeted at the primary she is admitted at `/git/ws1/spine` and refused at `/payments`. The no-op mutation is killed. |
| R1-8 | P3 | No test covered `spine serve` wiring the freeze lookup into the push resolver; the startup warning inspected only the handler's fallback pair and fired on every shared-mode start | **Fixed** (`d816aba`). `TestBuildServerConfig_GitPushResolverWiresAdmission` drives `buildServerConfig` (single mode) and checks the resolver's pair is the store and the orchestrator; the `nil` mutation is killed. `buildGitHTTPHandler` gains `perWorkspace` (`deps.WSServicePool != nil`): shared mode logs an Info line instead of the fallback-pair warning; single mode keeps the warning, which there describes exactly what the resolver attaches. |
| R1-9 | P3 | `DecideBoundToRun`'s branch-record checks (`b.RunID`, `b.CurrentStepID`) were untested | **Fixed** (`54eea8b`). Two rows; each condition removed alone is killed. |

**Acceptance-criteria gaps**

- **AC 15 ("pass with no edits")** — not a defect: both edits (TASK-001's `nulHandler`,
  `TestHandleCreateBranch_Success`) were already recorded under Deviations and follow from this
  task's rules; their assertions are unchanged. The AC text is now annotated in place.
- **AC 16 / DC-8 (`ReadTimeout` bound)** — R1-2/R1-4: the premise was wrong; DC-8 and AC 16 are
  annotated in place and the documents corrected.
- **AC 7(a) (`*` rule)** — R1-6; AC 7(a) annotated in place.
- The reviewers' "no P1/P2", "verified as holding" and "verified as claimed" notes need no action.
  `TestHandleCreateBranch_Success` stays on the operator token (R1-1 does not change that its
  unbound contributor is refused).

**Mutation checks (review round 1)** — scratch copy of the tree, each mutation alone:

| Mutation | Result (first failing test) |
|---|---|
| `findBranch` memo removed (R1-5) | killed — `TestDecide_FailClosed/bound_actor's_repeated_divergence_refs_are_memoized` |
| divergence-trigger rule removed (R1-1) | killed — `TestDecideBoundToRun_DivergenceTrigger`; gateway `TestHandleCreateBranch_DivergenceTriggerHolderAllowed` |
| any completed run-level execution instead of the latest (R1-1) | killed — `TestDecideBoundToRun_DivergenceTrigger` (carol row) |
| trigger status check dropped (R1-1) | killed — `TestDecideBoundToRun`, `…_DivergenceTrigger` |
| trigger rule applied to an undiverged run (R1-1) | killed — `TestDecideBoundToRun_DivergenceTrigger` |
| `DecideBoundToRun` drops `b.RunID == run.RunID` (R1-9) | killed — `TestDecideBoundToRun` |
| `DecideBoundToRun` drops `b.CurrentStepID == k.step` (R1-9) | killed — `TestDecideBoundToRun` |
| admitted `spine/**` refs fall through to the policy (R1-6) | killed — `TestServeHTTP_SpineRefsSkipPolicyButNotAdmission` |
| gateway drops `WithRepositoryID` (R1-7) | killed — `TestGitPush_RepositoryScopedBinding` |
| `buildServerConfig` passes `nil` lookup to the resolver (R1-8) | killed — `TestBuildServerConfig_GitPushResolverWiresAdmission` |
| shared-mode fallback warning not suppressed (R1-8) | killed — `TestBuildGitHTTPHandler_WarnsWhenRunBranchesAreOperatorOnly` |
| init-repo generic note restored (R1-3) | killed — `TestGitPush_InitAndReservedNamespace` |
| `pushFailureNote` quotes all of git's output (R1-3) | killed — `TestPushFailureNote` |

14 mutant runs, all killed.

**Gates (review round 1; code at `aa4ab61`)**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | 41 packages: 39 `ok`, 2 no test files, no failures |
| `go test -race -p 4 -count=1 ./core/refadmit ./service/internal/githttp ./service/internal/gateway ./service/cmd/spine ./service/internal/cli` | all `ok`, no races |
| `SPINE_TEST_DATABASE_URL=…/s016_r1scen go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (fresh DB, dropped after) | 20 PASS, 0 SKIP |
| `SPINE_TEST_DATABASE_URL=…/s016_r1int go test -tags integration -p 4 -count=1` on the touched packages and `core/divergence` (fresh DB, dropped after) | all `ok` (none of this round's packages has integration-only tests; `adapters/store` is untouched this round) |
| `golangci-lint run --build-tags scenario --new-from-rev main` on `core/refadmit`, `githttp`, `gateway`, `cmd/spine`, `cli` | `0 issues` each |
| `golangci-lint run --build-tags scenario`, branch vs `main` (`git archive main`) | `cli` 1 = 1; `cmd/spine` 10 < 11; `gateway` 89 < 90; `githttp` 19 < 20; `core/refadmit` 0 (new) |
| `golangci-lint run --enable-only=gosec` on the same packages | 0 issues |
| `git diff --name-only main...HEAD \| grep '\.go$' \| xargs gofmt -l` | empty |
| SMP: `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | OK, clean, clean |
| SMP: `go test -p 4 -count=1` on the 31 packages whose dependencies include `adapters/store`, `core/divergence` or `core/refadmit` | 30 `ok`, 1 no test files |
| SMP: `./dev/lint-spine-internals.sh`; overlay `var _ refadmit.RunReader = (*RunStore)(nil)` | OK; compiles. SMP checkout unmodified |
| `codex review --base main` (round 4, on `aa4ab61`) | "No actionable regressions were identified in the admission logic or gateway wiring." (its sandbox could not open listening sockets, so it ran only the socket-free tests). No P1/P2 reported, so none fixed or rejected |

**Commits (review round 1):** `cb8c8b8` (R1-5), `54eea8b` (R1-9), `4d23317` (R1-1), `891450d`
(R1-6), `020cc4a` (R1-7), `d816aba` (R1-8), `35dcb38` (R1-3), `aa4ab61` (R1-2/R1-4 docs and this
round's doc updates), then this note. No migration, table, column, endpoint, event type or
environment variable is added. The only change reaching SMP's dependency graph is inside
`core/refadmit` (memo, the `DecideBoundToRun` trigger rule, a new `Reason` constant), which nothing
in `core/` or `adapters/` calls and SMP does not call, so SMP behaviour is identical for every input.

**Follow-ups opened by this round** (for the epic's follow-up list): bound the git CGI child
(process group + deadline) or add a post-unpack re-check, with an owner decision on large
pushes/clones and `receive.maxInputSize` (R1-4); the R1-1 trigger rule is pending the owner with
the OQ6 ruling (alternative: operator-only).
