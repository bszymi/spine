# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

---

## INIT-023 — Security Audit Remediation (EPIC-004 TASK-014: workflow and write-context governance)

### Security

- **A reviewer can no longer commit a workflow straight to `main` (audit F7, P2).** On the
  standalone gateway a reviewer's `PUT /workflows/{id}` without `write_context` committed to the
  authoritative branch (`write_mode: authoritative`), skipping both the ADR-008 lifecycle and
  branch protection — enough to drop `task-default`'s review gate or neuter `workflow-lifecycle`
  itself. It now opens an **update** `workflow-lifecycle` planning run, exactly as `POST
  /workflows` already did for creates (new `Orchestrator.StartWorkflowUpdatePlanningRun`).
- **`write_context: {}` on workflow endpoints is refused (V1).** It skipped the reviewer dispatch
  on create *and* update and committed to `main` with no `Workflow-Bypass` trailer, for every
  role. A workflow `write_context` now needs a `run_id` naming that workflow's own lifecycle run
  (V2: before, any planning run — another workflow's, an artifact-creation run — or a task run
  whose `task_path` the caller echoed was accepted, and the workflow then reached `main` under that
  run's review instead of `workflow_review`).
- **`workflow.Service` consults branch protection.** With a policy installed (`spine serve` installs
  the projection-backed one), a non-bypass workflow write is an `OpDirectWrite` on its real target,
  so the bootstrap `main: no-direct-write` rule refuses a direct workflow commit (`403`), and an
  ADR-008 bypass needs an operator-or-above actor.
- **Run branches have one writer on the API path (audit P3-6, P3).** `write_context` with a
  `run_id` (artifact and workflow writes) and `POST /artifacts/add` let any contributor commit into
  another actor's run branch, even while it sat in review; the governed merge then published the
  injection. Such a write is now accepted only from the actor bound to the run's current step —
  TASK-016's `core/refadmit` predicate, so pushes and API writes follow one rule — or an operator.
  While the current step is a `review` or `internal` step (task-default's `publish`, between
  approval and the merge) the branch is frozen for **every** role, operators included. P3-6 is
  closed for writes by actors who do not hold the step; who may *claim* a step is governed by
  TASK-007's step-entry policy, which gains the rule below.
- **A reworked step is re-offered only to its prior holder (P3-6 residual, owner question 7).**
  New opt-in `StepEntryPolicy.ReofferToPriorHolder`, on in `StrictStepEntryPolicy` (the
  `spine serve` default): a waiting author step that was held before in its run — after a
  `needs_rework` loop, or a restart-recovered row that still names its actor — can be claimed only
  by that prior holder; an operator can still assign it to anyone. Without it a contributor could
  claim a colleague's reworked draft and then write the branch as its bound actor. A step its
  holder released stays claimable by others; a retry after a failed attempt is not re-offered;
  nor is an attempt whose prior holder the claim itself would refuse — the holder no longer
  resolves, is not `active`, is off the execution's `eligible_actor_ids`, is of a type the step
  does not admit, or lacks one of its `required_skills` (only an `active` skill counts, as for any
  claim) — since that holder could never take it back. Each of those follows an operator or admin
  action (suspension, `actor.remove_skill`, `skill.deprecate`, a `step.assign` allow-list), never
  a contributor's. With `EnforceExecutionMode` on (strict), `automated_only` and `ai_only` steps
  are not re-offered at all: only operator-provisioned machine actors can bind them, and
  auto-selection already hands their rework to any eligible pool member, so pinning the claim to
  one runner or agent identity would only strand the attempt when it has gone.
- **Workflow definitions have one producer (design-check V3, V4).** The artifact endpoints'
  workflow-path guard was a raw prefix test, so `./workflows/x`, `governance/../workflows/x` and
  `workflows//x` reached `workflows/` on a run branch (and `PUT /artifacts/./workflows/<id>.yaml`
  replaced a workflow with artifact Markdown). The test is now canonical (dot segments, repeated
  slashes, case folding). `POST /runs` with `mode: planning` and a `workflows/` task path, which
  minted a non-lifecycle run that could lock that workflow against governed edits, is refused, and
  so is the same path in the engine's `StartPlanningRun`.
- **Not closed here: workflow edits by push.** "Workflow edits ride only their own run" (V2) is
  enforced on the API path. A git push is admitted by the binding alone (INIT-023 TASK-016) and the
  pre-receive gate checks no paths, so the actor bound to a task run's (or a lifecycle run's) current
  step can still push changes under `workflows/` — or any other governed path — onto that run
  branch, and the governed merge then publishes them under that run's review rather than
  `workflow_review`. Closing that is the governance path guard (INIT-023 TASK-006); see
  `KNOWN-LIMITATIONS.md`.

### Changed

- **`PUT /workflows/{id}` by a reviewer without `write_context`:** `200` + commit → **`201`**
  with a planning body (`run_id`, `branch_name`, `workflow_path`, `write_mode: planning`); `503`
  when no planning starter is wired; `404` (missing workflow), `422` (no version bump) or `400`
  (bad id) before any branch or run exists. The `spine workflow update` CLI accepts any status
  below 400 and needs no change.
- **One open lifecycle run per workflow:** a reviewer `POST`/`PUT /workflows` while a
  non-terminal planning run governs `workflows/<id>.yaml` is `409 conflict` naming it
  (`detail.run_id`, `branch_name`, `status`, `current_step_id`); a store failure is `503`.
- **`POST /workflows` for an id that already exists** still answers `409 already_exists`, but now
  before any branch or run exists. A workflow planning run whose branch write fails is marked
  `failed` (was: left `pending`, which scheduler recovery promoted to `active`).
- **Workflow `write_context`:** `{}` (or `task_path` only) → `400` for every role; a `run_id` whose
  run does not govern `workflows/<id>.yaml` → `400` (detail `run_task_path`, `workflow_path`).
  Operators who sent `write_context: {}` got an untrailered direct commit; they now omit
  `write_context` for the audited bypass.
- **Run-branch writes** (`write_context` with `run_id`, `POST /artifacts/add`): `409` while the
  current step is waiting or unassigned (claim it first, `POST /execution/claim`;
  `detail.execution_id`) or has no open execution; `403` for any other non-operator (the holder is
  never named); `409` (`detail.step_type`) during a `review` or `internal` step for every role;
  `409` operator-only when the run has no run-level cursor (legacy or diverged) or its branch is
  not an engine run branch; `409` when the step definition cannot be resolved; `503` when no step
  assigner is configured. API writes land on the run's branch in the primary repository, so while
  the current step is routed to a code repository (ADR-015 `repository: <id>`) only operators may
  write it: the step's holder gets `409` with `detail.repository_id` (the same rule as a push of
  that branch to the primary repository; before, any contributor could write it). After `POST /artifacts/entry` or a workflow planning run starts, the
  author claims the draft step before stacking edits (the same claim was already required to submit
  it); after `needs_rework`, the reworked step is claimed again.
- **A run that governs a workflow definition accepts no artifact writes** (`400`).
- **Artifact writes to a dot-segment or case-variant `workflows/` path** → `400`
  (`use_operation: workflow.*`).
- **`POST /runs` planning with a `workflows/` task path** → `400` (`use_operation: workflow.create`).
- **Under the strict step-entry policy**, an actor other than the prior holder claiming a reworked
  or restart-recovered author step gets `403`, and such steps are hidden from that actor's
  candidate listing; the reviewer who sent work back can no longer self-claim the rework.
- **Wiring:** single-workspace `spine serve` now wires the workflow planning starter (pooled
  workspaces already had one) and installs the branch-protection policy on every
  `workflow.Service`. No event type, schema, migration or environment variable is added.

### Upgrade note: workflow runs left behind by failed starts

Before this change a failed workflow-lifecycle start — a reviewer `POST /workflows` for an id that
already exists, or a git error on the branch write — left its planning run `pending` with the run
branch deleted, and scheduler recovery promotes such runs to `active` with a waiting `draft` step.
They never finish. After the upgrade the one-open-run rule counts them, so every reviewer `POST` /
`PUT` of that workflow answers `409` naming the run (`write_context` on it fails, its branch being
gone). Operators should look for them once and cancel them:

```sql
SELECT run_id, task_path, status, branch_name, current_step_id
  FROM runtime.runs
 WHERE mode = 'planning' AND task_path LIKE 'workflows/%'
   AND status NOT IN ('completed', 'failed', 'cancelled');
```

For each row whose `branch_name` no longer exists in the served repository (`git -C <repo>
rev-parse --verify --quiet refs/heads/<branch_name>` prints nothing), run `spine run cancel
<run_id>` (or `POST /api/v1/runs/{run_id}/cancel`) as an operator. Without database access, `GET
/api/v1/query/runs?task_path=workflows/<id>.yaml` lists the runs of one workflow. The `409` message
now names the operator cancel as the recovery for a stale run.

### For embedders

- `workflow.Service` gains `WithPolicy` and `WithDefaultRef`; `workflow.ValidateID`,
  `workflow.FilePath` and `workflow.InWorkflowsDir` are exported. **The zero value is unchanged:**
  with no policy no rule is evaluated and no role is checked on bypass writes (SMP's admin writes
  are actor-less bypass writes and keep working byte for byte). Do not call `WithPolicy` unless
  every bypass write context also binds a `domain.Actor` of role ≥ operator.
- `engine.WorkflowWriter` gains `Update` (`*workflow.Service` already has it).
  `Orchestrator.StartWorkflowUpdatePlanningRun` is new; `StartWorkflowPlanningRun` now refuses an
  existing id before any run exists and marks a run whose branch write failed `failed`.
- `Orchestrator.StartPlanningRun` refuses a path under `workflows/` (any spelling) with
  `ErrInvalidParams`. SMP's only caller (`CreateEntry`) builds `.md` paths under the artifact type
  directories, so its behaviour is unchanged.
- `StepEntryPolicy.ReofferToPriorHolder` (zero value off; on in `StrictStepEntryPolicy`). SMP does
  not use the step-entry policy today; an embedder that adopts `StrictStepEntryPolicy` gets it.

---

## INIT-023 — Security Audit Remediation (EPIC-001 TASK-016: ref-class push admission)

### Security

- **Run branches can no longer be pushed by anyone who can authenticate (audit F2, push half,
  P2).** With `SPINE_GIT_RECEIVE_PACK_ENABLED=true` the pre-receive gate skipped
  `refs/heads/spine/*` entirely, so any authenticated actor — a `reader` included — could create,
  advance, force-push or delete any run, planning or divergence branch, and the governed merge then
  published whatever the branch pointed at. Every ref update is now admitted by ref class
  (new package `core/refadmit`) before branch protection, whether or not a policy is wired. A run,
  planning or divergence branch accepts a create or update only from the actor holding the
  **latest** execution of its current step (`assigned` or `in_progress`) on an `active` run, with
  role `contributor`+, for the same repository — or from an operator/admin. Only operators may
  delete one. The parent run branch is operator-only while the run is diverged.
- **Review freeze on the push path (owner question 4).** Even the bound actor is refused while the
  current step's declared type is `review` or `internal`; the definition comes from the run's
  pinned workflow, and an unresolvable one refuses. Operators are exempt.
- **Tags and other non-branch refs are operator-only (audit F10, gateway half, P2).** A pushed
  `refs/tags/<run-branch>` or `refs/tags/main` shadowed the branch in the engine's bare-name git
  calls (the latter broke every governed merge). Tags, notes, `refs/<x>` and `refs/remotes/*` now
  need the operator role, as do `spine/init` and the rest of the reserved `spine` namespace —
  including `refs/heads/spine` itself (whose creation blocked every new run branch) and any case-fold
  spelling of `spine` (`SPINE`, `Spine`, `ſpine`), which a case-insensitive server filesystem
  aliases onto the real namespace.
- **Shadowing ref names are refused for everyone (INIT-023 TASK-002 D12).** Creating or updating a
  ref with a `refs` component after the first or any `HEAD` component (`refs/heads/refs/heads/main`,
  `refs/tags/refs/heads/main`, `refs/refs/heads/main`, `refs/heads/HEAD`) is refused for every
  principal, operators included: such a name makes every client push of the branch it shadows fail
  (`dst refspec … matches more than one`). Deleting one stays allowed. Refs with a control byte are
  refused for everyone too (defence in depth behind the TASK-001 parser).
- **Divergence branch creation is tied to the run (owner question 6).**
  `POST /runs/{run_id}/divergences/{divergence_id}/branches` now refuses, with `400`, a `branch_id`
  outside the workflow ID grammar (`^[a-z0-9][a-z0-9_\-.]*$`, at most 128 bytes, no `..`, no
  trailing `.`/`.lock`, not `refs`, which would make a ref name the push gate refuses for
  everyone) — a control byte was a `500` before — and, with `403`, a caller who is
  neither an operator nor bound to the run: the actor who submitted the step that triggered the
  divergence, or one holding a live cursor of the run. Before, any contributor could attach a
  branch to another actor's run and then become the actor allowed to push its divergence ref.

