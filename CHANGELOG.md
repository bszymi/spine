# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

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
  directories it made as well as the file (they leaked before).

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
    folded and written before: **now 400 `invalid_params`** (SMP's
    `validateRepoRelativePath` already refuses these);
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
  (`Lstat`, `ReadFile`, `ReadFileLimit`, `Create` + `Undo`, `Replace`,
  `WriteFile`, `Remove`, `RemoveEmptyDir`, `WalkRegularFiles`), `PathError`
  (unwraps to `ErrUnsafePath`), `SizeError` (unwraps to `ErrTooLarge`),
  `DefaultMaxReadBytes`. SMP may use it for its own sinks (SMP TASK-004/008).
  An `Undo` holds at most three descriptors until it is resolved, whatever
  the path's depth; `Create`'s `perm` must leave the file readable by the
  process (the `Undo` pins it with a read-only descriptor; `git add` reads
  it anyway).
- No exported signature changes in `artifact`, `workflow`, `git`,
  `engine.GitOperator`; no migration, environment variable or config.
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
