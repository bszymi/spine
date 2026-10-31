---
type: Architecture
title: Security Model
status: Living Document
version: "0.1"
---

# Security Model

---

## 1. Purpose

This document defines the security model for Spine at v0.x — how the system authenticates actors, enforces authorization, manages credentials, and maintains security boundaries between components.

The [Access Surface](/architecture/access-surface.md) defines the external interface and introduces authentication and authorization at a high level. This document makes those concepts concrete and extends them with credential management, component trust boundaries, and Git-level security guarantees.

All security decisions must comply with the [Constitution](/governance/constitution.md), particularly Actor Neutrality (§5) and Governed Execution (§4).

---

## 2. Security Principles

- **No anonymous access** — every operation must be attributable to an identified actor
- **Actor neutrality** — security constraints apply uniformly regardless of actor type (human, AI, automated)
- **Least privilege** — actors receive only the permissions required for their role
- **Credentials never in Git** — secrets, tokens, and credentials must never appear in artifact content or metadata
- **Trust the gateway, verify at boundaries** — internal components trust the identity established by the Access Gateway; security enforcement happens at defined boundaries, not everywhere
- **Auditability** — all authentication and authorization decisions must be traceable

---

## 3. Authentication

### 3.1 Actor Identity

Every request to Spine must carry a verified actor identity. The Access Gateway is responsible for establishing identity before any request reaches internal components.

**Identity attributes established at authentication:**

| Attribute | Description |
|-----------|-------------|
| `actor_id` | Stable unique identifier for the actor |
| `actor_type` | Classification: `human`, `ai_agent`, `automated_system` |
| `actor_role` | Authorization role (see §4) |
| `session_id` | Identifier for the current authentication session |

### 3.2 Authentication Methods

| Access Mode | Method | Identity Source |
|-------------|--------|-----------------|
| CLI | API token | Token maps to actor record |
| API | Bearer token (Authorization header) | Token maps to actor record |
| GUI | Session-based (login with credentials) | Credentials verified against actor record |
| Actor Gateway (inbound) | Service token | Pre-registered service identity for AI agents and automated systems |

**Note:** Local identity derived from Git configuration must not be used for authentication in shared or production environments. All access must be authenticated through the Access Gateway using valid credentials (e.g., API tokens or sessions).

### 3.2.1 Authentication by Actor Type

Spine supports different authentication mechanisms depending on actor type while maintaining a unified internal identity model.

- **Human actors** authenticate using interactive mechanisms (e.g., session-based login with credentials).
- **AI agents and automated systems** authenticate using service accounts and non-interactive credentials (API tokens).

Regardless of authentication method, all actors are normalized into the same internal identity model (`actor_id`, `actor_type`, `actor_role`) and are subject to the same authorization and workflow governance rules (Actor Neutrality).

Future versions may support stronger machine authentication mechanisms such as asymmetric key-based authentication or signed requests.

### 3.3 API Tokens

API tokens are the primary authentication mechanism for programmatic access.

**Token properties:**

- Tokens are opaque strings (no embedded claims)
- Each token is bound to exactly one actor
- Tokens have an optional expiration timestamp
- Tokens may be scoped to specific operations (read-only, contributor, full access)
- Tokens are stored as hashed values — the plaintext is returned only at creation time

**Token lifecycle:**

- Created by an admin or by the actor themselves (self-service)
- Revocable at any time by the owning actor or an admin
- Expired tokens are rejected immediately
- Revoked tokens are rejected immediately

**Security note:** API tokens are bearer credentials and must be treated as secrets with the same sensitivity as passwords.

### 3.4 Service Accounts

AI agents and automated systems authenticate via service accounts.

**Service account properties:**

- Each service account has a stable `actor_id` and `actor_type`
- Service accounts authenticate using API tokens (non-interactive credentials)
- Each service account may have multiple tokens for different environments or runtimes
- Tokens may be scoped and have expiration policies
- Service accounts are subject to the same authorization rules as human actors (Actor Neutrality §5)

**Best practices:**

- Do not share tokens across multiple agents or systems
- Use separate service accounts for distinct responsibilities (e.g., `ci-system`, `ai-reviewer`, `integration-webhook`)
- Rotate tokens regularly and revoke unused tokens

