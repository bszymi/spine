# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

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
  trailing `.`/`.lock`) — a control byte was a `500` before — and, with `403`, a caller who is
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
  not be configured". It still exits 0, as for any push failure.
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
