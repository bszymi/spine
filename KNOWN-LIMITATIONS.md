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

### Git Push: Run-Branch Admission Is Checked at Push Start and Again at Apply Time
- With `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, a run, planning or divergence branch is pushable only
  by the actor bound to its current step, or an operator (INIT-023 TASK-016). Three limits remain:
  - **Admission is decided twice; a narrow window remains.** The gate decides when the push's
    command section arrives, and — since INIT-024 TASK-005 (review finding N-9) — the pre-receive
    hook decides again once git has received and processed the pack, before any ref moves: it asks
    the server over a per-push channel, and the server re-runs the same admission (the run active,
    the latest execution of the step still the actor's and assigned or in progress, the review
    freeze) against the store as it is then, with nothing remembered from the first decision. A push
    whose step was submitted, reassigned or turned into a review or internal step while its pack was
    in flight is refused (`remote: spine: push refused: ref-admission: …`, `pre-receive hook
    declined`), and so is one the hook cannot get an answer for (fail closed). What remains is the
    time between that answer and git's ref update (the hook's exit), and the upload and processing
    before it, during which nothing is written. Branch-protection rules are still evaluated once, on
    the command section. Binding the governed merge to the
    reviewed commit (INIT-023 TASK-005; always on in `spine serve`) stays the content control:
    a push that lands after the step's submit bound the head is not merged unless a later
    producer step binds it — an `automated` step (a validate or summarize) re-binds to whatever head
    it completes at unless its submitter sends `expected_head_sha` ("Governed Merge Binding" 2; the
    shipped `artifact-creation`, `adr-creation`, `document-creation` and `task-spike` workflows have
    such a step before their review, and no Spine client sends the assertion yet) — and an approval
    of a branch that moved since the previous binding is refused
    (`409 run_branch_moved_since_gate`); an embedder that does not enable merge binding keeps the
    remaining window. SMP's own git gateway does not have the re-check yet (SMP companion epic).
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
  matching does not fold; on storage that does, receive-pack is refused (INIT-024 TASK-005; see
  "Served Repositories Need Case- and Normalization-Sensitive Storage" below).

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
  - **A push admitted while bound counts its actor as an author** (see "Run-Branch Admission Is
    Checked at Push Start and Again at Apply Time"): since INIT-024 TASK-005 such a push lands only if the actor is still bound
    when git applies it, and its actor counts as an author because it was bound.
- **Planning runs without a recorded starter are reviewable by nobody (INIT-024 TASK-001,
  Decision 2; owner question 1 default: fail closed).** Separation of duties counts the actor who
  started a planning-mode run as a run author, from the starter the engine records at start
  (`commit_meta` key `spine.governance.started_by`). A planning run started before the upgrade, or
  while separation of duties was off (`legacy`, or an embedder without the policy), has no record,
  and nobody can tell who wrote its content, so every candidate for its `review` steps is refused
  (403, `(run start: starter not recorded)`) and the candidate listing offers it to nobody. Such a
  run waits until an operator cancels it and starts it again, or runs
  `SPINE_STEP_ENTRY_POLICY=legacy` for the migration window. (A guard-only deployment records the
  starter too, so its runs stay reviewable when the policy is turned on.)
  - **A review bound before the upgrade stays bound.** Separation of duties is checked when an
    actor is bound; a starter who already holds its run's review keeps it. Cancel such runs, or
    release and re-offer the review, before relying on the rule.
  - **Standard runs are unchanged.** The actor who starts a standard run wrote no content and is
    not an author; nor is the author of the task file on `main` (out of scope).
  - **Embedders:** under `SeparationOfDuties`, every planning start (`StartPlanningRun`,
    `StartWorkflowPlanningRun`, `StartWorkflowUpdatePlanningRun`) needs `domain.WithActor` on the
    context, under exactly the actor ID the step-entry resolver later binds (the compare is
    exact; SMP: the member's `human-<uuid>` handle in `CreateEntry`). Without it every planning
    start is `ErrUnauthorized`; with an `Actors` resolver installed, so is an id it does not
    resolve (a platform user id recorded instead of the handle would otherwise never match the
    bound reviewer and silently reopen the self-review). Without a resolver the engine cannot
    check the id beyond its being non-empty: an embedder that binds actors by Spine's actor table
    must put that table's id on the context.
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
- **Not atomic without the store capability:** run-end termination writes each live execution
  through the engine's step write, which is conditional on (status, actor) only with a store that
  implements `engine.StepExecutionTransitioner` (INIT-023 TASK-008; `spine serve`'s PostgreSQL
  store does). An embedder whose store does not (SMP until its TASK-011) keeps a blind write that a
  concurrent bind can overwrite; `EnforceRunStatus` is the backstop.
- **Status preconditions on the run's own artifact** are evaluated against `main`, where commit
  status lands only at merge; a precondition requiring a status an earlier outcome commits can
  never hold mid-run (see `workflow.DeferredStatusPreconditions`). Custom workflows with such a
  precondition strand under the strict policy until it is removed.

### Engine State Writes and Submit Ownership: What INIT-023 TASK-008 Leaves Open
INIT-023 TASK-008 (audit P3-17 Spine half, P3-24) makes run-status writes compare-and-swap with
re-evaluation, makes step writes compare-and-swap on (status, actor) with a capable store, stops a
terminal execution from being routed twice and lets the engine verify the submitter
(`architecture/engine-state-machine.md` "Concurrent writes"). What remains:
1. **The engine's submitter check is opt-in.** `engine.SubmitRequest.ActorID` empty means "the
   caller enforces ownership", as before. `spine serve` always sends the authenticated actor (also
   when the gateway's own pre-check is skipped for want of a store); dev mode, with no
   authentication, sends none. An embedder that sends none (SMP until its TASK-011) relies on its
   own check; the snapshot handoff still stops an empty-`ActorID` submit from completing an
   execution rebound between `IngestResult`'s read and the write. Failing closed on an empty
   `ActorID` is a follow-up once every caller sends one (owner question 4: an Orchestrator option
   plus an explicit system-submit marker for the actor-less callers).
2. **Step compare-and-swap is opt-in for embedders.** A store without
   `engine.StepExecutionTransitioner` keeps the blind `UpdateStepExecution`; SMP opts in in its
   TASK-011 (owner question 3) and keeps its per-execution locks. Run-status compare-and-swap uses
   `TransitionRunStatus`, which every store has, so it is on everywhere.
3. **(status, actor), not a version:** an ABA by the same actor (release, then reclaim by the same
   actor) restores `(in_progress, A)`, so a stale write from that actor's earlier attempt can still
   match. It cannot cross actors. A version column would close it and needs a migration.
4. **Step write and next-step creation are not one transaction.** A cancel that commits between a
   submit's applied terminal write and `advanceToNextStep`'s `CreateStepExecution` leaves a
   `waiting` execution on the cancelled run; the run stays cancelled (the routing's `CompleteRun`
   or the publish handler's transition misses), and the stray execution is refused by the
   run-status entry checks (TASK-007) — inert, not removed. Fully transactional routing is a
   separate engine change.
5. **A lost submit can still write `commit_meta`.** With merge binding on, a captured binding (and
   this execution's evidence key) is written before the step's terminal write (wave 3, so a
   completed execution always has its binding). A submit that then loses its step
   compare-and-swap has already written them; `commit_meta` writes remain whole-map,
   last-writer-wins replaces built from the writer's own read of the run (TASK-005). The window is
   between the two writes of one submit; the recovery ledger's per-run lock does not cover step
   submits. Only the persisted key is left behind: the `required outputs declared but run branch
   unchanged` Warn and `step_completed` follow an applied terminal write only, and run detail
   returns completed executions only.
6. **A failed retry and the scheduler's failed-step recovery can race** to create the next
   attempt; both build the same deterministic execution id, so the loser's `CreateStepExecution`
   fails on the primary key. No duplicate is possible; the loser logs an error.
7. **Two replicas recovering the same completed execution** may both pass `RecoverCompletedStep`'s
   checks; the routing is claimed by creating the successor first under an attempt pinned from
   the checked listing, so one wins the primary key and the other gets a conflict, and
   `step_completed` is re-emitted under the execution's own id (the event log keeps the first
   copy). A *live* submit routing the same execution at that moment numbers its successor from
   its own later listing (`advanceToNextStep`), so it can create a second attempt beside the
   recovery's — a multi-replica rolling-restart window of milliseconds; the run cursor
   compare-and-swap keeps the cursor from being rewound. The queue consumer (dormant: no production
   caller) recovers a completed execution its own redelivery was refused for, but not while another
   delivery of the same assignment is still inside `SubmitStepResult` in that consumer — that one
   may be the winner, about to route — and the last delivery out recovers instead; a winner in
   another process, or a submit that does not go through the consumer, is not seen, so the same
   window applies there. A crash between creating the next execution and moving the run's
   cursor is finished by recovery (cursor moved, the `waiting` successor activated; before, the
   re-route created a duplicate successor attempt). A crash after the cursor moved but before the
   successor was activated is not: recovery leaves a `waiting` current step alone, so a
   machine-selected or internal successor is not activated until an operator acts (pre-existing).
   A divergence-branch execution is recovered on its branch's cursor (only the queue consumer
   reaches that today; the scheduler's recovery follows the run cursor, which is
   `divergence:<id>`); a branch marked completed whose convergence check never ran is not.
   **Upgrade note:** recovery re-emits `step_completed` under the per-execution id
   (`evt-<trace>-<execution_id>-completed`). An execution completed by a pre-TASK-008 binary was
   logged under the per-step id (`evt-<trace>-<step_id>-completed`), so if a crash interrupted its
   routing and the process restarts on this version, that one execution's `step_completed` is
   logged and delivered twice (once per id). It is a one-off at the upgrade, standalone only (no
   embedder calls recovery). Recovery cannot tell such an execution apart without a schema change,
   and skipping the re-emission would lose the event of every completion a crash interrupted before
   it was emitted.
8. **A release whose assignment cancel fails is compensated, not atomic.** With the step
   capability `ReleaseStep` writes the step first and cancels the assignment row after it; if the
   cancel fails, the step is put back (conditionally) and the error returned. A writer that took
   the released step in that instant makes the restore miss: the row stays active (an `ERROR
   release: assignment not cancelled and the step could not be restored` names it) and blocks
   claims until it expires or the run ends. One transaction across both stores would close it.
9. **Resuming a successor needs the conditional cursor move.** `RecoverCompletedStep` moves the
   cursor with `engine.RunCursorTransitioner`, and a divergence branch's cursor with
   `engine.BranchCursorTransitioner` (the PostgreSQL store has both); a store without them
   re-reads the run (the branch) right before a plain `UpdateCurrentStep` (`UpdateBranch`), which
   narrows but does not close the window in which a live submit's cursor could be rewound — or,
   for a branch, a completed branch reopened by a stale snapshot. No embedder calls recovery today.
10. **`ProcessResult` (`core/actor`) has no production caller.** It now refuses failed and skipped
   executions and a foreign actor and writes conditionally when it can; deleting the dead path is a
   follow-up.
11. **A lost claim's assignment row is removed only with `engine.AssignmentRowDeleter`.**
   `ClaimStep` creates its `claim-<execution>-<actor>` row before the conditional step write,
   because the active-assignment unique index is what serialises two claims. When the step write
   loses, the claim never bound its actor, so the row is deleted. That row would otherwise be read
   as a binding: the separation-of-duties ledger counts claim rows in every status, the
   prior-holder re-offer reads one as "held and released", and its id would collide with the same
   actor's next claim. The PostgreSQL store implements the port. An assignment store without it
   (or a failing delete) cancels the row instead, so no claim is blocked, and the three effects
   remain: the lost claimant is refused as a reviewer of that run, a prior holder's lost claim
   opens its re-offered rework attempt to others, and that actor cannot claim the execution again.
   It is reachable only together with `engine.StepExecutionTransitioner`, because a blind step
   write never loses.

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
   only accepted shapes unless the target moves concurrently (since TASK-012 in-process writers are
   serialized by the shared working-tree lock, so only another process on the same tree can); the
   check guards the push and other `CommitBinder` implementations. When a write lands
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
   push succeeds), and the run takes the permanent route. Since TASK-012 the merge, the check and the
   take-back run as one block under the working-tree lock, which closes both for in-process writers
   (see "Shared Working Tree" under Architecture Deviations).
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
13. **Convergence merged into the checked-out branch (audit F8) — closed by TASK-012.**
    `CommitConvergence` now merges the selected divergence branches into `run.BranchName`
    explicitly (and divergence branches fork from it), so converged content reaches `main` only
    through the bound merge, and the post-convergence rebind (§5, DC-6) sees the moved run branch.
    The rebind guard is still exercised only against a fake convergence. Dormant: no shipped
    workflow in Spine or SMP diverges.
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
15. **Recovery actions are serialised per process only.** The bound recovery ledger is built and
    its binding recorded under a per-run in-process stripe, from a fresh read of the run (wave 3
    review), so overlapping `retry` / `resolve-externally` requests for one run in one process
    chain their ledgers and keep each other's `head_sha` rebinds. Requests served by different
    processes sharing one runtime database are not serialised: the later `commit_meta` write
    (a whole-map replace) can still restore the other's bindings. What merges is then an earlier
    reviewed binding, never unreviewed content; closing it needs a compare-and-swap on
    `commit_meta` or a database lock per run. Dormant: recovery applies only to multi-repository
    partially-merged runs, and no code-repository catalog is wired in Spine or SMP.

### Governance-Path Guard (INIT-023 TASK-006, ADR-016)

The merge guard holds a governed merge that changes `.spine/`, `.spine.yaml`, `workflows/`,
`<artifacts_dir>/workflows/` or (INIT-024 TASK-002) a YAML file directly inside any directory
named `workflows` until an operator approves the bound commit; the projection guard refuses an
unprovenanced branch-protection removal. What remains:

1. **Opt-in for embedders.** The library's zero value is v1: until an embedder calls
   `WithGovernanceGuard` and `WithProtectionRemovalGuard` (SMP: its TASK-010), F3 stays open
   there. `spine serve` turns both on; `SPINE_GOVERNANCE_GUARD=off` restores v1.
2. **Override provenance lives in the event log, which is pruned.** An honoured override push
   vouches for a relaxation only through its `branch_protection.override` row in
   `runtime.event_log` (written synchronously at pre-receive by `spine serve` with the guards on,
   and by SMP's emitter). Retention prunes that table, so a database rebuilt long after a
   relaxation — or a fresh database restored from git — refuses it until an operator re-affirms
   it with an override push that **edits** the file (`docs/git-push-guide.md` §4). A failed
   synchronous write is logged and the push proceeds; the relaxation is then refused until
   re-affirmed. API-path override events carry no pre-receive range and never count.
3. **A refusal is not a sync error** (owner question 7). `sync_state` does not show it; the signals
   are the Warn log, `branch_protection.removal_refused`, and installed rows whose `source_commit`
   is older than the file at head. It is re-evaluated on the next change to the file and on every
   full rebuild. A provenance or rule-store read failure, or a retryable git failure while
   checking a record, by contrast, holds the sync in `error` and is retried; a record whose commit
   is not in the repository (an override recorded for a push that never landed) is a mismatch.
   With the guard on, a full rebuild whose branch-protection step failed on infrastructure keeps
   the previous `last_synced_commit` (not the head), so the retry keeps the newness anchor.
4. **Scope of the projection guard** (owner question 5): the authoritative branch only. Removing
   protection from other patterns (`release/*`, `staging`) is installed without a record. While the
   installed rules carry no `no-direct-write` on the authoritative branch the guard is inactive
   (ADR-009 §1 bullet 3).
5. **Paths outside `MergeRunBranch`.** Operator override pushes and ADR-008 operator bypass
   writes are not held by the merge guard (operators may land changes directly by design).
   Convergence is no longer one of them: since TASK-012 (audit F8) it merges divergence branches
   into the run branch only, so their content reaches `main` through the guarded merge. Workflow
   definitions get no projection provenance check.
6. **A refused merge that cannot be taken back blocks every governed merge** — whichever check
   refused it (INIT-023 wave 4 integration): TASK-005's structural check, TASK-017's merge-result
   check (V1) or this guard's post-merge assertion or failed marker write. Every one of them takes
   the merge back inside TASK-012's merge block, before the shared tree is released, with
   `ResetBranch` (a compare-and-swap to the base `main` held). If that fails, the refused commit is
   still on the local `main`, and two records keep anything from building on it:
   - **the shared tree** (`git.CLIClient`, standalone): the take-back stays pending in process,
     the block's own exit restore retries it, and every later take of the tree — another merge,
     an in-place write, a run start — retries it first and is refused while `main` still contains
     the refused merge;
   - **the guard** (any operator, with the guard on): the merge is recorded as a block, in
     process and on the refusing run's `commit_meta` (`spine.governance.blocked_target`), and every
     governed merge is refused as unavailable before it merges a code repository (not a counted
     attempt), retrying the take-back itself.

   Whichever completes the take-back clears both: a successful `ResetBranch` drops the tree's
   record, and the guard drops its block once `main` no longer contains the merge. An operator
   can also reset `main` by hand. A refusal of the guard keeps its run `committing` (its retries
   keep trying, and the retry budget never abandons it); a refusal of TASK-005 or V1 takes its
   permanent route, because it is a verdict on the reviewed commit. Both records are in process:
   after a restart only a refusing run of the guard sees its own block on its next attempt, so
   another run merging first could still publish the refused commit ("Governed Merge Binding" 7,
   "Shared Working Tree" 5).
7. **Post-merge refusals are permanent** (owner question 6). A merge whose result changes a
   governed path the pre-merge evaluation did not cover takes `merge_failed` on every attempt.
   This happens under git's **default** configuration too: rename following carries a run's edit
   of a path that `main` has since renamed into a governed tree (`docs/x.yaml` →
   `workflows/x.yaml`) onto the governed name, which the diff from the merge base cannot see;
   non-default merge configuration (`merge.directoryRenames`, custom merge drivers) is the other
   source. The remedy is the author's, and the refusal names it: update the run branch from
   `main` and resubmit — the merge base moves, the path is evaluated, and the merge is held for an
   operator decision. Merges that can never be evaluated (an unrelated history with no merge
   base, a diff over the git output limit) are refused permanently on the first attempt the same
   way; only failures that may be transient keep the run `committing`.
8. **Held runs have no timeout.** Cancel is not allowed from `committing`; an operator approves or
   rejects. The decision API runs the approved merge inside the request. Re-evaluating a held run
   on every scheduler tick re-emits `governance.merge_held` under the same event id (the event log
   and delivery queue de-duplicate it; live SSE listeners can see the repeat). A hold is not a
   merge attempt and never counts against TASK-012's retry budget, and the budget never abandons a
   held run (INIT-023 wave 4 integration). A transient failure *before* the gate on a held run's
   tick (the bound commit's check, the status lifecycle's reads) is a counted attempt, so after an
   approval the approved merge's first transient failure can exhaust the budget.
9. **Decision races are closed within one process only.** Every merge attempt of a run (guard
   on) and every decision on it hold the run's in-process governance stripe: a reject that arrives
   while an approved merge is in flight waits, then answers `409` (the merge won) if the merge
   completed or reached the local `main`; an approve can never recreate an approval a reject
   revoked. Across processes sharing one database (an embedder running replicas): the approval is
   written only while the run is `committing` (`MergeCommitMetaIfStatus`) and the reject revokes
   again after its route, so a late approval still cannot survive a reject — but a reject served
   by another process while an approved merge is in flight is not ordered with it (the merge may
   publish while the reject routes the run back), and a reject that lost to a merge completed in
   another process still empties the approval keys, so that merge loses its projection
   provenance (fail-safe: a relaxation it carried is refused until re-affirmed). Closing that
   needs a database lock per run. Stores without the atomic `MergeCommitMeta` /
   `MergeCommitMetaIfStatus` capabilities (an embedder's `RunStore`) use a read-modify-write,
   which can drop a concurrent engine write to the same run's reserved keys;
   `*store.PostgresStore` has both.
10. **Symlinks.** A symlink that is itself a guarded path is a governed change, but edits to the
   target of a symlink already on `main` that points outside the guarded trees are not. A symlink
   inside the run's own evidence directory is exempt like any evidence file. Spine's projection
   reads by git path and does not follow links; an embedder's runner may.
11. **Approval records are not branch-scoped.** A consumed approval names the engine's merge
    target (`main`); the projection proves landing by reachability from its own authoritative head.
12. **The lifecycle exemption's separation-of-duties check** refuses a reviewer who started the
    run (recorded at start in `spine.governance.started_by`; a run with no recorded starter gets
    no exemption) or who is a run author in the engine's separation-of-duties sense (bound to any
    author step execution of the run, in any status, or holding an assignment row of one). It
    inherits that definition's residual: an assign-bound execution reset by restart recovery and
    rebound to someone else leaves no record of the first actor (no append-only binding ledger
    without a schema change).
13. **The consumption-marker window holds the sync.** The marker is written after the merge moves
    the local `main` and before the push, and in single-workspace `spine serve` the projection
    follows the local `main` without being serialised with the merge (TASK-012's worktree lock
    covers git writers, not the projection). A sync that finds a protection removal at the head
    with no matching record, while a run is still `committing` with an approval whose commit is
    on the head and no reachable marker, therefore holds (`error`, the rules untouched, retried)
    instead of refusing — a refusal would advance past the approved merge, and with bootstrap rows
    installed even a full rebuild would then take the late record for a replay. Residual: while
    such a run stays in that state (its marker write and its take-back both failed, so governed
    merges are blocked; or its approved commit reached `main` through another merge), a pending
    protection removal keeps the sync held.
14. **An editing override push vouches for the whole file it leaves.** Owner question 8 counts an
    override push that changed `.spine/branch-protection.yaml` — the documented re-affirmation —
    so if `main` already carries an unauthorised relaxation (refused, rules kept), an operator's
    override push that edits the file for another reason (adds a rule) installs it too. Operators
    should compare the installed rules with the file (the `branch_protection.removal_refused`
    event names both) before an override push to it. An approved governed merge, by contrast,
    counts only for the kinds its own diff removed, or that a chain of earlier authorised states
    accounts for (ADR-016 §6; item 15).
15. **Several operator acts in one sync window are judged as a chain** (review round 2). When two
    or more authorised changes to `.spine/branch-protection.yaml` reach `main` before one
    projection install (two approvals inside one poll, a merge landing while the sync is held),
    the newest record's base config must be, byte for byte, the result of an earlier valid record
    it descends from, and so on back to an act that removed each kind itself. Two residuals:
    - any unauthorised write to the file between two such acts — even a comment-only edit —
      breaks the chain, and the result is refused (rules kept; recovery is an editing override
      push, docs/git-push-guide.md §4);
    - newness is judged against the installed rules, not against later acts in the same window:
      an unauthorised write that restores, byte for byte, the result of an authorised relaxation
      from earlier in the window, after something else in that window had tightened the file
      again, is installed on that relaxation's record. Every install ends the window.

    The chain only uses records the provenance reader returns (`Lookback`, 200 per source).
16. **A merge whose post-merge checks never ran** (INIT-023 wave 4 review; with TASK-017's V1 on
    as well as with this guard). Every merge attempt that can move the local `main` first records
    `spine.merge.unverified` (`"<base> <source>"`) on its run, and the write that records the
    accepted merge clears it before the push. A `MergeBound` error after git moved `main` (the
    trailing `rev-parse` failed, the git child was killed after its ref update) is taken back in
    the merge block (a `main` that cannot be checked after the error keeps the run `committing`,
    even on a permanent error); a run that still carries the record — the process died inside the block (on
    `SIGTERM` the scheduler and the HTTP server do not wait for one), or the take-back failed — has
    the merge taken back at the start of its next attempt, which merges and checks again. Before,
    the next attempt found `main` up to date and published the unverified merge. What remains: the
    record is on that run only, so between a restart and its next attempt another writer (another
    run's governed merge, an in-place write, a run start) can build on the unverified merge, and
    its push publishes it. The run's next attempt then finds the merge buried: it refuses as
    unavailable (Error log), keeps the record, blocks governed merges on the merge with the guard
    on (item 6), and the retry budget never abandons the run, until an operator resets the local
    `main` so it no longer contains the merge (and, if another writer already pushed it, the
    published branch too). Cancel is not allowed from `committing`, so there is no other way out:
    the engine never accepts the unverified merge on its own. A buried merge the engine cannot
    locate within the replay walk limit refuses the same way.
17. **A stale hold is cleared only by an attempt the gate allows.** A run held for a decision
    whose commits then reach `main` another way (an operator lands the run branch, another
    approved run's branch carried them) is no longer held; the next attempt the gate allows clears
    the hold, and the retry budget applies again. While every attempt fails before the gate (a
    transient check of the bound commit), the hold stays and the run stays `committing`. A decision
    on a run whose refused merge is still blocked (item 6) reports it: `GET /runs/{run_id}/governance`
    shows `blocked`, a reject is `409` until the run's own retry has taken the merge back, and an
    approve whose merge ended blocked says so in `merge_error`.
18. **The workflow-directory backstop is broad on one side and short on the other** (INIT-024
    TASK-002, review finding N-5). Every decision adds the orchestrator's
    `<artifacts_dir>/workflows/` and every changed `*.yaml` / `*.yml` file that sits directly in
    a directory named `workflows` (any depth, case-folded) to the guarded set — the files
    themselves, not their directory — so an embedder that configured the projection's
    `artifacts_dir` without `WorkflowDirs` or `WithArtifactsDir` still holds a new definition.
    The cost: a YAML file directly in any `workflows` directory that is not a Spine definition
    (`.github/workflows/ci.yml`, a documentation folder) needs an operator approval; scripts,
    READMEs and nested files changed beside it are not held by the backstop. There is no switch
    to turn the backstop off while the guard is on (INIT-024 TASK-002 owner question 3). The residual: a nested file (`<artifacts_dir>/workflows/steps/x.yaml`) under a
    projection directory that neither `WorkflowDirs` nor the orchestrator names merges unheld.
    The projection does not install nested files as definitions, but an embedder's runner may
    read them (SMP's step profiles live under the root `workflows/steps/`, which the default
    `workflows/` entry covers). Pass `projection.WorkflowDirs(artifactsDir)` or call
    `WithArtifactsDir` to close it.

### Projection Sync: What INIT-023 TASK-011 Leaves Open
- **Workspace repositories are not repaired** (INIT-024 TASK-002). A workflow file that carries a
  second YAML document with content is refused from this release on: the first sync of each
  process re-checks every installed row against the file at head and quarantines it (no new run
  binds the workflow) until the second document is removed. Copies of the three shipped creation
  workflows made before the release keep `timeout_outcome: valid` on their `validate` step until
  an operator edits them (CHANGELOG, INIT-024 TASK-002).
- **A run bound before the upgrade to a two-document workflow file is stranded in standalone**
  (INIT-024 TASK-002). `spine serve` loads a run's workflow from git (`GitWorkflowLoader`),
  re-parsing the file at the commit the run is pinned to on every action that needs it (assign,
  claim, submit, merge), and that file is refused now. Removing the second document on `main` does
  not change the pinned commit: cancel the run (cancelling does not load the workflow; not allowed
  from `committing`) and start it again. Projection-backed reads are unaffected — the scheduler's
  timeout and recovery scans, and SMP's projection workflow loader, read the stored row by path,
  which the projection keeps (quarantined, at its old `source_commit`).
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
- **Several projection instances over one store share `sync_state` by last writer wins.** This
  happens with SMP replicas, a re-warmed workspace, or a provisioning rebuild next to the sync loop.
  Each sync's final `idle` or `error` write is unconditional, so it can replace a status or
  baseline another instance wrote while it ran.
  - **What it costs:** a rebuild in flight on one instance can briefly show as `idle` over a
    half-written projection. A workflow row that a stale-head reconciliation deleted can be lost
    if the instance that projected it finishes a whole sync during that reconciliation; the row
    comes back when its file next changes or on a full rebuild.
  - **What is narrowed:** the reconciliation's own baseline reset moves only `last_synced_commit`
    (`SetSyncBaseline`, codex 6.1 catch-up), so it no longer erases a concurrent status. A custom
    `store.Store` without that capability gets the plain `idle@head` write.
  - **Follow-up:** a compare-and-set on every `sync_state` write, or a lock across instances.
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
  forward it gets the historical read: a successful read is trusted, and every read failure other
  than the client's own refusals is infrastructure. Since the INIT-023 wave 4 follow-up
  `*git.CLIClient.ReadFile` itself refuses a gitlink or a directory (`git.ErrNotBlob`) — and,
  since the review of that follow-up, a file entry whose recorded object is a tag, a tree or a
  commit, which the tree types as a blob from its mode — and the projection describes that refusal
  as the entry it is, so a decorator over it no longer reads a gitlink's recorded blob or a tag's
  target; the gap remains only for a `GitClient` whose `ReadFile` is not backed by
  `*git.CLIClient` (SMP's `gitread` read at SMP `main` 1ae9b0e3 still peels a tag; CHANGELOG).
- **A gitlink at an artifact path is refused as not a file** (INIT-023 wave 4 follow-up; closed
  for every reader built on `*git.CLIClient`). Until then a gitlink whose recorded id names a blob
  in the repository was read as that blob — by artifact discovery, `artifact.Service` reads and
  branch validation — while diffs and repository viewers showed a submodule pointer. `ReadFile`
  now types the entry from its mode before reading anything: discovery records a `not_a_file`
  refusal (and removes a governed row the gitlink replaces), branch validation fails the file, and
  reads answer 404. The same holds for a regular-file entry that records the id of an annotated
  tag (`git cat-file blob` peeled it to its target blob, while diffs show the tag object's text), a
  tree or a commit (review of the follow-up): `ReadFile` reads the recorded object's own type and
  refuses anything but a blob. Refusing gitlink entries, and file entries whose object is not a
  blob, in a governed repository at the push and merge boundary would also keep them out of `main`
  (follow-up). The default receive config accepts such a push (it checks only that the objects
  exist); `receive.fsckObjects` refused one carrying a file entry that records a tag in a check
  with git 2.54, but neither Spine nor SMP sets it.
- **A first full rebuild that was interrupted** (the process stopped while `sync_state` was
  `rebuilding` with no synced commit) is not retried automatically, because it cannot be told apart
  from a first rebuild still running on another instance; run `POST /api/v1/system/rebuild`. A first
  rebuild that *fails* records `error` and is retried by the next sync.

### Artifact Status Lifecycle: What INIT-023 TASK-017 Leaves Open

The governed merge judges artifact-status changes against the declared lifecycle
(`architecture/engine-state-machine.md` §5A). What it does not cover:

- **Embedders get it only by opting in.** `Orchestrator.WithStatusLifecycle`,
  `workflow.Service.WithStatusLifecycle` and `validation.WithBlockingStatusConflicts` are off by
  default; `spine serve` turns all three on. SMP adopts them in SMP INIT-027 EPIC-004 TASK-011.
- **Direct authoritative writes are not lifecycle-checked**: artifact API create/update on `main`
  and the task governance actions (`handlers_tasks.go`), which are operator-authority paths behind
  branch protection. Their status rewrite uses the same byte-preserving regex as the cascade and
  does not re-parse its result (follow-up).
- **Deleting an artifact the run added on the same branch is not checked** (it never existed on
  `main`), and that includes a planning run's own additions. Deleting the run's TaskPath, and any
  artifact the run does not own, is refused.
- **Undeclared statuses already on `main`** (`Architecture: Active`, `Product: Draft for Review`,
  `ADR: Superseded by ADR-027` exist in real repositories; `artifact.Parse` never checked status
  values) are kept as they are: a body edit merges (raw comparison), a run that *normalises* one
  it does not own is refused `non_owned_status_change`, and a run that owns one is refused
  `undeclared_status`. Normalising needs a direct authoritative write.
- **The status-conflict merge gate reads the projection**, so it can lag `main` by one sync
  (standalone polls every 30 s). Within that window an epic completion can be refused
  `status_conflict` although `main` is consistent; an `epic-lifecycle` run has no publish step, so
  such a refusal fails the run and the author starts a new one. Children the same run adds are
  invisible to the gate, so an `epic-lifecycle` run that also adds a `Pending` task under the epic
  completes it (SC-002 after the fact). Evaluating the rules from the bound tree needs a listing
  port on `GitOperator` (follow-up).
- **A violation the author cannot fix on the branch loops in a publish-step workflow.** When the
  workflow's own target is illegal from `main`'s state (for example `task-default` started on a
  task that is `Completed` on `main`) or the merge-base status is undeclared, every attempt routes
  back to `execute` (owner question 10, default). The step classification and the outcome detail
  name the cause.
- **Link edits that change blocking are not status changes.** A run can still edit a non-owned
  task's `blocked_by` links, which un-blocks or re-blocks it with no status transition; the
  reviewer sees the edit (follow-up).
- **Artifact `id`s are not unique by construction.** A run can add a new path carrying an existing
  `id` with an initial status; the projection is keyed by path (follow-up with the ID-allocation
  work).
- **Exotic but valid status encodings on an owned artifact are refused** (`status_rewrite_failed`):
  a quoted key, `status : X`, a complex key, a continuation line, or an empty value followed by
  another key. The byte-preserving writer cannot rewrite them; the author writes a plain
  `status: X` line. A YAML-node writer would lift this (follow-up).
- **An unreadable Markdown file in a run's diff refuses the merge** (`unreadable_artifact`): a
  `.md` inside the artifact scope over the read limit (4 MiB), a non-blob entry at an artifact path,
  or a listed path that does not read. This is permanent by design — a retry could never succeed.
- **A gitlink whose recorded id names a blob in the repository** was read as that blob, exactly
  as the projection read it; since the INIT-023 wave 4 follow-up `ReadFile` refuses it as not a
  file (`git.ErrNotBlob`), so it is an `unreadable_artifact` like any other non-blob entry at an
  artifact path (see "Projection Sync" above).
- **A run branch with more than one merge-base with `main`** (a criss-cross history: the branch
  merged a commit of another run's branch that `main` merged separately) is judged against git's
  newest merge-base. A status change the other merge-base carries is then attributed to the run and
  refused (`non_owned_status_change` on an artifact the run did not touch) — fail closed; merging
  `main` into the run branch gives it one merge-base again. Whatever the plan accepts, the merge
  result is judged against `main` before any push (§5A.3), so nothing unjudged lands. The
  governance guard (TASK-006) lists *every* merge base (`engine.MergeBaseLister`, `git merge-base
  --all`) because it decides on the pre-merge diff alone; the lifecycle deliberately keeps the
  single base, also where the operator offers the list (INIT-023 wave 4 integration): its
  base-independent check is the merge-result judgement, judging against several bases would only
  add refusals that check already makes or that `main` does not support, and the verdict would
  then depend on the embedder's operator (SMP's has no lister yet). Both fail closed.
- **The merge-result check pairs renames with git's rename detection** between `main` before the
  merge and the merge result. A non-owned artifact the branch renamed and that `main` (or the
  branch) edited so heavily that git no longer pairs the two (under 50 % similar) reads as a
  deletion plus an addition and is refused; the author re-does the rename on a branch from current
  `main`.
- **A rename `main` made of the run's TaskPath after the fork** merges with the status change
  landing under `main`'s name; `run.TaskPath` keeps the name the run started with.
- **A merge whose merge-base moved is retried, not refused**: when `main` gains commits of the
  bound history between the judgement and the merge, the merge is taken back and the attempt
  retried. Each such retry, and each retryable failure judging the bound commit, is a counted
  attempt, so TASK-012's retry budget bounds them (INIT-023 wave 4 integration).
- **The merge-result check runs after the code repositories merged** (code-repository-first
  ordering) and after the local merge, like TASK-005's structural check: a refused merge is taken
  back off the local `main` (to exactly the base `main` held) and never pushed. With auto-push off
  the local `main` briefly held it; on the standalone shared tree that is inside TASK-012's merge
  block, so no later holder of the tree builds on it, and a take-back that fails keeps the tree
  refused until it completes (and, with the governance guard on, blocks every governed merge;
  "Governance-Path Guard" 6).
- **Runs in flight at the upgrade** whose branch carries status edits on artifacts they do not own
  are refused at the merge; a publish-step workflow routes them back to `execute`.

### Supply Chain: What INIT-024 TASK-009 Leaves Open

CI pins every action and image, scans for reachable advisories and enforces toolchain parity
(`dev/pincheck`, CI jobs `vuln` and `toolchain-parity`). What it does not cover:

- **Repository settings are owner actions** (INIT-024 §7d): a `main` ruleset (PR and CI required,
  no force push), approval for all outside contributors, and "allowed actions: selected" with SHA
  pinning required. Until the ruleset exists, nothing makes these CI jobs required.
- **Dependabot does not move the toolchain.** It proposes a new golang image for the Dockerfile but
  never edits go.mod's `toolchain` line, the `ci.yml` git-floor container, the Makefile's
  `DOCKER_GO` or the setup-go `go-version`s. `toolchain-parity` fails that PR until all of them
  agree, and that failure is intended. The same goes for a compose postgres bump: the `ci.yml`
  postgres service must be moved to the same ref by hand. The govulncheck version (`ci.yml`,
  Makefile) is also moved by hand.
- **The scan of shell commands is best-effort.** In workflow `run:` scripts and Makefiles,
  `dev/pincheck` reads `docker run|create|pull` (and `docker container run|create`, `docker image
  pull`) and takes the first non-flag word as the image. An unknown boolean flag makes it read the
  next word as the image, which is a refusal, not a pass. It does not see:
  - images in other scripts (`dev/*.sh`, scripts a step runs from a file);
  - commands assembled at run time (`eval`, `sh -c "$CMD"`);
  - images pulled by other tools (`crane`, `skopeo`, `ctr`, `podman`).

  An image passed through a variable defined elsewhere is refused as computed, not resolved.
- **govulncheck scans the default build only.** Code behind the `integration` and `scenario` tags
  and `_test.go` files is not analysed. A new upstream advisory fails every PR until it is fixed
  or the job is changed. This is the default fail-closed policy (TASK-009 owner question 2).
- **The runtime image's packages are not pinned.** The debian digest fixes the base, but `apt-get
  install git ca-certificates` resolves at build time. There is no image vulnerability scan, SBOM
  or signing: Spine has no image publishing pipeline.

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
  database and auth. In shared mode a workspace whose ServiceSet has no auth service answers the
  uniform `401` instead, with the cause logged (INIT-024 TASK-007; see "Session and Auth Hygiene").
- **`SPINE_DEV_MODE` (API only).** `1`/`true` lets unauthenticated requests through `authorize()`
  on `/api/v1`. It is refused at startup only when `SPINE_ENV=production`, so it is accepted with
  `SPINE_ENV=staging` (INIT-023 TASK-015 owner question 5: unchanged, pending the owner). An unset
  or unknown `SPINE_ENV` no longer reaches this check: `spine serve` refuses to start without a
  known value (INIT-024 TASK-007). Since INIT-023 TASK-015 it has **no effect on `/git`**; it used to drop git
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
- Since INIT-024 TASK-006 (review finding N-2) the bypass is also **bound to a workspace**: a
  trusted address reads only the workspaces its `SPINE_GIT_HTTP_TRUSTED_CIDRS` entry names
  (`ws-a=172.28.1.10/32;ws-b=172.28.1.11/32`), and the trust is looked up under the workspace the
  request resolved to. In shared mode (`db`, `platform-binding`) the bare list refuses to start
  (owner question 1 default, pending the owner); in single mode it is bound to the one workspace.
  Any other workspace, and any workspace that does not resolve, answers the uniform `401`. CIDRs
  bound to different workspaces that overlap are accepted (one runner may serve two workspaces)
  with a startup warning.
- A caller without a valid token for the workspace — none, a junk or non-bearer header, an expired
  or revoked token, another workspace's token — gets that same uniform `401` for every `/git`
  request, read or push, with push on or off, whatever the workspace's state (INIT-024 TASK-006;
  with push off the `403` naming `SPINE_GIT_RECEIVE_PACK_ENABLED` is shown only to an
  authenticated caller).
- **Residual: the binding is per address, not per job.** A runner address bound to a workspace
  reads that workspace's whole repository — every branch, not only the run branch of the job it is
  executing — and any process or job on that host can do the same. Two tenants that share a runner
  address (or a runner host) share each other's repositories. Give each tenant its own runner
  addresses. A job-scoped clone token, after which trusted CIDRs could be dropped, is a follow-up
  (TASK-006 owner question 2).
- **Only the served repository's protocol paths reach `git-http-backend`** (INIT-024 TASK-004,
  from TASK-006's security review). The handler's read and push classification used to match path
  suffixes, and `git-http-backend` serves the directory a request path names before its protocol
  part as a repository: a path prefix inside the served tree reached it as a repository — another
  git directory inside the working tree — also token-less for a trusted peer, within the bound
  workspace only. Now a read or a push is exactly one of the paths `git-http-backend` serves at the
  repository root (`/info/refs`, `/git-upload-pack`, `/git-receive-pack`, `/HEAD`,
  `/objects/info/{packs,alternates,http-alternates}`, a loose object or a pack/idx by its full id),
  relative to the repository the gateway resolved, and the gateway classifies push and read on the
  same path it hands the handler. Any other git path — a prefix, a `.` or `..` segment, a doubled
  or trailing slash inside it — is refused with `403` before `git-http-backend` runs; TASK-006's
  `..` residual (refused only by `git-http-backend`'s own alias check, with a `500`) is closed with
  it. Through the gateway an empty path segment where the repository segment goes (a doubled slash
  right after the workspace) is an empty repository id, which names the primary repository as it
  always has, so that request is the primary's own protocol path.
- **Residual: `SPINE_GIT_HTTP_DEV_NO_AUTH` is not workspace-scoped.** The development flag (accepted
  only with `SPINE_ENV=development`) lets every client read every workspace, in every mode.
- **Residual: a single-mode server without an auth service tells workspaces apart.** The one
  known exception to the uniform `401` above, in single mode only: when the server has no auth
  service, a resolved workspace answers `503` "authentication not configured" to a caller
  presenting a bearer, where a missing workspace answers `401` — a misconfiguration signal kept on
  purpose for operators. In shared mode a workspace whose ServiceSet has no auth service answers
  the uniform `401` like every other refusal (INIT-024 TASK-007); the cause is logged at `Error`.
- **Residual: TCP-level proxies are invisible.** A proxy that relays connections without adding a
  header — Docker's userland proxy for a published port, which connects from the bridge gateway
  (`x.y.z.1`), or an L4 load balancer — looks like a direct peer. Never put such an address in
  `SPINE_GIT_HTTP_TRUSTED_CIDRS`; the compose file no longer trusts its whole bridge subnet, and
  `docker-compose.override.yaml.example` shows runner-only recipes.
- **Git capacity is shared fairly, within one process (INIT-024 TASK-005, review finding N-13).**
  A git request takes one slot from three caps: its principal's — the (workspace, actor) of a token
  request, or the (workspace, peer address) of a token-less read, never a shared empty actor — its
  workspace's, and the process's (`SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR`,
  `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_WORKSPACE`, `SPINE_GIT_HTTP_MAX_CONCURRENT`); a full cap
  answers `503` with `Retry-After: 5`. Defaults: in shared mode 2 / 4 / 8; on a single-workspace
  server the per-workspace and per-actor caps default to the process-wide one (8), which is then the
  server's capacity (it was one cap of 5) — an explicitly lower per-workspace cap binds, with a
  startup warning when the process-wide cap is set above it. A push takes its slot only after its
  command section has arrived (within `SPINE_GIT_COMMAND_READ_TIMEOUT`, default 10 s) and been
  admitted, so a client that stalls there holds none; the command sections being read and admitted
  at once are capped too, at twice the slot caps per principal and per workspace (one more push is
  answered `503` at once). Residuals: the caps are per replica; each push in admission may buffer up
  to the 1 MiB command-section cap and evaluates admission and branch protection for every ref it
  names — there is no separate ref-count cap, the 1 MiB bounds it; a fetch whose request body
  stalls, and a push whose pack stalls, hold their own principal's slot until the server's 30 s
  `ReadTimeout`; members of two workspaces can fill the process-wide ceiling between them; in shared
  mode runners that share one token share one actor's slots (2 by default) — raise
  `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR` for them; the per-client-IP rate limiter is unchanged.
- **The command-read cut needs a ResponseWriter that reaches the connection.** A stalled command
  section is cut by moving the connection's read deadline (`http.ResponseController`); the
  gateway's middleware writers support it. Behind a writer that cannot, the fallback — closing the
  request body — is best-effort: a net/http server body is then read until the server's
  `ReadTimeout` (still without a git slot).

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

### Session and Auth Hygiene (INIT-024 TASK-007)
- **SSE revalidation window (N-14).** An open `/api/v1/events/stream` re-validates its bearer —
  revocation, expiry, actor status and the `events.read` permission — on every heartbeat, 30 s
  (owner question 1 default, pending the owner), and closes on failure. Events broadcast between
  the revocation and the next heartbeat are still delivered: the window is at most one interval.
  A token-store error or timeout during the check closes the stream too (fail closed); the client
  reconnects. Proactive close through an event bus is out of scope.
- **Pre-authentication builds (N-15).** In shared mode an `/api/v1` request without a well-formed
  bearer is refused before the workspace is even resolved — no registry query, no platform binding
  fetch, no ServiceSet build — and every refusal before the bearer is validated is the same `401`.
  A well-formed but invalid bearer still resolves the workspace and builds its services: the
  bearer can only be checked against that workspace's own token store. That build is not cheap or
  side-effect free — the first one opens the workspace's database pool, sweeps its subscription
  secrets, seeds the bootstrap `smp-admin` rows when `SMP_ADMIN_TOKEN` is set and starts the
  workspace's event delivery — and it is **not** bounded by idle eviction: every `Get`/`Release`
  refreshes the entry's last access, so one junk bearer per workspace per idle timeout (10 min by
  default) keeps a workspace built indefinitely. The per-IP rate limiter bounds the request rate,
  not the number of workspaces kept warm. In `db` mode this adds little — the projection sync
  already builds every active workspace on each poll — but in `platform-binding` mode it lets an
  anonymous caller who knows workspace ids keep them loaded. A per-workspace build rate limit or a
  negative cache for failed builds is a follow-up (TASK-007 review round 1, finding 3).
- **`/git` still resolves before its gate.** `/git/{workspace}/...` resolves the workspace before
  it looks at the bearer, because trusted-CIDR and development reads are token-less and need the
  workspace to decide; in `platform-binding` mode a header-less request therefore still costs a
  binding fetch for a workspace id the cache does not hold. The answers are uniform (below); the
  fetch is not.
- **Timing.** Response *timing* still differs between a missing workspace (no build, no query)
  and an existing one; the refusals are not constant-time.
- **Shared mode answers `401` for an unbuildable workspace (N-15).** A caller whose workspace's
  services fail to build, or whose token store errors, gets the uniform `401` — even with a token
  that is valid there — where it used to get `500`/`503`. The cause is logged at `Warn`/`Error`
  with the workspace id; operators read outages from the logs, not from client status codes.
- **"Verified" evidence (N-17).** `run.status` marks each repository's evidence `verified: true`
  only when it was read from the authoritative branch, and `false` with the run branch as
  `source_ref` otherwise; `unverified_repositories` lists the latter. "Verified" means the record
  passed the run's governed merge, not that the server produced or signed it: a run's own evidence
  directory is excused from the governance-path approval ("Governance-Path Guard" above), so its
  content is still the producer's, reviewed with the run. The merge gate never read this display
  path.
- **`SPINE_ENV` is required (N-16).** `spine serve` and `spine secrets reencrypt` refuse to start
  unless it is `production`, `staging` or `development` (owner question 2 default: refuse rather
  than treat unset as production, pending the owner). `staging` and `development` still turn every
  production gate off; choosing them is now explicit, not a fallback.

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
- **Status: enforced (INIT-024 TASK-005, review finding N-8).** With receive-pack on, the git
  endpoint probes each repository's storage on the first push request it serves for it (once per
  repository and process lifetime; reads never run or wait on the probe): in a fresh directory under
  `<git-common-dir>/refs/heads` — where the protected branches' loose ref files live — whose name
  starts with `.` (git's ref iteration, `pack-refs` and the ref advertisement skip it; no ref name
  can address it; it is removed at once), it creates three pairs of names a folding filesystem
  identifies — ASCII case, Unicode NFC/NFD and the Kelvin sign against `k` — and reads them back. A
  repository on storage that folds any pair has receive-pack refused: the push advertisement and the
  push answer `403` naming this requirement before the body is read, and the repository's
  `http.receivepack` is set to `false`. Clones and fetches are served as before (owner question 2).
  A probe that cannot answer (an unwritable `refs/heads` directory) refuses pushes with `500 push
  gate unavailable`; that refusal is reused for 30 seconds before a push probes again. A
  case-sensitive APFS volume is still normalization-insensitive and is refused too. A **reftable**
  repository (`git init --ref-format=reftable`) keeps no ref files and so cannot alias ref names,
  but has no `refs/heads` directory: the probe runs in `refs/` and refuses it on folding storage as
  well (a fail-closed false positive). A deployment that serves pushes from such storage today,
  against this requirement, has them refused after the upgrade; move the repositories to a
  case-sensitive, normalization-sensitive filesystem. SMP's own gateway has no probe yet (SMP
  companion epic).

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
- **Git floor:** git 2.30 or newer for the git client (`rev-parse --end-of-options`); embedders
  call `git.CheckGitVersion` at boot. `spine serve` needs git 2.38 or newer and refuses to start
  below it (INIT-024 TASK-003: a budgeted merge computes its result with `git merge-tree
  --write-tree`; `git.CheckBudgetGitVersion`; "Checkout Budget"). Every shipped image runs 2.39.5
  or later.
- **No DWIM for a missing reserved or home branch** (INIT-024 TASK-004; review finding N-10). A
  bare revision in the reserved namespace (first component a case-fold spelling of `spine`, the
  rule push admission uses: run, planning and divergence branches, `spine/init`) or naming the
  tree's home branch, with no `refs/heads/<n>`, is `not_found` — never resolved by git's DWIM. A
  run branch is usually missing because cleanup deleted it, and the evidence querier still reads it
  for terminal runs; a same-named `refs/tags/<n>` or `refs/<n>` used to answer for it in every
  read (`ReadFile`, `RefSHA`, `Diff`, `ListFiles`, `TreeEntryType`, `MergeBase`, `Log{Since}`,
  `MeasureTree`). Name such a tag in full (`refs/tags/spine/...`) to read it.
- **Residual — user `?ref=` for any other non-branch name still follows DWIM.** A read with
  `?ref=<name>` where `<name>` is not an existing branch, outside the reserved namespace and not
  the home branch, resolves as git always has, so a pushed `refs/<name>` beats
  `refs/tags/<name>`. Name tags in full (`?ref=refs/tags/<name>`). TASK-016 blocks such pushes for
  non-operators. Narrowing DWIM to the reserved and home names keeps `?ref=<tag>` and abbreviated
  ids working on the public API (TASK-004 owner question 1 default: narrow).
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
  buffers more than its limits. `ReadFile` probes the blob's size (`git ls-tree -l` of the exact
  path since the wave 4 follow-up, `git cat-file -s` before) and refuses one over the per-blob
  limit before reading it (`git.DefaultMaxBlobBytes`, 4 MiB — the worktree
  read limit too; `git.WithMaxBlobBytes`), with a permanent error matching `git.ErrBlobTooLarge`
  (API 413). Every other read command — log, rev-parse, merge-base — fails past 16 MiB of output
  (`git.DefaultMaxOutputBytes`, `git.WithMaxOutputBytes`, never below 4 KiB;
  `git.ErrOutputTooLarge`). The listings (`ListFiles`, `ListFilesFunc`) and `Diff` are parsed as
  git writes them (INIT-024 TASK-004) and bounded by what they **keep**, not by what git prints:
  see "Listings bound the paths they keep" below.
  Read commands also run with `GIT_ALLOC_LIMIT` (4 x the per-blob limit, never below 1 MiB, which
  git's own fixed allocations need; `git.WithMaxGitAllocBytes`), so git refuses any single
  allocation over it — it will not inflate a small blob stored as a delta on a huge base — and
  read and diff commands pin git's delta-base
  cache to the per-blob limit (`core.deltaBaseCacheLimit`, passed in `GIT_CONFIG_PARAMETERS`
  after any inherited entries); they run in the C locale so git's refusal is recognised whatever
  the host's language. Any other command's output is held once: the parsed messages share its
  buffer, which grows in powers of two (so while it grows it is briefly held at half and full
  size); a listing holds only the paths it keeps, each copied once. Mutating commands (checkout, merge, commit, add, branch, config, push) keep only the
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
- **Listings bound the paths they keep** (INIT-024 TASK-004; review finding N-6). A listing keeps
  the paths its caller asked for — `ListFiles`' pattern, or `ListFilesFunc`'s predicate, applied
  while git lists — and drops the rest as they arrive; `Diff` keeps every path it returns. What is
  kept (each path plus its NUL) is held to a limit: `git.WithMaxOutputBytes` when the embedder sets
  it; otherwise, for a tree listing, `git.DefaultMaxListingBytes` (~17.2 MiB: the checkout budget's
  default 16 MiB of path bytes, one byte per default entry and 1 MiB of margin), and for a diff,
  which holds both trees' paths, `git.DefaultMaxDiffBytes` (~33.4 MiB: twice that, plus the
  margin); on a client that carries a checkout budget never less than what that budget admits —
  so a tree any budgeted merge admits always lists in full, and one merge's diff always fits. No
  single path has a length limit of its own (review round 1: a per-path cap let one long governed
  path refuse every listing of its tree); one unfinished record is held only up to the room the
  listing has left (at least 4 KiB). Past the limit the listing fails closed with the same
  permanent `git.ErrOutputTooLarge`. Artifact and workflow discovery (`FullRebuild`), the first
  sync's workflow-row sweep and `NextID` keep only the paths they use, through the optional
  `git.PathFilterLister` capability (`git.ListFilesKeeping` falls back to `ListFiles` for a client
  without it), and the branch-protection lookup by its `ListFiles` pattern, so a commit whose listing is huge but which holds few
  governed files projects normally. Before, a merged commit whose `ls-tree -r` printed more than
  16 MiB (about 160k files, or a few thousand deep paths — a few KB as a pack) failed every rebuild
  and every sync's sweep until it was reverted.
- **Listings stop at a record ceiling** (INIT-024 TASK-004 review round 1). With no bound on the
  raw output, a tree of a few small objects whose shared subtrees expand to millions of listed
  paths would cost one parse per path. So a tree listing fails closed (`git.ErrOutputTooLarge`,
  `OutputTooLargeError.Records`) past 4 times the checkout budget's entry count — the client's
  budget, else the default's: 800,000 records — and a diff or the evidence count past twice that,
  whether the records are kept or dropped. The budget counts directories too, so only a tree no
  budgeted checkout would admit reaches it. Callers that want one directory name it
  (`ListFilesUnder`: discovery with an artifacts directory, workflow discovery, `NextID`), and git
  then walks only that subtree; a repository-root discovery lists the whole tree under the ceiling.
- **Residual — a sync window whose changed paths alone exceed the diff limit fails closed.**
  `IncrementalSync` diffs the last synced commit against head, and `Diff` keeps every path, so a
  window that adds or removes more than the limit's worth of paths leaves the sync state `error`
  at the previous commit; every later tick fails the same way. An operator rebuild
  (`POST /api/v1/system/rebuild`, or `FullRebuild`) recovers it — it lists only the governed paths
  — and later windows sync normally. With a checkout budget (`spine serve`) one merge's diff always
  fits the default limit, so a window over it takes many large merges between two syncs, or an
  embedder whose explicit `WithMaxOutputBytes` is smaller. Falling back to a rebuild automatically
  is not done (INIT-024 TASK-004 owner question 1 default) and is a recorded follow-up (INIT-024
  TASK-010's close notes).
- **Residual — more governed candidates than the limit.** A tree whose `.md` and workflow paths
  alone exceed the listing limit (or the record ceiling) still fails `FullRebuild` closed. In
  standalone no governed merge can bring one in: the budget bounds both.
- **`diff` runs without `GIT_ALLOC_LIMIT`.** Rename detection loads both blobs of a candidate
  pair, and a legitimate large rename on `main` must not fail `Diff`; so a large blob already on
  the diff base can still be inflated in the git child while its rename is detected (its
  delta-base cache is pinned like a read command's). The step-output evidence count, whose
  commit pair the run-branch author chooses, no longer goes through `Diff` (INIT-024 TASK-004;
  review finding N-12): `CLIClient.CountChangedPaths` runs `git diff --name-only -z --no-renames`
  as a read command (`GIT_ALLOC_LIMIT`, the pinned cache), which loads no blob, and keeps no path.
  A rename then counts as two changed paths (a delete and an add). An operator without the
  capability (SMP's) keeps counting through `Diff` (owner question 2 default).
- **`Diff` ignores the repository's submodule ignore settings** (INIT-024 TASK-004). In a
  non-bare tree `git diff <a> <b>` honours `submodule.<name>.ignore` in the checked-out
  `.gitmodules` and `diff.ignoreSubmodules` in any config: `ignore = all` hid a gitlink change at a
  governed path from the governance guard, the projection and the status cascade. `Diff` and
  `CountChangedPaths` pass `--ignore-submodules=none`, so every gitlink change is reported
  (`modified`). `ListFiles` (`ls-tree`) was never affected.
- **History records cannot be forged by a commit message** (INIT-024 TASK-004; review finding
  N-11). `Log` and `HasCommitWithTrailer` print each record after a boundary drawn per call from
  `crypto/rand` (128 bits) and require every record to start with a full object id; anything else
  fails the call (permanent, "unparseable log output"), never a skipped record. Before, a message
  holding the fixed `---END---` separator and lines shaped like a record became a second entry of
  `GET /api/v1/query/history`, with whatever id, author, date and trailers it chose.
- **Named-ref window (closed by the wave 4 follow-up).** The size probe and the read are two git
  processes, but the read is `git cat-file blob <oid>` of the object the probe typed and sized, so
  a ref given by *name* that moves between them cannot change what is read. (Until then the read
  was `git show <ref>:<path>` and a moved ref could overrun the probed size into the same refusal,
  with size -1.)
- **Deltas on a large base are refused.** A legitimate artifact stored as a delta on a base larger
  than `GIT_ALLOC_LIMIT` (16 MiB by default) cannot be read and is refused as oversized. The
  refusal says so (`git.BlobTooLargeError.GitAllocLimit`, `git_alloc_limit_bytes` on
  `projection_file_skipped`, and the 413 message names the git allocation limit), because the
  file itself may be well under the per-blob limit.
- **`ReadFile` reads blobs only** (wave 4 follow-up). It used to return whatever `git show
  <rev>:<path>` rendered: a tree path's listing, a gitlink as a commit with its patch (a diff queue
  of millions of small allocations, which `GIT_ALLOC_LIMIT` does not bound: 272 MiB measured by
  SMP), and a path holding `..` or a `^` suffix as a range or a revision. A gitlink or a directory
  is now refused unread (`git.ErrNotBlob`), and the path never reaches a revision expression. A
  file entry is read only when the object it records is a blob (`git cat-file -t <oid>` before
  the read): one recording a tag, a tree or a commit is the same refusal (review of the follow-up).
- **The merge-binding plumbing** (INIT-023 TASK-005's `CommitBinder` methods: `rev-parse
  --verify`, `rev-list --parents`, `merge-base --is-ancestor`, `ls-tree`, the private-index
  `read-tree` / `hash-object` / `update-index` / `write-tree` / `commit-tree`, `update-ref`,
  `read-tree -m -u`, `symbolic-ref`) runs in the C locale with the per-command output limit and the
  bounded stderr (wave 3 integration), but without `GIT_ALLOC_LIMIT` or the pinned delta-base
  cache: the take-back's `read-tree -m -u` materialises the working tree like checkout, and the
  read-only commands parse the commit being merged, which the merge itself then reads without an
  allocation limit.
- **Not covered:** receive-time inflation (`index-pack` reconstructs a pushed delta to hash it;
  `receive.maxInputSize` bounds the pack, not the object). Checkout-time inflation is now bounded
  by the checkout budget, and local mutating commands by a wall-clock deadline (INIT-024 TASK-003:
  both on in `spine serve`, opt-in for embedders; "Checkout Budget" below).
- **Cost:** a `ReadFile` of a file runs three git processes — the entry probe (`ls-tree -l`), the
  object-type check (`cat-file -t`) and the read (`cat-file blob`) — where it ran one before
  TASK-003 (`show`) and two after it; a miss runs one (two when the revision names no tree). So a
  full rebuild of a large repository takes roughly three times as long in git processes as before
  TASK-003. A batched probe (one `cat-file --batch` for the type, size and content) is a follow-up.
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

### Checkout Budget (INIT-024 TASK-003; review finding N-1)
- **Bound.** A small push can name one object from many tree entries; a checkout writes every
  occurrence (a 4,470-byte pack made `EnterBranch` write ~200 MB at `a560c210`, and two levels of
  1000 entries reach terabytes). A `git.CLIClient` that carries a checkout budget now measures the
  tree before every command that writes one — `EnterBranch` (before the worktree's directory
  exists), `Checkout`, `Merge` and `MergeBound`, `ResetBranch`'s `read-tree -m -u` and its
  `read-tree --reset -u` fallback, the shared tree's restore (its `reset --hard` of HEAD and its
  switch to the home branch), and `Clone` (made `--no-checkout`, measured, then `reset --hard`) —
  and refuses one over it with a permanent error matching `git.ErrTreeTooLarge` before anything is
  written or moved. The limits: 200,000 entries (files, links, gitlinks and directories), 2 GiB of
  blob bytes and 16 MiB of summed path bytes (`git.TreeBudget`; owner question 1, defaults pending
  the owner). The measure reads object headers through a streamed `git ls-tree -r -t -l -z` that
  stops at the first crossing, so measuring a bomb costs about its first 200,000 entries.
- **Checkouts write stored bytes** (review round 2). Every budgeted repository carries
  `git.NeutralCheckoutAttributes` — `* -text -ident -filter !eol !working-tree-encoding
  !conflict-marker-size` — as the last line of `<git-common-dir>/info/attributes`, the
  highest-precedence attribute source, so no `.gitattributes` (macros included), operator attribute
  file (`core.attributesFile`, the system file, an earlier line of `info/attributes`) or
  `core.autocrlf` / `core.eol` setting can make a checkout, merge or linked worktree write more
  than the blobs the measure sums; conflict markers are always 7 characters. `spine serve` writes
  the line at start, when it builds a workspace's services, on every push it serves, into every
  budgeted clone and into every code-repository clone it reuses (operator lines are kept), and a
  budgeted measure refuses (`git.ErrNeutralAttributesMissing`, fail closed) a repository whose
  file does not end in it — the last line that is not blank or a comment, by git's own rules
  (only space, tab and CR are blanks). `merge` is left alone, so `binary` and `merge=union` merge
  as before. The writer only writes at the top of a repository (never a repository that merely
  contains the directory it is given). When it first adds the line it brings trees checked out with
  conversions to their stored bytes, by content, leaving real uncommitted changes in place (the
  CHANGELOG has the steps); what each tree kind sees:
  - the shared workspace tree (standalone, home branch): its converted files are rewritten when
    the line is added (`spine serve` start or the workspace build); its real changes are left by
    that step and then, as before, quarantined and reset by the restore around its next sequence;
  - a code-repository clone the pool reuses and a served repository's work tree: only the
    converted files are rewritten; real changes (tracked edits, staged changes, untracked files)
    stay as they were — a recorded path keeps the bytes it was checked out with (CRLF, an expanded
    `$Id$`), so committing it as is records those bytes;
  - a linked worktree: marked (its real changes recorded) when the line is added, renormalised
    when a call is made on its own path (one Spine never calls on — an operator's — stays
    converted, with its mark); its record was never compared under the line, so that call
    quarantines the tracked state first (`git stash create`, SHA at Warn) before rewriting the
    unrecorded differences — an edit made there after the line is in the quarantine, not in the
    tree; a worktree that cannot be marked is logged at Warn and left as it is;
  - a renormalisation stopped past the line (an I/O error or a crash: every other failure is
    checked before the line) is retried by the next call, which quarantines the tracked state
    first (`git stash create`, SHA at Warn) and then rewrites the unrecorded differences — an edit
    made between the two calls is in the quarantine, not in the tree;
  - a tree whose marker has no record (an earlier build of this change wrote it, or it cannot be
    read: damaged, torn, or past its bounds — the marker is untrusted input): the same quarantine
    first, then every unrecorded unstaged difference is rewritten; staged changes are still never
    rewritten;
  - only the calling tree's hold is taken while the line is added; a sequence running in another
    worktree of the same repository at that moment runs on under the new attributes (one-time).
  A check that fails before the line — HEAD over the budget, a torn or unreadable HEAD, a cancelled
  request — leaves no line and no marker. A failure after the work tree's record but before the line
  is in place (the worktree listing failing, a cancel while another worktree is marked, the
  `info/attributes` write failing) leaves a marker without the line; the next call that adds the
  line records the tree again and renormalises it. Either way the repository's budgeted checkouts,
  merges and pushes
  keep refusing (`ErrNeutralAttributesMissing`; the git endpoint answers `500 push gate
  unavailable`) until a call succeeds: a tree already over the budget when the line is first added
  needs the limit raised for that call (or the tree reduced outside Spine), not a repair push or
  merge, which the missing line refuses too. A work tree with more uncommitted changes than a
  record holds (2,097,152 paths) is not given the line either (the build or push logs why). The
  renormalisation's `git status` is streamed and held to a tree listing's bounds ("Git Read
  Bounds": the listing limit, `SPINE_GIT_TREE_MAX_PATH_BYTES` plus one byte per
  `SPINE_GIT_TREE_MAX_ENTRIES` plus 1 MiB, and the record ceiling, four times the entry budget), so
  every tracked path of a tree the budget admits fits. A status that is larger anyway (more index
  entries than HEAD's tree, such as staged additions past the margin) fails closed:
  before the line it refuses the call (no line, until the work tree has fewer differences); past
  the line the tree stays marked, the shared workspace tree is repaired by the restore around its
  next sequence (which quarantines and resets it), and a code-repository clone of the git pool by
  deleting the clone, so the pool clones it again (a budgeted clone gets the line before its
  checkout). A path marked
  assume-unchanged or skip-worktree is trusted as git trusts it: not compared, not rewritten. A
  shared-mode workspace build waits for the tree's hold (to add the line, or to renormalise a
  marked tree) at most its init deadline (`SPINE_WS_POOL_INIT_TIMEOUT`; "Shared Working Tree",
  item 9): past it the call gives up, the workspace is served, and the next build retries.
  Not neutralised (operator configuration): a host `merge.<name>.driver` named by a `merge=`
  attribute, and `pull.twohead` (Spine never pulls). Consequences: an LFS pointer stays a pointer,
  `git add` keeps CRLF, does not collapse `$Id: … $`, runs no clean filter (a file Spine writes at
  an LFS path on a git-lfs host is stored as its full content) and does not re-encode, and a tree
  checked out with conversions
  before the upgrade is brought to its stored bytes once, when the line is first added (retried
  until it succeeds; above).
- **A merge is measured by its result** (review rounds 1-2). After the switch to the target — in
  the working tree `git merge` will see, whose merge attributes it reads — `git merge-tree
  --write-tree` computes the tree the merge will leave (merge-ort; conflict markers included) on
  the target tip read inside the working-tree hold the merge takes, and that tree is measured on
  every limit before `git merge` runs; the tip is checked again after the switch and after the
  measure (a moved tip is a transient refusal). The target is measured on its own before the
  switch when the switch is a checkout (HEAD on another branch); an up-to-date replay (result =
  target tree) is not measured. merge-tree and the measured `git merge` run under
  `GIT_ALLOC_LIMIT` = max(`MaxTotalBytes`, the read allocation limit) — 2 GiB by default; a merge
  that needs one allocation over it is refused (`git.MergeAllocRefusedMessage`). (The 16 MiB read
  limit alone would refuse a merge of any packed file over 16 MiB.) `GIT_ALLOC_LIMIT` caps single
  allocations, not the git child's memory: a content merge of a large file changed on both sides
  holds several copies of it at once — about 10x the file (a 115 MB file took merge-tree to
  1.2 GB RSS), so up to ~10 GiB near git's own ~1 GiB per-file diff cap. This is pre-existing
  (`git merge` itself needs the same); the operator controls are the server's memory limit
  (container / cgroup), a `SPINE_GIT_TREE_MAX_BYTES` whose ~10x fits it, and keeping very large
  files out of merged branches. So two merges from one base can
  no longer walk `main` over the budget, and a repair merge that brings an over-budget `main` back
  under it is admitted. This needs git >= 2.38: `spine serve` refuses to start on an older git, and
  a budgeted library merge on one is refused.
- **On in standalone, off for embedders.** `spine serve` installs the budget and a 2-minute
  deadline on every local mutating git command as the process default
  (`git.SetDefaultCheckoutLimits`; `SPINE_GIT_TREE_MAX_ENTRIES`, `SPINE_GIT_TREE_MAX_BYTES`,
  `SPINE_GIT_TREE_MAX_PATH_BYTES`, `SPINE_GIT_MUTATE_TIMEOUT` set other positive values; there is
  no off switch). A library client built without `git.WithTreeBudget` / `git.WithMutateTimeout` in
  a process that never set the default — every embedder, SMP included — measures nothing and runs
  exactly as before (INIT-024 rule 2). SMP's own checkouts and merges (`gitread.CheckTreeDiff`,
  `boundRunBranchCheckout`, `RealGitOperator.MergeBound`, its `/git/{workspace}` gateway) are SMP's
  to bound (SMP INIT-027 EPIC-005); `git.MeasureTree` and `git.TreeBudget.Check` are the API for
  it.
- **Pushes are measured at pre-receive.** With receive-pack on, the git HTTP endpoint installs a
  pre-receive hook (`<git-dir>/hooks/pre-receive`, running the hidden `spine git-hook pre-receive`
  of the same binary) and pins `core.hooksPath` to its directory for the receive-pack it runs. Every
  pushed tip is typed in one `git cat-file --batch-check` after the pack is unpacked into git's
  quarantine and before any ref moves: a commit (or a tag of one) has its tree measured, each tree
  once however many tips share it; a blob or tree tip (a tag of a GPG key, say) is admitted
  unmeasured, since nothing Spine runs checks one out; a delete is skipped. Over the budget, or
  not measurable (a missing object, the request's `OpTimeout`), the whole push is refused with a
  `remote:` line naming the limit, its variable and the size it needs, and the quarantined objects
  are discarded. Consequences: a pre-receive hook Spine did not write is never replaced — pushes
  are refused until it is moved aside; hooks kept under a custom `core.hooksPath` (repository,
  global or system config) do not run for pushes served by Spine, which logs one Warn per
  repository naming them.
- **Not atomic with a checkout by name.** A branch measured by name is looked up, measured and
  then checked out by git, which reads the ref again; a branch that moves in between is written at
  its new tip. In standalone every pushed tip passes the same budget at pre-receive, so a branch
  cannot move to an over-budget tree there; a merge re-checks the tip it measured, and
  `MergeBound` merges the exact measured commit. A writer outside the process that moves the
  checked-out branch between the merge's last tip check and `git merge` (git refuses pushes to it
  by default), or that edits `info/attributes` behind the server, is not covered.
- **The budget bounds each checkout, not the checkouts in flight** (INIT-024 close, cross-task
  review; follow-up). Every checkout, merge and push is measured on its own, but nothing bounds
  the total bytes of the checkouts in progress at the same time, for one branch or across
  branches. Concurrent branch-scoped writes each write a full within-budget tree under the
  server's temporary directory (`TMPDIR`) before they finish — including concurrent writes to the
  same run branch, and writes git then refuses because that branch is already checked out — so a
  burst of them needs a multiple of the budget on that filesystem. There is no per-tenant disk
  quota either. Operator levers: a lower `SPINE_GIT_TREE_MAX_BYTES`, and `TMPDIR` on its own
  size-limited (quota'd) filesystem rather than a memory-backed tmpfs. Bounding the bytes in
  flight (one writer per branch, a process-wide byte allowance) is INIT-024 TASK-011.
- **Mutate deadline.** On expiry the command is SIGTERMed (git removes its locks) and the call
  fails with a permanent error wrapping `git.MutateTimeoutError`. With a deadline, `EnterBranch`
  adds the worktree `--no-checkout` and runs the `reset --hard` that fills it itself, because
  `worktree add` writes the tree in a child git the SIGTERM does not reach; the post-checkout hook
  is then not run. `push` is not deadlined by it (a governed merge's push stays bounded by the
  sequence timeout). A smudge filter the host configured keeps running after its git is stopped.
- **A clone's fetch is neither size-bounded nor deadlined** (security review P3-2). A clone's
  checkout is measured and deadlined, but the download before it is the remote's: a registered
  code-repository remote serving a huge pack fills the code-repository disk while it downloads (git
  has no fetch-side counterpart of `receive.maxInputSize`, and a fixed deadline would fail
  legitimate large clones). Operator controls: put `SPINE_CODE_REPO_BASE` on its own filesystem or
  quota, restrict which hosts code-repository remotes may name (egress policy), and limit who may
  register code repositories. A clone the budget refuses is remembered per remote and budget for
  10 minutes (a permanent 413-class error), so an over-budget remote is not fetched again on every
  access.
- **An over-budget home branch blocks the shared tree.** When the standalone tree's HEAD or home
  branch is itself over the budget — a repository already over the defaults at upgrade, a limit
  lowered below the existing tree, or a ref moved without a measured checkout (an operator's
  `update-ref`, a push to the checked-out branch under a non-default `receive.denyCurrentBranch`)
  — every restore refuses with nothing reset, so sequences on that workspace fail until the tree is
  reduced (a merge whose result fits is admitted) or the limits raised: fail closed rather than
  fill the disk. `spine serve` logs one Warn per workspace tree that is over the budget when it
  starts (or builds the workspace's services), naming the limit and the remedy.
- **One tree object over the git allocation limit is unmeasurable** (review round 1). A single
  directory whose tree object exceeds `GIT_ALLOC_LIMIT` (4x the per-blob read limit, 16 MiB by
  default — about 150,000 entries with long names) cannot be listed, so the tree is refused with
  `git.TreeObjectTooLargeMessage`. Raising `SPINE_GIT_TREE_*` does not help; `spine serve` has no
  setting for the allocation limit (an embedder raises it with `git.WithMaxBlobBytes` /
  `git.WithMaxGitAllocBytes`).
- **Refusals are permanent.** A refused governed merge is a permanent merge failure for the run
  (retrying cannot shrink the tree); a refused branch-scoped write fails with `git_error`. One
  budget covers every client of the process, code-repository clones and merges included, so a
  code repository larger than the defaults needs the limits raised for the whole deployment.
- **Residual — a tree on folding storage can stay marked** (INIT-024 close; follow-up). On case-
  or normalization-insensitive storage two tracked names that differ only in case or
  normalization share one file, so a rewrite from the index can never make both match their
  blobs. A tree marked for renormalisation whose marker has no readable record, or whose
  difference appeared after its record, then keeps the marker: every later call takes the tree's
  hold, quarantines the tracked state again (an unreachable `git stash create` commit each time,
  until git's garbage collection), rewrites and compares again; a code-repository clone in that
  state is unavailable on every access, and a workspace build logs an Error each time. The push
  path never reaches this (folding storage has receive-pack refused, "Git Push Gate: Served
  Repositories Need Case- and Normalization-Sensitive Storage"). Keep work trees on case- and
  normalization-sensitive storage; treating the paths that survive the rewrite as recorded, with
  one Warn, is the follow-up.

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

### Step Outputs Are Self-Attested Labels
- A step's `required_outputs` are logical labels (`architecture/workflow-definition-format.md`
  §5.3). The runtime binding §5.3 describes — the engine resolving each name to a concrete
  artifact — is **not implemented**: the engine only checks that the submitter *listed* every
  declared name in `artifacts_produced` (`validateRequiredOutputs`, `core/engine/result.go`).
  A step that declared outputs can be submitted with nothing pushed.
- Every shipped value is a label, not a path:
  - Spine `workflows/`: `artifact_content` (`adr-creation.yaml:29`, `artifact-creation.yaml:38`,
    `document-creation.yaml:32`), `decision_document` (`adr.yaml:29`), `deliverable`
    (`task-default.yaml:54`), `findings` (`task-spike.yaml:26`), `summary`
    (`task-spike.yaml:52`), `task_breakdown` (`epic-lifecycle.yaml:29`), `workflow_body`
    (`workflow-lifecycle.yaml:34`, and the `spine init-repo` seed,
    `service/internal/cli/initrepo.go:518`).
  - SMP `workflows/`: `artifact_content` (`adr-creation.yaml:29`, `artifact-creation.yaml:38`,
    `document-creation.yaml:32`), `task_breakdown` (`epic-lifecycle.yaml:28`), `deliverable`
    (`task-default.yaml:39`), `findings` (`task-spike.yaml:25`), `summary`
    (`task-spike.yaml:50`), `workflow_body` (`workflow-lifecycle.yaml:34`).
  - SMP's executors always attest the constant `deliverable` (`cmd/runner/operational_client.go:139`,
    `internal/engine/runner.go:379`, `internal/engine/adapters.go:232`).
- **What is recorded instead (INIT-023 TASK-008, audit P3-19; evidence, not enforcement — owner
  question 1: flag only):** `step_completed` carries `branch_change` (did the run branch's content
  change between the previous recorded head and this submit's head; `unchanged` needs no git call
  when the heads are equal, and a head moved by commits that change no path — an empty commit, a
  change and its revert — is `unchanged` too, `reason: head_moved_tree_unchanged`) and
  `required_outputs_unchanged` (declared outputs, content unchanged), plus a
  `WARN required outputs declared but run branch unchanged` once the submission applied; run
  detail (`GET /runs/{run_id}`)
  returns the same as `step_output_evidence`, from the reserved `spine.evidence.<execution_id>`
  `commit_meta` keys (it survives event-log retention). **Review remains the control.**
- **Evidence follows merge binding.** With binding off (an embedder without `EnableMergeBinding`,
  SMP until its TASK-009) and no `expected_head_sha`, nothing is captured: every entry is
  `unknown`/`head_sha_unrecorded` and run detail has no evidence. Only the primary repository is
  counted; code repositories are not (v1). A planning run's seed commit counts towards the first
  step's diff when no earlier head is recorded (`previous_source: baseline` shows it). A changed
  count is best-effort: a diff that fails or exceeds 3 s gives `unknown`/`diff_failed:<kind>`
  (an embedder's operator that resolves arguments by branch name first, as SMP's does, may lose
  the count — or a moved head's `unchanged` — when a branch is named like the SHA; the
  `unchanged` signal for equal heads needs no git call and is unaffected).
- Enforcing outputs needs path-typed outputs in the workflow format (both corpora) behind an
  opt-in flag; a follow-up.

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

### Shared Working Tree (standalone; INIT-023 TASK-012)
Standalone `spine serve` runs every governed git operation of a workspace in one non-bare working
tree (`SPINE_REPO_PATH`), serialized and self-healing (`architecture/git-integration.md` §6.6).
What that implies, and what it leaves open:
1. **One sequence at a time per workspace tree.** Governed merges, run starts, convergences, the
   status cascade and in-place artifact/workflow writes are serialized; a governed merge holds the
   tree across its push (bounded at 5 minutes), so an HTTP submit that triggers a merge may wait for
   another merge's push. A client disconnect no longer cancels a merge that has taken the tree: it
   completes (bounded), and only the wait for the tree is cancelled.
2. **A restore quarantines tracked changes in `SPINE_REPO_PATH`.** Whoever takes a tree that is not
   on a clean `main` aborts a merge in progress, stashes tracked changes into an unreferenced commit
   (`git stash create`; the SHA is logged at Warn and in the `working_tree_restored` audit record;
   `git stash apply <sha>` recovers it until `gc` prunes it, two weeks by default) and switches to
   `main`. That matters when an operator points standalone at a checkout they also edit (the default
   `SPINE_REPO_PATH` is `.`): uncommitted tracked edits there are moved out of the way at the next
   governed operation. Untracked (and ignored) files are never touched: one that blocks the switch to
   `main`, or that stands where the reset would rewrite a tracked path (a tracked directory replaced
   by a file, a tracked file un-tracked and rewritten, a tracked file replaced by a directory), fails
   the restore — before anything is reset — and so every governed operation until an operator moves
   it. A restore that fails after quarantining names the quarantine SHA in its error, its Error log
   and a `working_tree_restore_failed` audit record. The `reset --hard` fallback after a failed
   `git merge --abort` discards the conflicted state of that merge without quarantine — the only
   unquarantined discard. Quarantine objects are not pruned beyond git's own `gc`.
   **Phantoms** (review round 1): a tracked difference `git reset --hard` cannot remove — a
   `*.md text eol=lf` attribute over a file committed with CRLF, two paths that differ only in case
   on a case-insensitive filesystem — is content, not damage. The restore leaves it in place (Warn
   once, listing the paths), holds only the index to `HEAD`, switches home with `--discard-changes`
   when such a difference would make the plain switch refuse (after the same untracked/ignored
   obstruction check over every path the switch writes), and does not quarantine the same phantom
   again while its index blob and working-tree entry are unchanged. The entry is identified without
   reading through it (review round 2): its type, and a regular file's filtered blob or a symbolic
   link's own target — a link is never followed and nothing is opened at the leaf (a FIFO, a device)
   — so a case collision of a file and a link costs one quarantine, not one per take. More than 64
   residual paths are not remembered: while they show, every take quarantines them again (a stash
   commit, a Warn and a `working_tree_restored` audit record per take) but the tree still never
   wedges. git itself still refuses a checkout or merge that would
   rewrite a phantom path ("your local changes would be overwritten"), so a governed merge or a
   cascade checkout that touches it fails like any other permanent merge failure until the content
   is fixed (renormalize the file, or remove one of the colliding paths). A refused bound merge that
   carries a phantom is still taken back on the shared tree (review round 2): when `read-tree -m`
   refuses, the take-back quarantines the tracked state and resets the index and tree to the base,
   after the untracked-file check. On a tree with no home branch (a code-repository clone, an
   embedder's worktree) a local change is never discarded, so such a take-back stays refused and
   pending, and that tree refuses every later sequence until the content or the branch is fixed by
   hand (fail closed; dormant in standalone, and SMP never calls `ResetBranch`).
3. **One process per repository path.** The lock is in-process. A second `spine serve`, or an
   operator's git, on the same tree is not serialized, and a restore can quarantine its in-flight
   work.
4. **A stale `.git/index.lock` is reported, not removed** (owner question 5). Spine itself no longer
   leaves one behind (git children get `SIGTERM`, whose handlers remove lock files, and a taken
   sequence is never cancelled half-way); a process or host crash still can. Every governed merge in
   the workspace then fails transiently ("repository locked", naming the file) — bounded by the retry
   budget below — until an operator deletes the file.
5. **git's own detached auto-gc can still contend for ref locks.** That failure is transient and
   bounded by the retry budget. When it hits the take-back of a refused bound merge (the merge
   `MergeBound` already put on the local `main`), the client keeps the take-back pending: the
   merge block's own exit restore retries it, and until it succeeds every later take of the tree
   (governed merge, run start, in-place write) retries it first and refuses the tree while `main`
   still contains the refused merge — permanently, naming the merge, when something outside the
   server moved `main` on top of it. The record is armed before the take-back's first fallible step,
   and the take-back's git runs on its own 30 s bound detached from the caller (review round 2), so
   a sequence whose deadline is spent, or a git that cannot start, no longer releases the tree with
   the refused merge on `main`. The pending record is in-process: a restart between a failed
   take-back and the next take of the tree forgets it (the failed outcome and its Error log name
   the merge; reset `main` by hand). Reads (the projection, `HEAD` reads, git HTTP fetches) are
   not held back while it is pending. With the governance guard on, the same failed take-back is
   also a governance block, whichever check refused the merge ("Governance-Path Guard" 6).
6. **`HEAD` means `main` for reads, but reads are not serialized.** On the home-branch tree every read
   of `HEAD` resolves to `refs/heads/main`; a read that names a run branch, or a full rev, reads that
   ref while a sequence may be moving it. TASK-011 pins the projection explicitly. A read of the
   working tree's *files* would see whatever a sequence has checked out, so the one write path that
   reads files before writing — task acceptance/rejection and the follow-up link — reads under the
   tree's lock and commits only over the bytes it read (a change in between is a 409 conflict).
7. **Merge retries are counted per (run, repository), not per committing episode** (owner question
   4). After a run's first exhaustion its attempts are already above the bound, so every later
   committing episode of that run is abandoned at its first transient failure (an outcome counts
   only when it was attempted in the current episode, i.e. after the run's latest step activity):
   one network blip in a reworked run costs a full rework cycle. A per-episode counter needs a
   migration. In one process an abandonment never overlaps a merge attempt of the same run (it skips
   the tick while one is in flight; an attempt that starts during it stands down); across processes
   sharing one database the routing itself is conditional since TASK-008 (a route that loses to a
   concurrent completion writes nothing), but the re-record of the exhausted outcome is not. With
   the governance guard on, a run held for an operator decision and a run whose refused merge is
   still blocked on the local `main` are never abandoned (INIT-023 wave 4 integration). A *code
   repository* whose retries are exhausted is not routed like the primary (codex review round 8):
   its outcome is marked `retries exhausted (…)`, which the per-repository loop treats as a terminal
   failure, the primary then merges and the run waits in `partially-merged` for an operator
   (`RetryRepositoryMerge` / `ResolveRepositoryMergeExternally`); after a retry its next transient
   failure is exhausted at once (the same lifetime count). A failed attempt whose prior primary
   outcome cannot be read records nothing and the run stays in `committing` (a store outage, not
   counted), so a landed merge is never dropped from the outcome.
8. **Uncounted transient exits stay unbounded:** a lock wait cut short by the caller (by design),
   outcome-persistence errors (a store outage), the governance guard's "unavailable" refusals (its
   changed paths could not be computed, a refused merge still blocked on `main`, a waiter on the
   run's governance stripe that gave up — TASK-006 asked for them not to count), and any later
   transient exit that records no outcome. TASK-005's transient `VerifyCommit` exit and TASK-017's
   retryable failures judging the bound commit (INIT-023 wave 4 integration) record a failed,
   transient primary outcome and are counted (after re-reading the run, so a second entry never
   overwrites a completed run's merged outcome).
   **A push that fails after the merge landed is never abandoned** (review round 1): the merge is on
   the local target, where the next writer's push publishes it, so routing the run as failed (or
   back to `execute`) would contradict the branch, and taking the merge back is unsafe (another
   writer's push may already have published it, and runs started since may have been cut from it).
   Such a run stays in `committing` and its push is retried every tick until it lands, with an Error
   log once the budget is passed ("publication pending"); each retry is an "already up to date"
   merge plus the push, holding the tree for the push. A later attempt that fails before its own
   push (the restore, the merge or a check, transiently) keeps naming the earlier local merge in its
   failure detail, so it stays pending too (codex review round 6), and so does one after an attempt
   recorded `merged` whose run did not complete (a store failure after the record; codex review
   round 7). Only attempts that left nothing on the target are bounded and abandoned. The exemption
   holds within one committing episode (review round 2): an earlier merge is carried only from an
   outcome attempted after the run's latest step activity and never from a permanent-class one, so
   a run sent back to `execute` by a permanent push rejection is bounded again in its next episode
   (a failed read of the run's steps records nothing, like an unreadable prior). A code
   repository's re-merge under binding (its bound commit no longer on the default branch) carries
   nothing either. A *permanent* failure after a local merge (a rejected or
   unauthenticated push, a refusal) still routes the run while its merge stays on the local `main`,
   as before TASK-012; re-syncing local `main` is a follow-up.
9. **The multi-workspace scheduler is wired; a single leader is assumed** (INIT-024 TASK-008, the
   open half of P3-27). In the `db` and `platform-binding` resolver modes `spine serve` runs
   `MultiScheduler` over the workspace pool, so step and run timeouts, the orphan scan and the
   bounded merge retries fire in every active workspace, each with its own store, git clients and
   credentials. A tenant cannot hold up the others: one scan per workspace at a time, a tick that
   makes no progress for its wait (`SPINE_SCHEDULER_WORKSPACE_BUDGET` + 30 s) stops waiting and later
   ticks skip the stalled workspace until its scan ends, every scan is cancelled at a hard 10-minute
   deadline, and a panic in one workspace's scan is recovered. What remains:
   - **More than one replica.** There is no leader election (TASK-008 Decision 3, like the projection
     sync and every INIT-023 lock): every replica scans every workspace. The scheduler's own writes
     are compare-and-swap (INIT-023 TASK-008), so a timeout, a run-timeout termination or a
     recovery applies once, but each replica retries a committing run's merge (the tree lock and the
     merge-attempt gate are per process; item 7 says what the conditional writes cover across
     processes). Run one replica per registry, or accept the duplicate merge attempts.
   - **No startup crash recovery in the shared modes.** Single mode runs `RecoverOnStartup` before it
     serves (a `pending` run activated, an `assigned` step reset, a completed step routed on, a
     committing run retried); the shared modes run it for no workspace. Committing runs are still
     retried by the loop and orphaned active runs failed by the orphan scan (after three thresholds),
     but a run a crash left `pending`, or on a completed step that was never routed, waits for an
     operator. Per-workspace startup recovery is a follow-up (TASK-008 owner question 3).
   - **`platform-binding` schedules only resolved workspaces.** The platform has no enumeration
     endpoint, so the scheduler lists the bindings the process has resolved since it started
     (`PlatformBindingProvider.ListIDs`, the cache's ids): after a restart a workspace is scheduled
     only once a request has resolved it (as with the projection sync).
   - **How long a workspace that should stop is still scheduled**, per mode. A scan already running
     when it happens finishes (within the scan deadline below).
     - `db`: `POST /workspaces/{id}/deactivate` on this replica drops the resolver caches and evicts
       the workspace, so the next tick skips it. A row deactivated any other way (SQL, another
       replica's API) is still listed and resolved for up to the db provider's 60 s cache TTL.
     - `platform-binding`: a binding is re-fetched once its 5-minute cache TTL has passed, and a
       platform 404 drops it then. While the platform errors (5xx, network) a cached binding is
       served stale for up to 30 minutes more (`StaleGrace`), so up to 35 minutes in all. The
       invalidation webhook drops it at once, but only on the replica that receives it. A workspace
       the platform answers 403 for (or whose secrets cannot be read) stays listed: every tick then
       logs an Error for it, and each scan's resolve fetches it from the platform again.
   - **A stalled scan holds what it holds until its deadline.** A scan no tick waits for any more
     keeps its workspace's tree lock, one git process, one goroutine and its pool reference, and is not
     counted against later ticks' `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES`. Every scan runs under a
     hard 10-minute deadline (`MultiSchedulerConfig.ScanDeadline`; no variable): past it the scan's
     context is cancelled and the scan returns, giving back its pool reference, with an Error. The git
     commands it runs on that context are stopped with SIGTERM (git's handlers remove its lock files;
     a remote helper exits with it). A code repository's first clone is not: the git pool runs it
     detached, shared by every caller waiting for it, so it goes on after the scan has stopped waiting
     for it. Until the deadline a request for an evicted workspace (a deactivation, a binding
     invalidation, a credential rotation) waits for that reference: up to the deadline, or less if the
     request's own deadline comes first. A sequence on a shared working tree (the primary
     repository's merge and push) runs detached from the cancellation and ends within its own 5-minute
     bound.
   - **Building a cold workspace's services is bounded by the pool, not by the scan** (the build runs
     on the pool's lifetime context, which the services it starts need). The pool waits for a build
     at most its init timeout (`SPINE_WS_POOL_INIT_TIMEOUT`, default 2 minutes); every caller waiting
     on the build — the scan, requests, the projection sync, a request already waiting on an evicted
     entry — then gets an error (`ErrWorkspaceUnavailable`), and so does every caller until the
     abandoned build returns: no second build starts beside it (a late build registers its database
     pool over the replacement's). The build's one-shot calls — the database ping and the first
     connections, the subscription-secret sweep, its git commands, the first bootstrap-admin write
     and the internal subscription upsert — run under a deadline of the same length, so a database
     that accepts the connection and never answers fails the build at the timeout instead of holding
     it. When the abandoned build returns, a successful result is installed and served (a slow but
     healthy workspace is not rebuilt for ever), unless the workspace was evicted meanwhile or the
     pool closed; otherwise what it built is closed and the next caller builds afresh. A panic in a
     build is recovered: what the build had started is closed, and callers get the error. A
     request meets every one of these failures before its bearer can be validated (that needs the
     built services), so it answers the uniform pre-authentication `401` (INIT-024 TASK-007), even
     with a token that is valid there; the cause is logged.
     **An operator with a workspace that keeps failing this way:** check that its database answers
     (connection and authentication); if its healthy build is simply slower than the timeout (a large
     subscription sweep, a slow database), raise `SPINE_WS_POOL_INIT_TIMEOUT`; to bound connection
     setup independently of Spine, add `connect_timeout=<seconds>` to the workspace's database URL. A
     build stuck in a call nothing bounds (a builder step that does not use the init context) keeps
     the workspace unavailable — every request fails at once, nothing hangs — until it returns or the
     process restarts.
   - **An attempt cut by the scan deadline records nothing** — its outcome write uses the expired
     context — so, like a lock wait cut short (item 8), it does not count against the merge retry
     budget: a code repository whose remote never answers keeps its run in `committing`, retried on
     later ticks, each attempt held up to the deadline (only that workspace waits). On shutdown a stalled scan is waited for only until the 10 s
     shutdown deadline. As many tenants stalling in one tick as there are slots delay the others by one
     wait: the workspaces that tick left undispatched come first in the next tick of the same kind.
   - **Each tick lists every candidate row.** The scans read, per workspace and per tick, every
     unfinished step execution (`ListActiveStepExecutions`, `adapters/store/postgres_steps.go`), every
     run past its timeout, every stale active run and every committing run (`postgres_runs.go`), with
     no `LIMIT`; the soft budget bounds the work done on them, not the read. A workspace with a very
     large backlog is loaded into the shared process every 30 s (timeouts) and 60 s (orphans, merge
     retries). Paging is a follow-up.
   - **`platform-binding` reads secrets on every scan.** Listing for a tick reads none (`ListIDs`), but
     each scan resolves its workspace through the pool, and a platform-binding resolve dereferences
     the binding's three secret references every time (the provider caches no secret value; the
     secret client's own cache decides what reaches the backend): about three reads per workspace per
     scan, three scans a minute at the default intervals. The projection sync's `List` adds three
     more per cached binding per poll.
   - **A slow scan still lengthens its tick.** A tick waits as long as it makes progress, and the
     three kinds of scan (timeouts, orphans, merge retries) share one loop, as in single mode. A
     tenant whose scan is slow but never stalls (each merge retry just under the wait, every tick —
     a push that fails after its merge landed is retried every tick, item 7) lengthens each of its
     ticks by up to one workspace budget plus one wait, so the other tenants' next scan of another
     kind starts that much later: their timeouts are late, never lost. A scan that ends just after
     each wait costs one wait on every tick that dispatches it (every other tick of its loop, since
     the tick in between skips it).
   - **A recovered panic** releases what the scan released by `defer` (the pool reference, the tree
     lock, transactions); anything held otherwise stays held, the same assumption as net/http's
     per-request recovery behind every HTTP handler.
10. **`classifyGitError`'s bare `"lock"` substring** also matches paths in git's stderr (an untracked
    `blocked-notes.md` in the way of a checkout reads as "repository locked", transient). The retry
    budget caps the loop; matching `index.lock': File exists` precisely is a follow-up. Likewise
    curl's "Couldn't connect to server" is classified permanent.
11. **Ignored files and the engine's own sequences.** The restore never overwrites an ignored file
    (its switch home uses `--no-overwrite-ignore`, and an ignored file in the reset's way refuses
    it), but the engine's own checkouts and merges inside a sequence (the cascade's checkout of a run
    branch, a merge into `main`) keep git's default and overwrite an ignored file at a path the
    target branch tracks, as before TASK-012. Keep ignored local state out of `SPINE_REPO_PATH`.
12. **Embedders.** A tree no client registered a home branch for (SMP's per-request worktrees, code
    repository clones) is never restored or detached and keeps the real `HEAD`. Every git child is
    stopped with `SIGTERM` on cancel for every client, and the engine's merge blocks re-check the run
    status for every operator — neither changes a successful result.