Service accounts provide identity for non-human actors but do not grant additional privileges beyond assigned roles and skills.

### 3.5 Session Management

| Access Mode | Session Duration | Refresh |
|-------------|-----------------|---------|
| CLI | Per-invocation (stateless) or cached token | Token re-read on each invocation |
| API | Per-request (stateless) | No session state; token on each request |
| Event stream (SSE) | Until disconnect, re-validated on every heartbeat | Bearer re-checked every 30 s; the stream closes when it fails |
| GUI | Time-limited session with refresh | Session expires after inactivity timeout |

**Long-lived streams (INIT-024 TASK-007, review finding N-14).** `GET /api/v1/events/stream` is the
one request that outlives its authentication. On every heartbeat (30 s) it re-runs the connect-time
checks against the stream's own bearer — token revocation, expiry, actor status and the
`events.read` permission, through the auth service that validated it (the workspace's own in
shared mode) — and closes with a final `: reauthentication required` comment frame when any of them
fails, or when the check itself cannot complete (fail closed). The client's reconnect then meets
the normal `401`. Revocation therefore takes effect on an open stream within one heartbeat
interval; events broadcast inside that window are still delivered.

**Pre-authentication work in shared mode (N-15).** A workspace's ServiceSet (database pool,
bootstrap admin, subscription-secret sweep) is built only for a request that carries a well-formed
bearer, because that bearer can only be validated against the workspace's own token store. Until it
is validated, every refusal — missing or malformed header, unresolved, inactive or unbuildable
workspace, missing auth service, unknown, expired or revoked token, suspended actor, token-store
error — is one `401` with one body on `/api/v1`, so a caller cannot tell an existing workspace from
a missing one by status or message; a request without a well-formed bearer is refused before the
workspace is even resolved. On `/git` the same holds for every failure of a well-formed bearer
(build failure, missing auth service, token-store error, every token state), for a missing or
malformed `Authorization` header, and for a push while receive-pack is off (the `403` naming the
flag is shown only to a caller authenticated for the workspace). The cause is logged server-side.

---

## 4. Authorization

### 4.1 Role-Based Access Control

Spine v0.x uses a role-based authorization model. Roles are assigned to actors, not to access modes.

| Role | Permissions |
|------|------------|
| `reader` | Read artifacts, query projected state, view Runs and execution history |
| `contributor` | Reader + create/update artifacts, submit step results, start Runs |
| `reviewer` | Contributor + approve/reject tasks, execute governance steps |
| `operator` | Reviewer + system operations (projection rebuild, health checks, Run cancellation), and registering actors with a role strictly below its own |
| `admin` | Full access including actor management (admin and operator actors included), the skill catalogue, actor skill grants, token management, and configuration |

A non-admin actor can never change its own skills. Skills decide who may claim review and approval steps, so creating, updating, renaming or deprecating a skill and granting or removing an actor's skill are admin-only, and the actor-skill routes refuse a non-admin caller targeting itself with `reason=self_skill_change_forbidden` (INIT-023 TASK-013).

### 4.2 Role Hierarchy

Roles are hierarchical — each role includes all permissions of the roles below it:

```
admin > operator > reviewer > contributor > reader
```

### 4.3 Enforcement Points

Authorization is enforced at multiple layers to ensure both coarse-grained and fine-grained control:

**Access Gateway (primary):**

- Every request is checked against the actor's role before being forwarded to internal components
- Unauthorized requests are rejected with a clear error before reaching the engine
- The Access Gateway attaches the verified `actor_role` to the internal request model

**Workflow Engine (step-level):**

- Workflow definitions may impose additional constraints on who can execute specific steps
- Step-level constraints are expressed in the workflow's `execution` block (e.g., `eligible_actor_types`, `required_skills`)
- These constraints are checked when a step is assigned, not at the gateway level

### 4.4 Step-Level Authorization

Workflow definitions may restrict step execution beyond role-based permissions:

```yaml
steps:
  - id: architecture_review
    execution:
      eligible_actor_types: [human]
      required_skills: [architecture_review]
```

The Workflow Engine evaluates these constraints when assigning actors to steps. An actor must have both sufficient role permissions and meet step-level requirements.

#### 4.4.1 Step-entry policy (INIT-023 TASK-007)