### Changed

- **Pushes refused that were accepted before:** any push by a non-operator into `refs/heads/spine/**`
  other than the bound run/divergence branch; any delete of such a branch by a non-operator; any
  non-branch ref (tag, note, …) by a non-operator; shadowing names by anyone. In `SPINE_DEV_MODE`
  (no git authentication) every push into `spine/**` and every tag push is refused, since the gate
  sees no actor. Refusals arrive as `remote: ref-admission: …` lines with per-ref `ng` status
  (catalogue: `docs/git-push-guide.md` §5; how to push a run branch: §6). Pre-receive stays
  all-or-nothing.
- **Exploratory divergence branches** can be created only by an operator or by the actor bound to
  the run (in practice, the one who submitted the step that triggered the divergence), and their
  divergence refs are pushable by operators only, since the API creates no step execution for them
  (`KNOWN-LIMITATIONS.md`).
- **`spine init-repo`** run with a non-operator token against a Spine gateway now prints the
  server's refusal of `spine/init` (`Note: origin refused the push; spine/init exists only
  locally:` with the `ref-admission:` line and the operator-token remedy) instead of "remote may
  not be configured". It still exits 0, as for any push failure. Re-running `spine init-repo` in
  that clone does not retry the push (with nothing left to commit it stops before pushing), so the
  remedy is `git push -u origin spine/init` with an operator token (`docs/git-push-guide.md` §5).
- **Logs:** each refused ref is logged at Warn (`pre-receive ref admission denied`, with class and
  reason); an operator's push to a reserved or non-branch ref is logged at Info. No event type,
  schema, migration, endpoint or environment variable is added.
- **Startup warnings** in single mode when receive-pack is on but no run store or no
  step-definition lookup is wired (`run-branch pushes are operator-only: …`). In shared mode each
  workspace's pair is resolved per push and an Info line says so.
- **Admission is a push-start check.** git writes the ref after the pack is uploaded and
  processed; the upload is bounded by `ReadTimeout` but the processing is not, so a push started
  while bound can land after the step is submitted, during review (`KNOWN-LIMITATIONS.md`).

### Upgrade note

Shadow refs pushed before this change persist, and admission only stops new ones. On each
workspace repository, list them and delete every non-branch ref whose short name equals a branch
name, and every doubly-qualified or `HEAD`-named ref (`docs/integration-guide.md`, "Upgrading past
ref-class admission"):

```bash
git -C <repo> for-each-ref --format='%(refname)' | grep -v '^refs/heads/'
git -C <repo> for-each-ref --format='%(refname)' refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs
git -C <repo> for-each-ref --format='%(refname)' | grep -E '(^|/)HEAD(/|$)'
```

CI that pushes release tags must use an operator token.

### For embedders

- **New package `core/refadmit`** (imports `core/domain` and stdlib only): `Classify`,
  `NewEvaluator(RunReader).WithStepDefs(StepDefLookup).Decide`, `DecideBoundToRun` (which also
  binds the actor who submitted a diverged run's triggering step, `ReasonDivergenceTrigger`),
  `FrozenStepType`, `EvaluationErrorMessage`. `RunReader` is satisfied as is by
  `*store.PostgresStore`, `store.Store` and SMP's `spinecore.RunStore` (whose run-not-found error
  needs mapping to `domain.ErrNotFound` to read as "unresolved" rather than an evaluation error);
  `StepDefLookup` is satisfied by `*engine.Orchestrator`. A nil reader or lookup makes run,
  planning and divergence refs operator-only. Purely additive: nothing in `core/` or `adapters/`
  calls it, so embedded behaviour is unchanged until an embedder adopts it (SMP INIT-027 EPIC-004
  TASK-007).
- **New `divergence.ValidateExploratoryBranchID`.** `CreateExploratoryBranch` does not call it, so
  its behaviour is unchanged for embedders.
- `adapters/store` asserts that `*PostgresStore` and `Store` satisfy `refadmit.RunReader`
  (compile-time only).

---

## INIT-023 — Security Audit Remediation (EPIC-001 TASK-004: worktree write containment)

### Security

- **Worktree file I/O can no longer be redirected by a committed symlink (audit
  F5, P2).** `artifact.Service` resolved paths with `filepath.EvalSymlinks` up to
  the nearest existing ancestor and re-appended the unresolved leaf, so a
  *dangling* symlink as the last component passed the containment check and
  `Create` wrote the file at the link target, outside the repository or
  worktree; the rollback then removed the link, not the file. An in-root link
  such as `governance/a.md -> ../.git/info/attributes` passed too and planted
  attacker lines in `.git/info/attributes` of the standalone main tree, which
  git applies to every later merge. The workflow writer, the governed-merge
  status cascade (`CLIClient.WriteAndStageFile`), the acceptance/successor
  reads and the dormant renumber helpers had no containment check at all.
  Every one of them now goes through the new `core/worktreefs` package: each
  path component is `Lstat`-checked from a directory handle opened without
  following symlinks, inside an `os.Root`; any symbolic link component
  (dangling or not, leaf or parent) and any component git treats as `.git`
  (case, NTFS and HFS spellings, `git~1`) is refused before any filesystem
  change; new files are created `O_CREAT|O_EXCL` and updates replace by
  rename. A symlink pushed to a run branch between the cascade's read and its
  checkout is refused instead of overwriting its target.
- **Worktree reads are bounded.** A workflow update through a committed
  `workflows/<id>.yaml -> /dev/zero` read without end (the F4 cross-tenant OOM
  through a filesystem path). Links are now refused before any read, and every
  worktree read is capped at 4 MiB (the same value as TASK-003's git blob cap).
- **Failed creates clean up.** A create whose commit fails now removes the
  directories it made as well as the file (they leaked before). That includes
  a file a failing pre-commit hook rewrote in place or replaced through a temp
  file and a rename (`sed -i`, formatters), as long as the path is still
  untracked in HEAD, so a retry fails the same way instead of with
  `already_exists`. A file that is tracked in HEAD by then (a concurrent
  writer committed it) is kept; before, the rollback removed it by name. A
  failed update restores the original bytes and mode, re-creating the file
  (and its directory) if a failing hook deleted it; a symlink a hook put in
  its place is refused, not written through as before.

### Changed

- **HTTP status of refused paths** (standalone gateway; SMP's embedded
  endpoints map the same codes):
  - a write through a symlink, a path with a `.git` component (including the
    spellings git's NTFS rule treats as `.git` on every platform, such as
    `GIT~1/…`, `.git./…` and the backslash forms `.git\x.md`,
    `governance/a\.git`), or a path over 4096 bytes / a component over 255
    bytes: **500 `git_error` / `internal_error` → 400 `invalid_params`**,
    refused before any side effect (these previously failed at `git add`, or
    with an OS error, after the file was written);
  - a non-canonical path (`a//b.md`, `a/./b.md`, a trailing `/`), which was
    folded and written before: **now 400 `invalid_params`**. SMP is unaffected
    because its embedded artifact entry points pass only canonical paths
    (`normalizeTaskPath` for task actions, a `path.Clean` equality check for
    content writes, `BuildArtifactPath` for creates). SMP's
    `validateRepoRelativePath` is not that guard: it refuses only empty,
    absolute, NUL and `..` paths, and only in front of SMP's own
    `RealGitOperator`, so a new SMP entry point that passes a client path to
    `artifact.Service` must canonicalise or refuse it itself;
  - `Create` at a path that is an in-repo symlink to an existing file, or an
    existing directory: **409 `already_exists` → 400 `invalid_params`**;
  - artifact `Update` / accept / reject of an existing worktree file over
    4 MiB: **previously succeeded (2xx) → now 413 `payload_too_large`**.
    This is a working flow that is now refused, not a remapped error: the
    old code read and rewrote the file with no cap. The cap applies to the
    existing file, read for the rewrite and for the rollback copy, not to
    the new content (which the standalone gateway already caps at 512 KiB);
    such a file can only reach a branch by a push or an import. The
    best-effort `follow_up_from` link a successor task adds to such a file
    is skipped and logged, like any failure of that step.
  - workflow `Update` of an existing workflow file over 4 MiB: **500
    `internal_error` → 413 `payload_too_large`** (yamlsafe's 64 KiB parse
    cap already refused it).
  - Messages read `unsafe path: component "<repo-relative prefix>" is a
    symbolic link` (or `names the git directory`, `is not a directory`, …);
    they never echo the raw input or an absolute path.
  - In-repo symlinked directories on a write path (for example a `workflows/`
    that links to `ci/workflows/`) are refused rather than resolved (owner
    question 1 default). Such writes already failed (500) at `git add`.
- `CLIClient.WriteAndStageFile` stages with `--literal-pathspecs`.

### For embedders

- **New:** `core/worktreefs` (stdlib-only): `Clean`, `Open`/`Tree`
  (`Lstat`, `ReadFile`, `ReadFileLimit`, `Create` + `Undo` (`Rollback`,
  `RollbackClaiming`, `Release`), `Replace`, `WriteFile`, `Remove`,
  `RemoveEmptyDir`, `WalkRegularFiles`), `PathError` (unwraps to
  `ErrUnsafePath`), `SizeError` (unwraps to `ErrTooLarge`),
  `DefaultMaxReadBytes`. SMP may use it for its own sinks (SMP TASK-004/008).
  An `Undo` holds at most three descriptors until it is resolved, whatever
  the path's depth; `Rollback` then opens one more directory handle per
  directory the create made, all at once and only while it runs, and
  `WalkRegularFiles` holds one per level it is inside (both bounded by the
  4096-byte path cap). `Create`'s `perm` must leave the file readable by the
  process (the `Undo` pins it with a read-only descriptor; `git add` reads
  it anyway).
- **New:** `git.TrackedInHEAD(ctx, repoDir, path)` — whether a path is an
  entry of HEAD (literal match; an unborn HEAD or any git failure is an
  error). The artifact and workflow create rollbacks use it to claim a file a
  failing hook left in place of the one they created.
- No exported signature changes in `artifact`, `workflow`, `git`,
  `engine.GitOperator` (the two functions above are additions); no
  migration, environment variable or config.
- Behaviour changes reachable from SMP: the status codes above at the embedded
  artifact and workflow endpoints (SMP TASK-008 adopts them). One of them is
  a **newly refused flow**, not a status remap, and SMP TASK-008 must look
  for callers that expect success rather than for 500 expectations: an
  artifact `WriteContent`/`Update`, accept or reject of an existing worktree
  file over 4 MiB (pushed by a runner or imported) succeeded before and is
  now 413.
  `CLIClient.WriteAndStageFile` (not used by SMP, whose `RealGitOperator`
  implements its own) refuses unclean paths and symlinks with an error that
  wraps `worktreefs.ErrUnsafePath`; the cascade logs it and proceeds with the
  merge, as for any status-rewrite failure (owner question 3 default).
- `artifact.RenumberArtifact` / `UpdateLinksToRenamedArtifact` (no caller)
  take clean repo-relative paths, refuse symlinks, and the link rewrite skips
  `.git` and symlinked files.

---

## INIT-023 — Security Audit Remediation (EPIC-003 TASK-010: webhook delivery hardening)

### Security

- **Opt-in v2 webhook signatures (audit P3-11).** A v1 signature
  (`X-Spine-Signature: sha256=<HMAC(body)>`) covers the body only, so a
  captured delivery verifies forever. A subscription whose metadata holds
  `{"signature_version":"v2"}` now gets `X-Webhook-Timestamp` (send-time Unix
  seconds), `X-Webhook-Nonce` (16 random bytes, hex) and
  `X-Spine-Signature: v2=<HMAC(ts + "." + nonce + "." + body)>` — the format
  the Spine Management Platform's receiver verifies — signed afresh on every
  attempt. **v1 stays the default and byte-identical**; the integration guide
  recommends v2 and lists a receiver checklist. An unknown or non-string
  version, or v2 without a signing secret, fails closed (the delivery is
  dead-lettered, never sent unsigned or downgraded).
- **A half-open circuit can no longer wedge a subscription (P3-12).** A probe
  that exited before reaching the endpoint (subscription read error, rejected
  URL, request build error) left the circuit half-open, so the subscription
  delivered nothing until restart. Every probe is now resolved; an early exit
  returns the circuit to open and the next poll probes again.
