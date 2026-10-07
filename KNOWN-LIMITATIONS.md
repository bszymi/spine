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

### Git Push: Run-Branch Admission Is a Push-Start Check
- With `SPINE_GIT_RECEIVE_PACK_ENABLED=true`, a run, planning or divergence branch is pushable only
  by the actor bound to its current step, or an operator (INIT-023 TASK-016). Three limits remain:
  - **Admission time, not write time.** The gate decides when the push's command section arrives;
    git writes the ref only after the pack has been received and processed. A push started while
    bound can land after the actor submits the step. The window is the upload (at most the HTTP
    `ReadTimeout`, 30 s in `spine serve`) **plus** git's processing of the received pack (index,
    delta resolution, connectivity check), which nothing bounds: `net/http/cgi` ignores the request
    context and does not bound `git-http-backend`, and the pusher influences the processing time
    through the pack it sends. The review freeze is checked at the same moment, so a push started
    before the step was submitted can still land during review. Binding the governed merge to the
    reviewed commit (INIT-023 TASK-005) is the content control for that window; until it lands,
    the bound actor can still move the tip after validation. A real bound needs `git-http-backend`
    run in its own process group under a deadline (a deadline would also cut legitimate large
    pushes and clones, which today outlive `OpTimeout`), or a post-unpack re-check; both are
    INIT-023 EPIC-001 follow-ups.
  - **Force-push by the bound actor is allowed.** Fast-forward-only cannot be enforced per actor
    before the pack is unpacked, and `receive.denyNonFastForwards` would also block operators.
  - **Dev mode pushes are anonymous.** `SPINE_DEV_MODE` skips git authentication, so dev pushes
    reach the gate with no actor and can write ordinary branches only. Validating a presented
    bearer in dev mode belongs to INIT-023 TASK-015.
