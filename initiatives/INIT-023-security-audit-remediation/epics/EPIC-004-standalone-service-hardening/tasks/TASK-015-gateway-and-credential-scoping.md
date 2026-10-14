---
id: TASK-015
type: Task
title: "SSE field safety, shared-mode tenant scoping, git-endpoint auth and gitpool token-host scoping"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-14
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
  - type: blocked_by
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-016-run-branch-push-admission.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/tasks/TASK-014-workflow-and-write-context-governance.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/tasks/TASK-013-identity-and-skill-authority.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-009-narrow-default-trusted-cidrs-for-git-http.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-016-support-forwarded-for-in-rate-limiter.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-020-refuse-dev-mode-auth-in-production.md
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-006-credential-resolution-at-clone-time.md
  - type: related_to
    target: /architecture/adr/ADR-012-per-workspace-connection-pool-policy.md
---

# TASK-015 — SSE field safety, shared-mode tenant scoping, git-endpoint auth and gitpool token-host scoping

---

## Purpose

This task closes the last standalone-service items from the 2026-10-06 security audit:
**P3-28**, **P3-29**, **P3-30**, and the refuted-but-latent **gitpool token-host** item (audit
§4). It keeps every fix in `service/`, in additive `core/observe` and `adapters/git` API, and in
`adapters/gitpool` / `adapters/repository`, which SMP does not exercise.

Line numbers refer to Spine `61994fe` unless stated. "SMP" means
`spine-management-platform` at `80995559`, which embeds Spine `61994fe` (`.spine-revision`).

### P3-28 — workflow identifiers inject Server-Sent-Events frames (standalone)

**Defect.**
- Core workflow validation checks only that a step ID is non-empty (`core/workflow/parser.go:136-138`).
  The audit PoC `poc/delivery-scheduler/poc_stepid_test.go` parses and validates a step ID of
  `"a\ndata: {\"type\":\"run_completed\"}\n\nid: x"` with no error about the ID.
- The engine builds event IDs from workflow-authored identifiers:
  - step IDs: `core/engine/handlers.go:121,204` (`evt-%s-%s-started/completed`),
    `core/engine/step.go:116` (`-assigned`), `core/engine/claim.go:181` (`-claimed`),
    `core/engine/retry.go:100` (`-retry-%d`);
  - divergence-point IDs: `core/divergence/service.go:85` (`%s-div-%s`), then event IDs
    `div-started-%s` (`:136`) and `conv-completed-%s` (`convergence.go:218`);
  - divergence-branch IDs: execution IDs `core/engine/step.go:500,682`, then `evt-%s-started`
    (`step.go:375`, `acknowledge.go:79`).
- The standalone SSE writer prints the ID and type raw:
  `fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", …)` (`service/internal/gateway/handlers_events_stream.go:171`),
  for live events (`:153`) and for `Last-Event-ID` replay from `runtime.event_log` (`:91`).

**Who.** Anyone who can land a workflow file on the authoritative branch:
- a reviewer through `PUT /workflows/{id}` (F7; a planning run after TASK-014);
- an operator through the ADR-008 bypass;
- a pusher once `SPINE_GIT_RECEIVE_PACK_ENABLED=true`;
- a run branch carrying `workflows/*.yaml` through a governed merge (F3).

**Impact.** Every SSE reader in the workspace (`events.read` = reader role, `core/auth/permissions.go:92`):
- receives a forged event (for example a fake `run_completed`) that dashboards and IDE
  integrations act on;
- gets an attacker-chosen `Last-Event-ID`. An unknown cursor makes replay return nothing
  (`adapters/store/postgres_delivery.go:242-245`: `created_at > NULL`), so a reconnecting client
  silently misses events.

Integrity of a notification channel only; P3.

**Verified at `61994fe`** (design-phase regression test, real gateway router and broadcaster):
one broadcast event with the PoC step ID dispatches **3** events to a WHATWG-conformant parser
instead of 2. The first is a forged `message` with data `{"type":"run_completed"}`, and the
client's last event ID becomes `x-completed`. The same happens with CR-only and CRLF variants, and
on the replay path.

### P3-29 — process-global gateway state leaks across workspaces in shared mode (standalone)

In shared multi-workspace mode (`WORKSPACE_RESOLVER=db|platform-binding`, which wires a
`workspace.ServicePool`, `service/cmd/spine/serve_resolver.go:408-415,445-452`), three pieces of
process-global gateway state are reachable by a **tenant operator** (`system.metrics`,
`system.rebuild` = operator, `core/auth/permissions.go:72-73`):

1. **`/system/metrics`** (`handlers_system.go:134-141`) returns `observe.ExportPrometheus()`
   (`core/observe/export.go:71-106`). That export includes:
   - `spine_workspace_pool_*{workspace_id=…}` for **every** workspace in the process
     (`export.go:111-150`);
   - process-global counters and histograms that aggregate every tenant's runs and steps.
2. **The rebuild registry** (`server.go:205`, `sync.Map`) is keyed by
   `"rb-" + traceID[:8]` (`handlers_system.go:87-97`), and the trace ID is client-settable through
   `X-Trace-Id` (`middleware.go:277-289`).
   - Tenant B can read tenant A's rebuild status (`:123-131`).
   - Tenant B can replace A's entry by starting a rebuild with the same trace prefix (`:97`).
3. **The SSE limiter** (`server.go:203,373`) is keyed on the bare `actor_id`
   (`handlers_events_stream.go:46-55`). Actor IDs are only unique within a workspace. Every
   workspace has its own `smp-admin`, and human IDs are often guessable. So five streams in
   workspace A exhaust the same ID's slots in workspace B (`429`).

**Impact.** Cross-tenant disclosure of the tenant list and activity, cross-tenant
read/overwrite of rebuild status, and a cross-tenant SSE denial of service. P3.

**Verified at `61994fe`:** a tenant-A operator's `/system/metrics` contains `workspace_id="…-ws-b"`
and `spine_runs_started_total`. Tenant B reads and overwrites A's rebuild (`200`). A second
workspace's `smp-admin` stream gets `429`.

### P3-30 — git-endpoint authentication bypasses (standalone)

The `/git/{workspace_id}/*` handler (`service/internal/gateway/handlers_git.go:55-232`) skips the
bearer check when `authBypass` is true (`:102-104`):

```go
trustedBypass := !push && s.gitHTTP.IsTrustedIP(r.RemoteAddr)
authBypass := pushDisabledShortCircuit || trustedBypass || s.devMode
```

1. **Raw `RemoteAddr`.**
   - `IsTrustedIP` (`service/internal/githttp/handler.go:166-184`) tests the TCP peer. Behind a
     reverse proxy whose address is inside `SPINE_GIT_HTTP_TRUSTED_CIDRS`, every client the
     proxy relays can clone and fetch every workspace's repository without a token. That
     includes run branches and governed content.
   - `SPINE_TRUSTED_PROXY_CIDRS` is honoured only by the rate limiter (`ratelimit.go:106-133`).
   - The shipped `docker-compose.yaml` trusts the whole bridge network,
     `SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.0.0/16` (`:31`, network `:166`). That range includes the
     bridge gateway `172.28.0.1`, which is the source address Docker's userland proxy uses for
     the loopback-published port `127.0.0.1:8089` (`:20`).
   - So any local process on the host can clone token-less. This is **code-traced**: as the audit
     notes, Docker was not run.
2. **Dev mode drops git auth.**
   - `s.devMode` is `SPINE_DEV_MODE`. It is refused only when `SPINE_ENV=production`
     (`service/cmd/spine/serve_config.go:183-193`, `serve_cmd.go:42-48`), so it is accepted with
     `SPINE_ENV` unset or `staging`.
   - It removes authentication from clone **and push**: anonymous pushes reach the pre-receive
     gate with no actor. With F1 (TASK-001) unfixed, that includes protected branches.
3. **Same defect class: XFF first line only.** `clientIPForRateLimit` reads only the first
   `X-Forwarded-For` header line (`ratelimit.go:115`). A proxy that adds its own line instead of
   appending (HAProxy `option forwardfor`) leaves the client-forged line first, so a client picks
   its own rate-limit bucket.

**Verified at `61994fe`** (real `git-http-backend`):
- `GET info/refs` from `172.28.0.1` returns `200` with no token, both with and without an
  `X-Forwarded-For` naming an internet client;
- under `devMode`, an unauthenticated clone returns `200` and an unauthenticated `POST
  git-receive-pack` returns `200`;
- the multi-line XFF bucket is the forged `1.2.3.4`.

### Latent — gitpool sends the process-wide push token to any code-repo host

**Mechanism (real).**
- `cliCloner.Clone` copies the process base options (`adapters/gitpool/pool.go:618`).
  `NewCLIClientFactory` does the same for the cached fetch/push client (`:566-582`).
- With `SPINE_GIT_PUSH_TOKEN` set, those options carry `git.WithPushToken`
  (`adapters/git/cli.go:184-221`). `NewCLIClient` then creates a host-agnostic `GIT_ASKPASS`
  script (`cli.go:70-112`), and `Clone` and every push feed the token to it (`:304-321`,
  `:650-668`).
- A code-repo binding with no `credentials_ref` (zero `Credential`) therefore sends the
  deployment-wide token to whatever `https` host its `clone_url` names. This contradicts the
  `Cloner` contract, "An empty Credential means 'no auth'" (`pool.go:70`).

**Why it is unreachable at `61994fe`** (audit §4, re-verified here):
- No `RepositoryManager` is wired in `spine serve`.
- `needRepoManager` returns 503 whenever a workspace resolver is set, which is true in every serve
  mode (`handlers_repositories.go:83-87`).
- Both production registries load a primary-only catalog (`service/cmd/spine/serve_wiring.go:220`,
  `service/internal/workspace/pool.go:854`).
- SMP never imports `adapters/gitpool`.

It becomes **P1** the moment a git-backed `.spine/repositories.yaml` catalog or a
`RepositoryManager` is wired: a tenant operator registers `https://attacker.example/x.git` and
receives the platform's push credential for every workspace.

**Companion leak, same surface.** `repository.RedactCloneURL` (`adapters/repository/clone_url.go:108-121`)
strips only a userinfo *password*. A token in the username position
(`https://<token>@github.com/…`) passes `ValidateCloneURL` and is echoed by
`GET /api/v1/repositories` (`handlers_repositories.go:41`). That route is also unreachable today,
through the same `needRepoManager` gate (`handlers_repositories.go:83-87`).

**Verified at `61994fe`:** the three audit PoCs (`poc/crypto-secrets-auth/pushtoken_exfil_test.go`,
`poc/gitpool_tokenleak/`, `poc/verify-r1-3/poc_gitpool_test.go`) and
`poc/crypto-secrets-auth/redact_cloneurl_test.go` all reproduce. A new factory-path test shows a
cached code-repo client pushing the token to its origin.

## Deliverable

Branch `INIT-023/EPIC-004/TASK-015-gateway-and-credential-scoping`, one PR, rebased on
TASK-016.

| Area | Files |
|---|---|
| P3-28 | `service/internal/gateway/handlers_events_stream.go`, `handlers_workflows.go` |
| P3-29 | `service/internal/gateway/handlers_system.go`, `routes.go`, `handlers_events_stream.go`; `core/observe/export.go` |
| P3-30 | `service/internal/gateway/handlers_git.go`, `ratelimit.go`, `server.go`; `service/cmd/spine/serve_config.go`, `serve_cmd.go`, `serve_wiring.go`; `docker-compose.yaml`, `.env.example` |
| Latent gitpool | `adapters/git/cli.go`, `adapters/gitpool/pool.go`, `adapters/gitpool/credentials.go`, `service/cmd/spine/serve_wiring.go`, comment-only `service/internal/workspace/pool.go`; `adapters/repository/clone_url.go` |
| Docs | `docs/integration-guide.md` (§6.2, §7, env table), `README.md`, `KNOWN-LIMITATIONS.md`, `CHANGELOG.md` |

It also includes the regression tests and migrated tests listed in the Acceptance Criteria.

A design-phase prototype of every change is at
`spine-management-platform/tmp/init027-orchestration/spine-init023/_work/task015/` (`README.txt`).
It covers the overlay sources, migrated tests, regression tests and 13 mutation overlays, and
compiles and passes against `61994fe`. It is a starting point, not a mandate.

## Design

### 0. Invariants

- **I1.** No byte below `0x20`, and no `0x7f`, reaches an SSE `id:` or `event:` line, from any
  source (API, git push, merge, replay). A data payload never adds an SSE field.
- **I2.** In shared mode, a tenant request reads and writes only process-global gateway state
  keyed by its own workspace.
- **I3.** The git trusted-CIDR bypass is evaluated against the real client. A configured proxy's
  own address never satisfies it. A git push always carries a validated bearer.
- **I4.** Only `SPINE_GIT_HTTP_DEV_NO_AUTH`, accepted only under `SPINE_ENV=development`, turns git
  read authentication off. `SPINE_DEV_MODE` has no effect on `/git`.
- **I5.** The process-wide push token is offered only to the primary repository's own `https`
  origin host, in single-workspace mode, or a binding uses its own `credentials_ref`. A
  credential-less clone or push to any other host carries no Spine credential.
- **I6.** Nothing in `core/` changes behaviour. The only `core/` change is an additive export
  function, so SMP's embedded engine, projection and workflow writer are untouched.

> **Design-check note (2026-10-06): I1, I3 and I5 are tightened; I2 is reworded.** The reasons are
> in the notes under §1, §4.1 and §5.2.
> - **I1 (amended).** No byte below `0x20`, no `0x7f`, no invalid UTF-8 and none of U+0085, U+2028
>   or U+2029 reaches an SSE `id:` or `event:` line. Those three code points never appear raw in a
>   `data:` line either.
> - **I3 (amended).** Only a *direct* peer gets the token-less bypass. A request is never
>   token-less if its TCP peer is a declared proxy (`SPINE_TRUSTED_PROXY_CIDRS`), or if it carries
>   `X-Forwarded-For`, `Forwarded` or `X-Real-IP`. No forwarding header is ever trusted for git
>   access.
> - **I5 (amended).** "Process auth" covers both the push token and the process credential helper
>   (`SPINE_GIT_CREDENTIAL_HELPER`). Inheritance is judged against the URL git will actually
>   contact:
>   - the binding's `clone_url`, for a clone;
>   - the reused clone's `origin` push URL, for fetch and push.
> - **I2 (wording).** Two pieces of state stay process-global by design: the per-client-IP rate
>   limiter and the git concurrency cap. They key on network address or capacity, not on tenant
>   identity (see Out of scope).

### 1. P3-28 — the SSE writer refuses unsafe field values

`service/internal/gateway/handlers_events_stream.go`. `writeSSEEvent` (`:169-176`) keeps its
signature, which existing tests call, and delegates to two new functions:

```go
// sseFieldSafe reports whether v can be written as a single-line SSE field value.
func sseFieldSafe(v string) bool // false if any byte < 0x20 or == 0x7f

// formatSSEEvent renders one event record.
func formatSSEEvent(id, eventType string, data []byte) []byte
```

`formatSSEEvent` does three things:
- writes `id: <id>` only when `id != "" && sseFieldSafe(id)`;
- writes `event: <type>` only when the type is non-empty and safe;
- normalises CRLF and CR in `data` to LF, then writes one `data: ` line per line (the client
  re-joins them with LF, per the WHATWG algorithm), then a blank line.

It writes into a `bytes.Buffer` and makes one `w.Write`.

- **Safe frames are byte-identical** to today's `Fprintf` output. Every engine event has an ID
  matching `[A-Za-z0-9_-]` plus the trace prefix, and its data is `json.Marshal` output or `jsonb`
  text, which never contains a raw newline.
- **Unsafe ID: omitted, not escaped** (Decision 2). The client's last-event-ID buffer is unchanged,
  so a reconnect replays from the previous event (at-least-once, no gap). The JSON `data` still
  carries the true `event_id`.
- The live loop (`:145-156`) and the replay loop (`:90-96`) log one `Warn`
  (`"sse: omitting unsafe event id"`, `event_id`, `event_type`) when `!sseFieldSafe(id)`. slog
  quotes the value.

> **Design-check note (2026-10-06): Unicode line separators.**
>
> **The gap.**
> - `sseFieldSafe` accepts every byte from `0x20` up except DEL, so U+0085 (NEL), U+2028 and
>   U+2029 pass.
> - A WHATWG parser ignores them. A client that splits lines by Unicode rules does not: for
>   example, Python `str.splitlines()`, which several SSE client libraries use. Checked
>   2026-10-06: it splits `id: a data: {"type":"run_completed"}` into two fields, so an ID
>   like that injects a frame.
> - Such IDs still reach the writer through git push and governed merge, because the §2 gateway
>   rule only covers the API.
> - The same code points also reach `data:` lines:
>   - `jsonb` text, used on replay, never escapes them;
>   - Go's `json.Marshal`, used live, escapes U+2028 and U+2029 but not U+0085.
>
> **Cheap hardening, required by this check:**
> - `sseFieldSafe` also returns false for invalid UTF-8 and for U+0085, U+2028 and U+2029. Other
>   non-ASCII is still allowed (AC 2).
> - `formatSSEEvent` rewrites those three code points in `data` as `\u0085`, ` ` and
>   ` `.
>   - Both callers pass JSON. In valid JSON these code points can only occur inside strings, so
>     the JSON value is unchanged.
>   - A frame whose data contains none of them stays byte-identical.
> - New AC 19.

### 2. P3-28 — the standalone gateway refuses unsafe workflow identifiers

`service/internal/gateway/handlers_workflows.go`:

```go
var workflowIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// checkWorkflowIdentifiers returns ErrValidationFailed (422) with one
// domain.ValidationError{RuleID:"schema", Field:…, Severity:"error"} per bad identifier.
func checkWorkflowIdentifiers(body string) error
```

- It parses with `workflow.Parse`. When the body does not parse, it returns nil and leaves the
  error to the service's existing contract. If TASK-011 has landed, it uses the decoder TASK-011
  introduces (`workflow.ProjectionDefinition`) instead, so both see the same document.
