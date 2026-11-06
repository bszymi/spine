# Changelog — Unreleased

This file tracks user-visible changes that are not yet cut into a named
release. Promote entries to `CHANGELOG.md` at the repo root when a
release is cut — this file is a staging area.

---

## INIT-024 — Security Review Remediation (EPIC-004 TASK-013: dependency refresh — pgx v5.11.0, chi v5.5.0, LocalStack 4.14.0)

Replaces Dependabot #602 (postgres digest) and #604 (the Go module group) with the same versions,
and moves the test stack's LocalStack to 4.14.0. No runtime schema change.

### Security

- **The `sslmode=disable` startup gate reads the setting as pgx does.** `spine serve`, `spine
  migrate` and `spine secrets reencrypt` refuse a database URL that disables TLS unless
  `SPINE_INSECURE_LOCAL=1`. The gate looked for the literal text `sslmode=disable`. pgx v5.11.0
  trims spaces around a URL query key and value and takes the last of a repeated parameter, so
  `?sslmode= disable` and `?sslmode=require&sslmode= disable` now connect without TLS (v5.9.2
  refused the first and kept TLS for the second), and both passed the gate. It now reads every
  `sslmode` setting the way pgx does (spaces, percent-escapes such as `%64isable`, key/value
  spacing, quotes and backslashes; text inside a password or another quoted value is not a
  setting) and refuses any that reads as `disable`. Everything it refused before it still refuses.
  Settings from the environment (`PGSSLMODE`) are still not read.
- **No piece of a workspace password reaches an error.** pgx v5.11.0 ends a URL's user part at the
  first `@` (net/url, and pgx v5.9.2, at the last), so an unencoded `@` in a password splits it and
  the tail becomes the host part, which pgx splits into a host list at `,` and names host by host in
  a connect error. Workspace pool errors and `spine secrets` output now mask the password and every
  piece of it between URI delimiters (`@ , : [ ] / ? & = #`). pgx v5.11.0's parse errors also quote the
  part of a malformed string they stopped at (`missing "=" after "…"`, a component that does not
  decode), which can be a piece of a password; the workspace error keeps pgx's reason with those
  quoted fragments masked.

### Changed (operators): connection strings under pgx v5.11.0

pgx v5.11.0 parses `postgres://` URLs with a parser that matches libpq instead of net/url, and its
key/value parser now matches libpq too. Every connection string Spine ships (compose, test
defaults, docs, provisioned workspace URLs) parses as before. These spellings changed:

- **An unencoded `@` in a URL password** now ends the user part at the first `@`; the connection
  fails. Percent-encode it (`%40`). The compose files put `POSTGRES_PASSWORD` and
  `POSTGRES_REGISTRY_PASSWORD` into a URL unencoded, so those passwords must not contain `@` (nor,
  as before, `/`, `%` or spaces); `.env.example` says so.
- **`+` in a URL query value is literal** (it was a space): `?password=a+b` is `a+b`. Encode a
  space as `%20`.
- **An unencoded `=` in a URL query value is refused** (`?password=abc==` used to work). Encode it
  as `%3D`; base64-generated passwords often end in `=`.
- **A repeated URL query parameter: the last wins** (it was the first). `?sslmode=require&sslmode=disable`
  now connects without TLS; the startup gate refuses it.
- **Key/value strings: a backslash escapes the next character and is dropped**, as in libpq.
  `password=a\b` is `ab`; write `a\\b`. An unquoted value with a space (`application_name=my app`)
  is refused instead of being sent as a runtime parameter.
- **Also refused now:** a URL query parameter without `=` (`?foo`), and a bare IPv6 host (`postgres://::1/db`;
  write `[::1]`). **Now accepted:** an unencoded `#` or `?` in a URL password, an empty URL port
  (`?port=` means 5432), and `ssl=true` (an alias of `sslmode=require`).
- **Multiple hosts:** ports align with hosts by position (`h1,h2:5433` is h1:5432 and h2:5433), and
  an empty element of a URL host list (`h1,,h2`) means the default host, usually the Unix socket
  (it was dropped).
- **Home directory:** pgx finds `~/.pgpass`, `~/.pg_service.conf` and `~/.postgresql/` through
  `$HOME` rather than the OS account, and looks the OS user up only when no user is given.
- **timestamptz in text format** is returned in the client's time zone (`time.Local`), as the
  binary format always was. Spine uses the extended protocol, where these values arrive in binary,
  so nothing Spine reads changes; only a connection string that selects
  `default_query_exec_mode=simple_protocol` or `exec` sees it, and the instant is unchanged.

### Changed (standalone)

- **HTTP `QUERY` requests reach the wildcard routes.** chi v5.5.0 matches the `QUERY` method on
  `Handle`/`HandleFunc` routes, where v5.2.5 answered `405` before any handler ran. `QUERY` on
  `/api/v1/artifacts/*`, `/api/v1/workflows/*` and `/api/v1/tasks/*` now gets the handlers' `404`,
  and on `/git/*` the auth gate and then `403 unsupported git operation`. Method-specific routes
  still answer `405`.

### Changed (embedders)

- **Minimal version selection raises an embedder's floors** when it moves its Spine pin: pgx
  v5.11.0, chi v5.5.0, cobra v1.10.2 (pflag v1.0.9), aws-sdk-go-v2 v1.47.5 (config v1.33.12,
  credentials v1.20.12, secretsmanager v1.50.7), smithy-go v1.28.7, x/sync v0.24.0, x/time v0.16.0.
- **pgx v5.11.0 adds `TypeMap() *pgtype.Map` to the `pgx.Rows` interface.** Every type that
  implements `pgx.Rows` (test fakes included) must add it; returning nil is allowed when the rows
  are not decoded through a type map.
- The connection-string changes above apply to every connection string an embedder hands pgx.
- No exported Spine API changed.

### Tests and CI

- The compose and CI postgres pin moves to `postgres:18-bookworm@sha256:5c4783a8…` (PostgreSQL 18.6,
  index of 2026-10-28).
- The test stack's LocalStack moves from 3.8.1 to `localstack/localstack:4.14.0@sha256:3ebc3759…`,
  the last release of the open-source image. Dependabot ignores LocalStack's major updates: its
  date-numbered tags (2026.x) are the consolidated image, which needs an account token to start.
- The two password-redaction tests now test Spine's withholding against parse errors that carry
  the password whatever pgx masks; pgx v5.11.0 masks the query-parameter passwords their fixtures
  relied on.

## INIT-024 — Security Review Remediation (EPIC-004 TASK-014: Go 1.27)

Replaces Dependabot PR #598. Spine builds with Go 1.27.3, and the workflow projection no longer
depends on which `encoding/json` engine the binary is built with. No runtime schema change; the
stored projection JSON of an accepted workflow is unchanged.

### Changed

- **Go toolchain go1.27.3.** go.mod's `toolchain` line, the Dockerfile builder, the CI git-floor
  container and the Makefile's `DOCKER_GO` move to `golang:1.27.3-bookworm@sha256:b08bc994…`
  (the index of 2026-11-05; `GOLANG_VERSION=1.27.3` on every platform), and every setup-go step
  installs 1.27.3. The `go` line stays `go 1.26.0`, so embedders can still build Spine with Go
  1.26.
- **golangci-lint v2.15.0** (`GOLANGCI_LINT_VERSION` in the Makefile; v2.11.4 cannot load Go 1.27
  code).
- **JSON on the wire (operators).** Where a string holds invalid UTF-8, which only git-derived
  text such as a path can, Go 1.27's encoder writes the U+FFFD replacement character itself where
  Go 1.26 wrote the escape `�`. This applies to live API responses and SSE frames. The
  decoded value is the same, and stored JSON is unchanged (PostgreSQL re-serialises jsonb).
  Webhook bodies are read back from jsonb, so they do not change either. Every other v1
  behaviour Spine relies on was measured unchanged (the task's JSON audit).

### Fixed

- **The projection refuses non-string YAML mapping keys explicitly.** Go 1.27 runs the v1
  `encoding/json` API on the v2 engine (`GOEXPERIMENT=jsonv2` is on by default). Go 1.26's
  `json.Marshal` refused every `map[interface{}]interface{}`, which yaml.v3 builds for a mapping
  whose keys are not all plain strings, and the projection's round-trip check (INIT-023 TASK-011)
  relied on that. The v2 engine marshals integer and tagged-string keys as JSON strings, so under
  Go 1.27 `commit: {1: x}` would have projected as `{"1":"x"}`, and a `!!binary`-tagged key as
  the field name it decodes to. `ProjectionDefinition` now walks the decoded document and refuses
  such a mapping, and any NaN or infinite number, before it encodes. That is exactly what Go 1.26
  refused, now under every Go version and every JSON engine.

### Changed (embedders)

- `workflow.ProjectionDefinition` refuses the same files as under Go 1.26 whatever toolchain the
  embedder uses, still with `ErrProjectionRoundTrip`. The error now names the mapping or number
  ("the mapping at … has a key that is not a plain YAML string", "… is a non-finite number")
  instead of the encoder's message.
- **SMP:** take this commit before (or together with) moving to Go 1.27. With an older Spine pin,
  SMP on Go 1.27 would project workflows that Spine and SMP on Go 1.26 refuse.

### Tests

- New tests pin the refusal for every key shape yaml.v3 produces, at every map-valued position
  strict Parse accepts, and on the walker alone (no JSON encoder involved). A test built only
  with the legacy engine shows that the walker refuses exactly what that engine refuses.
- `TestMetadataParseMatchesTheJSONEngine` (adapters/delivery) checks the two properties of the
  JSON engine that the webhook TLS decision relies on: struct fields match keys exactly when
  `strings.EqualFold` does, and a repeated key keeps the last value.

---

## INIT-024 — Security Review Remediation (EPIC-005 TASK-010: GitHub CI after the push — git identity)

The first GitHub CI run after the INIT-023/INIT-024 push (run 41856447149) failed three tests:
GitHub's runner gives git no identity, while the machine every earlier run used had one. The tests
were right: the paths they exercise fail the same way in a real deployment with no git identity. No
runtime schema change.

### Fixed

- **Commits and merges no longer need a git identity on the host.** Where git has none — Spine's
  standalone image (its `spine` user has no GECOS name, and the image configures no `user.name`),
  or any host with no `user.name`/`user.email` in any git config — every commit and merge the git
  client made in a repository Spine did not provision itself was refused "Committer identity
  unknown": the authoritative repository an operator mounts, a workspace cloned from an existing
  Spine repository (provisioning sets a repo-local identity only on the repositories it
  initialises), and every code-repository clone. Artifact and workflow writes failed `500
  git_error`, and a governed merge failed its run. Now, when git cannot resolve an identity, the
  commit records `Spine <spine@spine.local>` (the quarantine commit's identity since INIT-023
  TASK-012) as committer, and as author only where the caller names none (merge commits, system
  commits). An actor's commit keeps the actor as author.

### Changed (operators)

- An identity git resolves is kept exactly: the repository's own config, the global or system
  config, `GIT_COMMITTER_*`/`GIT_AUTHOR_*`, or git's own guess from the passwd entry and the host
  name where that guess works. Configure one when commits must carry another committer. A partial
  identity (a name without an address) is replaced whole by Spine's.

### Changed (embedders)

- **`git.CLIClient.Commit`, `Merge` (every strategy but `fast-forward`), `MergeBound`,
  `CommitFilesOnto` and `git.StageAndCommit` probe the identity first:** `git var
  GIT_COMMITTER_IDENT`, and `GIT_AUTHOR_IDENT` where the commit has no explicit author, in the
  repository with the command's environment — skipped when that environment names the identity
  completely. When git resolves it, the commit or merge runs exactly as before (same argv, same
  environment, same commit). Only git's own identity refusal brings in the fallback; a probe that
  fails for any other reason adds nothing. Embedders that configure a committer (SMP's embedded
  scopes set one repo-local) make the same commits as before, at the cost of one or two `git var`
  processes per commit or merge.
- New test helper `testutil.NoGitIdentity(t)`: no git config, no identity variables, and
  `user.useConfigOnly=true`, so git refuses to guess an identity on any host.

### Tests and CI

- `make test` and the `ci.yml` `go test ./...` step run with no global or system git config and
  `user.useConfigOnly=true`, so a test that depends on the host's git identity fails locally as it
  fails on GitHub. `TestP36_R12_ReviewFreeze_InjectionNeverReachesMain` now also checks that the
  governed merge happened: on the identity-less runner it failed the run and the test passed
  vacuously.

---

## INIT-024 — Security Review Remediation (EPIC-002 TASK-011: aggregate checkout bound)

Closes the P3 the TASK-010 cross-task review found next to N-1: the checkout budget bounded one
tree at a time, but concurrent branch-scoped writes each put a whole tree on the temporary disk every
workspace shares, including concurrent writes to one run branch that git then refused, and
concurrent writes to different branches each got the full budget. No runtime schema change.

### Security

- **One write scope per branch.** `git.EnterBranch` claims the branch for the repository in-process
  before it looks the branch up, measures it or creates the temporary directory, and holds the claim
  until the scope's Cleanup has removed the worktree. The repository is identified by its git common
  directory's device and inode (found upward from the given directory, as git does), so every
  spelling of its path is one repository. A second concurrent write to the branch is refused at
  once, before any git process runs and before anything is written (it used to write the tree and
  then be refused by git's "already checked out" guard, which could also let several worktrees
  attach to one branch).
- **Bounded, fair in-flight checkout weight.** `spine serve` admits a branch-scoped write only while
  the weights of every open branch-scoped write stay within `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`.
  A tree weighs its file bytes plus 4096 per entry, so trees of many empty files or directories are
  bounded too. Admission is fair across repositories (workspaces in shared mode): each has its own
  queue, served in turn, and holds at most half the ceiling (one tree heavier than half only while
  it has nothing else in flight), so one workspace's waiting writes never hold up another's that
  fits. A write that does not fit waits; one still waiting when its request ends or after the mutate
  deadline (one for all its waits) fails `503` (retryable); a tree heavier than the ceiling on its
  own is refused. The tree written is the tree weighed: the worktree is filled at the weighed commit
  and attached to the branch without a second checkout, a branch that moved while its write waited
  is measured and admitted again, and one that keeps moving, or moves while the worktree is being
  filled, fails `503` without its new tree ever being written. The weight
  is returned only after the worktree is gone, and a branch-scoped write now pushes to origin after
  its scope is released.

### Changed (operators)

- New variable `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`: a positive integer of bytes; anything else
  refuses to start. Unset, it is twice one maximal tree's weight (`SPINE_GIT_TREE_MAX_BYTES` + 4096
  x `SPINE_GIT_TREE_MAX_ENTRIES`), so a maximal tree fits within one workspace's half:
  5,933,367,296 bytes (about 5.5 GiB) with the defaults. A value at or below a maximal tree's weight
  logs a startup warning (a branch-scoped write of a heavier tree is refused, and one of a maximal
  tree leaves no room for other workspaces). The bound is per process; replicas sharing a
  temporary disk each have their own.
- Concurrent writes with a run's write context to the same run branch: one proceeds and the others
  fail `git_error` at once, where before git refused most of them after writing (and sometimes let
  two through, one of which then failed at commit). Retry after the first has finished.
- One workspace can no longer fill the in-flight room: it holds at most half of it, and its waiting
  writes do not delay other workspaces'. A large tree can still wait behind a steady stream of other
  workspaces' smaller writes until its deadline (no reservation).

### Changed (embedders)

- **A concurrent same-branch entry is refused before writing instead of after.** With no limits set
  (the library default) `git.EnterBranch` still runs the same git commands with the same argv; the
  only change is that a second scope for a branch this process already holds is refused before any
  git process, with an untyped error wrapping the new `git.ErrBranchInUse` — not a `*GitError`, like
  git's refusal it replaces, so callers classify it as before (the artifact and workflow services:
  `git_error`). The claim's key is derived in-process (no extra git process). `WriteScope.Cleanup`
  of a branch scope now runs its removal once however often it is called.
