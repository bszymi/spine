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

### Projection Sync: What INIT-023 TASK-011 Leaves Open
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
  forward it gets the historical read: a successful read is trusted (a gitlink recording the id of
  a blob in the repository is read as that blob), and every read failure is infrastructure (a
  dangling gitlink holds the sync in `error`, fail closed, instead of being skipped or rejected).
- **A gitlink at an artifact path whose recorded id names a blob in the repository is read as that
  blob** — by artifact discovery in both projection entry points and by every other artifact
  reader (`artifact.Service` reads, branch validation) — while diffs and repository viewers show a
  submodule pointer. Workflow files and `.spine/branch-protection.yaml` are typed before they are
  read, so this cannot change a run's workflow or the branch rules (TASK-011 review round 2).
  Typing every artifact in the projection alone would not close it for the other readers and would
  add a git process per artifact to every full rebuild; refusing gitlink entries in a governed
  repository at the push and merge boundary closes it for every reader at once (follow-up).
- **A first full rebuild that was interrupted** (the process stopped while `sync_state` was
  `rebuilding` with no synced commit) is not retried automatically, because it cannot be told apart
  from a first rebuild still running on another instance; run `POST /api/v1/system/rebuild`. A first
  rebuild that *fails* records `error` and is retried by the next sync.

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
- When `SPINE_DATABASE_URL` is not set, auth service is nil
- Auth middleware returns 503 (fail closed) — no bypass
- Production deployments MUST configure database and auth

### Path Traversal
- `safePath` in artifact service resolves symlinks and validates prefix
- `GIT_LITERAL_PATHSPECS=1` prevents Git pathspec injection
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
- **Status:** a documented deployment requirement, not yet enforced. Fail-closed handling (a
  per-repository case-sensitivity probe that refuses receive-pack, or fold-and-normalize rule
  matching that also covers the `refs/heads/` prefix test) is an INIT-023 EPIC-001 follow-up,
  paired with SMP's equivalent follow-up.

## Architecture Deviations

### Convergence Evaluation Step
- Convergence strategies are evaluated programmatically
- The spec describes an "evaluation step" where an actor reviews branch outcomes
- Current implementation routes to the step with `converge` field after auto-evaluation

### Branch Isolation
- Git branches are created for isolation
- No runtime enforcement prevents cross-branch artifact reads via API
- Isolation is convention-based, not enforced at the store level