- **Exploratory divergence branches: creation is bound to the run, their refs are operator-only.**
  Creating one (`POST /runs/{run_id}/divergences/{divergence_id}/branches`) needs an operator, or
  the actor who submitted the run-level step that triggered the divergence (while the run's cursor
  is `divergence:<id>`, the engine's state for an open exploratory window), or an actor holding a
  live cursor of the run. The API creates no step execution for the new branch, so nobody below
  operator can then push its divergence ref (pushes need a live execution). Wiring step
  executions for exploratory branches is a follow-up (TASK-016 owner question 6).
- **Case-insensitive storage.** The admission folds the `spine` namespace with Unicode simple case
  folding, so case-variant spellings are operator-only on any filesystem. Branch-protection rule
  matching does not fold; see "Served Repositories Need Case- and Normalization-Sensitive Storage"
  below.

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
- Worktree file I/O (artifact create/update and their rollbacks, acceptance/successor reads,
  workflow create/update, the merge-time status cascade, renumber) goes through
  `core/worktreefs`. Each path component is checked with `Lstat` from a directory handle opened
  without following symlinks, inside an `os.Root`. Any symbolic link component is refused
  (`invalid_params`), and so is any `.git` component (including git's case, NTFS and HFS
  variants). New files are created `O_CREAT|O_EXCL` and updates replace by rename. A failed
  create removes what it made, including a file a failing hook replaced by a rename while the
  path is untracked in HEAD. A narrow race remains in the shared standalone tree only: a
  concurrent update of the same still-uncommitted path that lands just before that rollback is
  removed with it — the update fails, or, if it commits between the HEAD check and the unlink,
  its file is deleted from the working tree (not from git). Reads of worktree files are capped
  at 4 MiB (`payload_too_large`), so an artifact Update / accept / reject of an existing file
  over 4 MiB (only reachable by a push or an import) is refused where it used to succeed.
  (INIT-023 TASK-004, audit F5.)
- Residual: committed symlinks are still checked out as links (Spine does not set
  `core.symlinks=false`), and reads through `git show` never follow them.
- `GIT_LITERAL_PATHSPECS=1` / `--literal-pathspecs` prevents Git pathspec injection
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

### Git Ref Arguments
- **Grammar** (INIT-023 TASK-002; audit F10, P3-32, P3-36): the git client
  (`adapters/git.CLIClient`, `EnterBranch`) builds every ref argument through one grammar.
  Branch arguments are short names or `refs/heads/<n>` that pass `refname.Validate`; they are
  looked up exactly (`git show-ref --verify`, no DWIM) and reach git as object ids, so a pushed
  tag or ref named after a run branch or `main` can no longer stand in for it. Revisions resolve
  branch-first, then exactly as before. Push sources are `<oid>^{commit}`, push destinations
  `refs/heads/<n>`; `Push` accepts branches only. A new branch is cut from `<oid>^{commit}` or
  `HEAD^{commit}`: `git branch` alone re-resolves a bare start point by DWIM, so a ref named after
  the base's object id would replace it and a `refs/tags/HEAD` would make it fail. Every ref
  follows `--end-of-options` and is validated before git starts; a refusal is a permanent error
  wrapping `git.ErrInvalidRef`. A repository's `default_branch` must be a short branch name
  (`main`, not `refs/heads/main`; API 400, engine precondition).
- **Cost:** a revision given by name runs one extra `git show-ref` per call when it is
  `refs/heads/<n>` or a bare name that is a valid branch name, whether or not the branch exists:
  branches, tags (`v1.2`), abbreviated ids and `origin/<n>`. Full object ids, `HEAD` and other
  `refs/*` names (`refs/tags/<t>`, `refs/remotes/<r>/<n>`) cost nothing extra. Callers that read
  many files at one name resolve it once with `RefSHA` and read by object id
  (`artifact.Service.List` does; SMP's run-artifacts reader is handed off to SMP INIT-027 EPIC-004
  TASK-008).
- **Git floor:** git 2.30 or newer (`rev-parse --end-of-options`). `spine serve` refuses to start
  below it; embedders call `git.CheckGitVersion` at boot. Every shipped image runs 2.39.5 or later.
- **Residual — user `?ref=` for a non-branch name still follows DWIM.** A read with
  `?ref=<name>` where `<name>` is not an existing branch resolves as git always has, so a pushed
  `refs/<name>` beats `refs/tags/<name>`. Name tags in full (`?ref=refs/tags/<name>`). TASK-016
  blocks such pushes for non-operators.
- **Residual — missing non-branch refs.** A `refs/tags/...` or `refs/remotes/...` argument naming
  a ref that does not exist can still be DWIM-substituted by a name such as
  `refs/tags/refs/tags/...`; push admission refuses to create or update those names for every
  principal (TASK-016 rule 0b; below).
- **Residual — doubly-qualified refs on the remote break pushes.** If the repository a client
  pushes to holds `refs/heads/refs/heads/<n>`, `refs/refs/heads/<n>` or
  `refs/tags/refs/heads/<n>`, every push to `<n>` fails (`dst refspec refs/heads/<n> matches more
  than one`), whatever the client argv; in standalone that is the governed-merge push to `main`.
  The git client cannot fix this. The failure is classified permanent, so the engine does not
  loop on it. Push admission refuses to create or update such names for every principal: Spine
  INIT-023 TASK-016 (standalone, rule 0b) and SMP INIT-027 EPIC-004 TASK-005 §7. Existing such refs are not deleted
  automatically; list them in each served repository and delete them (see `CHANGELOG.md`):
  `git -C <repo> for-each-ref refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs`.
- **Residual — refs named `HEAD` or after an object id.** Spine's own git calls are immune to
  them (exact lookups, object ids, `^{commit}` start points), but other git clients that work on
  the served repository or its clones are not (`git branch <n> HEAD` dies next to a
  `refs/tags/HEAD`; `git checkout <oid>` can land on a branch named `<oid>`). TASK-016 refuses new
  `HEAD` components; existing ones, and object-id-named refs, are listed and deleted per the
  `CHANGELOG.md` upgrade notes.
- **Dormant — code-repo default branch must exist locally.** Code-repository clones are made with
  `--no-tags` and hold a local branch only for the remote's `HEAD`. For a `default_branch` that is
  not the remote's `HEAD`, run start's `git branch <run> <d>` already failed (`git branch` never
  reaches `origin/<d>`), while the code-repo merge's `git checkout <d>` created `<d>` from
  `origin/<d>` by guessing; the merge is now refused too (`switch --no-guess`), and run start
  reports `not_found`. No shipped build wires a code-repository catalog (P3-36, P3-37), so this is
  dormant.

## Architecture Deviations

### Convergence Evaluation Step
- Convergence strategies are evaluated programmatically
- The spec describes an "evaluation step" where an actor reviews branch outcomes
- Current implementation routes to the step with `converge` field after auto-evaluation

### Branch Isolation
- Git branches are created for isolation
- No runtime enforcement prevents cross-branch artifact reads via API
- Isolation is convention-based, not enforced at the store level