- **Checked fields** (empty values are left to core's "required field missing"):
  `steps[i].id`, `divergence_points[i].id`, `divergence_points[i].branches[j].id`,
  `convergence_points[i].id`.
  - Convergence-point IDs do not reach an event ID at `61994fe`. They are included for symmetry
    and are cheap.
- **Call sites:**
  - `handleWorkflowCreate` right after the `id/body required` check (`:40-43`). That is before
    the planning-run dispatch (`:60-88`), so the planning-run path and the direct path refuse the
    same bodies.
  - `handleWorkflowUpdate` right after `body required` (`:151-154`). That is before TASK-014's new
    dispatch.
  - `handleWorkflowValidate` appends the same errors to `result.Errors` and sets
    `Status="failed"` (after `:289`), so `/validate` never passes a body that `POST`/`PUT` refuse.
- **Not in `core/workflow`** (Decision 3). Already projected or loaded workflows, every SMP
  workflow, and SMP's `WorkflowWriter` (which wraps `workflow.Service`) are unaffected. All 8
  workflows shipped in Spine `workflows/` and all 7 in SMP `workflows/` pass. This was probed on
  2026-10-06, and a committed guard test covers the Spine set.

> **Design-check note (2026-10-06): the rule breaks every standalone deployment's API, not only dev
> setups.**
>
> **Why.**
> - No step-ID format is documented today: `architecture/workflow-definition-format.md` has none.
>   Customer workflows may therefore already use upper case or `.`.
> - Such a workflow keeps loading and running, because §2 is not in core.
> - But `PUT /workflows/{id}` on it is refused until its identifiers are renamed. Renaming is safe
>   for in-flight runs, which are pinned to their version. The `422` already names each field and
>   the pattern.
> - API bodies cannot hide a different identifier from this check. `workflow.Parse` is strict
>   (`yamlsafe.DecodeIntoStrict`, `KnownFields(true)`), so a case-variant key such as `ID:` makes
>   the body unparsable. Both the direct path and `StartWorkflowPlanningRun` (`core/engine/run.go:304-312`)
>   then refuse it. Returning nil on a parse error is therefore safe.
>
> **Changes.**
> - `CHANGELOG.md` gets a general `Breaking` entry for the identifier rule, not only
>   `Breaking (dev setups)`.
> - `architecture/workflow-definition-format.md` documents the pattern for `steps[].id`,
>   `divergence_points[].id`, `branches[].id` and `convergence_points[].id`.
> - Owner question 7 asks whether to allow upper case and `.`. Neither is unsafe for SSE.

### 3. P3-29 — shared-mode scoping of gateway state

**3.1 Metrics.**
- `core/observe/export.go` gains an additive function:
  `func ExportPrometheusWorkspace(workspaceID string) string`. It renders only the
  `spine_workspace_pool_*` gauges and counters whose `workspace_id` equals the argument, with no
  process-global series.
  - `writePoolMetrics` (`:111`) gains a `keep func(id string) bool` filter.
  - `ExportPrometheus` passes `nil`, so its output is unchanged.
- `handleMetrics` (`handlers_system.go:134-141`):
  - When `s.servicePool != nil` (shared mode), it serves
    `ExportPrometheusWorkspace(WorkspaceConfigFromContext(ctx).ID)`.
  - A missing workspace config returns `400`. That is unreachable past `authMiddleware`.
  - Otherwise it serves the unchanged full export.
- **New platform route.** `GET /api/v1/platform/metrics` (`handlePlatformMetrics`, full export)
  sits in the existing `operatorTokenMiddleware` group (`routes.go:41-47`), next to
  `/workspaces`. It authenticates with `SPINE_OPERATOR_TOKEN`; when that is unset it returns 503
  "operator token not configured". Shared-mode deployments keep one full scrape target.

**3.2 Rebuild registry.**
- `rebuildRegistryKey(ctx, rebuildID)` returns `cfg.ID + "\x00" + rebuildID` when a workspace is
  resolved, and the bare ID otherwise.
- `Store` (`:97`) and `Load` (`:124`) both use it. A foreign workspace's ID returns `404`.
- The `rebuild_id` wire format (`rb-xxxxxxxx`) is unchanged.

**3.3 SSE limiter.**
- `sseLimiterKey(ctx, actorID)` returns `cfg.ID + "\x00" + actorID` when a workspace is resolved,
  and the bare actor ID otherwise.
- `handleEventStream` acquires and releases with the same captured key (replacing `:46-55`).
- `sseLimiter` itself is unchanged, and the cap stays `SPINE_SSE_MAX_CONN_PER_ACTOR` (default 5),
  now per workspace.

### 4. P3-30 — git-endpoint authentication

**4.1 Real peer.** New in `handlers_git.go`:

```go
// gitPeerIP returns the client address the trusted-CIDR bypass is evaluated against, or nil.
func gitPeerIP(r *http.Request, trustedProxies []*net.IPNet) net.IP
```

- If `RemoteAddr` is not in `SPINE_TRUSTED_PROXY_CIDRS`, the peer is `RemoteAddr`. Behaviour is
  unchanged for direct connections.
- If it is a proxy:
  - the peer is the right-most hop across **all** `X-Forwarded-For` lines that is not itself a
    trusted proxy;
  - a missing header, an all-proxy chain, or an unparsable hop yields `nil` (fail closed).
  - This differs from `clientIPForRateLimit`, which correctly falls back to `RemoteAddr` for
    bucketing (Decision 6).
- The gate becomes:

```go
trustedBypass := false
if !push {
    if ip := gitPeerIP(r, s.trustedProxyCIDRs); ip != nil {
        trustedBypass = s.gitHTTP.IsTrustedIP(ip.String())
    }
}
devBypass := !push && s.gitDevNoAuth
authBypass := pushDisabledShortCircuit || trustedBypass || devBypass
```

- The gate comment (`:73-92`) is rewritten.
- `IsTrustedIP` (`githttp/handler.go:166`) is unchanged: it already accepts a bare IP.
- A runner reached **through** a configured proxy is now judged by its forwarded address. That is
  the intended reading of "real peer".

> **Design-check note (2026-10-06): §4.1 as written leaves two token-less paths open.** Both were
> reproduced against the prototype with a probe overlay on `ov_all_fix.json`
> (`go test -overlay … -run TestDesignCheck_ ./service/internal/gateway/`).
>
> 1. **Undeclared proxy: the audit's literal scenario.**
>    - `gitPeerIP` returns `RemoteAddr` whenever the peer is not in `SPINE_TRUSTED_PROXY_CIDRS`.
>    - That variable is documented today for the rate limiter only (`.env.example:48-56`) and is
>      unset by default.
>    - So a reverse proxy at `172.28.0.1`, inside the git CIDRs and relaying
>      `X-Forwarded-For: 203.0.113.9`, still gets **200** with no token.
>    - The fix depends on the operator reading the new docs. The overlap `Warn` cannot help: it
>      fires only when proxies are declared.
> 2. **Declared proxy that passes the client's header through.**
>    - A proxy that forwards the client's own `X-Forwarded-For` unchanged lets the client name a
>      runner address. nginx forwards request headers by default, so an nginx config that sets
>      only `X-Real-IP` behaves this way.
>    - Example: peer `10.0.0.2` (declared) with client-supplied `X-Forwarded-For: 172.28.0.5`
>      gets **200**. That is exactly `TestP330_RunnerBehindProxyJudgedByForwardedAddress`.
>    - Today `SPINE_TRUSTED_PROXY_CIDRS` only picks a rate-limit bucket. Under §4.1 it would also
>      grant repository read access, a far larger blast radius for the same setting.
>
> **Amended gate.** It replaces the `gitPeerIP` algorithm above; the function signature and call
> site are unchanged.
> - If `RemoteAddr` is in `SPINE_TRUSTED_PROXY_CIDRS`, return `nil`. A proxied request is never
>   token-less.
> - If the request carries any `X-Forwarded-For`, `Forwarded` or `X-Real-IP` header, return
>   `nil`. Such a request came through a proxy the operator did not declare.
>   - This closes case 1 for every proxy that adds one of those headers.
>   - A direct git client sends none of them, and a client that adds one only loses the bypass.
>   - Log one rate-limited `Warn` naming `SPINE_TRUSTED_PROXY_CIDRS`.
> - Otherwise return the parsed `RemoteAddr`.
>
> **Consequences.**
> - `forwardedHops` (§4.2) stays, for the rate limiter only.
> - A runner behind a proxy presents a token, as every non-runner client already does. Trusting
>   its forwarded address instead is Owner question 6. That option would need its own variable,
>   documented as "the proxy must append or overwrite `X-Forwarded-For`".
> - What remains is a TCP-level proxy that adds no header, such as Docker's userland proxy. Code
>   cannot see it, so §4.4 (the compose default) and the docs cover it.
> - Affected sections: §8 behaviour table, Decision 6, AC 8 (amended), AC 20 (new).

**4.2 One XFF reader.**
- New `forwardedHops(r)` in `ratelimit.go` joins `r.Header.Values("X-Forwarded-For")` and splits on
  commas.
- `clientIPForRateLimit` (`:115-131`) and `gitPeerIP` both use it. The rate limiter now buckets on
  the proxy-observed client when the proxy adds its own header line.

**4.3 Dev flag, decoupled from `SPINE_DEV_MODE`.**
- `gateway.Server` gains `gitDevNoAuth bool`, and `ServerConfig` gains `GitHTTPDevNoAuth bool`
  with a startup `Warn` in `NewServer`. `devMode` no longer appears in `handleGit`.
  `SPINE_DEV_MODE` keeps its API meaning (`authorize`, `middleware.go:253-267`).
- `service/cmd/spine/serve_config.go` adds:
  - `gitHTTPDevNoAuthEnabled()`: `SPINE_GIT_HTTP_DEV_NO_AUTH` ∈ {`1`,`true`,`yes`,`on`},
    case-insensitive.
  - `guardGitHTTPDevNoAuth(env, on) error`: returns an error unless
    `env == "development"` (Owner question 1).
  - `overlappingCIDRs(gitTrusted []string, proxies []*net.IPNet) []string`.
- `serve_cmd.go`:
  - calls the guard right after `guardDevModeEnv` (`:43-45`), before any DB or secret work, so
    a misconfiguration fails at startup;
  - parses `SPINE_GIT_HTTP_TRUSTED_CIDRS` once;
  - logs a `Warn` naming any overlap with `SPINE_TRUSTED_PROXY_CIDRS`, because token-less clones
    from the overlap stop working;
  - passes `GitHTTPDevNoAuth` through `serveDeps` (`serve_wiring.go:157-163`) into `ServerConfig`
    (`:459`).
- **Push semantics.** The flag never covers a push: pushes always present a bearer, which is
  validated and attached as the actor. This also satisfies TASK-016's request that a bound actor
  can push in development.

**4.4 Compose and environment defaults.**
- `docker-compose.yaml` drops `SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.0.0/16` (`:30-31`), so tokens
  are required by default (Owner question 4). The compose file defines no runner service, and no
  script or Make target relies on token-less clone (checked 2026-10-06).
- `docker-compose.override.yaml.example` gains a commented recipe:
  - pin runner containers to `ipam.config[].ip_range: 172.28.1.0/24`, which excludes the
    gateway `172.28.0.1`;
  - set `SPINE_GIT_HTTP_TRUSTED_CIDRS=172.28.1.0/24`.
- `.env.example` (`:25-56`):
  - documents the bridge-gateway caveat and the proxy-overlap rule;
  - adds `SPINE_GIT_HTTP_DEV_NO_AUTH`.

> **Design-check note (2026-10-06): `ip_range` applies to the whole network, not to runner
> containers.**
> - Setting `ipam.config[].ip_range: 172.28.1.0/24` on `spine-internal` makes every container on
>   that network allocate from `172.28.1.0/24`: `spine`, `spine-db` and the runners.
> - The recipe still excludes the bridge gateway, which is its purpose, but it does not "pin
>   runner containers". The override example must say so.
> - It should also offer the tighter options:
>   - a dedicated runner network whose `ip_range` excludes its gateway;
>   - per-runner `ipv4_address` values, each listed as a `/32` in
>     `SPINE_GIT_HTTP_TRUSTED_CIDRS`.

### 5. Latent gitpool item — process token scoped to the primary host

**5.1 `adapters/git/cli.go` (additive).**
- `func WithoutPushToken() CLIOption` clears `pushToken` and `pushUsername`, so `NewCLIClient`
  creates no askpass. It mirrors `WithoutCredentialHelper` (`:63-67`).
- `func (c *CLIClient) RemoteURL(ctx context.Context, remote string) (string, error)` runs
  `git remote get-url -- <remote>`, so `insteadOf` rewrites apply. It returns `""` with a nil
  error on "No such remote".
- Neither is added to the `GitClient` interface (`client.go:11-32`), which SMP implements.
- A configured credential helper is left alone. Git helpers are keyed by host, so they cannot
  answer for a foreign host.

> **Design-check note (2026-10-06): read the push URL.**
> - `git remote get-url -- origin` returns the *fetch* URL.
> - The process token is fed only to clone and push (`cli.go:304-321`, `:650-668`). A push goes to
>   `remote.origin.pushurl`, or through a `pushInsteadOf` rewrite, when one is set, and that can
>   name a different host. Checked with git 2.54: `get-url` and `get-url --push` return different
>   URLs for such a repository.
> - So the helper becomes `RemoteURL(ctx, remote string, push bool)`, running
>   `git remote get-url [--push] -- <remote>`.
>   - §5.3 reads the primary's push URL.
>   - §5.2 (amended) reads the code clone's push URL.
> - Detect "no such remote" by git's exit status 2, not by matching stderr text: the text is
>   translated under a non-English `LANG`.
> - The note under §5.2 withdraws "a configured credential helper is left alone" (item 2 there).

**5.2 `adapters/gitpool`.**
- `Credential` (`credentials.go:23-33`) gains `InheritProcessAuth bool`. The zero value means "no
  process token", which is what the `Cloner` contract (`pool.go:70`) always promised.
- New `Option WithProcessAuthHosts(hosts ...string)`:
  - each entry is an `https` URL or a bare `host[:port]`;
  - the canonical key is the lower-cased host without a trailing dot, plus the effective port
    (443 when omitted);
  - entries that are not `https` are ignored.
- `Pool.inheritsProcessAuth(cloneURL)` is true only for an `https://` clone URL whose key is in
  the set.
- The pool sets `cred.InheritProcessAuth = cred.IsEmpty() && p.inheritsProcessAuth(repo.CloneURL)`:
  - right after `resolveCredential` (`pool.go:288`), so no `CredentialResolver` can opt a binding
    in;
  - and in the pass-through branch (`:262`).
- `NewCLIClientFactory` (`:566-582`) and `cliCloner.Clone` (`:608-630`) append
  `git.WithoutPushToken()` when `cred.IsEmpty() && !cred.InheritProcessAuth`. The per-binding
  branch is unchanged.
- The cache key (`:270`, `:308`) needs no change. Inheritance is a pure function of `CloneURL`
  (covered by `UpdatedAt`) and the pool's fixed host set.

> **Design-check note (2026-10-06): two gaps in §5.2.**
>
> **1. Bypass, reproduced against the prototype: a reused clone pushes to its own origin, not to
> `clone_url`.**
> - `ensureClone` reuses any existing working tree at `LocalPath` and never checks its `origin`
>   (`pool.go:334-337`). Inheritance, by contrast, is computed from the binding's `CloneURL`.
> - Attack path:
>   1. A binding is cloned from `https://attacker.example/x.git`.
>   2. Its `clone_url` is updated to the primary host, keeping the same `local_path`.
>      `repository.update` needs only operator (`core/auth/permissions.go:102`).
>   3. The new `UpdatedAt` causes a cache miss, `InheritProcessAuth` becomes true, and the clone
>      is reused.
>   4. The engine's `client.Push(ctx, "origin", …)` (`core/engine/multi_repo_merge.go:237`,
>      `multi_repo_branch.go:145`) sends the process token to the attacker.
> - A second binding whose `local_path` points at the first binding's clone does the same, with no
>   update at all.
> - The probe `TestDesignCheck_ExistingCloneOriginMismatch` (overlay on `ov_all_fix.json`,
>   2026-10-06) failed with `BYPASS: process token pushed to the reused clone's foreign origin`.
>
> **Fix.**
> - After `ensureClone`, when `InheritProcessAuth` would be true, read the clone's push URL with
>   `RemoteURL(ctx, "origin", true)` on a bare `git.NewCLIClient(repo.LocalPath)`.
> - Keep inheritance only if that URL is `https` and its `hostKey` is in the set. A read error
>   means no inheritance.
> - Apply the same check in pass-through mode (`:262`).
> - The cache key is still enough. Changing a clone's origin takes a re-clone or a binding
>   update, and either one changes `UpdatedAt` or the path.
> - New AC 21.
>
> **2. The process credential helper is a platform credential too.**
> - §5.2 strips only the token. `SPINE_GIT_CREDENTIAL_HELPER` (allowlist `cache`, `store`,
>   `osxkeychain`, `manager`, `pass`; `cli.go:140-146`) still rides every code-repo clone and push
>   as `-c credential.helper=<helper>` (`cli.go:281-288`, `:642-643`).
> - Host-keyed helpers do not answer a foreign host. In shared mode, though, they answer for the
>   platform's own host. A tenant binding naming `https://github.com/<other-org>/private.git` is
>   then cloned with the platform's stored GitHub credential: the confused deputy that Decision 9
>   rejects for the token.
>
> **Fix.**
> - Wherever §5.2 appends `git.WithoutPushToken()` (`cred.IsEmpty() && !cred.InheritProcessAuth`),
>   also append `git.WithoutCredentialHelper()`.
> - In single mode, helper credentials then reach code repos only on the primary's push host,
>   the same rule as the token. Code repos on other hosts use `credentials_ref`; nothing
>   reachable today depends on another route.
> - Helpers configured in the global or system git config, outside Spine's options, are not
>   touched. See Out of scope.
> - New AC 22.

**5.3 Wiring.**
- `service/cmd/spine/serve_wiring.go` gains a helper,
  `codeRepoProcessAuthHosts(ctx, primary *git.CLIClient, shared bool, log) []string`:
  - in shared mode (`deps.WSServicePool != nil`) it returns nil;
  - otherwise it returns the primary's `origin` URL, read with `RemoteURL` (non-`https` origins
    are later ignored by `hostKey`);
  - a read error is logged, and the result is nil.
  - The result is passed as `gitpool.WithProcessAuthHosts(...)` to the top-level pool
    (`:264-273`).
  - The origin URL itself is never logged, because it may embed credentials.
- `service/internal/workspace/pool.go` `buildServiceSet` passes **no** hosts (Decision 9). Only its
  comment changes: `:863-889` currently promises that credential-less bindings "still work"
  through the process token.

**5.4 `RedactCloneURL`** (`adapters/repository/clone_url.go:108-121`).
- For an `https` URL it removes all userinfo. For other schemes it removes userinfo only when a
  password is present.
- SSH login names (`ssh://git@…`, `git+ssh://git@…`) and the SCP form are kept.
- The doc comment (`:100-107`) is updated.
- Stored values are untouched. Only the response shape changes.

### 6. Existing tests that change

| Test | Change |
|---|---|
| `service/internal/gateway/handlers_git_test.go` | Six tests set `devMode: true` (`:270`, `:328`, `:973`, `:1030`, `:1077`, `:1191`); five depend on it to bypass git auth. The four read tests (`:973`, `:1030`, `:1077`, `:1191`) switch to `gitDevNoAuth: true`. `TestHandleGit_PerWorkspacePolicyInvoked` (push, `:328`) presents a bearer validated by a stub auth store. `TestHandleGit_PushFlagOff` (`:270`) needs neither, because the flag-off short-circuit runs first. |
| `service/scenariotest/scenarios/git_http_per_repo_test.go:182`, `runner_clone_context_test.go:195` (`//go:build scenario`) | `DevMode: true` → `GitHTTPDevNoAuth: true`. Both only clone. |

No other test changes. The prototype runs all of `go test ./...` green with only these edits.

> **Design-check note (2026-10-06): §6 misses the push scenario suite, so "no other test changes"
> is wrong.** The prototype's `go test ./...` never runs the `//go:build scenario` files against a
> database, and CI runs only `go test ./...` (`.github/workflows/ci.yml:67`).
>
> **Eight push scenarios break.**
> - They live in `service/scenariotest/scenarios/git_http_serve_test.go` and push anonymously
>   under `DevMode: true` (helpers at `:78` and `:878`; `ServerConfig.DevMode` at `:136` and
>   `:926`):
>   - `TestGitHTTP_PushAcceptedWhenFlagOn` (`:555`);
>   - `TestGitHTTP_PushRejectedByPreReceivePolicy` (`:651`);
>   - `TestGitHTTP_PushOverrideHonouredForOperator` (`:776`);
>   - `TestGitHTTP_PushDeleteRejectedByNoDelete` (`:1008`);
>   - `TestGitHTTP_PushOverrideRejectedForContributor` (`:1058`);
>   - `TestGitHTTP_UnusedOverrideEmitsNoEvent` (`:1127`);
>   - `TestGitHTTP_MixedPushRejectedAllOrNothing` (`:1186`);
>   - `TestGitHTTP_PushedCommitIsByteIdentical` (`:1255`).
> - After this task every one of those pushes gets `401`.
> - The actor-injection wrapper (`:933-938`) does not help: `validateGitAuth` reads only the
>   `Authorization` header.
>
> **Migration.**
> - Mint a token with `auth.Service.CreateToken` for an actor with the role each scenario needs:
>   - operator, for the override scenarios;
>   - contributor, for `…RejectedForContributor`;
>   - contributor or operator, everywhere else.
>
>   Other scenarios already mint tokens this way (`gateway_integration_test.go`).
> - Push with `git -c http.extraHeader="Authorization: Bearer <token>"`, as TASK-016 AC 12 does.
> - Drop the wrapper and the `devMode` parameter from both helpers.
> - Clones keep using the `127.0.0.0/8` trusted CIDR. That is a direct peer with no forwarding
>   header, so the §4.1 amendment does not affect it.
>
> **TASK-016's scenarios.**
> - `TestGitHTTP_PushReservedRefRejectedAnonymously` (TASK-016 AC 13) pushes anonymously in
>   devMode. It becomes "an anonymous push gets `401`, and the server lacks the ref". TASK-016
>   AC 12 already proves the reserved-ref refusal for a bound contributor.
> - TASK-016's `feature/push` edit to `TestGitHTTP_PushAcceptedWhenFlagOn` is kept.
>
> **Before merging.** Re-run `grep -rn "devMode\|DevMode" --include='*_test.go' service` at this
> task's merge base, and list in the Implementation Notes every hit that relies on a git auth
> bypass. AC 16 is amended to run the scenario suite explicitly, and AC 23 is new.

### 7. Data, format and migration

**No change:**
- runtime or registry schema, migrations, event catalogue;
- `rebuild_id` format, webhook payloads, push wire responses.

**New:**
- environment variable `SPINE_GIT_HTTP_DEV_NO_AUTH`;
- route `GET /api/v1/platform/metrics`.

**Exported Go API, all additive:**
- `observe.ExportPrometheusWorkspace`;
- `git.WithoutPushToken`, `(*git.CLIClient).RemoteURL`;
- `gitpool.WithProcessAuthHosts`, `gitpool.Credential.InheritProcessAuth`;
- `ServerConfig.GitHTTPDevNoAuth`, in an internal package.

### 8. Behaviour changes (standalone)

