# Known Limitations — Spine v0.x

This document tracks known gaps and deferred functionality.

## Remaining Limitations

### WriteContext / Branch Writes
- `write_context` field is now parsed from API requests and attached to context
- Artifact service `enterBranch()` method uses write context for Git branch operations
- **Remaining gap:** Automatic write context injection for step execution (actor writes within a run don't automatically get the run branch as write context)

### Idempotency-Key
- Header is accepted on write operations
- In-memory queue deduplicates via idempotency key set
- **Remaining gap:** No persistent deduplication store — duplicate requests survive restarts

### Queue Consumer Delivery
- Consumer framework exists and delivers to registered ActorProviders
- Mock provider works for testing
- **Remaining gap:** No production providers for human (email/webhook) or AI (Anthropic/OpenAI) actors

### Discussion and Comments
- Architecture fully designed (discussion-model.md)
- Zero implementation — planned feature
- Thread creation, comments, resolution not yet built

### Webhook Delivery (after INIT-023 TASK-010)
- **v1 signatures stay replayable.** v1 (the default) signs the body only. Opt a subscription
  into v2 (`metadata.signature_version: "v2"`, integration guide §6.1) for a timestamp and
  single-use nonce inside the MAC. Making v2 the default for new subscriptions is an open
  owner question.
- **The subscription test ping is unsigned**, so a receiver that enforces signatures rejects
  it. Follow-up: sign it in the subscription's version.
- **Redirects are followed with the signature headers.** Each hop is re-validated, but Go's
  client copies the headers (a fresh v2 signature and nonce included) to the redirect target.
  Not changed here: refusing redirects would dead-letter receivers that rely on an
  http→https or trailing-slash redirect today.
- **A slow endpoint throttles its workspace.** The dispatcher waits for a whole batch
  (default 5 deliveries) before claiming again, so an endpoint that answers just under the
  10 s timeout — even with a success — limits every subscription in the workspace to about
  5 deliveries per 10 s. The fair claim does not help; per-subscription in-flight limits are
  the follow-up.
- **A replica-local fault holds a subscription for every replica.** The circuit breaker is in
  memory per process. With several dispatchers on one runtime database (SMP replicas), a
  replica whose circuit for a subscription is open, or which cannot read the subscription (for
  example one started without its encryption key), holds that subscription's due queue in the
  database until its next attempt, up to 60 s per cycle; rows that arrive between holds are
  still claimable by the healthy replica. A shared (database-backed) breaker would remove this.
- **The start of a hold is kept in memory.** `MaxCircuitHold` (24 h) counts from when the
  dispatcher process saw the circuit open, or first failed to read the subscription, so a
  restart (or a replica that has just started) starts it again. A subscription that stays
  unreadable while its dispatcher restarts more often than every 24 hours never has its rows
  dead-lettered by the cap: they stay `failed` (at the attempt budget, so SMP's run banner
  shows them) and are retried every minute. A "held since" column (a schema change) would
  remove this.
- **A refused target URL dead-letters the whole due queue.** A permanent verdict on a
  subscription (deleted, invalid metadata, refused URL, unusable TLS or signing configuration)
  dead-letters every delivery of it that is due, not only the one being tried. A host name is
  refused on what it resolves to at that moment, so a DNS answer that points at a non-public
  address even briefly costs the subscription its due deliveries; they stay replayable.
- **Re-holding a very large backlog is one large write per probe cycle.** When an open circuit's
  probe time arrives, the whole held backlog becomes due and the first hold after the probe
  re-schedules it in one statement: about 70 ms for 2 000 rows and 1.6 s for 100 000 rows on a
  loaded development machine (PostgreSQL 17), once a minute, inside one poll.
- **A lost status write strands a row in `delivering`.** Without a `claimed_at` column (a
  schema change) such a row is never re-claimed.
- **NAT64 with a network-specific prefix** (RFC 6052 §2.2) cannot be recognised from the
  address; egress policy is the control there.
- **Rows written before TASK-010 keep their unredacted `last_error`** until retention deletes
  them; the task's Implementation Notes give a one-off `UPDATE` an operator can run.
- **Operator replay keeps `attempt_count`**, so a replayed exhausted delivery is
  dead-lettered by its first failure. The response body of a delivery is drained without a
  size cap (bounded only by the 10 s client timeout).

### Git Push: Bodies Larger Than the Client's `http.postBuffer` Are Refused
- With `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, a push whose request body is larger than the client's
  `http.postBuffer` (1 MiB by default) fails with `error: RPC failed; HTTP 400`. Nothing is
  applied, and no branch-protection rule is involved.
- **Why:** git sends such a body with `Transfer-Encoding: chunked`. The bridge to
  `git-http-backend` (Go's `net/http/cgi`) refuses every chunked request body ("Chunked request
  bodies are not supported by CGI"), because CGI needs a `CONTENT_LENGTH`.
- **Workaround:** raise the client's buffer above the size of the push, for example
  `git config http.postBuffer 157286400` (150 MiB) in the clone, or
  `git -c http.postBuffer=157286400 push …`. git then sends one body with a `Content-Length` and the
  push applies. The client holds that body in memory.
- **Scope:** clone and fetch send small request bodies (want/have lines) and are normally
  unaffected. SMP's gateway spools a chunked push body to disk before CGI and accepts these
  pushes.
- **Status:** this predates INIT-023 and is pinned by `TestHandler_ChunkedPushBodyRefusedByCGI`
  (`service/internal/githttp`). Spooling chunked bodies before CGI, with a size cap, is an
  INIT-023 EPIC-001 follow-up (owner: bszymi).

### Git Push: Run-Branch Admission Is a Push-Start Check
- With `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, a run, planning or divergence branch is pushable only
  by the actor bound to its current step, or an operator (INIT-023 TASK-016). Three limits remain:
  - **Admission time, not write time.** The gate decides when the push's command section arrives;
    git writes the ref only after the pack has been received and processed. A push started while
    bound can land after the actor submits the step. The window is the upload (at most the HTTP
    `ReadTimeout`, 30 s in `spine serve`) **plus** git's processing of the received pack (index,
    delta resolution, connectivity check), which nothing bounds: `net/http/cgi` ignores the request
    context and does not bound `git-http-backend`, and the pusher influences the processing time
    through the pack it sends. The review freeze is checked at the same moment, so a push started
    before the step was submitted can still land during review. Binding the governed merge to the
    reviewed commit (INIT-023 TASK-005; always on in `spine serve`) is the content control for that
    window: a push that lands after the step's submit bound the head is not merged, and an approval
    of a branch that moved since is refused (`409 run_branch_moved_since_gate`); an embedder that
    does not enable merge binding keeps the gap. A real bound needs `git-http-backend`
    run in its own process group under a deadline (a deadline would also cut legitimate large
    pushes and clones, which today outlive `OpTimeout`), or a post-unpack re-check; both are
    INIT-023 EPIC-001 follow-ups.
  - **Force-push by the bound actor is allowed.** Fast-forward-only cannot be enforced per actor
    before the pack is unpacked, and `receive.denyNonFastForwards` would also block operators.
  - ~~**Dev mode pushes are anonymous.**~~ Resolved by INIT-023 TASK-015: `SPINE_DEV_MODE` no
    longer affects `/git`, and every push presents a validated bearer, so the gate always sees an
    actor.
