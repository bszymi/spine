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

### Webhook Delivery (after INIT-023 TASK-010)
- **v1 signatures stay replayable.** v1 (the default) signs the body only. Opt a subscription
  into v2 (`metadata.signature_version: "v2"`, integration guide §6.1) for a timestamp and
  single-use nonce inside the MAC. Making v2 the default for new subscriptions is an open
  owner question.
- **The subscription test ping is unsigned**, so a receiver that enforces signatures rejects
  it. Follow-up: sign it in the subscription's version.
- **Redirects are followed with the signature headers.** Each hop is re-validated, but Go's
  client copies the headers (a fresh v2 signature and nonce included) to the redirect target.
  Not changed here: refusing redirects would dead-letter receivers that rely on an
  http→https or trailing-slash redirect today.
- **A slow endpoint throttles its workspace.** The dispatcher waits for a whole batch
  (default 5 deliveries) before claiming again, so an endpoint that answers just under the
  10 s timeout — even with a success — limits every subscription in the workspace to about
  5 deliveries per 10 s. The fair claim does not help; per-subscription in-flight limits are
  the follow-up.
- **A replica-local fault holds a subscription for every replica.** The circuit breaker is in
  memory per process. With several dispatchers on one runtime database (SMP replicas), a
  replica whose circuit for a subscription is open, or which cannot read the subscription (for
  example one started without its encryption key), holds that subscription's due queue in the
  database until its next attempt, up to 60 s per cycle; rows that arrive between holds are
  still claimable by the healthy replica. A shared (database-backed) breaker would remove this.
- **The start of a hold is kept in memory.** `MaxCircuitHold` (24 h) counts from when the
  dispatcher process saw the circuit open, or first failed to read the subscription, so a
  restart (or a replica that has just started) starts it again. A subscription that stays
  unreadable while its dispatcher restarts more often than every 24 hours never has its rows
  dead-lettered by the cap: they stay `failed` (at the attempt budget, so SMP's run banner
  shows them) and are retried every minute. A "held since" column (a schema change) would
  remove this.
- **A refused target URL dead-letters the whole due queue.** A permanent verdict on a
  subscription (deleted, invalid metadata, refused URL, unusable TLS or signing configuration)
  dead-letters every delivery of it that is due, not only the one being tried. A host name is
  refused on what it resolves to at that moment, so a DNS answer that points at a non-public
  address even briefly costs the subscription its due deliveries; they stay replayable.
- **Re-holding a very large backlog is one large write per probe cycle.** When an open circuit's
  probe time arrives, the whole held backlog becomes due and the first hold after the probe
  re-schedules it in one statement: about 70 ms for 2 000 rows and 1.6 s for 100 000 rows on a
  loaded development machine (PostgreSQL 17), once a minute, inside one poll.
- **A lost status write strands a row in `delivering`.** Without a `claimed_at` column (a
  schema change) such a row is never re-claimed.
- **NAT64 with a network-specific prefix** (RFC 6052 §2.2) cannot be recognised from the
  address; egress policy is the control there.
- **Rows written before TASK-010 keep their unredacted `last_error`** until retention deletes
  them; the task's Implementation Notes give a one-off `UPDATE` an operator can run.
- **Operator replay keeps `attempt_count`**, so a replayed exhausted delivery is
  dead-lettered by its first failure. The response body of a delivery is drained without a
  size cap (bounded only by the 10 s client timeout).

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
