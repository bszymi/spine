---
id: TASK-015
type: Task
title: "SSE field safety, shared-mode tenant scoping, git-endpoint auth and gitpool token-host scoping"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
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
