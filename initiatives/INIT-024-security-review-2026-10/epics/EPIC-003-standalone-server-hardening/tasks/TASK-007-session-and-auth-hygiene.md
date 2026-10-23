---
id: TASK-007
type: Task
title: "Session and auth hygiene: SSE revalidation, no pre-auth ServiceSet, explicit SPINE_ENV, evidence provenance"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/tasks/TASK-015-gateway-and-credential-scoping.md
  - type: related_to
    target: /architecture/execution-evidence.md
  - type: related_to
    target: /architecture/security-model.md
---

# TASK-007 — Session and auth hygiene: SSE revalidation, no pre-auth ServiceSet, explicit SPINE_ENV, evidence provenance

---

## Purpose

This task closes four standalone P3 findings from the 2026-10-22 review (N-14, N-15, N-16, N-17;
detail in SMP's git-ignored `tmp/security-review-2026-10-22/spine/REPORT.md`). Each is a session,
boot or display edge of standalone `spine serve`. SMP embeds none of the touched packages.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-14 | P3 | Standalone | A member with an open SSE stream whose token is later revoked or whose actor is suspended. The stream keeps delivering until it disconnects. |
| N-15 | P3 | Standalone shared | An unauthenticated caller. `servicePool.Get` builds the workspace's services (DB pool, bootstrap, secret sweep) before auth, and a build failure answers 500 while a missing workspace answers the uniform 401 — an existence oracle. |
| N-16 | P3 | Standalone | A deployment with `SPINE_ENV` unset or mistyped. Production gates (plaintext webhook secrets refused, strong `SMP_ADMIN_TOKEN`, dev-mode refused) silently do not apply. |
| N-17 | P3 | Standalone | A run-branch author, for `run.status`. Evidence for an active run is read from the author-writable run branch first and shown as "checks passed". It is display-only (the merge gate reads the server record), but it misleads a human reviewer. |

## Current code (verified at `a560c210`)

- **N-14.** `handleEventStream` (`service/internal/gateway/handlers_events_stream.go:26`) calls
  `authorize` once (`:27`), then loops on `events`, `ssDone`, `r.Context().Done()` and a 30 s
  `heartbeat` (`:144, :168`). Nothing re-validates the bearer. `ValidateToken`
  (`core/auth/auth.go:26`) does check revocation (`:37`), expiry (`:40`) and actor status (`:43`),
  but only at connect.
- **N-15.** `workspaceMiddleware` (`service/internal/gateway/middleware.go:93`) calls
  `servicePool.Get` (`:114`) before `authMiddleware` (`routes.go:56-57`). A `Get` failure answers
  500 (`middleware.go:116`). `authMiddleware` (`:168`) validates the bearer only after. So the
  ServiceSet is built for an unauthenticated caller, and the 500/401 split is an oracle.
- **N-16.** `resolveRuntimeEnv` (`service/cmd/spine/serve_config.go:179-186`) maps any value that
  is not exactly `production`/`staging`/`development` to `""`, and `""` takes the non-production
  path everywhere (`ProductionStrict` `:71`, the `SMP_ADMIN_TOKEN` and secret-key gates, the
  subscription-secret sweep `subscription_secrets.go:137`). `guardGitHTTPDevNoAuth`
  (`serve_config.go:227`) already treats unset as not-development; nothing else requires an
  explicit value.
- **N-17.** The evidence querier reads `run.BranchName` first for non-terminal runs
  (`core/evidence/querier.go:146-149` `RefsForRun`), and the loader parses it with plain
  `yaml.Unmarshal` (`core/evidence/loader.go:126`). `.spine/runs/<run>/evidence/*.yaml` on the run
  branch is author-writable. `run.status` shows it (wired at `service/cmd/spine/serve_wiring.go`
  via `EvidenceQuerier`), with no "unverified" marker.

## Design

1. **N-14: revalidate on each heartbeat, close on revocation.**
   - On the `heartbeat` tick (`handlers_events_stream.go:168`), before writing the heartbeat,
     re-run `authSvc.ValidateToken` against the stream's original bearer (captured at connect).
   - A failure (revoked, expired, suspended actor) closes the stream: a final comment frame and
     return. The client reconnects and gets the connect-time 401.
   - The bearer is captured once; the stream holds no long-lived store ref (it already releases
     the pool ref at `:136`), so revalidation uses `authSvc` captured at connect, re-resolved per
     tick to pick up an evicted workspace's rebuilt services.
   - The heartbeat interval (30 s) is the revalidation interval. Owner question 1 covers a shorter
     one.
2. **N-15: authenticate before building the workspace.**
   - Swap the middleware order so the bearer shape is checked before `servicePool.Get`.
     `authMiddleware` already tolerates a deferred workspace-resolution error (`middleware.go:168`
     comment) and collapses to a uniform 401. The change is to not call `servicePool.Get` until a
     well-formed bearer is present.
   - Concretely: `workspaceMiddleware` resolves the workspace config (cheap, no ServiceSet) and
     stashes any error, as today. The ServiceSet build (`servicePool.Get`) moves after
     `authMiddleware` validates the bearer — either into `authMiddleware`'s tail or into a thin
     third middleware that runs only for authenticated requests.
   - A ServiceSet build failure for an authenticated caller keeps its 500; for an
     unauthenticated caller there is no build, so no oracle.
3. **N-16: require an explicit, known `SPINE_ENV`.**
   - `spine serve` and `spine secrets` refuse to start when `SPINE_ENV` is unset or not one of
     `production`/`staging`/`development`, with a message naming the three values.
   - `resolveRuntimeEnv` keeps mapping to the normalized value; a new `requireRuntimeEnv` at the
     command entry is the gate.
   - Owner question 2 covers whether unset should mean production rather than refuse.
4. **N-17: label branch-sourced evidence as unverified.**
   - `RefsForRun` keeps probing the run branch for active runs (operators rely on it), but the
     summary records the source. `RepositorySummary` gains `SourceRef string` and
     `Verified bool`: `Verified` is true only when the evidence was read from the server-written
     authoritative branch, false when it came from the run branch.
   - `run.status` renders unverified repository evidence with an explicit `unverified` marker, so
     a human reviewer is not shown author-writable content as fact.
   - The merge gate is unchanged: it never trusted this display path.

## SMP impact

- **None.** SMP embeds no part of `service/internal/gateway`, `service/cmd/spine` or
  `core/evidence`, and runs no standalone `spine serve`. The task is standalone-only; the SMP gate
  re-runs and is expected inert.
- N-17's `RepositorySummary` fields are additive (`omitempty`); any SMP code that unmarshalled the
  summary still works.

## Decisions

1. **Revalidate on the existing heartbeat, not on a new timer.** The heartbeat already wakes the
   loop every 30 s; reusing it adds no goroutine and bounds the post-revocation window to one
   interval. Proactive close via an event bus is a larger change (out of scope).
2. **Fail closed on `SPINE_ENV`.** An unset value is the exact gap the review flagged
   (`N-16`), and INIT-023 already treats unset as not-development for the git bypass. Requiring it
   makes every production gate deliberate. Owner question 2 offers the softer "unset = production".
3. **Keep reading run-branch evidence, but mark it.** Operators use in-flight evidence for active
   and partially-merged runs (`querier.go` comment). Removing the read would blind them; marking
   it unverified keeps the information and removes the false authority.

## Owner questions

1. **Revalidation interval.** *Default:* the 30 s heartbeat. *Alternative:* a separate shorter
   interval for revocation (for example 10 s) at the cost of a second timer.
2. **Unset `SPINE_ENV`.** *Default:* refuse to start. *Alternative:* treat unset as `production`
   (safe-by-default) rather than refusing, so an operator who forgets it gets the strict path, not
   a boot failure.
3. **N-17 marker shape.** *Default:* a boolean `verified` plus `source_ref` on each repository.
   *Alternative:* a top-level `evidence_trust` enum on the run summary.

## Acceptance Criteria

1. **Regression: N-14** (`service/internal/gateway`, `TestEventStream_ClosesOnRevocation`). This
   ports `scratch/standalone/sa3/zz_sa3_sse_revocation_test.go`: an open stream whose token is
   revoked (and a variant whose actor is suspended) closes at the next heartbeat; a fresh request
   with the same token gets 401. At `a560c210` the stream keeps delivering.
2. **N-15 no pre-auth build** (`TestMiddleware_NoServiceSetForUnauthenticated`). An unauthenticated
   request in shared mode never calls `servicePool.Get` (asserted through a counting pool), and a
   missing workspace and a build-failing workspace both answer the uniform 401. At `a560c210` the
   build runs and the codes differ.
3. **N-15 authenticated still works** (`TestMiddleware_AuthenticatedBuildsServiceSet`). A valid
   bearer builds the ServiceSet and reaches the handler; a build failure for an authenticated
   caller is 500.
4. **Regression: N-16** (`service/cmd/spine`, `TestServe_RequiresKnownSpineEnv`). Unset and a
   typo'd `SPINE_ENV` each refuse to start, naming the three values; each known value starts. At
   `a560c210` unset starts on the non-production path.
5. **N-17 provenance** (`core/evidence`, `TestRunSummary_BranchEvidenceMarkedUnverified`). Evidence
   read from the run branch has `Verified == false` and `SourceRef == run.BranchName`; evidence
   from the authoritative branch has `Verified == true`. `run.status` renders the unverified
   marker (`service/internal/gateway` `TestRunStatus_ShowsUnverifiedEvidence`). At `a560c210`
   there is no marker.
6. **Existing suites.** `go test ./service/... ./core/evidence/...` and the scenario suite pass,
   except the known pre-existing failures.
7. **SMP compatibility gate** (initiative §5) passes with no SMP change (inert; additive summary
   fields).
8. **Documentation.** `security-model.md` (SSE revalidation; evidence provenance),
   `docs/integration-guide.md` (`SPINE_ENV` now required; the env-var table),
   `architecture/execution-evidence.md` (unverified marker) and `CHANGELOG.md`.

## Compatibility

- Standalone only.
- **Boot:** `SPINE_ENV` is now required (owner question 2 may soften this). The CHANGELOG and the
  env table say so, and the shipped compose files set it (initiative §7a lists the compose
  overlap with TASK-009).
- **Wire:** `RepositorySummary` gains two `omitempty` fields; `run.status` shows an unverified
  marker on branch-sourced evidence.

## Out of scope

- Proactive SSE close through an event bus (Decision 1).
- Reading evidence only from the server ref (that would blind operators; N-17 marks instead).
- SMP's own session handling.

## File overlaps

- `service/cmd/spine/serve_config.go`: this task touches `resolveRuntimeEnv`/`requireRuntimeEnv`;
  TASK-006 touches `parseGitHTTPTrustedCIDRs`. Different functions.
- `docker-compose*.yaml`: this task sets `SPINE_ENV=development`; TASK-009 sets image digests.
  Different keys.
- `service/internal/gateway/middleware.go`, `handlers_events_stream.go`, `core/evidence/*`:
  this task only.

---

## Implementation Notes

*(Implementation, 2026-10-23. Branch `init/INIT-024-TASK-007-session-and-auth-hygiene`, cut from
`init/INIT-024-scaffold` `12376b55` (= `a560c210` + the INIT-024 scaffold; no code difference
outside `initiatives/`). macOS (APFS), git 2.54.0 (Apple), go 1.26.8, PostgreSQL 18.6 in `i23-pg`;
the git-floor job in the local `golang:1.26-bookworm` image (git 2.39.5). The design's line
numbers held at `12376b55`.)*

### Owner questions (defaults, pending the owner)

| Question | Default taken | Where |
|---|---|---|
| OQ1 revalidation interval | The 30 s heartbeat; no second timer | `handleEventStream` heartbeat branch, `sseRevalidator` |
| OQ2 unset `SPINE_ENV` | Refuse to start (not "unset = production") | `requireRuntimeEnv` in `serveCmd` and `runSecretsReencrypt` |
| OQ3 N-17 marker shape | `verified` (bool, always serialized) + `source_ref` per repository; additionally a run-level `unverified_repositories` list (additive; not the `evidence_trust` enum alternative) | `core/evidence/summary.go` |

### What was built

| Commit | Change |
|---|---|
| `e4d33de3` | **N-14** `handleEventStream`: `newSSERevalidator` captures the stream's bearer (re-read from its own `Authorization` header), its auth service (`authFrom`: the ServiceSet's in shared mode) and actor id before any header is written; every heartbeat tick runs `sseRevalidator.check` (`auth.ValidateToken` + same actor id + `auth.Authorize(events.read)`, 5 s timeout) before the keepalive and on any error writes `: reauthentication required` and returns. `Server.sseHeartbeat` (0 = 30 s) + `export_test.go` `SetSSEHeartbeatForTest`. **N-15** `workspaceMiddleware` resolves the config only; `authMiddleware` sends shared mode (`servicePool` + `wsResolver`) to `authenticatePooled`: bearer shape (no I/O) → unresolved workspace → `acquireServiceSet` (the former Get/release block) → the ServiceSet's own `Auth.ValidateToken` → membership check; every refusal before validation is `writeUniformUnauthorized` (one status, one body) and its cause is logged. `/git`: `validateGitAuth`'s shared-mode path is `validatePooledGitAuth` (no fallback to the nil process-level auth; uniform 401 for a build failure, a missing auth service, a store error and every token state). Single mode is unchanged (`bearerFromRequest` keeps its messages). **N-16** `requireRuntimeEnv` (serve_config.go) gates `serveCmd` (right after the git floor) and `runSecretsReencrypt`; `docker-compose.yaml` sets `SPINE_ENV=development`. **N-17** `RepositorySummary.SourceRef`/`Verified`, `RunSummary.UnverifiedRepositories`; `BuildSummary` → `buildSummary(..., authoritative, ...)`, `Querier.SummarizeForRun` passes its primary branch; `spine run inspect` prints `UNVERIFIED (read from <ref>, author-writable)` and a headline count. Tests below; dependent tests updated (see "Existing tests that change"). |
| `f2b8666f` | Docs: `architecture/security-model.md` (§3.5 session table, long-lived streams, shared-mode pre-authentication; §6.3 actor-written records), `architecture/execution-evidence.md` §6.4 output shape + new §6.4.5, `docs/integration-guide.md` (`SPINE_ENV` row now **Yes**, §2.1 production gate, §6.2 SSE revalidation), README, `.env.example`, `api/spec.yaml` (`RunStatusResponse.evidence`), `KNOWN-LIMITATIONS.md` (new "Session and Auth Hygiene" section, two existing bullets corrected), `CHANGELOG.md`. |
| `a197c9f7` | Self-review fix: `describePreauthErr` withholds a `pgconn.ParseConfigError` (which quotes the workspace connection string, query-parameter password unredacted) in the new pre-auth Warn lines on both paths, plus `TestPreauthLog_WithholdsConnectionString`; README quickstart and the reencrypt runbook set `SPINE_ENV`; two stale comments in the pooled bootstrap scenario. |
| (this commit) | These notes; status In Progress; one KNOWN-LIMITATIONS bullet dropped (see adversarial review, item 2). |

