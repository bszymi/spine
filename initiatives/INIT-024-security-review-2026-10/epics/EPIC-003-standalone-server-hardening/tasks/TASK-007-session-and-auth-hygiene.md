---
id: TASK-007
type: Task
title: "Session and auth hygiene: SSE revalidation, no pre-auth ServiceSet, explicit SPINE_ENV, evidence provenance"
status: Completed
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-11-04
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
init/INIT-024-scaffold | tar -x -C <dir>`). Logs are in SMP `local-scratch/i24/t007/`.

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
| 3 | `15abd1ea` (notes commit) | `01a16a2d-ee9d-7583-a777-c2006606369b` | none ("patch is correct") |

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
  The production code composes (TASK-006's push-off path goes through the gate, and shared mode
  reaches `validatePooledGitAuth`), but **TASK-006's tests do not, as they stand** (review round 1,
  finding 1; corrected — an earlier version of this note said "the two compose"): its shared-mode
  fixture sets the process-level `f.srv.auth` on a pooled server, which shared mode no longer
  consults, so three tests fail after the merge (`TestHandleGit_InvalidBearerIsNoWorkspaceOracle`
  shared leg, `TestHandleGit_TrustedCIDRScopedToWorkspace/shared_mode`,
  `TestHandleGit_PushNeverTakesWorkspaceBypass`: "valid bearer … status 401; want 200"). Apply the
  fixture change below as a coordination commit on the integration branch (see "Review round 1",
  ruling 1, for the trial merge that proves it). TASK-006's KNOWN-LIMITATIONS residual "a server
  without any auth service tells workspaces apart … still answers 503" no longer holds in shared
  mode (it is the uniform 401 here); narrow it to single mode.
- `architecture/security-model.md` and `CHANGELOG.md`: this task's N-15 text says the `/git`
  missing/malformed-header and push-off cases "are made uniform by INIT-024 TASK-006". Once both
  are merged, drop that qualifier (state the `/git` uniformity without it).
- `service/cmd/spine/serve_cmd.go` and `serve_config.go` with TASK-006: different functions and
  hunks (this task: the gate right after the git floor, `requireRuntimeEnv` after
  `resolveRuntimeEnv`).
- `docker-compose.yaml` with TASK-009: this task adds `SPINE_ENV=development` to the `spine`
  service's environment; TASK-009 pins `image:` lines.
- `CHANGELOG.md`, `KNOWN-LIMITATIONS.md`, `README.md`, `.env.example`, `docs/integration-guide.md`
  with TASK-006: separate sections; the CHANGELOG entries stack at the top.

#### TASK-006 fixture change for the integrator (`service/internal/gateway/git_trusted_cidr_scope_internal_test.go`, against `96dcea55`)

```diff
diff --git a/service/internal/gateway/git_trusted_cidr_scope_internal_test.go b/service/internal/gateway/git_trusted_cidr_scope_internal_test.go
index 7eebf31d..50a48181 100644
--- a/service/internal/gateway/git_trusted_cidr_scope_internal_test.go
+++ b/service/internal/gateway/git_trusted_cidr_scope_internal_test.go
@@ -109,6 +109,9 @@ type t006Fixture struct {
 	srv      *Server
 	resolver *t006Resolver
 	pool     *workspace.ServicePool
+	// wsAuth is, in shared mode, every workspace's own (ServiceSet) auth
+	// service; see setAuth.
+	wsAuth *auth.Service
 }
 
 // t006Server builds a gateway serving ws-a and ws-b with the given
@@ -131,13 +134,37 @@ func t006Server(t *testing.T, trusted map[string][]string, shared, receivePack b
 		resolver: resolver,
 	}
 	if shared {
-		f.pool = workspace.NewServicePool(context.Background(), resolver, workspace.PoolConfig{IdleCheckInterval: -1})
+		f.pool = workspace.NewServicePool(context.Background(), resolver, workspace.PoolConfig{
+			IdleCheckInterval: -1,
+			// Each workspace authenticates against its own ServiceSet's
+			// auth service, as in spine serve: shared mode never falls
+			// back to the process-level one (INIT-024 TASK-007).
+			Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
+				ss.Auth = f.wsAuth
+				return nil
+			},
+		})
 		t.Cleanup(f.pool.Close)
 		f.srv.servicePool = f.pool
 	}
 	return f
 }
 
