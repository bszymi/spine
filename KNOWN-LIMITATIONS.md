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

### Step-Entry Checks Are Opt-In for Embedders (and Off Under `legacy`)
- The engine's step-entry policy (`engine.StepEntryPolicy`, INIT-023 TASK-007) closes audit
  P3-3 (assign checks no actor constraints), P3-4 (precondition-failed steps stay bindable; two
  evaluator checks fail open), P3-15 (claim ignores `execution.mode`) and P3-16 (entry points
  ignore run status; cancel leaves steps live), and adds separation of duties.
- **The zero value is the pre-INIT-023 behaviour.** An embedder that has not called
  `WithStepEntryPolicy` (SMP until its TASK-011), and `spine serve` with
  `SPINE_STEP_ENTRY_POLICY=legacy`, still has all four findings open and no separation of duties.
  `spine serve` defaults to the strict policy; `legacy` is a logged escape hatch for operators
  migrating custom workflows and will be removed.
- **Separation-of-duties residual (accepted, Owner question 6):** `AssignStep` writes no
  assignment row, so if restart recovery resets an assign-bound execution and a different actor is
  bound, the first actor leaves no record and is not treated as a run author. Closing it needs an
  append-only binding ledger (a schema change; follow-up). SoD also cannot see git authorship
  directly; INIT-023 TASK-016 limits run-branch pushes to the bound actor.
- **Separation-of-duties bind race across processes:** the SoD check reads state and the bind
  writes it later. Within one process the engine serialises a run's check-and-bind (an in-process
  lock shared by every orchestrator), so one actor cannot win a live review and an author branch
  step of the same divergence run by racing two requests. Processes that share one database are not
  serialised: an embedder running several replicas with `SeparationOfDuties` on keeps that race
  (linear workflows are unaffected — the review execution exists only after the author step was
  submitted). Closing it needs a database-level lock per run around check and bind; TASK-008's
  per-row compare-and-swap does not, because the two binds write different execution rows.
- **Actor resolvers must be canonical:** the engine requires an `Actors` resolver to echo the
  requested ID, which stops it answering for a different identity, but it cannot tell a canonical
  handle from a variant the resolver chose to accept. A resolver that accepts case, whitespace or
  alternate UUID spellings of a handle lets the variant bind as a second identity and defeats
  separation of duties; embedders (SMP TASK-011's `human-<uuid>` resolver) must refuse every
  non-canonical input. Standalone has no resolver: actor lookup is an exact `auth.actors` match.
- **Stored data:** runs that were cancelled or failed before the upgrade keep their live
  executions. They are inert under `EnforceRunStatus` (every entry point refuses them, and the
  candidate listing hides them); a one-shot sweep is a follow-up.
- **Not atomic:** run-end termination is a blind write that a concurrent bind can overwrite until
  INIT-023 TASK-008's compare-and-swap; `EnforceRunStatus` is the backstop.
- **Status preconditions on the run's own artifact** are evaluated against `main`, where commit
  status lands only at merge; a precondition requiring a status an earlier outcome commits can
  never hold mid-run (see `workflow.DeferredStatusPreconditions`). Custom workflows with such a
  precondition strand under the strict policy until it is removed.

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