- **New limit field, opt-in.** `git.CheckoutLimits.InflightMaxBytes` (0, the default: no aggregate
  bound) weighs every branch scope of the process in one gate — a queue per repository served
  round-robin, at most half the ceiling per repository — from admission to Cleanup. With a ceiling
  and no `Budget`, `EnterBranch` alone measures against the default budget to weigh its tree; the
  process default and other clients are unchanged. New API: `ErrBranchInUse`; `ErrCheckoutsInFlight`
  with the typed cause `*InflightWaitError{Weight, Ceiling, Err}` (matches the context's error)
  inside `*GitError{Kind: ErrKindTransient, Message: CheckoutsInFlightMessage}`, which the artifact
  and workflow services report as `unavailable`; `ErrBranchMoved` (a branch that kept moving while
  its checkout was admitted; it also matches `ErrCheckoutsInFlight`, with `Message:
  BranchMovedMessage`); `TreeLimitInflightBytes` (a tree over the ceiling:
  `ErrTreeTooLarge`, permanent); `InflightEntryWeight`, `InflightWeight(TreeStats)` and
  `TreeBudget.MaxInflightWeight()` (an embedder can weigh its own checkouts the same way). No
  interface gains a method.
- **With a ceiling, a branch scope is attached without a checkout.** `EnterBranch` under
  `InflightMaxBytes` fills the worktree at the weighed commit and attaches it with `git symbolic-ref
  HEAD refs/heads/<n>` instead of `git switch`, so a branch that moved after it was weighed is
  refused (`ErrBranchMoved`) without its new tree being written. The scope is on the branch exactly
  as before (a commit in it moves the branch). git's "already used by worktree" guard is kept by
  a `git worktree list --porcelain -z` check: a branch checked out in another worktree is refused
  with an untyped error matching `ErrBranchInUse` (classified `git_error`, as git's own refusal was;
  the message names no path). Without a ceiling the attach is the `switch` it was, with the same
  argv.
- `artifact.Service` pushes a branch-scoped write to origin after releasing the write scope (the
  branch is read inside it); the push and its error handling are otherwise unchanged, and an
  in-place write pushes inside its scope as before.

---

## INIT-024 — Security Review Remediation (EPIC-002 TASK-005: standalone push-gate races)

Closes review findings N-8, N-9 and N-13 (P3, standalone git endpoint). No runtime schema change;
SMP embeds none of this code (its own gateway's parity is an SMP companion follow-up).

### Security

- **Receive-pack refused on case- or normalization-folding storage (N-8).** Branch protection
  matches ref names byte for byte, so on storage that folds names a push to `refs/heads/MAIN` could
  move `main`. With `SPINE_GIT_RECEIVE_PACK_ENABLED` on, the git endpoint now probes each served
  repository once, on its first push request (three name pairs — ASCII case, Unicode NFC/NFD, the
  Kelvin sign — in a private directory under `refs/heads` that git never lists), and refuses
  receive-pack for one whose storage folds any of them: `403` naming the requirement on the push
  advertisement and the push, before the body is read, and `http.receivepack=false` in that
  repository. Clones and fetches are unaffected and never wait on the probe. A probe that cannot
  answer refuses pushes (`500 push gate unavailable`) for 30 seconds before a push probes again.
- **Push admission re-checked when the push is applied (N-9).** Admission was decided when the
  command section was parsed, so a push whose pack was held back until its run step had been
  submitted, reassigned or turned into a review step still landed. The checkout-budget pre-receive
  hook now also asks the server, after the pack is in and before any ref moves, to admit every
  update again against the store as it is then; the push is refused unless the server admits all of
  them, and refused if the hook cannot reach it (fail closed). The channel is a per-push Unix socket
  in a private directory plus a one-time token, passed in that push's CGI environment only. An update
  the command section did not announce is refused too.
- **Fair git slots and a command-read deadline (N-13).** One process-wide semaphore of 5, taken
  before a push's body was read, let one authenticated member starve git for every tenant. A request
  now takes a slot per principal ((workspace, actor), or (workspace, peer) for a token-less read),
  per workspace and process-wide (defaults 2 / 4 / 8 in shared mode; 8 / 8 / 8 on a
  single-workspace server), and a push takes it only after its command section has arrived —
  within `SPINE_GIT_COMMAND_READ_TIMEOUT` (default `10s`) — and been admitted, so a stalled client
  holds none. The pushes in their command section and admission at once are capped at twice the
  slot caps, per principal and per workspace (`503` beyond).

### Changed (operators)

- New variables: `SPINE_GIT_HTTP_MAX_CONCURRENT` (8), `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_WORKSPACE`
  (4 in shared mode, the process-wide cap on a single-workspace server),
  `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR` (2 in shared mode, the process-wide cap on a
  single-workspace server), each a whole number from 1 to 1024, and `SPINE_GIT_COMMAND_READ_TIMEOUT`
  (`10s`), a positive duration; an invalid value refuses to start. A single-workspace server now has
  8 git slots (it had 5) and its capacity follows `SPINE_GIT_HTTP_MAX_CONCURRENT`; an explicitly
  lower per-workspace cap binds, and a process-wide cap set above it logs a startup warning. In
  shared mode runners that share one token share one actor's 2 slots: raise
  `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR` if they clone in parallel.
- A deployment that serves pushes from case- or normalization-insensitive storage (native
  macOS/Windows, a Docker Desktop bind mount such as the `./:/repo` override example, SMB, a
  case-sensitive APFS volume) — against the documented requirement — has receive-pack refused for
  those repositories after the upgrade.
- A push's override audit event (`branch_protection.override`) is written once the push holds its
  slot, hook and re-check, just before git runs (a push answered `503` writes none, as before).
- The hook now needs the server's temporary directory to be writable for the re-check socket
  (`/tmp` when `TMPDIR` is longer than 64 bytes; a relative `TMPDIR` refuses every push — set an
  absolute one). A reftable repository on folding storage is refused too (fail closed).

---

## INIT-024 — Security Review Remediation (EPIC-002 TASK-004: read-path robustness)

Closes review findings N-6, N-10, N-11 and N-12 (P3), plus two carried items: `Diff` now reports
gitlink changes that a repository's submodule ignore settings hid, and the git HTTP endpoint now
serves only the exact protocol paths of the repository it resolved. No runtime schema change.

### Security

- **Listings bound the paths they keep, not what git prints (N-6).** `ListFiles` and `Diff` parse
  git's NUL-delimited output as it streams. A listing keeps only the paths its caller wants
  (`ListFiles`' pattern, the new `ListFilesFunc` predicate, `ListFilesUnder`'s directory) and drops
  the rest as they arrive; what it keeps is held to a limit in total, and past it the listing fails
  closed with `ErrOutputTooLarge` as before. A listing also stops, failing closed, past a record
  ceiling (4 times the checkout budget's entries; twice that for a diff), so a small tree whose
  shared subtrees expand to millions of paths is a bounded walk. Artifact and workflow discovery,
  the first sync's workflow-row sweep, the branch-protection lookup and `NextID` keep only the
  paths they use (and scope git to a directory where they have one), so a merged commit with a
  huge path listing but few governed files no longer fails every rebuild and every sync until it is
  reverted. A sync window whose own changed paths exceed the diff limit still fails closed; an
  operator rebuild recovers it (KNOWN-LIMITATIONS, "Git Read Bounds").
- **No DWIM fallback for a missing reserved or home branch (N-10).** A bare revision in the
  reserved `spine` namespace (the push-admission rule, case-fold spellings included) or naming
  the tree's home branch, with no `refs/heads/<n>`, is `not_found`. A same-named tag or
  `refs/<n>` used to answer for a deleted run branch in every read of it — `ReadFile`, `RefSHA`,
  `Diff`, `ListFiles`, `TreeEntryType`, `MergeBase`, `Log{Since}`, `MeasureTree` — including the
  evidence querier's reads of terminal runs. Every other bare name resolves as before.
- **History records cannot be forged by a commit message (N-11).** `Log` and
  `HasCommitWithTrailer` delimit records with a per-call random boundary (128 bits) and require
  every record to start with a full object id; anything else fails the call. A message holding the
  old fixed `---END---` separator and a record-shaped tail used to become another
  `GET /api/v1/query/history` entry with an id, author, date and trailers of its choosing.
- **The output-evidence diff loads no blob (N-12).** The changed-path count of a submit — whose
  commit pair the run-branch author chooses — now runs `git diff --name-only -z --no-renames`
  as a read command (`CLIClient.CountChangedPaths`: `GIT_ALLOC_LIMIT`, the pinned delta-base
  cache) instead of the rename-detecting, allocation-unlimited `Diff`.
- **`Diff` reports every gitlink change.** In a non-bare tree a `submodule.<name>.ignore = all` in
  the checked-out `.gitmodules`, or `diff.ignoreSubmodules = all` in any git config, hid a gitlink
  change from `git diff` — and so from the governance guard, the projection and the status
  cascade. `Diff`, `CountChangedPaths` and the shared tree's untracked-obstruction check pass
  `--ignore-submodules=none`.
- **Git HTTP: only the served repository's protocol paths reach `git-http-backend`**
  (standalone). Read and push are now exactly the paths `git-http-backend` serves at the
  repository root; any other git path is refused with `403` before `git-http-backend` runs. A path
  prefix inside the served tree used to reach it as a repository (another git directory inside the
  working tree), within the bound workspace.

### Changed (operators)

- `spine serve`'s listings keep up to `git.DefaultMaxListingBytes` (~17.2 MiB) of paths and its
  diffs up to `git.DefaultMaxDiffBytes` (~33.4 MiB), both raised with the `SPINE_GIT_TREE_*`
  checkout budget: a tree the budget admits always lists in full, and one merge's diff fits. A
  listing stops past 4 times `SPINE_GIT_TREE_MAX_ENTRIES` records (a diff past 8 times).
- The step-output evidence (`branch_change.changed_paths`) counts a rename as two paths (a delete
  and an add); it counted one.
- A request to `/git/<ws>/...` for a path that is not one of the git protocol's own (`/info/refs`,
  `/git-upload-pack`, `/git-receive-pack`, `/HEAD`, `/objects/...` by full id) answers `403`
  "unsupported git operation" where it used to reach `git-http-backend` (`404`/`500`, or a
  sub-directory served as a repository).
- Reading a deleted run branch's name (`?ref=spine/run/...`) is `404` even when a tag of that name
  exists; read the tag as `?ref=refs/tags/spine/run/...`.

### Changed (embedders)

- **Shared at the bump** (initiative §7b Rule 4): listings refuse less — what failed on its raw
  size may now succeed, and a large kept set still fails; no listing or diff has a per-path length
  limit (a path of any length is listed, kept or dropped, and diffed; only the total binds — review
  round 1 dropped a 4 KiB per-path cap that let one long governed path, which an embedder's own
  merges can admit, wedge the projection); a listing fails closed past its record ceiling; a bare
  reserved-namespace (or home-branch) name with no branch is `ErrKindNotFound`; `Diff` reports
  gitlink changes the repository's submodule ignore settings hid; `Log` and `HasCommitWithTrailer`
  fail with a permanent "unparseable log output" error on output that does not split into records
  (none that git itself prints).
- New API (adapters/git): `PathFilterLister` (optional capability: `ListFilesFunc(ctx, ref,
  keep)`, `ListFilesUnder(ctx, ref, dir, keep)`), `CLIClient.ListFilesFunc`,
  `CLIClient.ListFilesUnder`, `ListFilesKeeping(ctx, lister, ref, dir, keep)` (the capability or a
  filtered `ListFiles`), `FileLister`, `CLIClient.CountChangedPaths(ctx, from, to)`,
  `DefaultMaxListingBytes`, `DefaultMaxDiffBytes`, `OutputTooLargeError.Records`. core/engine:
  `ChangedPathCounter` (optional; `*git.CLIClient` implements it). No existing interface gains a
  method.
- The listing and diff limits of a client built with `WithMaxOutputBytes(n)` are `n` (as the
  per-command limit was); without it, `DefaultMaxListingBytes` / `DefaultMaxDiffBytes`; with a
  checkout budget, never below what the budget admits (twice that for a diff). SMP's
  `gitread.NewClient` passes `SMP_GIT_MAX_OUTPUT_BYTES`, so SMP's limits are that value.
- An operator without `ChangedPathCounter` (SMP's `RealGitOperator`) keeps counting the evidence
  through `Diff`, unchanged (owner question 2 default).

---

## INIT-024 — Security Review Remediation (EPIC-002 TASK-003: checkout and merge amplification budget)

Closes review finding N-1 (P2): a push of a few KB could check out or merge into terabytes on the
disk every workspace shares, because git stores a repeated object once and a checkout writes every
occurrence (a 4,470-byte pack made `EnterBranch` write ~200 MB at `a560c210`). No runtime schema
change.

### Security

- **Checkout budget in standalone.** `spine serve` now measures every tree its git client is about
  to write — `EnterBranch` (before the worktree directory exists), `Checkout`, `Merge` and
  `MergeBound` (by the merge's RESULT tree, computed with `git merge-tree --write-tree` on the
  target tip, plus the target when the switch to it is a checkout), `ResetBranch`'s
  `read-tree -m -u` and its `--reset -u` fallback, the shared tree's restore (`reset --hard` of
  HEAD and the switch home), and `Clone` (now `--no-checkout`, measured, then `reset --hard`) —
  and refuses one over 200,000 entries, 2 GiB of blob bytes or 16 MiB of summed path bytes,
  before anything is written or any ref moves. Every budgeted repository carries
  `git.NeutralCheckoutAttributes` last in `info/attributes`, so what a checkout writes is exactly
  the stored bytes the measure sums (no attribute file or host setting can expand it); a merge is
  measured by its result after the switch, under `GIT_ALLOC_LIMIT` = max(`MaxTotalBytes`, the read
  allocation limit). The measure streams `git ls-tree -r -t -l -z`
  (object headers only) and stops at the first crossing. `spine serve` now needs git >= 2.38 and
  logs a Warn at start for a workspace tree already over the budget. New variables:
  `SPINE_GIT_TREE_MAX_ENTRIES`, `SPINE_GIT_TREE_MAX_BYTES`, `SPINE_GIT_TREE_MAX_PATH_BYTES` (a
  positive integer each; there is no off switch, and an invalid value refuses to start).
- **Pre-receive tree limits.** With `SPINE_GIT_RECEIVE_PACK_ENABLED` on, the git HTTP endpoint
  installs a real pre-receive hook in each served repository (`hooks/pre-receive`, running the
  hidden `spine git-hook pre-receive`) and pins `core.hooksPath` for the receive-pack it runs. Every
  pushed tip is measured after git unpacks the push into quarantine and before refs move; a tip over
  the budget, or one it cannot measure, refuses the whole push with a `remote:` line naming the
  limit and its variable.
- **Mutate deadline.** Every local mutating git command `spine serve` runs (checkout, merge,
  commit, the worktree checkout, the merge binding's and the restore's plumbing) has a wall-clock
  deadline, `SPINE_GIT_MUTATE_TIMEOUT` (default `2m`); on expiry git is SIGTERMed and the operation
  fails permanently. Push and a clone's fetch are not deadlined by it.

### Changed (operators)

- A governed merge of an over-budget run branch is a permanent merge failure; a branch-scoped
  write to one fails with `git_error`; a push of one is refused at pre-receive.
- The budget covers code-repository clients too (clone, checkout and merge): a code repository
  larger than the defaults — a monorepo over 200,000 entries or 2 GiB — needs the limits raised.
- A `pre-receive` hook Spine did not write in a served repository is never replaced: pushes are
  refused (`500 push gate unavailable`) until it is moved aside. Hooks under a custom
  `core.hooksPath` do not run for pushes served by Spine (one Warn per repository names them).
  Blob and tree tips (a tag of a key) are admitted unmeasured.
- A code-repository clone the budget refuses is a permanent (413-class) error, remembered per
  remote and budget for 10 minutes instead of being fetched again on every access.
- **Checkouts write stored bytes.** `spine serve` appends `* -text -ident -filter !eol
  !working-tree-encoding !conflict-marker-size` as the last line of each served repository's
  `info/attributes` (operator lines kept): no end-of-line conversion, `ident` expansion, smudge
  filter or re-encoding on checkout — an LFS pointer stays a pointer.
- **Commit-side normalisation is off** in those repositories: `git add` keeps CRLF content, does
  not collapse `$Id: … $`, runs no clean filter (`-filter`: on a host with git-lfs, a file Spine
  writes at an LFS path is stored as its full content, not a pointer) and does not re-encode
  (`!working-tree-encoding`).
- **Conflict markers are always 7 characters** in a conflicted merge, whatever
  `conflict-marker-size` a tree sets.
- **Upgrade: trees checked out with conversions are brought to their stored bytes once.** When
  the line is first added to a repository whose work trees were checked out with conversions (a
  `text`/`eol`/`ident` attribute, or the host's `core.autocrlf`), their files no longer match the
  blobs: merges over them are refused, and a read-modify-write through the tree would commit the
  converted bytes. `CLIClient.EnsureNeutralWorkTree` — at `spine serve` start (the single-mode
  workspace tree), when a shared-mode workspace's services are built, when the pool reuses a
  code-repository clone, and on every push the git endpoint serves — handles it:
  - it takes the calling tree's hold before it adds the line, so a sequence holding the tree never
    runs on under changed attributes;
  - still under the old attributes it runs every check that can fail: an unborn HEAD is identified
    positively (nothing to renormalise), a torn HEAD is refused, and HEAD's tree is measured against
    the budget from the stored objects. Any failure — HEAD over the budget, a torn ref, a cancelled
    request, a workspace build's init deadline — is returned with nothing written: no line, no
    marker, the tree as it was (fail closed: the repository's budgeted checkouts, merges and pushes
    keep refusing until the next call succeeds). A failure after the tree's record but before the
    line is in place (the worktree listing, a cancel while another worktree is marked, the
    `info/attributes` write) leaves a marker without the line, which the next call that adds the
    line records again;
  - it records each work tree's real changes by content (git compares every tracked file, not its
    stat data, with `core.fsmonitor` off: every tracked path that differs from HEAD or the index,
    staged or not) into a marker in that work tree's git directory — the calling tree's and every
    linked worktree's, the latter read without taking their index lock and with their index's time
    put back — and only then writes the line;
  - it compares the calling tree by content again under the line and rewrites from the index every
    file whose content differs, whose index entry matches HEAD and that is not in the record.
    Recorded paths, staged changes and untracked files are left exactly as they are, and one Warn
    gives their count; a recorded path keeps the bytes it was checked out with until it is committed
    or discarded. The mark is then cleared;
  - past the line only an I/O error or a crash can stop it; the error is returned meanwhile
    (matching `git.ErrRenormalisePending`) and the mark kept. The next call did not take that
    record itself, so — like a call on a linked worktree, or on a marker without a readable record
    (written by an earlier build of this change, damaged, or past its bounds: the marker is
    untrusted input) — it checks HEAD again, quarantines the tracked state first (`git stash
    create`, the SHA logged at Warn) and then rewrites every unrecorded unstaged difference. A push
    is not refused for a pending renormalisation (its measure needs only the line); it logs a Warn
    and the next push retries. A linked worktree is renormalised by a call on it.
  A tree whose file already ends in the line, with no mark, is not touched again.
- Not neutralised (operator configuration): a host `merge.<name>.driver` and `pull.twohead`.
- With the deadline on, `EnterBranch` adds its worktree `--no-checkout` and fills it with
  `reset --hard`, so a worktree's `post-checkout` hook no longer runs.

### Changed (embedders)

- **Opt-in; nothing changes until you opt in** (INIT-024 rule 2). A `git.CLIClient` built without
  the new options, in a process that never calls `git.SetDefaultCheckoutLimits`, measures nothing,
  starts no extra git process and keeps every argv and context exactly as before; `git.EnterBranch`
  likewise.
- New API (adapters/git): `TreeBudget{MaxEntries, MaxTotalBytes, MaxPathBytesTotal}` (a field
  <= 0 means its default; `DefaultTreeBudget()`, `Effective()`), `TreeBudget.Measure` /
  `TreeBudget.Check(ctx, *CLIClient, treeish)`, `MeasureTree(ctx, *CLIClient, treeish)`,
  `TreeStats`, `NeutralCheckoutAttributes`, `EnsureNeutralCheckoutAttributes` /
  `HasNeutralCheckoutAttributes(ctx, repoDir)`, `CLIClient.EnsureNeutralWorkTree(ctx)` (the writer
  plus the reset, retried until it succeeds, of a tree checked out with conversions;
  `ErrRenormalisePending` while it has not), `ErrNeutralAttributesMissing` (a budgeted measure
  on a repository without the line), `MergeAllocRefusedMessage`, `ErrTreeTooLarge` with the typed
  cause `*TreeTooLargeError{Limit, Max, Seen}` inside `*GitError{Kind: ErrKindPermanent, Message: TreeTooLargeMessage}` (distinct
  from `ErrBlobTooLarge` and `ErrOutputTooLarge`), `WithTreeBudget`, `WithMutateTimeout`,
  `*MutateTimeoutError` (matches `context.DeadlineExceeded`) inside
  `*GitError{Kind: ErrKindPermanent, Message: MutateTimeoutMessage}`, `CheckoutLimits`,
  `CLIClient.CheckoutLimits()`, `SetDefaultCheckoutLimits` / `DefaultCheckoutLimits`,
  `EnterBranchWithLimits`, `CheckBudgetGitVersion` / `MinBudgetGitVersion` (a budgeted merge needs
  git >= 2.38), `TreeObjectTooLargeMessage`, and the constants `DefaultTreeMaxEntries`,
  `DefaultTreeMaxTotalBytes`, `DefaultTreeMaxPathBytesTotal`, `DefaultMutateTimeout`. No
  interface gains a method.
- An embedder that runs its own checkouts and merges (SMP: `gitread.CheckTreeDiff`,
  `boundRunBranchCheckout`, `RealGitOperator.MergeBound`, the `/git/{workspace}` gateway) can hold
  them to the same budget with `git.TreeBudget{…}.Check(ctx, client, sha)` before the command, or
  turn the client's own budget on with `git.WithTreeBudget`.

---

## INIT-024 — Security Review Remediation (EPIC-003 TASK-008: the multi-workspace scheduler in shared-mode `spine serve`)

Closes the open half of audit P3-27 (re-confirmed PARTIAL by the 2026-10-22 review). A functional
gap more than a security finding (INIT-024 owner question 5). Standalone only: SMP constructs no
`MultiScheduler` and embeds none of `service/cmd/spine`. No runtime schema change.

### Fixed (standalone)

- **The shared resolver modes run a scheduler.** With `WORKSPACE_RESOLVER=db` or
  `platform-binding`, `spine serve` built no process-level store and therefore no scheduler: step
  and run timeouts never fired, orphaned runs were never failed and a committing run whose governed
  merge failed transiently was never retried. It now builds `MultiScheduler` over the workspace pool
  (beside `MultiProjectionSync`), starts it with the server and stops it on shutdown. Each workspace
  is scanned with its own store, event router and engine callbacks — the per-workspace orchestrator's
  `MergeRunBranch` (governance-aware), `AbandonExhaustedMerge` under the standalone retry budget of
  3, `RecoverCompletedStep`/`RetryStep`, `FailRun` and `TerminateLiveSteps` — so its git clients and
  credentials are that workspace's. A pool entry whose id is not the one asked for is refused, and
  every scan releases its pool reference. Single (`file`) mode is unchanged.
- **One tenant cannot stop the others' scans** (adversarial review of the wiring). In the shared
  modes one loop scans every tenant, and a tick waited for every workspace: one scan item that
  never returned — a code repository's push has no deadline, so a remote that accepts the
  connection and never answers holds it — stopped every tenant's timeouts, orphan scans and merge
  retries for good, and a panic in one workspace's scan ended the process. Now a tick stops waiting
  once it has made no progress for its wait (the workspace budget + the timeout interval, 60 s by
  default), the stalled scan keeps running in the background and later ticks skip its workspace
  until it ends; a workspace is never scanned twice at once; every scan is cancelled at a hard
  10-minute deadline, so a stalled one still gives back its pool reference (which an evicted
  workspace's next request waits for); and a panic is recovered, logged at Error with the workspace
  and the stack, and the workspace scanned again on the next tick.

### Changed (standalone)

- New variables, read only in the shared modes but validated in every mode (an invalid value refuses
  to start; unset, empty or blank means the default): `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES`
  (default 4, 1–256) and `SPINE_SCHEDULER_WORKSPACE_BUDGET` (default `30s`, a positive Go duration).
  A startup Info line prints the effective bounds, the tick wait and the scan deadline.
- `SPINE_ORPHAN_THRESHOLD`: a positive value sets the orphan threshold in every mode, as before in
  single mode; unset or `0` keeps the 30-day default. A **negative** value now refuses to start in
  the shared modes (it would have made every active run an orphan, failed on the first orphan tick);
  single mode still ignores it, as before, and now logs a startup Warn.
- Shared modes: building a workspace's services is bounded. The build runs on the pool's lifetime
  context (the services it starts live as long as the set), so a workspace database that accepts
  the connection and never answers held every request, scan and projection sync of that workspace
  indefinitely. Now:
  - the pool waits for a build at most `SPINE_WS_POOL_INIT_TIMEOUT` (new; default `2m`; unset,
    empty or blank means the default, anything but a positive Go duration refuses to start);
    every caller waiting on the build — including one already waiting on an evicted entry — then
    gets an error, and so does every caller until the abandoned build returns (no second build of
    the workspace starts beside it);
  - the build's one-shot calls (the database ping and first connections, the subscription-secret
    sweep, its git commands, the first bootstrap-admin write, the internal subscription upsert) run
    under a deadline of the same length, so a database that never answers fails the build;
  - a build that finishes after the timeout is installed and served, unless the workspace was
    evicted meanwhile or the pool closed; otherwise it is closed and the next caller builds afresh;
  - the build runs in its own goroutine; a panic in it (the builder hook included) is recovered,
    what it had started is closed, and callers get an initialization error (its stack is logged);
  - a request meets each of these failures before its bearer can be validated (that needs the
    built services), so it answers TASK-007's uniform pre-authentication `401` (below), on
    `/api/v1` and `/git` alike; the cause is logged.
- In `platform-binding` mode the scheduler lists workspaces from the provider's cache keys
  (`ListIDs`), reading no secret; each scan's resolve still reads the binding's secrets
  (`KNOWN-LIMITATIONS.md`, item 9).
- On SIGINT/SIGTERM `spine serve` now waits, within the 10 s shutdown deadline, for the shared-mode
  scheduler's running scans before it closes the workspace pool, so a merge is not cut off by its
  store closing under it.
- Remaining limits (single leader across replicas, no startup crash recovery in the shared modes,
  `platform-binding` scheduling only resolved workspaces, per-mode deactivation lag, unbounded
  per-tick listings, the scan deadline's window): `KNOWN-LIMITATIONS.md`, "Shared Working Tree",
  item 9.

### Upgrading (shared-mode operators)

The shared modes had no scheduler, so the first ticks after the upgrade act on everything that has
built up in every active workspace:

- every run past its `timeout_at` is cancelled, and its live steps closed (first timeout tick,
  about 30 s after start);
- every step past its step timeout is failed, or completed with its `timeout_outcome` — a step
  still `waiting` or `assigned` is measured from its creation, not from when someone picked it up;
- every active run without step activity for three orphan thresholds is failed (first orphan tick,
  about 60 s), and committing runs are retried under the budget of 3;
- `SPINE_ORPHAN_THRESHOLD`, which had no effect in these modes, takes effect (a negative value now
  refuses to start).

Review the active runs, their timeouts and the configured orphan threshold before upgrading.

### Changed (embedders)

- `adapters/scheduler.MultiScheduler` (SMP constructs none; `scheduler.Scheduler` is unchanged):
  - A tick no longer waits without bound: `MultiSchedulerConfig.TickWait` (new; 0 means
    `WorkspaceBudget + TimeoutInterval`) is how long a tick waits without progress — no slot coming
    free, none of its scans ending — before it returns; the stalled scan keeps running and later
    ticks skip its workspace until it ends. A tick that keeps making progress is never cut short.
    A scan a tick stopped waiting for is not counted against a later tick's
    `MaxConcurrentWorkspaces`, and the workspaces a stalled tick left undispatched come first in the
    next tick of the same kind, so one that stalls every tick cannot starve those after it.
  - A workspace is scanned by one scan at a time: a tick skips (with a Warn) a workspace whose scan
    of any kind is still running, including a workspace the resolver lists twice.
  - A panic in a workspace's scan is recovered (Error log with `scan`, `workspace_id`, `panic` and
    `stack`); its pool reference is still released.
  - Every workspace scan runs under `MultiSchedulerConfig.ScanDeadline` (new; 0 means
    `DefaultScanDeadline`, 10 minutes): its context is cancelled past it. A scan cut by it records
    nothing whose write uses that context.
  - `OrphanThreshold` 0 **or negative** now means the 30-day default (only 0 did).
  - New methods: `ScanTimeouts`, `ScanOrphans` and `RunCommitRetry` run one tick of each loop
    synchronously (`Start` calls them); `Wait(ctx)` waits for `Start` to return and every scan to
    end; `Config()` reports the effective configuration. `Stop` is idempotent; `Start` must be called
    once.

---

## INIT-024 — Security Review Remediation (EPIC-003 TASK-007: session and auth hygiene)

Closes review findings N-14, N-15, N-16 and N-17 (all P3, standalone `spine serve`). No runtime
schema change. SMP embeds none of the changed server packages; `core/evidence` gains additive
fields only.

### Security

- **Open event streams re-validate their bearer (N-14).** `GET /api/v1/events/stream` checked the
  bearer once, at connect, so a stream kept delivering a workspace's events after its token was
  revoked or expired, its actor suspended, or its `events.read` role lost. Every heartbeat (30 s)
  now re-runs those checks against the stream's own bearer and closes the stream with a
  `: reauthentication required` comment frame when one fails — or when the check cannot complete
  (fail closed). The reconnect gets the normal `401`.
- **No workspace is built for an unauthenticated request (N-15).** In shared mode
  (`WORKSPACE_RESOLVER=db` or `platform-binding`) the workspace middleware built the workspace's
  ServiceSet — database pool, bootstrap admin, secret sweep — before the bearer was looked at, and a
  build failure answered `500` where a missing workspace answered `401`. The ServiceSet is now built
  only for a request with a well-formed bearer (it is needed to validate it), and every refusal
  before the bearer is validated — missing or malformed header, unresolved, inactive or unbuildable
  workspace, missing auth service, unknown, expired or revoked token, suspended actor, token-store
  error — is one `401` with one body, and a request without a well-formed bearer is refused before
  the workspace is resolved (no registry query or platform binding fetch). On `/git` in shared mode
  every refusal of a caller not authenticated for the workspace is the uniform `401` too: a missing
  or malformed header, a push with receive-pack off, and every failure of a well-formed bearer,
  where a build failure or a missing auth service answered `503` and a token-store error `500`.
  Causes are logged at `Warn`/`Error`.
- **A workspace connection string never reaches an error or a log.** pgx's parse error quotes the
  workspace's `database_url` and masks only a userinfo password, so a password given as a query
  parameter appeared in `pool.Get` errors, in the multi-workspace projection sync's `Error` log on
  every poll and in the shared-mode request path. The pool now reports the parse reason without
  the string (`workspace.ErrInvalidDatabaseURL`), and masks the string's credentials in connect and
  ping errors.
- **`SPINE_ENV` is required (N-16).** `spine serve` and `spine secrets reencrypt` refuse to start
  unless `SPINE_ENV` is `production`, `staging` or `development` (case and surrounding whitespace
  ignored). An unset or mistyped value used to run every production gate — secret key required,
  `SPINE_CODE_REPO_BASE` required, weak `SMP_ADMIN_TOKEN` refused, `SPINE_DEV_MODE` refused — on its
  lenient branch.
- **Run-branch evidence is labelled unverified (N-17).** `run.status` showed evidence read from the
  author-writable run branch as fact. Each repository entry now says where it was read from and
  whether that is the authoritative branch, and `spine run inspect` marks the rest `UNVERIFIED`.

### Changed (standalone)

- **Boot:** `SPINE_ENV` must be set. The shipped `docker-compose.yaml` sets
  `SPINE_ENV=development`; set your deployment's value explicitly (owner question 2 default:
  refuse rather than treat unset as production, pending the owner).
- **Wire, shared mode:** pre-authentication `401`s on `/api/v1` are all `authentication required`
  (they used to carry `authorization header required`, `invalid authorization header format`,
  `invalid token`, `token expired`, `token revoked` or `actor suspended`). A caller whose workspace
  cannot be built, or whose token store errors, gets that `401` instead of `500`/`503`. Single
  mode is unchanged.
- **Wire, `run.status`:** each `evidence.repositories[]` entry gains `source_ref` and `verified`
  (always serialized), and `evidence` gains `unverified_repositories` (omitted when empty).
- **SSE:** a stream whose bearer stops validating closes at the next heartbeat; clients reconnect.

### Upgrade note: set `SPINE_ENV` before upgrading

`spine serve` and `spine secrets reencrypt` no longer start without `SPINE_ENV`. Before rolling
this out, add `SPINE_ENV=production` (or `staging` / `development`) to every deployment manifest,
compose file, systemd unit and job that runs either command; a process that used to start with it
unset, or misspelled (`prod`, `Production1`), now exits with `SPINE_ENV is not set` / `… is not a
known runtime environment`. A deployment that ran unset in production was running with the
production gates off: before it sets `SPINE_ENV=production`, it must also meet them — a
`SPINE_SECRET_ENCRYPTION_KEY`, an absolute `SPINE_CODE_REPO_BASE`, an `SMP_ADMIN_TOKEN` of at least
32 characters and 8 distinct characters (or none), and `SPINE_DEV_MODE` off — or it will refuse to
start on the next gate. The shipped `docker-compose.yaml` sets `SPINE_ENV=development`.

### Changed (embedders)

- `evidence.RepositorySummary` gains `SourceRef string` (`source_ref,omitempty`) and `Verified bool`
  (`verified`, always serialized); `evidence.RunSummary` gains `UnverifiedRepositories []string`
  (`unverified_repositories,omitempty`). `Querier.SummarizeForRun` marks a record verified only when
  it was read from its authoritative branch (`main`, or `WithPrimaryBranch`) and that is not the
  run's own branch; `BuildSummary` does the same against `main`, so a direct caller passing `HEAD`
  gets unverified records. Ref selection, `Status` and every other field are unchanged. SMP does not
  import `core/evidence`.
- (Standalone-internal, listed for completeness: `service/internal/workspace.BuildPgxpoolConfig`
  and `NewWorkspaceDBPool` return `ErrInvalidDatabaseURL` with the parse reason instead of pgx's
  `*pgconn.ParseConfigError`; `service/internal/...` is not importable by embedders.)

---

## INIT-024 — Security Review Remediation (EPIC-003 TASK-006: workspace-scoped trusted-CIDR clone)

Closes review finding N-2 (P2, standalone shared mode). No runtime schema change. Standalone
`spine serve` only: SMP runs its own `/git` gateway and embeds none of the changed packages.

### Security

- **Token-less git reads are bound to a workspace (N-2).** The `SPINE_GIT_HTTP_TRUSTED_CIDRS`
  bypass was decided from the peer address alone, so in shared mode (`WORKSPACE_RESOLVER=db` or
  `platform-binding`) a runner host trusted for one tenant — or any job it ran — cloned every
  tenant's repository without a token, and the answer differed for missing and present
  workspaces. The trust is now looked up under the workspace the request resolved to: an address
  reads only the workspaces it is bound to, and any other workspace — or one that does not resolve,
  is inactive or is unavailable — answers the same uniform `401` an untrusted caller gets. Pushes
  are unchanged (they never took the bypass), as are the INIT-023 proxy and forwarding-header rules.
- **Every `401` on `/git` is the uniform one (adversarial review of the above).** A caller that sent
  a junk or malformed bearer got the auth error's own message (`invalid token`, `invalid
  authorization header format`, `token expired`, ...) only for a workspace that exists. Every
  authentication failure now gets the uniform message. A `503`/`500` for a resolved workspace is
  still surfaced in single mode; in shared mode TASK-007 (above) makes it the uniform `401` too.
- **With push off, the flag's `403` is for authenticated callers only (review round 1).** A push
  request (`info/refs?service=git-receive-pack`, `POST git-receive-pack`) with
  `SPINE_GIT_RECEIVE_PACK_ENABLED` off skipped the auth gate whenever the workspace resolved, so it
  answered `403` for an existing workspace and `401` otherwise. It now meets the auth gate: a caller
  authenticated for the workspace gets the `403` naming the flag, and any other caller the uniform
  `401`, whatever the workspace's state. The one known remaining difference is the single-mode
  `503` of a server with no auth service at all (`KNOWN-LIMITATIONS.md`).

### Changed (operators)

- `SPINE_GIT_HTTP_TRUSTED_CIDRS` takes a **per-workspace form**: entries
  `<workspace-id>=<cidr>[,<cidr>...]` separated by `;`, e.g.
  `SPINE_GIT_HTTP_TRUSTED_CIDRS='ws-a=172.28.1.10/32;ws-b=172.28.1.11/32,172.28.1.12/32'` (quoted:
  an unquoted `;` ends a shell value). It is accepted in every mode. CIDRs bound to different
  workspaces that overlap log a startup Warn.
- The **bare list** (`172.28.1.10/32,172.28.1.11/32`) stays valid on a single-workspace server
  (`WORKSPACE_RESOLVER` unset or `file`), where `spine serve` binds it to the one workspace
  (`SPINE_WORKSPACE_ID`, default `default`). **In shared mode it refuses to start** (owner question
  1 default, pending the owner), with an error naming the per-workspace form, before any registry,
  secret or database is opened.
- **A malformed value refuses to start** in every mode: an unparsable CIDR (a bare address without
  a prefix length included), a workspace ID `workspace.ValidateID` refuses, a workspace named twice
  or bound to no CIDR, or a bare entry mixed into the per-workspace form. It used to disable the
  whole `/git` endpoint with a Warn log line.
- Single mode: a trusted runner asking for a workspace that does not resolve now gets the uniform
  `401` instead of `404`/`403`/`503`; a per-workspace entry for a workspace the server does not serve
  logs a startup Warn and has no effect. The `git HTTP endpoint enabled` Info line logs
  `trusted_cidrs_by_workspace` instead of `trusted_cidrs`. A trusted runner refused because its
  own workspace did not resolve is logged at Info.
- **With push off, a push without a valid token now gets `401`, not the `403` naming
  `SPINE_GIT_RECEIVE_PACK_ENABLED`.** To see the flag message, push with a bearer for the
  workspace (`git -c http.extraHeader="Authorization: Bearer <token>" push …`).

### Changed (embedders)

- None. `service/internal/githttp` (`Config.TrustedCIDRs` → `Config.TrustedCIDRsByWorkspace`,
  `Handler.IsTrustedIP` → `Handler.IsTrustedIPForWorkspace`) is internal to the standalone server
  and not importable outside `service/`.

---

## INIT-024 — Security Review Remediation (EPIC-001 TASK-002: workflow definition strictness)

Three P3 findings of the 2026-10-22 review (N-4, N-5, N-7): places where a workflow definition,
or the guard over it, was less strict than the rest of the system. No runtime schema change.

### Security

- **Shipped validate steps fail closed on timeout (N-4).** The `validate` steps of
  `workflows/adr-creation.yaml`, `artifact-creation.yaml` and `document-creation.yaml` set
  `timeout_outcome: valid`, so a validator that was missing, hung or starved for 5 minutes passed
  the run on to review. They now time out as `invalid`, back to `draft` (the files are at version
  `1.1`). **Operators who copied these files into their repositories should change
  `timeout_outcome: valid` to `timeout_outcome: invalid` on the `validate` step there**: Spine
  does not rewrite workspace repositories. A corpus test pins that no shipped automated step's
  timeout outcome routes into a review step.
- **One YAML document per workflow file (N-7).** `workflow.Parse` (through
  `yamlsafe.DecodeIntoStrict`) read the first document of a file and ignored anything after a
  `---` separator. A second document with content is now refused: `invalid YAML: unexpected
  second document (only one document per file)`, or `invalid YAML: trailing content: …` when the
  trailing content does not parse. An empty trailing document (a bare `---`) and an explicit
  document end (`...`) are still accepted. The workflow API refuses such a body at create and
  update; the projection treats the file as refused (INIT-023 TASK-011): its last verified row is
  kept, quarantined, no new run binds the workflow, and the sync advances. Each process's first
  sync re-checks every installed workflow row against the file at head, so a file that already
  carries a second document is quarantined then. Neither Spine's nor SMP's shipped corpus has a
  document separator.
- **The governance guard covers every directory the projection could install workflows from
  (N-5).** The guarded set was fixed when `WithGovernanceGuard` ran: `Paths` (or the defaults)
  plus `WorkflowDirs`. An embedder that set `artifacts_dir` on the projection but passed no
  `WorkflowDirs` left `<artifacts_dir>/workflows/` unguarded while the projection installed its
  definitions. With the guard on, each decision now also guards `<ArtifactsDir()>/workflows/`
  when the orchestrator has an artifacts directory (`WithArtifactsDir`, called before or after
  `WithGovernanceGuard`), and, as a backstop, every changed `*.yaml` / `*.yml` file that sits
  directly in a directory named `workflows` at any depth (case-folded). The backstop guards those
  files only, not their directory: a script or README changed beside them is judged by the
  configured set alone. The post-merge assertion applies the backstop to the paths the merge
  actually changed.

### Changed (standalone)

- With the guard on (the `spine serve` default), a governed merge that changes a YAML file
  directly inside any directory named `workflows` — `.github/workflows/ci.yml` included — now
  waits for an operator approval like a change to `workflows/` (the hold names those files only).
- **Upgrade: runs bound to a workflow file with a second YAML document.** The engine re-reads and
  re-parses a run's workflow at the commit the run is pinned to whenever it acts on the run
  (assign, claim, submit, merge). A run started before this release on a file that carries a
  second document with content therefore fails those actions afterwards. Remove the second
  document on `main`, cancel the run (cancelling does not load the workflow; as always, not from
  `committing`) and start it again. Projection-backed reads are not affected: the scheduler's
  timeout and recovery scans and SMP's projection workflow loader read the stored row, which the
  projection keeps (quarantined) at the commit runs are pinned to.
- `spine workflow list`, `show` and `resolve` accept a workflow file exactly as the projection
  does (`workflow.ProjectionDefinition`: strict keys, one document, the round trip). They decoded
  leniently before, so they could list a file `Active`, or resolve a binding to it, that the
  server had quarantined. `list` and `resolve` now skip such a file with a warning on stderr that
  names it and the reason; `show` returns the error.
- A timed-out `validate` step of the three shipped creation workflows routes to `draft`.

### Changed (embedders)

- **Shared behaviour (no opt-in):** `workflow.Parse` and `yamlsafe.DecodeIntoStrict` refuse a
  second non-empty YAML document, so the projection quarantines such a workflow file and
  `ProjectionDefinition` and the workflow writers refuse it.
- With the guard on, more merges are held: `<ArtifactsDir()>/workflows/**` and the backstop's
  files are added to the configured set at every decision (and to the post-merge assertion). `GovernanceGuard.WorkflowDirs` is still the documented wiring; it is the only
  source that also guards nested files of a projection directory the orchestrator was never told
  about. `GovernanceGuardConfig()` still returns the configuration, not the per-decision set. The
  zero value (guard off) is unchanged.
- New in `core/branchprotect`: `GovernancePaths.WithLiteralEntries(entries ...string)` (adds
  literal, case-folded prefix entries; unlike `NewGovernancePaths` it does not refuse glob or
  quoting characters, which are legal in git paths) and `WorkflowDefinitionPaths(paths)` (the
  backstop: the definition-shaped paths themselves).
  `GovernancePaths.Match` decides exactly as before; it now looks the folded path and each of its
  `/`-ancestors up in an index instead of scanning every entry, so an evaluation stays linear when
  a run's diff adds many derived entries.

---

## INIT-024 — Security Review Remediation (EPIC-004 TASK-009: toolchain, dependency floors, CI pinning)

Closes the Spine half of the 2026-10-22 review's supply-chain findings SC-01, SC-06, SC-08 and
SC-09. Build and CI only: no Spine package changes, no runtime behaviour or schema change.

### Security

- **Go toolchain go1.26.10.** `go.mod` gains `toolchain go1.26.10`, the newest 1.26 patch. At
  go1.26.8 govulncheck reports 11 reachable standard-library advisories, among them GO-2026-6809
  (crypto/x509 did not apply the denylist of untrustworthy intermediate certificates; fixed in
  go1.26.10) and the go1.26.9 net/http, net/textproto, crypto/tls and os fixes; at go1.26.10 it
  reports none.
- **Dependency floors.** pgx v5.8.0 → v5.9.2 (GO-2026-5004, reachable from the delivery
  listener's `QueryRow`, though only the simple protocol, which Spine never selects, is affected;
  GO-2026-4771/4772). x/text v0.29.0 → v0.42.0 (GO-2026-6629, GO-2026-5970). x/text v0.42.0
  raises x/sync v0.17.0 → v0.23.0 and the `go` line `1.26` → `1.26.0`. chi stays at v5.2.5:
  GO-2026-5774/5775/5777 are in `middleware.RealIP`, a package Spine does not import
  (module-level only).
- **The standalone image is digest-pinned.** The Dockerfile builds on
  `golang:1.26.10-bookworm@sha256:632dff73…` and runs on `debian:bookworm-slim@sha256:7ff6ba31…`
  (tag kept beside each digest). The golang image sets `GOTOOLCHAIN=local`, so its own Go is the
  compiler that ships.
- **CI is pinned and read-only.** `ci.yml` has `permissions: contents: read` at the top.
  - Every action is a full commit SHA with a `# vX.Y.Z` comment (`actions/checkout` v4.4.0,
    `actions/setup-go` v5.6.0), and checkouts no longer persist the token in `.git/config`.
  - Every job runs on `ubuntu-24.04` instead of the floating `ubuntu-latest`.
  - Every setup-go step installs `go-version: '1.26.10'` (go.mod's toolchain) instead of reading
    `go-version-file`. setup-go v5 reads only the `go` line, and the toolchain download that
    followed collided with the module-cache restore.
  - The postgres service image is the compose files' digest-pinned ref. The git-floor container
    is the Dockerfile's builder image, so that job tests the shipped git and Go.
- **New CI jobs.**
  - `vuln` runs `govulncheck@v1.9.0 ./...` and fails on any reachable advisory, after checking
    that the Go in use is go.mod's toolchain.
  - `toolchain-parity` runs `dev/pincheck`. It refuses a missing toolchain line, and an unpinned
    image or action anywhere it can find one: Dockerfiles at any depth (including `# syntax=` and
    `RUN --mount=from=`), workflows and composite actions parsed as YAML, compose files, and
    `docker run|pull|create` in `run:` scripts and the Makefile. It also refuses a golang
    reference under any registry path, or a setup-go version, that differs from the toolchain; a
    CI image that differs from the compose stack's image of the same repository; and a
    `*-latest` runner. With `-resolve` it also reads the digest's `GOLANG_VERSION` from the
    registry without pulling, and checks that each action's `# vX.Y.Z` tag is exactly its pinned
    commit (`git ls-remote`). Without it, bumping go.mod alone would keep shipping the old
    compiler with nothing red.
  - `image-build` builds the standalone image from its pinned bases.
- **Dependabot.** `.github/dependabot.yml` covers `gomod`, `github-actions`, `docker` (Dockerfile)
  and `docker-compose`, weekly, with minor and patch updates grouped and a 7-day cooldown. It
  does not move go.mod's `toolchain` line, the setup-go version or the images inside `ci.yml`.
  `toolchain-parity` fails a half-done golang or postgres bump.
- **Compose.** The postgres and localstack images in `docker-compose.yaml` and
  `docker-compose.test.yaml` are digest-pinned with a tag comment.
- **Not covered here (owner actions, INIT-024 §7d).** A `main` ruleset (PR and CI required, no
  force push), approval for all outside contributors' workflow runs, and "allowed actions:
  selected" with SHA pinning required are repository settings this change cannot make. Now that
  every action is SHA-pinned, the last of these can be turned on without breaking CI.

### Changed (embedders)

- **Module floors.** Spine's `go.mod` now requires pgx v5.9.2, x/text v0.42.0 and x/sync v0.23.0,
  and says `go 1.26.0`. Minimal version selection raises an embedder that selects older versions.
  These floors were chosen to equal SMP's own selections at 3addd44b. SMP's module build list
  (`go list -m all`) is identical before and after this change, so SMP builds unchanged with
  `-mod=readonly` and `go mod tidy` changes nothing in SMP. The `toolchain` line is not
  inherited: an embedder's own go.mod decides its compiler.
- New dev-only package `dev/pincheck` (`package main`; standard library plus `gopkg.in/yaml.v3`,
  already a Spine dependency). Nothing imports it, and `go test ./...` now includes its tests
  (fixtures plus a check of this repository's own pins).

---

## INIT-024 — Security Review Remediation (EPIC-001 TASK-001: a planning run's starter is a run author)

Closes review finding N-3 (2026-10-22): separation of duties did not count the actor who started a
planning run, although that actor wrote the run's content. No runtime schema change, no API change.

### Security

- **The starter of a planning run cannot review it under separation of duties.** A planning start
  (`POST /runs` with `mode: planning`, `POST /artifacts/entry`, and the workflow-lifecycle starts
  behind `POST` / `PUT /workflows`) writes the caller's content onto the run branch, but the caller
  was bound to no execution, so separation of duties did not see them: they could let another
  actor (an AI draft step) submit the draft unchanged and then claim, approve and merge their own
  content. Every planning start now records the authenticated starter in the run's `commit_meta`
  (`spine.governance.started_by`, the key the ADR-016 lifecycle exemption already used) before the
  content is written, and separation of duties counts that actor as a run author: assign, claim and
  engine auto-selection of the run's review steps refuse it (403, naming `(run start)`), and the
  candidate listing does not offer it. A failed record aborts the start before any content is
  written. With merge binding off the record also survives commit-carrying outcomes.

### Changed (standalone)

- Under the default (strict) step-entry policy, a planning start with no authenticated actor
  (a dev-mode request without a token) is refused with **401** before any run, branch or git read
  exists.
- **An in-flight planning run's review is refused** after the upgrade: a planning run started
  before it (or under `SPINE_STEP_ENTRY_POLICY=legacy`) has no recorded starter and can be
  reviewed by nobody (403, `(run start: starter not recorded)`). Cancel and restart such runs, or
  keep `SPINE_STEP_ENTRY_POLICY=legacy` until they finish (`KNOWN-LIMITATIONS.md`).
- The reviewer who started a workflow-lifecycle run can no longer claim its review (it was held
  for an operator by the lifecycle exemption before; the exemption's starter check stays).

### Changed (embedders)

- **Inert with the zero value.** Without `StepEntryPolicy.SeparationOfDuties` (and without the
  governance guard's lifecycle exemption) no starter is recorded, no commit_meta write is made at
  start and nothing new is refused; `StartPlanningRun`'s signature is unchanged.
- With `SeparationOfDuties` on, `StartPlanningRun`, `StartWorkflowPlanningRun` and
  `StartWorkflowUpdatePlanningRun` return `ErrUnauthorized` unless the context carries
  `domain.WithActor` with a non-empty `ActorID`, which must be exactly the ID the step-entry
  resolver later binds for that person (SMP: the member's `human-<uuid>` handle in `CreateEntry`).
  With `StepEntryPolicy.Actors` installed the starter is resolved through it first: an id it does
  not know is `ErrUnauthorized`, any other resolver error keeps its code (`ErrUnavailable` stays
  503). Resolve the handle before the call: the engine's refusal comes from inside
  `StartPlanningRun`, after nothing was persisted.
  They record it with one `MergeCommitMeta` (or `GetRun` + `SetCommitMeta`) call before the content
  write; a failed write fails the start, deletes the branch and marks the pending run failed.
- A workflow planning run whose start fails on the branch logs "planning run start failed; could
  not mark the run failed" (was "workflow planning run start failed; …") when it cannot be marked
  failed. A guard-only starter-record failure logs "governance guard: the planning run's starter
  was not recorded; if it is a workflow-lifecycle run, its workflow edit will need an operator
  approval" (was "… the lifecycle run's starter …").
- Merge binding off: a commit-carrying outcome's commit_meta write keeps
  `spine.governance.started_by` (every other reserved key is still dropped). The key is written
  only under separation of duties or the guard, so without either nothing changes.

---

## INIT-023 — Security Audit Remediation: wave 4 follow-up

Fixes found after the wave 4 integration (a TASK-003 residual SMP INIT-027/EPIC-004/TASK-008 measured
at ae513dca, and two guards no test pinned). No runtime schema change.

### Security

- **`CLIClient.ReadFile` reads blobs only.** It read content with `git show <rev>:<path> --`,
  which renders whatever git's revision parser makes of the spec: a gitlink whose commit is in the
  repository came back as that commit with its whole patch (SMP measured 272 MiB in the git child
  for a gitlink to a commit adding 10^6 paths through one shared subtree — every object tiny, so
  neither the size probe, the output cap nor `GIT_ALLOC_LIMIT` stopped the diff queue), a gitlink
  recording the id of a blob in the repository came back as that blob (content no diff shows), a
  directory as its listing, and a path holding `..` or a `^` suffix as a range or a revision with
  its parents — a missing `d..main` read as `git log`, `<gitlink>^!` as a commit, `<gitlink>^@` as
  an empty file, and even an existing file named `d..main` was rendered as the range. ReadFile now
  types the entry first (`git --literal-pathspecs ls-tree -l -z --full-tree`, which reads tree
  objects and the blob header only and takes the type from the entry's mode) and reads the probed
  blob by object id (`git cat-file blob <oid>`, byte-identical to `show` for a blob). The path
  never reaches a revision expression.
- **A file entry that records a tag, a tree or a commit is not read as a file** (adversarial review
  of the follow-up above). A regular-file entry's mode does not make its recorded id a blob's:
  `update-index --cacheinfo`, `mktree` or any tree writer records whatever id it is given, and a
  push with the default receive config checks only that the object exists. `git cat-file blob
  <oid>` dereferences, so an entry recording an annotated tag whose target is a blob read as that
  blob — content no diff shows (git diff shows the tag object's own text): a reviewed workflow
  replaced by a review-less one projected Active, `.spine/branch-protection.yaml` relaxed to
  `rules: []`, and discovery and branch validation saw the hidden front matter. An entry recording
  a tree or a commit made `cat-file` fail with a plain "bad file", which the projection classified
  as infrastructure (the tree, which records only the mode, said "blob"): the sync was held in
  `error` at every tick until someone rewrote the file — a wedge any push could plant, and branch
  validation skipped the file (ReadErrors are ignored, TASK-011 AC 28) so a planning branch could
  carry it onto `main`. (At 07825efb `git show` printed the tag header or the object, a
  deterministic parse rejection.) ReadFile now reads the recorded object's own type (`git cat-file
  -t <oid>`, from its header, never dereferenced) before it sizes or reads anything, and refuses
  every type but blob as the not-a-file refusal.

### Changed (embedders)

- `CLIClient.ReadFile` of a **gitlink or a directory** returns
  `*git.GitError{Kind: ErrKindPermanent, Op: "read_file", Message: git.NotBlobMessage}` wrapping a
  new `*git.NotBlobError{Ref, Path, Type}` (`Type` is `git.TreeEntryCommit` or
  `git.TreeEntryTree`; `Error()` never prints the path), matching the new `git.ErrNotBlob`. It used
  to return the rendered listing or commit (a 0-byte success for some `^` forms), the blob a
  gitlink records, or — for a gitlink whose commit is absent — `fatal: bad object …` with the same
  Kind. Size refusals (`ErrBlobTooLarge`, `BlobTooLargeError`) and misses are unchanged: a miss is
  still `ErrKindNotFound` with git's `fatal: bad revision '<rev>:<path>'` text, now built by the
  client (Err is nil instead of the exit status). The path is the one `git show` looked up: a
  `./` or `../` prefix is relative to the repository root (`./task.md` still reads `task.md`;
  climbing out is still git's Permanent "outside repository" error) and a trailing `/` names only
  a directory (`dir/` is the not-a-file refusal instead of the listing). A path no tree entry can
  have — `""`, a leading `/`, an empty, `.` or `..` component inside it, a NUL — is a miss
  without a lookup, as git's literal lookup was. A revision that names no tree is a miss only when
  `git rev-parse --verify --quiet <rev>^{tree}` proves it by exit status; any other ls-tree
  failure keeps git's classified error. Every ordinary file read costs three git processes (plus
  the branch lookup): the entry probe (`ls-tree -l`), the object-type check (`cat-file -t <oid>`)
  and the read (`cat-file blob <oid>`) — one more than the two (`cat-file -s`, `show`) before.
- `CLIClient.ReadFile` of a **file entry recording an object that is not a blob** (an annotated
  tag, a tree or a commit, at a 100644, 100755 or 120000 entry) is the same refusal, its
  `NotBlobError` carrying the new field `FileEntry: true` and `Type` set to the object's own type
  (`git.ObjectTag` — a new constant, `"tag"` —, `git.TreeEntryTree` or `git.TreeEntryCommit`);
  `Error()` says "the path's file entry records a <type> object, not file content". A tag whose
  target is a blob used to return that blob's bytes; a tree, a commit or a tag of one used to
  return a Permanent `*GitError` with git's `bad file` text. `TreeEntryType` is unchanged: it reads
  only trees, so it still answers `TreeEntryBlob` for such an entry. The projection describes the
  refusal as the tree entry `non_blob_object` (in the `not_a_file` rejection's cause and the
  branch-protection sync error), discovery records it as `RefusedNotAFile`, branch validation
  fails it ("not validated: the path holds no file content (a gitlink, a directory, or an entry
  recording a tag, a tree or a commit)"), and the artifact and workflow reads answer it 404 — the
  callers below need no change of their own.
- Callers treat the refusal like TASK-003's size refusal: discovery records it as
  `artifact.RefusedNotAFile` (`"not_a_file"`, a new `RefusalReason`; `projection_file_skipped`
  with `reason: not_a_file`, sizes null) and removes a governed row a gitlink replaces — a gitlink
  `.md` was a `ChangeSet.ReadErrors` entry (`read_skipped`) when its commit was absent and was read
  as rendered content otherwise; branch validation fails such a file (the message above) instead
  of skipping it; the projection's
  `isContentReadError` includes it, and a not-a-file refusal is a `not_a_file` workflow rejection
  (never `read_bounds`) even through a `GitClient` decorator that does not forward
  `TreeEntryTyper`; `artifact.Service.Read` and `workflow.Service.Read` answer it `ErrNotFound`
  (404, was a 500); `artifact.IsIDCollision` counts it as a collision. Every other caller already
  failed closed on a non-NotFound error.
- **SMP:** INIT-027/EPIC-004/TASK-008's tripwire `TestClient_SpineReadFileRendersTheGitlinksPatch`
  fails on the next `.spine-revision` bump that includes this change — expected: Spine's ReadFile no
  longer runs `show` on a gitlink, so SMP TASK-010 can delegate `gitread.Client.ReadFile` to it
  (`git.NotBlobError` / `git.ErrNotBlob` / `git.NotBlobMessage` mirror SMP's `gitread` names and
  envelope, so `gitread` can alias them). So do the "dangling gitlink" rows of SMP's tests that
  compare `gitread`'s errors with `CLIClient.ReadFile`'s (`TestBounded_ErrorContractEqualsCLIClient`
  on SMP `main`, its `gitread.Client` successor on the wave 4 line): the Kind is still Permanent,
  but the Message is now `NotBlobMessage`, not git's `bad object` text.
- **SMP (not changed here; SMP owns it):** SMP's own `gitread.ReadFile` / `ReadBlob` at SMP `main`
  1ae9b0e3 read with `cat-file blob --end-of-options <rev>:<path>`, which peels a tag the same way,
  and SMP's `gitread.Wrap` puts that read in front of every `CLIClient` it hands to the projection,
  artifact and workflow services — so the tag-to-blob read is SMP's to close by the same
  object-type check, before or at the bump that delegates to this ReadFile. (A file entry recording
  a tree or a commit is already SMP's `gitread.NotBlobError`: `readSizedBlob` types the object with
  `cat-file -t` after the failed read.) SMP tests that pin Spine's ReadFile argv or count its git
  processes see the extra `cat-file -t <oid>`.

---

## INIT-023 — Security Audit Remediation: wave 4 cross-task review

Fixes for the review of the four wave 4 tasks together. No runtime schema change; everything below
is inside an option the embedded zero value leaves off (the status lifecycle, the governance guard,
the retry budget's exhaustion handler).

### Security

- **A merge whose post-merge checks never ran is no longer published as a replay.** With the
  status lifecycle or the governance guard on, a `MergeBound` error after git had moved the local
  `main`, or a process death inside the merge block, left the merge on `main`; the next attempt
  found `main` up to date, skipped V1 and the governance post-merge assertion, and pushed it (a
  review-less workflow carried into `workflows/` by a rename on `main`; a criss-cross resolution
  that reopened a Completed task). Each attempt that can move `main` now records the reserved
  `spine.merge.unverified` key first and clears it in the write that records the accepted merge,
  before the push; a late `MergeBound` error is taken back at once (and when the local `main`
  cannot be checked after it, the run stays `committing`, even on a permanent error); and a run
  that still carries the record has the merge taken back at the start of its next attempt, which
  merges and checks again (KNOWN-LIMITATIONS "Governance-Path Guard" 16).

### Changed (standalone)

- **A failed read no longer decides a governance hold.** The hold, its clearing and the retry
  budget's held test used to fall back to the run's last completed step when the workflow (or the
  step list) could not be read, so one transient `git show` could abandon a held, undecided run or
  leave an approved run's hold in place, retried forever. A failed read is now an error: the hold is
  not recorded this time, the abandonment skips the tick, and an approval clears the hold from every
  step that carries it.
- **The retry budget decides its route before writing**, so a failed workflow read during an
  abandonment no longer fails a publish-workflow run outright.
- **A stale hold or blocked target no longer stops the retry budget**: an attempt the gate allows
  clears a hold left on the run, and a recorded block whose merge is no longer in the local `main`'s
  history is cleared.
- **A decision is a merge attempt in flight for the retry budget**: an abandonment can no longer
  route an approved run back between the approval and its merge (the decision answers `503`,
  nothing recorded, while an abandonment runs).
- **`GET /runs/{run_id}/governance` reports `blocked`** (and `blocked_merge_sha`) while a refused
  merge of the run that could not be taken back is still on the local `main`; a reject is then a
  truthful `409` instead of "the approved merge already reached the local main", and an approve
  whose merge ended blocked reports it in `merge_error`.

### Changed (embedders)

- With `WithStatusLifecycle` or `WithGovernanceGuard` on, `MergeRunBranch` writes one more
  reserved `commit_meta` key per merge attempt (`engine.CommitMetaMergeUnverified`) and refuses an
  attempt as `ErrUnavailable` while an unverified merge of the run is buried in the local `main`'s
  history (until an operator resets it); a local `main` that cannot be read before the merge is a
  transient merge failure (counted). The guard's consumption marker is written in the same
  `commit_meta` write.
- `AbandonExhaustedMerge` returns an error, and writes nothing, when a read it needs fails (the
  hold, a recorded block against the local `main`, the route); it never abandons a run carrying
  `CommitMetaMergeUnverified`.
- `DecideGovernanceMerge` answers `ErrUnavailable` while `AbandonExhaustedMerge` is abandoning the
  run, and `ErrConflict` for a reject while the run's refused merge is still blocked on the local
  `main`. `GovernanceMergeStatus` gains `Blocked` and `BlockedMergeSHA`.

---

## INIT-023 — Security Audit Remediation: wave 4 integration (TASK-012, TASK-008, TASK-017, TASK-006)

The four wave 4 tasks merged onto one branch, plus the coordination their combination needs. No
runtime schema change; every new behaviour below is inside an option the embedded zero value
leaves off (the status lifecycle, the governance guard, the retry budget's exhaustion handler) or
in a step write whose result is the same for every input SMP produces.

### Changed (standalone)

- **One order of checks in a governed merge**: binding → status lifecycle → governance gate (hold)
  → code repositories → the merge block on the shared working tree (re-check, `MergeBound`,
  structural check, merge-result check V1, governance post-merge assertion and consumption marker,
  push). A merge that violates the lifecycle and touches governance paths is refused, never held
  (`architecture/engine-state-machine.md`, "Order of the checks in a governed merge").
- **The governance guard's post-merge assertion and consumption marker run inside the merge
  block, before the push**, and its refusal is taken back before the shared tree is released. The
  marker is the one store write a merge block makes.
- **A failed take-back blocks every governed merge whichever check refused the merge**: with the
  guard on, a refusal of TASK-005's structural check or of V1 whose take-back failed records the
  same governance block (`spine.governance.blocked_target` on the refusing run) as the guard's own
  refusals, on top of the shared tree's pending take-back (KNOWN-LIMITATIONS "Governance-Path
  Guard" 6).
- **The retry budget never abandons a run held for an operator governance decision, or one whose
  refused merge is still blocked on the local `main`.** A transient failure before the gate on a
  held run's tick still counts.
- **A retryable failure judging the bound commit under the status lifecycle is a counted merge
  attempt** (a failed, transient primary outcome), so the retry budget bounds it.

### Changed (embedders)

- `Orchestrator.MergeRunBranch` with `WithStatusLifecycle`: a retryable failure judging the bound
  commit now records a failed primary merge outcome with a transient class (after re-reading the
  run) and still returns the error; it records nothing while the run is no longer `committing`.
- The status lifecycle's merge-result refusal (V1) takes the local target back to exactly the
  merge's `BaseSHA`, never to the merged commit's first parent.
- With `WithGovernanceGuard` on: `Orchestrator.AbandonExhaustedMerge` returns `false` for a run
  whose merge is held for a decision or whose refused merge is still blocked; a structural or V1
  refusal whose take-back failed writes `spine.governance.blocked_target` on the run and blocks
  governed merges like the guard's own; the hold record on a step and its clearing on approval go
  through the optional `engine.StepExecutionTransitioner` when the store has it (a stale copy of
  a step that moved is no longer written back); and with the status lifecycle on too,
  `DecideGovernanceMerge` and `GovernanceMergeStatus` compute the merge's governance facts from
  the lifecycle's merge source, as the gate does — a merge the lifecycle refuses is
  `ErrPrecondition` ("nothing to decide") for a decision and an `EvaluationError` for the status.
- `git.CLIClient.MergeBases` (TASK-006) resolves a `HEAD` argument to the home branch on a
  home-branch tree, as `MergeBase` and every other read method do (TASK-012); a client with no
  home branch is unchanged.
- `docs/integration-guide.md`: the three wave 4 embedding sections are numbered §10 (TASK-008),
  §11 (TASK-017) and §12 (TASK-006).

---

## INIT-023 — Security Audit Remediation (EPIC-002 TASK-006: governance-path guard)

Audit F3 (P2, both surfaces), "run-branch slippage": a run branch could carry governance files
into `main` through a governed merge, and the projection installed them as live policy. ADR-016
supersedes ADR-009's v1 decision not to guard governed merges by path. Owner questions take their
defaults, pending the owner (authorization model (a): operator approval of the bound commit; the
ADR is `Proposed` until the owner signs it off). No migration.

### Security

- **A governed merge that changes governance paths waits for an operator.** With the guard on
  (`spine serve` default), a merge that changes `.spine/`, `.spine.yaml`, `workflows/` or
  `<artifacts_dir>/workflows/` is held: the run stays `committing`, nothing is merged (no code
  repository either), the publish step (or the last completed step of a workflow with no publish
  step) carries `error_detail.classification = governance_approval_required`, and
  `governance.merge_held` is emitted once per bound commit and hold episode. An `operator`/`admin`
  approves or rejects exactly the bound commit (`POST /api/v1/runs/{run_id}/governance-decision`);
  approve merges at once, reject revokes the approval and takes `merge_failed` (or fails a
  no-publish run, branch kept). Decisions are serialised with the run's merge attempts in process:
  a reject during an approved merge in flight waits and is answered `409` if the merge won, and an
  approval is recorded only while the run is still `committing`, so a late approve can never
  outlive a reject. The approval lives in reserved `runs.commit_meta` keys
  (`spine.governance_approval.*`) that workflow outcomes cannot write. Closes the self-reviewed
  `rules: []` / review-less `task-default` PoC.
- **Every merge base, and a post-merge check.** The changed paths are the union of the diffs from
  every merge base of `main` and the merged commit (`git merge-base --all`): with one merge base a
  criss-cross history let a run revert another run's tightened `.spine/branch-protection.yaml` or
  workflow unseen. After the merge, before the push, a merge that changed a governed path the
  evaluation did not cover (rename following under git's default configuration, or non-default
  merge configuration) is refused permanently, taken back to the base and never pushed; the
  refusal tells the author to update the run branch from `main`, after which the change is held
  for an operator decision. A merge that can never be evaluated (no merge base, a diff over the
  git output limit) is refused permanently the same way instead of staying `committing`. If that take-back fails, the run stays `committing` and every
  governed merge is refused as unavailable until the local `main` no longer contains the refused
  merge (each attempt retries the take-back).
- **The projection refuses an unprovenanced protection removal.** A branch-protection config that
  removes a protection kind from the authoritative branch is installed only when a database record
  of an operator act that **changed** the config matches it byte for byte, is reachable from the
  sync head and is newer than the installed rules: an honoured override push
  (`runtime.event_log`), or an approved governed merge's consumption marker
  (`spine.governance_approval.merge_sha`/`base_sha`, written before the push) that accounts for
  every kind being removed: its own diff removed it (its base config applied it), or its base
  config is, byte for byte, the result of an earlier valid record its base descends from, judged
  the same way — so two authorised relaxations landing before one projection tick both count, and
  an unauthorised write between them breaks the chain. Otherwise the rules
  stay, `branch_protection.removal_refused` is emitted and a Warn logged — except while an approved
  merge is in flight (its run still `committing`, its approved commit on the head, no reachable
  marker yet): the sync then holds and retries instead of refusing. Commit trailers are
  never trusted. Inactive while `no-direct-write` is already off on the authoritative branch. A
  provenance read failure, or a retryable git failure while checking a record, holds the sync
  (retried) instead of refusing; with the guard on, a full rebuild whose branch-protection step
  failed that way keeps the previous `last_synced_commit` (the guard's newness anchor) instead of
  the head.

### Changed (standalone)

- **`spine serve` turns both guards on.** `SPINE_GOVERNANCE_GUARD=off` (`false`, `0`, `no`)
  restores v1; any unrecognised value keeps them on with a startup Warn. The lifecycle exemption
  (a workflow-lifecycle planning run may merge its own `workflows/<id>.yaml` — never the governing
  workflow, never a file that declares `applies_to: [Workflow]` now, at a merge base, on `main` or
  in the merged result, and only when the
  reviewer neither started the run — the starter is recorded at start; a run without one gets no
  exemption — nor is a run author: bound to any author step of it or holding an assignment of one)
  and the own-run evidence carve-out
  (`.spine/runs/<run id>/evidence/`) are on.
- **Runs whose agents edit `workflows/**` or `.spine/**` now wait for an operator** (for example
  step profiles or agent definitions). Operators decide through the new routes
  (`POST /runs/{run_id}/governance-decision`, permission `run.governance.decide`; read the state
  with `GET /runs/{run_id}/governance`); statuses 400/403/404/409/412/503 as in `api/spec.yaml`.
- **The commit-retry sweep no longer logs a held merge at Error** each tick (Debug instead).
- **A refused relaxation is not a sync error.** The sync advances (`idle`), so the diff window
  does not grow and nothing downstream stalls; the rules shown by `branch_protection_rules` keep
  their old `source_commit`. Recovery: an operator override push that **edits** the file
  (`docs/git-push-guide.md`). A database rebuilt from git refuses a relaxed config until then.
- **Honoured override pushes are written to `runtime.event_log` synchronously.** With the guards
  on, the git push path records each `branch_protection.override` event in the event log before
  git moves the ref, whatever `SPINE_EVENT_DELIVERY` says (the delivery subscriber's later write
  of the same id is a no-op). The projection guard accepts an override as provenance only through
  that row; the asynchronous router alone could let a sync run before it and refuse the
  authorised relaxation for good. A failed write is logged and the push proceeds.
- New events (operational, in `allEventTypes()`): `governance.merge_held`,
  `governance.merge_decided`, `branch_protection.removal_refused` (`architecture/event-schemas.md`
  §4.16–§4.18). New step failure classification `governance_approval_required`.

### Changed (embedders)

All opt-in; the zero value is today's behaviour, and no interface an embedder implements grew.

- `engine.(*Orchestrator).WithGovernanceGuard(engine.GovernanceGuard{...})` — call after
  `EnableMergeBinding`; it errors (guard left off) without merge binding or without a
  `GitOperator` that implements the new optional `engine.MergeBaseLister`
  (`MergeBases(ctx, a, b) ([]string, error)`; `*git.CLIClient` has it). Fields: `Paths` (replaces
  the defaults), `WorkflowDirs` (pass `projection.WorkflowDirs(artifactsDir)` or the projection
  service's `WorkflowDirs()`), `ExemptWorkflowLifecycleTaskPath`, `ExemptOwnRunEvidence`,
  `MaxReportedPaths`. `GovernanceGuardEnabled()`, `GovernanceGuardConfig()`.
- `MergeRunBranch` returns a `precondition_failed` error wrapping
  `engine.ErrGovernanceApprovalRequired` for a held merge (the run stays `committing`). Retry
  loops should treat it as waiting, not failed, and not count it against a retry budget.
- `DecideGovernanceMerge(ctx, engine.GovernanceDecisionRequest)` and
  `GovernanceMergeStatus(ctx, runID)` are the decision and read APIs (the engine checks the
  operator role itself). With the guard on, `MergeRunBranch` and `DecideGovernanceMerge` take a
  per-run in-process stripe (re-entrant through the context; nothing with the guard off);
  embedders running replicas must serialise the two across processes themselves. Optional store
  capabilities `MergeCommitMeta(ctx, runID, patch)` (atomic `commit_meta || patch`) and
  `MergeCommitMetaIfStatus(ctx, runID, status, patch) (bool, error)` (the same, only in `status`;
  used for the approval) — `*store.PostgresStore` has both; without them the engine falls back to
  read-modify-write and a re-read. A reject after the approved merge reached the local `main` is
  `ErrConflict`; a merge that can never be evaluated is `ErrPrecondition` for a decision and a
  permanent refusal for `MergeRunBranch`.
- `engine.MergeBaseLister` contract: unrelated histories are an empty list with a nil error
  (`(*git.CLIClient).MergeBases` maps `git merge-base`'s exit 1 to that); an error means "retry".
- Reserved key `engine.CommitMetaGovernanceStartedBy` (`spine.governance.started_by`): the
  workflow-lifecycle run's starter, written by `StartWorkflowPlanningRun` /
  `StartWorkflowUpdatePlanningRun` from the context's actor only while the guard and the lifecycle
  exemption are on.
- `projection.(*Service).WithProtectionRemovalGuard(projection.ProtectionRemovalGuard{...})`
  with a `projection.GovernanceProvenanceReader` (`*store.PostgresStore.ListGovernanceAuthorizations`);
  a nil reader refuses every removal. `projection.ErrProtectionRemovalUnauthorized`.
- `branchprotect.Request.Governance` (`*branchprotect.GovernanceCheck`), consulted only for
  `OpGovernedMerge`; `EvaluateGovernance`, `GovernancePaths`, three reason codes
  (`governance_approved`, `governance_approval_required`, `governance_unevaluable`). A nil
  `Governance` keeps the v1 decision. A custom `Policy` that denies governed merges with the hold
  reason code gets a plain refusal, never a hold — only the engine's own decision holds.
- Reserved key constants `domain.CommitMetaGovernanceApproval*`, `domain.FailureGovernanceApprovalRequired`,
  the three event types in `core/domain`.
---

## INIT-023 — Security Audit Remediation (EPIC-002 TASK-017: declared artifact-status lifecycle)

Audit P3-21, Spine half: artifact status transitions were not a state machine, the
status-conflict rules only warned, and the merge-time cascade stamped the outcome's status onto
whatever the branch added. Owner questions answered by their defaults, pending the owner.

### Security

- **A governed merge no longer publishes status edits the run does not govern.** With the declared
  lifecycle on (`spine serve`), the merge judges the bound commit's diff: a run may change only the
  status of artifacts it owns — its TaskPath, plus the artifacts a planning run adds — along a
  declared transition from the merge-base status, and only when the workflow declares the effect.
  Reopening a `Completed` task, completing a sibling task or an initiative, retiring an ADR,
  retyping, deleting, renaming out of scope or breaking the front matter of an artifact the run
  does not own is refused (`status_lifecycle_violation`), and nothing is merged. A run's own change
  that is not a declared transition (for example `Completed → Pending`) is refused too.
- **The cascade stamps only run-owned artifacts.** A standard run's additions keep their authored
  status, which must be an initial status of their type (an execution run can no longer mint
  `Completed` work, or an ADR "Completed"). Every rewrite is re-parsed: an owned artifact whose
  status line the byte-preserving writer cannot rewrite (a quoted key, a continuation line, …) is
  refused instead of landing the author's own value.
- **What the merge result changes is judged against `main` before any push**
  (`architecture/engine-state-machine.md` §5A.3): only the run's own artifacts may change status,
  to the planned status and from the judged one; every other artifact keeps its type, status and
  existence, and an added one carries an initial status. Anything else is refused
  (`merge_result_mismatch`) and taken back off the local `main` — a `*.md merge=union` attribute
  that keeps two status lines, a criss-cross history `git merge` resolves against a base the
  judgement never saw, or a concurrent merge of commits the bound commit builds on (when `main`
  gained such commits between the judgement and the merge, the attempt is retried and judged
  again). A rename `main` made after the fork merges as it did before the lifecycle.
- **`workflow.Service` refuses an impossible `commit.status`** at create and update, and
  `POST /api/v1/workflows/{id}/validate` reports it (`rule_id: commit_status`): every outcome's
  `commit.status` must be a declared status of every `applies_to` type
  (`architecture/workflow-validation.md` §5.5 rule 1). A workflow planning run with such a body is
  refused before its branch exists.
- **A Completed epic with open tasks (SC-002) or initiative with open epics (SC-003) blocks**: an
  owned transition to `Completed` that trips one is refused `status_conflict`. The check is made
  under the name `main` gives the artifact (a rename `main` made after the fork is followed), and a
  status `main` already holds is not re-asked (a resumed run whose merge already landed completes).

### Changed (standalone)

- `spine serve` turns the lifecycle on for the process and every pooled workspace, with no switch
  (owner question 5; it requires the merge binding `serve` already enables). The rollback is the
  previous build.
- **Severity change:** with SC-002 and SC-003 blocking, `POST /api/v1/system/validate` and
  `spine validate` report them as errors (status `failed`) where they reported warnings. No shipped
  workflow declares a `cross_artifact_valid` precondition, so step activation is unaffected.
- **`commit.status` is now applied under `artifacts_dir`** (`spine init-repo` defaults it to
  `spine`): the cascade used to read the TaskPath without the directory, fail silently and merge
  with the status unchanged.
- A refused merge records the primary merge outcome as failed with class `precondition` and a
  detail starting `status_lifecycle_violation:`, classifies the last completed step
  `status_lifecycle_violation`, and routes like every refusal: `task-default` goes back to
  `execute`; workflows without a publish step (`epic-lifecycle`, `artifact-creation`, `adr*`,
  `document-creation`, `task-spike`) fail the run. A transient git error keeps the run in
  `committing`. See KNOWN-LIMITATIONS "Artifact Status Lifecycle".

### Changed (embedders)

All opt-in; the zero value is today's behaviour, byte for byte. No method was added to any
interface an embedder implements, and there is no schema change.

- `domain.StatusLifecycle` / `DefaultStatusLifecycle()` (the declared table; `Types`, `States`,
  `Initial`, `IsState`, `IsInitial`, `Allows`, `Transitions`, `WithoutTransition`) and
  `domain.FailureStatusLifecycle` (`"status_lifecycle_violation"`, not retryable).
- `Orchestrator.WithStatusLifecycle` / `StatusLifecycle()`, and `Orchestrator.WithArtifactsDir` /
  `ArtifactsDir()` (where `run.TaskPath` lives; the root by default, only read by the lifecycle).
  With the lifecycle on and merge binding off, `MergeRunBranch` refuses with `ErrUnavailable`.
  New: `engine.StatusLifecycleError` / `StatusLifecycleViolation`, `ErrStatusLifecycleViolation`,
  the `Lifecycle*` reason constants, and the optional `engine.StatusChangeValidator` interface
  (found by type assertion on the validator; `*validation.Engine` implements it).
- `workflow.Service.WithStatusLifecycle` and `workflow.ValidateCommitStatuses` (never called from
  `workflow.Validate`, the projection or the loaders); `workflow.CommitStatusRuleID`.
- `validation.WithBlockingStatusConflicts`, `DefaultBlockingStatusConflicts` (`SC-002`, `SC-003`),
  `Engine.BlockingStatusConflicts()` and `Engine.ValidateStatusChange`.
- `artifact.LifecycleFields`, `artifact.EqualExceptStatus`, `artifact.PathScope` (the projection's
  artifact-path predicate, moved from `adapters/projection`, which now delegates to it — no
  behaviour change) and `artifact.Service.ArtifactsDir()`.
- Scenario harness: `harness.WithStatusLifecycle()` / `WithDeclaredStatusLifecycle()`; the harness
  default is unchanged.
---

## INIT-023 — Security Audit Remediation (EPIC-002 TASK-008: submit ownership, step-output evidence, compare-and-swap state writes)

Audit P3-17 (Spine half), P3-19 and P3-24. Owner questions take their defaults, pending the owner:
`required_outputs` unchanged is flagged, not refused (1); a cancel that loses the race to a merge
start answers 409 (2); SMP opts into the step compare-and-swap in SMP TASK-011 (3); failing closed
on an empty submitter is a follow-up after SMP TASK-011 (4); run detail carries the evidence
through reserved `commit_meta` keys (5).

### Security

- **A cancelled run is no longer resurrected and merged (P3-24).** Every run-status mutator read
  the run, judged the trigger and wrote unconditionally, so a cancel that committed inside
  `CompleteRun`'s read was overwritten (`cancelled → committing`) and the run merged to `main`.
  Run writes are now compare-and-swap on the status that was read, re-judged against the state
  that won (at most three attempts): a completion after a cancel answers 409 and nothing is merged,
  emitted or cleaned up; a cancel that raced a pause still cancels; a cancel that raced a merge
  start answers 409 instead of cancelling a run whose merge then lands. Covers `CompleteRun`,
  `FailRun`, `CancelRun`, `PauseRun`, `ResumeRun`, run start, the permanent-merge-failure path,
  the scheduler's run-timeout cancel and startup recovery, and the gateway's no-orchestrator
  cancel.
- **Step writes cannot overwrite a writer that won (P3-24).** With the PostgreSQL store (`spine
  serve`) every engine step write is conditional on the (status, actor) it read. A submit that
  races the timeout scanner no longer overwrites the failed step and routes its outcome; two merge
  attempts no longer both advance the publish step; a lost activation delivers no assignment and
  runs no handler; a lost claim removes its own assignment row (it never bound its actor, so
  neither the separation-of-duties author ledger nor the prior-holder re-offer may read it as a
  binding; without the optional `engine.AssignmentRowDeleter` it is cancelled); a lost failure
  creates no retry.
  The timeout scanner, startup recovery and the gateway's fallback assigner are conditional too.
- **A completed step is never routed twice (P3-24, design check).** `step.submit` on `completed`
  is an accepted no-op transition, so a second submit that re-read the row after the first one
  completed it flipped a `rejected` review to `approved` and merged it (two racing submits, a
  submit racing the timeout's outcome, or a recovery re-route racing a live replica). The submit
  path now refuses an execution that is terminal when read, or that moved since the submission was
  validated, and the scheduler's crash recovery re-routes only through
  `Orchestrator.RecoverCompletedStep` (active run, the current step, no later execution).
- **The engine checks who submits (P3-17).** The authenticated actor reaches `IngestResult`, which
  refuses a submission for an execution bound to another actor (`403`) before anything else — also
  on a completed execution, and before the required-outputs check, so a non-owner can no longer
  fail the owner's step by omitting an output. `spine serve` sends it even where the gateway's own
  pre-check is skipped (no store wired). A release by a stale claim of an execution since bound to
  someone else is `403` too.
- **Step outputs are recorded as evidence (P3-19).** `required_outputs` are labels the submitter
  attests (KNOWN-LIMITATIONS "Step Outputs Are Self-Attested Labels"). `step_completed` now records
  whether the run branch changed since the previous recorded head (`branch_change`) and flags a
  step that declared outputs but changed nothing (`required_outputs_unchanged`, plus a Warn log).
  Review stays the control; nothing is refused.

### Changed (standalone)

- **`step_completed` has a payload and a per-execution event id.** The documented base fields
  (`step_id`, `step_name`, `outcome`, `next_step`, `attempt`, `duration_ms`, `has_commit`,
  `branch_id`) plus `execution_id`, `actor_id` (also on the envelope), `required_outputs`,
  `artifacts_produced` (at most 32 entries of 512 bytes; `artifacts_produced_count` exact),
  `branch_change` and `required_outputs_unchanged` (architecture/event-schemas.md §4.2). The id is
  `evt-<trace>-<execution_id>-completed`, so every rework attempt is recorded and delivered (the
  per-step id made the event log and the delivery queue drop attempt 2 onwards). A moved head
  costs one `git diff` of at most 3 s at submit; an unmoved one none. `branch_change` is judged on
  the content: a head moved by commits that change no path (an empty commit, a change and its
  revert) is `unchanged` with `reason: head_moved_tree_unchanged`, and flagged.
  **Upgrade note:** an execution completed by an earlier binary was logged under the per-step id;
  if a crash interrupted its routing and the restart runs this version, crash recovery re-emits it
  under the per-execution id, so that one execution's `step_completed` is logged and delivered
  twice (KNOWN-LIMITATIONS "Engine State Writes …" 7).
- **`GET /runs/{run_id}` adds `step_output_evidence`** (one entry per completed step that recorded
  it), read from the reserved `runs.commit_meta` keys `spine.evidence.<execution_id>` — no git call
  beyond the workflow load, and it survives event-log retention.
- **`POST /runs/{run_id}/cancel` answers 409** when the cancel lost a race (to a merge start, or to
  another writer on the no-orchestrator fallback). `POST /runs/{run_id}/steps/{step_id}/assign` on
  the engine-less fallback answers 409 when the step was bound meanwhile.
- **A lost submit race answers 200 with the winner's persisted state** — what a sequential
  duplicate submit already got — or 409 when the execution is still live but moved (rebound,
  released). The step's own actor acknowledging it concurrently is not a conflict. Likewise
  `POST /steps/{execution_id}/acknowledge` that loses to the same actor's identical transition (a
  concurrent retry, or its own submit auto-acknowledging) answers the idempotent `in_progress`
  result a sequential retry gets, not 409; any other lost race stays 409.
- **A submit that loses to the timeout scanner** leaves the step failed (no `step_timeout` is
  emitted, and `spine_timeouts_detected_total` is not incremented, when the submit won instead).
  A run that moved to `committing` between the run-timeout re-read and its cancel is not cancelled.
- Crash recovery of a completed step that did not advance re-routes the stored outcome and
  re-emits its `step_completed` (same id; `branch_change` `unknown`/`recovered`). When routing had
  already created the successor but not moved the run's cursor, recovery moves the cursor and
  activates that successor instead of creating a second attempt of it.

### Changed (embedders)

- **New, opt-in:** `engine.SubmitRequest.ActorID` and `engine.StepResult.ActorID` (empty keeps
  caller-enforced ownership); the optional `engine.StepExecutionTransitioner` store port, found by
  type assertion (no interface an embedder implements gained a method; without it every step write
  is the blind `UpdateStepExecution`, unchanged); `(*store.PostgresStore).TransitionStepExecution`
  and `TransitionRunStatusAt`; the optional `engine.RunCursorTransitioner` with
  `(*store.PostgresStore).TransitionCurrentStep` (crash recovery's conditional cursor move) and
  `engine.BranchCursorTransitioner` with `(*store.PostgresStore).TransitionBranchCursor` (the same
  for a divergence branch's cursor: cursor only, while the branch is open);
  `engine.AssignmentRowDeleter` with `(*store.PostgresStore).DeleteAssignment` (type-asserted on
  the assignment store: a claim whose conditional step write lost removes the row it created,
  only while the row names that execution and actor);
  `Orchestrator.RecoverCompletedStep` and
  `Orchestrator.StepOutputEvidence`; the types `engine.BranchChange` and
  `engine.StepOutputEvidence` with their `BranchChange*` / `EvidenceReason*` constants (an
  `unchanged` entry whose head moved carries `EvidenceReasonTreeUnchanged`),
  `engine.CommitMetaEvidencePrefix`, `engine.ErrRunStateChanged`, `engine.ErrStepStateChanged`.
- **Shared behaviour at the next bump (not opt-in):**
  - Run-status writes go through `TransitionRunStatus` (every `RunStore` has it). A `CancelRun`
    that loses the race to a merge start returns the state machine's `ErrConflict` (a sequential
    cancel of a `committing` run already did); a `CompleteRun` after a concurrent cancel returns
    `ErrConflict` instead of resurrecting the run; a lost `failRunOnMergeError` returns nil and
    records no `git_conflict` detail (the detail is now written after the applied run write). A
    `TransitionRunStatus` error is returned at once, wrapped (`errors.Is` holds); a `GetRun` error
    stays wrapped as before.
  - `SubmitStepResult` on an execution that is terminal when read returns `ErrConflict` wrapping
    `ErrStepStateChanged` instead of re-routing it, and one whose (status, actor) moved since
    `IngestResult` validated it does the same; `IngestResult` answers both with its unchanged
    idempotent response when the row is terminal, else 409.
  - `step_completed` has a payload and the per-execution id: a subscription that includes it
    receives one delivery per rework attempt. With binding off its `branch_change` is
    `unknown`/`head_sha_unrecorded` and no git call is made; nothing is persisted.
  - With merge binding on, a step capture's single `commit_meta` write also carries
    `spine.evidence.<execution_id>` (inside the reserved `spine.` prefix; `stripReserved` keeps
    workflows from writing it), and evidence costs at most one 3 s diff on the submit path.
  - `AcknowledgeStep` with the step capability: losing its compare-and-swap to the same actor's
    identical transition returns the idempotent `in_progress` result (a store without the
    capability never misses, so nothing changes there).
  - `ReleaseStep` refuses (`ErrForbidden`) an execution bound to an actor other than the releasing
    one. With the step capability it writes the step before cancelling the assignment row, and
    puts the step back (conditionally) when that cancel fails.
  - `core/actor.Gateway.ProcessResult` (no production caller) refuses a failed or skipped
    execution and a foreign actor.
---

## INIT-023 — Security Audit Remediation (EPIC-004 TASK-012: standalone git-state safety — convergence target, serialized working tree, bounded merge retries)

Audit F8 (P2), F9 (P2) and P3-27, all in standalone `spine serve`. Owner questions take their
defaults, pending the owner: 3 automatic merge retries; a poisoned tree is quarantined and the
operation continues; `MultiScheduler` is bounded but not wired; retries counted per run lifetime (no
migration); a stale `.git/index.lock` is reported, never deleted; a taken working-tree sequence is
bounded at 5 minutes (a constant).

### Security

- **Convergence merges into the run branch, never into `main` (F8).** The selected divergence
  branches used to be merged with no target, which the git client read as "into whatever is checked
  out" — `main` in standalone's shared tree — so divergence-branch content reached the governed
  branch while the run was still active and its review step waiting. Convergence now names the run
  branch as the target, refuses a run that is not the divergence's own or has no `spine/` run branch
  before anything changes, and consults branch protection about the branch that moves. Divergence
  branches fork from the run branch (also for exploratory branches). `git.CLIClient.Merge` refuses
  an empty `Target`.
- **The shared working tree is serialized and self-healing (F9).** Every stateful git call holds a
  process-wide lock on the tree, and the engine's multi-step sequences (status cascade, governed
  merge with its push, run start, recovery ledger, convergence, code-repository merge) and in-place
  artifact/workflow writes each hold it once, as git-only blocks: no event is emitted and no step is
  routed while it is held. Concurrent approvals no longer fail each other, a status commit can no
  longer land on `main` outside the governed merge, and a run branch is never cut from another run's
  branch. The tree's home branch (`main`) is restored by whoever takes it: a merge in progress is
  aborted, tracked changes are quarantined into an unreferenced commit (logged with its SHA), `HEAD`
  goes back to `main`; untracked and ignored files are never touched (one in the way fails the
  restore before anything changes) and a stale `.git/index.lock` is reported.
  A tree left dirty or off `main` therefore no longer fails every later governed merge in the
  workspace. A tracked difference no hard reset removes (a `text eol=lf` attribute over a file
  committed with CRLF, case-only path collisions on a case-insensitive filesystem) is left in place
  instead of failing every later restore — content, including a divergence branch's, could
  otherwise wedge the workspace (review round 1). Such a difference is identified without reading
  through it: a symbolic link is never followed and a FIFO never opened (a file/link case collision
  used to make every restore hash the link's target outside the tree, or block on a FIFO for 30 s),
  and a type change is remembered like a modification, so it is quarantined once, not at every
  take (review round 2).
- **A client disconnect can no longer poison the tree (F9 via request cancellation).** Git children
  are stopped with `SIGTERM` (git removes its lock files) instead of `SIGKILL`, and a sequence that
  has taken the tree runs to completion detached from the request (bounded at 5 minutes); only the
  wait for the tree is cancelled, and that is not recorded as a merge failure.
- **The same run entered twice (HTTP submit plus the scheduler's retry)** no longer flips a
  completed run to failed: the merge blocks re-read the run and stand down unless it is still
  `committing`.
- **A refused bound merge is taken back before the tree is released**, so an in-place write's
  auto-push or the next run start never builds on or publishes it. When the take-back itself fails
  (a ref lock), it stays pending: the block's exit restore and every later take of the tree retry
  it first and refuse the tree while `main` still holds the refused merge. A take-back is remembered
  from its first fallible step (a failure in its commit checks, or a sequence whose deadline was
  spent, used to be forgotten and the tree released with the refused merge on `main`), runs on its
  own 30 s bound detached from the caller, and on the shared tree a refused merge whose content no
  checkout makes clean is quarantined and reset instead of keeping the whole tree refused (review
  round 2).
- **Task acceptance and the follow-up link read the task under the tree's lock** and commit only
  over the bytes they read: an accept issued while a sequence had a run branch checked out used to
  commit that branch's unreviewed file to `main`; a change that lands between the read and the
  write is now a conflict (409).
- **`HEAD` reads mean `main` on the shared tree:** a run started while another run's cascade had its
  branch checked out bound the workflow an unreviewed branch's task type chose.
- **Governed-merge retries are bounded (P3-27).** A merge that keeps failing transiently is
  retried at most 3 times by the scheduler (the value `spine serve` already configured, now
  enforced), then routed like a permanent failure: a publish-step workflow loops back through
  `merge_failed`, a legacy terminal-commit workflow fails the run (branch kept); an exhausted code
  repository is a terminal failure of that repository instead (the primary merges and the run waits
  in `partially-merged` for operator recovery, codex round 8). The step carries the
  new classification `merge_retries_exhausted`. A transient failure verifying the bound commit
  (merge binding) is now a recorded, counted attempt too (recorded only while the run is still
  `committing`). A push that fails after the merge landed on the local `main` is not abandoned —
  the run is not reported failed while its merge sits on the branch for the next push to publish;
  it stays `committing` and the push is retried until it lands, also when a later attempt fails
  before reaching its push, and a run whose merge was recorded but whose completion failed is
  never abandoned either (review round 1, codex rounds 6–7). That exemption holds within one
  committing episode: after a permanent push rejection sends the run back to `execute`, the
  reworked run's next episode is bounded like any other (review round 2).

### Changed

- One governed merge, run start, convergence or in-place write at a time per workspace tree; an
  HTTP submit that triggers a merge may wait for another merge's push.
- New log and audit records: `shared working tree restored` and `shared working tree: tracked
  changes quarantined` (Warn), `working_tree_restored` and `working_tree_restore_failed` (audit,
  with `quarantine_sha`), `merge_retries_exhausted` (audit) with an Error log,
  `run is no longer committing; this merge attempt stands down` (Info), the scheduler's
  `merge retries exhausted; …` and `workspace scan budget spent; …` (Warn) records.
- New values: step `error_detail.classification` `merge_retries_exhausted`; the
  `retries exhausted (attempts=N, max_retries=M): ` prefix in
  `repository_merge_outcomes.failure_detail`. No migration, table, column or event type.
- Divergence branches fork from the run branch instead of `HEAD`.

### Changed (embedders)

- **API changes (Spine library):** `divergence.Service.EvaluateAndCommit`, `CommitConvergence`
  and `CreateExploratoryBranch`, `engine.ConvergenceHandler.EvaluateAndCommit`,
  `engine.DivergenceHandler.CreateExploratoryBranch` and the gateway's `BranchCreator` take the
  run (`*domain.Run`). `git.CLIClient.Merge` refuses an empty `Target` (`"merge target
  required"`), and an abort (`Strategy: "abort"` or `Source: "--abort"`) that names a `Target`.
  SMP passes `*divergence.Service` to `WithDivergence`/`WithConvergence`, implements none of these
  interfaces and never calls `CLIClient.Merge`, so it compiles unchanged; its divergence adapter
  receives an explicit `Target` (the run branch it resolved itself before) and `run.BranchName` as
  the divergence base (passed through unchanged).
- **Additive:** `git.WorkingTreeGuard`, `(*CLIClient).WithWorkingTree`, `git.WithHomeBranch`,
  `(*CLIClient).RestoreWorkingTree`, `(*CLIClient).HomeBranch`, `git.TreeRestoreReport`,
  `git.TreeRestoreError`, `git.TreeWaitError`, `(*git.WriteScope).Context`,
  `engine.Orchestrator.AbandonExhaustedMerge`, `engine.MergeRetriesExhaustedError`,
  `domain.RepositoryMergeOutcome.RetriesExhausted`, `domain.RepositoryMergeOutcome.MergedLocally`,
  `domain.PushFailureDetail`, `domain.PendingPublicationDetail`,
  `domain.RepositoryMergeOutcome.LocalMergeSHA` / `.KeepPendingPublication` /
  `.KeepsPendingPublicationOf`, `worktreefs.Tree.LstatEntry`,
  `domain.FirstExhaustedOutcome`, `git.TreeRestoreReport.TookBack` /
  `.Phantoms`,
  `domain.FailureMergeRetriesExhausted`, `scheduler.WithCommitExhausted`,
  `scheduler.CommitExhaustedFunc`, `(*scheduler.Scheduler).CommitRetryBudget`, the
  `MultiSchedulerConfig` fields `CommitMaxRetries` / `WorkspaceBudget` /
  `MaxConcurrentWorkspaces` and `ServiceSet.CommitExhaustedFn`.
- **Behaviour every embedder sees (Rule 4):** every git child the git client starts is stopped
  with `SIGTERM` when its context ends (`Kill` 10 s later, or at once where the signal is
  unsupported) — a cancelled git now exits through its own handler instead of being killed; and
  the engine's cascade and primary-merge steps re-read the run first (one extra `GetRun` each) and
  skip a run that is no longer `committing` instead of merging against a deleted branch. Neither
  changes a successful result.
- **Lock scope for embedders:** every `CLIClient` stateful call, and an in-place
  `git.EnterBranch(ctx, dir, "")` scope until its `Cleanup`, takes an uncontended lock on its tree's
  path; the registry entry is removed when the last holder leaves. A tree with no registered home
  branch (`WithHomeBranch` is never set by SMP) is never restored or detached and keeps the real
  `HEAD`. Use `scope.Context(ctx)` for git calls made inside an in-place scope; a locked call made
  with a context that does not carry the scope's ticket waits for the scope itself.
- **`artifact.Service` acceptance/rejection** (`AcceptTask`, `RejectTask`, the follow-up link)
  reads the task file inside an uncontended in-place scope on an embedder's worktree (the same
  bytes as before) and commits only if the file is unchanged when it writes — always true on an
  embedder's per-request worktree, where nothing else writes.
- **`git.CLIClient.ResetBranch`** remembers a take-back it could not complete — from the first
  call that can fail, the lock wait included; only proof that `expected` names no commit arms
  nothing — and the next `WithWorkingTree`, in-place `EnterBranch` scope or restore on that tree
  retries it first (and fails while the branch still holds the refused merge). Its git now runs on
  its own context, detached from the caller's cancellation and bounded at 30 s (review round 2: a
  take-back is a compensating action). On a tree with a home branch a refused two-tree switch is
  quarantined (`git stash create`) and reset (`read-tree --reset -u`) after the untracked-file
  check; on any other tree it is refused as before. Embedders that never call `ResetBranch` on a
  `CLIClient` (SMP) never have one and see none of this.
- **`domain.RepositoryMergeOutcome.KeepPendingPublication`** no longer carries a permanent-class
  prior, and the engine applies it only to a prior attempted in the run's current committing
  episode (one `ListStepExecutionsByRun` read, only when a carry would apply; a failed read records
  nothing and the run stays `committing`). This narrows a failure-detail prefix this task added;
  successful merges are unchanged.

---

## INIT-023 — Security Audit Remediation (EPIC-004 TASK-015: SSE field safety, shared-mode scoping, git-endpoint auth, gitpool credential scoping)

Standalone `spine serve` hardening (audit P3-28, P3-29, P3-30) plus the latent gitpool
credential item. Owner questions answered by their defaults, pending the owner, except question 2
(ruled "never" by the orchestrator; the design default was "yes, on the primary's own host").

### Security

- **SSE frames cannot be injected by workflow identifiers (P3-28).** Event IDs embed step,
  divergence-point and branch IDs, and core validation accepts a step ID with a line break, so a
  workflow on the authoritative branch could make every SSE reader dispatch a forged event (for
  example a fake `run_completed`) and adopt an attacker-chosen `Last-Event-ID`, live and on
  replay. The writer now omits an `id:` or `event:` line whose value holds a control character,
  invalid UTF-8, U+0085, U+2028 or U+2029 (a Warn, at most once a minute, with the count it did not
  log and the identifier cut to 80 bytes), writes `data` one line per
  `data:` line, and escapes U+0085/U+2028/U+2029 inside the JSON data. Every engine event's frame
  is byte-identical to before.
- **Workflow identifiers are checked on the standalone API (P3-28, defence in depth).**
  `POST`/`PUT /api/v1/workflows` refuse step, divergence-point, divergence-branch and
  convergence-point IDs outside `^[a-z0-9][a-z0-9_-]{0,63}$` with `422 validation_failed` (one
  `schema` error per field) before anything is written or a planning run starts;
  `POST /workflows/{id}/validate` reports them with `status: failed`.
- **Shared-mode state is scoped to the workspace (P3-29).** With a workspace `ServicePool`
  (`WORKSPACE_RESOLVER=db|platform-binding`): `/system/metrics` returns only the caller's
  `spine_workspace_pool_*` series (it used to name every tenant and carry the all-tenant
  counters); the rebuild registry is keyed by (workspace, `rebuild_id`), so another tenant reusing
  your `X-Trace-Id` prefix gets `404` instead of reading or replacing your rebuild; the SSE
  connection cap is counted per (workspace, actor), so streams in one workspace no longer exhaust
  the same actor ID's slots (every workspace's `smp-admin`) in another.
- **Token-less git access only for direct peers (P3-30).** The `SPINE_GIT_HTTP_TRUSTED_CIDRS`
  bypass was judged against the raw TCP peer, so a reverse proxy — or Docker's bridge gateway
  fronting the published port, inside the compose default `172.28.0.0/16` — gave every client it
  relayed token-less read access to every workspace's repository. A request whose peer is in
  `SPINE_TRUSTED_PROXY_CIDRS`, or that carries `X-Forwarded-For`, `Forwarded`, `X-Real-IP`,
  `X-Forwarded-Host`, `X-Forwarded-Proto` or `Via`, now needs a bearer; no forwarded address is
  trusted for access. A refused trusted-CIDR peer logs a rate-limited Warn naming
  `SPINE_TRUSTED_PROXY_CIDRS`.
- **`GET /git/…/info/refs` accepts only the queries a git client sends (P3-30 companion).** The
  push/read classifier parsed the query with Go's `url.ParseQuery`, while git-http-backend parses
  it with its own parser, and the two disagree on queries no client sends: git keeps the last of a
  repeated `service` (Go the first), and a name without `=` takes the next segment as its value
  (`service&git-receive-pack` is `service=git-receive-pack` to git, an empty `service` to Go). So
  `info/refs?service=git-upload-pack&service=git-receive-pack` and `info/refs?service&git-receive-pack`
  counted as reads for the token-less paths (a trusted-CIDR runner, `SPINE_GIT_HTTP_DEV_NO_AUTH`)
  and returned the receive-pack advertisement (the push-side ref and capability list; the pack
  upload itself always needed a bearer). `info/refs` now accepts, byte for byte, only an empty
  query, `service=git-upload-pack` or `service=git-receive-pack`. Any other query counts as a push
  (it needs a bearer) and is refused with `400` before git-http-backend; that includes `service=`
  with an empty value, which git-http-backend used to refuse with `403`. With receive-pack off (the
  default) such a query does not take the push-disabled auth bypass either (wave 3 review): through
  it an unauthenticated caller got `400` for an existing workspace and the uniform `401` for a
  missing one, the workspace-existence oracle EPIC-004 TASK-031 closed. A token-less one, a
  trusted-CIDR runner's included, now gets `401` whether or not the workspace exists; an
  authenticated one still gets the `400`.
- **`SPINE_DEV_MODE` no longer drops git authentication (P3-30).** It is accepted whenever
  `SPINE_ENV` is not `production`, and it removed authentication from clone **and push**
  (anonymous pushes reached the pre-receive gate). Pushes now always present a validated bearer.
  The new `SPINE_GIT_HTTP_DEV_NO_AUTH` opens clone/fetch only, and `spine serve` refuses it unless
  `SPINE_ENV=development`.
- **Rate-limit bucket reads every `X-Forwarded-For` line (P3-30).** It read only the first line,
  so behind a proxy that adds its own line (HAProxy `option forwardfor`) a client picked its own
  bucket.
- **A credential-less code repository never uses the process git credentials (latent gitpool
  item).** The gitpool cloner and client factory sent the process-wide `SPINE_GIT_PUSH_TOKEN`, and
  used `SPINE_GIT_CREDENTIAL_HELPER`, for a code-repo binding with no `credentials_ref` — against
  whatever host its `clone_url`, or a reused clone's `origin`/`pushurl`, named. Dormant (no
  code-repo catalog or `RepositoryManager` is wired in `spine serve`), P1 once one is. Both are now
  stripped for such bindings, on every host, and git runs with the host's own credential sources
  switched off too (no system or global git config — so no `credential.helper`,
  `http.extraHeader` or askpass configured there — no `GIT_ASKPASS`, and no process SSH agent or
  key for `ssh://` URLs).
- **`clone_url` redaction covers tokens in the username position (latent).**
  `GET /api/v1/repositories` echoed `https://<token>@host/...`; all userinfo of an `https` URL is
  now redacted (SSH logins and the SCP form are kept), and a malformed legacy value never echoes
  anything before its last `@`. Stored values are unchanged.

### Breaking

- **Workflow identifier rule (all standalone deployments).** A workflow whose step, divergence,
  branch or convergence IDs use upper case, `.`, `/`, spaces or more than 64 characters keeps
  loading and running (core validation is unchanged), but `PUT /api/v1/workflows/{id}` on it is
  refused with `422` until the identifiers are renamed (in-flight runs are pinned to their version,
  so a rename is safe for them). The rule is documented in `architecture/workflow-definition-format.md`
  §3.6. All shipped Spine and SMP workflows conform.
- **Shared-mode metrics scrapers** must move to `GET /api/v1/platform/metrics` (operator token) to
  keep the full export; `/system/metrics` in shared mode now returns the caller's workspace series
  only. Single-workspace mode is unchanged.
- **Runners behind a reverse proxy** need a bearer to clone (owner question 6 default).

### Breaking (dev setups)

- `SPINE_DEV_MODE` no longer gives token-less git access, for clone or push. For token-less local
  clones set `SPINE_GIT_HTTP_DEV_NO_AUTH=1` **and** `SPINE_ENV=development` (startup fails
  otherwise, including with `SPINE_ENV` unset); push with a token (`git -c
  http.extraHeader="Authorization: Bearer <token>" push …`).
- `docker-compose.yaml` no longer sets `SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.0.0/16`, so every clone
  through the compose stack needs a token by default (owner question 4 default).
  `docker-compose.override.yaml.example` documents runner-only recipes that exclude the bridge
  gateway.
- A trusted git CIDR that overlaps `SPINE_TRUSTED_PROXY_CIDRS` stops granting token-less clones;
  `spine serve` logs the overlap at startup.
- A code repository with no `credentials_ref` that relied on the process push token, the process
  or a host-level credential helper, or the process's SSH identity now clones and pushes
  unauthenticated: give it an `https` clone URL and a `credentials_ref` (there is no per-binding SSH
  credential yet). Its clone and push also ignore the rest of the system and global git config — a
  CA bundle or `sslVerify`, `http.proxy`, `url.<base>.insteadOf`, `safe.directory`: use the
  environment forms (`GIT_SSL_CAINFO`, `HTTPS_PROXY`, …), which still apply, and keep its clone
  owned by the `spine serve` uid (KNOWN-LIMITATIONS). Dormant today: `spine serve` wires no
  code-repository catalog.

### Changed

- New route `GET /api/v1/platform/metrics` (full export, `SPINE_OPERATOR_TOKEN`; `401` for a wrong
  token, `503` when unset). New environment variable `SPINE_GIT_HTTP_DEV_NO_AUTH`. No migration,
  schema, event type or `rebuild_id` format change.
- `/system/metrics` in shared mode answers `400` if no workspace resolved (unreachable past
  authentication).
- New logs: `sse: omitting unsafe event fields (rate-limited)` (Warn, at most once a minute, with
  `suppressed_since_last_warn`), the rate-limited proxied-peer Warn on `/git`, `git request rejected
  — non-canonical info/refs query` (Warn, with the query's length, never its bytes), the startup Warns for `SPINE_GIT_HTTP_DEV_NO_AUTH` and for
  a CIDR overlap.

### For embedders

- **`core/observe.ExportPrometheusWorkspace(workspaceID)`** — additive; `ExportPrometheus` output is
  byte-identical.
- **`adapters/git.WithoutPushToken()`** — additive `CLIOption`, mirroring
  `WithoutCredentialHelper`. No existing option, interface (`GitClient`) or call path changes.
- **`adapters/git.WithoutAmbientCredentials()`** — additive, opt-in `CLIOption`: a client with no
  token or helper of its own runs clone and push without the system and global git config
  (`GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL` = null device) or environment-injected config, with
  `-c credential.helper=` and `-c http.extraHeader=`, `GIT_ASKPASS`, `SSH_ASKPASS` and
  `SSH_AUTH_SOCK` emptied, `GIT_TERMINAL_PROMPT=0`, and a `GIT_SSH_COMMAND` that reads no
  ssh_config and disables the agent and key-based authentication, so no host credential source
  answers. Non-credential system and global settings (CA bundle, proxy, `insteadOf`,
  `safe.directory`) are dropped with them for that clone or push; their environment forms still
  apply. A client with its own credential is unchanged; without the option nothing changes.
- **`adapters/gitpool`**: `NewCLIClientFactory` and `NewCLICloner` strip the process push token and
  credential helper from their base options for an empty `Credential`, and apply
  `WithoutAmbientCredentials`. SMP does not import `adapters/gitpool`.
- **`adapters/repository.RedactCloneURL`** redacts all `https` userinfo and never echoes the
  userinfo of an unparsable or opaque value. SMP does not call it.
- Core workflow validation, the projection, `workflow.Service` (SMP's `WorkflowWriter`) and
  `core/refadmit` behaviour are unchanged; the identifier rule lives in `service/` only.

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

## INIT-023 — Security Audit Remediation (EPIC-001 TASK-003: bounded reads and NUL-delimited listings)

### Security

- **Git reads are bounded (audit F4, Spine half, P2).** The git CLI client buffered every
  command's whole stdout and copied a `ReadFile` result twice more (an 8 MiB blob cost 49 MiB),
  so a compressible pushed file of a few hundred MiB — a pack of well under a MiB — could take a
  shared, multi-tenant process down on every projection sync, run-detail view or artifact read
  that touched it. `ReadFile` now probes the blob's size (`git cat-file -s`) and refuses one over
  the per-blob limit (`git.DefaultMaxBlobBytes`, 4 MiB, equal to the worktree read limit) before
  reading it; the read itself fills one buffer pre-sized to the blob. Every other read command
  fails past 16 MiB of output, stderr keeps 64 KiB (head and tail) for every command — `Clone`'s
  included, which relays the remote's sideband — and read commands run with
  `GIT_ALLOC_LIMIT` (4 x the per-blob limit, at least 1 MiB), so git refuses any single
  allocation over it: a small blob stored as a delta on a huge base, or a gitlink named `*.md`
  that `git show` renders as a commit with its patch, is refused instead of inflated. That limit
  is per allocation, not git's total: read and diff commands also pin git's delta-base cache
  (`core.deltaBaseCacheLimit` = the per-blob limit; 96 MiB by default), which a chain of
  sub-limit delta bases otherwise filled (a 3 MB read cost 127 MiB in the git child; now 49–52 MiB).
  Each concurrent read can still hold about three times the alloc limit in git (about 50 MiB with
  the defaults). A command's output is held once (the parsed result shares its buffer). Mutating
  commands never fail on output size (git applies a merge before it prints its diffstat);
  `git diff` gets the output ceiling and the pinned cache but no alloc limit
  (`KNOWN-LIMITATIONS.md`, "Git read bounds").
- **Non-ASCII and control-character paths are no longer silently dropped (audit P3-9, Spine
  half).** `Diff` and `ListFiles` parsed git's C-quoted output, so a governed artifact in a
  directory with a non-ASCII letter — which `artifact.Slugify` produces from an ordinary
  non-English title ("Wdrożenie") — was never projected, never status-cascaded at merge, and
  `NextID` allocated its ID a second time. Both now read NUL-delimited output (`-z`) and return
  raw paths. A path that is not valid UTF-8, or holds a control character (C0, DEL, C1),
  U+2028/U+2029 or a bidi embedding/override/isolate control, is refused and recorded instead of
  projected (owner question 2, default pending the owner); invalid UTF-8 would otherwise fail every
  sync on PostgreSQL. A row a projection built before this change holds at such a path — possible
  only under `core.quotePath=false`, where git listed bytes >= 0x80 raw — no longer outlives its
  file: an incremental sync deletes it when the file is deleted, renamed away or modified, and a
  workflow row is kept quarantined, or removed when it does not verify, by the first sync's sweep
  or a sync that changes the file (new `projection_workflow_rejected` check `unsafe_path`; wave 3
  integration). Invalid UTF-8 still never reaches the store. An untouched artifact row at such a
  path goes at the upgrade's projection rebuild.

### Changed

- **413 for oversized reads:** `GET /api/v1/artifacts/<path>` (including `?ref=<run branch>`) and
  the workflow read return `413 payload_too_large` ("… is N bytes, over the L-byte read limit")
  for a blob over the per-blob limit, and ("… cannot be read within the read limits: git needed an
  allocation over the A-byte git allocation limit …") for a file git refuses to produce — the
  file itself may be small (a delta on a large base, or a gitlink's patch). A miss keeps its kind
  and text (`bad revision`, 404).
- **Projection:** an oversized `*.md` and any artifact or workflow candidate at a refused path are
  skipped, recorded and advanced past — never a sync error. A projected artifact modified past the
  limit loses its row. Each skip is a Warn line (`projection skipped file`), a
  `spine_projection_files_skipped_total` increment and a new **`projection_file_skipped`** event
  (`architecture/event-schemas.md` §4.15); `git_alloc_limit_bytes` says when it was git's
  allocation limit, not the file's size, that refused it. `projection_synced` gains `refused`.
  A skip is reported as soon as discovery decides it, so a sync held in `error` for another
  reason (an unreadable changed file, a failed wipe, a failed second listing of the root
  `workflows/` when `artifacts_dir` is a subdirectory — wave 3 review) still reports it, and
  re-reports it on each retry under the same event id; the counter counts each report.
- **The status cascade skips an added file at a refused path** (wave 3 review, codex): with the
  raw `Diff` listing such a file was read and rewritten, and under merge binding the cascade commit
  — which refuses a control byte in a path — then failed as a whole, so the run merged with its
  task's status unchanged. It is skipped, as it was while git quoted it (binding off too).
- **An oversized workflow file is a `projection_workflow_rejected` with check `read_bounds`** (its
  verified previous row kept quarantined, or not projected) and the sync advances. An oversized
  `.spine/branch-protection.yaml` is a held sync error with the rules unchanged.
- **A read-command output over 16 MiB fails closed:** a commit on `main` whose listing or diff
  exceeds it makes `IncrementalSync`, `FullRebuild` and `NextID` fail for that workspace (sync
  state `error`, nothing written) until the content is removed.
- **Behaviour:** existing subscriptions that receive every event type now also receive
  `projection_file_skipped`.
- **Diff details:** a copy record (only with `diff.renames=copies`) is reported as `added` at the
  copy's destination instead of `modified` at its source; a leading or trailing space in a listed
  path is kept; a malformed `git diff -z` stream is an error, never a partial list.
- `artifact.Service.List` and `workflow.Service.List` leave refused paths out, as they were while
  git quoted them; `artifact.IsIDCollision` counts an oversized existing file as a collision;
  `engine.ValidateBranch` (no caller in Spine or SMP) fails each refused file ("not validated: …")
  instead of letting it merge unvalidated.
- **Cost:** one extra git process per `ReadFile`, two on a miss; a full rebuild of a large
  repository takes roughly twice as long.
- **Read commands and `git diff` run in the C locale** (`LC_ALL=C`, `LANGUAGE=`; listings, diff,
  log, show, cat-file, rev-parse, merge-base), as the exact branch lookup already did: git
  translates the prefix of its alloc-limit refusal, which must be recognised. On a host with a
  translated git their error text is now English, so a miss that a translated message used to
  hide is classified `not_found` as on an English host. Their output does not depend on the
  locale. They also get `GIT_CONFIG_PARAMETERS` with the delta-base cache pin appended to any
  inherited value (the argv is unchanged).

### Upgrade note

Run one projection rebuild per workspace after upgrading (standalone: `POST
/api/v1/system/rebuild`). `IncrementalSync` processes only changes since the last synced commit,
so artifacts and workflows already on `main` at a non-ASCII path are not picked up until they next
change. After the rebuild, expect:

- artifact IDs that the old `NextID` allocated twice (an `EPIC-001` alongside a hidden
  `EPIC-001-résumé/`) to show up as two rows with the same ID — there is no uniqueness constraint,
  so the rebuild does not fail; renumber them;
- previously invisible workflow files with non-ASCII names to become active. List them first:
  `git ls-tree -r -z --name-only main -- workflows .spine | LC_ALL=C grep -z '[^ -~]'`.

SMP runs the rebuild per workspace in SMP INIT-027 EPIC-004 TASK-008 (owner question 3).

### For embedders

- **New in `adapters/git`:** `DefaultMaxBlobBytes`, `DefaultMaxOutputBytes`, `WithMaxBlobBytes`,
  `WithMaxOutputBytes`, `WithMaxGitAllocBytes`, `ErrBlobTooLarge`, `ErrOutputTooLarge`,
  `BlobTooLargeError` (with `GitAllocLimit` set when git refused an allocation), `OutputTooLargeError`
  (`WithMaxOutputBytes` is floored at 4 KiB and the
  alloc limit at 1 MiB, below which ordinary git output and git's own fixed allocations stop
  fitting). Refusals are `*GitError{Kind: ErrKindPermanent}`
  wrapping the typed cause, so `err.(*git.GitError)` keeps working. Every `NewCLIClient` gets the
  defaults; nothing to configure.
- **`Diff` and `ListFiles` return raw paths.** A caller that C-unquoted `Diff` paths must stop
  (SMP INIT-027 EPIC-004 TASK-003 already moved its reader to its own NUL-delimited diff).
- **New in `core/artifact`:** `RefusedFile`, `RefusalReason` (`RefusedOversized`,
  `RefusedUnsafePath`; `RefusedFile.GitAllocLimit`), `UnsafePath`, `ReadLimitPhrase`, and additive `Refused` fields on
  `DiscoveryResult` and `ChangeSet`. **New** `domain.UnsafeRepositoryPath`,
  `domain.EventProjectionFileSkipped`, `observe.Metrics.ProjectionFilesSkipped`.
- **A `GitClient` that wraps `ReadFile`** should make its too-large error match
  `git.ErrBlobTooLarge` (`errors.Is`); until then discovery treats it as a read error and
  `Service.Read` as a git error, exactly as before.
- No interface, schema, migration, endpoint or environment variable changes.

---

## INIT-023 — Security Audit Remediation (EPIC-002 TASK-005: SHA-bound governed merge)

### Security

- **The governed merge publishes the commit that was validated and reviewed, not the run-branch
  tip (audit F2, merge half, P2; P3-37).** The merge resolved the run branch's *name* when it ran,
  so a commit pushed after `validate` passed, after the reviewer read the diff, or while the run sat
  in `committing` between merge attempts reached `main` under the trusted "Merge run …" message —
  including `.spine/branch-protection.yaml` with `rules: []` and rewritten `workflows/**`. With merge
  binding on:
  - every main-path step completion records the run-branch head the engine resolved (reserved
    `runs.commit_meta` keys `spine.bound_sha`, `spine.bound_by`, `spine.head.<step>`,
    `spine.repo.<id>.bound_sha`; no schema change). The binding is stored before the execution is
    marked completed (wave 3 integration): the scheduler's step recovery re-submits a completed
    execution without resolving the head again, so a failed `commit_meta` write or a crash after
    the completion used to leave the run without that step's binding, and the next gate, with
    nothing to compare against, approved whatever the branch then held. A failed write now leaves
    the step open; it is still one `SetCommitMeta` per completion, with the outcome's commit map;
  - a `review` or `convergence` step's outcome that leads to the merge is refused when the branch
    moved since the previous binding (policy `reject`, the default; `rebind` accepts the new head);
  - every merge attempt merges exactly the bound commit, applies the status cascade at that commit
    with plumbing (never on the run branch), checks that the result is built from only the previous
    target tip and that commit, and pushes it without force; the cascade and recovery-ledger
    commits must be children of exactly the bound commit, and a merge result that fails the check
    is taken back off the local target before anything else can build on it or publish it;
  - the cascade rewrites only what the bound commit added since its merge-base with `main`. When
    `main` already holds the bound commit, a retry recomputes against `main` as it was before this
    run's earlier merge, and that merge counts as this run's only when its second parent is exactly
    the cascade commit this run builds against it (wave 3 review). Matching the commit graph alone
    let a run whose branch was reset onto another run's merged commit — the bound actor may
    force-push — take that run's merge for its own and rewrite the `status:` of every artifact
    that run added, with an empty reviewed diff; it now cascades `run.TaskPath` only, as with
    binding off;
  - a run with no binding the run itself recorded fails closed (`precondition`): a publish workflow
    routes `merge_failed` back to `execute`, a legacy terminal-commit workflow fails the run.

  The window after the reviewer read the diff is closed for clients that send `expected_head_sha`
  (below). An approval without it binds the head at submit time: the moved-head check compares that
  head with the previous binding, so a branch moved during the review and moved back to the bound
  commit before the submit is not detected (KNOWN-LIMITATIONS "Governed Merge Binding").
- **Workflow outcomes can no longer write `spine.*` commit metadata.** Reserved keys are stripped
  from every outcome `commit:` map before storage, with binding on or off, and `workflow.Validate`
  reports them (`steps[i].outcomes[j].commit`).
- **Code repositories (P3-37, dormant):** under binding, a run whose code-repository clients cannot
  bind is refused at `StartRun` before any branch exists; each code repository merges its bound
  commit; an already-merged repository is merged again when its bound commit is not on its default
  branch (the `merge_failed` loop-back no longer drops reviewed work); an operator retry rebinds
  only to a `head_sha` it names.

### Changed (standalone)

`spine serve` enables merge binding on the process-level and every per-workspace orchestrator, with
the `reject` policy and no switch to turn it off (owner questions 4 and 6; the rollback is the
previous build). Externally visible:

- **`POST /steps/{assignment_id}/submit` accepts `expected_head_sha`** (optional, a full lowercase
  hex commit id): the head the submitter worked on. Answers, with `detail.reason`:
  - `409 run_branch_moved` — the run branch is not at `expected_head_sha` (also with binding off);
  - `409 run_branch_moved_since_gate` — a review/convergence approval of a branch that moved since
    the previous step bound it; send the work back with a rework outcome;
  - `400 expected_head_sha_invalid` / `expected_head_sha_not_applicable` — malformed, or on a run
    with no branch, a divergence-branch step or an internal step; refused before the
    required-outputs check, so it never fails the step;
  - `503 run_branch_head_unavailable` — the head could not be resolved for a submit that must bind
    it; nothing was recorded.

  None of these answers changes the step: they come before the required-outputs check, so a submit
  that is refused on its head and also lacks a required output is not failed for the output.
- **UX change: a commit made during a review can no longer be approved in the same review.** A
  commit that reaches the run branch while a review is open makes the approval answer
  `409 run_branch_moved_since_gate`; the reviewer must choose a rework outcome so the change is
  validated again. In `spine serve` such a commit can only be an operator's push, a push started
  before the review that lands during it, or an API write (`/artifacts/add`, `write_context`)
  authorized during the step before the review that lands after that step's submit (the gateway
  reads the run once before the write; `service/internal/gateway/run_branch_write.go`): API writes
  are refused during a `review` step for every role (INIT-023 TASK-014) and pushes for
  non-operators (TASK-016), so a reviewer's fix-up goes through a rework round, not the open
  review. A commit that lands after a producer's submit is merged when a later producer step — an
  `automated` validate or summarize, which re-binds to the head it completes at — completes after
  it without `expected_head_sha` (KNOWN-LIMITATIONS "Governed Merge Binding" 2).
- **Runs in flight at upgrade** that reach the merge without a binding fail closed (publish
  workflows route back to `execute` and re-bind on the next round).
- **The status cascade commit is no longer on the run branch**; it reaches `main` only through the
  merge. A `merge_failed` loop-back no longer leaves `status: Completed` on the branch being
  reworked.
- **A run branch whose tip held commits the merge left out is kept** instead of deleted, and a
  `WARN merge_binding: commits after the bound head were not merged` names `run_id`, `bound_sha`
  and `tip_sha`. A tip that cannot be read or judged keeps the branch too (`WARN merge binding: run
  branch tip unknown; keeping the run branch`); only a branch that no longer exists is not kept.
- **Planning-run collision renumber is skipped** under binding (it committed on the branch tip).
- **A merge the structural check refuses is taken back** off the local `main` (compare-and-swap
  `update-ref`, the working tree following with a two-tree `read-tree -m -u`), so the next governed
  merge or in-place write cannot publish it — also with `SPINE_GIT_PUSH_ENABLED=false`, where the
  local `main` is the published branch. A write that landed on `main` between the merge's base read
  and the merge is kept, and the run stays in `committing` (outcome class `remote_unavailable`) so
  the next attempt merges on top of it instead of failing the run or routing it back to `execute`
  (a write that lands during a replay of a landed merge is not kept: KNOWN-LIMITATIONS "Governed
  Merge Binding" 7). A take-back that cannot run is an `ERROR merge binding: refused merge NOT
  taken back` and is named in the merge outcome's failure detail.
- **Merge retries add nothing.** A retry or resume after the merge landed on the local `main`
  recomputes the same status-cascade commit — also when the run added child artifacts — so it finds
  `main` already up to date instead of adding a second cascade commit and merge commit. The earlier
  merge is recognised by recomputing this run's cascade commit against it (wave 3 review), never by
  the commit graph alone.
- **Operator recovery actions on one run are serialised** under binding (wave 3 review). Two
  overlapping `retry` / `resolve-externally` requests for one partially-merged run each chained
  their ledger on the binding they had read and replaced the whole `commit_meta` from that
  snapshot, so the later one restored the other repository's old binding (an operator's
  `head_sha` rebind was lost and the old commit merged) and left the other ledger out of the
  published history. The ledger is now built and recorded under a per-run in-process stripe from
  a fresh read of the run; a request whose run is no longer partially merged by then is
  `409`, and one whose context ends while it waits is `503`, both with nothing written. Requests
  served by different processes sharing a database are not serialised (KNOWN-LIMITATIONS
  "Governed Merge Binding" 15).
- **`POST /runs/{run_id}/repositories/{repository_id}/retry` accepts `head_sha`** (and `spine run
  merge retry --head-sha`): the code repository's current run-branch head to rebind to. Without it
  the retry merges the bound commit again; a stale `head_sha` is `409 run_branch_moved`. The ledger
  commit carries a `Rebound-SHA` trailer and the event a `rebound_sha` field. Under binding the
  recovery ledger commit is built as a child of the bound commit and is not pushed to the run branch.
- **Push of the merge:** `push --end-of-options origin <merge>^{commit}:refs/heads/main` (no force),
  in place of `push origin <main oid>^{commit}:refs/heads/main`. A transient push failure recovers
  on the next attempt; a non-fast-forward refusal stays permanent, as before.

### Embedders (library)

- New opt-in `Orchestrator.EnableMergeBinding(engine.MergeBindingConfig{MovedHead: ...})` and
  `MergeBindingEnabled()`. It returns an error, leaving binding off, unless the `GitOperator`
  implements the new optional `engine.CommitBinder` interface (`ResolveBranchHead`, `VerifyCommit`,
  `IsAncestor`, `CommitParents`, `CommitFilesOnto`, `MergeBound`, `PushCommit`, `ResetBranch`). No method was added
  to any interface an embedder implements. `*git.CLIClient` implements it; embedders run the shared
  suite `core/engine/enginetest.RunCommitBinderConformance` against their own operator. The suite
  accepts both forms of `ResetBranch` the contract allows — the local branch moved back, or dropped
  by a remote-backed operator so the next `MergeBound` re-creates it from the remote (a clean working
  tree either way) — and every commit it hands the binder is on a branch of `Source`, so a harness
  whose `Sync` fetches sees it. `*git.CLIClient`'s `CommitBinder` commands run under TASK-003's
  read bounds (wave 3 integration): the C locale, so a lock race stays a retryable error on a
  translated host, the per-command output limit (`git.ErrOutputTooLarge`) and the bounded stderr;
  not `GIT_ALLOC_LIMIT` (KNOWN-LIMITATIONS "Git Read Bounds").
- Additive fields: `git.MergeOpts.SourceSHA` (merge exactly that commit; `CLIClient.Merge` honours
  it), `git.MergeResult.BaseSHA`, `engine.StepResult.ExpectedHeadSHA`,
  `engine.SubmitRequest.ExpectedHeadSHA`. New: `Orchestrator.RetryRepositoryMergeAt`,
  `engine.HeadBindingDetail` (the `Detail` of every binding refusal) and the `Reason*` constants,
  the `CommitMeta*` key constants, `git.IsCanonicalObjectName`.
- **Shared behaviour at the next bump, binding off:** reserved `spine.*` keys are stripped from
  outcome `commit:` maps (no shipped workflow in Spine or SMP uses them); `workflow.Validate` now
  rejects an outcome `commit:` key starting with `spine.` in any letter case, at
  `steps[i].outcomes[j].commit` — not opt-in, so every workflow an embedder validates (SMP's workflow
  writer and seeding included) is refused with such a key; a client that sends
  `ExpectedHeadSHA` gets it checked (old clients never send it); `git.CommitOpts` trailers outside
  the standard four are now written in sorted order instead of map order. Everything else — every
  code path and git argv — is unchanged without `EnableMergeBinding`.

---

## INIT-023 — Codex 6.1 catch-up review of waves 1 and 2

### Changed

- **Worktree reads and creates no longer wait on a FIFO** (`core/worktreefs`, TASK-004). After its
  checks, a leaf is opened by name once more: `ReadFile`'s read, and `Create`'s read-only pin of
  the file it has just made. That open is now non-blocking, so a leaf swapped for a named pipe in
  the window is refused with `ReasonChanged` instead of blocking the call, and the handles it
  holds, until a writer appears. Request cancellation could not interrupt the wait. `ReadFile`
  also requires the opened file to still be a regular file, because `SameFile` cannot catch every
  swap: on Linux a leaf deleted and re-created as a FIFO can reuse the freed inode number. Git
  cannot check out a FIFO, so only a local process writing inside the worktree could do this.
  Nothing changes for any other input.
- **Stored workflow definitions holding an integer above 2^53 now verify** (`core/workflow`,
  `adapters/projection`, TASK-011). `workflow.VerifyProjectionDefinition` decoded the stored JSON
  through float64, so a definition that `ProjectionDefinition` had accepted and stored (for example
  `retry: {limit: 9007199254740993}`) then failed its own verification. A later rejected edit of
  that file deleted the row instead of keeping it quarantined for the runs pinned to it. Integers
  that fit an int64 are now read exactly, and every other number reads as before. The first-sync
  reconciliation compares stored and head definitions the same way, so a row that differs from head
  only in such an integer is replaced instead of being counted unchanged. Definitions whose
  integers are all at or below 2^53, which includes every shipped Spine and SMP workflow, see no
  change.
- **The reconciliation's baseline reset keeps a concurrent status** (`adapters/projection`, the
  TASK-011 sweep as amended by the wave 2 cross-task review). When the first-sync reconciliation
  writes a row on an otherwise up-to-date sync, it re-records the head it judged by. That write
  was an unconditional `idle` at that head, which replaced any status another instance had written
  since this sync read `idle@head`:
  - `rebuilding`: a reader saw `idle` over a half-written projection;
  - `error`: a same-head rebuild failure was erased, and the next tick took the up-to-date return
    and skipped the forced branch-protection retry;
  - the no-baseline `error` a partial wipe records: the next sync diffed from the head instead of
    rebuilding, so the wiped rows stayed missing.

  Now only `last_synced_commit` moves, in a single `UPDATE`, and a state with no baseline is left
  alone. When no other writer is involved the result is `idle@head` as before.

### For embedders

- **New optional store capability:** `(*store.PostgresStore).SetSyncBaseline(ctx, commit) error`.
  `projection.Service` asserts it and it is not added to `store.Store`, like
  `ListWorkflowProjections`. A custom store without it keeps the previous `idle@head` write. SMP's
  projection writer and provisioning pass `*store.PostgresStore` and get the new behaviour with no
  change. For SMP it differs from before only when another instance writes `sync_state` inside that
  window.

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
  sees no actor. *(Superseded by INIT-023 TASK-015: `SPINE_DEV_MODE` no longer skips git
  authentication, and every push presents a bearer.)* Refusals arrive as `remote: ref-admission: …` lines with per-ref `ng` status
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
    otherwise up to date, it re-records the head it judged by as `sync_state`'s
    `last_synced_commit` instead of returning without a write. Its head is resolved before the rows
    are listed, so another instance (an SMP replica, a re-warmed workspace) may have projected a
    newer head in between; if it did, the next tick re-diffs from this head and restores what the
    reconciliation undid. Through `(*store.PostgresStore).SetSyncBaseline` the status another
    instance wrote meanwhile is kept, and a state with no baseline is left alone (codex 6.1
    catch-up; a store without the capability writes `idle` at that head). A row whose projecting
    sync spans the whole reconciliation can still be lost, as with any two instances projecting at
    different heads;
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