Before INIT-023 only the pull path (`ClaimStep`) checked step-level constraints;
`AssignStep` bound any actor ID to any step, no binding path looked at
`execution.mode`, preconditions or run status, and nothing stopped the executor
of a run from being bound to its review (audit P3-3, P3-4, P3-15, P3-16 and the
F3 separation-of-duties amplifier). The engine now enforces these through an
**opt-in** `engine.StepEntryPolicy` with independent switches:

| Switch | Effect |
|---|---|
| `EnforceRunStatus` | Assign, claim, acknowledge, result submission, step activation and retry refuse (409) a run that is not `active`; the candidate listing hides such runs; a late submit on an execution terminated with its run answers 409. |
| `EnforceEligibility` | The actor must exist and be `active` (404 / 403); on assign it must also be in the effective `eligible_actor_ids` list (403) and match `eligible_actor_types` (409). |
| `EnforceExecutionMode` | Engine-owned steps (`type: internal` or `mode: spine_only`) can never be assigned or claimed (409); `automated_only` / `ai_only` / `human_only` bind only `automated_system` / `ai_agent` / `human` actors (409). |
| `EnforcePreconditions` | The engine evaluates the step's preconditions at assign and claim (412), strictly ([Workflow Definition Format](/architecture/workflow-definition-format.md) §5.2); activation uses the same strict evaluator, so a failing step stays `waiting` and is not dispatched. |
| `RequireSkillsOnAssign` | `required_skills` on assign (409; 503 if no skill lookup is wired). |
| `SeparationOfDuties` | A run author cannot be bound to the run's `review` steps, and a live reviewer cannot take its author steps (403; 503 if the assignment ledger cannot be read) — by assignment, claim or engine auto-selection (which leaves the step `waiting`). A run author is an actor bound to an author execution of the run, or, for a planning-mode run, the actor who started it (INIT-024 TASK-001; below). The candidate listing does not offer an actor what this rule would refuse. Within a process a run's check and bind are serialised. See [Actor Model](/architecture/actor-model.md) §7.3. |
| `TerminateStepsOnCancel` | Cancelling or failing a run (including the scheduler's run timeout) moves its live executions to `skipped` / `run_terminated` and cancels their active assignments. Safe only together with `EnforceRunStatus`. |

**The planning starter is a run author (INIT-024 TASK-001, review finding N-3).** A
planning-mode run (`StartPlanningRun`: `POST /runs` with `mode: planning` or
`POST /artifacts/entry`; `StartWorkflowPlanningRun` / `StartWorkflowUpdatePlanningRun`:
`POST` / `PUT /workflows` below operator) writes the caller's content onto its branch at start,
and the caller is bound to no execution. Under `SeparationOfDuties`:

- every planning start needs an authenticated actor on the context (`domain.WithActor`; the
  gateway binds it for every authenticated request). Without one the start is refused with
  `ErrUnauthorized` (401) before any run row, branch or git read exists. An embedder with an
  `Actors` resolver must put the id it binds on the context: an id the resolver does not resolve
  is refused the same way;
- the engine records that actor in the run's `commit_meta` (`spine.governance.started_by`, the key
  the ADR-016 lifecycle exemption already reads) before it writes the content; a failed record
  aborts the start (branch deleted, run marked failed);
- the recorded starter is a run author, so it cannot be bound to the run's `review` steps (403,
  naming `(run start)`), whoever submitted the draft;
- a planning run with no recorded starter — started before the upgrade, or while the policy was
  off — can be reviewed by **nobody** (403, `(run start: starter not recorded)`): cancel and restart
  it, or run `SPINE_STEP_ENTRY_POLICY=legacy` for the migration window (`KNOWN-LIMITATIONS.md`).

The comparison is exact, like every separation-of-duties compare. A standard run's starter wrote
no content and is not an author.

Checks run in a fixed order and nothing is written before a bind is refused.
An embedder whose actors are not all rows in Spine's actor table supplies an
`Actors` resolver. The engine requires its answer to echo the requested actor ID
exactly, which stops a resolver from mapping one ID to a different identity. It
cannot tell a canonical handle from a variant the resolver chose to accept, so
the resolver must also refuse (not found) every input that is not byte for byte
a stored canonical handle — no case folding, trimming or lenient UUID parsing.
Only then can no alias of an actor defeat the exact-string separation-of-duties
and ownership compares. Standalone needs no resolver: actor lookup is an exact
match on `auth.actors`.

