# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

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
    once, and a malformed escape in it is a **400 `invalid_params`**.
  - An admin may manage any actor's skills, its own included (2xx, now audited).
  - Renaming a skill whose old or new name an active workflow's `required_skills` references
    answers **409** unless `?force=true` is passed (was 200), with the same body shape as
    deprecation and `hint: "use ?force=true to rename anyway"`.
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
- **Restart-during-rollout residue.** An old-config replica that restarts mid-rollout and then
  loads a workspace first seeded during the overlap revokes the new bearer there. Recover by
  rotating forward to a fresh value. Prefer a recreate strategy for rotations.
- **Unsetting `SMP_ADMIN_TOKEN`** after it was set still leaves the existing bootstrap row valid
  (unchanged); retire it with `DELETE /api/v1/tokens/{token_id}`.
- **Dev mode** (`SPINE_DEV_MODE`, no actor in context) is unchanged: allow-all on these routes.
- **New audit lines** (`[AUDIT]`, `audit_operation=`): `actor_skill_added`,
  `actor_skill_removed`, `actor_skill_change_refused`, `skill_renamed`, `skill_deprecated`,
  `actor_created`, `actor_create_refused`, `bootstrap_admin_token_revoked`. None carries a
  bearer.
- **Wire format:** new 403 detail `reason` values `self_skill_change_forbidden` and
  `role_ceiling_exceeded`; the new `bootstrap-<16 hex>` token id format (legacy 12-hex ids keep
  their value until rotated).

### Upgrade notes

- **Bootstrap bearers from past rotations are revoked on first load.** Earlier versions never
  revoked, so every older bootstrap row of `smp-admin` that still authenticates is revoked by the
  first load of each workspace by the new code. Make sure the platform uses the current
  `SMP_ADMIN_TOKEN`.
- **Review pre-existing actor-skill grants.** Grants made by non-admins before the upgrade stay in
  place, and `auth.actor_skills` records no grantor. Review holders of `review`,
  `workflow_review` and `architecture` (for example `GET /api/v1/actors/{id}/skills` per actor,
  or SQL on `auth.actor_skills`) and remove unexpected grants as an admin.
- **Integrations whose non-admin actors (including AI agents) register or manage skills** must
  move that to an admin token.
- **Rotate `SMP_ADMIN_TOKEN` once after upgrading** to retire the legacy hash-derived token id of
  the live bearer.

### For embedders

- **`auth.BootstrapAdminConfig.SupersededBefore time.Time` (new, optional).** Rows created at or
  after it are never revoked; the zero value revokes every bootstrap row older than the current
  one. `spine serve` passes its process start. Keyed literals compile unchanged.
- **`auth.BootstrapInternalAdmin` behaviour.** It now revokes superseded bootstrap rows of
  `smp-admin` (it calls `AuthStore.ListTokensByActor` and `RevokeToken`, both already part of
  `store.AuthStore`; a store whose `RevokeToken` returns `ErrNotFound` for an already-revoked row
  is handled), never re-activates a suspended or deactivated `smp-admin`, mints random token ids,
  and returns nil (Error log) when the configured bearer's row is revoked. Revocation errors other
  than not-found are returned, wrapped. It still accepts any non-empty bearer: strength policy is
  the embedder's. SMP's provisioning seed is unaffected (no SMP path consults `auth.tokens`).
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