+// setAuth installs the auth service a test's bearers validate against:
+// the server's own in single mode; in shared mode every workspace's
+// ServiceSet's, which is what spine serve checks a /git bearer against
+// (built ServiceSets are evicted so the next request picks it up).
+func (f *t006Fixture) setAuth(authSvc *auth.Service) {
+	if f.pool == nil {
+		f.srv.auth = authSvc
+		return
+	}
+	f.wsAuth = authSvc
+	for id := range f.resolver.cfgs {
+		f.pool.Evict(id)
+	}
+}
+
 // do routes target through the real /git routes (chi, as mounted by the
 // gateway) from remoteAddr, with headers as name, value pairs.
 func (f *t006Fixture) do(method, target, remoteAddr string, headers ...string) *httptest.ResponseRecorder {
@@ -202,7 +229,7 @@ func TestHandleGit_TrustedCIDRScopedToWorkspace(t *testing.T) {
 			}
 			// A bearer still reads any workspace the token admits.
 			authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
-			f.srv.auth = authSvc
+			f.setAuth(authSvc)
 			if w := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, runnerA, "Authorization", "Bearer "+bearer); w.Code != http.StatusOK {
 				t.Errorf("ws-a's runner with a bearer, ws-b: status %d; want 200", w.Code)
 			}
@@ -297,7 +324,7 @@ func TestHandleGit_PushNeverTakesWorkspaceBypass(t *testing.T) {
 	}
 	// With a bearer the push advertisement is served.
 	authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
