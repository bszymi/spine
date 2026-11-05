---
id: TASK-006
type: Task
title: "Workspace-scoped trusted-CIDR clone in shared mode"
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
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-009-narrow-default-trusted-cidrs-for-git-http.md
---

# TASK-006 — Workspace-scoped trusted-CIDR clone in shared mode

---

## Purpose

This task closes **N-2 (P2)** from the 2026-10-22 review (SA-2; detail in SMP's git-ignored
`tmp/security-review-2026-10-22/spine/REPORT.md`). INIT-023 TASK-015 narrowed the trusted-CIDR
clone bypass to direct peers, but it stays a per-process decision: a trusted address reads every
workspace, not just the ones it serves.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-2 | P2 | **Standalone, shared mode** (`db` or `platform-binding` resolver) with `SPINE_GIT_HTTP_TRUSTED_CIDRS` set. Not SMP: SMP runs its own gateway. | A host whose address is in the trusted list — in a multi-tenant deployment, a runner host serving one tenant, or job content on it. It is the documented runner configuration (`docs/integration-guide.md:651`, `docker-compose.override.yaml.example`). |

- **The defect.** The token-less read bypass is decided from the peer IP alone, independent of
  the `/git/{workspace}` the request names. A trusted runner host can clone any workspace's
  repository with no token, a cross-tenant confidentiality break. The bypass also turns the
  endpoint into a workspace-existence oracle (present vs missing answer differently).

## Current code (verified at `a560c210`)

- `service/internal/gateway/handlers_git.go:142-146`: `trustedBypass` is
  `s.gitHTTP.IsTrustedIP(ip.String())` on a direct-peer read. The workspace id
  (`parseGitPath`, `:75`) is not consulted.
- `service/internal/githttp/handler.go:198-216` `IsTrustedIP` checks only the process-wide
  `trustedCIDRs`.
- The list is built process-wide at `service/cmd/spine/serve_cmd.go:155` from
  `SPINE_GIT_HTTP_TRUSTED_CIDRS` and passed to the one handler
  (`buildGitHTTPHandler`, `serve_builders.go:496`; wired `serve_wiring.go:515`). Shared mode is
  `deps.WSServicePool != nil` (`serve_wiring.go:381, :515`).
- INIT-023 TASK-015 added the proxy and forwarded-header guards (`gitPeerIP`, `:552`;
  `warnProxiedTrustedGitPeer`), and the startup overlap warning
  (`serve_cmd.go:156`), but no per-workspace binding.

## Design

1. **Bind trusted CIDRs to workspaces in shared mode.**
   - A new environment shape, `SPINE_GIT_HTTP_TRUSTED_CIDRS` gains an optional per-workspace form:
     `ws-a=172.28.1.10/32;ws-b=172.28.1.11/32`. The existing bare-list form
     (`172.28.1.10/32,172.28.1.11/32`) stays valid and means **single mode only**.
   - In shared mode, `handleGit` computes `trustedBypass` as "the peer is in the set bound to the
     workspace this request names", not "the peer is in the global set". A new
     `IsTrustedIPForWorkspace(workspaceID, ip)` replaces the bare `IsTrustedIP` call at
     `handlers_git.go:145`.
   - A peer trusted for workspace A gets no token-less read of workspace B: B answers the uniform
     401, exactly as an untrusted peer does, so the existence oracle closes too.
2. **Fail closed at startup in shared mode.**
   - `spine serve` in shared mode (`WSServicePool != nil`) refuses to start when
     `SPINE_GIT_HTTP_TRUSTED_CIDRS` is set in the bare-list form, because an unbound list would be
     the cross-tenant bypass this task removes. The error names the per-workspace form.
   - Single mode keeps the bare list (there is one workspace).
   - An empty/unset list stays "every caller needs a token", unchanged.
3. **No change to the push path or to the proxy and forwarded-header guards.** Push never takes a
   bypass (INIT-023), and the TASK-015 proxy rules still apply before this check.

## SMP impact

- **None.** SMP runs its own `/git/{workspace}` gateway (`internal/customer/githttp`) and does not
  embed `service/internal/gateway` or `service/cmd/spine`. SMP's gateway already scopes auth per
  workspace (`gateway.go:388`). The task is standalone-only; the SMP gate re-runs and is expected
  inert.

## Decisions

1. **Per-workspace CIDRs, not job-scoped tokens.** A job-scoped clone token is the fuller fix, but
   it needs a token-minting and -validation surface the runner integration does not have today.
   Binding CIDRs to workspaces closes the cross-tenant read with the existing configuration shape
   and is the review's own first recommendation. A job-scoped token is a follow-up (owner
   question 2).
