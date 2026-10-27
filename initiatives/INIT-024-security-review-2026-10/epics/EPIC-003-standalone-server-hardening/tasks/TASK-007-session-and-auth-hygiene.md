---
id: TASK-007
type: Task
title: "Session and auth hygiene: SSE revalidation, no pre-auth ServiceSet, explicit SPINE_ENV, evidence provenance"
status: Pending
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