- **Failing subscriptions can no longer starve the delivery queue (P3-5).**
  Non-retryable 4xx answers, circuit-held rows, rows of deleted subscriptions
  and rejected URLs were written back as immediately claimable and re-claimed
  ahead of every newer row, so a few of them stalled every other
  subscription's deliveries for good. Every claimed delivery now ends
  `delivered`, `dead`, or scheduled in the future; the queue is claimed
  round-robin across subscriptions; a subscription that cannot be
  delivered to for a while (open circuit, unreadable subscription) has its
  whole due queue held until its next attempt, so it takes about two claim
  slots per probe cycle however large its backlog; and a subscription whose
  deliveries can never succeed (deleted, invalid metadata, refused target
  URL, unusable TLS or signing configuration) has its whole due queue
  dead-lettered at once, so it takes one claim slot — also when five or
  more such subscriptions have backlogs at once.
- **No address oracle in webhook target validation (P3-25).** Create, update,
  the subscription test, `last_error` and the delivery log no longer reveal a
  resolved IP address, an address class for a DNS name, Spine's own source
  address, or the DNS resolver's address — neither for a failed connect nor
  for a connection reset or broken pipe after connect. IPv6 transition addresses (NAT64 `64:ff9b::/96`, 6to4, IPv4-
  compatible, SIIT) are judged by the IPv4 address they embed, so for example
  `64:ff9b::a9fe:a9fe` (cloud metadata through NAT64) is refused; RFC 8215
  local-use NAT64, Teredo, site-local `fec0::/10` and `0.0.0.0/8` are refused.
- **Webhook URL secrets are redacted (P3-26).** Path, query and fragment of a
  target URL (where Slack/Discord-style webhooks carry their credential) no
  longer reach `last_error`, the delivery log, the dispatcher's logs or the
  subscription-test response: `Post "https://hooks.example/[redacted]": …`.
  Rows written before this change are not rewritten (they reach `dead` and are
  reaped by retention).

### Changed

- **Delivery outcomes.** Non-retryable failures go `dead` on their first
  attempt (they used to be `failed` and retried forever). `408` and `425` are
  now retryable, like `429` and 5xx. Rows whose subscription was deleted, whose
  metadata is invalid, whose target URL is refused or whose TLS or signing
  configuration cannot be used go `dead` at once — the first such row
  dead-letters every other row of the subscription that is due by the next
  poll in the same step — each with a delivery-log row giving the reason.
  HTTP-status outcomes keep the exact text `HTTP <code>`.
- **Unreadable subscription.** A transient failure to read the subscription
  (database, decryption) used to re-try the delivery on every poll until the
  fault cleared. It is now retried on the backoff schedule and then every
  minute, recorded as `resolve subscription: temporarily unavailable` (the
  database error goes to the operator log only), still counts its attempts,
  and is delivered automatically once the subscription can be read; it goes
  `dead` only once it has been held for 24 hours
  (`resolve subscription: temporarily unavailable: held longer than 24h0m0s`),
  counted from the first failed read of the current run of failures, or from
  when it was queued if that is later — a delivery queued days earlier is
  not dead-lettered by a fault that has just begun. The subscription's other
  due deliveries wait for the same retry without spending attempts.
- **Circuit holds.** A delivery held by an open circuit is deferred to the next
  probe time without consuming an attempt (it used to burn one per poll),
  together with the rest of its subscription's due queue, and goes `dead`
  once it has been held for 24 hours (`circuit open: held longer than
  24h0m0s`), counted from when the circuit opened, or from when it was
  queued if that is later. The start of a hold is kept in memory by the
  dispatcher process, so a restart starts the 24 hours again.
- **Claim order.** Due deliveries are claimed round-robin across subscriptions,
  oldest first within each; global cross-subscription FIFO was never
  documented. A single-subscription queue is claimed in the old order.
- **Error texts.** Validator refusals read `target_url: <class> address not
  permitted` for a literal IP, `target_url: host resolves to a non-public
  address (allowlist it via SPINE_WEBHOOK_ALLOWED_HOSTS if intended)` for a
  host name, `target_url: destination address not permitted` at connect time,
  `target_url: connect <host>:<port>: <reason>` for a connect failure,
  `read tcp <host>:<port>: <reason>` (or `write …`) for a failure after
  connect and `target_url: resolve <host>: no such host|timeout|temporary
  failure|lookup failed` for DNS. Parse errors no longer quote the URL.
- **Subscription API.** `metadata` is accepted as a JSON object (the
  documented form, which used to fail to decode) or, as before, a base64
  string; it is validated on `POST`/`PATCH` (`400 invalid_params`),
  including a key given in more than one letter case (`tls` and `TLS`), which
  used to be read one way by the API and another by the dispatcher.
  Subscription responses include `signature_version`, which is `"invalid"`
  whenever the stored metadata would not pass that validation.
- **Docs.** The integration guide's TLS pin example used the wrong key and
  encoding: the key is `tls.pinned_spki_sha256` and the value is hex (the CA
  key is `tls.ca_bundle_pem`).

### For embedders

- **New `store.DeliveryStore` methods** `DeferDelivery(ctx, deliveryID, reason,
  until)` and `HoldDueDeliveries(ctx, store.DeliveryHold)` (new type
  `store.DeliveryHold`); `*store.PostgresStore` implements both, with no
  schema change. Types that embed the `store.Store` / `store.DeliveryStore`
  interface compile unchanged; a type that implements `DeliveryStore` method
  by method must add them (the SMP v2 contract test's `contractQueue` fake
  needs `HoldDueDeliveries`; a no-op is enough for it).