2. **Refuse to start rather than silently narrow.** An operator who set an unbound trusted list in
   shared mode intended the (unsafe) behaviour; failing closed at startup makes the change
   visible, as INIT-023's other startup gates do.
3. **The single-mode form is unchanged**, so existing single-workspace deployments and the dev
   compose keep working.

## Owner questions

1. **Shared-mode unbound list.** *Default:* refuse to start (Design 2). *Alternative:* start, warn,
   and treat the list as bound to no workspace (so no token-less read works) — safe but silent.
2. **Job-scoped tokens.** *Default:* not in this task; per-workspace CIDRs are enough for the
   pilot. *Alternative:* a follow-up task mints a per-run clone token the runner presents, after
   which trusted CIDRs can be dropped.

## Acceptance Criteria

1. **Regression: N-2** (`service/internal/gateway`,
   `TestHandleGit_TrustedCIDRScopedToWorkspace`). This ports
   `scratch/standalone/sa2/zz_sa2_trusted_cidr_crosstenant_test.go`: a server in shared mode with
   `ws-a` bound to the runner address; a token-less clone of `ws-a` from that address succeeds,
   and a token-less clone of `ws-b` from the same address answers 401. At `a560c210` both answer
   200.
2. **Existence oracle closed** (`TestHandleGit_UnboundWorkspaceUniform401`). From a trusted
   address, a token-less read of a missing workspace and of a real workspace the address is not
   bound to both answer the same uniform 401.
3. **Startup gate** (`service/cmd/spine`, `TestServe_SharedModeRefusesBareTrustedList`). Shared
   mode with a bare-list `SPINE_GIT_HTTP_TRUSTED_CIDRS` refuses to start, naming the per-workspace
   form. Single mode with the same value starts.
4. **Parsing** (`TestParseGitHTTPTrustedCIDRs_PerWorkspace`). The bare and per-workspace forms
   both parse; a malformed entry is a startup error.
5. **Push unaffected** (`TestHandleGit_PushNeverTakesWorkspaceBypass`). A push from a bound
   trusted address still requires a bearer.
6. **Existing suites.** `go test ./service/...` and `scenario-git-http` pass.
7. **SMP compatibility gate** (initiative §5) passes with no SMP change (inert).
8. **Documentation.** `docs/integration-guide.md` (the trusted-CIDR section),
   `docker-compose.override.yaml.example` (the per-workspace recipe), `KNOWN-LIMITATIONS.md` and
   `CHANGELOG.md`.

## Compatibility

- Standalone only. The bare-list form stays valid in single mode and is refused in shared mode
  (Design 2).
- The per-workspace env form is new.

## Out of scope

- Job-scoped clone tokens (owner question 2).
- SMP's gateway (already per-workspace).

## File overlaps

- `service/cmd/spine/serve_config.go`: this task touches `parseGitHTTPTrustedCIDRs`; TASK-007
  touches `resolveRuntimeEnv`. Different functions.
- `service/cmd/spine/serve_wiring.go` / `serve_builders.go`: this task changes the git-handler
  config; TASK-008 changes the scheduler block. Different blocks.
- `service/internal/gateway/handlers_git.go` and `service/internal/githttp/handler.go`
  (`IsTrustedIP`): this task only in EPIC-003. TASK-003/005 touch other parts of
  `githttp/handler.go`; `IsTrustedIP` and `ServeHTTP`'s CIDR block do not overlap their lines.

## Implementation Notes

