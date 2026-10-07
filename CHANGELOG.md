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
  mode (it never assumes `migrate`); `--all-workspaces` requires
  `WORKSPACE_RESOLVER=db`; `--allow-unreachable-inactive=<ids>` acknowledges
  decommissioned inactive workspaces whose database is gone (listed in the
  summary). `platform-binding` has no registry to enumerate: verify by
  loading every workspace and checking its `subscription secrets swept`
  log.
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
  values; a metadata-only update also no longer reverts a concurrent
  `rotate-secret`.
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
  same workspace id and mode. A bound `UpdateSubscription` first reads the
  stored secret (one extra `SELECT`) and, when it already opens to the
  given secret, leaves the `signing_secret` column untouched and writes the
  other columns only. SMP adopts this in SMP INIT-027 EPIC-004 TASK-006.
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
- **Rollback below this release:** set `downgrade`, verify every workspace
  (`spine secrets reencrypt [--all-workspaces]` with
  `SPINE_SECRET_ENVELOPE_MODE=downgrade`, or the platform-binding log
  check), confirm every workspace is clean, then roll back. A rollback
  that skipped this leaves `enc:v1(enc:v2:…)` rows behind; this release
  repairs those whose inner value is the row's own and refuses the rest
  (`rotate-secret`). A `downgrade` period resets the conversion-log
  baseline.
- No schema change and no migration file.

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