- **Standalone (`spine serve`)** installs `StrictStepEntryPolicy()` (every switch
  on) by default. `SPINE_STEP_ENTRY_POLICY=legacy` restores the pre-INIT-023
  behaviour with a startup WARN; any other value refuses to start. With a policy
  installed the gateway's engine-less step-assign fallback is disabled.
- **Embedders** get the zero value — today's behaviour — until they call
  `WithStepEntryPolicy` (SMP adopts it in its TASK-011).
- `required_skills` (and `RequireSkillsOnAssign`) is a real boundary in
  standalone because skill grants and the skill catalogue are admin-only
  (INIT-023 TASK-013, §4.1): a non-admin can no longer grant itself
  the skill a review step requires. Separation of duties refuses self-review
  independently of skills.

### 4.5 Authorization and Git Operations

When the Artifact Service commits to Git on behalf of an actor:

- The commit author is set to the authenticated actor's identity
- The actor must have sufficient role permissions for the operation that triggered the commit
- The Artifact Service does not perform additional authorization checks — it trusts the authorization established by the Access Gateway and Workflow Engine


### 4.6 Skills (Execution-Level Permissions)

In addition to role-based access control, workflows may require specific skills for step execution.

Skills are fine-grained, domain-specific permissions (e.g., `architecture_review`, `security_approval`, `deployment_access`).

**Properties:**

- Skills are assigned to actors alongside roles
- Skills are evaluated by the Workflow Engine (not the Access Gateway)
- Skills do not grant access to operations — they constrain who may execute specific workflow steps
- Roles define *what an actor can do*; skills define *which actor is eligible to perform a specific step*

Skills extend the authorization model at execution time but do not replace role-based access control.

---

## 5. Credential Management

### 5.1 Credential Categories

| Category | Examples | Storage |
|----------|----------|---------|
| Actor credentials | Passwords, API tokens | Access Gateway's credential store (hashed) |
| Integration secrets | External API keys, webhook secrets, LLM provider tokens | Runtime configuration (encrypted at rest) |
| Git credentials | SSH keys, access tokens for Git hosting | Runtime environment (not managed by Spine) |

### 5.2 Credential Rules

- **Never in Git** — no credential may appear in artifact content, metadata, commit messages, or any file tracked by Git
- **Never in events** — credentials must not appear in event payloads or log messages
- **Hashed at rest** — actor credentials (passwords, tokens) are stored as salted hashes
- **Encrypted at rest** — integration secrets are stored encrypted in runtime configuration
- **Scoped access** — components access only the credentials they need (Actor Gateway accesses LLM tokens; Access Gateway accesses actor credentials)

### 5.3 Credential Rotation

- API tokens may be rotated by creating a new token and revoking the old one
- Integration secrets are rotated by updating runtime configuration
- Rotation does not require Git commits (credentials are not governed artifacts)
- The system should support concurrent validity of old and new credentials during rotation windows
- The at-rest encryption key for integration secrets rotates through a key ring: the new primary key seals, previous keys (`SPINE_SECRET_PREVIOUS_KEYS`) still decrypt, so old and new keys are valid together until a re-encryption sweep has moved every value to the new key; each `enc:v2` value names its key and is bound to its workspace and row (INIT-023 TASK-009, `docs/integration-guide.md` §2.1)

### 5.4 Credential Validation on Artifact Writes

The Artifact Service should reject commits that contain patterns matching known credential formats (API keys, tokens, passwords) in artifact content. This is a best-effort safeguard, not a guarantee.

---

## 6. Component Trust Boundaries

### 6.1 Trust Model

```
Untrusted Zone          │  Trust Boundary        │  Trusted Zone
                        │                        │
CLI / API / GUI ────────┤── Access Gateway ──────┤── Artifact Service
External actors         │   (authenticates,      │   Workflow Engine
External integrations   │    authorizes)         │   Projection Service
                        │                        │   Event Router
                        │                        │   Validation Service
Actor Gateway ──────────┤── (outbound: trusted   │
  (inbound from actors) │    to untrusted)       │
                        │── (inbound: untrusted  │
                        │    to trusted)         │
```