-	f.srv.auth = authSvc
+	f.setAuth(authSvc)
 	if w := f.do(http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", runnerA, "Authorization", "Bearer "+bearer); w.Code != http.StatusOK ||
 		w.Header().Get("Content-Type") != t015ReceivePackAdvertisement {
 		t.Errorf("ws-a's runner with a bearer, push advertisement: status %d (%s); want 200 %s", w.Code, w.Header().Get("Content-Type"), t015ReceivePackAdvertisement)
@@ -306,7 +333,7 @@ func TestHandleGit_PushNeverTakesWorkspaceBypass(t *testing.T) {
 	// With receive-pack off a token-less push from the bound runner is
 	// the uniform 401; with a bearer it is the 403 naming the flag.
 	off := t006Server(t, t006Bindings, true, false)
-	off.srv.auth = authSvc
+	off.setAuth(authSvc)
 	if w := off.do(http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", runnerA); w.Code != http.StatusUnauthorized {
 		t.Errorf("ws-a's runner, push off, no token: status %d %q; want 401", w.Code, w.Body.String())
 	}
@@ -482,7 +509,7 @@ func TestHandleGit_InvalidBearerIsNoWorkspaceOracle(t *testing.T) {
 	for _, shared := range []bool{true, false} {
 		f := t006Server(t, t006Bindings, shared, false)
 		authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
-		f.srv.auth = authSvc
+		f.setAuth(authSvc)
 		for _, peer := range []string{t006Outsider, t006RunnerA} {
 			for _, h := range []string{"Bearer junk", "Basic eDp5", "Bearer ", "bearer", "Token " + bearer} {
 				ref := f.do(http.MethodGet, "/git/ws-missing/"+t006UploadInfoRefs, peer+":40000", "Authorization", h)
```

### Review round 1 (2026-10-25; 1 P2, 6 P3; rulings by the coordinator)

Head after the round: `9bf1e072` + this notes commit. Commits: `26889778` (finding 2), `160573e8`
(finding 4), `f2e729d1` (findings 3, 5, 6, 7), `9bf1e072` (finding 1's comment). Deviations 1 and
2 (AC 3's 500 → the uniform 401; AC 2 literal for a well-formed junk bearer) stand as documented;
OQ1–OQ3 stay pending the owner.

1. **P2 — TASK-006's tests after the merge.** Trial merge of TASK-006 `96dcea55` into `f2e729d1`
   in a throwaway detached worktree under scratch (removed afterwards; no branch created or
   touched): conflicts only in `handlers_git.go` (keep both functions) and `CHANGELOG.md` (stack
   both entries); builds and vets. Without the fixture change, `go test ./service/internal/gateway/...`
   fails three TASK-006 tests (`merge-gw-before.log`), all because the shared fixture sets
   `f.srv.auth` on a pooled server. With the change above (each shared fixture's ServiceSets get
   the auth service through the pool Builder, `setAuth` in place of the four `f.srv.auth =`
   lines), the package is green under `-race` (`merge-gw-after.log`), both legs of
   `TestHandleGit_InvalidBearerIsNoWorkspaceOracle` pass, `go test ./service/... ./core/evidence/`
   is green (`merge-service-unit.log`), and the scenario-git-http job on the merge passes every
   step with 0 skips (`TestGitHTTP_*` 90, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*`
   130; `ci-merge.out`). The exact diff is in the Reconciliation section; the note's "the two
   compose" claim is corrected there and in `validatePooledGitAuth`'s comment (`9bf1e072`).
2. **P3 — connection-string leak at the source.** `BuildPgxpoolConfig` maps pgx's parse error
   (`*pgconn.ParseConfigError`, which quotes the URL and masks only a userinfo password) to
   `workspace.ErrInvalidDatabaseURL` plus the reason; `NewWorkspaceDBPool` masks the string's
   credentials (userinfo, `password=`/`sslpassword=` in URL or key=value form, the string itself)
   in open and ping errors (`connStringFreeError`). Tests (`dbpool_credentials_test.go`): seven
   unparsable strings (bad sslmode with a userinfo or query password, bad connect_timeout with a
   query password or sslpassword, key=value forms, a bad pool option) → no password, no host, the
   reason kept; an unreachable but parsable string → the connect error carries no password;
   **the review's case end to end** — db-mode configs with those URLs through a real
   `ServicePool`: no `pool.Get` error and no `MultiProjectionSync` log line carries a password.
   **Red at the base** (`red-r1-dsn-base.log`): every case leaks (`password=TOPSECRET-A2` in the
   Get error, the URL with the host everywhere). The gateway's `describePreauthErr` stays as
   defense in depth.
3. **P3 — KNOWN-LIMITATIONS "Pre-authentication builds" corrected**: `Get`/`Release` refresh
   last access, so one junk bearer per workspace per idle timeout keeps it built; a first build
   opens the pool, sweeps secrets, seeds the bootstrap `smp-admin` rows and starts event
   delivery; `db` mode's projection sync builds every active workspace anyway. A per-workspace
   build rate limit or negative cache is recorded there as a **follow-up** (not in this task).
4. **P3 — bearer shape before Resolve.** `workspaceMiddleware`, in shared mode, answers
   `writeUniformUnauthorized` for a request without a well-formed bearer before Resolve. Every
   route behind it is behind `authMiddleware`, whose shared path answered exactly that 401, so no
   response changes; only the resolution (registry query, platform binding fetch, secret
   dereference) is saved. **Routes that take no bearer and are unchanged** (they do not pass
   through this middleware): `GET /api/v1/system/health`; the operator routes (`/api/v1/workspaces`,
   `/api/v1/workspaces/{id}`, `.../deactivate`, `/api/v1/platform/metrics`; operator token);
   `POST /internal/v1/workspaces/{id}/binding-invalidate` (its own bearer); and `/git/...`, which
   must resolve before its gate because trusted-CIDR and development reads are token-less — a
   residual now in KNOWN-LIMITATIONS ("`/git` still resolves before its gate"). No bearer-less
   route made the change unsafe. Test: `TestWorkspaceMiddleware_PlatformBindingNoFetchWithoutBearer`
   — a real `PlatformBindingProvider` against a counting platform server, as `spine serve` wires
   platform-binding mode; seven malformed headers × five workspace states (bound, missing,
   platform 500, invalid id, no header) × three routes → **0 binding fetches**, one byte-identical
   401; a well-formed junk bearer still resolves and gets the same body. **Red at the base**
   (`red-r1-resolve-base.log`): 63 fetches.
5. **P3 — docs true at this head.** `security-model.md` and the CHANGELOG now say that on `/git`
   every failure of a well-formed bearer is uniform here, and that the missing/malformed-header
   and push-off cases are made uniform by TASK-006; the Reconciliation section tells the
   integrator to drop that qualifier once both are merged.
6. **P3 — `api/spec.yaml`**: `verified` = read from the authoritative branch = passed the
   governed merge (§6.4.5); author-written evidence on a completed run shows `true`.
7. **P3 — `docs/operator-runbook.md`** gains the `SPINE_ENV` startup-refusal row; the CHANGELOG
   gains "Upgrade note: set `SPINE_ENV` before upgrading" (and what a formerly-unset production
   deployment must meet once it sets `production`).

**Gates at `9bf1e072`:** `go build ./...`; `go vet` plain, `-tags integration`, `-tags scenario`;
`./dev/lint-boundaries.sh`; `go test -p 4 ./...` (40 packages ok; `r1-gate-go.log`); `-race` on
workspace, gateway and cmd/spine; `make build` and the embed smoketest; the scenario-git-http job
on the branch (every step, 0 skips: `TestGitHTTP_*` 87, `TestBootstrapInternalAdmin_*` 26,
`TestWorkflowLifecycle_*` 130, the rest as before; `ci-br2.out`); git-floor in
`golang:1.26-bookworm` (git 2.39.5) both steps plus this task's tests (`gitfloor/out-r1.log`);
golangci-lint on the touched packages, `service/internal/workspace` included, 0 new issues (149
vs 151 at the base), gosec 0/0; gofmt clean. **SMP gate re-run** (the workspace pool changed, though
`service/internal/...` is not importable by SMP): build and four vets clean; race identical to
before (only the two by-design gitread contract tests, proven at the base); testgate runtime 2143
passed / 3 allowed skips, githttp 588 / 1; no ws_* database or role leaked.

**Codex** (`codex review --base init/INIT-024-scaffold`, plain, model `gpt-6.1-sol` confirmed in
the review sub-session JSONL): review-cycle round 1 at `9bf1e072`, session
`01a16ae1-2908-7251-8f7d-9789eac64102`, no findings ("patch is correct").

**Not run:** SMP's full Platform-DB testgate suites; GitHub CI (nothing pushed).

### Integration A (2026-10-27)

Branch `init/INIT-024-integration-a`, cut from Spine local main `83d82409` (= `a560c210` + the
INIT-024 scaffold + TASK-001), in the TASK-008 group's worktree. Merge order (each `--no-ff`,
"Merge <branch> into the INIT-024 integration A branch"): TASK-009 `b8dc7bb3` → `899cd774`;
TASK-002 `07d6ca8a` → `c6bad6c0`; TASK-006 `96dcea55` → `000c5461`; TASK-007 `55a67a45` →
`e6ab7e81`; TASK-008 `c6541bd5` → `8a0a1b29`; TASK-003 `02334613` → `4f502b7e`. Coordination
commits (prefix "INIT-024 integration A — "): `7d0e8522` (TASK-006 × TASK-007), `69c15fb4`
(TASK-007 × TASK-008), `55789b28` (TASK-003 × TASK-007/TASK-008), `377c3343` (TASK-007 ×
TASK-008 logging). Nothing is pushed; the status stays In Progress until GitHub CI has run after a
push.

`CHANGELOG.md` conflicted at every merge (each task adds its own section at the top): every
section is kept once, newest merge on top, each followed by a `---` line.

**This task.** Merged fourth (`e6ab7e81`). Conflicts: `CHANGELOG.md`, and
`service/internal/gateway/handlers_git.go`, where TASK-006's `logTrustedPeerResolveFailure` and
`isUnauthorizedError` and this task's `validatePooledGitAuth` are all kept. The reconciliation
items above were applied in coordination `7d0e8522`: the TASK-006 fixture diff (the three
TASK-006 tests failed on the merge before it, as predicted, and pass after it); the "made uniform
by TASK-006" qualifier was dropped from `CHANGELOG.md` and `architecture/security-model.md` at
`7d0e8522` (the `/git` uniformity is stated without it); TASK-006's 503 residual is narrowed to
single mode. Later coordination involving this task:
- `69c15fb4` (with TASK-008): `TestPreauth_BoundedPoolBuildFailuresAnswerUniform401` — on `/api/v1`
  and `/git`, a build hung past InitTimeout (first call, abandoned, valid token), a caller waiting
  on an evicted build, a panicking and a failing build (valid token), and an unknown token on a
  built workspace all answer the missing workspace's `401` byte for byte; the hung calls return at
  about InitTimeout. Red at TASK-008's tip without this task: 16 calls answer `500`, `503` or a
  distinct `401` message. Docs: TASK-008's CHANGELOG entry, item 9 and the
  `SPINE_WS_POOL_INIT_TIMEOUT` row now say a request sees those failures as this task's `401`.
- `55789b28` (with TASK-003): `TestPreauth_PendingRenormalisationIsInvisibleBeforeAuthentication`
  — a workspace whose build left TASK-003's renormalisation pending answers the missing
  workspace's `401` on `/api/v1` and `/git` (reads, push advertisements, pushes; no header or a
  junk bearer); a valid token is served; no unauthenticated push installs the budget hook. Red at
  TASK-003's tip (without this task): six distinct `401` messages.
- `377c3343` (with TASK-008): `authenticatePooled`'s lines repeated the `workspace_id` the request
  context's logger already carries; they now log it once (`TestPreauthLog_NamesTheWorkspaceOnce`,
  red without the change: every line carried two).

Gates at `377c3343` (logs in the SMP repo, `local-scratch/i24/int-a/`; heavy legs under
the shared heavy-suite lock):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`, `make build`, embed-smoketest | OK / rc 0 / "library surface is sound" |
| `go vet` plain / `-tags integration` / `-tags scenario` | rc 0 ×3 |
| `go test -p 4 -count=1 ./...` | rc 0 |
| `go test -race -p 4 -count=1 ./...` | rc 0 |
| ci.yml `scenario-git-http`, every run step, own database | every step rc 0, 0 skips: `TestGitHTTP_*` 90 passes, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, the shared-mode scheduler test; every required `--- PASS` present |
| `go test -tags integration -p 1 ./...` (migrated registry database) | 39 packages ok; only the two known pre-existing failures (`TestAWSClient_Contract_LocalStack`, `TestRunModeDatabaseDefault`) |
| ci.yml `git-floor` (local `golang:1.26-bookworm`, git 2.39.5, go1.26.8 with `GOTOOLCHAIN=local`, `--pull=never`; the pinned 1.26.10 image is not present locally) | both CI steps rc 0, plus TASK-003's budget/hook tests and the integration coordination tests rc 0 |
| golangci-lint v2.11.4 `--new-from-rev main ./...` plain / integration / scenario; `--enable-only=gosec` | 0 issues ×4 |
| `gofmt -l` on the files changed since main | clean, except `service/scenariotest/scenarios/planning_run_test.go`, already unformatted on main (left as is) |
| `go run ./dev/pincheck`; `go mod tidy -diff`; `go mod verify` | OK; no diff; all modules verified |
| SMP compatibility (SMP `82899ace` detached, `../spine` = the integration head, `GOFLAGS=-mod=readonly`, 0 dirty files before and after) | `go build ./...` and `go vet` plain / integration / scenario / faultinject rc 0; `go test -race -p 4 ./...` 151 packages ok, 0 failures; testgate Spine-runtime leg PASS (2447 passed, 3 allowed skips); testgate githttp leg (`-tags integration`, -race) PASS (590 passed, 1 allowed skip); no `ws_*` database or role left |

Codex (`codex review --base main`, plain, from the integration worktree): round 1 at `377c3343`,
review sub-session `01a17e99-5b95-71f3-995c-e5f81a1d37b2`, model `gpt-6.1-sol`: no findings ("No
actionable regressions were identified"). It covered every merged task and coordination commit,
including TASK-003's `5636c02d` and `e57a6741`, which had no task-level codex round.

**Phase 4 (2026-10-30).** TASK-003's phase-4 renormalisation leaves a renormalisation pending past
the line only on an I/O error or a crash. `TestPreauth_PendingRenormalisationIsInvisibleBeforeAuthentication`
(coordination `55789b28`) now makes the build's rewrite fail with a stale `index.lock`, which root
cannot bypass either (`5ae1e563`, `5ae28915`). The property is unchanged: a pending
renormalisation is invisible to an unauthenticated caller on `/api/v1` and `/git`. Gates: see
TASK-003's Integration A, phase 4.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: Completed.** GitHub CI run 41875102030 at Spine `main` `ce0b9c9e` (this initiative's
  closing commit `fd35f802`, pushed on 2026-11-04 with the owner's approval, plus TASK-010's
  git-identity fix) passed all 8 jobs. Every AC is met, and the owner questions stay at their
  recorded defaults.