- **Shared behaviour at the first bump past this change, whatever the
  opt-in** (SMP owner: SMP TASK-006 R1/R2 must land first):
  - a 4xx from the receiver dead-letters the delivery on its first attempt;
  - orphaned, malformed and URL-rejected rows go `dead`, and so does a row
    whose per-subscription TLS configuration cannot be built (it used to be
    `failed` and retried on every poll); the first such row of a
    subscription dead-letters the subscription's whole due queue in one
    statement (every row due by the next poll), not one row per claim;
  - a row held by an open circuit, or whose subscription cannot be read, goes
    `dead` once it has been held for `DispatcherConfig.MaxCircuitHold`
    (zero, the default, means 24 hours), counted from the later of its
    `created_at` and the start of the hold on that dispatcher process — the
    circuit opening, or the first failed read of the current run — which is
    kept in memory and starts again after a restart; before, held rows never
    became terminal from holding alone;
  - a transient failure to read the subscription (database, decryption, for
    example a replica without its encryption key) is retried with backoff and
    then every minute instead of on every poll, still counts attempts (so
    SMP's stuck-dispatch view shows it at the budget) and is delivered
    automatically once readable; it is never dead-lettered for this before
    `MaxCircuitHold`;
  - a dispatcher that holds a subscription (open circuit, unreadable
    subscription) holds its whole due queue in the shared database until its
    next attempt — with several dispatchers on one runtime database, a
    replica-local fault delays that subscription's due rows for the other
    replicas too, by up to the 60 s circuit recovery time per cycle;
  - `last_error` texts are redacted as above. NTP sync across receiving
    replicas becomes a prerequisite once a subscription opts into v2.
- **New, additive API in `adapters/delivery`:** `SignV1`, `SignV2`,
  `SignatureVersionV1/V2`, `MetadataKeySignatureVersion`, `HeaderSignature`,
  `HeaderTimestamp`, `HeaderNonce`, `ErrInvalidSubscriptionMetadata`,
  `ValidateSubscriptionMetadata`, `SignatureVersionOf`,
  `WithSignatureVersion`, `SubscriptionDetail.SignatureVersion`,
  `BootstrapConfig.SignatureVersion` (empty — the zero value — leaves the
  stored metadata alone), `DispatcherConfig.MaxCircuitHold` and
  `DefaultMaxCircuitHold`, `CircuitBreaker.Acquire` / `Permit` /
  `NextAttemptAt` / `OpenedAt`, `RedactURL`, `RedactError`, `BlockedAddressError`,
  `ErrBlockedDestination`, `DialError`.
- **`TargetValidator.CheckAddr`** now returns a `*BlockedAddressError`
  (text without the address) and `SafeDialContext` returns
  `ErrBlockedDestination` / `*DialError`; callers that matched the old texts
  (`"loopback address 127.0.0.1 not permitted"`) must match the class or use
  `errors.As` / `errors.Is`. A DNS failure from `SafeDialContext` (and so
  from `TargetValidator.HTTPClient` / `Transport`) has a new text without the
  resolver's address but still unwraps to the `*net.DNSError`, and still
  reports `Timeout()` for a DNS timeout, also through `url.Error.Timeout()`.
  The connection `SafeDialContext` returns re-renders read, write and close
  failures as a `*net.OpError` with no `Source` and with `Addr` naming the
  dialled host:port (`read tcp hooks.example:443: read: connection reset by
  peer`); `Op`, `Net`, the inner error and `Timeout()` are unchanged, so
  `errors.Is(err, syscall.ECONNRESET)` and `errors.As(err, &*net.OpError)`
  still work. `RedactError` also re-renders any other `*net.OpError` that
  names addresses.
- `CircuitBreaker.AllowDelivery`, `RecordSuccess`, `RecordFailure` and
  `State` are unchanged, but `AllowDelivery` is documented as not for
  dispatch use (it cannot resolve a probe).

---

## INIT-023 — Security Audit Remediation (EPIC-004 TASK-013: identity and skill authority)

All of the HTTP and startup changes below are in standalone `spine serve` (`service/`). The
`core/auth` changes reach embedders; see "For embedders".

### Security

- **Skill management is admin-only (audit F6).** Any contributor could grant itself any skill,
  strip one from another actor, or mint, rename or deprecate catalogue entries — and step
  eligibility is decided by skill names, so a contributor could grant itself `review`, claim and
  accept the review of its own run, and have it merged into `main`. Now:
  - `POST /api/v1/skills`, `PATCH /api/v1/skills/{id}` and `POST /api/v1/skills/{id}/deprecate`
    by a contributor, reviewer or operator answer **403** (were 2xx).
  - `POST`/`DELETE /api/v1/actors/{other}/skills/{id}` by a non-admin answer **403**
    (insufficient role; were 2xx for contributor and up). The routes authorize the new
    admin-only operations `actor.add_skill` / `actor.remove_skill` instead of `skill.update`.
  - A non-admin's request on its **own** `/actors/{self}/skills/{id}` answers **403** with
    `reason=self_skill_change_forbidden` in the error detail, and is audit-logged. This holds
    for a percent-encoded id too (`/actors/op%2D1/…` for `op-1`): `{actor_id}` is now decoded
    once. A malformed escape in it is refused with **400**: over HTTP by net/http itself, before
    routing, with a plain-text body (not the JSON error envelope); an in-process caller that
    hands the handler a raw path gets the JSON `invalid_params` error.
  - An admin may manage any actor's skills, its own included (2xx, now audited).
  - Renaming a skill whose old or new name an active workflow's `required_skills` references
    answers **409** unless `?force=true` is passed (was 200), with the same body shape as
    deprecation and `hint: "use ?force=true to rename anyway"`.
  - `PATCH /api/v1/skills/{id}` and `POST /api/v1/skills/{id}/deprecate` re-read the skill right
    before writing it, which **narrows, but does not close,** the window in which a concurrent
    admin request is silently undone (a description edit could write a stale `active` status back
    over a forced deprecation, and a deprecation could revert a concurrent rename, across the
    whole request including its workflow scans). If the re-read finds the skill renamed since the
    request first read it, the request answers **409 `conflict`** ("skill was renamed by a
    concurrent request; retry") and writes nothing. A change another admin commits between the
    re-read and the write is still overwritten, because the write replaces every column; a
    conditional `UPDATE` is a recorded follow-up (`KNOWN-LIMITATIONS.md`, "Skill Eligibility Is
    Matched by Name").
- **Actor role ceiling (audit P3-33).** `POST /api/v1/actors` by an operator with role
  `operator` or `admin` answers **403** with `reason=role_ceiling_exceeded` (was 201). A
  non-admin may register only roles strictly below its own; `actor.create` itself stays an
  operator operation. Admins may register any role.
- **Rotating `SMP_ADMIN_TOKEN` revokes the previous bootstrap bearer (audit P3-31).** The old
  bearer used to stay valid as an admin of every workspace after a rotation. Now each
  workspace's first load after the restart revokes every older `bootstrap-…` row of
  `smp-admin` created before that replica started; tokens an admin minted for `smp-admin` are
  kept. Revocation is never undone: a **rollback to a revoked bearer is refused (401)** and the
  load logs at Error (`SMP_ADMIN_TOKEN matches a revoked bootstrap admin token`).
- **A suspended or deactivated `smp-admin` stays that way (audit P3-31).** The bootstrap used
  to re-activate it on every workspace load, undoing incident response. It now heals only name
  and role (and an empty status), and logs at Warn that the platform bearer will be refused.
- **Bootstrap token ids no longer disclose the bearer (audit P3-35).** New bootstrap rows get
  `bootstrap-<16 random hex>` ids instead of `bootstrap-<sha256(bearer)[:12]>`, which a workspace
  admin could list and use as an offline oracle; the insert log no longer prints the hash.
- **`SMP_ADMIN_TOKEN` strength gate (audit P3-35).** A value shorter than 32 characters or with
  fewer than 8 distinct characters makes `spine serve` refuse to start when
  `SPINE_ENV=production` and log a warning elsewhere (it was accepted). The message never echoes
  the token.

### Changed

- **`SMP_ADMIN_TOKEN` rotation is no longer zero-downtime.** Platform requests that still carry
  the old bearer get **401** from each workspace's first load after the Spine restart until the
  platform switches (were 2xx). Switch the platform's bearer in the same change window
  (`docs/operator-runbook.md` §5.6).
- **Rollout residues.** Both are confined to workspaces first loaded during a rotation's rollout
  (a row for one bearer and none for the other). Prefer a recreate strategy for rotations.
  - An old-config replica that restarts mid-rollout and then loads such a workspace revokes the
    *new* bearer there. Recover by rotating forward to a fresh value.
  - If the new bearer is seeded first and a replica still carrying the old bearer (an old-config
    replica, or pre-upgrade code during the upgrade rollout) then loads the workspace, the *old*
    bearer is inserted after the new one and **stays valid until the next rotation**: no replica
    carrying the new bearer ever revokes a row newer than its own, restarts included. Only a
    replica that **started after** that row was written logs it, at Warn ("a live bootstrap admin
    token is newer than the current SMP_ADMIN_TOKEN's row …") with the row's `token_id`, on each
    load of such a workspace. A replica that was already running when the row was written stays
    quiet on every load of it, idle-eviction reloads included, because its data looks the same as
    a rollout still in progress. So **restart every replica once more after a rolling rotation**
    (idle eviction is not enough), watch for the Warn, and revoke each named row with
    `DELETE /api/v1/tokens/{token_id}` (`docs/operator-runbook.md` §5.6 (e)).
- **A failed admin bootstrap is retried in the background.** If a workspace load's bootstrap
  fails with a store error (for example while revoking a superseded bearer), `spine serve` logs
  at Error, keeps the workspace in service, and retries (from 1s, doubling to at most 1 min)
  until it succeeds or the workspace is unloaded. Before, it was retried only when the
  workspace was next loaded, which for a busy workspace meant the next restart. A hash collision
  is not retried.
- **Unsetting `SMP_ADMIN_TOKEN`** after it was set still leaves the existing bootstrap row valid
  (unchanged); retire it with `DELETE /api/v1/tokens/{token_id}`.
- **Dev mode** (`SPINE_DEV_MODE`, no actor in context) is unchanged: allow-all on these routes.
- **New audit lines** (`[AUDIT]`, `audit_operation=`): `actor_skill_added`,
  `actor_skill_removed`, `actor_skill_change_refused`, `skill_renamed`, `skill_deprecated`,
  `actor_created`, `actor_create_refused`, `bootstrap_admin_token_revoked`. None carries a
  bearer. In `spine serve` the admin bootstrap's log and audit lines now carry `workspace_id`
  (they named no workspace before).
- **Wire format:** new 403 detail `reason` values `self_skill_change_forbidden` and
  `role_ceiling_exceeded`; the new `bootstrap-<16 hex>` token id format (legacy 12-hex ids keep
  their value until rotated).

### Upgrade notes

- **Bootstrap bearers from past rotations are revoked on first load.** Earlier versions never
  revoked, so every older bootstrap row of `smp-admin` that still authenticates is revoked by the
  first load of each workspace by the new code. Make sure the platform uses the current
  `SMP_ADMIN_TOKEN`. A row inserted by pre-upgrade code *after* the current bearer's row (a
  workspace first loaded during the upgrade rollout) is newer than it and is not revoked. Only a
  replica started after that row was written logs it at Warn, so **restart every replica once
  more when the upgrade rollout has finished** (idle eviction is not enough); see "Rollout
  residues".
- **Review pre-existing actor-skill grants.** Grants made by non-admins before the upgrade stay in
  place, and `auth.actor_skills` records no grantor. Review holders of `review`,
  `workflow_review` and `architecture` (for example `GET /api/v1/actors/{id}/skills` per actor,
  or SQL on `auth.actor_skills`) and remove unexpected grants as an admin.
- **Integrations whose non-admin actors (including AI agents) register or manage skills** must
  move that to an admin token.
- **Rotate `SMP_ADMIN_TOKEN` once after upgrading** to retire the legacy hash-derived token id of
  the live bearer.

### For embedders

- **This is a shared-behaviour change, not an opt-in** (INIT-023 §7b rule 4, recorded there for
  TASK-013): the zero `BootstrapAdminConfig` no longer means "today's behaviour".
  `BootstrapInternalAdmin` with only `Token` set now revokes superseded bootstrap rows, keeps a
  suspended or deactivated `smp-admin` as it is, and mints random ids. It is inert in an
  embedder that never validates the `auth.tokens` rows it writes (SMP today). An embedder that
  adds such a verifier should pass its process start as `SupersededBefore`, and must accept that
  revocation is never undone: re-running the bootstrap with a mistyped bearer revokes the real
  one, and a rollback to a revoked bearer stays refused until the bearer is rotated to a fresh
  value.
- **`auth.BootstrapAdminConfig.SupersededBefore time.Time` (new, optional).** Rows created at or
  after it are never revoked; the zero value revokes every bootstrap row older than the current
  one. A row newer than the current one is never revoked, whatever the cutoff; with no cutoff, or
  when it predates the cutoff, it is logged at Warn with its token id. `spine serve` passes its
  process start. Keyed literals compile unchanged.
- **`auth.BootstrapInternalAdmin` behaviour.** It now revokes superseded bootstrap rows of
  `smp-admin` (it calls `AuthStore.ListTokensByActor` and `RevokeToken`, both already part of
  `store.AuthStore`; a store whose `RevokeToken` returns `ErrNotFound` for an already-revoked row
  is handled), never re-activates a suspended or deactivated `smp-admin`, mints random token ids,
  and returns nil (Error log) when the configured bearer's row is revoked. Revocation errors other
  than not-found are returned, wrapped: the two extra store calls are a new way for the call to
  fail, and a caller that retries (SMP's provisioning step does) completes the sweep on the next
  call, because the sweep also runs when the current bearer's row already exists. It still
  accepts any non-empty bearer: strength policy is the embedder's. SMP's provisioning seed is
  unaffected (no SMP path consults `auth.tokens`).
- **`core/auth` operation map:** `skill.create`, `skill.update`, `skill.deprecate` now require
  `admin`; new operations `actor.add_skill` and `actor.remove_skill` (admin). Callers of
  `auth.Authorize` / `auth.RequiredRole` see the new roles.
- **New:** `auth.AuthorizeActorSkillChange(caller, op, targetActorID)` and
  `auth.AuthorizeRoleGrant(caller, granted)`, with `auth.ReasonSelfSkillChange` and
  `auth.ReasonRoleCeiling`. Both refuse a nil or non-active caller first;
  `AuthorizeRoleGrant` also refuses an unknown or empty granted role for every caller.

---

## INIT-023 — Security Audit Remediation (EPIC-001 TASK-001: pre-receive ref validation)

### Security

- **Git push gate evaluates the ref git actually updates (audit F1, P1).** The
  pre-receive parser split the `\0<capabilities>` suffix off the first command
  line only, while `git receive-pack` ends *every* command's ref at its first
  NUL. A NUL after the ref on a later command line therefore made the gate
  evaluate a different ref than git updated, so a pusher could write to or
  delete a literal-protected branch (`main: [no-direct-write]`,
  `develop: [no-delete]`) with no override event. Every command line is now
  validated: hex object ids of one hash length, and a ref that passes git's
  refname grammar (new `refname.ValidateQualified`) — no NUL or other control
  byte, `refs/` prefix, no `..`/`@{`/`.lock`, and the rest. A NUL on any line
  but the first is refused, and the first line's capability list must be
  printable ASCII so the gate and git read the same feature set. Such a push is
  refused whole as `branch-protection: malformed push: command N: …`. No push
  that git would apply as written is *newly* refused, with one deliberate
  exception: a first-command capability list that is not printable ASCII (a
  TAB separator, or a byte >= 0x80, both of which git itself accepts). No git,
  JGit, libgit2 or go-git client sends such a list — git rewrites a
  non-printable `agent=` to `.` — so well-formed pushes from a full clone,
  push options and deletes are unchanged. Shapes the gate already refused stay
  refused, now as `command 1: malformed ref update …`. That includes a push
  from a **shallow clone** (`git clone --depth`, common in CI): git sends a
  `shallow <oid>` line ahead of the commands and applies the push, but the gate
  refused it before this change and still does. Push from a full clone.
- **SHA-256 deletes are classified as deletes.** The gate recognised only the
  SHA-1 null object id (40 zeros), so in a SHA-256 repository a delete (64
  zeros) was evaluated as a direct write and passed a `no-delete`-only rule.

### Changed

- **Message text:** parse-error denials now read
  `branch-protection: malformed push: command N: …`. The documented
  `malformed push` prefix is unchanged. Such a denial carries no per-ref `ng`
  lines (it never did); `docs/git-push-guide.md` §5 now says so and lists the
  causes.
- **Deployment requirement documented:** with pushes enabled, served
  repositories must live on case- and normalization-sensitive storage
  (`KNOWN-LIMITATIONS.md` → Security Notes). Enforcing it is a follow-up.

### For embedders

- **`branchprotect.Policy.Evaluate` contract change.** `Evaluate` now returns
  `(DecisionDeny, nil, err)` for a `Request.Branch` containing an ASCII control
  byte (0x00–0x1f, 0x7f), for every operation kind and rule set, ahead of every
  allow path (governed merge, non-branch ref, no matching rule). A non-nil error
  now also means *malformed request*, not only *rule source unavailable*;
  callers already treat any error as a refusal. A malformed `Branch` is not
  always a caller bug: when `Branch` embeds client input, validate that input
  first and report it as invalid input, because this error cannot tell bad input
  from an outage. Spine's own divergence branch API does so since INIT-023
  TASK-016: it validates `branch_id` first (`divergence.ValidateExploratoryBranchID`)
  and answers HTTP 400 for a control byte (it answered 500 while only this guard
  refused it). This is a second layer behind the
  parser: an embedder that passes a pushed ref to `Evaluate` raw (SMP's gateway
  does) is protected from F1 by this guard alone.
- **New:** `adapters/git/refname.ValidateQualified(ref string) error`, git's
  refname grammar for fully-qualified refs, returning `ErrInvalidParams`.

---

## INIT-023 — Security Audit Remediation (EPIC-001 TASK-002: one ref grammar for the git client)

### Security

- **A pushed tag or ref can no longer stand in for a run branch or `main` (audit F10, P2).** git
  resolves a bare name by DWIM (`refs/<n>` and `refs/tags/<n>` before `refs/heads/<n>`), and
  branch protection allows every push outside `refs/heads/`. A `refs/tags/<run-branch>` made the
  governed merge publish the tag's unreviewed commit under the trusted merge message (also through
  the commit-status cascade, which read the shadow's files); a `refs/tags/main` made every
  governed push fail permanently. The git client now looks every branch up exactly
  (`git show-ref --verify refs/heads/<n>`) and hands git the object id: merge sources, branch
  bases, checkouts, worktrees, pushes, and every read of a name that is an existing branch. A
  missing branch is `not_found`, never a DWIM fallback to `refs/tags/refs/heads/<n>`. Pushes send
  `<oid>^{commit}:refs/heads/<n>`, which no local ref can make ambiguous.
- **A ref named after an object id or `HEAD` no longer redirects or blocks run-branch creation.**
  `git branch` is the one git command that re-resolves its start point by DWIM after parsing it.
  So a pushed `refs/tags/<oid>` (or `refs/<oid>`, `refs/heads/<oid>`, `refs/remotes/<oid>`) named
  after the base's object id replaced that base: a run branch cut from `main`, `refs/heads/main` or
  the object id itself started at the attacker's commit (the code-repository run-start path, and
  embedders). A `refs/HEAD`, `refs/tags/HEAD`, `refs/heads/HEAD` or `refs/remotes/HEAD` made
  `git branch <run> HEAD` fail with `ambiguous object name: 'HEAD'`, so one pushed ref blocked every
  run start and every divergence branch in the workspace (this predates INIT-023). `CreateBranch`
  now passes `<oid>^{commit}` or `HEAD^{commit}`, which no ref name can match.
- **Ref arguments never become git options (P3-32).** Every ref argument is validated before any
  git process starts and follows `--end-of-options`; `diff`, `show` and `log` end in `--`.
- **`default_branch` is validated (P3-36, dormant).** A code repository's `default_branch` reached
  git argv unchecked (`--force` force-pushed, `--pathspec-from-file=<file>` echoed a host file
  into the merge outcome). The repositories API now refuses a value that is not a branch name,
  and the engine refuses it at run start and code-repo merge as a precondition, with no git call.

### Changed

- **Git 2.30 or newer is required.** `spine serve` refuses to start below it (owner question 3).
  Every shipped image runs Debian bookworm's git 2.39.5 or newer; Ubuntu 20.04's 2.25 is refused.
- **`Clone` no longer imports tags** (`git clone --no-tags`, persisted as
  `remote.origin.tagOpt=--no-tags`; owner question 1). On a workspace or code-repo clone
  provisioned after this change, `?ref=<upstream tag>` reads fail until the tag is pushed to the
  workspace repository. In standalone the provisioned clone **is** the repository the git endpoint
  serves, so runners' clones of a newly provisioned workspace carry no upstream tags either: a
  runner step that runs `git describe --tags` or derives a version from a tag finds none. Existing
  clones are unaffected, and tags pushed to the workspace repository later are served as before.
- **A bare `?ref=<name>` that names an existing branch now always reads the branch,** even when a
  same-named tag or `refs/<name>` exists (before, the shadow won). Every other `?ref=` value
  resolves as before.
- **Branch names are validated by git's own grammar as well** (`refname.Validate` converges on
  `refname.ValidateQualified`): a component starting with `.` or ending in `.lock`, `//`, a
  leading `/`, a trailing `.`, `HEAD`, `@` and names under `refs/` are now refused (HTTP 400 on
  `write_context.branch`). git refuses to create all of them as branches.
- **`default_branch`** on `POST`/`PATCH /api/v1/repositories` must be a short branch name
  (`main`, not `refs/heads/main`; HTTP 400 `invalid_params`). The engine applies the same rule to a
  stored value at run start and at code-repository merge (a precondition failure, no git call);
  code-repository catalogs are not wired in any shipped build.
- **A path missing at a revision that exists in the served working tree is now `not_found`
  (HTTP 404), not `git_error` (HTTP 500).** git reports that case as
  `path '<p>' exists on disk, but not in '<rev>'`, which the classifier did not recognise
  (`permanent`). With the trailing `--` on `git show` it reports `bad revision`, which is
  `not_found`. Artifact and workflow reads (`GET` with `?ref=`) now return 404 for it, and
  `evidence.Load` returns `Found: false` instead of an error. Every other read miss was already
  `not_found`.
- **Error text.** Grammar refusals read `git <op>: invalid git ref: <what> "<value>": <cause>:
  invalid git ref`. Every `ReadFile` miss now reads `fatal: bad revision '<rev>:<path>'` (it read
  `path '<p>' does not exist in '<rev>'`), where `<rev>` is the branch's object id for a branch
  read; the kind is `not_found`, as before. Log consumers matching `does not exist in` must match
  `bad revision` instead. A checkout of a missing branch carries `git switch`'s message.

### Changed (embedders)

- **`CLIClient.Push` accepts branches only** (owner question 2): a tag or any other refspec is
  refused with `git.ErrInvalidRef`. Every Spine and SMP caller pushes branches.
- **`CLIClient.PushBranch` no longer sets upstream tracking** (`branch.<n>.remote` /
  `branch.<n>.merge`; owner question 4). Nothing in Spine or SMP reads them.
- **Grammar refusals are `*git.GitError{Kind: ErrKindPermanent, Err: git.ErrInvalidRef}`,**
  returned before any git process; `Diff`/`MergeBase` refusals used to be plain wrapped errors
  (the `invalid git ref` text is kept). An empty revision is refused (`ReadFile("", p)` used to
  read the index). `RefSHA` uses `rev-parse --verify` and still returns `not_found` for an unknown
  ref.
- **`Merge`:** the abort sentinel (`Strategy: "abort"` or `Source: "--abort"`) runs exactly
  `git merge --abort`, with no checkout. A branch source is merged by object id, so without
  `Message` git's default text reads `Merge commit '<oid>'`; a missing source branch is
  `not_found`.
- **Revision arguments are validated** (`ReadFile`, `ListFiles`, `RefSHA` and `TreeEntryType` ref,
  `LogOpts.Since`; `Diff` and `MergeBase` already were; `TreeEntryType` is the optional
  `git.TreeEntryTyper` capability added by INIT-023 TASK-011): they must pass `git.ValidateRef`, so rev expressions such
  as `HEAD~1`, `<sha>^`, `HEAD^{tree}`, `a..b`, and anything containing `:`, `\` or a space are
  refused with `git.ErrInvalidRef` before git runs. Resolve such an expression to an object id
  first. No Spine or SMP caller passes one; the gateways' `?ref=` already refused them.
- **A revision given by name costs one `git show-ref` per call** (`ReadFile`, `ListFiles`,
  `RefSHA`, `TreeEntryType`, `Diff`, `MergeBase`, `LogOpts.Since`). This applies to any bare name that is a valid
  branch name, whether or not that branch exists: a branch, a tag such as `v1.2`, an abbreviated
  id or `origin/<n>` (each is first looked up as `refs/heads/<name>`). It also applies to
  `refs/heads/<n>`. Full object ids, `HEAD` and other `refs/*` names (`refs/tags/<t>`,
  `refs/remotes/<r>/<n>`) cost nothing extra. `RefSHA` of an existing branch runs the lookup
  instead of `rev-parse`. A caller that reads many files at one name, whether a branch, a tag or
  `origin/<n>`, should resolve it once with `RefSHA` and read by object id, as
  `artifact.Service.List` now does; that also reads one consistent snapshot. SMP's
  `RunBranchDiffReader` (reads by `refs/heads/<run>`) pays this per file until SMP INIT-027 EPIC-004
  TASK-008 resolves its run ref once.
- **`CreateBranch`** cuts the branch from `<oid>^{commit}` or `HEAD^{commit}` (see Security), so
  git never writes upstream tracking for it, even with `branch.autoSetupMerge=always`.
- **`Checkout` uses `git switch --no-guess`:** it never detaches onto a tag and refuses while a
  merge, rebase, cherry-pick, revert or bisect is in progress.
- **No branch is guessed from a remote-tracking ref any more.** At `main`, with no local
  `refs/heads/<n>` but a unique `refs/remotes/origin/<n>` (and `origin` configured),
  `Checkout(<n>)` (`git checkout <n>`) and `EnterBranch(<n>)` (`git worktree add <dir> <n>`)
  created a local branch tracking it and used that. Now `Checkout` fails and `EnterBranch` returns
  a wrapped `not_found` `*GitError`; no local branch is created. A standalone
  `write_context.branch` that exists only as `origin/<n>` in the served repository is therefore a
  `git_error` instead of an implicit branch creation. Spine creates run branches locally, and SMP
  calls `EnterBranch` only with `""`. A `CreateBranch` base in that state was already refused
  (`git branch` never guessed: `not a valid object name`, permanent); it is now `not_found`.
- **`EnterBranch`** adds the worktree detached at the branch's object id, then switches to the
  branch and checks `symbolic-ref HEAD`. For an existing local branch the resulting worktree is the
  same as before; a missing branch (including one present only as `origin/<n>`, above) is a wrapped
  `not_found` `*GitError`. **`CurrentBranch`** reads `symbolic-ref` (it returned `heads/<n>` when a
  same-named tag existed) and returns the branch name on an unborn `HEAD`.
- **`artifact.Service.List`** resolves its ref once and reads one snapshot.
- **New API (additive):** `git.ErrInvalidRef`, `git.IsFullObjectName`, `git.QualifyBranch`,
  `git.ValidateBranchName`, `git.ValidateShortBranchName`, `git.ValidateRemoteName`,
  `git.CheckGitVersion`, `git.MinGitVersion` (`"2.30"`). Embedders should call `CheckGitVersion`
  at boot (SMP INIT-027 EPIC-004 TASK-008). The `GitClient` method set and the `MergeOpts`,
  `GitError` and `FileDiff` types are unchanged.

### Upgrade notes

- **Delete doubly-qualified refs in every served repository.** A ref such as
  `refs/heads/refs/heads/main`, `refs/refs/heads/main` or `refs/tags/refs/heads/main` in a
  repository clients push to makes every push to the branch it shadows fail
  (`dst refspec refs/heads/main matches more than one`) — in standalone, every governed merge.
  This release makes such refs inert in the engine's own lookups but cannot fix the remote side;
  push admission (INIT-023 TASK-016) is what refuses new ones. Existing ones are not deleted
  automatically (owner question 5). List them with
  `git -C <repo> for-each-ref refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs`
  and delete every one listed (`git -C <repo> update-ref -d <ref>`).
- **Delete refs named `HEAD` or named after an object id as well.** Spine's own git calls are now
  immune to them, but no legitimate workflow creates them, and other git clients (runners'
  clones, operators' shells) are not: `git branch <n> HEAD` or `git checkout -b <n> HEAD` dies
  `ambiguous object name: 'HEAD'` next to a `refs/tags/HEAD`, and `git branch <n> <oid>` or
  `git checkout <oid>` can land on a ref named `<oid>`. List them with
  `git -C <repo> for-each-ref refs/HEAD refs/tags/HEAD refs/heads/HEAD refs/remotes/HEAD` and
  `git -C <repo> for-each-ref --format='%(refname)' | grep -E '^refs/((tags|heads|remotes)/)?([0-9a-fA-F]{40}|[0-9a-fA-F]{64})(/HEAD)?$'`,
  and delete every one listed the same way. Push admission (INIT-023 TASK-016) refuses new `HEAD`
  components; it does not refuse object-id-named refs, which remain a residual (see
  `KNOWN-LIMITATIONS.md`, "Git Ref Arguments").

---

## INIT-023 — Security Audit Remediation (EPIC-003 TASK-009: enc:v2 secret envelope and re-encryption sweep)

### Security

- **Webhook signing secrets are bound to their workspace and row (audit
  P3-10).** The `enc:v1` at-rest envelope had no associated data and no key
  id, and one key is shared by every workspace database, so a signing secret
  copied from workspace A's `runtime.event_subscriptions` row into workspace
  B's opened in B, and B then signed its deliveries with A's key. Standalone
  `spine serve` now writes the new `enc:v2` envelope: AES-256-GCM under an
  HKDF subkey, naming its key (`kid`), and authenticating the workspace id
  this process serves (from configuration, never the row) and the
  subscription id. A transplanted or swapped `enc:v2` value is refused, never
  read as plaintext.
- **P3-10 is closed only after `strict` mode or key retirement — not by
  default.** This release's default mode, `migrate`, still accepts legacy
  `enc:v1` and plaintext values and converts them, because it cannot tell a
  genuine pre-upgrade `enc:v1` row from a copy of another workspace's
  pre-upgrade `enc:v1` value; the audit's attacker holds exactly such a copy
  (a pre-upgrade backup). Once every workspace has swept clean, set
  `SPINE_SECRET_ENVELOPE_MODE=strict`, or rotate the key and drop every key
  that ever sealed an `enc:v1` value. Making `strict` the default is a
  recorded follow-up (owner question 1, default taken pending the owner).
  Both closing steps stop values planted **from then on** only: a plant a
  `migrate` sweep already converted is a valid `enc:v2` row and survives
  `strict` and a key rotation. The sweep is the only path that converts a
  legacy value to `enc:v2` (a write that keeps a subscription's secret —
  `PATCH`, `pause`, `activate` — leaves the stored bytes untouched), and it
  logs every conversion per row, so review that log against its documented
  baseline (the first clean `migrate` sweep after any `downgrade` period,
  older binary or keyless period) and `rotate-secret` what it flags before
  either step (`docs/integration-guide.md` §2.1).
- **Adding the key later now encrypts existing rows (audit P3-1, Spine
  half).** Rows written without a key stayed plaintext forever: the comment
  and docs promised re-encryption "on the next update", but the bootstrap and
  update paths skip unchanged secrets, so that write never came. Spine now
  runs an explicit re-encryption sweep each time it opens a workspace store
  (file mode: at startup; shared mode: when a workspace loads) and on demand
  (`spine secrets reencrypt [--all-workspaces]`). The sweep converts
  plaintext, `enc:v1` and previous-key rows to the mode's envelope under the
  primary key with a compare-and-swap (a concurrent `rotate-secret` wins),
  leaves rows it cannot open untouched and reports them, and never logs a
  secret or a stored value.
- **Keys rotate.** `SPINE_SECRET_PREVIOUS_KEYS` adds decrypt-only keys to the
  ring; the sweep re-keys every row to the primary key, after which the old
  key can be removed. Previously rotation was a manual decrypt-and-re-save.
  With overlapping processes, rotate in two rollouts: first add the new key
  to every ring as a previous key, then promote it (a process without the
  new key refuses rows re-keyed to it, and its webhook fan-out fails).
  A process still on the first rollout can re-key a workspace back to the
  old key while the promotion rolls out, so verify every workspace once the
  promotion has completed and before removing the old key.

### Added

- `SPINE_SECRET_PREVIOUS_KEYS` (comma-separated base64 keys, decrypt-only;
  requires `SPINE_SECRET_ENCRYPTION_KEY`) and `SPINE_SECRET_ENVELOPE_MODE`
  (`migrate` default, `strict`, `downgrade`; case-insensitive; anything else
  refuses to start).
- `spine secrets reencrypt [--all-workspaces]`: sweeps the file-mode
  workspace, or every registry workspace — active and inactive (an inactive
  workspace's rows still need their key); prints counters and refused rows
  only (database-sourced text quoted; database errors carry no
  credentials); exits non-zero unless every workspace is clean. Use it before switching to `strict` or dropping a previous key.
  It requires `SPINE_SECRET_ENVELOPE_MODE` to be set explicitly, to serve's
  mode (it never assumes `migrate`); without `--all-workspaces` it also
  requires the workspace id to be stated — `SPINE_WORKSPACE_ID` set to serve's
  value, or `--workspace default` — because the id is part of every `enc:v2`
  value's associated data and a guessed `default` seals rows a serve bound to
  another id refuses (INIT-023 wave 2 cross-task review); its first line names
  the bound id (`workspace_id=`); `--all-workspaces` requires
  `WORKSPACE_RESOLVER=db`; `--allow-unreachable-inactive=<ids>` acknowledges
  decommissioned inactive workspaces whose database is gone (listed in the
  summary). `platform-binding` has no registry to enumerate, and a request
  to a workspace a process already holds does not sweep it: after a rolling
  update, restart every process once the rollout has completed, then load
  every workspace and check its latest `subscription secrets swept` record
  across all replicas (`docs/integration-guide.md` §2.1, *Verifying every
  workspace*). In file and `db` mode, run the command once the rollout has
  completed: a process still on the previous setting can sweep a workspace
  back while it runs.
- Startup log `secret envelope configured` (`primary_key_id`,
  `previous_key_ids`, `mode`) and per-workspace sweep logs
  (`subscription secrets swept`, one `subscription secret converted` per row,
  one `Error` per refused row). After a clean `migrate` sweep Spine logs a
  hint to switch to `strict` (`Warn` when `SPINE_ENV=production`).

### Changed

- **A refused signing secret fails its reads.** Under the new envelope a row
  that does not open (wrong or removed key, transplanted from another
  workspace or row, a legacy value in `strict`, an unknown `enc:v<N>:`
  version) fails `GetSubscription` and the list calls, as a wrong-key row
  always did; one refused active row stops the workspace's webhook delivery
  for the event types it subscribes to until it is fixed. Rows are never
  skipped or blanked.
- **A write that keeps a subscription's secret no longer rewrites it.**
  Under the new envelope, `PATCH`, `pause`, `activate` and a bootstrap that
  only realigns the target URL write the other columns and leave the stored
  secret's bytes untouched, so only the (logged) sweep converts legacy
  values. "Keeps" is judged against the secret the caller read, so a
  `rotate-secret` that lands between a `PATCH`'s read and its write is
  neither reverted nor replaced by the stale secret.
- **`POST /subscriptions/{id}/rotate-secret` no longer reads the old
  secret.** It replaces the stored value directly
  (`PostgresStore.ReplaceSubscriptionSecret`), so it is the repair for a
  refused row; a read-modify-write rotation would fail on exactly those
  rows. The response is unchanged.
- **One workspace per runtime database is now load-bearing.** Two workspaces
  registered on one runtime database refuse each other's rows once bound.
  In file mode, two deployments that share a key and the default
  `SPINE_WORKSPACE_ID=default` can transplant `enc:v2` values between them:
  use a distinct key per deployment, or a distinct workspace id.
- Doc fix: `README.md` names the crypto package `core/crypto`.

### For embedders

- **No behaviour change until you opt in.** A `store.PostgresStore` with no
  `SetSubscriptionSecretBinding` writes `enc:v1` and reads plaintext and
  `enc:v1` exactly as before. `crypto.NewSecretCipher`, `Encrypt`, `Decrypt`,
  `ParseEncryptionKey`, `EncryptionKeySize`, `SetSecretCipher`,
  `NewPostgresStoreWithQuerier`, the `SubscriptionStore` interface and
  `delivery.BootstrapInternalSubscription` keep their signatures.
- **New `core/crypto` API:** `SecretCipher` is now a key ring
  (`NewSecretCipherRing(primary, previous...)`, `KeyID`, `PrimaryKeyID`,
  `KeyIDs`); the `enc:v2` envelope is `Seal(plaintext, aad)` /
  `Open(value, aad) (Opened, error)` with `AssociatedData(purpose,
  fields...)`; `ClassifyEnvelope`/`Envelope`; sentinels `ErrNoCipher`,
  `ErrMissingAAD`, `ErrUnknownKeyID`, `ErrEnvelopeAuth`,
  `ErrMalformedEnvelope`, `ErrUnsupportedEnvelope`. A zero-value
  `SecretCipher` now returns errors instead of panicking.
- **New `adapters/store` API:** `SetSubscriptionSecretBinding(
  SubscriptionSecretBinding{WorkspaceID, Mode})`, `SubscriptionSecretBinding()`,
  `ParseSecretEnvelopeMode`, `ReplaceSubscriptionSecret(ctx, id, secret)`
  (replace without opening the stored value), `ReencryptSubscriptionSecrets(ctx)
  (SecretSweepReport, error)` with `SecretSweepIssue`/`SecretSweepConversion`,
  and sentinels `ErrSecretCipherRequired`, `ErrNoSecretBinding`,
  `ErrLegacySecretRefused`, `ErrEnvelopeInput`, `ErrNestedEnvelope`. Under a
  binding, writes take plaintext secrets only (an envelope input is refused)
  and every store that touches one workspace's subscriptions must use the
  same workspace id and mode. A bound `UpdateSubscription` writes the
  `signing_secret` column only when the caller set a secret: for a value
  returned by a bound read (`GetSubscription`, the list calls — the value
  remembers the secret that read returned, in unexported fields) it
  compares `SigningSecret` with that; for a value the caller built it
  compares with the stored value (one extra `SELECT`). A successful bound
  `CreateSubscription` or `UpdateSubscription` refreshes what the value
  remembers to the secret it left in the row, so a value reused for a
  further update is judged against that, not against its first read. Build
  updates from the value the store returned. SMP adopts this in SMP
  INIT-027 EPIC-004 TASK-006.
- **Deltas reachable without a binding** (none is reachable from SMP's
  current inputs: its credentials are hex and it produces no `enc:v2`
  value):
  - `Decrypt("enc:v2:…")` now errors instead of returning the envelope text
    as if it were the secret: with a cipher the error wraps `ErrMissingAAD`;
    with a nil or zero-value cipher it is `ErrNoCipher` (as for `enc:v1`).
  - `IsEncrypted` is true for `enc:v2:`. So an unbound `CreateSubscription`
    or `UpdateSubscription` stores an input that starts with `enc:v2:`
    **verbatim** (the never-double-encrypt pass-through), where it used to
    seal it as `enc:v1`; reading that row back through an unbound store
    then errors.
  - `Decrypt` opens `enc:v1` with every ring key. Its error texts changed:
    an authentication failure is `decrypt: secret envelope failed
    authentication (…)` (wraps `ErrEnvelopeAuth`), and a short or
    undecodable `enc:v1` value is `malformed secret envelope: …` (wraps
    `ErrMalformedEnvelope`).

### Upgrade and rollback

- **One replica, no overlap:** stop the old process before the new one
  starts (Kubernetes `strategy: {type: Recreate}` — the default
  `RollingUpdate` overlaps them); deploy (default `migrate`); each workspace
  is swept as it loads; verify every workspace (`spine secrets reencrypt
  [--all-workspaces]` with serve's mode, or the platform-binding log check);
  review the conversion log; then set `strict`.
- **Several replicas, or any overlap of old and new:** a binary older than
  this release reads `enc:v2` as plaintext and signs with the envelope text;
  receivers reject it and the dispatcher does not retry a 4xx other than
  `429`, so those events are lost. Deploy every replica with
  `SPINE_SECRET_ENVELOPE_MODE=downgrade` first, then, once no old replica
  remains, switch to `migrate`.
- **Rollback below this release:** set `downgrade` (from `strict` with
  overlapping processes, via a `migrate` rollout first), verify every workspace
  (`spine secrets reencrypt [--all-workspaces]` with
  `SPINE_SECRET_ENVELOPE_MODE=downgrade`, or the platform-binding log
  check), confirm every workspace is clean, then roll back. A rollback
  that skipped this leaves `enc:v1(enc:v2:…)` rows behind; this release
  repairs those whose inner value is the row's own and refuses the rest
  (`rotate-secret`). A `downgrade` period resets the conversion-log
  baseline.
- No schema change and no migration file.

---

## INIT-023 — Security Audit Remediation (EPIC-002 TASK-007: step-entry policy)

### Security

- **Engine step-entry policy (audit P3-3, P3-4, P3-15, P3-16; F3 amplifier).**
  `AssignStep` bound any actor ID to any step; neither binding path looked at
  `execution.mode`, preconditions or run status; two precondition checks failed
  open; a cancelled run's live steps stayed bindable and submittable; and the
  executor of a run could be bound to its own review. The new opt-in
  `engine.StepEntryPolicy` closes each of these behind its own switch
  (`EnforceRunStatus`, `EnforceEligibility`, `EnforceExecutionMode`,
  `EnforcePreconditions`, `RequireSkillsOnAssign`, `SeparationOfDuties`,
  `TerminateStepsOnCancel`). `spine serve` turns all of them on by default.

### Changed (standalone)

`spine serve` installs `engine.StrictStepEntryPolicy()` unless
`SPINE_STEP_ENTRY_POLICY=legacy`. Externally visible changes under the default:

- **New env var `SPINE_STEP_ENTRY_POLICY`:** unset or `strict` → strict policy;
  `legacy` → pre-INIT-023 behaviour with a startup `WARN`; any other value
  refuses to start. `legacy` is a migration escape hatch and will be removed.
- **Assign and claim refusals** (`POST /runs/{run_id}/steps/{step_id}/assign`,
  claim), nothing written on refusal:
  - `409` run not `active` ("run R is cancelled; a step can only be assigned on
    an active run"); `409` engine-owned step (`type: internal` / `mode:
    spine_only`); `409` actor type outside the mode (`automated_only` →
    `automated_system`, `ai_only` → `ai_agent`, `human_only` → `human`);
  - `404` unknown actor; `403` suspended or deactivated actor;
  - assign only: `403` actor outside the effective `eligible_actor_ids`, `409`
    actor type outside `eligible_actor_types`, `409` "actor missing required
    skills" (`503` if no skill lookup is wired);
  - `403` separation of duties ("separation of duties: actor A executed step S of
    run R and cannot review it"), `503` if the assignment ledger cannot be read;
  - `412` preconditions not met — now evaluated by the engine at assign and
    claim, strictly: `custom`/unknown types, `cross_artifact_valid` without a
    validator and `discussions_resolved` without a checker fail, and
    `field_value`/`field_present` on `status`, `id`, `type`, `title` read the
    typed front-matter fields (so `field_value status` can now pass);
  - a run that is not `active` answers the `409` above even after its live
    executions were closed with it (the run status is checked before the
    execution is looked up, so a cancelled run never answers `404 no active
    execution`).
- **Strict preconditions also govern step activation.** The same strict
  evaluator runs when the engine activates a step, so a step whose
  precondition used to be skipped (a `custom` or unknown type,
  `cross_artifact_valid` with no validator, `discussions_resolved` with no
  checker) now stays `waiting` with a precondition failure and is neither
  dispatched nor auto-assigned — and, since assign and claim evaluate it too,
  cannot be bound until the condition holds. In the other direction a
  `field_value`/`field_present` condition on `status`, `id`, `type` or `title`,
  which could never pass before, now passes when the artifact matches.
- **Separation of duties also governs engine auto-selection.** When the engine
  auto-selects an actor for an `automated_only` or `ai_only` step and the
  selected actor authored the run (for a `review` step) or holds the run's live
  review (for an author step), the step stays `waiting` with an
  info log ("auto-selected actor refused by separation of duties"); no other
  candidate is tried, so a non-author must claim it or an operator assign one.
  A workflow whose only eligible runner or agent also executed an author step
  stalls at that review.
- **Run authors include every step a bound actor may push for** (INIT-023 wave 2
  cross-task review). Separation of duties counts an execution as authorship
  when its step type is anything but `review` or `internal`: besides `manual`
  and `automated`, a `convergence` step (a divergence workflow's
  `evaluation_step`) and a step with no or an unknown `type` now count, because
  run-branch push admission (TASK-016) lets the bound actor of any such step
  push commits. So the actor who evaluated a convergence point can no longer be
  bound to the run's review (`403` separation of duties), and an actor holding
  a live review cannot be bound to a `convergence` step of the same run.
  Embedders that set `SeparationOfDuties` get the same rule.
- **Separation of duties is serialised per run.** Within one `spine serve`
  process, two concurrent binds into the same run (assign, claim or
  auto-selection) run their separation-of-duties check and bind write one after
  the other, so one actor cannot win a review and an author branch step of the
  same divergence run by racing two requests. The lock covers only a re-run of
  that check and the write: workflow loads, actor lookups and precondition git
  reads happen outside it, so a slow read in one run does not hold up binds in
  others. A request whose context ends while it waits answers `503` and binds
  nothing.
- **Run status:** acknowledge, result submission, step activation and retry on a
  run that is not `active` answer `409`; the step-execution candidate listing no
  longer offers executions of non-active runs.
- **Candidate listing and separation of duties:** `GET /execution/steps` with
  `actor_id` no longer offers that actor a `waiting` execution its claim would
  refuse under separation of duties (a review of a run it authored, or an
  author step of a run whose review it holds). A candidate whose check cannot
  run (unreadable assignment ledger) stays listed; its claim answers `503`.
- **Run end:** cancelling, failing (including a permanent merge failure) or
  timing out a run moves its `waiting`/`assigned`/`in_progress`/`blocked`
  executions to `skipped` with `error_detail.classification: run_terminated`
  and cancels their active assignments. A later result submission on such an
  execution answers `409` (it answered `200` with `status: skipped`); a duplicate
  submit of a `completed` execution is still idempotent. Executions of runs that
  ended before the upgrade are not swept; they are refused and hidden.
- **Gateway assign fallback:** with a policy installed the engine-less store
  binder is disabled, so an assign with no orchestrator wired answers `503 step
  assigner not configured` instead of binding unchecked.
- **Reference workflow:** `workflows/epic-lifecycle.yaml` v1.1 drops execute's
  `artifact_status: In Progress` precondition, which could never hold mid-run
  (commit status lands only at merge); the execute step is now bindable.
- Operators with custom workflows that require, on the run's own artifact, a
  status an earlier outcome commits must remove that precondition (see
  `workflow.DeferredStatusPreconditions`) or run with `legacy` while migrating.

### For embedders

- **No behaviour change unless you opt in.** Every new check is guarded by a
  `StepEntryPolicy` bool; an orchestrator without `WithStepEntryPolicy` runs
  exactly the previous logic. One nuance: a policy that sets only `Actors`
  (a resolver) is not "the zero value" — `ClaimStep`'s actor lookup then uses
  the resolver (`IsZero` ignores `Actors`).
- **New (additive):** `engine.StepEntryPolicy`, `engine.EntryActorResolver`,
  `engine.StrictStepEntryPolicy`, `StepEntryPolicy.IsZero`,
  `(*Orchestrator).WithStepEntryPolicy`, `(*Orchestrator).StepEntryPolicy`,
  `(*Orchestrator).TerminateLiveSteps`; `workflow.StepTriggerRunTerminated`
  (`run.terminated`, live → `skipped`); `domain.FailureRunTerminated`
  (`run_terminated`, not retryable); `workflow.DeferredStatusPreconditions` and
  `workflow.DeferredStatusPrecondition` (pinned shape: StepID, Index, Type,
  Status, SetBy); `(*store.PostgresStore).CancelActiveAssignmentsForExecution`;
  `(*store.PostgresStore).ListAssignmentsByRunAndActor`;
  `scheduler.WithRunTerminated`, `scheduler.RunTerminatedFunc`,
  `scheduler.ServiceSet.RunTerminatedFn`. No exported signature changes and no
  interface gains a method: the SoD ledger reads (`ListAssignmentsByRunAndActor`,
  falling back to `ListAssignmentsByActor`) and the assignment cancel are
  optional ports type-asserted on the assignment store; `*store.PostgresStore`
  provides all three.
- **Resolver contract:** the `Actors` resolver's answer must have `ActorID`
  equal to the requested ID, or the bind answers `ErrNotFound`; a
  `*domain.SpineError` keeps its code (`ErrUnavailable` stays 503); an untyped
  error becomes `ErrInternal`. The echo check only stops a resolver from
  answering for a *different* ID. The resolver itself must be canonical: it
  answers `ErrNotFound` for any input that is not byte for byte a stored actor
  handle (no case folding, trimming, brace/`urn:` UUID forms or other lenient
  parsing). A resolver that builds the actor from the requested ID lets a
  variant of a run author's handle bind as a second identity and defeats
  separation of duties. The engine passes the requested ID to the resolver
  unmodified.
- **Separation-of-duties ledger:** the engine prefers a run-scoped ledger read,
  `ListAssignmentsByRunAndActor(ctx, runID, actorID)`, type-asserted on the
  assignment store (`*store.PostgresStore` provides it; one indexed query, no
  schema change), and falls back to `ListAssignmentsByActor` filtered in memory.
- **Bind serialisation is per process.** Under `SeparationOfDuties` the engine
  serialises each run's SoD check and bind write with an in-process lock shared
  by every orchestrator in the process. Binds made by different processes
  sharing one database are not serialised; an embedder that runs several
  replicas with `SeparationOfDuties` on keeps that race (see
  `KNOWN-LIMITATIONS.md`).
- **The bind lock is narrow and context-aware.** It is striped by run ID and
  shared by every orchestrator in the process, so in a multi-tenant embedder
  unrelated runs of different workspaces can share a stripe. It is held only
  while check 7 is re-run (the run's executions and the assignment-ledger rows
  are read again) and the bind is written. Workflow loads, the `Actors`
  resolver, skill lookups and precondition git reads run before it is taken.
  The checks themselves still run in the documented order first. A waiter whose
  context ends gets `ErrUnavailable` wrapping `ctx.Err()`, and nothing is bound.
  Under `SeparationOfDuties` each assign or claim therefore reads the run's
  executions and ledger rows twice.
- **Run gate:** `EnforceRunStatus` refuses every non-`active` status (including
  `pending`, `paused`, `committing`, `partially-merged`), stricter than a gate
  that refuses only terminal runs.
- **`TerminateStepsOnCancel` without `EnforceRunStatus`** logs a `WARN`: the
  termination write is not atomic against a concurrent bind.
- `validation.EvaluatePreconditions` is unchanged.

---

## INIT-023 — Security Audit Remediation (EPIC-003 TASK-011: projection sync integrity)

### Security

- **Workflow parser differential closed (audit P3-13, narrowed).** The projection stored a workflow
  as JSON that every loader decodes with `encoding/json`, which matches keys case-insensitively
  (including Unicode folding: `ſteps` reads as `steps`), while the governed path parses strictly. A
  file carrying a look-alike key was refused by `workflow.Parse`, shown benign by a YAML viewer and
  executed with the hidden steps. A workflow row is now written only when strict `workflow.Parse`
  accepts the file *and* its stored JSON reads back to exactly the parsed definition. The stored
  JSON of every accepted file is byte-identical to before.
- **A dropped `Diff` error no longer skips governance changes (P3-14).** `IncrementalSync` computes
  one diff per sync for artifacts, workflows and branch protection. A diff, head-resolution or store
  failure, a failed workflow-rename delete, and a failed read of a changed file that git itself
  caused (a lock, a git process that was killed, could not start or exited without a message) now
  set `sync_state = error` without advancing `last_synced_commit`, so the next tick retries. Whether
  a read failure is git's or the commit's is decided from the commit's tree, never from git's error
  text, which echoes the committed — attacker-chosen — path.
- **Standalone projection follows `refs/heads/main`, not the checked-out branch (P3-22).** `spine
  serve` (single- and multi-workspace) pins the projection to the governed-merge target, so a run
  branch checked out in the shared working tree is never projected as live state.
- **`artifacts_dir` is applied by incremental sync too (P3-23).** Both entry points now use one
  path scope, so an out-of-tree `.md` file can no longer overwrite a governed artifact's row,
  `<artifacts_dir>/workflows/` edits are projected incrementally, and `templates/` are never
  projected.

### Changed

- **Rejected workflow files.** A workflow that fails strict parsing or the round trip is not
  projected and is reported as a Warn log line `projection: workflow file rejected` (fields
  `workflow_path`, `commit`, `check`, `action`, `kept_source_commit`, `quarantined`, `error`) and a
  new operational event `projection_workflow_rejected`. A rejection never sets `sync_state = error`.
  The file's last verified row is kept for runs already pinned to it, but its `status` column becomes
  `Quarantined`, so **new runs of that workflow fail with "no active workflow" until the file is
  fixed**. A previously valid file that the stricter reading now refuses — for example an unquoted
  `version: 2026-10-06` — stops binding new runs after the upgrade; search the logs for the Warn line
  and quote the value. An unverifiable stored row is removed. `workflow.Validate` stays advisory at
  projection (Info log); `WithStrictWorkflowValidation(true)` makes it a rejection and is off.
  Content the projection row cannot store is a rejection too (check `round_trip`): U+0000 anywhere,
  or a workflow path, `id` or `status` over 1024 bytes (PostgreSQL refuses an index entry over
  about 2.7 KB, which used to hold the sync in `error` with the previous row still bindable). An
  entry at a workflow path that is not a file — a gitlink, a directory — is a rejection with check
  `not_a_file`. The entry is typed from the commit's tree **before** the file is read, so a gitlink
  is refused even when the id it records names a blob in the repository: `git show` prints that
  blob, and before this release the projection accepted it — a body no diff shows (the diff and
  repository viewers show only a submodule pointer). A gitlink whose id names nothing used to hold
  the sync in `error` on every tick.
- **A non-file entry at `.spine/branch-protection.yaml`** (a gitlink) is treated like a malformed
  config (ADR-009 §1): the projected rules stay as they are and the sync is held in `error` until
  the path holds a file again. Before, a gitlink recording the id of a `rules: []` blob projected
  the relaxed rules while the diff showed only a submodule pointer.
- **The first sync after the upgrade reconciles every stored workflow row** against head, once per
  projection instance: rows outside the workflow locations or absent at head are removed, stale rows
  are replaced, unchanged rows keep their `source_commit`. A row whose file was deleted after the
  last synced commit is removed as the pending diff would remove it, with an Info line, not reported
  as a rejection. If the reconciliation fails (a git or store error) the sync is held in `error` and
  retries it on the next tick, but the changes in the window are still applied.
- **Only direct children of `workflows/` (and `<artifacts_dir>/workflows/`) are workflows.** Nested
  files such as runner step profiles under `workflows/steps/` are no longer projected; existing rows
  for them are deleted on the first sync (Info log only when they are not Active).
- **`projection_synced` payload** gains `workflows_rejected` and `read_skipped` (changed artifact
  files git cannot read because the commit's tree holds no file at the path — a gitlink whose
  recorded id is not an object in the repository, a directory — are still skipped, but now logged
  at Warn with the entry type and counted). Changed artifacts are not typed before a read that
  succeeds; see `KNOWN-LIMITATIONS.md`.
- **`sync_state.error_detail`** names the cause of an infrastructure failure (for example
  `resolve sync target: resolve authoritative ref refs/heads/main: …`), sanitised to one line of at
  most 512 bytes, instead of a bare label. A `FullRebuild` that fails now records `error` with the
  cause and keeps the previous commit (before, it returned leaving `rebuilding`, or on an empty
  store no row at all); a failed wipe records no commit, so the next sync rebuilds instead of
  diffing from a commit that no longer describes the store.
- **Workflow API:** create, update and `POST /workflows/{id}/validate` refuse a body the projection
  would refuse, with `validation_failed` (validate reports rule `projection_round_trip`); a workflow
  planning run refuses it with `invalid_params`.
- **Standalone upgrade notes.**
  - A repository with no `refs/heads/main` (a fresh `init-repo` before the `spine/init` branch is
    merged, or one governed on another branch) logs one startup Warn and reports
    `sync_state = error` (with `refs/heads/main` in `error_detail`, on an empty store too) until
    `main` exists; the first sync after that rebuilds. The engine cannot merge there either.
  - A store whose `last_synced_commit` is a run-branch SHA left by the old behaviour heals on the
    next sync while that commit exists. If it was pruned (`git cat-file -e <sha>^{commit}` fails),
    the sync stays in `error`; run `POST /api/v1/system/rebuild`.
  - Deployments with `artifacts_dir` set should run `POST /api/v1/system/rebuild` once to repair
    rows the old incremental handling corrupted.
  - A full rebuild now discovers and plans workflow rows before it wipes, so a git failure in
    discovery no longer leaves an empty projection. A workflow file git fails to read during a
    rebuild keeps its previous row, quarantined, until the next sync's reconciliation reads the file
    (an incremental sync that fails the same way leaves the row as it was and holds the sync).

### For embedders

- **New:** `workflow.ProjectionDefinition`, `workflow.VerifyProjectionDefinition`,
  `workflow.ErrProjectionRoundTrip`, `workflow.ProjectionRoundTripRuleID`. An embedder that mirrors
  `workflow.Service.ValidateBody` (SMP's validate preview) should switch its `Parse` call to
  `ProjectionDefinition` with the same rule id.
- **New options on `projection.Service`:** `WithAuthoritativeBranch(branch) error` (zero value keeps
  `Head()`, which is right for bare repositories), `AuthoritativeBranch()`,
  `ProbeAuthoritativeRef(ctx)`, `WithStrictWorkflowValidation(bool)` (off). `projection.WorkflowDirs`
  and `projection.WorkflowRejection` are exported.
- **New:** `artifact.DiscoverChangesFromDiffs`, `ChangeSet.ReadErrors`, and `Ref` / `Err` fields on
  `artifact.DiscoveryError` (additive). `engine.AuthoritativeBranch`, `domain.WorkflowStatusQuarantined`,
  `domain.EventProjectionWorkflowRejected`, and the optional store capability
  `(*store.PostgresStore).ListWorkflowProjections` (not added to `store.Store`).
- **New optional git capability:** `git.TreeEntryTyper` (`TreeEntryType(ctx, ref, path)`, with
  `git.TreeEntryBlob` / `TreeEntryTree` / `TreeEntryCommit`), implemented by `*git.CLIClient` and not
  added to `git.GitClient`. Its `ref` is a Rev-class argument of the `CLIClient` ref grammar
  (INIT-023 TASK-002): validated before git starts (an empty ref or a rev expression is refused with
  `git.ErrInvalidRef`), a name that is an existing branch is looked up exactly (one `git show-ref`)
  and reaches git as its object id, and it follows `--end-of-options`. The projection asks it what a commit's tree holds at a path before
  every workflow and branch-protection read, and after a failed changed-artifact read. **A
  `GitClient` decorator passed to `projection.NewService` should forward it:** without it the
  projection keeps the historical read — a successful read is trusted, so a gitlink recording the
  id of a blob in the repository is read as that blob — and no read failure can be proved
  deterministic, so a dangling gitlink holds the sync in `error` (fail closed) instead of being
  skipped or rejected.
- **Shared behaviour for an embedder that runs `projection.Service` (SMP).** Without opting in to
  anything:
  - rejected workflow files (including `not_a_file` entries and over-long indexed fields) are
    quarantined or not projected instead of projected; every workflow read and the
    branch-protection read cost one extra `git ls-tree` process (the entry is typed first);
  - a gitlink at `.spine/branch-protection.yaml` holds the sync in `error` with the rules unchanged,
    even when the id it records names a blob;
  - the first sync's reconciliation deletes **every** stored row outside the workflow definition
    locations — every nested `workflows/**` row, not only the empty-status `workflows/steps/*`
    step profiles: an Active nested definition (for example in an imported repository) is removed
    and reported, and in-flight runs pinned to it lose their row;
  - when that reconciliation writes a row (deletes, replaces or quarantines one) on a sync that is
    otherwise up to date, it re-records `sync_state` (`idle` at the head it judged by) instead of
    returning without a write. Its head is resolved before the rows are listed, so another instance
    (an SMP replica, a re-warmed workspace) may have projected a newer head in between; if it did,
    the next tick re-diffs from this head and restores what the reconciliation undid. A row whose
    projecting sync spans the whole reconciliation can still be lost, as with any two instances
    projecting at different heads;
  - incremental sync no longer projects `templates/`;
  - a `Diff` failure, a `Head()` failure (previously the state was left untouched), and a read of a
    changed file that git itself failed (a lock, a killed or unspawnable process) are each an
    `error` tick, so a document index gated on `idle` pauses for that tick; `error_detail` now
    carries the cause, and a failed `FullRebuild` records `error`;
  - the write path the embedder wraps refuses what the projection refuses: `workflow.Service`
    `Create` / `Update` (SMP's `WorkflowWriter`) return `validation_failed` and
    `engine.StartWorkflowPlanningRun` returns `invalid_params` for a round-trip-failing body, U+0000,
    or a path, `id` or `status` over 1024 bytes.

  A canonical workflow whose file is rejected (or is not a file) and has no row stays
  without a row — an embedder that rebuilds whenever a canonical workflow row is missing must exclude
  such paths from that probe, or it rebuilds on every pass (SMP TASK-012).

---

## Spine Core library packaging — dual-target (standalone + embedded)

Disposition: **Option A** — Spine remains available both as a standalone
service and as an embeddable library. Driven by the Spine Management Platform's
move to run Spine Core in-process (SMP INIT-011); the standalone control-plane
HTTP transport SMP previously consumed is retired on the consumer side, while
the standalone service binary, its git smart-HTTP endpoint, and the bootstrap
bearer path are all retained.

### Added

- **`cmd/embed-smoketest`** — a library-consumer smoke test that imports
  `core/...`, builds a complete `Engine` with in-binary stub ports, and exits
  0. Proves Spine Core is linkable and constructible without the `service/`
  tier. Built and run on every PR.
- **`dev/lint-boundaries.sh`** dependency-direction guard, run as the
  `ADR-017 boundary lint` CI job ahead of build/test — fails the PR if `core/`
  reaches into `service/` or `adapters/`. The standalone build doubling as a
  link-time guard makes a boundary violation a build failure, not just a lint.

### Changed

- **README** now documents the two deployment forms and the boundary that
  makes the embedded form possible (see "Consuming Spine Core as a Library"):
  the `core/ → adapters/ → service/` dependency direction, the two CI gates,
  and how an external consumer supplies its own ports.

### Not yet implemented (follow-up)

- `core/engine`'s `GitOperator` signature still references DTO types from
  `adapters/git`, so a library consumer transitively pulls `adapters/git` for
  those type names. Relocating the git operation DTOs into `core/` (e.g.
  `core/git/`) removes the last library-surface leak; `embed-smoketest`
  surfaces it today rather than hiding it.

---

## INIT-018 — Branch Protection (EPIC-002 policy + EPIC-003 API enforcement)

### Added

- **Branch-protection policy evaluated on every Spine API write.** Every
  ref-advancing call in `internal/artifact` (Artifact Service
  `Create`/`Update`) and `internal/engine` (Orchestrator
  `MergeRunBranch`) consults `branchprotect.Policy.Evaluate`. Governed
  merges are allowed unconditionally; direct writes to a branch covered
  by `no-direct-write` are rejected with a `forbidden` error.
- **`/.spine/branch-protection.yaml`** is seeded by `spine init-repo`
  with the documented defaults (`main: [no-delete, no-direct-write]`).
  Repositories without the file still evaluate against the same
  bootstrap defaults — protection is correct pre-seed.
- **`write_context.override: true`** on **artifact endpoints** opts into the
  per-operation branch-protection override (ADR-009 §4). Only actors
  with `operator+` role effectively use it; contributors see a distinct
  `override_not_authorised` reason. Every honored override emits a
  `branch_protection.override` governance event and adds a
  `Branch-Protection-Override: true` commit trailer. Unnecessary overrides
  (flag set on an unmatched branch) are silent. The equivalent Git push
  path override (`git push -o spine.override=true`) is planned for
  EPIC-004 — the Git endpoint is read-only in this release.
- **`branch_protection.override`** governance event delivered through
  the standard subscription / SSE / event_log path.

### Changed

- `architecture/git-integration.md` §6.4 no longer says "manual merges
  are not allowed" — it now points at ADR-009's enforced policy.
- `architecture/security-model.md` §7.3 rewritten. Previously described
  branch protection as a Git-hosting-level concern; now documents
  Spine's own enforcement surface, the two rule kinds, the bootstrap
  defaults, and the override mechanism.

### Not yet implemented (follow-up)

- Workflow endpoints (`workflow.create`, `workflow.update`) reject
  `write_context.override: true` with `400` today: `workflow.Service`
  consults `branchprotect.Policy` (since INIT-023 TASK-014) but emits no
  ADR-009 override event or trailer. Operators writing directly to a
  workflow definition use the ADR-008 operator-bypass path instead (omit
  `write_context` as `operator+` role — adds a `Workflow-Bypass`
  trailer). Unifying the two bypass mechanisms is tracked in the
  TASK-003 scope-decision note.
- Git push path enforcement (EPIC-004) — the pre-receive handler is the
  subject of a follow-up epic.

---

## INIT-018 — Branch Protection (EPIC-004 Git-path enforcement, in progress)

### Added

- **Git push endpoint (`git-receive-pack`) behind a config flag.** The
  Spine git HTTP server now serves `git-receive-pack` when
  `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (accepted: `1`/`true`/`yes`/
  `on`). Default remains off, so existing deployments upgrade with no
  behaviour change. When off, push returns **403** with a message
  naming the flag so operators can find the switch without grepping
  source. Authentication is enforced on push the same way it is on
  clone/fetch (trusted-CIDR bypass or bearer token).

### Added (TASK-002)

- **Pre-receive branch-protection enforcement.** With
  `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, every push is intercepted at
  the HTTP layer before `git-http-backend` advances any ref. The
  command section of the receive-pack request is parsed into
  `(old, new, ref)` triples; each triple is classified (`delete` on
  all-zero `new`, else `direct-write`) and passed through
  `branchprotect.Policy.Evaluate`. Any Deny rejects the entire push
  (pre-receive semantics — all-or-nothing) with a git-shaped
  receive-pack-result body: a `remote: branch-protection: <rule>
  denies <branch>` line on the client and `ng <ref> pre-receive hook
  declined` per ref.
- `spine/*` refs bypass the policy by design (out of scope for
  user-authored rules per ADR-009 §3) so scheduler-managed and run
  branches still flow through.
- Policy evaluation is served from the projected runtime table —
  same decision point as API-path writes, no Git reads in the hot
  path.

### Added (TASK-003)

- **`git push -o spine.override=true` operator override.** The
  Git-path equivalent of `write_context.override` (ADR-009 §4).
  Operators pushing with this option bypass a matching rule for
  that single push; contributors setting the same option are
  rejected with a distinct "override not authorised" reason, not
  silently accepted.
- **`branch_protection.override` governance events on the push
  path.** Every honored override emits exactly one event per
  overridden ref update (not per push — a single push overriding
  two branches emits two events) with the ADR-009 §4 payload,
  including a `pre_receive_ref` block carrying the
  `(old_sha, new_sha, ref)` the client attempted. The Git server
  never rewrites client-produced commits, so this event is the
  sole audit record on the push path.
- Pushes are not rewritten to add any trailer — commits land
  byte-identical on the server to what the client sent.
- Unused overrides (option set on a push that did not need it) do
  not emit events, to avoid log pollution.
- `receive.advertisePushOptions=true` is set on every workspace
  repo alongside `http.receivepack=true`, so `git push -o` is
  actually supported end-to-end.

### Changed

- Gateway `GitPushResolverFunc` replaces the earlier
  `GitPushPolicyFunc` — it now returns both the per-workspace
  policy and event emitter in one pool-held tuple, so the
  pre-receive gate can emit override events to the target
  workspace's stream.

### EPIC-004 release notes (TASK-004)

A consolidated summary of the Git-path story now enabled by
`SPINE_GIT_RECEIVE_PACK_ENABLED=true`:

- `git push` over HTTPS is a **first-class, enforced surface**. The
  prior "push is on the roadmap" prose has been retired from
  `product/features/branch-protection.md`.
- Every push is authenticated with a bearer token — the
  trusted-CIDR bypass used for clone/fetch does **not** apply to
  push, so runner subnets that are token-less for reads still need
  a token for writes.
- Every ref update in the push goes through `branchprotect.Policy`
  (same evaluator as the API path). Denials reject the whole push
  with a git-shaped `remote: branch-protection: <rule> denies
  <branch>` line and per-ref `ng <ref> pre-receive hook declined`.
- Operators bypass a matching rule with `git push -o
  spine.override=true`. Each honored override emits exactly one
  `branch_protection.override` governance event per overridden
  ref update. The commit is **not** rewritten — the event is the
  sole audit record for a Git push override.
- `/architecture/git-integration.md` §6.5 documents the
  enforcement sequence; `/docs/git-push-guide.md` is the
  operator-facing how-to (includes the client-visible error
  catalogue).

### Upgrade notes

Clients sending `write_context` to artifact endpoints may now include an
optional `override: boolean`. Omitting it is equivalent to `false`.
Clients sending the field from a non-operator role will see their
request rejected with `override_not_authorised` — previously any
direct write that should have hit a rule was a silent success (the
policy was not wired).
