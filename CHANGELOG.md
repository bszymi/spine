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
  added to `git.GitClient`. The projection asks it what a commit's tree holds at a path before
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