### 6.2 Boundary Rules

**Access Gateway boundary (inbound):**

- All external requests are untrusted until authenticated
- The Access Gateway is the single point of authentication for external access
- After authentication, the internal request model carries verified identity — internal components trust this identity

**Actor Gateway boundary (outbound and inbound):**

- Outbound requests to actors (step assignments) carry trace context but not internal credentials
- Inbound responses from actors are untrusted — the Workflow Engine validates all actor responses against workflow constraints before accepting outcomes
- Actor responses that produce durable artifacts must pass through the Artifact Service's validation

**Inter-component trust:**

- Internal components (Artifact Service, Workflow Engine, Projection Service, Event Router, Validation Service) trust each other
- No inter-component authentication is required in a single-process deployment
- If components are extracted into separate services, mutual TLS or service tokens should be introduced

### 6.3 Actor Response Validation

Actor responses are untrusted input. The system must:

- Validate that the response corresponds to an active step assignment
- Validate that the response conforms to the expected output schema
- Validate that any artifacts produced conform to the artifact schema
- Not execute arbitrary code provided by actors
- Not trust actor-provided metadata for authorization decisions
- Not present actor-written records as server records. Execution evidence is the case in point
  (INIT-024 TASK-007, review finding N-17): `run.status` reads an active run's evidence from its run
  branch, which the run's author can push anything to, so every repository entry carries the ref it
  was read from (`source_ref`) and `verified`, true only for the authoritative branch (which only
  a governed merge or an operator override writes), and the summary lists `unverified_repositories`;
  `spine run inspect` prints an `UNVERIFIED` marker. The governed merge never reads this display
  path ([execution-evidence.md](/architecture/execution-evidence.md) §6.4.5).

---

## 7. Git Security

### 7.1 Commit Attribution

Every Git commit must be attributable to a specific actor:

- The commit author field maps to the actor's identity
- System-generated commits (e.g., projection rebuilds, automated transitions) use a dedicated system actor identity
- Commit messages include the `Trace-ID` trailer for correlation with runtime execution (per [Observability](/architecture/observability.md) §3.3)

### 7.2 Commit Signing (v0.x Guidance)

Commit signing is recommended but not required for v0.x:

- When enabled, commits are signed using the actor's GPG or SSH key
- The Artifact Service signs commits on behalf of actors using a service-level signing key
- Signature verification may be enforced at the Git hosting level (e.g., GitHub branch protection)

Commit signing becomes more important as the system matures and audit requirements increase.

### 7.3 Branch Protection

The authoritative branch (typically `main`) is protected **by Spine itself** (per [ADR-009](/architecture/adr/ADR-009-branch-protection.md)), not by an external forge. Spine is its own Git server (see [Boundaries §2.1](/product/boundaries-and-constraints.md)), so forge-level protection is not available — the enforcement point lives in `internal/branchprotect` and is consulted on both the Git push path and the Spine API write path (ADR-009 §3).

The ruleset lives at `/.spine/branch-protection.yaml` on the authoritative branch and is mirrored into the runtime `branch_protection_rules` table by the Projection Service. Two rule kinds are supported in v1:

- **`no-delete`** — blocks `git push --delete` and Artifact Service deletion paths on the named branch.
- **`no-direct-write`** — blocks any ref advance that is not a Spine-governed merge. Governed merges produced by the Artifact Service merge path (with an authorizing Run) are allowed; direct `artifact.create` / `workflow.create` / `workflow.update` without `write_context`, or raw `git push`, are not. Workflow writes are evaluated by branch protection too (INIT-023 TASK-014, audit F7): `workflow.Service` checks every non-bypass write as a direct write on its real target branch before touching the worktree, and an ADR-008 operator bypass requires an operator-or-above actor (it is the sanctioned, trailer-audited direct commit, so it is not rule-evaluated on the authoritative branch). In `spine serve` the reviewer path never reaches this check: reviewers are routed to a lifecycle planning run. The embedded library's zero value installs no policy (SMP writes only through its own admin bypass).

Bootstrap defaults apply to repositories that have not yet seeded the config file, so protection is correct even pre-seed — `main` is always `no-delete` + `no-direct-write` unless an operator explicitly relaxes it.