| Surface | Before (`61994fe`) | After |
|---|---|---|
| SSE frame for an engine event | `id/event/data` | byte-identical |
| SSE event whose ID holds a control byte | raw ID, injected frames | no `id:` line, one event, a `Warn` |
| `POST`/`PUT /workflows` with an identifier outside `[a-z0-9][a-z0-9_-]{0,63}` | accepted | `422 validation_failed`, nothing written, no planning run |
| `POST /workflows/{id}/validate`, same body | `passed` | `failed` with per-field errors |
| `/system/metrics`, shared mode | all tenants and globals | the caller's `spine_workspace_pool_*` series only |
| `/system/metrics`, single mode | full | unchanged |
| `/api/v1/platform/metrics` | 404 | full export, `SPINE_OPERATOR_TOKEN` |
| `/system/rebuild/{id}` from another workspace | 200 (and overwritable) | 404 |
| SSE cap, shared mode | per actor ID across tenants | per (workspace, actor) |
| `/git` read from a configured proxy address | trusted if the proxy IP is in the git CIDRs | judged by XFF; no usable XFF means a token is required |
| `/git` read from a runner behind a configured proxy | token required | trusted if the forwarded address is in the git CIDRs |
| `SPINE_DEV_MODE` on `/git` | no auth for clone and push | no effect |
| `SPINE_GIT_HTTP_DEV_NO_AUTH=1` | n/a | reads without a token; refused at startup unless `SPINE_ENV=development` |
| Rate-limit bucket, multi-line XFF | first line | right-most non-proxy hop over all lines |
| Compose default | `/16` token-less | tokens required |
| Credential-less code repo (dormant) | process token sent to any host | never, except the single-mode primary origin host |
| `GET /repositories` `clone_url` with an https username token (dormant) | echoed | redacted |

**Breaking for local standalone dev setups:** anyone relying on `SPINE_DEV_MODE` for
token-less git, or on the compose `/16`, must set `SPINE_GIT_HTTP_DEV_NO_AUTH=1` and
`SPINE_ENV=development`, or present a token. This goes in the CHANGELOG `Security` and
`Breaking` entries.

> **Design-check note (2026-10-06): rows that the amendments change.**
>
> | Surface | Before (`61994fe`) | After (amended) |
> |---|---|---|
> | `/git` read from a configured proxy address | trusted if the proxy IP is in the git CIDRs | token required; no forwarded address is trusted |
> | `/git` read from a runner behind a configured proxy | token required, unless the proxy IP itself was trusted | token required. The original "trusted by forwarded address" is Owner question 6 |
> | `/git` read from a direct peer that sends `X-Forwarded-For`, `Forwarded` or `X-Real-IP` | trusted if the peer is in the git CIDRs | token required, and a rate-limited `Warn` |
> | SSE `id:`/`event:` with U+0085, U+2028, U+2029 or invalid UTF-8 | written raw | omitted |
> | SSE `data:` with U+0085, U+2028 or U+2029 | written raw | written as `\uXXXX` escapes; the JSON value is unchanged |
> | Credential-less code repo and the process credential helper (dormant) | helper used on every host | helper used only on the single-mode primary push host |
> | Credential-less code repo whose reused clone's origin is foreign (dormant) | process token sent | never sent |
> | `POST`/`PUT /workflows` with an identifier outside the rule | accepted | `422`. Breaking for **all** standalone deployments (note under §2) |

### 9. SMP impact

**None expected; no dependent SMP task.**
- SMP never imports `service/` or `adapters/gitpool`. Its lint, `dev/lint-spine-internals.sh`,
  forbids `service/`.
- SMP imports `adapters/git`, `core/observe` and `adapters/repository`. It gains only additive
  symbols there.
- SMP never calls `RedactCloneURL`, `ValidateCloneURL`, `WithPushToken` or
  `observe.ExportPrometheus` (grep, 2026-10-06).
- SMP's own gateway (`internal/customer/githttp`), SSE, metrics and workflow writer are untouched.
  Core workflow validation does not change.

**Compatibility gate** (run against the prototype on 2026-10-06; repeated on the task branch,
AC 15):
`go build -mod=readonly ./cmd/... ./internal/...`,
`go vet -mod=readonly ./internal/customer/...`, and
`go test ./internal/customer/spinecore/ ./internal/customer/githttp/` all pass with
`replace ../spine` at the prototype.

The change reaches SMP only through SMP TASK-012's `.spine-revision` bump.

> **Design-check note (2026-10-06): correction.** SMP *does* call `git.WithPushToken`, at
> `internal/customer/spinecore/workflowwriter.go:335`, `artifactservice.go:306` and
> `artifactservice.go:447`. It uses it to build a token-authenticated `CLIClient` for its own
> gateway. The conclusion still holds:
> - `WithPushToken`, `NewCLIClient`, `Clone` and `run` are unchanged.
> - The new `WithoutPushToken` and `RemoteURL` are opt-in. The §5 amendments change no existing
>   `adapters/git` behaviour.
> - From `adapters/repository`, SMP uses only `repository.PrimaryRepositoryID` and `PrimarySpec`.
>   From `core/observe`, it uses only the context, trace and logger helpers (grep, 2026-10-06).
> - SMP's own `/git/{workspace_id}` gateway (`internal/customer/githttp`) has no trusted-CIDR or
>   dev-mode bypass, so P3-30 has no SMP counterpart.
> - SMP still needs no dependent task. The compatibility build
>   (`go build -mod=readonly -overlay ov_src_only.json ./cmd/... ./internal/...`) passed again on
>   2026-10-06.

### 10. Coordination and ordering

- **TASK-016 (hard; `blocked_by`).**
  - It edits `handleGit` (repository ID and run-ref context, around `:196` and `:227`). This task
    edits the auth gate (`:73-104`) and appends `gitPeerIP`. The conflict is mechanical.
  - TASK-016's `refadmit` refuses reserved refs for a nil actor. This task removes the only
    anonymous-push path (`devMode`), so reserved-ref pushes always carry an actor.

  > **Design-check note (2026-10-06):** removing that path also breaks TASK-016's anonymous devMode
  > scenario and the eight existing push scenarios. This task migrates them; see the note under §6
  > and AC 23.
- **TASK-014 (same wave).**
  - Both edit `handlers_workflows.go`. Keep `checkWorkflowIdentifiers` immediately after the
    body checks, before TASK-014's update planning dispatch, `refuseIfWorkflowRunOpen` and
    `resolveWorkflowWriteContext`.
  - Both edit `server.go` (different fields). Whichever lands second rebases.
- **TASK-013.** `serve_config.go` and `serve_cmd.go` are touched next to `validateOperatorToken` and
  the dev-mode guard. Textual conflicts only.
- **TASK-011.** Its write-path decoder (`workflow.ProjectionDefinition`) should be used by
  `checkWorkflowIdentifiers` if it has landed. This keeps TASK-011's rule: content checks at create
  and update only, never at projection.
- **TASK-009.** It touches `service/internal/workspace/pool.go` (`PoolConfig`); this task touches
  only a comment there.
- **TASK-001.** It closes the NUL-ref push (F1). Until it lands, this task's removal of anonymous
  dev pushes narrows F1's reach in dev deployments.
- **Wave 3, risk medium.** No migration. No SMP change.

### 11. Documentation

- **`docs/integration-guide.md`:**
  - §6.2 SSE: the cap is per (workspace, actor) in shared mode, and an event whose ID contains a
    control character is sent without an `id:` line.
  - §7 Authentication (`:465-470`): the real-peer rule, the proxy-overlap rule, "pushes always need
    a bearer", and `SPINE_GIT_HTTP_DEV_NO_AUTH`.
  - Environment table (`:608-614`): `SPINE_DEV_MODE` no longer affects `/git`; a new
    `SPINE_GIT_HTTP_DEV_NO_AUTH` row; `SPINE_TRUSTED_PROXY_CIDRS` now also feeds the git trust
    check.
  - A note on `/api/v1/platform/metrics`.
- **`README.md`** (`:49-59`): the same two env rows.
- **`KNOWN-LIMITATIONS.md` "Auth Bypass in Dev Mode"** (`:66-69`): rewritten. That stale text
  predates `SPINE_DEV_MODE`. The new text describes the API flag and the git read flag.
- **`CHANGELOG.md`:** a `Security` entry for each item, and a `Breaking (dev setups)` entry.

## Decisions

1. **DECIDED — Fix P3-28 at the SSE writer; the gateway identifier rule is defence in depth.**
   Workflows reach `main` by API, git push and governed merge, and are loaded from projection. Only
   the writer sees every source, including replay.
   - *Rejected: validating identifiers at the source only.* It misses push, merge and
     already-stored rows.
2. **DECIDED — Omit an unsafe `id:`/`event:` line; do not escape it, drop the event, or
   percent-encode it.**
   - *Rejected: escaping or encoding.* An escaped ID is unknown to `ListEventsAfter`, and an
     unknown cursor replays **nothing** (`postgres_delivery.go:242-245`), turning one bad event
     into a silent gap. Omission leaves the client cursor in place, so a reconnect re-delivers
     (at-least-once).
   - *Rejected: dropping the event.* It loses a legitimate state change.
   - The true ID stays in the JSON data.

   > **Design-check note (2026-10-06): what omission costs.**
   > - An omitted `id:` never advances the client's cursor.
   > - If more than `sseReplayCap` (100) unsafe-ID events follow the last safe one, every reconnect
   >   replays the same first 100 and never catches up through replay. Live events still arrive.
   > - Once §2 is in place, only git push or a governed merge can introduce such a workflow, so
   >   this degraded delivery is accepted. It is recorded in `KNOWN-LIMITATIONS.md` together with
   >   the §11 SSE text.
3. **DECIDED — Gateway-only identifier charset, `^[a-z0-9][a-z0-9_-]{0,63}$`.**
   - *Rejected: `core/workflow` `ValidateSchema`.* It would change SMP's `WorkflowWriter` and the
     projection/load path, and refuse already-stored customer workflows. That conflicts with I6 and
     with TASK-011's "content checks at create/update, never at projection".
   - *Rejected: normalising identifiers.* It changes execution IDs.
   - **Length cap 64.** It matches the trace-ID and repository-ID caps and bounds event-ID growth.
     The longest shipped step ID is 18 characters.
4. **DECIDED — Scope metrics by "a `ServicePool` is wired", not by "a workspace config is
   present".** File mode also resolves a workspace config, and single-tenant scrapes must keep
   the global series.
   - *Rejected: keeping global counters in the tenant view.* They are an all-tenant activity side
     channel (Owner question 3).
   - *Rejected: removing metrics in shared mode.* That would leave operators blind.
   - The platform route keeps a full scrape target.
5. **DECIDED — Composite registry keys; keep the `rb-xxxxxxxx` format.**
   - *Rejected: random rebuild IDs.* That changes the documented response, and the workspace key
     alone closes the cross-tenant path.
6. **DECIDED — `gitPeerIP` is separate from `clientIPForRateLimit`, and they share
   `forwardedHops`.** The limiter must fall back to `RemoteAddr` (some bucket is better than none).
   The trust check must fail closed.

   > **Design-check note (2026-10-06): partly superseded by the §4.1 note.**
   > - `gitPeerIP` no longer reads `X-Forwarded-For`. It refuses every proxied request and every
   >   request that carries a forwarding header.
   > - `forwardedHops` now serves only the rate limiter.
7. **DECIDED — A dedicated, read-only, development-only git flag.**
   - *Rejected: keeping `devMode` and adding a production guard.* That is today's behaviour, and the
     gap is precisely `SPINE_ENV` unset or `staging`.
   - *Rejected: anonymous pushes under the flag.* Push admission (TASK-016), branch protection and
     audit all need an actor. A local developer can mint a token.
8. **DECIDED — The pool decides inheritance, and signals it through
   `Credential.InheritProcessAuth`.**
   - *Rejected: adding the clone URL to `ClientFactory`'s signature.* It would churn 54 call sites
     and fakes.
   - *Rejected: a host-aware askpass script or URL-scoped credential helper in `adapters/git`.* It
     changes every push path, including the primary. It is kept as the redirect-hardening
     follow-up.
   - *Rejected: no inheritance at all.* That is Owner question 2. Kept as an option, but the
     single-tenant convenience is documented (`workspace/pool.go:876-880`).
9. **DECIDED — Shared-mode pools never inherit.** The process token is a platform credential.
   Even on the primary's own host (for example `github.com`), a tenant binding naming
   `github.com/<other-org>/private` would make Spine clone with the platform's authority and serve
   the result through `/git/{ws}/{repo}`. That is a confused deputy across tenants.

   > **Design-check note (2026-10-06):** the same reasoning applies to the process credential
   > helper (`SPINE_GIT_CREDENTIAL_HELPER`). Note 2 under §5.2 strips it alongside the token.
10. **DECIDED — Redact all https userinfo; do not reject it at `ValidateCloneURL`.**
    - *Rejected: rejecting userinfo at validation.* It would refuse provider URLs with a
      non-secret username (for example Azure DevOps `https://org@dev.azure.com/…`) and existing
      bindings. The display loses only a username.
11. **DECIDED — Include the multi-line XFF fix in the rate limiter.** It is the same defect class
    and a two-line change, and P3-30's helper needs the reader anyway.

## Owner questions

The design works without answers to these, using the stated defaults.

1. **How strict is `SPINE_GIT_HTTP_DEV_NO_AUTH`?**
   - *Default:* accepted only with `SPINE_ENV=development`. Startup fails otherwise, including
     when `SPINE_ENV` is unset.
   - *Alternative:* mirror `SPINE_DEV_MODE` (refused only under `production`). That is looser,
     and it is the condition the audit flagged.
2. **May a credential-less code repo in single-workspace mode use the process push token on the
   primary's own `https` origin host?**
   - *Default:* yes, on an exact host and port match only. This keeps the convenience documented
     at `workspace/pool.go:876-880`.
   - *Alternative:* never. Every private code repo then needs `credentials_ref`. That is simpler,
     and nothing reachable today depends on it.
   - Shared mode never inherits, whatever the answer (Decision 9).

   > **Design-check note (2026-10-06): what "yes" grants, even in single mode.**
   > - Any operator can bind any repository on the primary's host (`repository.create` and
   >   `repository.update` are operator, `core/auth/permissions.go:100-102`). If the process token
   >   can read that repository, the operator can then read it through `/git/{ws}/{repo}`.
   > - For a GitHub token with org-wide scope, that means every private repository in the org.
   > - If operators are not meant to have the token's full reach, choose "never". It is also the
   >   smaller change: no `WithProcessAuthHosts`, no origin reads, and no AC 21.
   > - With "yes", the amendments under §5.1 and §5.2 are mandatory.
3. **What does a tenant operator see at `/system/metrics` in shared mode?**
   - *Default:* only its own `spine_workspace_pool_*` series. The full export moves to
     `GET /api/v1/platform/metrics` behind `SPINE_OPERATOR_TOKEN`.
   - *Alternative:* also keep the process-global aggregate counters, which disclose platform-wide
     activity but no tenant names.
   - The route name is also the owner's call.
4. **Should the compose file keep token-less clones?**
   - *Default:* no. The `/16` line is removed, and the override example documents an `ip_range`
     recipe that excludes the bridge gateway.
   - *Alternative:* ship that recipe as the default.

> **Design-check note (2026-10-06): three questions added.** The design still works without
> answers, using the defaults below.

5. **Should `SPINE_DEV_MODE` itself require `SPINE_ENV=development`?**
   - After this task, `SPINE_DEV_MODE` still switches off API authentication (`authorize`,
     `middleware.go:253-260`) whenever `SPINE_ENV` is unset or `staging`
     (`serve_config.go:185-193`). That is the same environment gap Decision 7 closes for git.
   - *Default:* no change here. TASK-013 keeps dev mode as "allow all".
   - *Alternative:* tighten `guardDevModeEnv` to `env == "development"` in this task.
     - The cost is one line plus the `TestGuardDevModeEnv` table.
     - It joins the same `Breaking (dev setups)` entry.
     - Unit tests set `devMode` directly, so they are unaffected.
6. **May a runner behind a reverse proxy clone without a token, judged by `X-Forwarded-For`?**
   - *Default (the amended §4.1):* no. A proxied request always needs a token.
   - *Alternative (the original §4.1):* yes, but only through a new variable,
     `SPINE_GIT_HTTP_TRUSTED_PROXY_CIDRS`, separate from the rate limiter's variable. Its docs must
     require proxies that append or overwrite `X-Forwarded-For`, because a pass-through proxy lets
     a client forge a runner address.