- **Exploratory divergence branches: creation is bound to the run, their refs are operator-only.**
  Creating one (`POST /runs/{run_id}/divergences/{divergence_id}/branches`) needs an operator, or
  the actor who submitted the run-level step that triggered the divergence (while the run's cursor
  is `divergence:<id>`, the engine's state for an open exploratory window), or an actor holding a
  live cursor of the run. The API creates no step execution for the new branch, so nobody below
  operator can then push its divergence ref (pushes need a live execution). Wiring step
  executions for exploratory branches is a follow-up (TASK-016 owner question 6).
- **Case-insensitive storage.** The admission folds the `spine` namespace with Unicode simple case
  folding, so case-variant spellings are operator-only on any filesystem. Branch-protection rule
  matching does not fold; see "Served Repositories Need Case- and Normalization-Sensitive Storage"
  below.

### Run-Branch API Writes and Workflow Governance: What INIT-023 TASK-014 Leaves Open
- On the standalone gateway, an API write onto a run branch (`write_context` with a `run_id`,
  `POST /artifacts/add`) is accepted only from the actor bound to the run's current step or an
  operator, never during a `review`/`internal` step; reviewer workflow writes go through a
  `workflow-lifecycle` run; `workflow.Service` consults branch protection (audit F7, P3-6). Limits:
  - **Claiming a never-held step is open.** The binding follows whoever holds the step. A step
    that was never held in its run (the first attempt — the `draft` step right after a planning or
    workflow run starts, owner question 4: no auto-bind at start) can be claimed by any eligible
    actor, who can then write the branch as its bound actor. That takeover is attributed
    (`step_executions.actor_id`, the claim row, a `step_assigned` event), the starter is locked
    out, and the content still has to pass a frozen review that, under the strict step-entry
    policy, its author cannot claim. Under that policy a *reworked* or restart-recovered author
    step is re-offered only to its prior holder (`ReofferToPriorHolder`); under `legacy` and in
    the embedded zero value it is open too. A step its holder **released** — a first attempt or a
    reworked one — is claimable by others (release is the holder's own hand-off; it stays
    attributed through the cancelled claim row, which is how the engine tells a released attempt
    from a fresh one). A retry whose preceding attempt failed is not re-offered, nor is an attempt
    whose prior holder the engine's own claim checks would refuse: the holder no longer resolves,
    is not `active`, is off the execution's `eligible_actor_ids`, is of a type the step does not
    admit, or lacks one of its `required_skills`. The host's authorization is not among those
    checks: the engine cannot see it, and judging it from the actor's role would lift every
    re-offer for an embedder whose resolver leaves the role empty. So a prior holder whose role an
    admin lowers below `spine serve`'s `execution.claim` (contributor) keeps the rework pinned
    until the role is restored or an operator assigns it. With `EnforceExecutionMode` on,
    `automated_only` / `ai_only` steps are never re-offered (their claimers are
    operator-provisioned pool members). A reworked `hybrid` / `human_only` / unmoded author step
    whose prior holder is still `active` but no longer working on it (a person who left the work,
    a pooled agent identity that was scaled down) waits for that holder or an operator assign
    (`POST /runs/{run_id}/steps/{step_id}/assign`); it is hidden from everyone else's candidate
    listing.
  - **Time-of-check gap.** The run and its executions are read once, before the write. Only the
    bound actor passes, so the gap is that actor racing their own `step.submit` with a write (one
    request's duration). Binding the governed merge to the reviewed commit (INIT-023 TASK-005) is
    the backstop.
  - **An idle lifecycle run holds its workflow.** Only one non-terminal lifecycle run may govern a
    workflow (`409` names it). A reviewer who opens one and never submits blocks other reviewers'
    edits of that workflow: `workflow-lifecycle`'s `draft` step has no timeout (only `review` has
    `168h`). Recovery is an operator cancel or the operator bypass. Changing the timeout is a
    governed workflow edit. Two first requests racing within milliseconds can both start a run;
    the merge-conflict path is the backstop.
  - **Runs left behind by failed workflow starts before the upgrade hold their workflow.** A run
    a failed start left `pending` (branch deleted; scheduler recovery may since have made it
    `active`) never finishes, so the one-open-run `409` names it on every reviewer edit of that
    workflow until an operator cancels it. New failures leave the run `failed`. The CHANGELOG
    upgrade note has the query to find such runs.
  - **A step routed to a code repository writes no primary run branch through the API.** API
    writes land on the run's branch in the primary repository; ADR-015 has a step write only its
    own repository, so while the current step is routed elsewhere (`repository: <id>`) only an
    operator may write that branch, the step's holder included (`409`, `detail.repository_id`), as
    for a push. A workflow whose code step must also update the task artifact does it in a step
    routed to the primary repository.
  - **Workflow edits by push are not confined to the lifecycle run.** I7 ("a workflow edit rides
    only its own lifecycle run") and the V2 closure hold on the API path only. Push admission
    (INIT-023 TASK-016) admits the bound actor by binding alone and the pre-receive gate checks no
    paths, so the actor bound to a task run's current step can push a commit that changes
    `workflows/**` (or any other governed path) onto that run branch, and the governed merge
    publishes it under that run's ordinary review, not `workflow_review`. Owner: INIT-023 TASK-006
    (governance path guard).
  - **The store-fallback step lookup is not version-pinned.** With an orchestrator wired the
    review freeze reads the run's pinned workflow; the engine-less fallback (no run engine at all)
    reads the current projection. A missing step still refuses.
  - **F7's "no second reviewer" clause** is closed only with TASK-007's separation of duties
    (strict in `spine serve`) and TASK-013's admin-only skills: under `legacy`, one reviewer can
    claim and submit `draft` and then claim and approve `review` of the same update run.
  - **SMP's own API writes onto planning branches** (`AddToRun`, `WriteContent`) are outside this
    task (SMP TASK-007 or a new SMP task).

### Step-Entry Checks Are Opt-In for Embedders (and Off Under `legacy`)
- The engine's step-entry policy (`engine.StepEntryPolicy`, INIT-023 TASK-007) closes audit
  P3-3 (assign checks no actor constraints), P3-4 (precondition-failed steps stay bindable; two
  evaluator checks fail open), P3-15 (claim ignores `execution.mode`) and P3-16 (entry points
  ignore run status; cancel leaves steps live), and adds separation of duties.
- **The zero value is the pre-INIT-023 behaviour.** An embedder that has not called
  `WithStepEntryPolicy` (SMP until its TASK-011), and `spine serve` with
  `SPINE_STEP_ENTRY_POLICY=legacy`, still has all four findings open and no separation of duties.
  `spine serve` defaults to the strict policy; `legacy` is a logged escape hatch for operators
  migrating custom workflows and will be removed.
- **Separation-of-duties residual (accepted, Owner question 6):** `AssignStep` writes no
  assignment row, so if restart recovery resets an assign-bound execution and a different actor is
  bound, the first actor leaves no record and is not treated as a run author. Closing it needs an
  append-only binding ledger (a schema change; follow-up).
- **Separation of duties sees bindings, not git authorship.** It cannot tell who pushed which
  commit. INIT-023 TASK-016 admits a non-operator's push to a run or divergence branch only from
  the actor bound to the branch's current step, and only while that step is not a `review` or
  `internal` step; every other step type (`manual`, `automated`, `convergence`, or a step with no
  or an unknown `type`) makes its bound actor a run author. So every non-operator who could have
  pushed is in the author ledger, apart from the assign-reset residual above. Two gaps remain:
  - **Operators push unbound and are invisible to SoD.** Operators and admins may push any run
    branch without being bound (TASK-016 owner question 1), and such a push makes them no run
    author: an operator who pushes commits to a run branch can then be bound to its review. That
    is within operator authority in standalone; where it matters, compare the run branch's commit
    authors with the reviewer before accepting.
  - **A push admitted while bound can land after the step is submitted** (see "Run-Branch
    Admission Is a Push-Start Check"); its actor still counts as an author, because it was bound.
- **Separation-of-duties bind race across processes:** the SoD check reads state and the bind
  writes it later. Within one process the engine serialises a run's check-and-bind (an in-process
  lock shared by every orchestrator, held only while the check is re-run and the bind written), so
  one actor cannot win a live review and an author branch step of the same divergence run by racing
  two requests. Processes that share one database are not serialised: an embedder running several
  replicas with `SeparationOfDuties` on keeps that race (linear workflows are unaffected — the
  review execution exists only after the author step was submitted). Closing it needs a
  database-level lock per run around check and bind; TASK-008's per-row compare-and-swap does not,
  because the two binds write different execution rows.
- **Actor resolvers must be canonical:** the engine requires an `Actors` resolver to echo the
  requested ID, which stops it answering for a different identity, but it cannot tell a canonical
  handle from a variant the resolver chose to accept. A resolver that accepts case, whitespace or
  alternate UUID spellings of a handle lets the variant bind as a second identity and defeats
  separation of duties; embedders (SMP TASK-011's `human-<uuid>` resolver) must refuse every
  non-canonical input. Standalone has no resolver: actor lookup is an exact `auth.actors` match.
- **Stored data:** runs that were cancelled or failed before the upgrade keep their live
  executions. They are inert under `EnforceRunStatus` (every entry point refuses them, and the
  candidate listing hides them); a one-shot sweep is a follow-up.
- **Not atomic:** run-end termination is a blind write that a concurrent bind can overwrite until
  INIT-023 TASK-008's compare-and-swap; `EnforceRunStatus` is the backstop.
- **Status preconditions on the run's own artifact** are evaluated against `main`, where commit
  status lands only at merge; a precondition requiring a status an earlier outcome commits can
  never hold mid-run (see `workflow.DeferredStatusPreconditions`). Custom workflows with such a
  precondition strand under the strict policy until it is removed.

### Governed Merge Binding (INIT-023 TASK-005)
SHA-bound governed merges (`Orchestrator.EnableMergeBinding`) close audit F2's merge half: the
merge publishes the commit the run's steps validated and reviewed, never the run-branch tip (see
`architecture/git-integration.md` §6.3). What remains:
1. **Binding off, the merge uses the tip.** The zero value is the pre-INIT-023 behaviour. That is
   SMP's state until SMP TASK-009 implements `engine.CommitBinder` on its operator and enables
   binding; `spine serve` always enables it (no switch, owner question 6).
2. **`automated` steps are not gates** (owner question 5). A `validate` step binds whatever head it
   completes at, so window (a) — a push between the commit that was tested and the validate submit
   — is closed only when the runner sends `expected_head_sha` with the commit it tested. A
   compromised runner credential can submit `validate` without testing anything at all; binding
   cannot protect "validated" against that holder. A declared per-step `binds_head` is a follow-up.
3. **A workflow whose first step is a gate** has no earlier binding to compare with: its first
   approval binds the head it approves (logged at Info). Binding at run start is a follow-up.
4. **A gate binds only when its advancing outcome reaches the merge through gates or internal
   steps.** In a shape such as `review → finalize (manual) → end + commit`, `finalize` is a producer
   and rebinds to the tip, so the review does not decide what merges. No shipped workflow in Spine
   or SMP has this shape.
5. **Convergence residual:** the rebind after a convergence happens only when the head before
   `EvaluateAndCommit` equals the binding, but a push between that read and the convergence merge
   still rides along. Dormant: no shipped workflow diverges.
6. **Code repositories (P3-37), binding off:** code-repository merges still merge the tip, and an
   already-merged repository is skipped after a `merge_failed` loop-back, dropping the next round's
   reviewed work. Under binding both are fixed. Both paths are dormant until a code-repository
   catalog is wired.
7. **A refused merge is on the local `main` until it is taken back.** `MergeBound` moves the local
   target before the structural check (DC-3) runs. A refused result is then taken back,
   compare-and-swap, to the state before the merge, so neither the next governed merge nor an
   in-place write's auto-push publishes it. Until then — and for good when the take-back cannot run
   (the target moved on, or a local change in the shared working tree would be overwritten; logged
   at `ERROR` and named in the outcome's failure detail) — it is on the local `main`, which with
   `SPINE_GIT_PUSH_ENABLED=false` is the published branch. `git.CLIClient`'s `MergeBound` produces
   only accepted shapes unless the target moves concurrently (until TASK-012 nothing serializes the
   worktree); the check guards the push and other `CommitBinder` implementations. When a write lands
   on the target between the merge's base read and the merge, the merge commit's parents are
   `[write, merged commit]`: it is refused and taken back to the write (the merge's first parent,
   when that is a newer descendant of the reported base), and the run stays in `committing` —
   outcome class `remote_unavailable` — so the scheduler's next attempt merges on top of the write.
   Only a take-back that could not restore the write sends the run down the permanent route (a
   legacy terminal-commit workflow fails the run, a publish workflow routes `merge_failed` back to
   `execute`). Two residuals of the race: a `CommitBinder` that itself committed onto the target
   before merging is not fully undone (its commit is kept as the "write", and a binder that does so
   on every attempt, or that persistently reports a stale `BaseSHA`, keeps the run retrying in
   `committing`; nothing is pushed on that path, and the conformance suite pins `BaseSHA`); and a
   write that lands during a *replay* (the bound commit already on the local `main`, after a failed
   push or a partially-merged resume) is not kept: `git merge` is then "already up to date" and
   reports the write itself as its result, which the check cannot tell from a commit the binder
   made, so it is refused and taken back to the base. The write is then dropped from the local
   `main` (with auto-push off, from the published branch; with auto-push on, a write its writer
   already pushed stays on origin and the local `main` must be re-synced before the next governed
   push succeeds), and the run takes the permanent route. TASK-012 runs the merge, the check and the
   take-back under one worktree lock, which closes both.
8. **A garbage-collected bound commit fails closed.** If the run branch is force-pushed away from
   the bound commit and `git gc` prunes it, the merge refuses (`precondition`) and a publish workflow
   routes back to `execute`. Pinning the bound commit with a ref is a follow-up.
9. **The recovery ledger under binding** is a child of the bound commit, referenced only by the
   run's `commit_meta` until the resumed merge publishes it: it lives only in the object store of
   the repository that wrote it and is not pushed anywhere (with binding off it was committed on the
   run branch and pushed). A resumed merge that runs after `git gc` pruned it (unreachable objects
   older than `gc.pruneExpire`, two weeks by default) or in another clone (a remote-backed embedder
   with several clones) cannot verify it and fails closed (`precondition`) — nothing unreviewed can
   merge — and the ledger is visible in git only once merged (the event carries
   `ledger_commit_sha`). Dormant: the ledger exists only for multi-repository partially-merged runs,
   and no code-repository catalog is wired in Spine or SMP. Pinning the bound and ledger commits
   with a durable ref is the follow-up (it is the same gap as item 8). Its commit date is the bound
   commit's (the commit is built deterministically); the time of the operator action is in the
   audit event.
10. **Excluded commits are logged, not evented.** When the primary's or any code repository's run
    branch holds commits the merge left out — or its tip cannot be read or judged — every run branch
    of the run is kept (not only the one with excluded commits) and a `merge_binding: commits after
    the bound head were not merged` warning names `run_id`, `bound_sha` and `tip_sha` (a `run branch
    tip unknown` warning names the branch and the error); a dedicated event type is a follow-up. Only
    code repositories this run merged are judged: one resolved externally or skipped is cleaned up
    as with binding off.
11. **Collision renumber is skipped under binding** (planning runs whose artifact id collided at
    merge fail instead of renumbering): the renumber commits on the branch tip. Neither product
    wires a collision handler today.
12. **Without `expected_head_sha` an approval binds the head at submit time, not the head the
    reviewer read.** The moved-head check compares the head at submit with the previous binding. If
    the run branch is moved while the reviewer reads the diff (say, a commit that hides a hunk) and
    moved back to exactly the bound commit before the submit, the check passes and the bound commit
    — hunk included — merges, although the diff the reviewer read never showed it. Window (b) is
    therefore closed only for clients that send `expected_head_sha`, which no Spine client does
    yet (no CLI flag; the bound head is not returned to reviewers) and SMP TASK-009 will. In
    standalone, TASK-016's review freeze limits who can push the run branch during a review to
    operators. Making the assertion mandatory on advancing gate submits is owner question 1;
    returning the bound head in the submit response is a follow-up.
13. **Convergence merges into the checked-out branch (audit F8, owned by TASK-012).**
    `CommitConvergence` merges the selected divergence branches with no target, so in standalone
    (where the shared worktree has `main` checked out) the converged content reaches `main`
    directly, without the bound merge or its structural check, and the post-convergence rebind
    (§5, DC-6) sees an unmoved run branch. The rebind guard is exercised only against a fake
    convergence. Dormant: no shipped workflow in Spine or SMP diverges. TASK-012 gives the
    convergence an explicit run-branch target.
14. **A replay finds the run's earlier merge within 64 first-parent commits.** A retry or resume
    after the merge landed diffs the status cascade against `main` as it was just before that merge,
    found on `main`'s first-parent chain, so it recomputes the same cascade commit. A merge there
    counts as this run's only when its second parent is exactly the cascade commit this run builds
    against that merge's first parent (it carries the run's `Run-ID` and `Trace-ID` trailers; wave 3
    review) — never on the commit graph alone, which let a run whose branch was reset onto another
    run's merged commit cascade that run's artifacts. If more than 64 first-parent commits landed on
    `main` between the attempts, `main`'s history cannot be read, or the recompute fails (a read of
    a cascade path failing in between), the cascade falls back to the current merge-base: a run
    that added child artifacts then gets a second cascade commit and merge commit (no content
    beyond the reviewed one).

### Projection Sync: What INIT-023 TASK-011 Leaves Open
- **`workflow.Validate` is advisory at projection.** Strict parsing and the projection round trip are
  enforced (a file that fails either is not projected), but a workflow that fails the full
  `workflow.Validate` suite is still projected, with one Info log line. 6 of Spine's 8 shipped
  workflow files (and 4 of SMP's 7) fail `Validate` today, so `WithStrictWorkflowValidation` stays
  off in both repositories until a corpus-repair task (INIT-023 owner question 7).
- **A full rebuild re-stamps `source_commit`** on every accepted workflow row. An embedder that pins
  runs to a row's `source_commit` (SMP) strands in-flight runs on every `FullRebuild`.
  Preserving `source_commit` for unchanged rows is a recorded follow-up (TASK-011 owner question 5).
- **Rows corrupted before the upgrade by the old `artifacts_dir` handling** (an out-of-tree `.md`
  file overwriting a governed artifact's row) are not repaired automatically. Standalone deployments
  with `artifacts_dir` set should run `POST /api/v1/system/rebuild` once after upgrading.
- **Non-Active workflow rows are only swept on stores with the all-rows capability.**
  `*store.PostgresStore` has it; a custom `store.Store` without `ListWorkflowProjections` gets an
  Active-only sweep.
- **Incremental deletes leave the task's execution projection row** (`projection.execution_projections`)
  until the next full rebuild. This predates INIT-023; incremental sync never called
  `DeleteExecutionProjection`.
- **Artifact content PostgreSQL cannot store** — U+0000 in front matter or body, or a path or id
  longer than a btree index entry allows (about 2.7 KB) — keeps incremental sync in `error` until the
  file changes. Workflow files of either kind are rejected instead (TASK-011: U+0000, and a path,
  `id` or `status` over 1024 bytes); the artifact side is a follow-up.
- **`QueryService.CheckFreshness` still compares against the working tree's `HEAD`**, not the
  authoritative branch. It has no production caller.
- **A workflow file git fails to read** (a lock, a git process killed or unable to start, a missing
  object) is an infrastructure error. An incremental sync holds in `error` and leaves the previous
  row as it was — still Active, like every other change in the unapplied window; a full rebuild
  restores a verified previous row quarantined, and the next sync's reconciliation retries the file
  (the rest of the window is applied meanwhile). A missing blob is repository corruption — a push
  cannot plant one, receive-pack's connectivity check refuses it — and holds the sync until the
  repository is repaired. An entry that is not a file (a gitlink — the one non-file entry at a file
  path a push can carry — or a directory) is a `not_a_file` rejection instead, decided from the
  commit's tree before the file is read. Oversized blobs become `read_bounds` rejections once the
  bounded-read work (TASK-003) registers its sentinel in `isContentReadError`
  (`adapters/projection/workflow_rows.go`).
- **Entries are typed from the commit's tree** (`git.TreeEntryTyper`, implemented by
  `*git.CLIClient`): before every workflow and branch-protection read, and after a failed
  changed-artifact read. A `GitClient` decorator passed to `projection.NewService` that does not
  forward it gets the historical read: a successful read is trusted (a gitlink recording the id of
  a blob in the repository is read as that blob), and every read failure is infrastructure (a
  dangling gitlink holds the sync in `error`, fail closed, instead of being skipped or rejected).
- **A gitlink at an artifact path whose recorded id names a blob in the repository is read as that
  blob** — by artifact discovery in both projection entry points and by every other artifact
  reader (`artifact.Service` reads, branch validation) — while diffs and repository viewers show a
  submodule pointer. Workflow files and `.spine/branch-protection.yaml` are typed before they are
  read, so this cannot change a run's workflow or the branch rules (TASK-011 review round 2).
  Typing every artifact in the projection alone would not close it for the other readers and would
  add a git process per artifact to every full rebuild; refusing gitlink entries in a governed
  repository at the push and merge boundary closes it for every reader at once (follow-up).
- **A first full rebuild that was interrupted** (the process stopped while `sync_state` was
  `rebuilding` with no synced commit) is not retried automatically, because it cannot be told apart
  from a first rebuild still running on another instance; run `POST /api/v1/system/rebuild`. A first
  rebuild that *fails* records `error` and is retried by the next sync.

## Resolved (Previously Listed)

The following limitations have been resolved:

| Limitation | Resolution | PR |
|---|---|---|
| Events not emitted | All 16 event types now emitted | #123, #124 |
| CLI init-repo placeholder | Fully implemented with directory structure and seed docs | #126 |
| CLI query commands missing | All 4 query subcommands implemented | #127 |
| Workflow binding not wired | Wired in production binary serve command | #131 |
| Git commit retry missing | Scheduler commit retry wired with callbacks | #131 |
| Engine orchestrator not wired | Full production wiring in serve command | #131 |
| Metrics not exportable | Prometheus /system/metrics endpoint | #125 |
| No audit logging | Audit entries for acceptance, rejection, convergence | #125 |
| Run StartedAt not persisted | UpdateRunStatus now sets started_at on activation | This PR |
| WriteContext ignored in API | Handlers parse write_context and attach to context | This PR |
| Divergence permissions missing | Added to operationRoles map | This PR |

## Test Coverage

| Package | Coverage | Notes |
|---------|----------|-------|
| engine | ~81% | Core orchestration, well tested |
| cli | ~82% | All commands tested |
| observe | ~98% | Metrics, tracing, audit |
| divergence | ~83% | Service + convergence |
| validation | ~92% | All rules tested |
| domain | 100% | Types and state logic |
| gateway | ~64% | Handler paths need more integration tests |
| scheduler | ~75% | Recovery + timeout tested, some paths uncovered |
| projection | 0% | Requires live Git + database |
| store | 0% | Integration tests exist separately |

## Security Notes

### Token Hashing
- SHA-256 used for API token storage (appropriate for 256-bit random tokens)
- Not bcrypt/argon2 — tokens are not user-chosen passwords, they are cryptographically random

### Auth Bypass in Dev Mode
- **No auth service:** when `SPINE_DATABASE_URL` is not set the auth service is nil, and the auth
  middleware answers 503 (fail closed) — no bypass. Production deployments MUST configure a
  database and auth.
- **`SPINE_DEV_MODE` (API only).** `1`/`true` lets unauthenticated requests through `authorize()`
  on `/api/v1`. It is refused at startup only when `SPINE_ENV=production`, so it is accepted with
  `SPINE_ENV` unset or `staging` (INIT-023 TASK-015 owner question 5: unchanged, pending the
  owner). Since INIT-023 TASK-015 it has **no effect on `/git`**; it used to drop git
  authentication for clone and push.
- **`SPINE_GIT_HTTP_DEV_NO_AUTH` (git reads only).** `1`/`true`/`yes`/`on` lets every client clone
  and fetch over `/git` without a bearer. `spine serve` refuses to start with it unless
  `SPINE_ENV=development` (unset or `staging` is refused too). It never covers a push: pushes
  always present a bearer, validated and attached as the actor. The two flags accept different
  value sets (`SPINE_DEV_MODE` only `1`/`true`); aligning them is a follow-up.

### Git HTTP: Token-Less Clones Are for Direct Peers Only
- Since INIT-023 TASK-015 (audit P3-30) the `SPINE_GIT_HTTP_TRUSTED_CIDRS` bypass applies only to a
  clone or fetch whose TCP peer is inside the list, is **not** in `SPINE_TRUSTED_PROXY_CIDRS`, and
  sent no `X-Forwarded-For`, `Forwarded`, `X-Real-IP`, `X-Forwarded-Host`, `X-Forwarded-Proto` or
  `Via`. No forwarded address is trusted for access (owner question 6: a runner behind a reverse
  proxy presents a token; pending the owner).
- **Residual: TCP-level proxies are invisible.** A proxy that relays connections without adding a
  header — Docker's userland proxy for a published port, which connects from the bridge gateway
  (`x.y.z.1`), or an L4 load balancer — looks like a direct peer. Never put such an address in
  `SPINE_GIT_HTTP_TRUSTED_CIDRS`; the compose file no longer trusts its whole bridge subnet, and
  `docker-compose.override.yaml.example` shows runner-only recipes.
- **Residual: process-wide git capacity.** In shared mode the git concurrency cap (5 in-flight
  pack operations) and the per-client-IP rate limiter are process-wide: one tenant's slow clones
  can fill every git slot and make every tenant's git requests answer `503`. They key on network
  address and capacity, not tenant identity; a per-workspace fair share is a follow-up.

### SSE: An Event With an Unsafe ID Does Not Advance the Client Cursor
- Since INIT-023 TASK-015 (audit P3-28) an event whose ID (or type) contains a control character,
  invalid UTF-8, U+0085, U+2028 or U+2029 is streamed without its `id:` (or `event:`) line, so a
  workflow identifier cannot inject SSE frames. Omitting the ID keeps the client's cursor at the
  previous event: a reconnect replays that event again (at-least-once, never a gap).
- **Residual:** if more than 100 (`sseReplayCap`) unsafe-ID events follow the last safe one, every
  reconnect replays the same first 100 and never catches up through replay; live events still
  arrive. Such IDs can come only from a workflow whose identifiers break the identifier rule, which
  the standalone API refuses (`workflow-definition-format.md` §3.6) but a git push or a governed
  merge can still land, and core validation (shared with embedders) does not apply.

### Code Repositories Authenticate Only With `credentials_ref`
- Since INIT-023 TASK-015 a code repository with no `credentials_ref` clones and pushes with no
  credential: the gitpool cloner and client factory strip the process-wide `SPINE_GIT_PUSH_TOKEN`
  and `SPINE_GIT_CREDENTIAL_HELPER` for it, on every host (owner question 2 ruled "never", pending
  the owner), and run git with every host credential source switched off
  (`git.WithoutAmbientCredentials`): no system or global git config is read (so no
  `credential.helper`, askpass program, `http.extraHeader` or `core.sshCommand` configured there,
  URL-scoped ones included), no config injected through `GIT_CONFIG_COUNT`/`GIT_CONFIG_PARAMETERS`,
  `GIT_ASKPASS`, `SSH_ASKPASS` and `SSH_AUTH_SOCK` are emptied, and for `ssh://` / SCP-style URLs ssh
  runs with no ssh_config, no agent and no key-based authentication. A private code repository
  needs its own `credentials_ref`. Only the primary repository uses the process credentials.
- **SSH code repositories cannot authenticate.** `credentials_ref` resolves to an HTTPS token only;
  there is no per-binding SSH key, so an `ssh://` code repository with no credential is reached
  anonymously or not at all. Use an `https` clone URL with a `credentials_ref`. Per-binding SSH
  credentials are a follow-up.
- **No host git config at all, credential or not.** Because the system and global config are not
  read, their non-credential settings do not apply to a credential-less code repository's clone or
  push either: a CA bundle (`http.sslCAInfo` / `http.sslCAPath`, `http.sslVerify`), a proxy
  (`http.proxy`), URL rewrites (`url.<base>.insteadOf`), `safe.directory`, `http.version` and the
  like. The environment equivalents still apply — `GIT_SSL_CAINFO`, `GIT_SSL_CAPATH`,
  `GIT_SSL_NO_VERIFY`, `HTTPS_PROXY` / `https_proxy` / `ALL_PROXY` — so set those in the
  environment of `spine serve` instead. `safe.directory` and `insteadOf` have no environment form:
  every clone under `SPINE_CODE_REPO_BASE` must be owned by the uid `spine serve` runs as
  (otherwise that repository's commits work, through the operator's `safe.directory`, but its
  push fails with "detected dubious ownership"), and a clone URL must name its real host. The
  same client's local operations (commit, merge, log) still read the host config. Passing
  Spine-controlled values for these on the command line is a follow-up for when code repositories
  are wired; `TestLatentGitpool_CredentialLessCodeRepoReadsNoHostConfig`
  (`adapters/gitpool/process_auth_scope_test.go`) pins the current behaviour.
- **Residuals:** curl's `~/.netrc` lookup, which git enables for HTTP, still answers for a host it
  lists — do not keep a `.netrc` in the `HOME` of the `spine serve` process. Config in a reused
  clone's own `.git/config` still applies (only filesystem access can write it). On git 2.30–2.31,
  which ignore `GIT_CONFIG_GLOBAL`, the global config is still read; generic `credential.helper`
  and `http.extraHeader` values are reset on the command line there, but a URL-scoped
  `http.<url>.extraHeader` in the global config is not (git ≥ 2.32 is unaffected).
- **Residual:** a reused clone whose `origin` differs from the binding's `clone_url` is not
  refused or re-cloned, so `/git/{ws}/{repo}` can serve a different repository than the binding
  names (no credential reaches it). Follow-up.

### Path Traversal
- Worktree file I/O (artifact create/update and their rollbacks, acceptance/successor reads,
  workflow create/update, the merge-time status cascade, renumber) goes through
  `core/worktreefs`. Each path component is checked with `Lstat` from a directory handle opened
  without following symlinks, inside an `os.Root`. Any symbolic link component is refused
  (`invalid_params`), and so is any `.git` component (including git's case, NTFS and HFS
  variants). New files are created `O_CREAT|O_EXCL` and updates replace by rename. A failed
  create removes what it made, including a file a failing hook replaced by a rename while the
  path is untracked in HEAD. A narrow race remains in the shared standalone tree only: a
  concurrent update of the same still-uncommitted path that lands just before that rollback is
  removed with it — the update fails, or, if it commits between the HEAD check and the unlink,
  its file is deleted from the working tree (not from git). Reads of worktree files are capped
  at 4 MiB (`payload_too_large`), so an artifact Update / accept / reject of an existing file
  over 4 MiB (only reachable by a push or an import) is refused where it used to succeed.
  (INIT-023 TASK-004, audit F5.)
- Residual: committed symlinks are still checked out as links (Spine does not set
  `core.symlinks=false`), and reads through `git show` never follow them.
- `GIT_LITERAL_PATHSPECS=1` / `--literal-pathspecs` prevents Git pathspec injection
- Scoped commits use `-- path` to prevent accidental broader commits

### Git Push Gate: Served Repositories Need Case- and Normalization-Sensitive Storage
- **Requirement:** with `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, every repository the git endpoint
  serves must live on a **case-sensitive, normalization-sensitive** filesystem: a Linux
  filesystem or the shipped compose named volume (`repo:/repo`). Do not serve pushes from a
  native macOS (APFS/HFS+) or Windows `spine serve`, from a bind mount of such a host directory,
  or from an SMB-backed or other case-folding volume. That includes the repository's own dev
  override, `docker-compose.override.yaml.example` (`./:/repo`, also shown in
  `architecture/docker-runtime.md` §9.2), on Docker Desktop for Mac or Windows: both files carry
  this warning. On such a bind mount, Linux git inside the container sees the host's
  case-insensitive filesystem.
- **Why:** branch protection matches the ref name byte-for-byte, exactly as the client sent it
  (INIT-023 TASK-001 makes the gate evaluate the same bytes git writes). A case- or
  normalization-insensitive filesystem then aliases two different names onto one ref file. A push
  to `refs/heads/MAIN` matches no rule and is allowed, and git then writes or removes the file that
  *is* `refs/heads/main` on such storage. `main` moves, or a `no-delete` branch disappears, without
  review and without an override event. Whether the ref is packed decides only which push lands:
  - **Packed ref** (any `git clone --bare` repository, or after `git gc`/`pack-refs`): a plain
    `git push origin HEAD:refs/heads/MAIN` creates a loose `refs/heads/MAIN`, which shadows the
    packed `main`.
  - **Loose ref** (the usual state in a non-bare checkout, such as the `./:/repo` bind mount): git
    refuses that plain create (`reference already exists`). A hand-crafted update of
    `refs/heads/MAIN` that carries `main`'s current object id still moves `main`, and a
    hand-crafted delete of `refs/heads/DEVELOP` that carries `develop`'s id removes `develop`.
    git's refusal to update a checked-out branch does not catch it, because it compares names
    byte-wise.

  `refs/HEADS/main` takes the non-branch allow the same way, and the aliasing is not ASCII-only
  (APFS also folds the Kelvin sign onto `k` and NFD onto NFC). `core.ignorecase` prevents none of
  this.
- **Status:** a documented deployment requirement, not yet enforced. Fail-closed handling (a
  per-repository case-sensitivity probe that refuses receive-pack, or fold-and-normalize rule
  matching that also covers the `refs/heads/` prefix test) is an INIT-023 EPIC-001 follow-up,
  paired with SMP's equivalent follow-up.

### Git Ref Arguments
- **Grammar** (INIT-023 TASK-002; audit F10, P3-32, P3-36): the git client
  (`adapters/git.CLIClient`, `EnterBranch`) builds every ref argument through one grammar.
  Branch arguments are short names or `refs/heads/<n>` that pass `refname.Validate`; they are
  looked up exactly (`git show-ref --verify`, no DWIM) and reach git as object ids, so a pushed
  tag or ref named after a run branch or `main` can no longer stand in for it. Revisions resolve
  branch-first, then exactly as before. Push sources are `<oid>^{commit}`, push destinations
  `refs/heads/<n>`; `Push` accepts branches only. A new branch is cut from `<oid>^{commit}` or
  `HEAD^{commit}`: `git branch` alone re-resolves a bare start point by DWIM, so a ref named after
  the base's object id would replace it and a `refs/tags/HEAD` would make it fail. Every ref
  follows `--end-of-options` and is validated before git starts; a refusal is a permanent error
  wrapping `git.ErrInvalidRef`. A repository's `default_branch` must be a short branch name
  (`main`, not `refs/heads/main`; API 400, engine precondition).
- **Cost:** a revision given by name runs one extra `git show-ref` per call when it is
  `refs/heads/<n>` or a bare name that is a valid branch name, whether or not the branch exists:
  branches, tags (`v1.2`), abbreviated ids and `origin/<n>`. Full object ids, `HEAD` and other
  `refs/*` names (`refs/tags/<t>`, `refs/remotes/<r>/<n>`) cost nothing extra. Callers that read
  many files at one name resolve it once with `RefSHA` and read by object id
  (`artifact.Service.List` does; SMP's run-artifacts reader is handed off to SMP INIT-027 EPIC-004
  TASK-008).
- **Git floor:** git 2.30 or newer (`rev-parse --end-of-options`). `spine serve` refuses to start
  below it; embedders call `git.CheckGitVersion` at boot. Every shipped image runs 2.39.5 or later.
- **Residual — user `?ref=` for a non-branch name still follows DWIM.** A read with
  `?ref=<name>` where `<name>` is not an existing branch resolves as git always has, so a pushed
  `refs/<name>` beats `refs/tags/<name>`. Name tags in full (`?ref=refs/tags/<name>`). TASK-016
  blocks such pushes for non-operators.
- **Residual — a branch beats a same-named tag in a bare-name read.** Revisions resolve
  branch-first, so a bare `<name>` that is both `refs/heads/<name>` and `refs/tags/<name>` reads
  the branch; git itself, and Spine before INIT-023, read the tag. Branch-first is what stops a
  pushed tag from standing in for a run branch or `main`. Since INIT-023 TASK-016 tags are
  operator-only in standalone while an ordinary branch is pushable by any contributor that branch
  protection does not stop, so a contributor can push `refs/heads/<tag-name>` and every read that
  passes the bare name — `?ref=<tag-name>` on the artifact, artifact-list and workflow read APIs —
  then returns the branch's content instead of the operator's tag, with no warning. Engine-internal
  reads (run branches, `main`, object ids) are unaffected. Name tags in full
  (`?ref=refs/tags/<name>`), or protect the tag names' branch namespace with a branch-protection
  rule. Refusing an ambiguous bare name, or qualifying user-supplied refs at the API, is an
  INIT-023 follow-up (TASK-002 cross-task review (wave 2)).
- **Residual — missing non-branch refs.** A `refs/tags/...` or `refs/remotes/...` argument naming
  a ref that does not exist can still be DWIM-substituted by a name such as
  `refs/tags/refs/tags/...`; push admission refuses to create or update those names for every
  principal (TASK-016 rule 0b; below).
- **Residual — doubly-qualified refs on the remote break pushes.** If the repository a client
  pushes to holds `refs/heads/refs/heads/<n>`, `refs/refs/heads/<n>` or
  `refs/tags/refs/heads/<n>`, every push to `<n>` fails (`dst refspec refs/heads/<n> matches more
  than one`), whatever the client argv; in standalone that is the governed-merge push to `main`.
  The git client cannot fix this. The failure is classified permanent, so the engine does not
  loop on it. Push admission refuses to create or update such names for every principal: Spine
  INIT-023 TASK-016 (standalone, rule 0b) and SMP INIT-027 EPIC-004 TASK-005 §7. Existing such refs are not deleted
  automatically; list them in each served repository and delete them (see `CHANGELOG.md`):
  `git -C <repo> for-each-ref refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs`.
- **Residual — refs named `HEAD` or after an object id.** Spine's own git calls are immune to
  them (exact lookups, object ids, `^{commit}` start points), but other git clients that work on
  the served repository or its clones are not (`git branch <n> HEAD` dies next to a
  `refs/tags/HEAD`; `git checkout <oid>` can land on a branch named `<oid>`). TASK-016 refuses new
  `HEAD` components; existing ones, and object-id-named refs, are listed and deleted per the
  `CHANGELOG.md` upgrade notes.
- **Dormant — code-repo default branch must exist locally.** Code-repository clones are made with
  `--no-tags` and hold a local branch only for the remote's `HEAD`. For a `default_branch` that is
  not the remote's `HEAD`, run start's `git branch <run> <d>` already failed (`git branch` never
  reaches `origin/<d>`), while the code-repo merge's `git checkout <d>` created `<d>` from
  `origin/<d>` by guessing; the merge is now refused too (`switch --no-guess`), and run start
  reports `not_found`. No shipped build wires a code-repository catalog (P3-36, P3-37), so this is
  dormant.

### Git Read Bounds
- **Bounds** (INIT-023 TASK-003; audit F4): the git CLI client (`adapters/git.CLIClient`) never
  buffers more than its limits. `ReadFile` probes the blob's size (`git cat-file -s`) and refuses
  one over the per-blob limit before reading it (`git.DefaultMaxBlobBytes`, 4 MiB — the worktree
  read limit too; `git.WithMaxBlobBytes`), with a permanent error matching `git.ErrBlobTooLarge`
  (API 413). Every other read command — listings, diff, log, rev-parse, merge-base — fails past
  16 MiB of output (`git.DefaultMaxOutputBytes`, `git.WithMaxOutputBytes`, never below 4 KiB;
  `git.ErrOutputTooLarge`).
  Read commands also run with `GIT_ALLOC_LIMIT` (4 x the per-blob limit, never below 1 MiB, which
  git's own fixed allocations need; `git.WithMaxGitAllocBytes`), so git refuses any single
  allocation over it — it will not inflate a small blob stored as a delta on a huge base, or render
  a gitlink `*.md` as a commit with its patch — and read and diff commands pin git's delta-base
  cache to the per-blob limit (`core.deltaBaseCacheLimit`, passed in `GIT_CONFIG_PARAMETERS`
  after any inherited entries); they run in the C locale so git's refusal is recognised whatever
  the host's language. A command's output is held once: the parsed paths and messages share its
  buffer, which grows in powers of two (so while it grows it is briefly held at half and full
  size). Mutating commands (checkout, merge, commit, add, branch, config, push) keep only the
  first 16 MiB of their output and never fail on its size. Defaults pending the owner (task owner
  questions 1 and 4).
- **git's memory per read is about three times the alloc limit, not one.** `GIT_ALLOC_LIMIT` is
  checked per allocation. An object at the end of a delta chain whose bases are each just under
  the limit is rebuilt base by base, so one step holds the base being patched, its result and the
  delta-base cache — about 50 MiB in the git child with the defaults (49–52 MiB measured for a
  3 MB file on a chain of eight 15 MiB bases; 124–127 MiB before the cache was pinned). Each
  concurrent read pays it, so a replica serving N such reads at once needs about N x 50 MiB of
  headroom for git. Lowering it means lowering the alloc limit (owner question 4), which refuses
  more legitimate deltas.
- **A read-command output over the ceiling fails closed.** A commit on `main` whose listing or
  diff exceeds 16 MiB (roughly 50k files, or fewer with long paths) makes `IncrementalSync`,
  `FullRebuild` and `NextID` fail for that workspace — the sync state is `error` at the previous
  commit and nothing is written — until the content is removed or the limit raised. Reaching it
  takes a governed merge. Before TASK-003 the same commit exhausted the process's memory instead.
- **`diff` runs without `GIT_ALLOC_LIMIT`.** Rename detection loads both blobs of a candidate
  pair, and a legitimate large rename on `main` must not fail `Diff`; so a large blob already on
  the diff base can still be inflated in the git child while its rename is detected (its
  delta-base cache is pinned like a read command's).
- **Named-ref window.** The size probe and the read are two git processes. A ref given by *name*
  can move between them; Spine's buffer stays capped (the read overruns into the same refusal, with
  size -1) and git's memory stays within the per-read bound above. Projection reads use commit ids; the
  window applies to reads by name (`?ref=<branch>`, and the merge-time status cascade with merge
  binding off, which reads the run branch by name; with binding on — always in `spine serve` —
  INIT-023 TASK-005 reads it at the bound commit).
- **Deltas on a large base are refused.** A legitimate artifact stored as a delta on a base larger
  than `GIT_ALLOC_LIMIT` (16 MiB by default) cannot be read and is refused as oversized. The
  refusal says so (`git.BlobTooLargeError.GitAllocLimit`, `git_alloc_limit_bytes` on
  `projection_file_skipped`, and the 413 message names the git allocation limit), because the
  file itself may be well under the per-blob limit.
- **What `git show` renders counts.** The limit applies to what is read for a path: a tree path's
  listing, or a gitlink rendered as a commit with its patch, is refused like a large blob.
- **The merge-binding plumbing** (INIT-023 TASK-005's `CommitBinder` methods: `rev-parse
  --verify`, `rev-list --parents`, `merge-base --is-ancestor`, `ls-tree`, the private-index
  `read-tree` / `hash-object` / `update-index` / `write-tree` / `commit-tree`, `update-ref`,
  `read-tree -m -u`, `symbolic-ref`) runs in the C locale with the per-command output limit and the
  bounded stderr (wave 3 integration), but without `GIT_ALLOC_LIMIT` or the pinned delta-base
  cache: the take-back's `read-tree -m -u` materialises the working tree like checkout, and the
  read-only commands parse the commit being merged, which the merge itself then reads without an
  allocation limit.
- **Not covered:** receive-time inflation (`index-pack` reconstructs a pushed delta to hash it;
  `receive.maxInputSize` bounds the pack, not the object), checkout-time inflation (`git worktree
  add` / `checkout` of a branch holding an oversized blob, including the standalone branch-write
  worktree), and wall-clock timeouts on git commands.
- **Cost:** one extra git process per `ReadFile` (two on a miss: the probe, then the `git show`
  that produces today's error), so a full rebuild of a large repository takes roughly twice as
  long. A batched probe is a follow-up.
- **Refused paths** (P3-9, owner question 2): `Diff` and `ListFiles` return raw NUL-delimited
  paths, so non-ASCII artifacts are now projected. A path that is not valid UTF-8 or holds a
  control character (C0, DEL, C1), U+2028/U+2029 or a bidi control (U+202A–U+202E,
  U+2066–U+2069) is never projected (`projection_file_skipped`, reason `unsafe_path`) and is left
  out of `artifact.Service.List` and `workflow.Service.List`, as it was while git quoted it.
  A row an earlier projection made at such a path — possible only under `core.quotePath=false`,
  where git listed bytes >= 0x80 raw — is removed when its file is deleted, renamed away or
  modified, and a workflow row is kept quarantined (or removed) by the first sync's sweep, or by a
  sync that changes the file (wave 3 integration); an artifact row at such a path whose file
  never changes stays until the upgrade's projection rebuild (CHANGELOG). `artifact.Slugify`
  never produces one. Other readers that take a path from the caller
  (`artifact.Service.Read`, the merge-time status cascade) are unchanged.
- **Embedders that wrap `ReadFile`** (SMP's bounded reader) return their own too-large error;
  until it matches `git.ErrBlobTooLarge`, discovery files such a read under its read errors and
  `Service.Read` reports a git error rather than 413 (SMP INIT-027 EPIC-004 TASK-008).

### Webhook Signing Secrets: `migrate` Mode Does Not Close the Cross-Workspace Transplant
- **What:** signing secrets are sealed in the `enc:v2` envelope, bound to the workspace and the
  subscription (INIT-023 TASK-009). But the default `SPINE_SECRET_ENVELOPE_MODE=migrate` still
  accepts legacy `enc:v1` and plaintext values and its re-encryption sweep converts them. A copy of
  another workspace's pre-upgrade `enc:v1` value planted in a workspace database is indistinguishable
  from a genuine pre-upgrade row and becomes a valid `enc:v2` row (audit P3-10).
- **Closing it:** set `SPINE_SECRET_ENVELOPE_MODE=strict` once every workspace has swept clean
  (`spine secrets reencrypt [--all-workspaces]` with serve's mode), or retire every key that ever
  sealed an `enc:v1` value. Either step stops values planted from then on only: a plant a `migrate`
  sweep already converted stays a valid `enc:v2` row. The sweep is the only path that converts a
  legacy value (a write that keeps a subscription's secret leaves its stored bytes untouched) and it
  logs every conversion per row (`subscription secret converted`), so before either step review that
  log against its baseline — the first clean `migrate` sweep after any `downgrade` period, older
  binary or keyless period — and rotate every subscription it flags
  (`docs/integration-guide.md` §2.1). A value planted *during* such a period cannot be told apart.
- **Status:** strict-by-default in a later release is a follow-up (owner question 1).

### Webhook Signing Secrets: No All-Workspaces Verification in `platform-binding` Mode
- `spine secrets reencrypt --all-workspaces` needs the workspace registry (`WORKSPACE_RESOLVER=db`).
  `platform-binding` has none to enumerate, and a process sweeps a workspace only when it builds it
  (its first request after the process starts, or after an eviction): a request to a workspace the
  process already holds does not sweep it. During a rolling update, a process still on the previous
  setting that first loads a workspace late in the rollout sweeps it back (to `enc:v1`, to the old
  primary key, or to `enc:v2`), and no clean record logged before that shows it. So before switching
  to `strict`, dropping a previous key or rolling back, the operator must restart every process once
  the rollout of the change has completed (unless it was a stop-the-world restart), then load every
  bound workspace and check that each one's latest `subscription secrets swept` record across all
  replicas comes from a process started after the rollout and shows `refused=0` and `conflicts=0`
  (`docs/integration-guide.md` §2.1, *Verifying every workspace*). A workspace without such a record
  is unverified; flipping early stops its webhook fan-out when it next loads, or, before a rollback,
  leaves `enc:v2` rows the old binary signs with as text. An enumeration source for this mode (the
  platform's binding list) or an on-demand sweep is a follow-up.

### Webhook Signing Secrets: One Unopenable Row Stops a Workspace's Fan-Out
- A signing secret that does not open (wrong or removed key, transplanted, legacy in `strict`) fails
  the whole subscription list call, so the workspace's webhook delivery stops for the event types
  that row subscribes to until the row is fixed (`POST /subscriptions/{id}/rotate-secret`, which
  replaces the secret without reading the refused value, or deleting the subscription). Rows are
  deliberately not skipped (that would silently drop events) or blanked (unsigned deliveries). A
  non-decrypting listing method would narrow the blast radius; it is a recorded follow-up because it
  changes the `SubscriptionStore` interface.
- The same fail-closed refusal applies when two workspaces share one runtime database, or a workspace
  id changes without the documented `downgrade` procedure (`docs/integration-guide.md` §2.1).

### Platform Bootstrap Bearer (`SMP_ADMIN_TOKEN`) Rotation
- **Behaviour (INIT-023 TASK-013):** the first load of a workspace after a restart with a new
  `SMP_ADMIN_TOKEN` revokes the previous bootstrap bearer there (only `bootstrap-…` rows of
  `smp-admin` created before the replica started; admin-minted `tok_…` tokens are kept).
  Revocation is never undone.
- **Not zero-downtime:** platform requests that still carry the old bearer get 401 from each
  workspace's first load after the Spine restart until the platform switches. Switch both in one
  change window (`docs/operator-runbook.md` §5.6). A two-phase `SMP_ADMIN_TOKEN_PREVIOUS` grace
  is a recorded follow-up (TASK-013 owner question 2).
- **Rollout residues:** rows are ordered by time, which cannot tell a stale bearer re-inserted
  late from a newer one. Both residues are confined to workspaces first loaded during a
  rotation's rollout (a row for one bearer and none for the other); only bearer generation
  numbers would remove them (owner question 5), and the default accepts them.
  - *Restart during the rollout:* an old-config replica that restarts mid-rollout and then loads
    such a workspace revokes the *new* bearer there. Recovery is to rotate forward to a fresh
    value.
  - *Old bearer inserted after the new one:* if a new-config replica seeds the new bearer first
    and a replica still carrying the old bearer (an old-config replica, or pre-upgrade code during
    the upgrade rollout) then loads the workspace, the old bearer's row is newer than the new
    one's. No replica carrying the new bearer revokes it, restarts included, so the **old bearer
    stays valid until the next rotation**. Only a replica that started after that row was
    written logs it at Warn (with the row's `token_id`), on each load of such a workspace. A
    replica already running when the row was written cannot tell it from a rollout still in
    progress and stays quiet on every load, idle-eviction reloads included, so the Warn appears
    only once every replica is restarted after the rollout. Revoke the named row with
    `DELETE /api/v1/tokens/{token_id}` (runbook §5.6 (e)).
- **A failed bootstrap leaves the old bearer live until a retry succeeds:** `spine serve`
  retries a failed admin bootstrap in the background (from 1s, up to 1 min apart) for as long as
  the workspace stays loaded; each failure is logged at Error.
- **Clock dependence:** rows are ordered by the `created_at` the inserting replica stamps, so the
  rollout cutoff assumes replica clocks agree to well within the deploy's duration.
- **Legacy token ids:** bootstrap rows written before TASK-013 keep their
  `bootstrap-<sha256(bearer)[:12]>` id (listable by a workspace admin, printed at Debug on each
  load) until the next rotation revokes them. Rotate once after upgrading.
- **Unsetting `SMP_ADMIN_TOKEN`** stops seeding but does not revoke the existing bootstrap row;
  revoke it with `DELETE /api/v1/tokens/{token_id}`.

### Skill Eligibility Is Matched by Name
- Workflow `required_skills` and claim-time eligibility match skill *names*, not ids. Since
  INIT-023 TASK-013 every operation that can move eligibility (granting, removing, creating,
  renaming, deprecating) is admin-only, a non-admin can never change its own skills, and a rename
  or deprecation that touches a name an active workflow requires needs `?force=true`. Matching by
  immutable `skill_id` is a deferred follow-up (a governed workflow-format change).
- The rename and deprecation guards scan **active** workflow projections only; a run pinned to a
  superseded workflow version that names the skill is not detected.
- `auth.actor_skills` records no grantor, so grants made by non-admins before the upgrade are not
  distinguishable. Review `review`, `workflow_review` and `architecture` holders after upgrading.
- `PATCH /skills/{id}` and `POST /skills/{id}/deprecate` are read-modify-write. They re-read the
  skill right before writing (a rename in between is a 409), but a change another admin commits
  between that re-read and the write is still overwritten. Closing the window needs a
  conditional `UPDATE` in the store (a follow-up).

## Architecture Deviations

### Convergence Evaluation Step
- Convergence strategies are evaluated programmatically
- The spec describes an "evaluation step" where an actor reviews branch outcomes
- Current implementation routes to the step with `converge` field after auto-evaluation

### Branch Isolation
- Git branches are created for isolation
- No runtime enforcement prevents cross-branch artifact reads via API
- Isolation is convention-based, not enforced at the store level