**Override.** Operators may bypass a rule per-operation:
- **Spine API path (available today):** `write_context.override: true` on the operation. Honored on artifact endpoints. Workflow endpoints still refuse `override` with `400` (no override event or trailer is emitted on the workflow surface); operators use the ADR-008 bypass there — see [INIT-018 EPIC-003 TASK-003](/initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-003-write-context-override.md).
- **Git push path:** `git push -o spine.override=true`, recognized by the pre-receive gate when `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (off by default; `docs/git-push-guide.md` §4).

Every honored API-path override produces a `branch_protection.override` governance event and adds a `Branch-Protection-Override: true` commit trailer. The Git path emits the event but does not rewrite pushed commits — the event is the sole audit record on that path. Per-actor allow-lists are out of scope for v1 — operator override is the only escape hatch.

Force pushes are prohibited on protected branches (commits are never rewritten, per Constitution §7); `no-direct-write` covers them as a subset, since a force push is a non-governed advance.

**Ref-class push admission (INIT-023 TASK-016).** Branch-protection rules gate user-facing *branches* only (ADR-009 §3, §6), so on the Git push path a second gate runs first, on every ref update, whether or not a rule set is wired (`core/refadmit`; class table in [API Operations](/architecture/api-operations.md) §6.1):

- Tags, notes and every other non-branch ref, `spine/init`, and the rest of the reserved `spine` namespace (under any case-fold spelling) are writable by `operator`/`admin` only. A pushed tag can no longer shadow a run branch or `main` in the engine's bare-name git calls (audit F10).
- A run, planning or divergence branch can be created or updated only by the actor who holds the **latest** execution of its current step (`assigned` or `in_progress`) on an `active` run, with role `contributor` or above, for the same repository — or by an operator. Nobody below operator may delete one (ADR-032 §5 parity). Even the bound actor is refused while the current step's declared type is `review` or `internal` (review freeze). This closes F2's push half: a non-owner can no longer move a run branch under review, or during the `committing` merge window, and so cannot get unreviewed content published by the governed merge.
- Nobody, operators included, may create a ref with a control byte or with a non-leading `refs` or any `HEAD` path component (such a name breaks every client push of the branch it shadows); deleting one stays allowed for cleanup.
- The same predicate governs API writes onto run branches (`write_context` with a `run_id`, `artifact.add`; INIT-023 TASK-014, audit P3-6), so pushes and API writes follow one rule. On the API path the review/internal freeze applies to operators as well (operators recover through `needs_rework`, cancel, or a push), and a waiting author step that someone held before is re-offered only to that prior holder under `spine serve`'s step-entry policy (`ReofferToPriorHolder`), so another eligible actor cannot claim a colleague's reworked step and then write as its bound actor.
- Everything fails closed: no actor, no run store, no step-definition lookup, an unknown run, a store error or an unresolvable step definition all refuse non-operators. Operators are never blocked by store state, so they remain the remediation route. Operator pushes to reserved refs are logged at Info; no governance event is emitted.
- **Limits.** Admission is decided when the push's command section arrives, and decided again — since INIT-024 TASK-005 (review finding N-9) — by the pre-receive hook once git has received and processed the pack and before any ref moves: the hook asks the server over a per-push channel (a Unix socket in a private directory and a one-time token, both passed in that push's CGI environment only), and the server re-runs the same admission, with the review freeze, against the store as it is then. A push whose step was submitted, reassigned or frozen while its pack was in flight is refused; so is one whose hook gets no answer (fail closed). What remains is the time between that answer and git's ref update. Merging exactly the reviewed commit is still the content-integrity control (INIT-023 TASK-005; always on in `spine serve`): a push that lands after the step's submit bound the head is not merged unless a later producer step binds it — an `automated` step such as a validate or summarize re-binds to whatever head it completes at unless its submitter sends `expected_head_sha` (KNOWN-LIMITATIONS "Governed Merge Binding" 2; the shipped `artifact-creation`, `adr-creation`, `document-creation` and `task-spike` workflows have such a step before their review) — and an approval of a branch that moved since the previous binding is refused (`409 run_branch_moved_since_gate`); an embedder that does not enable merge binding keeps the gap. A bound actor may still force-push the branch it holds: fast-forward-only cannot be enforced before the pack is unpacked. Every push presents a bearer: since INIT-023 TASK-015 no setting lets a push skip authentication (`SPINE_DEV_MODE` no longer affects the git endpoint, and `SPINE_GIT_HTTP_DEV_NO_AUTH` opens development reads only), so admission always sees an actor.

**Governance-path guard (ADR-016; INIT-023 TASK-006, audit F3).** `no-direct-write` lets governed merges through by design, and before this guard a run branch could carry `rules: []` or a review-less workflow into `main` through one: the projection then installed it as live policy. Two guards close that "run-branch slippage"; both are opt-in in the embedded library (zero value: v1) and on in `spine serve` (`SPINE_GOVERNANCE_GUARD=off` restores v1):

- **Merge guard (engine).** A governed merge that changes `.spine/`, `.spine.yaml`, `workflows/` or `<artifacts_dir>/workflows/` — and, as a backstop built at each decision (INIT-024 TASK-002), any `*.yaml` / `*.yml` file directly inside a directory named `workflows`, whatever the embedder configured — is **held** in `committing` until an `operator`/`admin` approves exactly the bound commit (`POST /runs/{run_id}/governance-decision`; `governance.merge_held`, `governance.merge_decided`). The changed paths are the union of the diffs from **every** merge base of `main` and the merged commit (a single merge base misses a governance file a criss-cross history reverts), and after the merge, before the push, the merge may not have changed any governed path the evaluation did not cover (refused permanently otherwise, the local target restored). Matching is case-folded and fail-closed (unsafe or undecodable paths are governed). The approval is a server-side record in reserved `runs.commit_meta` keys that workflow outcomes cannot write, recorded only while the run is still `committing`; a reject revokes it, and decisions are serialised with the run's merge attempts (in process), so a reject never takes effect underneath an approved merge in flight. A merge that can never be evaluated (no merge base, an oversized diff) is refused permanently, not left `committing`. Exemptions: a workflow-lifecycle planning run's own `workflows/<id>.yaml` (`spine serve` only; never the governing workflow, never a file that governs workflows, and only when the reviewer neither started the run nor is one of its authors), and the run's own `.spine/runs/<run id>/evidence/`.
- **Protection-removal guard (projection).** A branch-protection config that removes a protection kind from the authoritative branch is installed only when a database record of an operator act that **changed** the config matches it byte for byte — an honoured override push, or an approved governed merge's consumption marker whose own diff removed the protection, or whose base config is byte for byte the result of an earlier such record that accounts for it in turn (a chain of custody, so two authorised relaxations in one sync window both count) — and that record is reachable from the sync head and newer than the installed rules. Otherwise the rules stay, `branch_protection.removal_refused` is emitted, and the sync still advances (not a sync error) — unless an approved merge is in flight without its consumption marker yet, which holds the sync for a retry. Commit trailers are never trusted. The guard is inactive while `no-direct-write` is already off on the authoritative branch. Recovery after a database rebuild, or for a relaxation that arrived another way, is an override push that edits the file ([Git Push Guide](/docs/git-push-guide.md)). The override's record is its `branch_protection.override` row in `runtime.event_log`, which the push path writes synchronously, before git moves the ref (SMP's emitter does the same; `spine serve` does it with the guards on, independent of event delivery).

### 7.4 Repository Access

- The Artifact Service requires read/write access to the Git repository
- The Projection Service requires read access
- No other component requires direct Git access
- Actor access to the repository should be mediated through Spine, not through direct Git operations

**The git HTTP endpoint (standalone `spine serve`; INIT-024).** Runners and operators reach a
workspace's repository over `/git/{workspace}/…`, which serves only the git protocol's own paths
at the root of the repository it resolved (`403` for any other path; INIT-024 TASK-004). Its
controls:

- **Every caller is authenticated for the workspace, or is a trusted runner bound to it**
  (INIT-024 TASK-006, review finding N-2). A token-less read is allowed only to a directly
  connected peer whose address `SPINE_GIT_HTTP_TRUSTED_CIDRS` binds to the workspace the request
  resolved to (or, with `SPINE_ENV=development` only, to any client under
  `SPINE_GIT_HTTP_DEV_NO_AUTH`); in shared mode only the per-workspace form is accepted at start.
  Every other caller without a valid token for that workspace gets one uniform `401`, whether or
  not the workspace exists (the one exception: a single-mode server with no auth service answers
  `503` to a caller that presents a bearer). Pushes always need a bearer.
- **What a push can make the server write is bounded** (INIT-024 TASK-003, review finding N-1).
  The tree of every pushed commit is measured at pre-receive — entries, blob bytes and summed path
  bytes — and an over-budget push is refused before any ref moves. Every tree the server itself
  checks out, merges, restores or clones is measured the same way before anything is written, and its
  repositories carry neutral checkout attributes, so a checkout writes exactly the stored bytes
  that were measured. The budget is on in `spine serve` with no off switch; in the embedded
  library it is opt-in (`git.WithTreeBudget`, `git.TreeBudget.Check`).
- **Pushes need case- and normalization-sensitive storage** (INIT-024 TASK-005, N-8). Branch
  protection matches ref names byte for byte, so a repository whose storage folds names has
  receive-pack refused (reads are still served).
- **Admission is decided again when the push is applied** (INIT-024 TASK-005, N-9; §7.3).
- **Capacity is shared fairly** (INIT-024 TASK-005, N-13). A request takes a slot from its
  principal's, its workspace's and the process's caps, and a push takes one only after its command
  section has arrived, within a deadline, and been admitted.

The residuals are in `KNOWN-LIMITATIONS.md` ("Git HTTP: Token-Less Clones Are for Direct Peers
Only", "Checkout Budget", "Git Push Gate: Served Repositories Need Case- and
Normalization-Sensitive Storage").

---

## 8. Sensitive Data Handling

### 8.1 What Must Not Be Stored in Git

- Credentials (passwords, API keys, tokens, secrets)
- Personally identifiable information (PII) unless explicitly governed
- Encryption keys or certificates
- Runtime configuration containing secrets

### 8.2 Log and Event Sanitization

- Log entries must not contain credentials or tokens
- Event payloads must not contain sensitive data
- Error messages should not leak internal system details to external actors
- Trace IDs and run IDs are safe to include in all contexts


### 8.3 Audit Logging

The system must record audit-relevant security events, including:

- Authentication attempts (success and failure)
- Authorization decisions (allow/deny)
- Token creation, usage, and revocation
- Actor-initiated operations affecting durable state (e.g., Git commits)

Audit logs are append-only and must not contain sensitive data such as credentials or tokens.

Audit logging is required for governance visibility but may be implemented incrementally in v0.x.

---

## 9. Constitutional Alignment

| Principle | How the Security Model Supports It |
|-----------|-----------------------------------|
| Actor Neutrality (§5) | All actors authenticate and authorize through the same mechanism; roles apply uniformly |
| Governed Execution (§4) | Authorization enforcement ensures actors can only execute operations permitted by their role and workflow constraints |
| Source of Truth (§2) | Credentials are excluded from Git; commit attribution preserves authorship integrity |
| Reproducibility (§7) | Commit signing and attribution ensure audit trail integrity |

---

## 10. Cross-References

- [Access Surface](/architecture/access-surface.md) §4 — Authentication and authorization overview
- [System Components](/architecture/components.md) §4.1 — Access Gateway, §4.6 — Actor Gateway
- [Domain Model](/architecture/domain-model.md) §3.4 — Actor entity
- [Observability](/architecture/observability.md) §3 — Trace ID propagation, §4 — Audit trail
- [Error Handling](/architecture/error-handling-and-recovery.md) §9 — Operator visibility
- [Constitution](/governance/constitution.md) §4 — Governed Execution, §5 — Actor Neutrality

---

## 11. Evolution Policy

This security model is expected to evolve as the system is implemented and security requirements become more concrete.

Areas expected to require refinement:

- Fine-grained permissions (per-artifact or per-workflow access control)
- OAuth2 / OIDC integration for human authentication
- Mutual TLS for inter-component communication in distributed deployments
- Audit log for authentication and authorization events
- Rate limiting and abuse prevention
- Commit signing enforcement policy

Changes that alter the trust model, authentication mechanism, or authorization enforcement should be captured as ADRs.