*(Implementation, 2026-10-23. Branch `init/INIT-024-TASK-006-workspace-scoped-trusted-cidr`, cut
from `init/INIT-024-scaffold` `12376b55` (= `a560c210` + the INIT-024 scaffold). macOS (APFS),
git 2.54.0 (Apple), go 1.26.5 (SMP's toolchain line: 1.26.8), PostgreSQL 18.6 in `i23-pg`; the
git-floor job in the local `golang:1.26-bookworm` image (git 2.39.5). The design's line numbers
held at `12376b55`.)*

### Owner questions (defaults, pending the owner)

| Question | Default taken | Where |
|---|---|---|
| OQ1 shared-mode unbound list | Refuse to start, naming the per-workspace form | `gitTrustedCIDRs.checkMode` (early, from `WORKSPACE_RESOLVER`) and `bind` (backstop, from the built `ServicePool`) |
| OQ2 job-scoped clone tokens | Not in this task; per-workspace CIDRs only. Residual recorded in `KNOWN-LIMITATIONS.md` | — |

### What was built

| Commit | Change |
|---|---|
| `c8be2f8b` | `githttp`: `Config.TrustedCIDRsByWorkspace` / `Handler.IsTrustedIPForWorkspace` replace the process-wide `TrustedCIDRs` / `IsTrustedIP` (exact workspace-ID match; an empty ID or an unparsable CIDR refuses `NewHandler`). `gateway.handleGit`: the bypass is looked up under the **resolved** `cfg.ID` and only when the workspace resolved; every `401` on `/git` is the uniform one (`isUnauthorizedError`). `cmd/spine`: `gitTrustedCIDRs` (bare `Unbound` / per-workspace `ByWorkspace`), `parseGitHTTPTrustedCIDRs` (now returns an error), `checkMode`, `bind`, `unservedTrustedWorkspaces`, `sharedWorkspaceResolverEnv`; the early gate in `serveCmd` and the binding after the resolver is built; `serveDeps.TrustedGitHTTPCIDRs` and `buildGitHTTPHandler` take the map. Tests below; existing tests and three scenario setups moved to the per-workspace config. |
| `087bc243` | Docs: integration guide §7 and env table, `docker-compose.override.yaml.example` (Option D, shared mode), `.env.example`, README, `KNOWN-LIMITATIONS.md`, `CHANGELOG.md`. |
| (this commit) | These notes; status In Progress. |

No migration, schema, table, column, route, event or Go interface change (the renamed githttp
`Config` field and `Handler` method are in an internal package). No package SMP embeds is
touched: every changed Go file is under `service/` (`service/internal/...` is not importable
outside `service/`, and SMP's `dev/lint-spine-internals` forbids `service/...`).

### Per-AC evidence

"Red at the base" means the branch's new test file run in a `git archive` of
`init/INIT-024-scaffold`, extracted for the run and deleted afterwards (re-create it with `git
archive init/INIT-024-scaffold | tar -x -C <dir>`). The logs are scratch `t006/base-gateway.log`,
`t006/base-cmd.log` and, for review round 1, `t006/base-gateway-r1.log`; the two shim files are
kept in `t006/base-shims/`. The gateway tests build their handler through one
helper, `t006GitHandler`; for the base run that helper is replaced by a shim handing the same
runner addresses to the base's process-wide `TrustedCIDRs` (the only trust an operator could
configure there). The `cmd/spine` RunE tests compile against the base unchanged (copied without
the parser and `bind` tests, whose types do not exist there).

1. **Regression N-2** — `TestHandleGit_TrustedCIDRScopedToWorkspace`
   (`service/internal/gateway/git_trusted_cidr_scope_internal_test.go`), the PoC ported: real
   git-http-backend, bare repos side by side under one root, `ws-a` bound to `172.28.1.10/32` and
   `ws-b` to `172.28.1.11/32`, through the real `/git` chi routes, in shared mode (a
   `ServicePool`) and in the PoC's pool-less two-workspace shape. ws-a's runner: ws-a `200` with
   ws-a's refs; ws-b `401` with nothing of ws-b for smart and dumb `info/refs`, `POST
   git-upload-pack`, `HEAD`, `objects/info/packs` and `/git/ws-b/spine/...`; ws-b's runner reads
   ws-b and gets `401` for ws-a; a bearer still reads ws-b. Plus
   `TestHandleGit_TrustedCIDRScopedToWorkspace_RealClone`: a real `git clone` over
   `httptest` with `127.0.0.1/32` bound to ws-a only — ws-a clones token-less, ws-b's clone and
   `ls-remote` fail and nothing of ws-b reaches the runner.
   **Red at the base:** both fail — ws-b answers `200` advertising `spine/run/tenant-b-secret`
   (`POST git-upload-pack` got past auth to git-http-backend's `415`), ws-b's runner reads ws-a,
   and the real clone of ws-b succeeds. **Green on the branch.**
   **The PoC itself:** at the base it reproduces (`ws-a -> 200`, `ws-b -> 200`); at the branch head
   it no longer compiles (`unknown field TrustedCIDRs`), and with its one config line moved to
   `TrustedCIDRsByWorkspace: {"ws-a": {"172.28.1.10/32"}}` it reports `ws-b: 401 (finding
   refuted)`.
2. **Existence oracle closed** — `TestHandleGit_UnboundWorkspaceUniform401`: from ws-a's runner
   (and its IPv4-mapped form), token-less reads of ws-b, a missing, an inactive and an
   unavailable workspace, `WS-A` and `ws-a-` (smart, dumb, `HEAD`) all answer exactly the
   untrusted caller's `401`: same status, body and content type, both modes. **Red at the base:**
   `200` for ws-b, `404`/`403`/`503` for the others.
3. **Startup gate** — `TestServe_SharedModeRefusesBareTrustedList`
   (`service/cmd/spine/git_trusted_cidrs_test.go`): `serveCmd().RunE` with
   `WORKSPACE_RESOLVER=db`, `platform-binding` and ` DB ` and a bare list returns the refusal naming
   the per-workspace form and its example, before the registry or the platform is touched (neither
   is configured). Single mode (`WORKSPACE_RESOLVER` unset or `file`) with the same value, and
   shared mode with the per-workspace form or no value, get past the gate (they stop at the later
   `SPINE_STEP_ENTRY_POLICY` gate the test arms, instead of starting a listener).
   `TestGitTrustedCIDRs_Bind` covers the binding: bare → `{served workspace: list}` in single mode,
   refused in shared mode (the backstop), per-workspace passed through in both. **Red at the base:**
   the shared-mode cases reach the step-entry gate (no refusal).
4. **Parsing** — `TestParseGitHTTPTrustedCIDRs_PerWorkspace`: bare (IPv4, IPv6, whitespace) and
   per-workspace forms (several CIDRs, `;;`, trailing `,`, `_` in IDs) parse; 14 malformed shapes
   are errors (unparsable CIDR, an address without a prefix, workspace bound to nothing, empty /
   spaced / traversal-shaped / percent-encoded ID, a workspace named twice, a bare entry mixed in,
   a comma between workspaces, a second `=`). `TestServe_MalformedTrustedCIDRsRefuseStartup`: four
   malformed values refuse `spine serve` in every mode with `SPINE_GIT_HTTP_TRUSTED_CIDRS: ...`.
   The existing `TestParseGitHTTPTrustedCIDRs` keeps its bare-list cases. **Red at the base:**
   `TestServe_MalformedTrustedCIDRsRefuseStartup` reaches the step-entry gate (at the base a
   malformed value only disabled `/git` with a Warn).
5. **Push unaffected** — `TestHandleGit_PushNeverTakesWorkspaceBypass`: from ws-a's bound runner,
   push enabled, `info/refs?service=git-receive-pack`, `POST git-receive-pack` and the two smuggled
   push queries all answer `401` without the receive-pack advertisement; with a bearer the
   advertisement is `200`; with push off the canonical advertisement is the uniform `401`
   token-less and the flag's `403` with a bearer (review round 1; it was the `403` token-less). A
   guard for the push-on half: that passes at the base too (push never took the bypass).
6. **Existing suites** — see Gates: `go test ./...`, the touched packages under `-race`, the
   integration suites, and the scenario-git-http job pass.
7. **SMP compatibility gate** — see below; inert, no SMP change.
8. **Documentation** — `docs/integration-guide.md` (§7 Authentication path 2 with both forms, the
   shared-mode refusal, the uniform `401`; the example clone; the env table row),
   `docker-compose.override.yaml.example` (Option D, per-workspace recipe; Options A–C labelled
   single-workspace), `KNOWN-LIMITATIONS.md` (the binding and four residuals), `CHANGELOG.md`
   (Security, Changed (operators), Changed (embedders): none), plus `.env.example` and README.

### Existing tests that change

- `TestHandleGit_TrustedCIDRStillSeesWorkspaceErrors` (TASK-031) pinned a distinct `404` for a
  trusted caller on the premise that a trusted address may know every workspace ID — the oracle
  AC 2 closes. Replaced by `TestHandleGit_TrustedCIDRSeesUniform401ForMissingWorkspace` (uniform
  `401`, even for a missing workspace whose ID the address is bound to; an authenticated caller
  still gets the `404`).
- Scenario `TestGitHTTP_InvalidWorkspace` asserted `404` for a token-less request from the trusted
  `127.0.0.1`; it now asserts the uniform `401` token-less and the `404` with a bearer.
- `TestIsTrustedIP*` / `TestNewHandler_InvalidCIDR` (githttp) became `TestIsTrustedIPForWorkspace`
  (cross-workspace, case, trailing space, empty ID, IPv6, IPv4-mapped), `..._NoCIDRs` and the
  invalid-config table. The t015 helper, three `handlers_git_test.go` handlers and three scenario
  setups bind their workspaces (`ws-1`; `default` and `ws-1`) to the same CIDR they trusted.

### Deviations and additions (with reasons)

1. **No process-wide list survives anywhere, not only in shared mode.** The design kept
   `IsTrustedIP` for single mode and switched to `IsTrustedIPForWorkspace` in shared mode. The
   gateway's only shared-mode signal is `servicePool != nil`, and the PoC's own server (two
   workspaces, no pool) would have kept the cross-tenant read under that rule. Instead githttp
   holds only workspace-bound trust, and `spine serve` binds the bare list to the single-mode
   workspace (`SPINE_WORKSPACE_ID`, default `default` — the ID `FileProvider` serves). The decision
   in `handleGit` is then mode-independent: a workspace without a binding is never token-less.
2. **The trust is looked up under the resolved `cfg.ID`, not the URL segment.** The repository
   served is always `cfg`'s (`cfg.RepoPath` or `cfg`'s own pool), so keying on `cfg.ID` pairs the
   trust with exactly what is served, and it works for the single-workspace route `/git/...`,
   where the URL names no workspace and the file resolver serves its one. A URL-keyed check would
   not be a cross-tenant hole by itself — the review's mutation of it still passes the new gateway
   tests, `/git//ws-b/...` included, since an empty segment binds nothing — but it loses the
   single-workspace route (ten `TestGitHTTP_*` scenarios clone through `/git`) and, without the
   resolved-workspace condition, gives a bound peer the distinct error of an unresolvable workspace
   (`TestHandleGit_TrustedCIDRSeesUniform401ForMissingWorkspace`).
3. **Single mode, behaviour change.** A trusted runner asking for a workspace that does not
   resolve gets the uniform `401` instead of `404`/`403`/`503` (no bypass without a resolved,
   bound workspace). Recorded in CHANGELOG.
4. **A malformed value refuses startup** (AC 4) in every mode. At the base it reached
   `githttp.NewHandler`, whose error disabled the whole `/git` endpoint with a Warn.
5. **The shared-mode refusal runs twice.** Early from `WORKSPACE_RESOLVER` (`db`,
   `platform-binding`), before any registry, secret or database is opened; again in `bind` against
   the resolver actually built (`wsWiring.Pool != nil`), so a future shared resolver kind cannot slip
   past the environment check. An unknown `WORKSPACE_RESOLVER` is left to its own refusal.
6. **Addition: the per-workspace form is accepted in single mode** (one configuration for both
   modes); an entry for a workspace the server does not serve logs a startup Warn and has no effect.
7. **Addition (adversarial review): every `401` on `/git` is the uniform one.** A junk or malformed
   bearer got the auth error's own message (`invalid token`, `invalid authorization header format`)
   only for a workspace that resolved, and the uniform message otherwise — an existence oracle for
   any caller, trusted or not (pre-existing, not in the review). `TestHandleGit_InvalidBearerIsNoWorkspaceOracle`
   is red at the base (ws-b `invalid token` vs the missing workspace's uniform body) and green on the
   branch. A `503`/`500` for a resolved workspace is still surfaced (the TASK-031 rationale); the
   no-auth-service `503` remains a documented residual.

### Adversarial self-review

`TestHandleGit_TrustedPeerCannotReachUnboundWorkspace` (both modes; status and body probed per
case, then pinned as "no `200`, nothing of ws-b"), from ws-a's runner against ws-b, with a control
showing ws-a's own repository served through the same routes:

| Attempt | Result on the branch |
|---|---|
| `WS-B`, `ws%2Db`, `%77s-b`, `ws-b%00`, `ws-b.`, `ws-a%2F..%2Fws-b` as the workspace segment | uniform `401` (no such workspace resolves) |
| `/git//ws-b/...` (empty chi segment, resolves ws-b) | `401` (at the base: `200` with ws-b's refs) |
| `/git/ws-a/../ws-b/...`, `/git/ws-a/ws-b/...` (repository segment) | ws-a's own catalog: `404` (shared) / `503` no pool (single) |
| `/git/ws-a/info/../../ws-b/...`, `objects/../../ws-b/HEAD`, `POST .../info/../../ws-b/git-upload-pack` | git-http-backend refuses the `..` path (`'...': aliased`, `500`), verified by hand outside Spine too |
| `/git/ws-a/info/%2E%2E/%2E%2E/ws-b/...` | `404` (literal `%2E` path) |
| `/git/ws-a/info%2F..%2F..%2Fws-b/...` | unknown repository `404` / `503` |
| `/git/info/refs?...` (single-workspace route, shared mode) | `401` |
| `X-Forwarded-For`, `Forwarded`, `X-Real-IP` naming ws-b's runner | `401` (forwarded requests are never token-less) |
| `HEAD`, `PROPFIND` | `401`, `405` |
| IPv4-mapped `[::ffff:172.28.1.10]` | ws-b `401`; ws-a `200` (same host) |
| IPv6 peer with a zone | `401` (unparsable peer) |
| Push vs fetch | AC 5 |
| A peer trusted for A asking for B, single vs shared | AC 1, both modes |
| A junk or malformed bearer as an oracle | Found and closed (addition 7) |

Not closed here (no cross-workspace read, recorded as follow-ups): the `..` refusal lives in
git-http-backend, not Spine (a gateway-side `400` for dot segments would be defence in depth);
`SPINE_GIT_HTTP_DEV_NO_AUTH` is not workspace-scoped (development only); a resolved workspace with
no auth service at all still answers `503` to a bearer.

### Gates

At `087bc243` (code identical to these notes' commit). Logs in SMP `local-scratch/i24/t006/`.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | pass |
| `make build` | pass |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | pass |
| `go vet ./...`, `-tags integration ./...`, `-tags scenario ./...` | pass (all three) |
| `go test -p 4 ./...` | pass except `core/artifact` `TestUpdateEmitsEvent` (`git commit ...:` under load; untouched package): passes standalone (`-count=3`) and the whole package re-run passes |
| `go test -race -p 4 -count=1` githttp, gateway, cmd/spine | pass |
| `go test -tags integration -p 4` githttp, gateway, cmd/spine (own database `s24006_ci_br2`) | pass |
| scenario-git-http job: `TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*` (`-tags scenario -count=1 -v`) | pass, 0 skips (89 / 26 / 130 PASS lines; a `TestGitHTTP_Push*` passed); before the `TestGitHTTP_InvalidWorkspace` update the first run failed exactly that scenario (the trusted caller's `404`, now the uniform `401`) |
| `TestRunnerCloneContext_*` (scenario setup moved to the per-workspace config) | pass |
| Full `go test -tags scenario -p 1 ./service/scenariotest/...` with a migrated registry, branch vs scaffold base | identical failure sets: the 31 known pre-existing top-level failures, no new one |
| git-floor job in `golang:1.26-bookworm` (`--pull=never`, git 2.39.5): both CI steps, plus the githttp/gateway TASK-006 and P3-30 tests | pass |
| golangci-lint v2.11.4 `--new-from-rev 12376b55` on githttp, gateway, cmd/spine, scenarios (plain / integration / scenario) | 0 new issues (the non-zero exit on plain/integration is the scenarios package having only scenario-tagged files) |
| `--enable-only=gosec --build-tags scenario`, branch vs base | 0 / 0 |
| `gofmt -l` and `git diff --check` on changed files | clean |

### SMP compatibility gate

SMP `3addd44b` (`.spine-revision` `ae513dca`) in `i23/s24-T006/smp`, whose `../spine` is this
worktree at `087bc243`, no SMP change, no `vendor/`.

- `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario ./scenariotest/...`,
  `-tags faultinject`: all pass.
- No SMP package imports a changed Spine package (every change is under Spine `service/`; the only
  SMP mention of `bszymi/spine/service` is `dev/lint-spine-internals`, which forbids it). The
  initiative §5 package set was still run under `go test -race -p 4`: all pass except
  `internal/customer/gitread` `TestClient_ErrorContractEqualsCLIClient/*/dangling_gitlink` and
  `TestClient_SpineReadFileRendersTheGitlinksPatch` — the two by-design gitread contract tests that
  fail against `a560c210` too, the same pair TASK-001 recorded.
- `dev/testgate` Spine-runtime leg (spinecore, spineembedded, runner, executionreadiness; platform
  and spine-runtime databases migrated with `dev/apply-migrations`): PASS, 2143 tests, 3 allowed
  skips. githttp `-tags integration` leg: PASS, 588 tests, 1 allowed skip (Linux `ru_maxrss`).
- No `ws_*` database or role left behind (0 before, 0 after); the `s24006_*` databases are dropped.

Verdict: inert for SMP, as the task predicted.

### Codex review (`codex review --base init/INIT-024-scaffold`, plain)

| Round | Review session | Model | Result |
|---|---|---|---|
| 1 (at `087bc243`) | `01a167fe-ef06-7dd1-a4b1-2fb1a3acdbd4` (`source: {"subagent":"review"}`, cwd this worktree) | `gpt-6.1-sol` | 0 findings, "patch is correct" (confidence 0.91); it re-ran the three touched packages' tests |

Clean in round 1, so no further round.

### Not run

- GitHub Actions on the pushed branch (nothing is pushed; INIT-024 owner question 1).
- `TestGitHTTP_InvalidWorkspace`'s new expectations at the base: the scenario file binds through
  `TrustedCIDRsByWorkspace`, which does not compile there; AC 2's red run covers the same
  behaviour (`404` for a trusted token-less caller at the base). Command, at the branch:
  `SPINE_TEST_DATABASE_URL=... go test -tags scenario -count=1 -run '^TestGitHTTP_InvalidWorkspace$' -v ./service/scenariotest/scenarios/`.
- A live `spine serve` in `db` mode with real runner containers: the startup gate is exercised
  through `serveCmd().RunE` and the request path through the real chi routes, git-http-backend
  and a real `git clone`. Command for a manual check:
  `WORKSPACE_RESOLVER=db SPINE_REGISTRY_DATABASE_URL=... SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.1.10/32 spine serve`
  (expect the refusal), then `SPINE_GIT_HTTP_TRUSTED_CIDRS='ws-a=172.28.1.10/32' spine serve` and
  `git clone http://spine:8080/git/<ws-b>` from 172.28.1.10 (expect `401`).

### Follow-ups

- Job-scoped clone tokens, after which trusted CIDRs can be dropped (owner question 2).
- Defence in depth: refuse `.`/`..` path segments in `handleGit` before git-http-backend (today
  git-http-backend's alias check refuses them with a `500`).
- Collapse the resolved-workspace "authentication not configured" `503` for a bearer-carrying
  caller, if the operator signal is judged less important than the residual oracle.

### Review round 1 (2026-10-23)

Two reviews (correctness/compatibility and security lenses) found no P1/P2. The P3s fixed here,
in `c69e4e7b` (code and tests), `bb2ab597` (docs), `e1e63acf` (a gocritic nit in a new test) and
this commit (notes):

| Finding | Disposition |
|---|---|
| Security #1 = correctness #2: with push off, `info/refs?service=git-receive-pack` and `POST git-receive-pack` skipped the auth gate whenever the workspace resolved, so the flag's `403` answered only for existing workspaces (pre-existing, EPIC-004 TASK-031) | **Fixed.** The push-disabled path is no longer an auth bypass (`pushDisabled` now only keeps the repository lookup out of the way). A caller authenticated for the workspace gets the `403` naming the flag; any other caller — no token, junk, `Basic`, a bare scheme, another workspace's token — gets the uniform `401`. `TestHandleGit_PushDisabledIsNoWorkspaceOracle`: existing vs missing vs inactive vs unavailable give identical status, body and headers for both requests, from an outsider and from ws-a's runner, in shared mode with per-workspace auth (each ServiceSet authenticates its own tokens, so ws-b's token is "another workspace's") and without a pool; ws-a's token gets the `403`. **Red at the base** (`403` for ws-a without a token; log `t006/base-gateway-r1.log`), green here. Changed tests: `TestHandleGit_PushDisabledReturns403NotAuth` became `TestHandleGit_PushDisabled403OnlyForAuthenticatedCaller`; `TestHandleGit_DisabledPushDoesNotResolvePolicy`, `TestHandleGit_PerRepoRouting_PushDisabledKeeps403`, the wave 3 guard, AC 5's push-off half and scenario `TestGitHTTP_PushRejected` now authenticate to see the `403` and assert the `401` without a token. Docs: git-push-guide 401/403 rows, integration guide (push section; the uniform-`401` statement with the one known `503` exception), `KNOWN-LIMITATIONS.md` (same, so "the one known exception" is true), `CHANGELOG.md` (Security and Changed (operators)) |
| Correctness #1: the git-push-guide `401` row said only "no Authorization header" | **Fixed.** The row lists every case the uniform `401` covers (no or non-bearer header; malformed, unknown, expired, revoked or another workspace's token; a missing, inactive or unavailable workspace) |
| Correctness #3 = security #3: an unquoted `;` ends a shell assignment | **Fixed.** The refusal hint, `.env.example`, the integration guide (§7 and the env table) and the CHANGELOG quote the per-workspace example and say why |
| Correctness #4: CIDRs bound to different workspaces that overlap were accepted silently | **Fixed.** `crossWorkspaceCIDROverlaps` and one startup Warn listing every overlapping pair (not a refusal: one runner may serve two workspaces); `TestCrossWorkspaceCIDROverlaps` (range typo, shared address, IPv6, same-workspace and disjoint silent). Docs: integration guide, `KNOWN-LIMITATIONS.md`, CHANGELOG |
| Correctness #5: a trusted peer's resolution error collapsed to `401` with no server-side trace | **Fixed.** `logTrustedPeerResolveFailure` logs at Info when the direct peer is trusted for the workspace its URL names and that workspace did not resolve (the URL segment is logged only when it equals a configured binding); the response is unchanged. `TestHandleGit_TrustedPeerResolveFailureIsLogged` (one line naming the workspace; none for an unbound workspace or an untrusted peer; the response equals the untrusted caller's). Red at the base (the bound runner got the inactive workspace's `403`) |
| Correctness #6: the notes cited the deleted `t006/base/` | **Fixed** (Per-AC evidence preamble) |
| Correctness #7: Deviation 2 overstated the `/git//ws-b` rationale | **Fixed** (Deviation 2 reworded: `cfg.ID` is the served workspace and covers the single-workspace route; a URL-keyed check is not by itself a cross-tenant hole) |
| Security #2: read classification by path suffix inside the bound workspace's tree | **Not here** (orchestrator routing): INIT-024 TASK-004, because `githttp/handler.go` is shared with TASK-003/005. `githttp/handler.go` is untouched by this round |

**Gates (round 1)**, at `bb2ab597` unless noted:

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | pass |
| `go test -p 4 -count=1 ./...` | pass (no failure this run) |
| `go test -race -p 4` githttp, gateway, cmd/spine | pass |
| scenario-git-http job (`TestGitHTTP_*` 90 PASS lines, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 130, 0 skips), `TestRunnerCloneContext_*`, `-tags integration` on the touched packages | pass |
| Full scenario suite, branch vs the scaffold base | identical failure sets (the 31 known pre-existing failures) |
| golangci-lint `--new-from-rev 12376b55` plain / integration / scenario | 0 new after `e1e63acf` (the first scenario-tag run flagged one `httpNoBody` in a new test) |
| gosec only, branch vs base; `gofmt -l`; `git diff --check` | 0 / 0; clean; clean |
| SMP gate (`../spine` at `bb2ab597`; `e1e63acf` after it touches only a Spine test): build, vet plain / integration / scenario / faultinject, testgate runtime (2143 passed, 3 allowed skips), testgate githttp (588 passed, 1 allowed skip), race set (only the two by-design gitread tests fail, as before), no `ws_*` left | pass, inert |

**Codex (round 1 fixes)**: `codex review --base init/INIT-024-scaffold` at `bb2ab597`, review session
`01a169ea-2396-78c2-ad1b-c66004f228be` (`source: {"subagent":"review"}`, this worktree), model
`gpt-6.1-sol`: 0 findings, "patch is correct" (confidence 0.90). Pass 2 at `f0e61fe4` (after the
`e1e63acf` test nit and the notes): review session `01a16a30-7c82-7021-babe-5bb2ad3d58f2`,
`gpt-6.1-sol`, 0 findings, "patch is correct" (confidence 0.89). Status left In Progress.

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

**This task.** Merged third (`000c5461`); conflict: `CHANGELOG.md` only. With TASK-007 merged,
three of this task's gateway tests failed (`TestHandleGit_TrustedCIDRScopedToWorkspace`,
`TestHandleGit_PushNeverTakesWorkspaceBypass`, `TestHandleGit_InvalidBearerIsNoWorkspaceOracle`:
the shared fixture set the process-level auth service on a pooled server, which shared mode no
longer consults). Coordination `7d0e8522` applied TASK-007's reconciliation diff to
`git_trusted_cidr_scope_internal_test.go` (`setAuth` gives each workspace's ServiceSet the auth
service); the three pass. It also narrowed this task's no-auth-service `503` residual to single
mode (KNOWN-LIMITATIONS, the integration guide, this task's two CHANGELOG sentences, a `handleGit`
comment): in shared mode TASK-007 answers it with the uniform `401`. Tests:
`TestHandleGit_MergedGateIsUniformInSharedMode` (missing or malformed header, push off, a
ServiceSet without auth, a failed build, inactive and unavailable workspaces: each answers the
missing workspace's `401`, status, body and headers; red at `000c5461`: five `503`s) and
`TestHandleGit_NoAuthService503IsSingleModeOnly` (pins the residual). TASK-003's merge conflicted
in `serve_builders.go` (`buildGitHTTPHandler`): this task's `TrustedCIDRsByWorkspace` and
TASK-003's `TreeBudget` are both kept.

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

### Status after GitHub CI (2026-11-04)

- **2026-11-04: Completed.** GitHub CI run 41875102030 at Spine `main` `ce0b9c9e` (this initiative's
  closing commit `fd35f802`, pushed on 2026-11-04 with the owner's approval, plus TASK-010's
  git-identity fix) passed all 8 jobs. Every AC is met; the live `spine serve` check under "Not run"
  is an optional manual check, not an AC, and the owner questions stay at their recorded defaults.