7. **How strict is the workflow identifier rule?**
   - *Default:* `^[a-z0-9][a-z0-9_-]{0,63}$`, as designed. An existing workflow with upper case or
     `.` in an identifier cannot be updated through the API until it is renamed.
   - *Alternative:* `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. It is equally safe for SSE and breaks
     fewer workflows.
   - Either way, `/` and whitespace stay refused, because step IDs appear in URL paths
     (`/runs/{run_id}/steps/{step_id}/assign`).

## Acceptance Criteria

The regression and guard tests below exist in the design prototype
(`_work/task015/regression/`). "Fails at `61994fe`" was observed on 2026-10-06 with
`go test -overlay`. The Implementation Notes must record the same run against the task's merge
base and the merge commit.

1. **P3-28 — no SSE frame injection, live and replay** (`service/internal/gateway`,
   `TestP328_SSE_WorkflowIdentifierCannotInjectFrames` and `TestP328_SSE_ReplayCannotInjectFrames`).
   - The test ports the audit PoC's step ID into the engine's event-ID shape (`handlers.go:204`).
   - It drives the real router and broadcaster (live) and the fake event log (replay).
   - It parses the stream with the WHATWG algorithm, with LF, CR and CRLF variants.
   - **Expected:**
     - exactly the broadcast events are dispatched, with no forged `run_completed` data frame;
     - the client's last event ID is never `x-completed` and never contains CR or LF;
     - the event's JSON still carries the true `event_id`;
     - a following safe event still sets its ID.
   - Both tests fail at `61994fe` (3 events dispatched).
   - *Mutation check:* `sseFieldSafe` returning true fails both.
2. **P3-28 — frame contract** (`TestP328_FormatSSEEvent`).
   - A safe `(id, event, data)` renders byte-identical to the old `Fprintf` format.
   - An ID containing LF, CR, NUL, TAB or DEL is omitted. An unsafe event type is omitted.
   - Multi-line data becomes one `data:` line per line.
   - A non-ASCII UTF-8 ID is kept.
   - *Mutation check:* not splitting `data` fails it.
3. **P3-28 — gateway identifier rule.**
   - `TestP328_WorkflowCreate_OperatorDirectCommit_RefusesUnsafeStepID`,
     `TestP328_WorkflowCreate_ReviewerPlanningRun_RefusesUnsafeStepID` and
     `TestP328_WorkflowUpdate_RefusesUnsafeStepID` use the audit PoC body. Each returns `422`, and
     neither the workflow service nor the planning starter is called.
   - `TestP328_WorkflowValidate_ReportsUnsafeIdentifiers` returns `status: failed`, with errors
     for `divergence_points[0].id`, `divergence_points[0].branches[0].id` and
     `convergence_points[0].id`.
   - All four fail at `61994fe` (201/201/200/`passed`).
   - Guard `TestP328_ShippedWorkflowsConform`: every `workflows/*.yaml` in the repository is
     accepted (201). It passes before and after.
   - *Mutation check:* `checkWorkflowIdentifiers` returning nil fails all four.
4. **P3-28 — core is unchanged by design.** `poc/delivery-scheduler/poc_stepid_test.go`, run with
   `-overlay` against the merge commit, still passes (core accepts the ID). AC 3 shows the gateway
   refusing the same body. Recorded.
5. **P3-29 — metrics.**
   - `TestP329_Metrics_SharedModeShowsOnlyCallerWorkspace`: a server with a `ServicePool`;
     workspace A's operator sees `workspace_id="…ws-a"` and neither `…ws-b` nor
     `spine_runs_started_total`. It fails at `61994fe`.
   - Guard `TestP329_Metrics_SingleModeUnchanged`: with no pool, the body equals
     `observe.ExportPrometheus()`.
   - `TestP329_PlatformMetrics_OperatorTokenOnly`: `200` with `SPINE_OPERATOR_TOKEN` and a
     body naming `…ws-b`; `401` with a wrong token. It fails at `61994fe` (404).
   - *Mutation check:* ignoring `servicePool` fails the first.
6. **P3-29 — rebuild registry** (`TestP329_Rebuild_RegistryScopedToWorkspace`).
   - Both tenants use the same `X-Trace-Id`.
   - B's status read of A's `rebuild_id` returns `404`.
   - B starting a rebuild does not change A's `started_at`.
   - It fails at `61994fe` (`200`).
   - *Mutation check:* a bare-ID key fails it.
7. **P3-29 — SSE limiter** (`TestP329_SSELimiter_KeyedPerWorkspace`, through `handleEventStream`).
   - Setup: cap 1, `smp-admin` holding a stream in A.
   - `smp-admin` in B still gets a stream.
   - A second stream in A gets `429`.
   - It fails at `61994fe`.
   - *Mutation check:* a bare actor key fails it.
8. **P3-30 — real peer** (real `git-http-backend`, bare repo).
   - `TestP330_ProxyAddressIsNeverATrustedGitPeer`: setup is git CIDR `172.28.0.0/16` and proxy
     `172.28.0.1/32`. All five cases return `401` with no token:
     - an internet client behind the proxy;
     - no XFF;
     - a forged trusted hop followed by the real client;
     - a forged first header line followed by the proxy's own line;
     - an unparsable hop.
   - `TestP330_RunnerBehindProxyJudgedByForwardedAddress` returns `200`.
   - Both fail at `61994fe` (`200` and `401` respectively).
   - Guard `TestP330_DirectRunnerStillTrusted`: a direct `172.28.0.5` gets `200`, and a direct
     outsider gets `401`, before and after.
   - *Mutation check:* `gitPeerIP` returning `RemoteAddr` fails both.

   > **Design-check note (2026-10-06): AC 8 amended for the §4.1 note.**
   > - `TestP330_ProxyAddressIsNeverATrustedGitPeer` and the guard are unchanged.
   > - `TestP330_RunnerBehindProxyJudgedByForwardedAddress` is replaced by
   >   `TestP330_ProxiedRequestIsNeverTokenless`:
   >   - setup: git CIDR `172.28.0.0/16`, proxy `10.0.0.0/8`, peer `10.0.0.2`;
   >   - `X-Forwarded-For: 172.28.0.5` gets `401`, and so does `Forwarded: for=172.28.0.5`;
   >   - it fails against the original prototype (`200`).
   > - *Mutation checks:*
   >   - "`gitPeerIP` returns `RemoteAddr`" still fails the first test;
   >   - "`gitPeerIP` trusts the right-most forwarded hop" fails the replacement.
9. **P3-30 — rate-limit XFF** (`TestP330_RateLimitClientIP_ReadsAllForwardedLines`). With two
   header lines, the bucket is the proxy-observed client, not the forged first line. It fails at
   `61994fe`.
10. **P3-30 — dev mode and the dev flag.**
    - `TestP330_DevModeNoLongerDropsGitAuth`: with `devMode=true`, a tokenless clone and a
      tokenless `POST git-receive-pack` both return `401`. It fails at `61994fe` (`200`, `200`).
    - `TestP330_GitDevNoAuth_ReadsOnly`: with `gitDevNoAuth`, the clone returns `200` and the push
      returns `401`.
    - `service/cmd/spine`:
      - `TestP330_GuardGitHTTPDevNoAuth` passes only for `development`; it errors for `""`,
        `staging` and `production`.
      - `TestP330_GitHTTPDevNoAuthEnabled` and `TestP330_OverlappingCIDRs` pass.
    - `serve_cmd.go` calls the guard before any DB or secret work, verified in review.
    - *Mutation checks:* re-adding `devMode` to the bypass fails the first test; dropping `!push`
      from `devBypass` fails the second.
11. **Latent gitpool — token never offered to a code-repo host** (`adapters/gitpool`).
    - The audit PoCs are ported with the assertion inverted. Each asserts that the TLS test host
      receives no `Authorization` carrying the process token, and that the host was contacted:
      - `TestLatentGitpool_PoolNeverOffersProcessTokenToCodeRepoHost` (from `pushtoken_exfil`);
      - `TestLatentGitpool_RegistryCatalogPathDoesNotLeak` (from `gitpool_tokenleak`, real registry
        with a catalog listing the code repo);
      - `TestLatentGitpool_ClonerZeroCredentialSendsNoProcessToken` (from `verify-r1-3`);
      - `TestLatentGitpool_FactoryClientPushSendsNoProcessToken` (new: a factory-built client
        pushing to its origin).
    - All four fail at `61994fe`.
    - *Mutation checks:* dropping the cloner strip fails the first three; dropping the factory
      strip fails the fourth.
12. **Latent gitpool — what must still work** (`TestLatentGitpool_AllowedHostAndPerBindingCredential`).
    - A host passed to `WithProcessAuthHosts` receives the process token.
    - A per-binding `Credential` sends the binding token and not the process token.
    - *Mutation check:* never setting `InheritProcessAuth` fails it.
13. **Host scope and wiring.**
    - `adapters/gitpool` internal `TestHostKey` (prototyped) is a table test:
      - `https://Git.Example.com/a.git`, `https://tok@git.example.com./a.git`, the bare
        `git.example.com` and `HTTPS://git.example.com:443/x` all map to `git.example.com:443`;
      - `:8443` is kept;
      - `ssh://`, the SCP form, `http://` and `""` are rejected.
      - `inheritsProcessAuth` is true only for an `https` URL on the same host and port. A different
        port, a different host, an `ssh://` URL, and a host named only by an `ssh://` entry are false.
    - A `service/cmd/spine` unit test of `codeRepoProcessAuthHosts`, against temporary repositories:
      - shared mode returns nil, even with an `https` origin;
      - single mode with origin `https://git.example.com/a.git` returns `[that URL]`;
      - single mode with no `origin` returns nil and logs nothing at `Error`.
    - Review confirms that `buildServiceSet` passes no `WithProcessAuthHosts`.

    > **Design-check note (2026-10-06): one more `codeRepoProcessAuthHosts` case.** The primary
    > has `url = https://fetch.example/a.git` and
    > `remote.origin.pushurl = https://git.example.com/a.git`. The function returns the push URL,
    > not the fetch URL (note under §5.1). The `buildServiceSet` check becomes a grep that finds
    > no `WithProcessAuthHosts` in `service/internal/workspace/pool.go`.
14. **Clone-URL redaction** (`adapters/repository`).
    - `TestRedactCloneURL_TokenInUsernameIsRedacted` covers five `https` shapes, including a
      bare-username token and `HTTPS://`; none retains the token. It fails at `61994fe`
      (2 of 5 leak).
    - Guard `TestRedactCloneURL_SSHLoginKept`: `ssh://git@`, `git+ssh://git@`, the SCP form and a
      plain `https` URL are unchanged; `ssh://git:secret@` loses only the credential.
15. **Audit PoC parity.** Run with `-overlay` against the merge commit:
    - `poc/gitpool_tokenleak` `TestPOC_LibraryLeaksProcessTokenToBindingHost` fails with "token
      not observed";
    - `poc/verify-r1-3` `TestPoC_LatentMechanism_CloneSendsProcessTokenToArbitraryHost` fails
      with "process token NOT sent";
    - `poc/crypto-secrets-auth/pushtoken_exfil_test.go` fails with "token was not sent";
    - `redact_cloneurl_test.go` fails with "no leak reproduced";
    - the production-wiring PoCs (`TestPOC_ProductionCatalogNeverReachesCloner`,
      `TestPoC_ProductionWiring_CodeRepoNeverResolvedSoNoClone`, both gateway `503` tests) still
      pass.
    - Recorded in the Implementation Notes.
16. **Existing tests migrated (Design §6) and no regression.**
    - Spine passes `go test ./...`, `go vet ./...`, `go vet -tags scenario ./service/scenariotest/...`,
      `make lint` and the ADR-017 boundary lint, plus the integration and scenario suites CI runs.
    - No migration.
    - No change to the `GitClient` or `engine.GitOperator` interfaces.

    > **Design-check note (2026-10-06): AC 16 amended.** "The integration and scenario suites CI
    > runs" names nothing, because CI runs only `go test ./...`. Replace it with an explicit run of
    > `make test-scenario` and `make test-integration` (or `make docker-test`) against the test
    > database, recorded in the Implementation Notes. The eight migrated `TestGitHTTP_Push*`
    > scenarios and TASK-016's scenarios must pass (note under §6).
17. **SMP compatibility gate.** The commands in Design §9 pass with `replace ../spine` at the
    task branch head. Results are recorded in the Implementation Notes. No SMP code change.
18. **Documentation.**
    - Design §11 is applied.
    - `docker-compose.yaml` no longer contains `SPINE_GIT_HTTP_TRUSTED_CIDRS` (grep).
    - `.env.example` documents `SPINE_GIT_HTTP_DEV_NO_AUTH` and the bridge-gateway caveat.
    - `CHANGELOG.md` has the `Security` and `Breaking (dev setups)` entries.

    > **Design-check note (2026-10-06): further documentation.**
    > - `CHANGELOG.md` also gets a general `Breaking` entry for the workflow identifier rule.
    > - `architecture/workflow-definition-format.md` documents the identifier pattern.
    > - `.env.example` and integration guide §7 state that proxied and header-carrying requests
    >   never get token-less git access.
    > - `KNOWN-LIMITATIONS.md` records the omitted-`id:` cursor residual (note under Decision 2).

> **Design-check note (2026-10-06): ACs 19-23 are new.** "Original prototype" means
> `_work/task015/ov_all_fix.json` as it stood on 2026-10-06.

19. **P3-28: Unicode line separators** (`TestP328_FormatSSEEvent` extended, plus a new
    `TestP328_SSE_UnicodeSeparatorsNotSplit`).
    - An ID or event type containing U+0085, U+2028, U+2029 or invalid UTF-8 is omitted.
    - A `data` payload containing U+0085, U+2028 or U+2029 is written with `\u0085`, ` ` and
      ` ` escapes. `json.Unmarshal` of the re-joined data equals the input value.
    - A Unicode-aware test parser, which also splits on U+0085, U+2028 and U+2029, dispatches
      exactly the broadcast events, live and on replay.
    - It fails against the original prototype.
    - *Mutation check:* dropping the code-point check fails it.
20. **P3-30: forwarding headers from an undeclared peer**
    (`TestP330_ForwardingHeaderFromUndeclaredPeerIsNotTrusted`).
    - Setup: git CIDR `172.28.0.0/16`, no `SPINE_TRUSTED_PROXY_CIDRS`.
    - Peer `172.28.0.1` gets `401` with each of `X-Forwarded-For: 203.0.113.9`,
      `Forwarded: for=203.0.113.9` and `X-Real-IP: 203.0.113.9`.
    - With none of those headers it gets `200` (guard).
    - It fails at `61994fe` and against the original prototype. Both return `200`; the probe
      `TestDesignCheck_UndeclaredProxyStillTrusted` showed this on 2026-10-06.
    - *Mutation check:* ignoring forwarding headers fails it.
21. **Latent gitpool: a reused clone with a foreign origin**
    (`TestLatentGitpool_ReusedCloneForeignOriginNeverInherits`, through `Pool.Client`).
    - Setup:
      - a clone already exists at `LocalPath`, and its `origin` names a TLS test host outside the
        set (second case: only `remote.origin.pushurl` names it);
      - the binding's `CloneURL` is on an allowed host (`WithProcessAuthHosts`).
    - `Push(ctx, "origin", "main")` sends no process token, and the test host was contacted.
    - It fails against the original prototype (probe
      `TestDesignCheck_ExistingCloneOriginMismatch`, 2026-10-06).
    - *Mutation check:* computing inheritance from `CloneURL` alone fails it.
22. **Latent gitpool: the process credential helper is not inherited**
    (`TestLatentGitpool_NonInheritingCodeRepoDropsProcessHelper`).
    - Setup: a factory-built client and a cloner, both with base options carrying
      `WithCredentialHelper("store")`, a zero `Credential` and `InheritProcessAuth=false`.
    - A `store` helper file that holds a credential for the TLS test host is never consulted: the
      host receives no `Authorization`.
    - With `InheritProcessAuth=true`, the helper is used.
    - *Mutation check:* not stripping the helper fails it.
23. **Scenario migration** (note under Design §6).
    - `grep -n "DevMode" service/scenariotest/scenarios/git_http_serve_test.go` returns nothing.
    - Every migrated push scenario presents a bearer minted inside the scenario.
    - TASK-016's anonymous reserved-ref scenario asserts `401` and that the server lacks the ref.
    - The `make test-scenario` run is recorded (AC 16).

## Out of scope / follow-ups

- **The identifier rule for every writer, including SMP.** Adopting the charset in
  `core/workflow` would cover SMP's `WorkflowWriter` and git-pushed workflows. It needs an owner
  decision and an SMP-side check of customer workflows. The SSE writer already makes it
  non-security-critical.
- **Rebuild registry hygiene.**
  - One in-flight rebuild per workspace, returning the `409` the spec already lists
    (`api/spec.yaml` `/system/rebuild`).
  - Pruning completed entries, since the registry grows without bound.
  - The detached rebuild logs through the finished request's context (`handlers_system.go:105`).
- **Shared-mode event routing.** Discussion and validation events go to the process-level emitter
  (`handlers_discussions.go:446-456`, `handlers_system.go:199-204`), not the workspace's. Tenant
  events are misrouted to the process store. That is a correctness gap, not a cross-tenant read
  path.
- **Redirect hardening for an allowed-host clone.** git asks `GIT_ASKPASS` again after a
  cross-host redirect. Options are a host-aware askpass, a URL-scoped credential helper, or
  `http.followRedirects=false` for process-token clones.
- **`GIT_TERMINAL_PROMPT=0` on every credential-less clone.** The tests set it. A daemon has no
  tty, but the explicit setting is cheap.
- **RFC 7239 `Forwarded` support, and logging the resolved peer** in the git request log
  (`githttp/handler.go:285`).
- **Refusing embedded credentials in `ValidateCloneURL`** (Decision 10).
- **Live Docker confirmation** of the bridge-gateway source address. It is not required, because
  the fix does not depend on it.

> **Design-check note (2026-10-06): further follow-ups, not required here.**
> - **Cross-tenant capacity on `/git`.**
>   - The git concurrency semaphore (`githttp/handler.go:229-240`, `MaxConcurrent`, default 5) and
>     the per-IP rate limiter are process-wide.
>   - In shared mode, one tenant's slow clones can fill every git slot, and every tenant then gets
>     `503`.
>   - A per-workspace fair share is a capacity feature, not a key-scoping fix like P3-29.
> - **Verifying `ensureClone`'s origin in general.** AC 21 stops the token leak. A reused clone
>   whose `origin` differs from `clone_url` should also be refused or re-cloned; otherwise
>   `/git/{ws}/{repo}` can serve a different repository from the one the binding names.
> - **Global and system credential helpers.** Pass `-c credential.helper=` on non-inheriting
>   code-repo clones and pushes. A host-level helper, such as the `osxkeychain` in Apple git's
>   system config, is then not consulted either.
> - **Flag values.** `SPINE_GIT_HTTP_DEV_NO_AUTH` accepts `yes` and `on`, but `SPINE_DEV_MODE`
>   accepts only `1` and `true` (`serve_config.go:178-181`). Align them the next time either is
>   touched.

## Design check

Adversarial review on 2026-10-06 against Spine `61994fe`, SMP `80995559` and the
`_work/task015` prototype. Every amendment is marked "Design-check note (2026-10-06)" in place.
The probes overlaid the prototype (`ov_all_fix.json`) with throwaway tests from the session
scratchpad; nothing was written to either repository.

**Bypasses found and fixed (each reproduced against the prototype):**
1. **P3-30, undeclared proxy.** The audit's literal scenario still returned `200`, because
   `gitPeerIP` trusted `RemoteAddr` whenever `SPINE_TRUSTED_PROXY_CIDRS` was unset.
   - *Changed (§4.1):* a request carrying `X-Forwarded-For`, `Forwarded` or `X-Real-IP` is never
     token-less. New AC 20.
2. **P3-30, forged `X-Forwarded-For` through a declared proxy that does not append.** It returned
   `200`, and the design would also have made the rate-limit variable a git-access grant.
   - *Changed (§4.1):* proxied requests are never token-less. AC 8 is amended, and the original
     behaviour is Owner question 6.
3. **Latent gitpool, reused clone.** Inheritance was computed from `clone_url`, but a reused clone
   pushes to its own `origin`. Updating a binding's `clone_url`, or sharing a `local_path`, sent
   the token to the foreign origin.
   - *Changed (§5.2):* the clone's `origin` push URL is checked as well. New AC 21.

**Gaps closed with cheap guards:**
4. The process credential helper was still attached to every code repo: the confused deputy of
   Decision 9, in shared mode. *Changed:* it is stripped together with the token (§5.2). New AC 22.
5. `RemoteURL` read the fetch URL, but the token is used to push. *Changed:* it reads the push URL
   (§5.1), and AC 13 gains a case.
6. U+0085, U+2028 and U+2029 pass `sseFieldSafe` and split lines in Unicode-aware SSE clients.
   *Changed:* the writer refuses them in `id:` and `event:` and escapes them in `data:` (§1). New
   AC 19.

**Factual corrections:**
7. §6 claimed no other test changes. In fact eight `//go:build scenario` push scenarios in
   `git_http_serve_test.go`, and TASK-016's anonymous reserved-ref scenario, depend on devMode
   anonymous push and would all return `401`. CI does not run them.
   - *Changed:* a migration plan (§6), AC 16 amended to run `make test-scenario`, and new AC 23.
8. §9 claimed SMP never calls `git.WithPushToken`. It does, at 3 sites. The change is still
   additive, and the SMP build was re-run green on 2026-10-06.
9. The compose `ip_range` recipe is network-wide, not per runner (§4.4).
10. The identifier rule breaks the API for every standalone deployment, not only dev setups.
    *Changed:* a general CHANGELOG `Breaking` entry, the format documented, and Owner question 7.

**Verified unchanged:**
- every `file:line` claim in the Purpose and Design sections (spot-checked: about 60 references);
- the prototype's touched packages pass with `go test -overlay ov_all_fix.json`;
- the API identifier check cannot be dodged with case-variant keys, because `workflow.Parse` is
  strict;
- in shared mode the `resolve()` fallback reaches no process-level store or broadcaster
  (`deps.Store` is nil), so the event-routing follow-up stays a correctness gap, not a read path;
- `RedactCloneURL` is the only `clone_url` output path (`toRepositoryResponse`).

**Added:** Owner questions 5 (tighten `SPINE_DEV_MODE` to development), 6 (forwarded-address
trust for runners behind a proxy) and 7 (identifier strictness), plus notes on Owner question 2
(the operator's reach under single-mode inheritance) and Decision 2 (the omitted-`id:` cursor
residual).

## Implementation Notes

*(Implementation, 2026-10-08 to 2026-10-14. Branch `init/INIT-023-TASK-015-gateway-and-credential-scoping`,
cut from Spine local `main` `d23b897` (`61994fe` + the INIT-023 scaffold + waves 1 and 2, including
TASK-016's `core/refadmit` admission and TASK-011's `workflow.ProjectionDefinition`). The
orchestrator named the branch. macOS (APFS), git 2.54.0 (Apple), go 1.26.5, PostgreSQL 17.10; Linux
checks in the local `golang:1.26-bookworm` image (git 2.39.5). The design's line numbers were
re-located on `d23b897`.)*

### Orchestrator rulings (recorded as defaults, pending the owner, except OQ2)

| Question | Ruling | Where |
|---|---|---|
| OQ1 `SPINE_GIT_HTTP_DEV_NO_AUTH` strictness | Default: accepted only with `SPINE_ENV=development`; startup fails otherwise, including unset | `guardGitHTTPDevNoAuth`, `serveCmd` |
| **OQ2** credential-less code repo on the primary's own host | **"Never" — an orchestrator ruling and a deviation from the design default ("yes, exact host and port"); recorded for the owner.** A code repo with no `credentials_ref` never uses the process push token or the process credential helper, on any host: no `WithProcessAuthHosts`, no `Credential.InheritProcessAuth`, no origin reads. Code repos are not wired in `spine serve` today, so nothing working is lost | `gitpool.NewCLIClientFactory`, `cliCloner.Clone` |
| OQ3 tenant view of `/system/metrics` (shared mode) | Default: only the caller's `spine_workspace_pool_*` series; the full export at `GET /api/v1/platform/metrics` behind `SPINE_OPERATOR_TOKEN` | `handleMetrics`, `handlePlatformMetrics` |
| OQ4 compose token-less clones | Default: dropped (the `/16` line removed); the override example documents runner-only recipes | `docker-compose.yaml`, `docker-compose.override.yaml.example` |
| OQ5 `SPINE_DEV_MODE` to development-only | Default: unchanged | — |
| OQ6 runner behind a proxy trusted by `X-Forwarded-For` | Default: no — a proxied request always needs a token | `gitPeerIP` |
| OQ7 workflow identifier strictness | Default: `^[a-z0-9][a-z0-9_-]{0,63}$`, in `service/` only, so SMP's workflows are unaffected | `workflowIdentifierPattern` |

### What was built

| Commit | Change |
|---|---|
| `2f3f22a` | P3-28 writer: `sseFieldSafe` (C0, DEL, invalid UTF-8, U+0085/U+2028/U+2029), `formatSSEEvent` (unsafe `id:`/`event:` omitted, one `data:` line per line, JSON escapes for the three separators, one `Write`), `warnUnsafeSSEFields` on the live and replay loops. AC 1, 2, 19 |
| `48e2036` | P3-28 gateway rule: `workflowIdentifierPattern`, `workflowIdentifierErrors` / `checkWorkflowIdentifiers` (decoded with TASK-011's `workflow.ProjectionDefinition`), wired into create (before the planning dispatch), update and validate; `architecture/workflow-definition-format.md` §3.6 and `api/spec.yaml`. AC 3 + guards |
| `0045987` | P3-29: `observe.ExportPrometheusWorkspace` (additive; `writePoolMetrics` gains a nil-means-all filter), `handleMetrics` scoped when a `ServicePool` is wired, `GET /api/v1/platform/metrics` in the operator-token group, `rebuildRegistryKey`, `sseLimiterKey`. AC 5, 6, 7 |
| `dbcb2ba` | P3-30: `gitPeerIP` (the design-check gate), `forwardingHeadersPresent`, the rate-limited `warnProxiedTrustedGitPeer`, `gitDevNoAuth` / `ServerConfig.GitHTTPDevNoAuth`, `forwardedHops` in the rate limiter; `gitHTTPDevNoAuthEnabled`, `guardGitHTTPDevNoAuth`, `overlappingCIDRs`, the serve wiring and startup Warns; the five `handleGit` unit tests migrated; a `core/refadmit` comment. AC 8, 9, 10, 20 |
| `a5da4db` | AC 23: scenario migration (`mintPushToken`, `bearerPushCmd`; the anonymous reserved-ref scenario asserts 401) and the two clone scenarios moved to `GitHTTPDevNoAuth` |
| `c646048` | Latent gitpool item under the OQ2 "never" ruling: `git.WithoutPushToken()` (additive); the cloner and factory strip the process token and helper for an empty `Credential`; the contract and wiring comments corrected. AC 11, 12, 21, 22 |
| `f454258` | `RedactCloneURL` removes all `https` userinfo. AC 14; `api-operations.md`, `operator-runbook.md` |
| `75ede23` | Compose default, override recipes, `.env.example`, integration guide §3/§6.2/§7/env table, README, git push guide, security model, KNOWN-LIMITATIONS, CHANGELOG |
| `404d9d1`, `0129a08` | Test hygiene: a locked log buffer (race-safe under `slog.SetDefault`), and the 13 gocritic findings on the new tests |
| `1686f18` | Codex P2: a malformed or opaque `clone_url` never echoes anything before its last `@` |
| `5b274f5` | Codex P1: `git.WithoutAmbientCredentials()` (additive, opt-in); gitpool's "no auth" path no longer lets a host-level helper or askpass answer |
| `2070df7` | Codex round 2 P1: the same option drops the process SSH identity (agent, keys, ssh_config) for `ssh://` and SCP remotes |
| `95f8fc1` | Codex round 3 P2: the same option reads no system or global git config and no environment-injected config (`http.extraHeader`, URL-scoped entries) |

No migration, schema, table, column, event type, webhook payload or `rebuild_id` format change
(§7). One new route (`GET /api/v1/platform/metrics`) and one new environment variable
(`SPINE_GIT_HTTP_DEV_NO_AUTH`). No interface changed: `GitClient`, `engine.GitOperator` and the
gitpool `ClientFactory` / `Cloner` signatures are as before.

### Per-AC evidence

"Fails at the merge base" means: the test file run with `go test -overlay` replacing the fixed
production file(s) with `main`'s (`d23b897`), plus a one-line shim where a test calls a new helper
(for example `sseLimiterKey` returning the bare actor ID, `gitPeerIP` returning `RemoteAddr` — the
merge base's behaviour).

1. **SSE injection, live and replay.** `TestP328_SSE_WorkflowIdentifierCannotInjectFrames` and
   `TestP328_SSE_ReplayCannotInjectFrames` (`service/internal/gateway/sse_frame_injection_test.go`)
   build the engine's `evt-<trace>-<step>-completed` ID from the PoC step ID and drive the real
   router and broadcaster (live) and the fake event log (replay), with LF, CR and CRLF variants and a
   WHATWG parser. Exactly the two events dispatch, the JSON carries the true `event_id`, the client's
   last ID is never `x-completed` and never holds CR/LF, and the safe sentinel sets its ID. The live
   test waits for a probe event instead of sleeping. At the merge base both fail with the audit's
   reproduction: 3 events, a forged `message` with `{"type":"run_completed"}`, last ID `x-completed`.
2. **Frame contract.** `TestP328_FormatSSEEvent`: safe frames byte-identical to the old `Fprintf`
   format (three shapes, one with non-ASCII data); LF, CR, CRLF, NUL, TAB, DEL, U+0085, U+2028,
   U+2029 and invalid UTF-8 in the ID omitted; unsafe types omitted; multi-line data split; a
   non-ASCII ID kept. `TestP328_SSEFieldSafe` pins the predicate.
3. **Gateway identifier rule.** `TestP328_WorkflowCreate_OperatorDirectCommit_RefusesUnsafeStepID`,
   `…_ReviewerPlanningRun_RefusesUnsafeStepID`, `TestP328_WorkflowUpdate_RefusesUnsafeStepID`
   (`422`, service and planning starter not called) and `TestP328_WorkflowValidate_ReportsUnsafeIdentifiers`
   (`failed` with the divergence, branch and convergence fields, and POST refusing the same three).
   At the merge base: 201 / 201 / 200 / `passed`. Guards: `TestP328_ShippedWorkflowsConform` (all 8
   shipped workflows get 201) and `TestP328_ShippedWorkflowIdentifiersChecked` (each decodes through
   the check's decoder, so the guard is not vacuous). Extra: `TestP328_WorkflowIdentifierPattern`,
   `TestP328_WorkflowIdentifierErrorEchoBounded`, `TestP328_WorkflowCreate_UnparsableBodyLeftToService`.
   SMP's 7 workflows were probed once with the same functions (overlay, not committed): all conform.
4. **Core unchanged.** The audit PoC `TestPoC_StepIDWithNewlineAccepted`, overlaid on `core/workflow`
   at the branch head, passes: core still accepts the ID; AC 3 shows the gateway refusing it.
5. **Metrics.** `TestP329_Metrics_SharedModeShowsOnlyCallerWorkspace` (own series, no `ws-b`, no
   `spine_runs_started_total`; no workspace → 400, never the full export), guard
   `TestP329_Metrics_SingleModeUnchanged` (with and without a file-mode workspace config, byte-equal
   to `ExportPrometheus()`, retried until the global metrics are quiescent),
   `TestP329_PlatformMetrics_OperatorTokenOnly` (200 with the token and `ws-b` in the body; 401 for a
   wrong or missing token; 503 when unset). `core/observe` `TestExportPrometheusWorkspace` covers
   gauges, counters and close reasons. At the merge base the first fails (both leaks, and 200 without
   a workspace) and the platform test gets 404.
6. **Rebuild registry.** `TestP329_Rebuild_RegistryScopedToWorkspace`: the same `X-Trace-Id` in both
   tenants gives `rb-deadbeef`; B reads A's ID → 404; B starting its rebuild leaves A's `started_at`;
   each reads its own. Merge base: 200.
7. **SSE limiter.** `TestP329_SSELimiter_KeyedPerWorkspace` through `handleEventStream`, cap 1,
   waiting on the limiter's own count instead of sleeping: B's `smp-admin` gets a stream, a second
   stream in A gets 429. `TestP329_SSELimiterKey` pins the key (an empty actor ID stays empty, so the
   limiter's "no limit" case is unchanged). Merge base: B gets 429.
8. **Real peer** (real `git-http-backend`, bare repository, `git_endpoint_auth_internal_test.go`).
   `TestP330_ProxyAddressIsNeverATrustedGitPeer` (git CIDR `172.28.0.0/16`, proxy `172.28.0.1/32`):
   all five cases 401; merge base 200 for all five. `TestP330_ProxiedRequestIsNeverTokenless`
   (amended AC 8: proxy `10.0.0.0/8`, peer `10.0.0.2`): `X-Forwarded-For`, `Forwarded` and
   `X-Real-IP` naming `172.28.0.5` all 401 (the merge base also answers 401 here — the proxy address
   is outside the git CIDR — so this test targets the original prototype; its mutation is below).
   Guard `TestP330_DirectRunnerStillTrusted` (direct `172.28.0.5` 200, outsider 401, and an IPv6
   direct peer 200). `TestP330_GitPeerIP` pins each rule, including a non-canonical header key.
9. **Rate-limit XFF.** `TestP330_RateLimitClientIP_ReadsAllForwardedLines`: two lines bucket on
   `203.0.113.9`, not the forged `1.2.3.4` (the merge base buckets on `1.2.3.4`); no header, an
   all-proxy chain, an empty line and an untrusted peer still fall back to `RemoteAddr`.
10. **Dev mode and the dev flag.** `TestP330_DevModeNoLongerDropsGitAuth` (`devMode=true`:
    token-less clone, push advertisement and `POST git-receive-pack` all 401; merge base 200/200/200)
    and `TestP330_GitDevNoAuth_ReadsOnly` (clone 200, push advertisement and push 401, a bearer push
    advertisement 200). `service/cmd/spine/git_dev_no_auth_test.go`: `TestP330_GuardGitHTTPDevNoAuth`
    (only `development`; `""`, `staging`, `production` refused; the error names the flag and the
    remedy), `TestP330_GitHTTPDevNoAuthEnabled`, `TestP330_OverlappingCIDRs`,
    `TestP330_BuildServerConfigWiresGitDevNoAuth`, and `TestP330_ServeRefusesGitDevNoAuthOutsideDevelopment`,
    which runs `serveCmd().RunE` with an unreachable database URL: it fails on the flag, so the guard
    runs before any store, secret or listener (it sits right after `guardDevModeEnv`).
    *Review rounds 1 and 2:* the read-only half also holds for queries no git client sends:
    `TestP330_GitDevNoAuth_ReadsOnly` refuses the repeated (`service=git-upload-pack&service=git-receive-pack`)
    and the valueless (`service&git-receive-pack`) push advertisement with `401` and still serves the
    dumb `info/refs`; `TestInfoRefsClassifier_NeverAReadWhereGitHTTPBackendAdvertisesReceivePack`
    checks every query of up to four tokens against the real git-http-backend (Review round 2).
11. **Token never offered to a code-repo host** (`adapters/gitpool/process_auth_scope_test.go`, a TLS
    test host that challenges with Basic and records `Authorization`):
    `TestLatentGitpool_PoolNeverOffersProcessTokenToCodeRepoHost`,
    `…_RegistryCatalogPathDoesNotLeak` (real registry with a catalog listing the code repo),
    `…_ClonerZeroCredentialSendsNoProcessToken`, `…_FactoryClientPushSendsNoProcessToken`. Each
    asserts the host was contacted and received no `Authorization` at all. All four fail at the merge
    base ("the process-wide push token was sent").
12. **What must still work.** Under OQ2 "never" there is no allowed host, so the first half of AC 12
    is not applicable. `TestLatentGitpool_PerBindingCredentialStillUsed`: a per-binding `Credential`
    sends the binding token and never the process token, for the cloner and the factory push, with
    either the process token or the process helper in the base options.
13. **Host scope and wiring.** Not applicable under OQ2 "never": `hostKey`, `inheritsProcessAuth`,
    `WithProcessAuthHosts`, `RemoteURL` and `codeRepoProcessAuthHosts` were not built.
    `grep -rn "WithProcessAuthHosts\|InheritProcessAuth" --include='*.go' .` returns nothing, so
    neither `serve_wiring.go` nor `workspace/pool.go` configures a process-auth exception.
14. **Clone-URL redaction.** `TestRedactCloneURL_TokenInUsernameIsRedacted` (5 shapes; merge base: 2
    of 5 leak), guard `TestRedactCloneURL_SSHLoginKept` (unchanged before and after), and
    `TestRedactCloneURL_UnparsableValueRedacted` (malformed and opaque legacy values; codex P2).
15. **Audit PoC parity** — see "Audit PoC parity" below: every expected outcome holds.
16. **No regression.** `go test ./...`, `go vet` (plain, `integration`, `scenario`), the boundary
    lint, the integration suites, the full scenario suite (the same 30 failures as `main`, all
    pre-existing) and CI's `scenario-git-http` job; no migration; no interface change. Table below.
17. **SMP compatibility gate** — below. No SMP change.
18. **Documentation.** §11 applied (integration guide §3, §6.2, §7, env table; README; KNOWN-LIMITATIONS
    "Auth Bypass in Dev Mode" rewritten); `grep -c SPINE_GIT_HTTP_TRUSTED_CIDRS docker-compose.yaml`
    → 0; `.env.example` documents `SPINE_GIT_HTTP_DEV_NO_AUTH`, the bridge-gateway caveat and that
    proxied and header-carrying requests are never token-less; CHANGELOG has `Security`, a general
    `Breaking` (the identifier rule), `Breaking (dev setups)`, `Changed` and `For embedders`;
    `workflow-definition-format.md` §3.6 documents the pattern; KNOWN-LIMITATIONS records the
    omitted-`id:` cursor residual.
19. **Unicode separators.** `TestP328_SSE_UnicodeSeparatorsNotSplit` (live and replay, each of U+0085,
    U+2028, U+2029, plus invalid UTF-8 live) with a parser that also splits like Python's
    `str.splitlines`: exactly the two events, and the decoded data equals the input value (live:
    U+0085 in the run ID, which `json.Marshal` does not escape). `TestP328_FormatSSEEvent_DataEscapesKeepJSONValue`
    re-joins the `data:` lines and compares JSON values.
20. **Undeclared proxy.** `TestP330_ForwardingHeaderFromUndeclaredPeerIsNotTrusted` (no
    `SPINE_TRUSTED_PROXY_CIDRS`, peer `172.28.0.1`): `X-Forwarded-For`, `Forwarded`, `X-Real-IP`, an
    empty `X-Forwarded-For` and `Via` all 401; the same peer with no header 200. Merge base: 200 for
    every header.
21. **Reused clone with a foreign origin.** `TestLatentGitpool_ReusedCloneForeignOriginNeverInherits`
    (through `Pool.Client`; the clone's `origin`, or only its `remote.origin.pushurl`, names the TLS
    host while the binding's `clone_url` names another): the push contacts the host and sends no
    `Authorization`. Merge base: the token is sent in both cases.
22. **Process credential helper.** `TestLatentGitpool_NonInheritingCodeRepoDropsProcessHelper`
    (`WithCredentialHelper("store")` in the base options, a `store` file holding a credential for the
    test host; cloner, factory push and pool clone send nothing). Its positive control shows a plain
    client with the helper does send the stored credential. The `InheritProcessAuth=true` half is not
    applicable under OQ2. Merge base: 1 `Authorization` in each case.
23. **Scenario migration.** `grep -n "DevMode" service/scenariotest/scenarios/git_http_serve_test.go`
    → nothing. Every push scenario mints its bearer (`mintPushToken`: an actor with the role it needs,
    `auth.Service.CreateToken`) and pushes with `git -c http.extraHeader=…`. TASK-016's anonymous
    reserved-ref scenario asserts 401 on the push advertisement and the pack upload, a failing
    credential-less push with no `ref-admission` line, and no ref on the server. 20 `TestGitHTTP_*`
    PASS, 0 SKIP; without the bearer the push scenarios fail (mutation below).

### Mutation checks

Each applied alone through `go test -overlay` (never in the worktree):

| Mutation | Result (first failing test) |
|---|---|
| `sseFieldSafe` always true | killed — `TestP328_SSE_WorkflowIdentifierCannotInjectFrames`, `TestP328_SSE_ReplayCannotInjectFrames` |
| `data` not split | killed — `TestP328_FormatSSEEvent` |
| no CR normalisation | killed — `TestP328_FormatSSEEvent` |
| code-point check dropped | killed — `TestP328_SSE_UnicodeSeparatorsNotSplit`, `TestP328_FormatSSEEvent` |
| data escapes dropped | killed — `TestP328_SSE_UnicodeSeparatorsNotSplit` |
| UTF-8 check dropped | killed — `TestP328_FormatSSEEvent`, `TestP328_SSEFieldSafe` |
| `checkWorkflowIdentifiers` returns nil | killed — the three create/update tests |
| `workflowIdentifierErrors` returns nil | killed — all four AC 3 tests |
| divergence / branch / convergence check dropped (each) | killed — `TestP328_WorkflowValidate_ReportsUnsafeIdentifiers` |
| `/validate` does not fail | killed — `TestP328_WorkflowValidate_ReportsUnsafeIdentifiers` |
| pattern widened to upper case and `.` | killed — `TestP328_WorkflowIdentifierPattern` |
| metrics ignore `servicePool` | killed — `TestP329_Metrics_SharedModeShowsOnlyCallerWorkspace` |
| tenant export unfiltered | killed — `TestExportPrometheusWorkspace`, `TestP329_Metrics_SharedModeShowsOnlyCallerWorkspace` |
| counter filter dropped | killed — `TestExportPrometheusWorkspace` |
| rebuild key bare | killed — `TestP329_Rebuild_RegistryScopedToWorkspace` |
| SSE key bare / Acquire with the bare ID | killed — `TestP329_SSELimiter_KeyedPerWorkspace` |
| `gitPeerIP` returns `RemoteAddr` | killed — `TestP330_ProxyAddressIsNeverATrustedGitPeer`, `TestP330_ForwardingHeaderFromUndeclaredPeerIsNotTrusted` |
| `gitPeerIP` trusts the right-most forwarded hop (original §4.1) | killed — `TestP330_ProxiedRequestIsNeverTokenless` |
| forwarding headers ignored | killed — `TestP330_ForwardingHeaderFromUndeclaredPeerIsNotTrusted` |
| header match case-sensitive | killed — `TestP330_GitPeerIP` |
| `devMode` back in the bypass | killed — `TestP330_DevModeNoLongerDropsGitAuth` |
| `!push` dropped from `devBypass` | killed — `TestP330_GitDevNoAuth_ReadsOnly` |
| rate limiter reads the first line only | killed — `TestP330_RateLimitClientIP_ReadsAllForwardedLines` |
| proxied-peer Warn not rate-limited | killed — `TestP330_ProxiedTrustedPeerWarnIsRateLimited` |
| guard refuses production only | killed — `TestP330_GuardGitHTTPDevNoAuth`, `TestP330_ServeRefusesGitDevNoAuthOutsideDevelopment` |
| guard not called in `serveCmd` | killed — `TestP330_ServeRefusesGitDevNoAuthOutsideDevelopment` |
| flag not wired into `ServerConfig` | killed — `TestP330_BuildServerConfigWiresGitDevNoAuth` |
| overlap never detected | killed — `TestP330_OverlappingCIDRs` |
| scenario pushes without the bearer | killed — `TestGitHTTP_PushAcceptedWhenFlagOn`, `…OverrideHonouredForOperator`, `…UnusedOverrideEmitsNoEvent`, `…PushedCommitIsByteIdentical` |
| cloner strip dropped | killed — the pool, registry and cloner AC 11 tests |
| factory strip dropped | killed — `…FactoryClientPushSendsNoProcessToken`, `…ReusedCloneForeignOriginNeverInherits` |
| token stripped but not the helper | killed — `…NonInheritingCodeRepoDropsProcessHelper` |
| strip applied to per-binding credentials too | killed — `…PerBindingCredentialStillUsed` |
| redaction password-only / all userinfo stripped / parse failure echoed raw | killed — `TestRedactCloneURL_TokenInUsernameIsRedacted` / `…SSHLoginKept` / `…UnparsableValueRedacted` |
| fallback stops at a delimiter / no opaque check | killed — `TestRedactCloneURL_UnparsableValueRedacted` |
| `WithoutAmbientCredentials` not applied by gitpool | killed — `TestLatentGitpool_CredentialLessCodeRepoIgnoresHostCredentials` |
| no helper reset on clone / on push | killed — same test (`global credential.helper`, `URL-scoped …`) |
| askpass environment not blanked | killed — same test (`inherited GIT_ASKPASS`) |
| `GIT_CONFIG_GLOBAL` / `GIT_CONFIG_NOSYSTEM` / `GIT_CONFIG_COUNT=0` dropped (each) | killed — same test (URL-scoped global, system, environment-injected header) |
| `-c http.extraHeader=` or `-c credential.helper=` dropped | survives on git ≥ 2.32 by design (the config switches cover it); killed with `GIT_CONFIG_GLOBAL` also dropped, which simulates git 2.30–2.31 |
| no `GIT_SSH_COMMAND` / agent socket kept / pubkey allowed | killed — `TestLatentGitpool_CredentialLessSSHCodeRepoUsesNoSSHIdentity` |

### Deviations and additions (with reasons)

- **OQ2 "never"** (orchestrator ruling, above). Not built: §5.1 `RemoteURL`, §5.2
  `Credential.InheritProcessAuth`, `WithProcessAuthHosts`, `hostKey`, `inheritsProcessAuth`, §5.3
  `codeRepoProcessAuthHosts`. AC 12's first half, AC 13 and AC 22's `InheritProcessAuth=true` case
  are not applicable; AC 21 holds trivially and is still tested. The strip is in gitpool itself, so
  any caller gets "an empty Credential means no auth".
- **Ambient credentials (codex rounds 1–3, beyond the design).** The design listed host-level helpers
  as a follow-up. Codex rated the gap P1, so it is closed with the additive, opt-in
  `git.WithoutAmbientCredentials()`, which gitpool's "no auth" path applies: no system or global git
  config (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL` = null device), no config injected through
  `GIT_CONFIG_PARAMETERS` / `GIT_CONFIG_COUNT`, `-c credential.helper=` and `-c http.extraHeader=`
  (the fallback for git 2.30–2.31), empty `GIT_ASKPASS` / `SSH_ASKPASS` / `SSH_AUTH_SOCK`,
  `GIT_TERMINAL_PROMPT=0`, and a `GIT_SSH_COMMAND` with no ssh_config, no agent and no key-based
  authentication. **Consequence for the owner:** `credentials_ref` carries an HTTPS token only and
  there is no per-binding SSH key, so an `ssh://` code repository with no credential is reached
  anonymously or not at all (dormant today; documented in KNOWN-LIMITATIONS and CHANGELOG).
- **The identifier check uses `workflow.ProjectionDefinition`** (TASK-011 had landed, as §2 asks) and
  is split into `workflowIdentifierErrors` (used by `/validate`) and `checkWorkflowIdentifiers` (the
  422). A refused identifier is echoed `%q`-quoted and capped at 80 bytes.
- **Forwarding headers.** Beyond the three the design names, `X-Forwarded-Host`, `X-Forwarded-Proto`
  and `Via` also mark a request as proxied (stricter, fail closed), matched case-insensitively. The
  Warn fires only when the raw peer is itself in the git CIDRs (the case whose behaviour changed), at
  most once a minute, and logs header names, never values.
- **SSE.** An unsafe event type is logged too (its own Warn). `sseLimiterKey` keeps an empty actor ID
  empty so the limiter's existing "no limit" case is unchanged.
- **Redaction.** Beyond §5.4, a value `url.Parse` refuses, or an opaque one with an `@`, loses
  everything before its last `@` (codex P2) instead of being echoed raw.
- **Scenario helpers.** `setupGitHTTPServerWithOptionsAndActor` and its actor-injection wrapper are
  removed; `mintPushToken` + `bearerPushCmd` replace them, which exercises the production
  bearer-to-actor path.
- **Docs beyond §11:** `api/spec.yaml`, `architecture/api-operations.md`, `docs/operator-runbook.md`,
  `docs/git-push-guide.md`, `architecture/security-model.md`; TASK-016's CHANGELOG devMode sentence
  is marked superseded. `core/refadmit/evaluator.go` changes one comment (no code).
- **Test infrastructure.** The shared PostgreSQL server (`localhost:55630`) was down from
  2026-10-13 (its install under the old scratchpad had been removed). The integration and
  `TestGitHTTP_*` runs of 2026-10-08 used it; every later database gate ran on a private,
  throwaway PostgreSQL 17.10 cluster (Homebrew binaries) in this session's scratchpad on
  `localhost:55715`, TCP only, which touched nothing shared. The `s015_scen1`, `s015_int_cmd`,
  `s015_int_ws` and `s015_int_wsreg` databases created on the shared server could not be dropped
  because the server is down; drop them if it returns. golangci-lint is the Homebrew v2.12.2 binary
  (the scratchpad one was gone). Codex's configured model is not available to this account, so
  every round ran with `-c model="gpt-5.6-sol"`; no config file was changed.
- **Commit trailers.** `0129a08` was first committed without the `Claude-Session` line (the harness
  briefly said to omit it); the unpushed commit's message was amended to restore it.

### Merge-base grep (§6)

`grep -rn "devMode\|DevMode" --include='*_test.go' service` at `d23b897`: `handlers_git_test.go` (6
hits; five relied on the git bypass — migrated, the sixth, the design's `TestHandleGit_PushFlagOff`, is `TestHandleGit_DisabledPushDoesNotResolvePolicy`
and no longer sets it), `git_http_serve_test.go` (12; the eight push scenarios and TASK-016's anonymous scenario relied
on it — migrated), `git_http_per_repo_test.go` and `runner_clone_context_test.go` (1 each; clone only
— moved to `GitHTTPDevNoAuth`), `githttp/ref_admission_tags_test.go` (a comment — updated). Not git
related, unchanged: `handlers_tokens_test.go` (3, API) and `service/cmd/spine` (9, the API dev mode
in `serveDeps` and the `guardDevModeEnv` table).

### Gates

Code at `95f8fc1` unless stated.

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | clean |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 2 without tests, no failures |
| `go test -race -p 4 -count=1` on the 9 touched packages (`adapters/git`, `adapters/gitpool`, `adapters/repository`, `core/observe`, `core/refadmit`, `service/cmd/spine`, `service/internal/gateway`, `service/internal/githttp`, `service/internal/workspace`) | all ok, no races; also `gateway` `-race -count=3`, the new gateway tests `-count=20` and the gitpool tests `-count=5`: ok |
| `-tags integration` on `service/cmd/spine` and `service/internal/workspace` (fresh databases; the workspace registry migrated from `migrations/registry`) | 178 and 223 PASS, 0 FAIL, 0 SKIP (the same counts on 2026-10-08) |
| CI `scenario-git-http`: `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` and `-run '^TestBootstrapInternalAdmin_'` (fresh database) | 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; 2 PASS, 0 SKIP |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` (fresh database) vs a `git archive main` export on its own database | 30 FAIL, the **same 30 as `main`** (the known pre-existing set: `TestSchemaMatchesProduction`, the gateway, divergence and artifact-mutation scenarios, …); 1488 PASS lines vs 1479 on `main` (the 9 new scenario steps); 6 SKIP in both |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5; `--pull=never --rm`, read-only mounts, `GOPROXY=off`), plus `adapters/gitpool`, `adapters/repository`, `gateway`, `cmd/spine`, `workspace` there | all ok, including every `TestLatentGitpool_*` and the SSE/P3-30 tests on Linux; no container left |
| `golangci-lint run --build-tags scenario --new-from-rev main` on the 10 touched packages (and `--build-tags integration` on `cmd/spine`, `workspace`) | 0 issues (13 gocritic findings on the first draft of the new tests were fixed in `0129a08`) |
| full issue count on the same packages, branch vs `main` export | 179 vs 185 (6 fewer `gofmt`; nothing new) |
| `golangci-lint run --enable-only=gosec` on the same packages | 0 issues |
| `git diff --name-only main...HEAD \| grep '\.go$' \| xargs gofmt -l` | empty |
| Audit PoCs overlaid (see below) | as AC 4 and AC 15 require |

### Codex review (`codex review --base main`, `-c model="gpt-5.6-sol"`)

| Round | Code | Findings | Disposition |
|---|---|---|---|
| — | `0129a08` | the configured model `gpt-6.1-sol` (and `gpt-6-sol`) is refused for this account | re-run with `gpt-5.6-sol` |
| 1 | `0129a08` | **P1** a credential-less clone or push still consults a global or system `credential.helper` and an inherited `GIT_ASKPASS`; **P2** the malformed-URL fallback stops at `?`/`#`, so a legacy credential with that character is echoed | both **fixed** (`5b274f5`, `1686f18`) with tests and mutations |
| 2 | `5b274f5` | **P1** for `ssh://` / SCP remotes the process's SSH agent and keys still authenticate | **fixed** (`2070df7`); consequence for SSH code repos recorded above |
| 3 | `2070df7` | **P2** an `http.extraHeader` in host config still rides the request | **fixed** (`95f8fc1`), generalised to "no system or global config" because a URL-scoped header outranks a generic `-c` reset |
| 4 | `95f8fc1` | "The changes consistently implement the intended credential isolation, tenant scoping, SSE sanitization, and Git endpoint authentication behavior. The full Go test suite passes, and no actionable correctness regressions were identified." | — |

No finding was rejected.

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T015/smp`, detached at `f5c6fd6d`, no `vendor/`
or `tmp/`; `go list -m github.com/bszymi/spine` resolves to this worktree; `GOFLAGS=-mod=readonly`;
`git status` clean after every step. Spine at `95f8fc1`.

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `-tags integration`; `-tags scenario`; `-tags faultinject` | OK, all clean |
| `go test -race -p 4 -count=1` on the 43 SMP packages whose test dependencies include a changed Spine package (`adapters/git`, `adapters/gitpool`, `adapters/repository`, `core/observe`, `core/refadmit`; `go list -test -f …`) | 39 ok, 2 without tests, **2 FAIL — pre-existing, not caused by this task**: `internal/customer/gitread` `TestBounded_ErrorContractEqualsCLIClient` and `internal/customer/projectionwriter` `TestBoundedProjection_IncrementalAddAndModifyOversized` / `…_OversizedWorkflowStalls` (a nil-pointer panic). They fail identically with `../spine` pointed at a `main` (`d23b897`) export through a scratch `-modfile`, and pass with it pointed at SMP's pin `61994fe`: SMP `main`'s INIT-027 TASK-004 bounded-read contract tests disagree with Spine's waves 1–2 `CLIClient` (TASK-002's argv/error changes). Reported as an open question |
| SMP CI "Apply platform migrations" / "Apply Spine runtime migrations" (`go run ./dev/apply-migrations -set platform -dir migrations`; `-set spine-runtime`) on fresh databases (roles `platform`, `platform_audit_retention`, `smp_prov` as in CI) | 115 and 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -timeout 10m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` with `TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` | 1835 passed, 0 failed, 2 allowed skips (Linux-only `ru_maxrss` memory tests; this host is macOS); `testgate: PASS` |
| `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |
| `./dev/lint-spine-internals.sh` | OK |

SMP never imports `adapters/gitpool` or calls `RedactCloneURL`, `ExportPrometheus` or the new options;
`adapters/git` and `core/observe` gain only additive symbols, and `WithPushToken`, `NewCLIClient`,
`Clone` and `run` behave identically for a client without `WithoutAmbientCredentials`. No SMP change.

### Audit PoC parity (AC 4, AC 15)

Overlaid at `95f8fc1` (`go test -overlay`, the PoC copied into a `zz_poc_*_test.go` overlay path,
nothing written to the tree):

| PoC | Result |
|---|---|
| `gitpool_tokenleak` `TestPOC_LibraryLeaksProcessTokenToBindingHost` | fails: "token not observed at evil host (hits=1)" |
| `gitpool_tokenleak` `TestPOC_ProductionCatalogNeverReachesCloner` | passes |
| `verify-r1-3` `TestPoC_LatentMechanism_CloneSendsProcessTokenToArbitraryHost` | fails: "process token NOT sent to attacker host" |
| `verify-r1-3` `TestPoC_ProductionWiring_CodeRepoNeverResolvedSoNoClone` | passes |
| `crypto-secrets-auth/pushtoken_exfil_test.go` | fails: "token was not sent to attacker host" |
| `crypto-secrets-auth/redact_cloneurl_test.go` | fails: "no leak reproduced" |
| `verify-r1-3` gateway `TestPoC_ProductionLikeConfig_RepositoryCreateRefused`, `TestPoC_SharedMode_RepositoryCreateRefused` | pass (both 503) |
| `delivery-scheduler/poc_stepid_test.go` (AC 4) | passes: core still accepts the step ID |

### Not run

- **CI on GitHub** (PostgreSQL 18, Linux runners): needs a push, which the orchestrator owns. Locally
  PostgreSQL 17.10 was used; nothing here uses a PostgreSQL 18-only feature, and the Linux checks ran
  in `golang:1.26-bookworm`.
- **SMP's other gated suites** (the platform-DB, faultinject, workspace-schema and provisioning
  steps of SMP `ci.yml`): the artifact names only the Spine-runtime step and the platform migrations,
  and those need MinIO and pgvector, which the private cluster lacks.
- **A live Docker check** of the bridge-gateway source address (out of scope, §Out of scope).
- **Old git (2.30–2.31)** for the `GIT_CONFIG_GLOBAL` fallback: simulated by mutation only.

### Follow-ups (added by this implementation)

- Per-binding SSH credentials for code repositories (SSH code repos cannot authenticate today).
- `~/.netrc` for credential-less code repos; a reused clone whose `origin` differs from its binding
  (both in KNOWN-LIMITATIONS).
- The SMP `gitread` / `projectionwriter` contract-test failures against Spine waves 1–2 (above).
- Align the value sets of `SPINE_DEV_MODE` and `SPINE_GIT_HTTP_DEV_NO_AUTH` (design-check
  follow-up, recorded in KNOWN-LIMITATIONS).

### Review round 1

*(2026-10-14. Three reviewers — correctness and compatibility, adversarial security,
completeness and tests — on `ea8fbf5`. Each finding was checked against the code first. Fixes on
top of `ea8fbf5`: `76bd161`, `3428fee`, `f8e3f6e`, `c4f01fe`, `a24bf89`.)*

#### Findings

| # | Finding (severity) | Disposition |
|---|---|---|
| R1 | The unsafe-SSE-field Warn has no rate limit: one Warn per field per event, per live subscriber and per replayed event, so past `sseReplayCap` unsafe IDs a reader-role client that reconnects in a loop drives log volume without bound (P3) | **Fixed** (`3428fee`). Real (code-traced at `ea8fbf5`: one Warn per unsafe field per written event, so 3 reconnects over a 100-event window whose IDs and types are both unsafe log 600 Warns). `warnUnsafeSSEFields` is now a `Server` method that logs one combined Warn (`sse: omitting unsafe event fields (rate-limited)`) at most once a minute for the process (an atomic timestamp, the `warnProxiedTrustedGitPeer` pattern), carries `suppressed_since_last_warn`, and cuts the ID and type to `maxIdentifierEcho` bytes (`boundedEcho`, now shared with the workflow identifier error). Frames are unchanged. Test `TestP328_UnsafeSSEFieldWarnIsRateLimited` drives the real replay path: 3×100 unsafe events log 1 Warn; after the interval a second Warn reports 299 suppressed; no log line is split or longer than 1 KiB; a safe event neither logs nor counts. Mutations (no rate limit, unbounded value, no count) killed |
| R2 | `WithoutAmbientCredentials` drops `safe.directory` (and global CA / proxy) for the push while the same client's commit and merge still read host config, so a foreign-owned clone commits but cannot push, and no config fixes it (P3, dormant) | **Fixed as documentation + a pinning test; the code change is deferred (follow-up), with the reason.** Real (git reads `safe.directory` only from protected config). KNOWN-LIMITATIONS now states the consequence and the remedy (every clone under `SPINE_CODE_REPO_BASE` owned by the `spine serve` uid), next to the `.netrc` residual; so do the option's doc comment, CHANGELOG and `multi-repository-integration.md` §6.2 (`f8e3f6e`). Not changed in code because neither cheap fix is clearly right: forcing `-c safe.directory=<clone>` on the push would skip git's ownership check — which stops a foreign-owned clone's hooks and config running as the Spine user — without the operator opting in; mirroring the operator's protected `safe.directory` means re-reading the host config this option exists to ignore. Code repos are not wired in `spine serve`, so the choice belongs with wiring them (follow-up below) |
| R3 | `IsReceivePack` / `isReadOnly` read the first `service` value, git-http-backend the last: `info/refs?service=git-upload-pack&service=git-receive-pack` is a read for the trusted-CIDR and `SPINE_GIT_HTTP_DEV_NO_AUTH` paths and returns the receive-pack advertisement (P3) | **Fixed only for the repeated form** (`76bd161`); **completed by review round 2 (R2-1)**, which replaced the `url.ParseQuery` classification with a byte-exact match. Reproduced at `ea8fbf5` with a real git-http-backend: `200 application/x-git-receive-pack-advertisement` under the dev flag, from a direct trusted runner, and for an authenticated caller. `IsReceivePack` now counts any receive-pack value (a repeated service fails closed to "push", so it needs a bearer), `isReadOnly` never treats a repeated service as a read, and `ServeHTTP` refuses a repeated service with `400` before the push gate, the read gate and CGI. ~~(the comment on `serviceParams` records why a single value is unambiguous: both parsers split on `&`/`=` and decode alike, and a pair `url.ParseQuery` drops keeps a literal value git-http-backend refuses)~~ *That claim was wrong (review round 2, R2-1): git-http-backend ends a name at `&` or `=` and reads the value to the next `&`, so in `service&git-receive-pack` the name without `=` takes the next segment as its value; `serviceParams` and the claim are gone.* Tests: `TestP330_RepeatedServiceParameterNeverOpensPushAdvertisement` (dev flag, trusted runner with push on and off, authenticated pusher, both value orders, a repeated upload-pack; never the receive-pack content type), the duplicate case added to `TestP330_GitDevNoAuth_ReadsOnly` and `TestP330_DirectRunnerStillTrusted` as suggested, `TestIsReadOnly` / `TestIsReceivePack` rows, `TestServeHTTP_RepeatedServiceParameterRefused`. Mutations (first value in `IsReceivePack`, first value in `isReadOnly`, refusal dropped) killed. SMP's own gateway has the same first-value classifier — reported, not changed (Follow-ups) |
| R4 | `PoolConfig.SecretClient` and `TestPool_NoResolverPassesEmptyCredentialThrough` still say a binding without `credentials_ref` works through `SPINE_GIT_PUSH_TOKEN` (P3) | **Fixed** (`c4f01fe`): both now say such a binding clones and pushes with no credential (OQ2 "never") and a declared ref fails closed. The test file's imports were gofmt'd because it is now touched (pre-existing ordering) |
| R5 | `multi-repository-integration.md` §6.2 promises "the same credential methods as the primary repo (SSH key, PAT, OAuth)" (P3) | **Fixed** (`f8e3f6e`): HTTPS token through `credentials_ref` only, no per-binding SSH key (follow-up), nothing inherited from the process or the host on any host, the host-config consequence, and a link to KNOWN-LIMITATIONS |
| R6 | Integration guide §7 shows a token-less runner clone that now gets `401`; the startup Warn still recommends "a narrow runner subnet" (P3) | **Fixed** (`c4f01fe`): the example presents a bearer (`git -c http.extraHeader=…`, not persisted) and says the token-less form needs the runner's own address in `SPINE_GIT_HTTP_TRUSTED_CIDRS` and a direct connection; the push note says "runner", not "subnet"; the Warn now says "the runners' own addresses only (never a range containing a Docker bridge gateway or a reverse proxy)". No test pins the Warn text |
| R7 | `observability.md` A.1 still says `/system/metrics` is unauthenticated and full, and omits `/platform/metrics` (P3) | **Fixed** (`c4f01fe`): an endpoint table (`system.metrics` operator bearer; full in single mode, the caller's `spine_workspace_pool_*` only in shared mode, `400` without a workspace; `/platform/metrics` behind `SPINE_OPERATOR_TOKEN`, `401`/`503`) and the pool series names |
| R8 | The loss of non-credential host config (CA bundle, proxy, `insteadOf`, `safe.directory`, `http.version`) for credential-less code repos is not documented (P3) | **Fixed** (`f8e3f6e`): KNOWN-LIMITATIONS, the CHANGELOG `Breaking (dev setups)` and `For embedders` entries, the `WithoutAmbientCredentials` comment and §6.2 list it and name the environment forms that still apply (`GIT_SSL_CAINFO`, `GIT_SSL_CAPATH`, `GIT_SSL_NO_VERIFY`, `HTTPS_PROXY` / `https_proxy` / `ALL_PROXY`). New `TestLatentGitpool_CredentialLessCodeRepoReadsNoHostConfig` pins both halves: a global `http.sslVerify=false` and a global `url.<base>.insteadOf` apply to a plain client (positive controls) and not to the credential-less cloner or factory push; `GIT_SSL_NO_VERIFY` in the environment still applies. Dropping `GIT_CONFIG_GLOBAL=/dev/null` fails three of its subtests. The suggested alternative — keep the host config and reset only the credential keys — is rejected: a URL-scoped `http.<url>.extraHeader` outranks a generic command-line reset (codex round 3), so it would reopen that credential path |

#### Acceptance-criteria gaps

| Gap | Disposition |
|---|---|
| OQ2 "never" (orchestrator ruling) leaves AC 12's first half, AC 13 and AC 22's `InheritProcessAuth=true` half unbuilt and AC 21 trivially true | **Not closable here — needs the owner.** The ruling stands as recorded; the task should not be closed until the owner confirms OQ2. If the owner chooses "yes, exact host and port", §5.1–5.3 become mandatory (`RemoteURL` with the push-URL read, the origin re-check for a reused clone, `Credential.InheritProcessAuth`, `WithProcessAuthHosts`, `hostKey`, `codeRepoProcessAuthHosts`, AC 21 as a real test) |
| AC 16 names `make lint` and `make test-integration`; only narrower commands were recorded | **Closed** by running the literal commands (table below): `make lint` (boundary lint + repo-wide `golangci-lint run`): 270 issues on the branch vs 275 on a `main` export, all pre-existing (5 fewer `gofmt`), 0 new by `--new-from-rev main`. (The earlier "179 vs 185" counted only the 10 touched packages.) `make test-integration` (`go test -tags integration ./...`, run with `-p 4 -count=1` on the shared machine) across every package, including `adapters/projection`, `adapters/delivery`, `adapters/store` and `adapters/secrets`: only the two known pre-existing failures. `make test-scenario`'s package set ran with `-p 1` |
| AC 4 / AC 15 ask for the PoCs against the merge base and the merge commit | **Closed for the current `main`.** The merge-base runs are the "fails at the merge base" overlays above. `main` (`d23b897`) is an ancestor of the head (`git merge-base --is-ancestor`), so the merge commit's tree is the head's tree, and the PoCs re-run at `a24bf89` give the AC 15 outcomes. If `main` moves before the merge (for example TASK-014 landing first), the re-run on the actual merge commit is the orchestrator's merge-time step |
| AC 10 / I4 held only for a single `service` value | ~~**Closed** by R3~~ R3 closed only the repeated form; the valueless form (`service&git-receive-pack`) stayed open until review round 2 (R2-1, R2-G1) |
| The reviewers could not re-run the DB gates (shared PostgreSQL down) | **Closed**: every DB gate below ran on a private throwaway PostgreSQL 17.10 cluster in this session's scratchpad (`localhost:55716`, TCP only, `s015_*` databases), removed afterwards. The shared server on `localhost:55630` was still down |

#### Gates (code at `a24bf89`)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...`; `-tags integration`; `-tags scenario` | clean |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 2 without tests |
| `go test -race -p 4 -count=1` on the 9 touched packages; the new gateway tests `-race -count=10`; `TestLatentGitpool_*` `-race -count=3` | all ok, no races |
| `make test-integration` command: `go test -p 4 -tags integration -count=1 ./...` (fresh database) | 5025 PASS, 2 FAIL — exactly the known pre-existing `adapters/store` `TestRunModeDatabaseDefault` and `adapters/secrets` `TestAWSClient_Contract_LocalStack` — 7 SKIP (the `DBProvider` tests, which need `SPINE_REGISTRY_TEST_DATABASE_URL`). With a registry database migrated from `migrations/registry`: `service/internal/workspace` 223 PASS and `service/cmd/spine` 178 PASS, 0 SKIP |
| CI `scenario-git-http`: `-run '^TestGitHTTP_'` and `-run '^TestBootstrapInternalAdmin_'` (fresh database) | 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; 2 PASS, 0 SKIP |
| `make test-scenario` packages: `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...`, branch vs a fresh `git archive main` export, own databases | 30 FAIL on both, the identical set (and the same as the set recorded above); 1488 vs 1479 PASS lines; 6 SKIP on both |
| `make lint` (branch / `main` export) | 270 / 275 issues, all pre-existing; `golangci-lint run --new-from-rev main` on the touched packages: 0 issues with `--build-tags scenario`, `integration` (`cmd/spine`, `workspace`) and plain; `--enable-only=gosec`: 0 issues. One gocritic finding on the first draft of the new githttp test was fixed in `a24bf89` |
| `git diff --name-only main...HEAD \| grep '\.go$' \| xargs gofmt -l` | empty |
| Mutations of the round-1 fixes (overlay) | 7 of 7 killed (listed under R1, R3, R8) |
| Audit PoCs overlaid at `a24bf89` | as AC 4 / AC 15 require: the four leak PoCs fail with their expected messages; `TestPOC_ProductionCatalogNeverReachesCloner`, `TestPoC_ProductionWiring_CodeRepoNeverResolvedSoNoClone` and both gateway `503` PoCs pass; `poc_stepid` passes |

SMP compatibility gate (SMP `f5c6fd6d`, `../spine` = this worktree at `a24bf89`, `GOFLAGS=-mod=readonly`,
`git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | OK, clean |
| `go test -race -p 4 -count=1` on the 43 SMP packages whose test dependencies include a changed Spine package | 39 ok, 2 without tests, the same 2 pre-existing failures as above (`gitread` `TestBounded_ErrorContractEqualsCLIClient`; `projectionwriter` `TestBoundedProjection_IncrementalAddAndModifyOversized` / `…_OversizedWorkflowStalls`), re-confirmed identical with `../spine` pointed at the fresh `main` export through a scratch `-modfile` |
| Platform and Spine-runtime migrations (`go run ./dev/apply-migrations -set platform -dir migrations`; `-set spine-runtime`; roles as in SMP CI, `smp_prov` posture `f`) | 115 and 26 versions recorded |
| Spine-runtime integration suites through `dev/testgate` (`spinecore`, `spineembedded`, `runner`, `executionreadiness`; `TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN`) | 1835 passed, 0 failed, 2 allowed Linux-only `ru_maxrss` skips; `testgate: PASS` |
| `go run ./dev/testgate … -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |
| `./dev/lint-spine-internals.sh` | OK |
| Leak check after the suites (as SMP CI's) | no `ws_*` database or role left; the private cluster was then stopped and deleted |

#### Codex review (`codex review --base main`)

| Code | Findings | Disposition |
|---|---|---|
| `a24bf89` | The configured model (`gpt-6.1-sol`) is still refused for this account ("not supported when using Codex with a ChatGPT account"), so the review ran with `-c model="gpt-5.6-sol"` as in the earlier rounds; no config file was changed. Verdict: "The changes consistently implement the intended credential isolation, tenant scoping, SSE sanitization, and Git endpoint authentication behavior. The full Go test suite passes, and no actionable correctness regressions were identified." | No P1/P2; nothing to fix or reject |

#### Follow-ups (added by review round 1)

- **SMP gateway, same classifier divergence (reported, not changed — SMP is out of scope here).**
  `internal/customer/githttp/handler.go` `IsReceivePack` / `isReadOnly` read the first `service`
  value too. SMP authenticates every git request, but `gateway.go` refuses a push to a viewer
  (below member) only through `IsReceivePack`, so with receive-pack enabled a viewer gets the
  receive-pack advertisement through a repeated `service` (advertisement only: the pack upload is
  matched by path). The same fix applies there. *Extended by review round 2:* the valueless form
  `info/refs?service&git-receive-pack` reaches SMP's classifier the same way (`url.Values.Get`
  reads `service=""`, a read), and the fix to port is round 2's byte-exact match, not round 1's
  repeated-value rule.
- When code repositories are wired: pass Spine-controlled values for `safe.directory`, the CA
  bundle and the proxy on the command line of a credential-less clone and push (R2, R8).

### Review round 2

*(2026-10-14. One second-round reviewer on `4a35e35`: one finding and two gaps. Each was checked
against the code first. Fixes on top of `4a35e35`: `84a149e` (code and tests), `dcd4986` (CHANGELOG,
integration guide), `f8914b8` (lint), `b4a0cd9` (HEAD guard test).)*

#### Findings

| # | Finding (severity) | Disposition |
|---|---|---|
| R2-1 | R3's fix is incomplete: the `serviceParams` comment claims a single `service` value is unambiguous, but git-http-backend ends a name at `&` or `=` (consuming it) and reads the value to the next `&`, so `info/refs?service&git-receive-pack` is `service=git-receive-pack` to git and `service=""` plus an unrelated key to `url.ParseQuery`. `isReadOnly` called it a read, so under `SPINE_GIT_HTTP_DEV_NO_AUTH` and from a direct trusted runner it returned the receive-pack advertisement without a token (I4, AC 10; P3) | **Fixed** (`84a149e`). Real: reproduced at `4a35e35` with the `t015GitServer` harness and the real git-http-backend: `200 application/x-git-receive-pack-advertisement` under the dev flag from `198.51.100.7` and from the direct trusted runner `172.28.0.5` with push on, also with `&x=1` appended; with push off, git's own `403 Service not enabled` (Spine's push gate never ran). **Fix — the reviewer's Option 2.** `infoRefsServiceOf` compares `r.URL.RawQuery` byte for byte with the three queries a git client sends: the empty query (dumb protocol), `service=git-upload-pack` and `service=git-receive-pack` (remote-curl appends exactly `service=<name>`; protocol v2 travels in the `Git-Protocol` header). `net/http/cgi` passes `RawQuery` verbatim as `QUERY_STRING` (checked in the Go 1.26 source), so these three strings mean the same to both parsers and no model of either is needed. Any other GET `info/refs` query is never a read (`isReadOnly`), is a possible push (`IsReceivePack`, so the gateway asks for a bearer when push is on), and `ServeHTTP` refuses it with `400 unsupported info/refs query` before the push gate, the read gate and CGI. The Warn logs the query's length, never its bytes. `serviceParams` / `repeatedService` and the false comment are gone; the CHANGELOG entry, its log line and the integration guide's route list now state the rule; the gateway call site says that "push" includes non-canonical queries. **Option 1 rejected** (a git-faithful parser compared with `url.Values`): it would still have to refuse every input where the two differ, so it adds a parser that could itself be wrong (for example `%00`, which git keeps literally because `hex2chr` rejects 0) without accepting anything a real client sends. **Behaviour changes, all for inputs no client sends:** `service=` (empty value) was a token-less read that git refused with `403 Unsupported service`, and is now `401` without a bearer (push on) or `400`; a repeated upload-pack was a token-less `400` and is now `401` without a bearer (push on) or `400`; any extra parameter on `info/refs` gets `400`. **Tests:** new `info_refs_query_test.go` — `TestInfoRefsClassifier_NeverAReadWhereGitHTTPBackendAdvertisesReceivePack` asks git-http-backend itself (no gate in front, receive-pack enabled) about every query the classifier calls a read or not-a-push, over the 1554 concatenations of up to four tokens from `service`, `=`, `&`, `git-receive-pack`, `git-upload-pack`, `;` plus encoded and mixed forms (`%73ervice&git-receive-pack`, `service&git-%72eceive-pack`, `%00`, `%zz`, `+`, `#`, case), with three positive controls the probe must see as the receive-pack advertisement; it also asserts that a GET `info/refs` is exactly one of read or push. `TestInfoRefsQuery_OnlyClientQueriesReachGitHTTPBackend`: every corpus query except the three is refused with `400`, both flag states, and the three are not. The valueless form is added to `TestP330_RepeatedServiceParameterNeverOpensPushAdvertisement` (dev flag, trusted runner push on and off, authenticated pusher, with and without a trailing pair, `service=`; never the receive-pack content type), `TestP330_GitDevNoAuth_ReadsOnly` (`401`; the dumb `info/refs` still `200`), `TestP330_DirectRunnerStillTrusted` (`401` push on, `400` push off), and the `TestIsReadOnly` / `TestIsReceivePack` / `TestServeHTTP_RepeatedServiceParameterRefused` tables. The new tests also pass on Linux git 2.39.5 (`golang:1.26-bookworm`, read-only mounts, container removed), so the positive controls hold on an older backend too. **Mutations** (overlay): the round-1 `url.ParseQuery` single-value logic (killed by 8 tests, and the differential test names `service&git-receive-pack`, `…&`, `…&x=1`, `x=1&service&git-receive-pack`, `%73ervice&git-receive-pack`, `service&git-%72eceive-pack`); the `400` dropped (4 tests); non-canonical not a push (5 tests); non-canonical a read (2 tests): 4 of 4 killed |
| R2-1a | (found while fixing R2-1) git-http-backend serves `HEAD` exactly as `GET` (verified with a standalone `net/http/cgi` probe: `HEAD info/refs?service=git-receive-pack` → `200 application/x-git-receive-pack-advertisement`), and no test pinned that Spine never passes a token-less `HEAD` to it | **Guard added, no code change** (`b4a0cd9`). The classifiers treat `HEAD` as neither a read nor a push, so on the token-less paths it reaches the handler, which refuses it with `403 unsupported git operation` before CGI (unchanged behaviour). `TestP330_HeadInfoRefsNeverServesPushAdvertisement` pins that for the canonical, valueless and repeated push forms under the dev flag and from a trusted runner (never the advertisement, never a push-side `200`), without over-specifying `HEAD` of the upload-pack form. Mutation "dumb-protocol GET branch also accepts `HEAD`" killed |

#### Gaps

| Gap | Disposition |
|---|---|
| R2-G1: AC 10 / I4 falsified by `info/refs?service&git-receive-pack` under the dev flag and from a direct trusted runner; the round-1 note "Closed by R3" does not hold | **Closed** by R2-1: the cases are in `TestP330_GitDevNoAuth_ReadsOnly` and `TestP330_DirectRunnerStillTrusted` as asked, the differential test covers the class, and the round-1 R3 row, AC-gap row and AC 10 evidence are corrected above (struck-through text kept for the record) |
| R2-G2: AC 12's first half, AC 13 and AC 22's `InheritProcessAuth=true` half have no falsifiable test because OQ2 "never" left that code unbuilt; AC 21 holds trivially | **Not closable here — unchanged, needs the owner** (as recorded in round 1). The task must not be closed until the owner confirms OQ2; if the owner picks "yes, exact host and port", §5.1–5.3 and real tests for those ACs are mandatory before close |
| R2-G3: everything else checked out | Noted. No change |

#### Gates (code at `b4a0cd9`; artifact-only commit after)

The shared PostgreSQL on `localhost:55630` was still not listening (the `psql` the orchestrator named
is gone with the old scratchpad), and the server on `localhost:55707` is another session's private cluster, so
every database gate ran on a new private, throwaway PostgreSQL 17.10 cluster (Homebrew binaries) in
this session's scratchpad on `localhost:55731`, TCP only, scram, `s015_*` databases; it was stopped and
deleted after the gates.

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...`; `-tags integration`; `-tags scenario` | clean |
| `go test -p 4 -count=1 ./...` (at `dcd4986` and again at `b4a0cd9`) | 40 packages ok, 2 without tests |
| `go test -race -p 4 -count=1` on the 9 packages the branch touches; `githttp` + `gateway` again at `b4a0cd9`; the round-2 tests `-race -count=5` | all ok, no races |
| `go test -tags integration -p 4 -count=1` on the 9 touched packages (fresh `s015_int_touched`) | 1835 PASS, 0 FAIL, 7 SKIP (the `DBProvider` tests); with `SPINE_REGISTRY_TEST_DATABASE_URL` on `s015_registry` migrated from `migrations/registry`: `service/internal/workspace` + `service/cmd/spine` 401 PASS, 0 SKIP |
| CI `scenario-git-http`: `-run '^TestGitHTTP_'` (fresh `s015_scen_githttp`) and `-run '^TestBootstrapInternalAdmin_'` (fresh `s015_scen_bootstrap`) | 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; 2 PASS, 0 SKIP |
| Every scenario that drives `/git` (`TestGitHTTP_*`, `TestRepositoryScenario_*`, `TestRunnerCloneContext_*`, `TestCrossRepoEvidence_*`, `TestWorkspace_*`, the partial-merge and back-compat scenarios), `-p 1`, with the registry URL set | 42 PASS, 1 FAIL: `TestWorkspace_ScopedServices/builder-creates-scoped-services` ("alpha Validator should not be nil"), identical on a fresh `git archive main` (`d23b897`) export with its own databases — pre-existing, not touched here (it skips when `SPINE_REGISTRY_TEST_DATABASE_URL` is unset) |
| golangci-lint (scratchpad build and Homebrew v2.12.2) `--new-from-rev main` on the touched packages, plain, `--build-tags integration` and `--build-tags scenario`; `--enable-only=gosec` in each | 0 issues. G504 on the new test's `net/http/cgi` import was fixed in `f8914b8` with the handler's justification |
| `git diff --name-only main...HEAD \| grep '\.go$' \| xargs gofmt -l` | empty |
| Mutations of the round-2 fix (overlay) | 5 of 5 killed (R2-1, R2-1a) |

SMP compatibility gate (SMP `f5c6fd6d`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, `git status`
clean after every step). Round 2 changes only `service/internal/githttp` and `service/internal/gateway`,
which SMP cannot import (Go's internal rule; `go list -deps -test ./...` in SMP lists neither), so every
Spine package SMP compiles is byte-identical to `a24bf89`, where round 1 ran the SMP unit set; that set
is empty for this round and was not re-run.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...`; `./dev/lint-spine-internals.sh` | OK, clean, clean, OK |
| Platform and Spine-runtime migrations (`go run ./dev/apply-migrations -set platform -dir migrations` on `s015_smp_it`; `-set spine-runtime` on `s015_spine_runtime_it`; roles as in SMP CI, `smp_prov` posture `f`) | 115 and 26 versions recorded |
| Spine-runtime integration suites through `dev/testgate` (`-race`; `spinecore`, `spineembedded`, `runner`, `executionreadiness`; `TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN`) | 1835 passed, 0 failed; the 2 Linux-only `ru_maxrss` skips allowed with `-allow-skip 'ru_maxrss in KiB is the Linux contract'`; `testgate: PASS` |
| `go run ./dev/testgate … -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only RSS skip; `testgate: PASS` |

#### Codex review (`codex review --base main`)

| Code | Findings | Disposition |
|---|---|---|
| `f8914b8`, `b4a0cd9` | Plain `codex review --base main` is still refused ("The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account"), so both runs used `-c model="gpt-5.6-sol"` as in earlier rounds; no config file was changed. Verdicts: "No actionable correctness issues were identified in the changed code. The full Go test suite passes." and "No actionable correctness issues were identified. The full Go test suite and targeted vet checks pass, and the security-scoping changes appear internally consistent with their tests and documentation." | No P1/P2; nothing to fix or reject |

#### Follow-ups (added by review round 2)

- **SMP gateway** — the round-1 follow-up above is extended: SMP's `IsReceivePack` / `isReadOnly`
  also take `info/refs?service&git-receive-pack` as a read; port round 2's byte-exact match.
- The `s015_scen1`, `s015_int_cmd`, `s015_int_ws` and `s015_int_wsreg` databases noted in round 1
  are still on the shared server, which is still down: drop them if it returns.

### Integration (wave 3 merge) (2026-10-14)

Integration branch `init/INIT-023-wave3-integration` (worktree `i23/s-int3/spine`), cut from Spine
local `main` `d23b897` (origin `61994fe` + the INIT-023 scaffold + waves 1 and 2). Merge order, each
`git merge --no-ff`, built and tested on its touched packages before the next: TASK-005 `06ccca2`
(merge `1f7af13`, clean), TASK-003 `b5ad640` (merge `88df8bb`; conflicts `CHANGELOG.md`, the
`adapters/git/cli.go` imports), TASK-014 `9c1778a` (merge `4ed5195`; `CHANGELOG.md`), TASK-015
`2096c1f` (merge `5029321`; `CHANGELOG.md`, the `CLIClient` fields in `adapters/git/cli.go`, the
`workflow.create` / `workflow.update` descriptions in `api/spec.yaml`). Every conflict kept both
sides: each task's CHANGELOG section (newest merge on top), both import sets, both field sets, and
TASK-015's identifier rule beside TASK-014's dispatch text. Coordination commits: `70d6c8e` (the
merge-binding plumbing runs under TASK-003's read bounds) with `f5b4347` (its lint), `94017aa`
(docs: review-time writes and the push window), `b911c04` (codex round 2: a captured binding is
stored before the step completes), `70bed62` (codex round 3: rows a pre-TASK-003 projection holds
at a refused path are retired). Nothing was pushed; `main` and the task branches are unchanged.

**This task in the integration.** Merge `5029321` (conflicts: `CHANGELOG.md`, every section kept;
`adapters/git/cli.go`, where `CLIClient` now carries this task's `noAmbientAuth` and TASK-003's
read-bound fields; `api/spec.yaml`, where `workflow.create` / `workflow.update` keep this task's
identifier rule beside TASK-014's dispatch text).

- *With TASK-003.* `Clone` keeps both this task's ambient-credential reset (arguments and
  environment) and TASK-003's bounded stderr; `runSink` applies this task's reset to `push` (an
  `opMutate` op, so it never receives the read-class locale or `GIT_ALLOC_LIMIT`); TASK-005's
  plumbing runner, now under TASK-003's bounds (`70d6c8e`), never contacts a remote, so the reset
  does not apply to it. `spine_projection_files_skipped_total` (TASK-003) is process-global and
  unlabelled: in shared mode it is served by `/api/v1/platform/metrics` only, as this task's docs
  say for every process-global series. TASK-003's `projection_file_skipped` ids are hex, so the SSE
  writer never omits their `id:` line.
- *With TASK-005.* `PushCommit` (op `push`) gets this task's reset for a credential-less code
  repository, and gitpool's clients stay `*git.CLIClient`, so TASK-005's `createRunBranches` binder
  check passes for them. TASK-005's merge-retry `head_sha` path resolves the workspace's resolver
  per request (`runMergeResolverFrom`), so no process-global gateway state was added.
- *With TASK-014.* The identifier check runs before TASK-014's update planning dispatch,
  `refuseIfWorkflowRunOpen` and `resolveWorkflowWriteContext` (§10), on create and update (checked
  on the merged handlers); TASK-014's run-branch authorization uses per-request workspace services.
- *With TASK-016 / TASK-013.* The migrated push scenarios (minted bearer) pass on the merged tree:
  `TestGitHTTP_*` 20/20 and `TestBootstrapInternalAdmin_*` 2/2. `security-model.md`'s push-window
  sentence (in text this task edited) no longer calls TASK-005 planned (`94017aa`).
- The `s015_*` databases the round-1 and round-2 notes asked to drop are not on the current shared
  server (a new PostgreSQL 18.6 container; its database list was checked): nothing to drop.

**Gates on the integration branch** (code at `70bed62`; only these artifact notes were committed
afterwards):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 17 non-scenario packages the four tasks and the coordination commits touched | all ok |
| `-tags integration -race -p 1` (`SPINE_TEST_DATABASE_URL` = `s3i_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3i_reg`, migrated from `migrations/registry`) on the touched packages with integration tests | `adapters/delivery` 315, `adapters/projection` 180, `service/cmd/spine` 184, `service/internal/workspace` 225 PASS (subtests counted), 0 FAIL, 0 SKIP |
| CI `scenario-git-http` job, its four steps run verbatim (`bash -eo pipefail`) on a fresh `s3i_ci3` | `TestGitHTTP_*` 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS; `TestWorkflowLifecycle_*` 4 PASS (incl. `_UpdateExisting`, `_UpdateExisting_DraftAuthorCannotReview`), with the harness's merge binding on; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS; 0 SKIP |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` (`s3i_scen3`, registry `s3i_reg3` migrated) vs a `git archive main` export on its own databases | 31 top-level FAIL, the **same set as `main`**: the 30 known pre-existing failures plus `TestWorkspace_ScopedServices` (pre-existing with the registry URL set). With an unmigrated registry database both trees also fail `TestWorkspace_BatchMigration` and `TestWorkspace_Lifecycle` (33 each, identical sorted `--- FAIL` lists, 70 lines with subtests); `main` passes those two with the registry migrated (checked) |
| `golangci-lint` v2.11.4 (`tmp/init027-orchestration/bin`) `run --new-from-rev main` on the touched packages: plain, `--build-tags=integration`, `--build-tags=scenario` (with `service/scenariotest/...`) | 0 issues each (one gocritic `appendAssign` on the first `runWith` change was fixed in `f5b4347`) |
| `--enable-only=gosec` on the touched packages (`integration,scenario` tags) and on `./...` | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| CI `git-floor` steps in the local `golang:1.26-bookworm` image (git 2.39.5; `--pull=never --rm`, read-only mounts, `GOPROXY=off`), plus `adapters/projection` | all ok; no container left |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, **no `-c model=` override**; each review session's `session_meta` has `source: {"subagent":"review"}` and `"model":"gpt-6.1-sol"`, the newest `rollout-2026-10-14T13-49-08-…`) | round 1 (`f5b4347`): "No actionable regressions were identified". Round 2 (`f5b4347`): one P2, TASK-005 — fixed in `b911c04`. Round 3 (`b911c04`): one P2, TASK-003 with TASK-011's sweep — fixed in `70bed62`. Three rounds is the cap, so `70bed62` itself was not reviewed by codex again (open question for the orchestrator). The earlier task rounds that ran on older models (`-c model="gpt-5.5"`, `gpt-5.6-sol`, `gpt-6-astra` because the default was refused then) are superseded by these gpt-6.1-sol rounds over the whole wave-3 diff |

**SMP compatibility** (`i23/s-int3/smp` = SMP `main` `f5c6fd6d`, detached, `../spine` = this
worktree, `GOFLAGS=-mod=readonly`; never modified, `git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 13 SMP packages importing a changed Spine package (`go list` with test imports) plus `internal/customer/workflow` | 11 ok, `projectionsvc` has no tests; `gitread` (`TestBounded_ErrorContractEqualsCLIClient`: `bad_ref`, `bad_sha`, `missing_path`, `path_beyond_a_symlinked_dir`) and `projectionwriter` (`TestBoundedProjection_IncrementalAddAndModifyOversized`, `…_OversizedWorkflowStalls`, a nil-pointer panic) fail — the **identical** `--- FAIL` list against a Spine `main` `d23b897` export through a scratch `-modfile`: pre-existing, SMP TASK-004 tests that SMP TASK-006 adapts at the bump |
| SMP TASK-006 tip `935852b5` (a temporary detached SMP worktree beside this one, removed afterwards) against this Spine | `gitread` and `projectionwriter` **pass**; `go build ./...`, `go vet` plain and `-tags integration` OK. TASK-006's adaptations fix the two pre-existing failures, also with wave 3 |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3c_smp_it` with `vector`, `s3c_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov` as in CI) | 1835 passed, 0 failed, 2 allowed skips (the Linux-only `ru_maxrss` tests; macOS host); `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |

Environment: the shared PostgreSQL 18.6 container on `localhost:55630`. Only `s3i_*` (Spine) and
`s3c_*` (SMP) databases were created, and all were dropped afterwards; the SMP suites' own `ws_*`
databases and roles were cleaned up by the suites (none left, checked). The only containers
started (the `git-floor` runs, `--rm`) were removed; the Docker VM kept at least 6.1 GB free. Logs:
`spine-management-platform/tmp/init027-orchestration/wave3-int/`.

### Cross-task review (wave 3) (2026-10-14)

The wave 3 cross-task review ran on the integration branch at `63e2318` (each P1/P2 put to two
skeptics, whose votes are summarised below; P3s had none). Every fix is on `init/INIT-023-wave3-integration`
(worktree `i23/s-int3/spine`), not on the task branches, which are unchanged: `f346c9e` (cascade
replay), `dcdfff5` (recovery serialisation) with `475e342` (its lint), `a6dbd67` (projection
refusal order), `7aedeca` (info/refs oracle), `3aa24b9` (docs), `cdd33ee` (codex round 1) with
`a02580e` (its lint). Each fix's test was checked to fail against the previous code with
`go test -overlay` (the old file swapped in), and to pass on the fix. Nothing was pushed.

| # | Finding (as raised) | Disposition |
|---|---|---|
| 1 | P3, `service/internal/gateway/handlers_git.go`: a non-canonical `GET /info/refs?<query>` counts as a push (`githttp.IsReceivePack`), so with receive-pack off (the default) and a workspace that resolved it took `pushDisabledShortCircuit` and reached `ServeHTTP`'s `400 unsupported info/refs query`, while a missing workspace failed the short-circuit and got the gate's uniform `401`: the workspace-existence oracle EPIC-004 TASK-031 closed, reopened for this sub-case (no new capability: the `git-receive-pack` POST 403-vs-401 already says as much) | **Fixed** (`7aedeca`): the short-circuit excludes a non-canonical info/refs (`githttp.IsNonCanonicalInfoRefs`), so it meets the auth gate like a read. Without a bearer it is `401` with the same body whether or not the workspace exists — a trusted-CIDR runner's included, since a push never takes the trusted bypass — and with one it is still `400`. The canonical receive-pack request keeps its `403` naming the flag. Test: `TestWave3_NonCanonicalInfoRefsIsNoWorkspaceOracle` (four queries; identical 401 bodies; 400 with a bearer; the canonical 403/401 guard) fails on the previous gateway. The four `TestP330` cases that pinned the token-less push-off `400` now expect `401` (round 2's "`400` push off" for the direct trusted runner is superseded). CHANGELOG, integration guide. SMP: its gateway's push-disabled short-circuit runs before workspace resolution, so porting round 2's byte-exact match (the follow-up above) cannot reopen this oracle there; keep it that way |

**Gates** (code at `a02580e`; `final-basic`, `final-gotest` and `final-race` ran on `cdd33ee`,
whose only difference is two `\u` escapes in one test's string literals):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 17 packages the four tasks touched (`adapters/{delivery,git,gitpool,projection,repository}`, `core/{artifact,domain,engine/...,observe,refadmit,workflow,worktreefs}`, `service/cmd/spine`, `service/internal/{gateway,githttp,workspace}`) | all ok |
| `-tags integration -race -p 1` on `adapters/delivery`, `adapters/projection`, `service/cmd/spine`, `service/internal/workspace` (`SPINE_TEST_DATABASE_URL` = fresh `s3f_it`, `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3f_reg`, migrated from `migrations/registry`) | 905 PASS (subtests counted), 0 FAIL, 0 SKIP |
| CI `scenario-git-http` job, its four steps verbatim (`bash -eo pipefail`) on a fresh `s3f_ci` (code `cdd33ee`) | `TestGitHTTP_*` 20 PASS (incl. `TestGitHTTP_Push*`), `TestBootstrapInternalAdmin_*` 2 PASS, `TestWorkflowLifecycle_*` 4 PASS, `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS; 0 SKIP |
| `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (`s3f_scen`, registry `s3f_reg2` migrated; code `cdd33ee`) | the same 31 top-level `--- FAIL` (59 lines with subtests) as the integration run's `scen-branch-3` (identical sorted lists): the 30 known pre-existing failures plus `TestWorkspace_ScopedServices`. `TestPartialMergeRetry_HappyPath`, `TestPartialMergeExternalResolution_HappyPath`, `TestCancelFromPartiallyMerged_AsymmetricCleanup` and `TestPlanningRun_StatusCascadesToChildren` pass |
| `golangci-lint` v2.11.4 `run --new-from-rev main` on `core/engine/...`, `adapters/projection/...`, `service/internal/{gateway,githttp}/...`, `service/scenariotest/...`: plain, `--build-tags=integration`, `--build-tags=scenario` | 0 issues each (a gocritic `builtinShadow` was fixed in `475e342`, a gosec G116 / staticcheck ST1018 in `a02580e`) |
| `--enable-only=gosec` on the touched packages (`integration,scenario`) and on `./...` | 0 issues |
| `gofmt -l` (the Go 1.26.5 toolchain's) on every `.go` file changed since `63e2318` | empty |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, no `-c model=` override) | round 1 (`475e342`; session `rollout-2026-10-14T15-20-33-01a13ac9-4f2b-…`, `source: {"subagent":"review"}`, `"model":"gpt-6.1-sol"`): one P2, fixed in `cdd33ee`. Round 2 (`cdd33ee`; `rollout-2026-10-14T15-30-35-01a13ad2-7e47-…`, review source, `gpt-6.1-sol`): "No actionable regressions were identified relative to the merge base." `a02580e` changes only test escapes, so no third round |

**SMP compatibility** (`i23/s-int3/smp` = SMP `main` `f5c6fd6d`, detached, `../spine` = this
worktree, `GOFLAGS=-mod=readonly`; never modified, `git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` (on `a02580e`) | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 13 SMP packages importing a changed Spine package (test imports included) plus `internal/customer/workflow` (on `a02580e`) | 11 ok, `projectionsvc` has no tests; `gitread` and `projectionwriter` fail with the **identical** `--- FAIL` list as the integration run (and Spine `main` `d23b897`): pre-existing SMP TASK-004 tests that SMP TASK-006's branch adapts (the integration run checked that TASK-006's tip passes them) |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3f_smp_it` with `vector`, `s3f_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites" via `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`; code `cdd33ee`) | 1835 passed, 0 failed, 2 allowed Linux-only skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed skip; `testgate: PASS` |

Not run: the CI `git-floor` job (git 2.39.5 in `golang:1.26-bookworm`) — not in this gate list, and
the Docker VM had 6.2 GB free; the new real-git tests use only porcelain any git has
(`update-ref`, a tab in a file name). Environment: the shared PostgreSQL 18.6 container on
`localhost:55630`; only `s3f_*` databases were created and all were dropped afterwards; the SMP
suites' `ws_*` databases and roles were cleaned up by the suites (none left, checked). No container
was started apart from `docker run --rm alpine:3 df` disk checks. Logs:
`spine-management-platform/tmp/init027-orchestration/wave3-fix-spine-s3f/`.

### Acceptance (wave 3) (2026-10-14)

The final gate on `init/INIT-023-wave3-integration` (worktree `i23/s-int3/spine`). It ran after the
codex 6.1 catch-up of waves 1 and 2 was merged in (`41c70dd8`: the worktreefs FIFO `O_NONBLOCK`
open, exact integers in stored workflow definitions, the sweep's `SetSyncBaseline`). The code gates
ran at `41c70dd8`. The two commits after it change documentation only: `c6938efd` (CHANGELOG) and
the commit that adds this note. Nothing was pushed. Main, the task branches and the SMP checkout
are unchanged.

**This task.** `TestGitHTTP_*` gives 20 PASS, 8 of them push scenarios.
`service/internal/{gateway,githttp}` and `adapters/{gitpool,repository}` pass under `-race`, and
SMP's githttp testgate passes against this Spine. The catch-up changes none of this task's files.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 21 packages changed since `d23b897` | 19 ok. `core/engine/enginetest` has no tests. `service/scenariotest/scenarios` builds only with `-tags scenario`; the scenario rows below cover it |
| `-tags integration -p 1 -count=1 -v` on the changed packages that have integration suites: `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace`. `SPINE_TEST_DATABASE_URL` = fresh `s3g_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3g_reg`, migrated from `migrations/registry` | 1086 PASS, subtests counted (315 / 191 / 171 / 184 / 225); 0 SKIP; 1 FAIL. The failure is `adapters/store` `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, column `workflow_version_label`), and it was there before: a `git archive` export of `main` `d23b897` fails it the same way. `adapters/store` is in scope only because of the catch-up's `SetSyncBaseline`; its `TestSetSyncBaseline_*` tests pass |
| CI `scenario-git-http` job: its four `run:` steps, read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, repo root as cwd, `SPINE_TEST_DATABASE_URL` = fresh `s3g_ci`) | All four steps exit 0. `TestGitHTTP_*`: 20 PASS, 8 of them `TestGitHTTP_Push*`. `TestBootstrapInternalAdmin_*`: 2 PASS. `TestWorkflowLifecycle_*`: 4 PASS, including both required ones. `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy`: PASS. 0 SKIP |
| Full scenario suite, `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...`, run side by side on this branch and on a `git archive` export of `main` `d23b897` (fresh `s3g_scen_{b,m}`, registry `s3g_reg_{b,m}` migrated) | The failure sets are identical: 31 top-level `--- FAIL` on each (59 lines with subtests). They are the 30 known failures plus `TestWorkspace_ScopedServices`, the same sorted list as the wave 3 review's run. Top-level PASS: 198 on the branch, 196 on main; the two extra are TASK-014's `TestWorkflowLifecycle_UpdateExisting*`. 0 SKIP |
| CI `git-floor` job: its three `run:` steps, verbatim, in `golang:1.26-bookworm` (`--pull=never --rm`; worktree, host module cache and step scripts mounted read-only; `GOPROXY=off`, `GOTOOLCHAIN=local`) | git 2.39.5. All three steps ok: `adapters/git`, `adapters/git/refname`, `core/engine`, then `adapters/git/...`, `core/artifact`, `core/workflow`. The container was removed |
| `golangci-lint` v2.11.4 `run --new-from-rev d23b897` on the changed packages, plain, `--build-tags integration` and `--build-tags scenario` | 0 issues each |
| `golangci-lint --enable-only=gosec` on the changed packages (`scenario`; `integration`) and on `./...` (plain; `integration,scenario`) | 0 issues, apart from one finding with `integration`: G602 at `adapters/store/postgres_integration_test.go:1563`. It was there before: the file is unchanged since `main`, and the `main` export reports the same finding |
| `gofmt -l` (Go 1.26.8) on the 150 `.go` files changed since `d23b897` | empty |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, no `-c model=` override) | Round 1 at `41c70dd8`: session `rollout-2026-10-14T16-10-25-01a13af6-f3bb-…`, `source: {"subagent":"review"}`, `"model":"gpt-6.1-sol"`. Round 2 at `c6938efd`: session `rollout-2026-10-14T16-26-49-01a13b05-f6d9-…`, same source and model. Both rounds returned "No actionable regressions were identified." Nothing to fix, so no third round |

**Merge `41c70dd8`: semantic check.** In every auto-merged file (`adapters/projection/service.go`,
`adapters/projection/sweep.go`, `core/worktreefs/tree.go`, `KNOWN-LIMITATIONS.md`), the merge's
change against each parent matches the other side's change against `d23b897` hunk for hunk.
Neither side lost or altered a line.

- **Baseline reset and the sweep.** On the up-to-date path, `resetSyncBaseline` now replaces the
  `idle@head` write. The wave 3 projection changes do not get in its way:
  - the TASK-003 refusals are reported on the diff path (`recordRefused`), which the up-to-date
    return never reaches;
  - the sweep's `unsafe_path` retirement is a row write, so it sets the `changed` flag that drives
    the reset;
  - so after a retirement only the baseline moves, as the catch-up intends.
- **Stored-definition comparisons.** The exact-integer `sameJSON` and `VerifyProjectionDefinition`
  are the only places that compare stored definitions, and wave 3 adds none. The keep-or-remove
  decision for an unsafe-path row (`retireUnsafeWorkflowRow` → `applyRejection`, and the sweep's
  `unsafe_path` branch) goes through `VerifyProjectionDefinition`, so it reads integers exactly
  too.
- **`core/worktreefs` and TASK-003.**
  - `git.DefaultMaxBlobBytes` is still defined as `worktreefs.DefaultMaxReadBytes`, which is
    unchanged.
  - `ReadFileLimit` still refuses on the Lstat size and still reads through
    `LimitReader(limit+1)`.
  - The non-blocking open and the `IsRegular` check only add a refusal before the read, and
    `clearNoBlock` makes the descriptor blocking again before `readCapped`.
  - TASK-005's `commitbinder.go` entry in the `scan_test.go` allowlist and the new
    `nonblock_*.go` files coexist; `core/worktreefs` passes, including under `-race`.
- **SMP.** `projectionsvc.New` passes a `*store.PostgresStore` (`NewPostgresStoreWithQuerier`), so
  SMP gets `SetSyncBaseline` without any change.
- **Defect found.** The only one: the CHANGELOG conflict resolution dropped the `---` rule before
  the catch-up section. `c6938efd` restores it (docs only).

**SMP compatibility.** `i23/s-int3/smp` is SMP `main` `f5c6fd6d`, detached, with `../spine` =
this worktree and `GOFLAGS=-mod=readonly`. The checkout was never modified, and `git status` is
clean.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` with no DSNs, on the 13 SMP packages that import a changed Spine package (test imports included) plus `internal/customer/workflow` | 11 ok; `projectionsvc` has no tests. Two packages fail with the same list as the wave 3 integration and review runs: `gitread` (`TestBounded_ErrorContractEqualsCLIClient`: `bad_ref`, `bad_sha`, `missing_path`, `path_beyond_a_symlinked_dir`) and `projectionwriter` (`TestBoundedProjection_IncrementalAddAndModifyOversized`, `TestBoundedProjection_OversizedWorkflowStalls`). These are the known SMP TASK-004 contract tests: they also fail against waves 1 and 2 on SMP `main`, and SMP's wave 3 branch fixes them. Reported here, not fixed |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3g_smp_it` with `vector`, `s3g_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, and `TEST_PROVISIONING_DSN` = `smp_prov` as in CI) | 1835 passed, 0 failed, 2 allowed Linux-only skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |

**Not run.**
- The other steps of SMP's integration job: the platform-DB shared suites, fenced migrate,
  workspace-schema, faultinject and verify-children. They are not in this gate list.
- Image-based gates (images, RDS emulation, an isolated `make dev-up` smoke). They need images
  built from these branches.
- GitHub CI. It cannot run this branch until Spine is pushed.

**Environment.**
- PostgreSQL: the shared PostgreSQL container on `localhost:55630`. Only `s3g_*` databases were
  created, and all of them were dropped. There were no `ws_*` databases or roles before the SMP
  suites ran and none after.
- Docker: the only containers started were the `git-floor` run (`--rm`) and the `alpine:3 df`
  disk checks. Nothing was pulled. The Docker VM had 6.3 GB free.
- Logs: `spine-management-platform/tmp/init027-orchestration/w3final/`.
