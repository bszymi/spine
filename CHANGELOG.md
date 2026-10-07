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
- **Revision arguments are validated** (`ReadFile`, `ListFiles` and `RefSHA` ref, `LogOpts.Since`;
  `Diff` and `MergeBase` already were): they must pass `git.ValidateRef`, so rev expressions such
  as `HEAD~1`, `<sha>^`, `HEAD^{tree}`, `a..b`, and anything containing `:`, `\` or a space are
  refused with `git.ErrInvalidRef` before git runs. Resolve such an expression to an object id
  first. No Spine or SMP caller passes one; the gateways' `?ref=` already refused them.
- **Reading a branch by name costs one `git show-ref` per call** (a bare existing branch name or
  `refs/heads/<n>`; object ids, `HEAD` and other `refs/*` cost nothing extra). A caller that reads
  many files from one branch should resolve it once with `RefSHA` and read by object id, as
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
  components; refusing object-id-named refs is recommended to it.

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