No migration, schema, table, column, route, event or Go interface change. `core/evidence` gains
three exported struct fields (additive; SMP does not import `core/evidence`); every other changed
Go file is under `service/` (`service/internal/...` and `service/cmd/spine` cannot be imported by
SMP, and SMP's `dev/lint-spine-internals` forbids `service/...` and `cmd/...`).

### Per-AC evidence

"Red at the base" = the branch's new test file copied into a `git archive init/INIT-024-scaffold`
extracted under scratch `t007/base` (since cleaned of the copies; re-create with `git archive
init/INIT-024-scaffold | tar -x -C <dir>`). Logs are in SMP `tmp/init027-orchestration/i24/t007/`.

1. **Regression N-14** — `TestEventStream_ClosesOnRevocation`
   (`service/internal/gateway/sse_revalidation_test.go`), the SA-3 PoC ported: a real `httptest`
   server, heartbeat 40 ms, a race-safe auth store. Subtests: actor suspended (the PoC sequence:
   a fresh request is 401, then the open stream must close at the next heartbeat and the PoC's
   `evt-after-suspension` must never arrive), token revoked, token expired, token store failing
   (fail closed). Each asserts the closing frame, that an event broadcast after the close is not
   delivered, and that a fresh request and a reconnect with `Last-Event-ID` get 401. Plus
   `TestEventStream_SharedModeClosesOnRevocation` (through a real `ServicePool`; the stream
   revalidates against the ServiceSet's auth after releasing its pool ref) and
   `TestEventStream_ValidBearerStaysOpen` (a valid heartbeat-only stream keeps its keepalives and
   events). **Red at the base** (`red-n14-base.log`; `SetSSEHeartbeatForTest` replaced by a no-op
   shim, `base-shims/export_test.go`, since the base has no knob): every revocation variant fails
   "stream still open 2s after the bearer stopped being valid", shared mode too. **Green on the
   branch** under `-race` (`green-n14.log`). The raw PoC (`-overlay`) still passes on the branch:
   it broadcasts immediately after the suspension, inside the one-interval window the design
   accepts (owner question 1); the committed test asserts the AC.
2. **N-15 no pre-auth build** — `TestMiddleware_NoServiceSetForUnauthenticated`
   (`preauth_serviceset_test.go`): shared mode through the real routes, a counting resolver behind
   a real `ServicePool` (its `Resolve` count = `servicePool.Get` calls) and a counting Builder.
   Seven malformed `Authorization` values × eight workspace states (healthy, build-failing,
   flaky, no auth service, token-store error, missing, inactive, no header) → all 401 with a
   byte-identical body, `Get` = 0, builds = 0. Then a junk bearer for every state, revoked /
   expired / suspended-actor tokens, and a token valid in ws-ok sent to the other states: the same
   body. **Red at the base** (`red-n15-base.log`): build-failing workspaces answer `500 "failed to
   initialize workspace services"`, the auth-less one `503 "authentication not configured"`, the
   bodies differ (`invalid token`, `authorization header required`, ...), and `servicePool.Get ran
   35 times for requests without a well-formed bearer`. **Green.**
3. **N-15 authenticated** — `TestMiddleware_AuthenticatedBuildsServiceSet`: a valid bearer builds
   once, reaches the handler (200), and the second request reuses the set. A build failure for a
   caller holding a token valid there is the uniform 401 (**deviation 1**); repairing the build,
   the same token gets 200. **Red at the base:** the build failure is 500.
   `/git` half (adversarial review): `TestGitAuth_SharedModeNoPreAuthOracle` — no bearer never
   builds; junk / valid-elsewhere / revoked / expired / suspended bearers against build-failing,
   auth-less, store-erroring, missing and inactive workspaces all get one 401 body. **Red at the
   base:** 503 (build failure, no auth service), 500 (store error), `invalid token` vs the uniform
   message. `TestPreauthLog_WithholdsConnectionString`: fails under a mutation that logs
   `err.Error()` (`mut/`).
4. **Regression N-16** — `TestServe_RequiresKnownSpineEnv` (`service/cmd/spine/spine_env_test.go`):
   `serveCmd().RunE` with every production requirement met and an unbindable listen port (`-1`,
   so no test ever binds a real port). Eleven unset/blank/mistyped values (`""`, `"   "`, `prod`,
   `prodution`, `Production1`, `live`, `dev`, `test`, `production,staging`, `production​`,
   `staging\x01`) are refused naming production, staging and development; seven accepted
   spellings of the three values (case, surrounding whitespace) run every startup step and stop at
   the listener (`server failed to start`). `TestSecretsReencrypt_RequiresKnownSpineEnv`: the same
   refusal values stop `spine secrets reencrypt` before the key ring. `TestRequireRuntimeEnv`
   (`spine_env_unit_test.go`): the table, plus a bounded echo of a 4 KiB value. **Red at the base**
   (`red-n16-base.log`, 22 failures): every refused value started — `server failed to start:
   listen tcp: address -1: invalid port`, i.e. it passed every gate on the non-production path —
   and reencrypt went on to its workspace-id check. **Green** (`green-n16.log`).
5. **N-17 provenance** — `TestRunSummary_BranchEvidenceMarkedUnverified`
   (`core/evidence/provenance_test.go`): eight cases — active run with run-branch-only evidence
   (`Verified=false`, `SourceRef=run.BranchName`, listed in `UnverifiedRepositories`), both refs
   (the run-branch copy is shown, unverified), merged to main (verified), completed on main
   (verified), failed with a preserved branch (unverified), `WithPrimaryBranch("trunk")`, a run
   branch named `main` (never verified), no evidence (`Present=false`, unverified, no source); the
   JSON always carries `"verified"`. `TestBuildSummary_ProvenanceUsesDefaultAuthority`: a direct
   caller reading `HEAD` gets unverified. Gateway: `TestRunStatus_ShowsUnverifiedEvidence` — a
   real `evidence.Querier` behind `GET /api/v1/runs/{id}`: run-branch evidence `verified:false`
   with the branch as `source_ref`, main evidence `verified:true`, `unverified_repositories:
   [billing]`. CLI: `TestInspectRun_ShowsUnverifiedEvidence` / `..._NoProvenanceFieldsNoMarker`.
   **Red at the base:** the core test does not compile (`r.Verified undefined`,
   `red-n17-core-base.log`); the gateway test reports `verified = <nil> (present=false)`,
   `source_ref = <nil>`, `unverified_repositories = <nil>` (`red-n17-gw-base.log`); the CLI test
   finds no marker (`red-n17-cli-base.log`). **Green.**
6. **Existing suites** — see Gates.
7. **SMP compatibility gate** — see below; inert, no SMP change.
8. **Documentation** — `security-model.md`, `docs/integration-guide.md`, `execution-evidence.md`,
   `CHANGELOG.md` as listed under `f2b8666f`, plus README, `.env.example`, `api/spec.yaml` and
   `KNOWN-LIMITATIONS.md`.

### Existing tests that change

- `TestP330_ServeRefusesGitDevNoAuthOutsideDevelopment` drops its `SPINE_ENV=""` case: an unset
  value is now refused one gate earlier (covered by `TestServe_RequiresKnownSpineEnv`);
  `TestP330_GuardGitHTTPDevNoAuth` still covers the guard's own unset branch.
- `spine secrets reencrypt` tests (`reencryptEnv`, `clearSecretBackendEnv`,
  `TestSecretsReencrypt_RequiresKeyAndTouchesNothing`, `..._SharedResolverNeedsAllWorkspaces`) set
  `SPINE_ENV=development` instead of `""` — the same non-production path, now explicit.
- The internal SSE tests `TestP329_SSELimiter_KeyedPerWorkspace` and
  `TestP328_UnsafeSSEFieldWarnIsRateLimited` call `handleEventStream` with a hand-built
  authenticated context; a stream now refuses to open without the bearer and auth service
  authMiddleware leaves behind, so they get both (`sse_stream_auth_internal_test.go`).

### Deviations and additions (with reasons)

1. **AC 3's "build failure for an authenticated caller is 500" is unreachable; it is the uniform
   401.** In shared mode the bearer can only be validated against the workspace's own token store,
   which is part of the ServiceSet (`buildServiceSet` builds `ss.Auth` from the workspace
   database; `spine serve` leaves the process-level auth nil in shared mode). A caller whose
   workspace fails to build is therefore never authenticated, and validating against any other
   service would check one tenant's bearer against another's store. The epic AC states exactly
   this ("a ServiceSet build failure is a uniform 401 for any caller the build left
   unauthenticated"). The cause is logged at `Warn` with the workspace id. The test proves the 401
   met a valid bearer (200 after repair).
2. **The ServiceSet build sits inside `authMiddleware`, between the shape check and validation**,
   not after validation (the design's "authMiddleware's tail or a thin third middleware"): the
   validation needs it. The epic AC's bar — no build without a well-formed bearer — holds; a
   well-formed junk bearer may still build (KNOWN-LIMITATIONS residual).
3. **The stream's auth service is captured once, not re-resolved per tick.** The handler releases
   its pool ref before the loop, and an eviction fires `ss.Done`, which closes the stream; the
   reconnect meets the rebuilt services. Re-acquiring a pool ref every 30 s would rebuild an
   idle-evicted workspace just for a heartbeat. A tick racing an eviction finds the store closing
   and closes the stream (fail closed).
4. **Revalidation also re-runs `auth.Authorize(events.read)` and pins the actor id**, and a check
   that cannot complete (store error, 5 s timeout) closes the stream: the stream's permission is
   the connect-time one, re-proved each tick.
5. **Every shared-mode pre-auth 401 is one body** (missing/malformed header, every token state),
   not only the build-failure case: the per-state messages answered only for an existing
   workspace, the same oracle (TASK-006 did the same on `/git`). Single mode keeps its messages.
6. **Addition: `/git` shared mode** (`validatePooledGitAuth`, adversarial review item 1).
7. **Addition: `unverified_repositories`** at the run level, because the `status` rollup counts
   unverified records; and the CLI marker (`spine run inspect`), the only other surface that
   renders evidence.
8. **Addition: the pre-auth logs withhold connection-string parse errors** (`a197c9f7`).
9. `.env.example` keeps `# SPINE_ENV=development` commented: a copy-pasted default would defeat
   the gate. The shipped `docker-compose.yaml` sets it (`docker-compose.test.yaml` starts no Spine
   server; CI starts none either, so no CI or scenario setup needed it).

### Adversarial self-review

1. **Pre-auth 500/503 vs 401 on any route.** `/api/v1` shared mode: closed (AC 2). `/git` shared
   mode: a junk bearer got 503 for a build-failing or auth-less workspace and 500 for a store
   error — closed (`validatePooledGitAuth`, test above). Operator routes check the operator token
   first; the binding-invalidation webhook checks its bearer first; `/system/health` takes no
   workspace. Timing still differs (documented residual).
2. **Found, not fixed here:** with receive-pack off, an unauthenticated push answered the
   operator-guidance 403 for an existing workspace and the uniform 401 for a missing one (INIT-008
   TASK-031). No build is involved, and TASK-006's review round 1 (`c69e4e7b`, its branch) already
   makes that 403 authenticated-only, so the bullet first written for KNOWN-LIMITATIONS was dropped.
3. **A stream kept alive another way.** Heartbeat-only streams revalidate on the same tick
   (`ValidBearerStaysOpen` proves the tick runs without events). A reconnect, with or without
   `Last-Event-ID`, meets authMiddleware (asserted). No other streaming or long-poll endpoint
   exists (`http.Flusher`/`NewResponseController` appear only in the stream handler; `/events`
   is a plain read). An event flood cannot starve the tick: `select` picks among ready cases at
   random and the ticker keeps its tick. A non-reading client hits the 5 s write deadline.
   Expired tokens and lost permissions close too. Residual: events inside the interval.
4. **Production with dev gates off by a `SPINE_ENV` spelling.** Every accepted spelling maps to
   exactly one of the three words (ASCII case + `TrimSpace`); a zero-width space, a control
   character, `prod`, a list, `Production1` are refused (table). `ProductionStrict`,
   `PrepareSubscriptionSecrets` and every gate read the same variable after the gate. `spine
   migrate` applies no environment gate. Only an explicit `staging`/`development` turns them off.
5. **Author-written evidence shown as server-verified elsewhere.** `run.status` (JSON and `spine
   run status`) and `spine run inspect` are the only renderers; the validation EV-* rules have no
   production `EvidenceResolver` wired; `step_output_evidence` is server-computed; the headline
   `status` now travels with `unverified_repositories`.
6. **Secrets in the new logs** — found and fixed (`a197c9f7`).

### Gates

All on the branch head before this commit (`a197c9f7` + the `validateGitAuth` comment refresh in
this commit), logs in scratch `t007/`. Databases `s24007_*` only, dropped afterwards (the
ws_* database and role counts were 0 before and after the SMP suites).

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | OK |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; "engine constructed and exercised; library surface is sound" (`bin/` removed afterwards) |
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario` | all clean |
| `go test -p 4 ./...` | 40 packages ok, 0 failures (`gate-go-test-all-final.log`). The first run found two internal SSE tests that open a stream without a bearer (see "Existing tests that change"); fixed before the commit. |
| `go test -race -p 4` on `service/internal/gateway`, `service/internal/cli`, `service/cmd/spine`, `core/evidence` | ok |
| Integration (`-tags integration`) of the four touched packages, `SPINE_TEST_DATABASE_URL` | ok (`ci-br/integration-touched.log`) |
| scenario-git-http job, every step of ci.yml (`ci.sh`) | `TestGitHTTP_*` 87 passes / 0 skips (Push passed); `TestBootstrapInternalAdmin_*` 26 / 0 (both named tests PASS — the pooled one drives the new authMiddleware ordering); `TestWorkflowLifecycle_*` 130 / 0; the workflow-policy, step-CAS, step-evidence, status-lifecycle and governance steps all pass with 0 skips; plus the cross-repo evidence scenarios (`-run Evidence`) 57 / 0 |
| Full scenario suite with a migrated registry, branch vs base (`scen.sh`) | identical failure sets at both: 31 top-level, 59 including subtests (the known pre-existing set; also identical to TASK-006's base run) |
| git-floor job in `golang:1.26-bookworm` (`--pull=never`, git 2.39.5) | both CI steps ok, plus this task's tests on Linux ok (`gitfloor/out.log`); container removed |
| golangci-lint v2.11.4 on touched packages (+ scenarios) vs base | 0 new issues (branch 145, base 147: two pre-existing gofmt findings fixed; the pre-existing `rangeValCopy` in `classify` now reports 280 instead of 256 bytes); `--enable-only=gosec` 0 / 0. Two findings in new test files were fixed (a literal U+200B, an embedded-field selector). |
| gofmt on every changed Go file | clean |

### SMP compatibility gate

`i23/s24-T007/smp` (SMP `3addd44b`, `../spine` = this worktree, clean at `a197c9f7`), `smp.sh`:
`go build ./...` and `go vet ./...` plain, `-tags integration`, `-tags scenario ./scenariotest/...`
and `-tags faultinject`: all clean. `go test -race -p 4` on the initiative §5 packages: every
package ok except `internal/customer/gitread`'s `TestClient_ErrorContractEqualsCLIClient` and
`TestClient_SpineReadFileRendersTheGitlinksPatch` — the two by-design gitread contract tests, and
they fail identically with SMP against the scaffold base (`go test -modfile` with `replace =>
t007/base`; `smp-base/gitread-base.log`). Testgate Spine-runtime leg: 4 packages, 2143 passed,
0 failed, 3 skipped (all allowed); githttp leg: 588 passed, 1 allowed skip. **Inert, as
expected:** SMP imports no changed package (`core/evidence` has no SMP importer;
`service/...`/`cmd/...` are forbidden by SMP's `dev/lint-spine-internals`), and N-16 changes no
shared env helper — `requireRuntimeEnv`/`resolveRuntimeEnv` live in `service/cmd/spine` (package
main); `core/auth`'s `SPINE_ENV` mention is a comment.

### Codex

`codex review --base init/INIT-024-scaffold`, plain, from the worktree; model confirmed
`gpt-6.1-sol` in each review sub-session (`source {"subagent":"review"}`, cwd = this worktree).

| Round | Head | Review session | Findings |
|---|---|---|---|
| 1 | `f2b8666f` | `01a169f1-0557-7e40-8889-825ea10e32ef` | none ("patch is correct", 0.87) |
| 2 | `a197c9f7` | `01a169f5-2ba1-70b0-9c55-9331337e4149` | none ("patch is correct") |

The one real defect found after round 1 came from the self-review (the credential in the new
pre-auth log line, `a197c9f7`), not from codex.

### Not run

- The SMP gate's full `ci.yml` Platform-DB suites (only the Spine-runtime and githttp testgate legs
  the rules name were run): `cd i23/s24-T007/smp && go run ./dev/testgate -allow-skip
  'TEST_AWS_OBJECT_STORE_' -- -race -count=1 -timeout 20m <the ci.yml package list>`.
- GitHub CI on a pushed commit (nothing is pushed; INIT-024 owner question 1).

### Reconciliation for integration (shared files)

- `service/internal/gateway/handlers_git.go` with TASK-006: TASK-006 rewrites `handleGit`'s auth
  gate and adds `isUnauthorizedError` right after `validateGitAuth`; this task changes
  `validateGitAuth`'s body and adds `validatePooledGitAuth` at the same spot — keep both functions.
  The two compose: TASK-006's push-off path now goes through the gate, and shared mode reaches
  `validatePooledGitAuth`. TASK-006's KNOWN-LIMITATIONS residual "a server without any auth service
  tells workspaces apart … still answers 503" no longer holds in shared mode (it is the uniform
  401 here); narrow it to single mode.
- `service/cmd/spine/serve_cmd.go` and `serve_config.go` with TASK-006: different functions and
  hunks (this task: the gate right after the git floor, `requireRuntimeEnv` after
  `resolveRuntimeEnv`).
- `docker-compose.yaml` with TASK-009: this task adds `SPINE_ENV=development` to the `spine`
  service's environment; TASK-009 pins `image:` lines.
- `CHANGELOG.md`, `KNOWN-LIMITATIONS.md`, `README.md`, `.env.example`, `docs/integration-guide.md`
  with TASK-006: separate sections; the CHANGELOG entries stack at the top.
