---
id: TASK-006
type: Task
title: "Workspace-scoped trusted-CIDR clone in shared mode"
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
