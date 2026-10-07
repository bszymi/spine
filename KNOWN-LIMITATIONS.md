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

### Platform Bootstrap Bearer (`SMP_ADMIN_TOKEN`) Rotation
- **Behaviour (INIT-023 TASK-013):** the first load of a workspace after a restart with a new
  `SMP_ADMIN_TOKEN` revokes the previous bootstrap bearer there (only `bootstrap-…` rows of
  `smp-admin` created before the replica started; admin-minted `tok_…` tokens are kept).
  Revocation is never undone.
- **Not zero-downtime:** platform requests that still carry the old bearer get 401 from each
  workspace's first load after the Spine restart until the platform switches. Switch both in one
  change window (`docs/operator-runbook.md` §5.6). A two-phase `SMP_ADMIN_TOKEN_PREVIOUS` grace
  is a recorded follow-up (TASK-013 owner question 2).
- **Rollout residues:** rows are ordered by time, which cannot tell a stale bearer re-inserted
  late from a newer one. Both residues are confined to workspaces first loaded during a
  rotation's rollout (a row for one bearer and none for the other); only bearer generation
  numbers would remove them (owner question 5), and the default accepts them.
  - *Restart during the rollout:* an old-config replica that restarts mid-rollout and then loads
    such a workspace revokes the *new* bearer there. Recovery is to rotate forward to a fresh
    value.
  - *Old bearer inserted after the new one:* if a new-config replica seeds the new bearer first
    and a replica still carrying the old bearer (an old-config replica, or pre-upgrade code during
    the upgrade rollout) then loads the workspace, the old bearer's row is newer than the new
    one's. No replica carrying the new bearer revokes it, restarts included, so the **old bearer
    stays valid until the next rotation**. Only a replica that started after that row was
    written logs it at Warn (with the row's `token_id`), on each load of such a workspace. A
    replica already running when the row was written cannot tell it from a rollout still in
    progress and stays quiet on every load, idle-eviction reloads included, so the Warn appears
    only once every replica is restarted after the rollout. Revoke the named row with
    `DELETE /api/v1/tokens/{token_id}` (runbook §5.6 (e)).
- **A failed bootstrap leaves the old bearer live until a retry succeeds:** `spine serve`
  retries a failed admin bootstrap in the background (from 1s, up to 1 min apart) for as long as
  the workspace stays loaded; each failure is logged at Error.
- **Clock dependence:** rows are ordered by the `created_at` the inserting replica stamps, so the
  rollout cutoff assumes replica clocks agree to well within the deploy's duration.
- **Legacy token ids:** bootstrap rows written before TASK-013 keep their
  `bootstrap-<sha256(bearer)[:12]>` id (listable by a workspace admin, printed at Debug on each
  load) until the next rotation revokes them. Rotate once after upgrading.
- **Unsetting `SMP_ADMIN_TOKEN`** stops seeding but does not revoke the existing bootstrap row;
  revoke it with `DELETE /api/v1/tokens/{token_id}`.

### Skill Eligibility Is Matched by Name
- Workflow `required_skills` and claim-time eligibility match skill *names*, not ids. Since
  INIT-023 TASK-013 every operation that can move eligibility (granting, removing, creating,
  renaming, deprecating) is admin-only, a non-admin can never change its own skills, and a rename
  or deprecation that touches a name an active workflow requires needs `?force=true`. Matching by
  immutable `skill_id` is a deferred follow-up (a governed workflow-format change).
- The rename and deprecation guards scan **active** workflow projections only; a run pinned to a
  superseded workflow version that names the skill is not detected.
- `auth.actor_skills` records no grantor, so grants made by non-admins before the upgrade are not
  distinguishable. Review `review`, `workflow_review` and `architecture` holders after upgrading.
- `PATCH /skills/{id}` and `POST /skills/{id}/deprecate` are read-modify-write. They re-read the
  skill right before writing (a rename in between is a 409), but a change another admin commits
  between that re-read and the write is still overwritten. Closing the window needs a
  conditional `UPDATE` in the store (a follow-up).

## Architecture Deviations

### Convergence Evaluation Step
- Convergence strategies are evaluated programmatically
- The spec describes an "evaluation step" where an actor reviews branch outcomes
- Current implementation routes to the step with `converge` field after auto-evaluation

### Branch Isolation
- Git branches are created for isolation
- No runtime enforcement prevents cross-branch artifact reads via API
- Isolation is convention-based, not enforced at the store level
