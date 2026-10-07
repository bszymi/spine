# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

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
  from an outage. Spine's own divergence branch API does not do that yet, so a
  `branch_id` carrying a control byte is refused with HTTP 500 rather than 400
  (INIT-023 TASK-016 owner question 6). This is a second layer behind the
  parser: an embedder that passes a pushed ref to `Evaluate` raw (SMP's gateway
  does) is protected from F1 by this guard alone.
- **New:** `adapters/git/refname.ValidateQualified(ref string) error`, git's
  refname grammar for fully-qualified refs, returning `ErrInvalidParams`.

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
  review (for a `manual`/`automated` step), the step stays `waiting` with an
  info log ("auto-selected actor refused by separation of duties"); no other
  candidate is tried, so a non-author must claim it or an operator assign one.
  A workflow whose only eligible runner or agent also executed an author step
  stalls at that review.
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
  `write_context.override: true` with `400` today — `workflow.Service`
  does not consult `branchprotect.Policy` yet. Operators writing
  directly to a workflow definition use the ADR-008 operator-bypass
  path instead (omit `write_context` as `operator+` role — adds a
  `Workflow-Bypass` trailer). Unifying the two bypass mechanisms is
  tracked in the TASK-003 scope-decision note.
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
