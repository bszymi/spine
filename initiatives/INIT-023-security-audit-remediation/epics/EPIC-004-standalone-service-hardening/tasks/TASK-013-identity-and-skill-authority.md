---
id: TASK-013
type: Task
title: "Admin-only skill management, actor role ceiling, and bootstrap-admin token hygiene"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: bugfix
created: 2026-10-06
last_updated: 2026-10-07
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/epic.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: /architecture/actor-model.md
  - type: related_to
    target: /architecture/api-operations.md
  - type: related_to
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-014-actor-skill-association-http-api.md
  - type: related_to
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-016-skill-deprecation-guard.md
  - type: related_to
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-002-platform-binding-bootstrap/tasks/TASK-001-bootstrap-internal-admin.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-018-scenario-bootstrap-admin-idempotency.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-027-bootstrap-admin-collision-strict-fail.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-007-step-entry-guards.md
---

# TASK-013 — Admin-only skill management, actor role ceiling, and bootstrap-admin token hygiene

---

## Purpose

This task closes four findings from the 2026-10-06 security audit. All four are about who may
change identity and authority in a workspace:

- **F6 (P2).** Any contributor can grant itself any skill, or strip a skill from someone else.
  That defeats every `required_skills` review gate.
- **P3-33.** An operator can create admin-role actors.
- **P3-31.** Rotating `SMP_ADMIN_TOKEN` does not revoke the old bearer. The bootstrap also
  re-activates a suspended or deactivated `smp-admin` on every workspace load.
- **P3-35 (dormant).** The bootstrap `token_id` exposes 48 bits of `sha256(SMP_ADMIN_TOKEN)`, and
  nothing checks the strength of `SMP_ADMIN_TOKEN`.

All four are **standalone-only** in effect. `BootstrapInternalAdmin` lives in `core/auth`, and SMP
calls it, so the bootstrap half changes a code path SMP runs. The change is inert there, and
Design §13 explains why.

Line numbers refer to Spine `61994fe`. Lines marked "SMP" refer to `spine-management-platform` at
`80995559`, which embeds Spine `61994fe` (`.spine-revision`).

### The defects

1. **Skill management is a contributor operation (F6).**
   - `core/auth/permissions.go:80-83` maps `skill.create`, `skill.update` and `skill.deprecate` to
     `RoleContributor`.
   - The actor-skill routes `POST` and `DELETE /api/v1/actors/{actor_id}/skills/{skill_id}`
     (`service/internal/gateway/routes.go:129-130`) authorize only `skill.update`
     (`handlers_actor_skills.go:10`, `:38`). They never compare `{actor_id}` with the caller
     (`:19`, `:47`).
   - Step eligibility matches skills by **name** (`core/engine/claim.go:115-141`,
     `core/actor/service.go:113-118`). `skill.update` can rename a skill
     (`handlers_skills.go:140-142`), and deprecation removes it from every holder's eligible set
     (`claim.go:127`).
   - So a contributor can do any of these:
     - grant itself `review`, then claim and accept the review step of its own run, after which the
       publish step merges the run into `main`;
     - strip `review` from the real reviewer;
     - deprecate `review` with `?force=true` (`handlers_skills.go:182-191`), which makes every
       review step unclaimable for everyone;
     - rename a skill to `review`, which makes all its holders eligible.
   - The docs say the opposite. `actor-model.md:98` says "Role and skills may be updated by an
     admin". `security-model.md:133` reserves actor management for `admin`.
2. **No role ceiling on `actor.create` (P3-33).**
   - `actor.create` is `RoleOperator` (`permissions.go:77`).
   - `handleActorCreate` (`handlers_actors.go:17-78`) accepts any valid requested role
     (`:45-52`) and registers it (`:71-75`).
   - An operator can therefore create `admin` actors. This is latent: minting a token
     (`token.create`) is admin-only (`permissions.go:95`). But it contradicts `security-model.md:133`
     and stages an escalation for the day any token path opens.
3. **Rotation leaves the old bearer valid, and suspension is undone (P3-31).**
   - `upsertInternalAdminToken` (`core/auth/bootstrap.go:103-145`) only ever inserts a row for the
     new hash. Its own doc comment admits that the stale row stays (`:54-57`).
   - The old row stays bound to the admin-role `smp-admin`, with no expiry, so a leaked bearer that
     was rotated away keeps full admin rights.
   - `upsertInternalAdminActor` (`:71-101`) builds `desired` with `Status: ActorStatusActive`
     (`:77`). It compares status (`:158-162`) and writes it back (`:87`).
     - So a `smp-admin` that an operator suspended or deactivated during an incident becomes
       active again on the next workspace load.
     - That load happens on every pooled `ServiceSet` build, via `newPooledWorkspaceBuilder`
       (`service/cmd/spine/serve_resolver.go:198-210`).
     - This bypasses Spine's own rule that deactivation is permanent
       (`core/actor/service.go:59-61`).
4. **The token id discloses the bearer hash, and the bearer is unchecked (P3-35).**
   - `internalAdminTokenID` returns `"bootstrap-" + sha256(bearer)[:12]` (`bootstrap.go:147-149`).
   - That id is listable through `GET /api/v1/tokens?actor_id=smp-admin` by a workspace admin
     (`handlers_tokens.go:85-107`). It is also logged on every insert (`bootstrap.go:143`).
   - `SMP_ADMIN_TOKEN` (`serve_config.go:61`) is seeded into **every** workspace that the process
     serves (`serve_resolver.go:235-252`). It gets no strength check, unlike
     `SPINE_OPERATOR_TOKEN`, which has a 32-character floor (`serve_config.go:21`, `:196-204`).
   - So a tenant admin of one workspace gets an offline oracle for a weak platform bearer that is
     admin in every workspace.
   - `auth.Service.CreateToken` already avoids this for ordinary tokens with a random `tok_` id
     (`core/auth/auth.go:64-69`).

### Who can exploit this, and the impact

| Finding | Attacker | Impact | Embedded (SMP) |
|---|---|---|---|
| F6 | **A2**: any contributor. `POST /actors` defaults new actors to contributor (`handlers_actors.go:45`), AI agents included. | Self-review: a contributor's own run is accepted and merged into `main` with no second person. Review gates can be emptied (strip, deprecate, rename): a governance DoS. | Not reachable. SMP never imports `service/`. It maps skill operations to workspace admin (SMP `internal/customer/workspace/permissions.go:64-67`) and has no actor-skill grant API. |
| P3-33 | **A2** with operator role | Creates admin-role actors. Latent until any non-admin token path exists. | Not reachable. SMP provisions actors itself. |
| P3-31 | Anyone holding a rotated-away `SMP_ADMIN_TOKEN` (a leaked platform bearer) | Keeps admin on every workspace after rotation. An operator's suspension or deactivation of `smp-admin` is silently undone on the next workspace load. | The code runs (SMP `provision_steps_runtime.go:423-428`). The rows it writes are not consulted: no live SMP path validates `auth.tokens` (SMP `internal/recovery/release.go:18-24`; `spinecore/tokenstore.go:147,175` have no non-test caller). |
| P3-35 | A workspace admin in a shared or platform-binding standalone deployment | 48-bit offline check of a guess for the platform bearer. With a weak bearer, that is cross-tenant admin. | Not reachable. No SMP surface lists `auth.tokens`. |

### Verified against `61994fe`

All seven audit PoCs reproduce unchanged with `go test -overlay`:

- `poc/skeptic-skills`: self-assign `review` gives 200, `DELETE` of the reviewer's skill gives 204, and forced deprecation gives 200.
- `poc/verify-r1-4`: self-grant, then claim, then `accepted`, then `engine git.Merge: source=spine/run/run-1 target=main`.
- `poc/standalone-service/poc_skill_selfgrant_test.go` and `poc/engine-authz/poc_skill_selfgrant_test.go`.
- `poc/crypto-secrets-auth/operator_mints_admin_test.go`: an operator `POST /actors role=admin` gives 201.
- `poc/crypto-secrets-auth/bootstrap_rotation_test.go`: the old bearer still validates, and its id is `bootstrap-<sha256[:12]>`.
- `poc/gap1-crypto-secrets-reaud/auth_bootstrap_poc_test.go`: a deactivated `smp-admin` is re-activated.

## Design

### Design-phase verification

The design below was prototyped against `61994fe` and SMP `80995559` with `go test -overlay`. No
repository file was changed.

- Prototype sources: `spine-management-platform/tmp/init027-orchestration/spine-init023/prototypes/TASK-013/`.
- Overlays: `…/_work/task013/`.

Results:

- **Before the fix.** Every regression test in the Acceptance Criteria failed against an
  inert-plumbing stub.
- **After the fix.** Every test passed with the full change.
- **Whole-tree suites.** `go test ./...` and `go vet ./...` stayed green, as did `go vet -tags scenario`.
- **Mutations.** All 16 mutations listed in the Acceptance Criteria turned at least one test red.
- **SMP.** SMP built and its workspace, recovery, `spinecore` and `customer-app` test packages
  passed against the prototype.

These are design notes. The implementer re-derives the change on the task branch.

> **Design-check note (2026-10-06): what the prototype did and did not cover.**
> - **Not covered by the prototype:** the AC 12 wiring test in `serve_delivery_test.go` and its
>   `serve_resolver.go` mutation. The design check prototyped and confirmed them separately
>   (§12 note).
> - **Prototype gaps found by the check:** the prototype still has the gaps in the §2 and §3
>   notes (`AuthorizeRoleGrant` fail-open, encoded self id). The amended AC 3, AC 7, AC 10 and
>   AC 21 are new, and they have not been run against a fixed prototype.
> - **Artifacts:** the design-check probes and overlays are in
>   `spine-init023/_work/task013-check/`.

### 1. Permission map (`core/auth/permissions.go:76-83`)

```go
"actor.create": domain.RoleOperator,           // unchanged; capped by AuthorizeRoleGrant (§4)

"actor.add_skill":    domain.RoleAdmin,          // new (api-operations.md §3.6 names)
"actor.remove_skill": domain.RoleAdmin,          // new

"skill.create":    domain.RoleAdmin,             // was RoleContributor
"skill.read":      domain.RoleReader,            // unchanged
"skill.update":    domain.RoleAdmin,             // was RoleContributor
"skill.deprecate": domain.RoleAdmin,             // was RoleContributor
```

The actor-skill routes stop borrowing `skill.update`. Editing the catalogue and granting a skill to
an actor are different privileges, and the two new names match `api-operations.md` §3.6
(`actor.add_skill` and `actor.remove_skill`). `skill.read` and the list route
(`handlers_actor_skills.go:58-76`) do not change.

### 2. New `core/auth/grants.go`

This file imports only `core/domain`, so it adds no ADR-017 boundary crossing.

```go
const (
    ReasonSelfSkillChange = "self_skill_change_forbidden"
    ReasonRoleCeiling     = "role_ceiling_exceeded"
)

// A caller below admin may never change its own skill set. This check runs
// before the role check and carries its own reason. Then Authorize(caller, op).
func AuthorizeActorSkillChange(caller *domain.Actor, op Operation, targetActorID string) error

// An admin may grant any role. Any other caller may grant only roles strictly
// below its own. Call this after Authorize(caller, "actor.create").
func AuthorizeRoleGrant(caller *domain.Actor, granted domain.ActorRole) error
```

- **Errors.** Both return `domain.ErrForbidden` with a `map[string]string` detail. The detail
  carries `reason`, the caller's role, the operation, and for the ceiling, `granted_role`. The
  gateway maps that to 403, and the detail appears in the error envelope
  (`service/internal/gateway/response.go:37-50`).
- **The self check runs only when `caller.Status` is active.** A suspended caller still gets
  `Authorize`'s status-class error. That keeps the ordering that
  `TestAuthorize_StatusCheckPrecedesRoleCheck` pins.
- **Why the self check runs first.** While the role map says admin-only, the self check is
  defence in depth. The distinct reason is what makes it observable:
  - the most suspicious request (an actor granting itself a review skill) gets its own response
    reason and audit line;
  - a mutation that deletes the self check is caught (AC 3).

> **Design-check note (2026-10-06): `AuthorizeRoleGrant` must fail closed on its own.** The
> prototype (`prototypes/TASK-013/core/auth/grants.go`) returns nil for
> `AuthorizeRoleGrant(operator, "")`, `(operator, "superuser")` and `(operator, "Admin")`, because
> `RoleLevel()` is 0 for any unknown role and `0 >= 4` is false. It also returns nil for a
> **suspended** admin. Both were reproduced with an overlay probe
> (`_work/task013-check/designcheck_test.go`, X3 and X4). `handleActorCreate` is not exposed today:
> it rejects unknown roles at `:48-51` and calls `Authorize` first. But the helper is exported
> from `core/auth`, so it is a public authorization primitive and must not depend on its caller's
> ordering. Required behaviour:
> - a non-active caller gets the same status-class `ErrForbidden` detail as `Authorize`, checked
>   first;
> - an unknown or empty `granted` role (`RoleLevel() == 0`) is refused with
>   `reason=role_ceiling_exceeded`, admins included;
> - after that, admin may grant any role and others only strictly lower roles, as designed.
>
> AC 7 gains the matching rows.

### 3. Actor-skill routes (`service/internal/gateway/handlers_actor_skills.go:9-56`)

- **New helper.** Add `(*Server).authorizeActorSkillChange(w, r, op, targetActorID) bool` in a new
  `service/internal/gateway/authz_identity.go`.
  - **Dev mode.** With no actor in the context, it delegates to `authorize()`
    (`middleware.go:253-268`). That keeps today's dev-mode allow-all and non-dev 401.
  - **Otherwise** it calls `auth.AuthorizeActorSkillChange`.
  - **On a `self_skill_change_forbidden` refusal** it emits
    `observe.AuditLog(ctx, "actor_skill_change_refused", "operation", op, "target_actor_id", id, "reason", …)`,
    then writes the error.
- **Assign.** `handleActorSkillAssign` reads `{actor_id}` and `{skill_id}`, then calls
  `authorizeActorSkillChange(w, r, "actor.add_skill", actorID)` in place of
  `authorize(…, "skill.update")` at `:10`. After a successful `AddSkillToActor` (`:29`) it emits
  `observe.AuditLog(ctx, "actor_skill_added", "target_actor_id", …, "skill_id", …, "skill_name", …)`.
- **Remove.** `handleActorSkillRemove` does the same with `"actor.remove_skill"` in place of
  `:38`, and emits `actor_skill_removed` after `RemoveSkillFromActor` (`:50`).
- **Audit lines** follow the existing pattern for governance-significant actions
  (`core/observe/audit.go:8`, used at `core/artifact/acceptance.go:20`). The caller's `actor_id`
  is already on the logger through `observe.WithActorID` in `authMiddleware`.

> **Design-check note (2026-10-06): a percent-encoded `{actor_id}` evades the self check.** chi
> routes on `r.URL.RawPath` when it is set, so `chi.URLParam` returns the segment still escaped.
> Nothing in `service/internal/gateway` unescapes path params. Against the prototype,
> contributor-1's `POST /actors/contributor%2D1/skills/sk-1` reached `Authorize` as a grant to
> "another" actor. It got a plain insufficient-role 403 with no `self_skill_change_forbidden`
> reason and no `actor_skill_change_refused` audit line (overlay probe
> `_work/task013-check/encoding_test.go`).
>
> Today this is **not** a privilege bypass, for three reasons:
> - the role map is admin-only;
> - `validActorID` (`core/actor/service.go:14`) forbids `%`;
> - `auth.actor_skills.actor_id` has a foreign key to `auth.actors` (`migrations/010`), so a
>   phantom id cannot hold a grant.
>
> It does, however, defeat Decision 3's stated purpose: a future loosening of the map would leave
> the self check bypassable by encoding. Required change: both actor-skill handlers decode
> `{actor_id}` once with `url.PathUnescape`, answer 400 `invalid_params` on a malformed escape, and
> pass the decoded id to **both** `AuthorizeActorSkillChange` and the store call. Do not resolve the
> target with `GetActor` before authorization: a 404-before-403 order would give non-admins an
> actor-existence oracle. AC 3 gains the encoded case.
>
> **Design-check note (2026-10-06): other paths to the same sinks, verified closed.**
> - **Sinks.** A grep of non-test Go in Spine finds no writer of `auth.skills`,
>   `auth.actor_skills` or `auth.actors` beyond the three handlers this task changes,
>   `core/actor/service.go` (no non-test caller of `AddSkill`/`RemoveSkill`) and the bootstrap.
> - **API surface.** `PATCH /skills` has no status field, so there is no un-deprecate path
>   (*review-round-1 correction:* except a PATCH racing a deprecation, which wrote the stale status
>   back; both handlers now re-read before writing, see Implementation Notes, Review round 1). No
>   actor role or status update route exists. The `api/spec.yaml` contract does not cover these
>   routes.
> - **Claim-side check.** It is live in standalone, because `WithBlockingStore(ss.Store)` is wired
>   at `serve_resolver.go:82` and `serve_wiring.go:405`. Its silent skip when no store is wired
>   (`claim.go:116`) is left alone by TASK-007 (its Decision 10).
> - **Default workflows.** Every review step in `workflows/*.yaml` requires the `review` skill
>   (or `workflow_review`). So once grants are admin-only, a contributor without the skill cannot
>   claim a review step.
> - **Operator assignment.** An operator can still bind anyone to a step through `step.assign`
>   with no skill check (P3-3). That is TASK-007's `RequireSkillsOnAssign`, not this task.

### 4. Skill catalogue mutations (`service/internal/gateway/handlers_skills.go`)

- **Create and update** (`:24-67`, `:118-156`). These are admin-only through §1. No other change
  to create.
- **Rename guard in `handleSkillUpdate`** (`:140-142`). When `req.Name` is set and differs from
  `skill.Name`, call `workflow.FindWorkflowsReferencingSkill`
  (`core/workflow/skill_references.go:24-40`) for **both** the old name and the new name.
  - If any active workflow references either name and `?force=true` is absent, answer 409. The
    body has the same shape as deprecation's (`:184-189`): `workflows`, plus a
    `hint: "use ?force=true to rename anyway"`.
  - After `UpdateSkill` succeeds, emit
    `observe.AuditLog(ctx, "skill_renamed", "skill_id", …, "from", old, "to", new)`.
  - A rename changes who satisfies `required_skills` exactly as deprecation does. That is the
    lever F6's "rename a skill to `review`" uses.
- **Deprecate** (`:158-204`). It stays admin plus `?force=true` when the skill is referenced
  (`:176-191`, unchanged). After `UpdateSkill` (`:194`) it emits
  `observe.AuditLog(ctx, "skill_deprecated", "skill_id", …, "skill_name", …, "forced", len(refs) > 0, "referencing_workflows", len(refs))`.

> **Design-check note (2026-10-06): limits of the rename guard.**
> - **When "rename to `review`" is a lever.** `auth.skills.name` is `UNIQUE`
>   (`migrations/009_skills_table.sql:8`), so renaming `x` to `review` in Postgres only succeeds
>   when no `review` row exists, for example when a workflow names a skill that was never added to
>   the catalogue. A deprecated `review` row still holds the name. When the row exists, the rename
>   fails with an unmapped unique violation (500), today and after this task. AC 5's "rename `x` to
>   `review`" case must therefore use a fixture with **no** `review` skill, so it models the real
>   lever. The gateway fake does not enforce uniqueness.
> - **Scope.** Like deprecation, the guard only scans **active** workflow projections
>   (`skill_references.go:25`). A run pinned to a superseded workflow version that names the skill
>   is not detected. This is accepted: it is consistent with deprecation, and the endpoint is
>   admin-only.

### 5. Actor creation ceiling (`service/internal/gateway/handlers_actors.go:17-78`)

- **Ceiling check.** After the role is parsed (`:45-52`) and before the id is generated (`:54`):
  if the caller is not nil (nil means dev mode, which `authorize()` already allowed), call
  `auth.AuthorizeRoleGrant(caller, actorRole)`.
  - On refusal, emit `observe.AuditLog(ctx, "actor_create_refused", "granted_role", …, "reason", …)`
    and write the 403.
  - After `Register` succeeds (`:71-75`), emit
    `observe.AuditLog(ctx, "actor_created", "created_actor_id", …, "granted_role", …)`.
- **Resulting matrix:**

  | Caller | May create |
  |---|---|
  | operator | reader, contributor, reviewer |
  | admin | any role |
  | reviewer and below | nothing; `actor.create` is operator, as today |

### 6. Bootstrap: never re-activate `smp-admin` (`core/auth/bootstrap.go:71-101`)

- **Status.** In the `err == nil` branch, if `existing.Status` is `suspended` or `deactivated`, set
  `desired.Status = existing.Status` and log at Warn: "internal admin actor is not active;
  bootstrap leaves its status unchanged and the platform bearer will be refused". The log carries
  `actor_id` and `status`.
- **Name and role** are still healed, and an empty or unknown status is still healed to `active`.
  That keeps `TestBootstrapInternalAdmin_HealsStaleActor`'s name/role contract (see Decision 6).
- **Create path.** When the actor is not found (`:92-97`), it is still created `active`.

> **Design-check note (2026-10-06): pin the "heal empty status" half.** No existing test pins it.
> `TestBootstrapInternalAdmin_PropagatesStoreErrors`' "UpdateActor failure" row seeds an empty
> status, but its empty name already forces the update. A plausible drift, "preserve any
> non-active status", would leave an empty-status `smp-admin` empty forever. Its bearer would then
> be refused as `actor ` with no log. AC 10 gains that row.
>
> **Design-check note (2026-10-06): future actor-status API.** "Operator intent" here means the
> **platform** operator, who has database access. If an actor suspend or deactivate endpoint is
> ever added in standalone, a workspace admin calling it on `smp-admin` would durably lock the
> platform out of that workspace, because this task makes the bootstrap respect the status. That
> endpoint must treat `smp-admin` as platform-operator-only. This is listed under Out of scope.

### 7. Bootstrap: revoke superseded bearers on rotation (`bootstrap.go:58-69`, `:103-145`)

- **Config.** `BootstrapAdminConfig` gains one optional field:

  ```go
  // Rows created at or after SupersededBefore are never revoked. Zero means
  // there is no extra bound: every older bootstrap row is revoked.
  SupersededBefore time.Time
  ```

- **`upsertInternalAdminToken`** now returns `(*domain.Token, error)`: the current bearer's row.
  1. **Hash found, bound to `smp-admin`, not revoked.** Return that row. Today this branch
     returns nil (`:109-113`).
  2. **Hash found, bound to `smp-admin`, revoked.** Log at **Error**: "SMP_ADMIN_TOKEN matches a
     revoked bootstrap admin token; the platform bearer will be refused until it is rotated to a
     fresh value", with the `token_id`. Return `(nil, nil)`. Nothing is un-revoked, and nothing
     else is revoked.
     - `GetActorByTokenHash` returns revoked rows; its SQL has no `revoked_at` filter
       (`adapters/store/postgres_tokens.go:15-20`).
     - So without this branch, a rollback to a revoked bearer is silent.
  3. **Hash bound to another actor.** Unchanged: `ErrAdminTokenHashCollision` (`:114-126`).
  4. **Not found.** Insert with a new random id (§8) and return the inserted row.
- **`BootstrapInternalAdmin`.** Once it has a current row, it calls
  `revokeSupersededAdminTokens(ctx, st, current, cfg.SupersededBefore, log)`.
  - That function lists `ListTokensByActor(ctx, "smp-admin")`
    (`adapters/store/postgres_tokens.go:52-61`).
  - It calls `RevokeToken` (`:43-50`) on every row that meets **all** of these:
    - it is not the current row and not already revoked;
    - `Name == "smp-admin-bootstrap"` and `TokenID` starts with `bootstrap-`. Admin-minted tokens
      for `smp-admin` get `tok_` ids (`auth.go:69`), so they are never touched;
    - its `CreatedAt` is not after the current row's `CreatedAt`;
    - if the cutoff is non-zero, its `CreatedAt` is before the cutoff.
- **Errors during revocation.** `ErrNotFound` from `RevokeToken` means another loader revoked it
  first (`mustAffect`, `adapters/store/pgutil.go:26-31`), so skip it. Any other error is returned
  wrapped as `revoke superseded bootstrap admin token <id>: …`.
- **Logging.** Each revocation is logged at Info and audited with
  `observe.AuditLog(ctx, "bootstrap_admin_token_revoked", "actor_id", "smp-admin", "token_id", …, "reason", "superseded")`.
- **The doc comment** at `:53-57` is rewritten to describe rotation revocation.
- **Why the cutoff.** In a rolling deploy, an old replica still carrying bearer A can load a brand
  new workspace after a new replica seeded it with bearer B. A's row is then the newest, so
  ordering by insertion alone would make the old replica revoke B.
  - The standalone server passes its **process start time** as the cutoff, so a replica never
    revokes a row created after it booted.
  - The new bearer's row is always created after every old replica booted, so an old replica can
    never revoke it.
  - A new replica revokes every row from before its own boot.
  - The one residue: a workspace first seeded by an old replica during the overlap keeps the old
    bearer valid until the next restart of a new-config replica. That is fail-safe toward
    availability, and the window is bounded.
- **Standalone wiring.**
  - `workspaceDeliveryConfig` (`serve_config.go:28-46`) gains `ProcessStart time.Time`.
  - `loadWorkspaceDeliveryConfig` (`:55-71`) sets it to `time.Now().UTC()`.
  - `bootstrapInternalAdmin` (`serve_resolver.go:235-252`) passes
    `SupersededBefore: cfg.ProcessStart` at `:239-241`.
  - The error policy at `:246-251` is unchanged. A revocation error is logged and swallowed like
    any non-collision error.

> **Review-round-1 note (2026-10-07): two corrections to this section.**
> - **The residue bound above is right only for one ordering.** It holds when the old bearer's row
>   is seeded first (old replica, then new replica). In the reverse order — a new-config replica
>   seeds the new bearer B first, then a replica still carrying A (an old-config replica, or
>   pre-upgrade code during the upgrade rollout) loads the same workspace and inserts A — A's row
>   is *newer* than B's. The "not after the current row" rule then spares A on every later load by
>   a B-carrying replica, restarts included, so **A stays valid until the next rotation**, not
>   until the next new-config restart. Time ordering cannot tell that row from a newer bearer
>   (owner question 5), so it is still never revoked; the sweep now logs it at Warn with its
>   token id once no rollout explains it (no cutoff, or created before the cutoff), and runbook
>   §5.6 (e) says how to find and revoke it. Reproduced by the review with an overlay probe and
>   pinned by `TestBootstrapSecurity_StaleBearerInsertedAfterCurrentIsFlagged`.
> - **"Logged and swallowed" no longer means "retried on the next load".** The pooled builder runs
>   only when a `ServiceSet` is rebuilt, so a busy workspace may never re-run the sweep before a
>   restart. `bootstrapInternalAdmin` now retries a failed bootstrap in the background until it
>   succeeds or the `ServiceSet` closes (see Implementation Notes, Review round 1).

> **Design-check note (2026-10-06): rotation is no longer zero-downtime.** Today, rotation adds
> the new row and keeps the old one, so the platform can switch bearers at any time after Spine
> restarts. After this task, the first load of a workspace by a new-config replica revokes the old
> row.
> - **Loads are lazy.** `workspaceMiddleware` builds the `ServiceSet`, and therefore runs the
>   bootstrap, before `authMiddleware` validates the bearer. The first request to a workspace after
>   the restart seeds the new bearer and revokes the old one, whichever bearer that request
>   carries.
> - **No safe ordering.** The platform cannot switch first: an old replica never seeded the new
>   bearer, so it would 401. It cannot switch later either: the old bearer is revoked on first
>   load.
> - **Result.** Every platform request that still carries the old bearer gets **401** from the
>   first load after the Spine restart until the platform switches. The window equals the skew
>   between the Spine rollout and the platform's switch, and it hits every workspace. This is the
>   intended security trade-off, but it is a behaviour change. §12 and the runbook must state it,
>   and Owner question 2 is reframed below.
>
> **Design-check note (2026-10-06): a residue the cutoff does not cover (verified).** An
> old-config replica that **restarts during the rollout** gets a fresh `ProcessStart`. A crash or
> node eviction restarts it from the old ReplicaSet's pod spec. Suppose that replica then first
> loads a workspace that has a new-bearer row and **no row for its own bearer**, meaning one first
> seeded during the overlap. It inserts the old bearer and **revokes the new one**: the new row is
> older than both the old bearer's fresh row and the restarted process's start.
> - Decision 8 means it never comes back. Every new replica then hits the revoked-current Error
>   branch for that workspace.
> - This was reproduced against the prototype (`_work/task013-check/designcheck_test.go`, X1).
> - It is inherent to ordering by time: the data cannot tell this case from a deliberate rollback.
> - It is bounded to workspaces created during the rollout, and the recovery is to rotate forward
>   to a fresh bearer.
>
> This residue is added to Decision 7 and the runbook, and it is Owner question 5.
>
> **Design-check note (2026-10-06): upgrade effect.** Today's code never revokes. So on the first
> load of each workspace by the new code, the sweep in branch 1 ("hash found, live") revokes
> **every** older bootstrap row, including bearers from past rotations that still authenticate
> today. That is the point of P3-31, but it is a one-time visible change at upgrade, and the
> CHANGELOG must say so. During the upgrade rollout itself, old-code replicas never revoke, so the
> rolling-deploy analysis above only applies to later rotations.
>
> **Design-check note (2026-10-06): unsetting `SMP_ADMIN_TOKEN` does not retire the bearer.** An
> empty token is still the no-op path (`bootstrap.go:59-61`), so the sweep never runs and existing
> bootstrap rows stay valid. The runbook must say how to retire the platform bearer entirely:
> - an admin revokes `smp-admin`'s live `bootstrap-…` row in each workspace with
>   `DELETE /api/v1/tokens/{token_id}`;
> - the bootstrap already respects that revocation (branch 2), and still will if the bearer is
>   later re-enabled.
>
> This keeps the documented no-op; no code change.

### 8. Bootstrap: random token id (P3-35, `bootstrap.go:105`, `:147-149`)

- **New format.** Replace `internalAdminTokenID(hash)` with `newInternalAdminTokenID()`. It returns
  `"bootstrap-" + generateRandomHex(8)`, which is 16 hex characters (`hash.go:21-27`), the same
  entropy as `tok_` ids.
- **Idempotency** never depended on the id. It is keyed on `token_hash`: the
  `GetActorByTokenHash` early return and the `UNIQUE` constraint in
  `migrations/002_auth_tables.sql:24`.
- **Format change.** New ids are 26 characters (`bootstrap-` plus 16 hex). Legacy ids are 22
  characters (`bootstrap-` plus 12 hex). Both keep the `bootstrap-` prefix that §7 matches on.
- **Existing rows keep their ids.** There is no migration. A legacy hash-derived id is neutralised
  by rotating the bearer: §7 revokes that row, and a revoked bearer is useless even if guessed.
- **Logging.** The insert log at `:143` now logs the random id, so the logs stop leaking the hash
  too.

> **Design-check note (2026-10-06): the legacy id of the *current* bearer stays until a rotation.**
> - **Exposure.** Without a rotation, the live bearer's row keeps its `bootstrap-<sha256[:12]>` id.
>   A tenant admin can still list it.
> - **Logs.** Branch 1's Debug line ("internal admin token already configured", `:111`) prints that
>   id on every workspace load.
> - **Production is covered.** The §9 gate refuses a weak bearer there, which forces a rotation.
>   A strong bearer makes the 48-bit prefix useless as an oracle.
> - **Elsewhere,** a weak bearer only warns.
>
> The runbook and CHANGELOG therefore recommend **rotating `SMP_ADMIN_TOKEN` once after
> upgrading**. That retires the hash-derived id, because §7 revokes the legacy row.

### 9. `SMP_ADMIN_TOKEN` strength gate (standalone only)

- **New function** in `service/cmd/spine/serve_config.go`, next to `validateOperatorToken`
  (`:196-204`):
  - `validateSMPAdminToken(token, env string) (warning string, err error)`;
  - empty means fine, because the feature is off;
  - it is weak if `len < operatorTokenMinLength` (32), or if it has fewer than 8 distinct bytes.
    The second rule catches `aaaa…` and `0000…`.
- **Behaviour by environment.**
  - `env == "production"`: error, so `spine serve` refuses to start.
  - Any other environment: a warning, which `serveCmd` logs.
- **Messages** name the problem and the remedy (`openssl rand -hex 32`). They **never** echo the
  token.
- **Call site.** `serveCmd` (`serve_cmd.go:41`) calls it right after `runtimeEnv` is resolved,
  before any store, secret or listener is opened.
- **Why it is not in `core/auth`.** SMP calls `auth.BootstrapInternalAdmin` with its own bearer,
  which its own config policy governs. Unit tests in both repos use short bearers (`"x"`,
  `"bearer"`, `"smp-bearer-secret"`, `"smp-admin-bootstrap-token-integration-test"`).
- **SMP's dev default** `smp-admin-bootstrap-token-ca4fdbd0db8a338350477d733d4946c6` (SMP
  `docker-compose.yaml:308`) would pass the gate anyway, at 58 characters with 24 distinct bytes.

### 10. Audit lines added

| `audit_operation` | Where | Fields |
|---|---|---|
| `actor_skill_added` / `actor_skill_removed` | actor-skill routes | `target_actor_id`, `skill_id`, `skill_name` (add only) |
| `actor_skill_change_refused` | self-change refusal | `operation`, `target_actor_id`, `reason` |
| `skill_renamed` | `PATCH /skills/{id}` with a name change | `skill_id`, `from`, `to` |
| `skill_deprecated` | deprecate | `skill_id`, `skill_name`, `forced`, `referencing_workflows` |
| `actor_created` / `actor_create_refused` | `POST /actors` | `created_actor_id` / `granted_role`, `reason` |
| `bootstrap_admin_token_revoked` | bootstrap rotation | `actor_id`, `token_id`, `reason` |

Every line also carries the logger's `actor_id` and `trace_id`. No line contains a bearer.

### 11. Data, format and migrations

- **No runtime schema change and no migration.** The task uses `auth.tokens.revoked_at` and the
  existing `AuthStore` methods. `store.AuthStore` (`adapters/store/interfaces.go:126-137`) is
  unchanged.
- **API wire changes:**
  - 403 where contributors, reviewers and operators used to succeed (§1, §5);
  - 409 on a forced-less rename of a referenced skill (§4);
  - new `reason` values in 403 details;
  - the new `bootstrap-<16 hex>` id format.
- **One public Go API change:** `BootstrapAdminConfig.SupersededBefore`. It is additive, and the
  zero value is valid.

### 12. Backward compatibility (standalone)

| Behaviour | Before | After |
|---|---|---|
| Contributor, reviewer or operator: `POST`/`PATCH /skills`, `POST /skills/{id}/deprecate` | 2xx | 403 |
| Non-admin `POST`/`DELETE /actors/{other}/skills/{id}` | 2xx (contributor and up) | 403 (insufficient role) |
| Non-admin on its **own** `/actors/{self}/skills/{id}` | 2xx | 403 `self_skill_change_forbidden` |
| Admin on any actor, itself included | 2xx | 2xx, audited |
| Operator `POST /actors` with role `operator` or `admin` | 201 | 403 `role_ceiling_exceeded` |
| Admin rename of a skill referenced by an active workflow | 200 | 409 unless `?force=true` |
| Dev mode (`SPINE_DEV_MODE`, no actor) | allow all | allow all (unchanged) |
| `SMP_ADMIN_TOKEN` rotation | old bearer stays valid | old bootstrap row revoked on first load after rotation |
| Rollback to a revoked bearer | n/a | refused (401); logged at Error |
| Suspended or deactivated `smp-admin` | re-activated on load | stays as is; logged at Warn |
| `SMP_ADMIN_TOKEN` shorter than 32 characters, or under 8 distinct bytes | accepted | production: refuses to start; elsewhere: warns |

> **Design-check note (2026-10-06): additional rows** (from the §3, §7 and §8 notes):
>
> | Behaviour | Before | After |
> |---|---|---|
> | Platform requests carrying the old bearer during a rotation | 2xx until the platform switches (zero-downtime) | 401 from the first workspace load after the Spine restart until the platform switches |
> | Bootstrap bearers from past rotations that still authenticate | valid | revoked on the first load by the upgraded code |
> | Old-config replica restarted mid-rollout loads a workspace created during the rollout | n/a | can revoke the new bearer in that workspace (residue; recover by rotating forward) |
> | Non-admin self-change with a percent-encoded `{actor_id}` | 2xx | 403 `self_skill_change_forbidden` (decoded once) |
> | Malformed escape in `{actor_id}` on the actor-skill routes | handler-dependent | 400 `invalid_params` |
> | `SMP_ADMIN_TOKEN` unset after having been set | rows stay valid | unchanged; retire with `DELETE /tokens/{id}` |

Nothing in this repository calls the skill or actor routes with a non-admin token outside the
gateway tests. The README's endpoint table (`README.md:286-287`) lists the routes without roles.
Integrators that let agents self-register skills must move that to an admin. That goes in the
changelog.

**Existing tests that change in lockstep**, and only these:

- **`service/internal/gateway/handlers_skills_test.go`.**
  - Add `setupSkillServerAdmin(t)`. It is `setupSkillServer` plus an `admin-1` actor, and it
    returns that actor's token.
  - `setupSkillServer` keeps returning the contributor token, because the regression tests use it
    as the attacker.
  - These tests switch to `setupSkillServerAdmin`:
    - `TestSkillCreate`, `TestSkillCreate_GeneratesSkillID`, `TestSkillCreate_MissingName`;
    - `TestSkillUpdate`, `TestSkillUpdate_NotFound`;
    - `TestSkillDeprecate`, `TestSkillDeprecate_NotFound`, `TestSkillDeprecate_BlockedByWorkflow`,
      `TestSkillDeprecate_ForceOverride`, `TestSkillDeprecate_UnrelatedSkillSucceeds`;
    - `TestActorSkillAssign`, `TestActorSkillAssign_Idempotent`, `TestActorSkillAssign_SkillNotFound`;
    - `TestActorSkillRemove`, `TestActorSkillRemove_NotFound`.
- **`service/internal/gateway/handlers_coverage2_test.go`.** `TestSkillCreate_InvalidJSON`,
  `TestSkillUpdate_InvalidJSON` and `TestSkillUpdate_WithCategory` switch to
  `setupSkillServerAdmin`. The reader-forbidden and unauthenticated tests stay unchanged and keep
  passing.
- **`core/auth/auth_test.go`.** `fakeStore` gains `ListTokensByActor`. Without it the embedded nil
  `store.Store` panics.
- **`core/auth/bootstrap_test.go`.**
  - `TestBootstrapInternalAdmin_CreatesActorAndToken`: the id assertion becomes
    `^bootstrap-[0-9a-f]{16}$` and "not a substring of the hash" (was `:127-130`).
  - `TestBootstrapInternalAdmin_HealsStaleActor`: name and role are healed, but the status stays
    `suspended` (was `:147-150`).
  - `TestBootstrapInternalAdmin_RotatedTokenInsertsNewRow`: still two rows, with the old one
    revoked and the new one live (was `:185-187`).
- **`service/scenariotest/scenarios/bootstrap_admin_idempotency_test.go`.** This is the scenario
  suite (build tag `scenario`). Its doc at `:81-91` already says "the OLD-bearer expectation flips
  and this scenario must update in lockstep".
  - `:123` and `:502`: the old bearer after rotation expects **401**. That needs a new
    `drive…ExpectingUnauthorized` step.
  - `:234` and `:701`: the id assertion becomes the regex above.
  - `assertRotationRowCounts` (`:360-…`) and `assertRotationRowCountsAfterPooledBootstrap`
    (`:815-…`): there are still two rows, and the old-hash row has `RevokedAt != nil`.

> **Design-check note (2026-10-06): one more existing test file changes.**
> `service/cmd/spine/serve_delivery_test.go` must change for AC 12's wiring test, and only in the
> `authCaptureStore` helper (`:281-385`):
> - it gains `ListTokensByActor` and `RevokeToken`. Today they are promoted from `stubStore` as a
>   no-op and an empty list (`stubstore_test.go:246-249`), so nothing would be revoked or
>   observable;
> - its `GetActorByTokenHash` carries `RevokedAt` (`:341` drops it).
>
> No existing `Test…` function in that file changes. The prototype did **not** include this test:
> neither `overlay_full.json` nor `prototypes/TASK-013` touches `serve_delivery_test.go`. The design
> check prototyped it (`_work/task013-check/wiring_test.go` with `overlay_wiring.json`). It passes
> on the full change, and the mutation "drop `SupersededBefore: cfg.ProcessStart` from
> `serve_resolver.go`" turns it red (`overlay_wiring_mut.json`).

### 13. SMP impact, and what SMP must change

**SMP needs no code change.** There is no SMP adaptation task. The `.spine-revision` bump in SMP
TASK-012 carries this change, and that task's gates must include AC 18.

- **Skill and actor changes (§1-§5).** These live in `core/auth`'s operation map and in
  `service/`. SMP does not use `auth.Authorize` or `RequiredRole`. It has its own map (SMP
  `internal/customer/workspace/permissions.go:136`), and `dev/lint-spine-internals` forbids it
  from importing `service/`.
- **Bootstrap (§6-§8).** SMP calls
  `spineauth.BootstrapInternalAdmin(ctx, pgs, spineauth.BootstrapAdminConfig{Token: p.spineBootstrapBearer})`
  from the `create_initial_actors` provisioning step and its retries (SMP
  `provision_steps_runtime.go:387`, `:423-428`). The bearer comes from `SPINE_AUTH_TOKEN` (SMP
  `cmd/customer-app/routes_platform.go:307`, `dev_cli.go:283`).
  - The keyed literal still compiles. The zero `SupersededBefore` means "revoke every older
    bootstrap row" in that workspace's runtime DB.
  - Those rows are not consulted by any live SMP verifier (SMP `internal/recovery/release.go:18-24`),
    so revoking them, the random id and leaving status alone change nothing observable.
  - SMP never suspends `smp-admin`. Its only `Deactivate` caller targets runner actors (SMP
    `spinecore/actorstore.go:125-133`), and execution selection excludes `smp-admin` (SMP
    `spinecore/execution_actor_store.go:94`, `:110`).
  - SMP's collision path (`TestCreateInitialActors_Integration_TokenHashCollisionFailsClosed`) is
    untouched, because §7 step 3 runs before any revocation.
- **Entropy gate (§9).** It lives in `service/`, so it never runs in SMP.
- **Optional, not required.** If SMP ever adds a verifier for `auth.tokens`, it should pass its
  process start as `SupersededBefore`. That is noted for SMP's own backlog, not filed here.

### 14. Coordination with sibling tasks

- **TASK-007 (step-entry policy).** It owns separation of duties. Its SoD rule is keyed on step
  type, not on skills, precisely because skills were self-grantable. This task makes
  `required_skills` (and TASK-007's `RequireSkillsOnAssign`) a real boundary in standalone. The two
  are independent and can merge in either order.
- **TASK-015 (shared-mode tenant scoping, git-endpoint auth).** It touches
  `service/internal/gateway` (`handlers_system.go`, `handlers_git.go`, `handlers_events_stream.go`)
  but not the files changed here. No ordering is needed.
- **TASK-009 and TASK-010 (secrets and webhooks).** No overlap. `serve_config.go` is touched only
  next to `validateOperatorToken`, so a textual conflict, if any, is trivial.

> **Design-check note (2026-10-06): more textual overlaps.** None of these is semantic, and no
> ordering is needed. Whichever task lands second rebases.
> - TASK-006 adds `"run.governance.decide"` to `core/auth/permissions.go`.
> - TASK-002 (git version floor), TASK-007 (engine-policy env), TASK-009 (cipher mode),
>   TASK-012 (`serve_cmd.go:125`) and TASK-015 (git dev flag, next to `guardDevModeEnv`) all edit
>   `service/cmd/spine/serve_cmd.go`, `serve_config.go` or `serve_resolver.go`.
>
> This task's `validateSMPAdminToken` call keys on `runtimeEnv == "production"`. It inherits any
> change TASK-015 makes to environment resolution. TASK-015 leaves `resolveRuntimeEnv` alone.

### 15. Documentation

- **`architecture/security-model.md` §4.1** (`:127-133`). The `operator` row gains "register
  actors below its own role". The `admin` row gains "skill catalogue, actor skills, admin and
  operator actors". Add a sentence after the table: "A non-admin actor can never change its own
  skills."
- **`architecture/api-operations.md`.** §3.6 (`:195-203`) states that `actor.add_skill` and
  `actor.remove_skill` are admin-only, as are skill create, update and deprecate. The §6 table
  (`:358-364`) matches §4.1.
- **`architecture/actor-model.md`.**
  - §3.2 (`:98`) keeps "Role and skills may be updated by an admin" and adds the ceiling for
    `actor.create`.
  - §7.1 notes that the platform bootstrap never re-activates `smp-admin`.
- **`docs/integration-guide.md`.**
  - §4 "Actor Model" (`:265-266`) corrects `operator` ("manage tokens" is wrong: token operations
    are admin, `permissions.go:95-97`) and `admin`.
  - The env table (`:600-…`) gains an `SMP_ADMIN_TOKEN` row covering the purpose, the 32-character
    and 8-distinct floor, refusal in production, and that rotation revokes the previous bootstrap
    bearer on the first workspace load after a restart.
- **`docs/operator-runbook.md` §5.** Add "Rotating `SMP_ADMIN_TOKEN`":
  1. Set the new value.
  2. Restart all replicas. The old bearer is revoked per workspace on first load.
  3. Expect the old bearer to be refused, including after a rollback.
  4. To suspend `smp-admin`, suspend it. The bootstrap no longer undoes that.
- **`README.md`.** The production startup gates table (`:47-54`) gains `SMP_ADMIN_TOKEN`.
- **`CHANGELOG.md`.** One entry under a new INIT-023 heading covering every row of the §12 table,
  with an upgrade note for integrators whose non-admin actors manage skills.

> **Design-check note (2026-10-06): extra runbook and changelog content.**
>
> `docs/operator-runbook.md` §5, "Rotating `SMP_ADMIN_TOKEN`", also says:
> - **(a) Switch together.** Switch the platform's bearer in the same change window as the Spine
>   restart. Requests still carrying the old bearer get 401 from each workspace's first load after
>   the restart.
> - **(b) Avoid old-config restarts.** During the rollout, do not let old-config replicas restart.
>   Prefer a recreate strategy, or scale the old ReplicaSet to zero first. If a workspace created
>   during the rollout refuses the new bearer (an Error log naming a revoked bootstrap token),
>   rotate forward to a fresh value. Revocation is never undone.
> - **(c) Rotate once after upgrading.** This retires the legacy hash-derived token id (§8 note).
> - **(d) To retire the bearer entirely,** revoke `smp-admin`'s live `bootstrap-…` row with
>   `DELETE /api/v1/tokens/{token_id}`. Unsetting `SMP_ADMIN_TOKEN` does not revoke it.
>
> The CHANGELOG also states the one-time upgrade revocation of bearers from past rotations.
>
> **Pre-existing actor-skill grants are not audited.** Grants made by non-admins before the upgrade
> stay in place, and `auth.actor_skills` records no grantor. The upgrade note tells operators to
> review `auth.actor_skills` for `review`, `workflow_review` and `architecture` holders, for
> example with `GET /api/v1/actors/{id}/skills` per actor, or with SQL, and to remove unexpected
> grants as admin.

## Decisions

1. **All skill mutations are admin-only, not "anyone may manage others but not themselves".**
   - Skills are authorization: they decide who may approve.
   - `actor-model.md:98` and `security-model.md:133` already say admin.
   - A rule that lets operators grant skills to others invites collusion between two non-admins,
     and it would still need SoD to be safe.
2. **New operation names for grants.** `actor.add_skill` and `actor.remove_skill` replace reuse of
   `skill.update`. Catalogue edits and grants are different privileges. The names come from
   `api-operations.md` §3.6, so code and spec agree.
3. **The self-change check runs before the role check, with a distinct reason, and admins are
   exempt.**
   - It is redundant with the role map today. It exists so that a future loosening of the map
     (for example, operators managing runner skills) cannot reopen self-grant, and so the attempt
     is visible in responses and audit.
   - Admins sit at the top of the hierarchy. Stopping an admin from approving its own work is
     TASK-007's step-level SoD, not a skill rule.
4. **The ceiling is "strictly below own role" for non-admins, and is enforced through a `core/auth`
   helper called from the gateway, not inside `actor.Service.Register`.**
   - `Register` (`core/actor/service.go:27-36`) has no caller identity, and SMP uses
     `actor.Service`. Changing its signature would ripple into SMP for no SMP benefit.
   - "Strictly below" means an operator cannot mint a peer operator. Equal-role minting multiplies
     operator credentials without an admin in the loop.
5. **`actor.create` stays operator.** Raising it to admin is Owner question 1. The ceiling closes
   the escalation either way.
6. **The bootstrap still heals name and role, but never status.**
   - The audit finding is about status: suspension and deactivation are the documented lifecycle
     controls (`actor-model.md` §7.1).
   - Standalone has no API that changes an actor's role, so role drift only comes from direct
     database edits, which the existing heal contract
     (`TestBootstrapInternalAdmin_HealsStaleActor`, INIT-020/EPIC-002/TASK-001) repairs.
   - An empty or unknown status is still healed to `active`. Only `suspended` and `deactivated`
     are operator intent.
7. **Rotation revocation is automatic, ordered by `created_at` and bounded by process start.**
   Alternatives considered:
   - **(a) An explicit `SMP_ADMIN_TOKEN_RETIRED` list.** Correct, but rotation hygiene would stay
     opt-in. Operators who forget it keep the finding open.
   - **(b) Operator-supplied generation numbers.** New config, and a token-name format to parse.
   - **(c) Ordering by `created_at` alone.** In a rolling deploy, an old replica that inserts its
     row last would revoke the new bearer.

   The process-start cutoff fixes (c) with one timestamp, at the cost of the bounded residue
   described in Design §7.

   > **Design-check note (2026-10-06): a second residue.** The cutoff protects against old
   > replicas that booted before the new bearer's row existed. It does not protect against an
   > old-config replica that **restarts** mid-rollout and loads a workspace first seeded during
   > the overlap: there it revokes the new bearer, permanently (Design §7 note, X1). Only (b),
   > generation numbers, removes both residues, because it orders by configuration and not by
   > time. That is Owner question 5. The default is to accept the residue and document the
   > recovery (rotate forward).
8. **Never un-revoke. A revoked current bearer is an Error log, not a returned error.**
   - Un-revoking would let a leaked bearer come back through a config rollback.
   - Returning an error would, in standalone production, fail workspace load for every user of the
     workspace, not just the platform. In SMP it would wedge provisioning.
   - The bearer is refused either way, and the log says why and what to do.
9. **A random token id, not an HMAC id.**
   - An HMAC needs a key that `core/auth` does not have. SMP and standalone would each have to
     supply one.
   - Determinism buys nothing, because idempotency is keyed on `token_hash`.
   - Existing rows keep their ids. Renaming a primary key needs a migration and breaks audit
     references, and rotation (§7) neutralises them.
10. **The strength gate lives in `service/` only, refuses in production and warns elsewhere.**
    - This mirrors `SPINE_SECRET_ENCRYPTION_KEY`'s production gate (`serve_config.go:235-248`).
    - It keeps `core/auth` policy-free for embedders.
    - "32 characters and 8 distinct bytes" is a floor against human-chosen values, not an entropy
      estimator. A Shannon estimate would add false positives on hex tokens without stopping a
      determined weak choice.
11. **Audit through `observe.AuditLog`, not new domain events.** No event type exists for identity
    changes. Adding them touches `event-schemas.md` and every subscriber, which is a follow-up.
12. **Rename of a referenced skill needs `?force=true`.** This is beyond the plan's wording, but it
    is the same eligibility change as deprecation, and F6 names it. It costs one more call to
    `FindWorkflowsReferencingSkill`.
13. **Rejected: matching `required_skills` by immutable `skill_id`** (the audit's one-line fix).
    - Workflow YAML names skills (`workflow-definition-format.md`), and SMP's workflows do too.
    - Switching is a governed-format change across both repositories.
    - With admin-only management and the rename guard, the attacker has no lever left. Changing the
      key is listed as a follow-up.

## Owner questions

1. **`actor.create` role.** Should operators keep `actor.create`, capped below their own role, or
   should it become admin-only as `security-model.md:133` literally reads?
   - **Default:** keep operator, with the strict-below ceiling. Operators can still provision
     runner and agent actors without holding admin.
2. **Rotation grace.** Should the superseded bootstrap bearer be revoked immediately on the first
   load after rotation, or after a grace window?
   - **Default:** immediately. A grace window means setting `expires_at`. That needs a new
     `store.AuthStore` method, which every `AuthStore` implementation, test fakes included, would
     have to add. It also leaves a leaked bearer valid for the window.
   - **Design-check note (2026-10-06): the reframed trade-off.**
     - **The cost of "immediately" is availability, not just hygiene.** Every rotation becomes a
       401 window for platform requests that still carry the old bearer (Design §7 note). Today's
       rotation is zero-downtime.
     - **A cheaper grace mechanism than `expires_at`** is an optional `SMP_ADMIN_TOKEN_PREVIOUS`.
       The sweep skips the row whose hash equals `HashToken(previous)`, and the next deploy
       without it revokes that row. It needs no `AuthStore` change: only a config field
       (`BootstrapAdminConfig.KeepToken` or similar) and one comparison.
     - **Options:**
       - (i) immediately, accepting the window (the designer's default);
       - (ii) the opt-in `SMP_ADMIN_TOKEN_PREVIOUS` two-phase rotation;
       - (iii) `expires_at`.
     - **Recommendation:** (i) for this task, with the runbook's "switch together" step, and file
       (ii) as a follow-up if a standalone platform-binding operator needs zero-downtime
       rotation.
3. **Strength gate outside production.** Should `SPINE_ENV=staging` refuse a weak `SMP_ADMIN_TOKEN`
   too?
   - **Default:** no. Warn, consistent with every other startup gate, which treats only
     `production` as strict.
4. **Rename guard.** Should an admin rename of a skill that an active workflow references require
   `?force=true` (409 otherwise)?
   - **Default:** yes, and it is audited. The alternative is audit only.
5. **Design-check addition (2026-10-06): the restart-during-rollout residue.** An old-config
   replica that restarts mid-rollout can permanently revoke the new bearer in workspaces first
   seeded during the overlap (Design §7 note, reproduced as X1). Accept this, or replace the time
   ordering with a bearer generation (`SMP_ADMIN_TOKEN_GENERATION=n`, recorded in the bootstrap
   row's `name`, and only lower generations revoked)?
   - **Default:** accept. Document the recovery, which is to rotate forward. Generation numbers
     are Decision 7(b), which the designer rejected for adding config and a name format to parse.
     They remain the only fix for both rollout residues.

## Acceptance Criteria

**Red before green.**

- The first commit on the task branch adds inert plumbing, plus every test below:
  - the `ReasonSelfSkillChange` and `ReasonRoleCeiling` constants;
  - `AuthorizeActorSkillChange`, returning `Authorize(caller, op)`;
  - `AuthorizeRoleGrant`, returning nil;
  - `BootstrapAdminConfig.SupersededBefore`, stored but unread;
  - `validateSMPAdminToken`, returning `("", nil)`;
  - `setupSkillServerAdmin`.
- The implementer records in Implementation Notes which tests fail on that commit and confirms
  they pass on the fix:
  - every new test in AC 1-14 is expected to fail, including AC 12's wiring test, because the
    stub revokes nothing;
  - the exceptions are `TestBootstrapSecurity_OldReplicaCannotRevokeNewerBearer` (AC 12) and
    AC 13. They are over-revocation guards and pass on the stub.
  - *Design-check note (2026-10-06):* AC 21's new test is also expected to fail on the stub.
    AC 10's new "empty status is healed" row is one more exception: it is an
    over-preservation guard and passes on the stub, which keeps today's heal-everything
    behaviour.
- Each AC names a mutation that must turn it red again.

**Regression tests derived from the audit PoCs.**

1. **`TestSkillAuthority_ContributorCannotEscalate`** in
   `service/internal/gateway/identity_authority_test.go`. It merges `poc/skeptic-skills`,
   `poc/standalone-service/poc_skill_selfgrant_test.go` and
   `poc/engine-authz/poc_skill_selfgrant_test.go`, with the assertions inverted. With the
   contributor token from `setupSkillServer`:
   - `POST /actors/contributor-1/skills/sk-review` answers 403 with detail
     `reason=self_skill_change_forbidden`;
   - `POST /skills {workflow_review}` answers 403;
   - `POST /actors/reviewer-1/skills/sk-review` (a grant to a colluding actor) answers 403;
   - `DELETE /actors/reviewer-1/skills/sk-review` answers 403;
   - `POST /skills/sk-review/deprecate?force=true` answers 403;
   - `PATCH /skills/sk-review {name}` answers 403.

   Afterwards `ValidateSkillEligibility` says the contributor is not eligible for `review` and the
   reviewer still is. The skill catalogue is unchanged.

   *Mutation:* any one of the following fails it:
   - reverting any of `skill.create`, `actor.add_skill` or `actor.remove_skill` to `RoleContributor`;
   - removing the self check in `AuthorizeActorSkillChange`.
2. **`TestSkillAuthority_SelfGrantedReviewChainBlocked`** in
   `service/internal/gateway/skill_selfreview_e2e_test.go`. It is `poc/verify-r1-4`, inverted.
   It uses the real router, the real `engine.Orchestrator` and the real
   `workflows/task-default.yaml`.
   - Step [2], the self-grant, answers 403. Step [3], the strip, answers 403, and `alice` stays
     eligible.
   - Step [4], `mallory`'s claim of her own review step, is refused.
   - The recording git operator has recorded **zero** merges.

   *Mutation:* reverting `actor.remove_skill` to contributor fails it. So does reverting
   `actor.add_skill` together with removing the self check.
3. **`TestSkillAuthority_NonAdminSelfChangeHasDistinctReason`.**
   - An operator's `POST /actors/op-1/skills/sk-1` (itself) answers 403 with
     `reason=self_skill_change_forbidden`, and the captured log contains
     `audit_operation=actor_skill_change_refused`.
   - The same operator granting to `contributor-1` answers 403 with no `reason`, which is a plain
     insufficient role.
   - *Design-check addition (2026-10-06):*
     - the operator's `POST /actors/op%2D1/skills/sk-1` (itself, percent-encoded) also answers 403
       with `reason=self_skill_change_forbidden`;
     - `POST /actors/op%ZZ/skills/sk-1` answers 400 `invalid_params`.

   *Mutation:* removing the self check fails it, even though the role map alone still returns 403.
   Dropping the `url.PathUnescape` of `{actor_id}` fails the encoded row.
4. **`TestSkillAuthority_AdminManagesSkills`.** An admin grants `review` to `contributor-1` and to
   itself, and both answer 200. The log contains `audit_operation=actor_skill_added`.

   *Mutation:* applying the self check to admins, or removing the audit line, fails it.
5. **`TestSkillAuthority_ReferencedSkillDeprecateAndRenameNeedForceAndAreAudited.`** With an
   active workflow projection that requires `review`, an admin's requests without force all answer
   409:
   - renaming `review` to anything;
   - renaming `x` to `review`;
   - deprecating `review`.

   `?force=true` deprecate answers 200, and the log contains `audit_operation=skill_deprecated`
   with `forced=true`.

   *Design-check note (2026-10-06):* for the "rename `x` to `review`" case, the fixture's catalogue
   has **no** `review` skill. Only the workflow references it. That is the state in which the
   lever works against Postgres's `UNIQUE(name)`.

   *Mutation:* removing the rename guard, or the deprecate audit line, fails it.
6. **`TestActorCreate_RoleCeiling`.** It is `poc/crypto-secrets-auth/operator_mints_admin_test.go`,
   inverted.

   | Caller | Requested role | Expected |
   |---|---|---|
   | operator | `admin` | 403, `role_ceiling_exceeded`, no actor stored |
   | operator | `operator` | 403, `role_ceiling_exceeded`, no actor stored |
   | operator | `reviewer` | 201 |
   | admin | `admin` | 201 |

   *Mutation:* `>=` changed to `>` in `AuthorizeRoleGrant` fails the operator-to-operator row.
   Removing the call in `handleActorCreate` fails the operator-to-admin row.
7. **`core/auth/grants_test.go`** covers both helpers exhaustively:
   - `AuthorizeActorSkillChange` for all 5 roles, for self and for other, plus a suspended caller,
     which must get the status-class detail and not the self reason;
   - `AuthorizeRoleGrant` for the full 5×5 matrix of caller role against granted role.
   - *Design-check addition (2026-10-06):* `AuthorizeRoleGrant` also gets these rows:
     - granted `""`, `"superuser"` and `"Admin"` are refused for every caller, admin included,
       with `reason=role_ceiling_exceeded`;
     - a suspended admin and a deactivated operator get the status-class detail
       (`actor_status`), not a nil error.

   *Mutation:* any off-by-one in either comparison fails a row. So does removing the
   `RoleLevel() == 0` refusal, or removing the status check in `AuthorizeRoleGrant`.
8. **`TestBootstrapSecurity_RotationRevokesOldBearer`** in
   `core/auth/bootstrap_security_test.go`. It is the first half of
   `poc/crypto-secrets-auth/bootstrap_rotation_test.go`, inverted.
   - After `old` then `new`, `ValidateToken(old)` fails with "token revoked".
   - `ValidateToken(new)` succeeds, and `Authorize(_, "token.create")` passes for it.

   *Mutation:* removing the `revokeSupersededAdminTokens` call fails it.
9. **`TestBootstrapSecurity_TokenIDIndependentOfBearer`.** It is the second half of the same PoC.
   - `BootstrapInternalAdmin` still accepts the bearer `"admin"`, which pins that `core/auth` does
     not judge strength.
   - Its row's id is not a prefix of `sha256("admin")`.
   - The updated `TestBootstrapInternalAdmin_CreatesActorAndToken` asserts
     `^bootstrap-[0-9a-f]{16}$`.

   *Mutation:* restoring the `"bootstrap-"+hash[:12]` id fails both tests.
10. **`TestBootstrapSecurity_DoesNotResurrectDeactivatedAdmin`.** It is
    `poc/gap1-crypto-secrets-reaud/auth_bootstrap_poc_test.go`, inverted, with its in-memory store.
    - After `actor.Service.Deactivate` and a second bootstrap, the actor is still `deactivated`
      and the bearer is refused.
    - **`TestBootstrapSecurity_KeepsSuspendedAdminSuspended`** does the same for `suspended`.
    - The updated `TestBootstrapInternalAdmin_HealsStaleActor` asserts that name and role are
      healed and the status stays `suspended`.
    - *Design-check addition (2026-10-06):*
      **`TestBootstrapSecurity_HealsEmptyStatusToActive`** seeds `smp-admin` with the canonical
      name and role and an **empty** status. After a bootstrap, the status is `active` and the
      bearer validates.

    *Mutation:* deleting `desired.Status = existing.Status` fails the first three. Widening the
    preserve rule to "any non-active status" fails the empty-status test.
11. **`TestBootstrapSecurity_RevokedCurrentBearerStaysRevoked`.** Bearers A, then B, then A
    (a rollback):
    - A is refused;
    - B still validates;
    - the captured log has a `level=ERROR` line containing "revoked bootstrap admin token".

    *Mutation:* deleting the revoked-current branch fails the log assertion. Removing the
    revocation sweep fails the "A is refused" assertion.
12. **Rollout cutoff.**
    - **`TestBootstrapSecurity_OldReplicaCannotRevokeNewerBearer`.** B is seeded with
      `SupersededBefore = now`, then an old replica with `SupersededBefore = now - 1h` seeds A. B
      must still validate.
      *Mutation:* ignoring the cutoff fails it.
    - **A wiring test in `service/cmd/spine/serve_delivery_test.go`.** It drives
      `bootstrapInternalAdmin` through `authCaptureStore`, which gains `ListTokensByActor` and
      `RevokeToken`. It sets `cfg.ProcessStart` between two pre-seeded legacy bootstrap rows and
      asserts:
      - only the row created before `ProcessStart` is revoked;
      - `loadWorkspaceDeliveryConfig().ProcessStart` is non-zero.

      *Mutation:* dropping `SupersededBefore: cfg.ProcessStart` from `serve_resolver.go` fails it.

      *Design-check note (2026-10-06):* this test and its mutation were missing from the
      designer's prototype. The design check prototyped both and confirmed them
      (`_work/task013-check/wiring_test.go`, `overlay_wiring.json` and `overlay_wiring_mut.json`).
      `authCaptureStore`'s `GetActorByTokenHash` must also return `RevokedAt`, so the test can
      assert through `ValidateToken` as well as through the stored record.
13. **`TestBootstrapSecurity_AdminMintedTokensSurviveRotation`.** A `tok_` token minted for
    `smp-admin` through `auth.Service.CreateToken`, even one named `smp-admin-bootstrap`, still
    validates after a rotation.

    *Mutation:* dropping the `bootstrap-` prefix condition fails it.
14. **Strength gate.**
    - **`TestValidateSMPAdminToken`** checks this table:

      | Token | Environment | Expected |
      |---|---|---|
      | empty | production | ok |
      | 64-hex | production | ok |
      | `admin`, a 31-character prefix, or `"ab"×20` | production | error |
      | `admin` | none | warning |
      | `"a"×64` | staging | warning |
      | SMP's 58-character dev default | production | ok |

      No error or warning contains the token.
    - **`TestServeRefusesWeakSMPAdminTokenInProduction`** calls `serveCmd().RunE` with
      `SPINE_ENV=production` and `SMP_ADMIN_TOKEN=admin`. It returns an error mentioning
      `SMP_ADMIN_TOKEN` before any store is opened.

    *Mutation:* disabling the distinct-byte rule fails the first test. Deleting the call in
    `serve_cmd.go` fails the second, because the run then stops at the
    `SPINE_SECRET_ENCRYPTION_KEY` gate instead.

**Suites, gates and compatibility.**

15. **Only the listed tests change.** The existing tests listed in Design §12 are updated as
    described, and no other existing test is modified. `go build ./...`, `go vet ./...` and
    `go test ./...` pass in Spine.
    *Design-check note (2026-10-06):* the list includes `service/cmd/spine/serve_delivery_test.go`,
    and only its `authCaptureStore` helper (§12 note). No existing `Test…` function there
    changes.
16. **Scenario suite.** `service/scenariotest/scenarios/bootstrap_admin_idempotency_test.go` is
    updated as in Design §12:
    - the old bearer expects 401 at `:123` and `:502`;
    - the id regex at `:234` and `:701`;
    - the old row is revoked in both row-count steps.

    `make test-scenario` passes with `SPINE_TEST_DATABASE_URL` set. This is the Postgres-backed
    proof that `ListTokensByActor` plus `RevokeToken` revoke the right row. The implementer
    records the command and its result in Implementation Notes.
17. **Repository gates.** `dev/lint-boundaries.sh` passes with no new entry in
    `core_to_adapters_allowlist`, as do `golangci-lint run` and `cmd/embed-smoketest`.
18. **SMP compatibility gate**, with `../spine` checked out at the task branch head and **no SMP
    file changed**:
    - `go build ./cmd/... ./internal/... ./scenariotest/... ./dev/...`;
    - `go test ./internal/customer/workspace/... ./internal/recovery/... ./internal/customer/spinecore/... ./cmd/customer-app/...`;
    - `dev/lint-spine-internals.sh` and its self-test;
    - with `TEST_PROVISIONING_DSN` set, all four `TestCreateInitialActors_Integration_*` tests,
      including `…TokenHashCollisionFailsClosed`.

    All pass. The implementer records the commands and results in Implementation Notes. The same
    gate runs again under SMP TASK-012's `.spine-revision` bump.
19. **No schema change.** `git diff <base>..HEAD -- migrations/` is empty, and `store.AuthStore`
    is unchanged.
20. **Docs and changelog.** Every document in Design §15 is updated, and `CHANGELOG.md` covers
    every row of the §12 table. A reviewer can check each listed section.
    *Design-check note (2026-10-06):* this includes the §12 note's extra rows and the §15 note's
    runbook steps (a)-(d). It also includes the CHANGELOG's two upgrade notes: past-rotation
    bearers are revoked on first load, and pre-existing actor-skill grants should be reviewed.
21. **Design-check addition (2026-10-06): revocation error paths.**
    **`TestBootstrapSecurity_RevocationErrorPaths`** in `core/auth/bootstrap_security_test.go`
    uses an `AuthStore` wrapper that injects errors:
    - a `ListTokensByActor` failure returns an error wrapping the cause and containing
      "list bootstrap admin tokens";
    - a `RevokeToken` failure other than not-found returns an error containing
      "revoke superseded bootstrap admin token" and the token id;
    - a `RevokeToken` that returns `domain.ErrNotFound` for the first superseded row (revoked
      concurrently) is skipped. The bootstrap returns nil and still revokes a second superseded
      row.

    *Mutation:* swallowing the list error, swallowing a non-not-found revoke error, or returning
    on not-found each fails one row.

## Out of scope / follow-ups

- **Separation of duties** (the reviewer differs from the executor) is TASK-007. The audit's F6
  one-line fix names it, but this task only makes `required_skills` trustworthy.
- **Matching `required_skills` by `skill_id`** is descoped (Decision 13). With admin-only
  management and the rename guard, no non-admin can move eligibility. Revisit if workflows ever
  move to id references.
- **Domain events for identity changes.** Today these are audit log lines only (Decision 11).
- **An actor update, suspend and reactivate API in standalone.** None exists. Role and status
  changes are database-only, and `security-model.md`'s "admin can update role" has no endpoint.
  That is noted, not built.
- **A rotation grace window** depends on Owner question 2.
- **Re-keying legacy `bootstrap-<hash12>` ids** is descoped. Rotation revokes them (Design §8).
- **An `SPINE_AUTH_TOKEN` strength gate in SMP** is SMP's config concern. Its rows are not
  consulted by a live verifier (SMP `internal/recovery/release.go:18-24`), so nothing is filed.
- **Dev-mode allow-all on the skill, actor and git routes** is unchanged here. Dev-mode git auth is
  TASK-015 (P3-30).
- **Tenant admins listing `smp-admin`'s token rows** is unchanged. With random ids the listing
  discloses nothing about the bearer.
- *Design-check additions (2026-10-06):*
  - **Reserving the `smp-admin` actor id on `POST /actors`.** An admin or operator can register
    an actor named `smp-admin` in a workspace where the bootstrap has not run yet, because
    `SMP_ADMIN_TOKEN` was unset at the time. A later bootstrap adopts that actor and heals its
    name and role. This is no escalation, because an operator cannot mint tokens. It is
    attribution hygiene, and it is noted as a follow-up.
  - **An actor status API** must treat `smp-admin` as platform-operator-only if one is ever
    added (§6 note).
  - **`SMP_ADMIN_TOKEN_PREVIOUS` (zero-downtime rotation) and bearer generation numbers** depend
    on Owner questions 2 and 5.
  - **`ClaimStep`'s silent skill-check skip** when no blocking store is wired (`claim.go:116`)
    belongs to TASK-007, which leaves it alone (its Decision 10). It is not reachable in
    `spine serve`, which always wires the store.
  - **Gateway-wide path-param unescaping.** No handler unescapes chi params. This task decodes
    only `{actor_id}` on the two actor-skill routes, where an identity comparison depends on it.

## Design check (2026-10-06)

The design was checked adversarially against Spine `61994fe` and SMP `80995559`:
- the audit PoCs were re-run against HEAD and against the prototype overlay;
- new probes were run with `go test -overlay`, under
  `tmp/init027-orchestration/spine-init023/_work/task013-check/`;
- SMP callers were grepped.

Neither repository was modified. The designer's text is kept, and each change is marked inline as
"Design-check note (2026-10-06)".

**Confirmed correct.**
- **File references.** Every Spine and SMP file:line reference sampled matched `61994fe` and
  `80995559`: the permission map, handlers, bootstrap, token store SQL, serve config, resolver and
  cmd, the docs anchors, the scenario test lines, and the SMP provisioning, recovery,
  permissions, actorstore and execution-actor-store references.
- **F6 PoCs.** `poc/skeptic-skills` reproduces at HEAD (200, 200, 204, 200) and is fully refused
  on the prototype (403 on every request, eligibility unchanged). The prototype's full overlay
  passes `core/auth`, `service/internal/gateway` and `service/cmd/spine`.
- **No other sinks.** No other code path writes `auth.skills`, `auth.actor_skills` or
  `auth.actors`. Claim-time skill matching is live in `spine serve`, and every shipped review step
  requires `review`.
- **SMP impact is nil.** SMP uses only `BootstrapInternalAdmin`, `HashToken` and
  `InternalAdminActorID`, always with the real `PostgresStore`. It has no skill-grant route (its
  `/skills` mount is list-only) and no verifier of `auth.tokens`. SMP E4-TASK-012 already carries
  AC 18.

**Changed, and why.**
1. **§2 and AC 7.** `AuthorizeRoleGrant` was fail-open for unknown or empty roles and for
   non-active callers (reproduced). It must fail closed by itself, because it is an exported
   `core/auth` primitive.
2. **§3 and AC 3.** A percent-encoded `{actor_id}` evaded the self check (reproduced). It is not a
   privilege bypass today, because of the admin-only map, the actor id regex and the foreign key.
   But it defeated Decision 3's purpose, so `{actor_id}` is now decoded once, with 400 on a
   malformed escape.
3. **§7, §12, §15, Decision 7, Owner questions 2 and 5.** Three rotation consequences were
   missing:
   - rotation stops being zero-downtime: the platform gets a 401 window;
   - an old-config replica that restarts mid-rollout can permanently revoke the new bearer in
     workspaces created during the rollout (reproduced as X1);
   - the upgrade itself revokes bearers from past rotations.

   All three are now documented, with runbook steps and owner questions.
4. **§8 and the runbook.** The live bearer's legacy hash-derived id stays listable, and is printed
   at Debug, until the first rotation. The runbook now says to rotate once after upgrading.
5. **§12 and AC 15.** AC 12's wiring test needs `serve_delivery_test.go`'s `authCaptureStore` to
   change. That contradicted "no other existing test is modified", and the prototype had never
   built that test. The design check built it, and the test and its mutation behave as the AC
   claims.
6. **§6 and AC 10.** The "heal an empty status to active" half of Decision 6 was unpinned. A test
   row was added.
7. **AC 21 (new).** The revocation error paths (list failure, revoke failure, concurrent
   not-found skip) had no test.
8. **§4 and AC 5.** The rename-to-`review` fixture must have no `review` row, because of
   `UNIQUE(name)`. The guard's active-projection-only scope is noted as accepted.
9. **§14.** Added the textual overlaps with TASK-002, TASK-006, TASK-007, TASK-009 and TASK-012.
10. **§15 and AC 20.** The CHANGELOG tells operators to review `auth.actor_skills` grants made
    before the upgrade, because no grantor is recorded.

## Implementation Notes

Implemented on branch `init/INIT-023-TASK-013-identity-and-skill-authority`, from Spine local
`main` `ee3c249` (origin `61994fe` + the INIT-023 scaffold + merged TASK-001). No SMP file was
changed. The orchestrator closes the task after merge.

### Orchestrator rulings and owner-question defaults

The owner questions take their defaults, recorded here **pending the owner**:

| Question | Default implemented |
|---|---|
| OQ1 `actor.create` role | Stays `operator`, capped by the strictly-below ceiling (`AuthorizeRoleGrant`). |
| OQ2 rotation grace | Immediate revocation on the first load after rotation. The runbook's "switch together" step (§5.6 (a)) covers the 401 window. The opt-in `SMP_ADMIN_TOKEN_PREVIOUS` two-phase rotation is a **recorded follow-up**, not built. |
| OQ3 strength gate outside production | Warn only; only `SPINE_ENV=production` refuses to start. |
| OQ4 rename guard | An admin rename of a skill whose old or new name an active workflow references answers 409 unless `?force=true`; renames are audited (`skill_renamed`). |
| OQ5 restart-during-rollout residue | Accepted and documented (runbook §5.6 (b), KNOWN-LIMITATIONS, CHANGELOG); the recovery is to rotate forward. |

Orchestrator ruling on SMP: SMP seeds `smp-admin` through `auth.BootstrapInternalAdmin`, so the
compatibility gate below proves SMP's provisioning seed path and its gated DB suites still pass.

### What was built

| Area | Change |
|---|---|
| `core/auth/permissions.go` | `skill.create`, `skill.update`, `skill.deprecate` → admin; new `actor.add_skill`, `actor.remove_skill` (admin). `actor.create` and `skill.read` unchanged. `Authorize`'s status refusal moved into `requireActive` (identical behaviour), shared with the new helpers. |
| `core/auth/grants.go` (new) | `ReasonSelfSkillChange`, `ReasonRoleCeiling`, `AuthorizeActorSkillChange` (status → self check for non-admins → role map) and `AuthorizeRoleGrant` (status → unknown/empty granted role refused for everyone → admin any → others strictly below). Both refuse a nil caller. Imports only `core/domain`. |
| `core/auth/bootstrap.go` | `BootstrapAdminConfig.SupersededBefore`; status preservation for suspended/deactivated `smp-admin` (Warn; empty/unknown status still healed); `upsertInternalAdminToken` returns the current row, refuses a revoked current row (Error log, no sweep, nil); `revokeSupersededAdminTokens` (§7 rules, not-found skipped, other errors wrapped with the token id, Info log + `bootstrap_admin_token_revoked` audit); random `bootstrap-<16 hex>` ids. Doc comment rewritten. |
| `service/internal/gateway/authz_identity.go` (new) | `authorizeActorSkillChange` (dev mode → `authorize()`; self refusal audited as `actor_skill_change_refused`), `actorIDParam` (decode-once, 400 `invalid_params` on a malformed escape), `errorReason`. |
| `handlers_actor_skills.go` | Assign/remove use `actor.add_skill` / `actor.remove_skill` with the decoded id for authorization and the store call; `actor_skill_added` / `actor_skill_removed` audits. |
| `handlers_skills.go` | Rename guard (both names, 409 + hint unless `?force=true`, deduplicated workflow list), `skill_renamed` and `skill_deprecated` audits. |
| `handlers_actors.go` | Ceiling via `AuthorizeRoleGrant` after the role parse, before the id is generated; `actor_create_refused` / `actor_created` audits. |
| `service/cmd/spine` | `validateSMPAdminToken` (32-char and 8-distinct-byte floor; production error, otherwise warning; never echoes the token) called in `serveCmd` right after the dev-mode guard; `workspaceDeliveryConfig.ProcessStart` set at boot and passed as `SupersededBefore` in `bootstrapInternalAdmin`. |
| Docs | `security-model.md` §4.1, `api-operations.md` §3.6/§6, `actor-model.md` §3.2/§7.1, `integration-guide.md` (role list, skills paragraph, `SMP_ADMIN_TOKEN` env row), `operator-runbook.md` §5.6 (steps 1-4, (a)-(d), failure modes), README startup gates, KNOWN-LIMITATIONS (rotation window/residue/clock dependence/legacy ids/unset; name-based skill matching and guard scope; reviewing pre-upgrade grants, which record no grantor), CHANGELOG (every §12 row and design-check row, both upgrade notes, embedder section). |

Commits (all prefixed `INIT-023 EPIC-004 TASK-013:`): `288dc9d` inert plumbing + red tests;
`6cb6708` core/auth; `57906ff` gateway; `f48e172` spine serve; `a4f8759` docs; then this notes
commit.

### Red before green

`288dc9d` is the inert-plumbing commit (constants, `AuthorizeActorSkillChange` = `Authorize`,
`AuthorizeRoleGrant` = nil, `SupersededBefore` stored/unread, `ProcessStart` unset,
`validateSMPAdminToken` = `("", nil)`, `setupSkillServerAdmin`) plus every test. On it,
`go test ./core/auth/ ./service/internal/gateway/ ./service/cmd/spine/` had exactly **26 failing
tests, all new or lockstep-updated**, and no pre-existing test failed:

- AC 1-6: `TestSkillAuthority_ContributorCannotEscalate`, `…_SelfGrantedReviewChainBlocked`,
  `…_NonAdminSelfChangeHasDistinctReason`, `…_AdminManagesSkills`,
  `…_ReferencedSkillDeprecateAndRenameNeedForceAndAreAudited`, `TestActorCreate_RoleCeiling`.
- AC 7: `TestAuthorizeActorSkillChange_Matrix`, `…_NilCallerFailsClosed`,
  `TestAuthorizeRoleGrant_Matrix`, `…_UnknownRoleRefusedForEveryCaller`, `…_InactiveCaller`,
  `…_NilCallerFailsClosed`, `TestSkillAndGrantOperationsAreAdminOnly`.
- AC 8-11, 21: `TestBootstrapSecurity_RotationRevokesOldBearer`, `…_TokenIDIndependentOfBearer`,
  `…_DoesNotResurrectDeactivatedAdmin`, `…_KeepsSuspendedAdminSuspended`,
  `…_RevokedCurrentBearerStaysRevoked`, `…_CutoffStillRevokesRowsBeforeIt`,
  `…_RevocationErrorPaths`; lockstep `TestBootstrapInternalAdmin_CreatesActorAndToken`,
  `…_HealsStaleActor`, `…_RotatedTokenInsertsNewRow`.
- AC 12 wiring and AC 14: `TestBootstrapInternalAdmin_RevokesOnlyRowsBeforeProcessStart`,
  `TestValidateSMPAdminToken`, `TestServeRefusesWeakSMPAdminTokenInProduction`.
- The bootstrap scenarios (AC 16) failed on the stub against Postgres (`token_id … does not match`).

New tests that pass on the stub, as designed: `TestBootstrapSecurity_OldReplicaCannotRevokeNewerBearer`
(AC 12) and `TestBootstrapSecurity_AdminMintedTokensSurviveRotation` (AC 13), the
over-revocation guards; `TestBootstrapSecurity_HealsEmptyStatusToActive` (AC 10), the
over-preservation guard; plus three extra guards — `TestAuthorizeActorSkillChange_InactiveCaller`
(status precedes the self check), `TestBootstrapSecurity_SweepScope` (other actors' rows and
non-bootstrap names are never swept) and `TestMemAuthRevokeContract` (fixture sanity). All pass on
the fix.

### Per-AC evidence

| AC | Evidence |
|---|---|
| 1 | `service/internal/gateway/identity_authority_test.go` `TestSkillAuthority_ContributorCannotEscalate`: self-grant 403 `self_skill_change_forbidden`; mint, colluding grant, strip, forced deprecate, rename all 403; eligibility and catalogue unchanged. |
| 2 | `service/internal/gateway/skill_selfreview_e2e_test.go` `TestSkillAuthority_SelfGrantedReviewChainBlocked` (real router, real `engine.Orchestrator`, real `workflows/task-default.yaml`): [2] 403, [3] 403 and alice still eligible, [4] claim refused ("missing required skills"), zero merges recorded. |
| 3 | `TestSkillAuthority_NonAdminSelfChangeHasDistinctReason`: operator self-grant and self-removal 403 with the reason and the audit line (`operation`, `target_actor_id`, `reason`, caller `actor_id`); grant to another actor a plain insufficient-role 403 (`required_role=admin`, no reason); `op%2D1` 403 with the reason and no undecoded id stored; `%ZZ` 400 at the wire (net/http) and 400 `invalid_params` from the handler (in-process request with a hand-set `RawPath`). See Deviation 1. |
| 4 | `TestSkillAuthority_AdminManagesSkills`: admin grants to `contributor-1` and to itself (200), `actor_skill_added` audit with all fields; encoded admin grant lands on the decoded id; removal audited. |
| 5 | `TestSkillAuthority_ReferencedSkillDeprecateAndRenameNeedForceAndAreAudited`: rename away 409 (body `workflows` + hint, name unchanged), unforced deprecate 409, forced deprecate 200 with `skill_deprecated … forced=true referencing_workflows=1`; rename `x`→`review` with **no** `review` row 409, forced 200 + `skill_renamed`; unreferenced rename and same-name update need no force. |
| 6 | `TestActorCreate_RoleCeiling`: operator→admin and operator→operator 403 `role_ceiling_exceeded` with `granted_role`, nothing stored; operator→reviewer and admin→admin 201; `actor_create_refused` / `actor_created` audits. |
| 7 | `core/auth/grants_test.go`: 5 roles × self/other × both ops; inactive callers (suspended, deactivated, empty) get `actor_status`, never the self reason; full 5×5 grant matrix; `""`, `superuser`, `Admin` refused for every caller; suspended admin / deactivated operator / empty-status admin get the status detail; nil callers refused without panicking; the operation map pinned. |
| 8 | `core/auth/bootstrap_security_test.go` `TestBootstrapSecurity_RotationRevokesOldBearer`: old bearer → "token revoked", new bearer validates and passes `token.create`; the revocation is audited; no log line carries a bearer or its hash prefix. |
| 9 | `TestBootstrapSecurity_TokenIDIndependentOfBearer` (bearer `admin` accepted; id matches `^bootstrap-[0-9a-f]{16}$`, not a hash prefix) and the updated `TestBootstrapInternalAdmin_CreatesActorAndToken`. |
| 10 | `TestBootstrapSecurity_DoesNotResurrectDeactivatedAdmin` (via `actor.Service.Deactivate`, Warn logged), `…_KeepsSuspendedAdminSuspended` (via `Suspend`; an explicit `Reactivate` still works), updated `TestBootstrapInternalAdmin_HealsStaleActor`, and `TestBootstrapSecurity_HealsEmptyStatusToActive`. |
| 11 | `TestBootstrapSecurity_RevokedCurrentBearerStaysRevoked`: A, B, A → A refused, B validates, `level=ERROR` "revoked bootstrap admin token". |
| 12 | `TestBootstrapSecurity_OldReplicaCannotRevokeNewerBearer`, `…_CutoffStillRevokesRowsBeforeIt`, and the wiring test `service/cmd/spine/serve_delivery_test.go` `TestBootstrapInternalAdmin_RevokesOnlyRowsBeforeProcessStart` (`authCaptureStore` gains `ListTokensByActor`, `RevokeToken`, `RevokedAt`; `ProcessStart` between two pre-seeded rows; only the earlier one is revoked; `loadWorkspaceDeliveryConfig().ProcessStart` non-zero). |
| 13 | `TestBootstrapSecurity_AdminMintedTokensSurviveRotation` (a `tok_` token named `smp-admin-bootstrap`, made strictly older than the rotation). |
| 14 | `service/cmd/spine/smp_admin_token_test.go`: `TestValidateSMPAdminToken` (the AC table plus the 32-char/8-distinct floor both sides; no message echoes the token, each names the variable and the remedy) and `TestServeRefusesWeakSMPAdminTokenInProduction`. |
| 15 | Only the Design §12 tests changed (plus gofmt import order in those files, Deviation 6); no existing `Test…` in `serve_delivery_test.go` changed. `go build ./...`, `go vet ./...`, `go test ./...` pass. |
| 16 | `bootstrap_admin_idempotency_test.go` updated (old bearer 401 via new `…ExpectingUnauthorized` steps at both sites, id regex at both sites, old row revoked / new row live in both row-count steps); passes against Postgres (command below). |
| 17 | `dev/lint-boundaries.sh` OK with no allowlist change; golangci-lint 0 new issues; `cmd/embed-smoketest` OK. |
| 18 | SMP compatibility gate below: all pass, no SMP file changed. |
| 19 | `git diff main...HEAD -- migrations/` is empty; `adapters/store/interfaces.go` (`store.AuthStore`) unchanged. |
| 20 | Every §15 document and the §15 note's runbook (a)-(d) updated; CHANGELOG covers every §12 row and the design-check rows plus both upgrade notes. |
| 21 | `TestBootstrapSecurity_RevocationErrorPaths`: list failure → wrapped, "list bootstrap admin tokens"; revoke failure → wrapped, "revoke superseded bootstrap admin token" + the token id; first revoke not-found (revoked concurrently) → skipped, nil returned, the second superseded row still revoked. |

**Audit PoCs.** All seven PoCs were re-run unchanged with `go test -overlay` (the gap1 PoC with
its fixtures renamed to avoid colliding with the new test file's `memAuth`): every one now fails
to reproduce — self-grant/strip/deprecate 403, the R1-4 chain stops at the claim (409 missing
skills, no merge), operator `role=admin` 403 `role_ceiling_exceeded`, old bearer "token revoked",
deactivated `smp-admin` stays deactivated. Their inverted forms are the committed tests above.

### Mutations

Run with `go test -overlay` on mutated copies (no repository file touched); **33 of 33 killed**:

| AC | Mutation | Killed by |
|---|---|---|
| 1 | `skill.create` / `actor.add_skill` / `actor.remove_skill` → contributor (3 runs); self check removed | AC 1 test |
| 2 | `actor.remove_skill` → contributor; `actor.add_skill` → contributor **and** self check removed | AC 2 test |
| 3 | self check removed; `url.PathUnescape` dropped | AC 3 test |
| 4 | self check applied to admins; `actor_skill_added` audit removed | AC 4 test |
| 5 | rename guard removed; `skill_deprecated` audit removed | AC 5 test |
| 6 | `>=` → `>` in `AuthorizeRoleGrant`; call in `handleActorCreate` disabled | AC 6 test |
| 7 | `>=` → `>`; self check narrowed (`!= admin` → `< operator`); `RoleLevel()==0` refusal removed; status check removed from `AuthorizeRoleGrant` | AC 7 tests |
| 8 | `revokeSupersededAdminTokens` call removed | AC 8 test |
| 9 | `"bootstrap-"+hash[:12]` restored | AC 9 test and `…_CreatesActorAndToken` |
| 10 | `desired.Status = existing.Status` deleted (each of the three tests killed individually); preserve rule widened to any non-active status | AC 10 tests; empty-status test |
| 11 | revoked-current branch deleted; sweep removed | AC 11 test |
| 12 | cutoff ignored; `SupersededBefore: cfg.ProcessStart` dropped from `serve_resolver.go`; `ProcessStart` not set | AC 12 tests |
| 13 | `bootstrap-` prefix condition dropped | AC 13 test |
| 14 | distinct-byte rule disabled; call in `serve_cmd.go` removed | AC 14 tests |
| 21 | list error swallowed; non-not-found revoke error swallowed; return on not-found | AC 21 test |

### Deviations from the design, with reasons

1. **`{actor_id}` is decoded only when `r.URL.RawPath` is set** (Design §3 note says "decode once
   with `url.PathUnescape`"). chi routes on `RawPath` when it is set and on the already-decoded
   `Path` otherwise; net/http sets `RawPath` only for a non-canonical escaping. Unescaping
   unconditionally would decode a `Path` a second time (`/actors/op%252D1/…` names the literal id
   `op%2D1`, not `op-1`). Decoding exactly when chi saw the escaped form is "decode once" relative
   to the wire. The AC 3 encoded row and its mutation behave as designed.
2. **A malformed escape never reaches the handler over the wire.** net/http rejects a request line
   with `%ZZ` (plain-text 400) before routing, so AC 3's "400 `invalid_params`" is reachable only
   from an in-process caller or a front end that hands the raw path through. The handler's 400 is
   kept as defence in depth; the test asserts both the wire-level 400 and the handler's
   `invalid_params` (in-process request with a hand-set `RawPath`).
3. **Both `core/auth` helpers refuse a nil caller** (`ErrForbidden`) instead of panicking, as
   befits an exported primitive; `AuthorizeRoleGrant`'s ceiling detail also carries
   `operation=actor.create`.
4. **`ProcessStart` is truncated to microseconds** (Postgres precision), so a row a replica inserts
   in its first microsecond never compares as "before" its own start.
5. **The rename 409 body deduplicates** a workflow that references both the old and the new name.
   `actor_create_refused`'s `reason` is read from the error detail (always `role_ceiling_exceeded`
   in practice, since `authorize()` already refused inactive callers).
6. **gofmt.** Eight touched files were not gofmt-clean on `main` (unsorted imports after the
   adapters move): `core/auth/{auth_test,bootstrap,bootstrap_test}.go`,
   `service/cmd/spine/{serve_delivery_test,serve_resolver}.go`,
   `service/internal/gateway/{handlers_coverage2_test,handlers_skills_test}.go` and the bootstrap
   scenario file. They were formatted (import order only), as TASK-001 did; golangci-lint reports 8
   fewer gofmt issues as a result.
7. **Extra tests** beyond the AC list (all listed above): `…_CutoffStillRevokesRowsBeforeIt`,
   `…_SweepScope`, `TestMemAuthRevokeContract`, the nil-caller and inactive-caller tests,
   `TestSkillAndGrantOperationsAreAdminOnly`, and extra rows in the AC 3-5 tests.
8. **KNOWN-LIMITATIONS.md** gained two Security Notes entries (rotation; name-based skill matching)
   although §15 does not list the file: they record the accepted residues (OQ2, OQ5), the replica
   clock dependence of `created_at` ordering, the legacy-id window and the active-projection-only
   guard scope.
9. **The runbook section is §5.6** under "5. Rotating Credentials"; its failure-modes table adds the
   Warn/Error log lines operators will see.

No design point required a schema change.

### Gates (all on the branch head before this notes commit, `a4f8759`)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` (no allowlist change) |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | "engine constructed and exercised; library surface is sound." |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 ./...` | rc 0 |
| `go test -race -count=1 -p 4 ./core/auth/ ./service/internal/gateway/ ./service/cmd/spine/` | ok ×3 |
| `SPINE_TEST_DATABASE_URL=…/s013_scenario go test -race -tags scenario -count=1 -p 4 -run '^(TestBootstrapInternalAdmin_\|TestGitHTTP_)' ./service/scenariotest/scenarios/` | ok (AC 16 Postgres proof; the CI `scenario-git-http` set, 19 `TestGitHTTP_*` pass, none skip) |
| `SPINE_TEST_DATABASE_URL=…/s013_scenario make test-scenario` | **not green, pre-existing**: run sequentially (`go test -tags scenario -count=1 -p 1 ./service/scenariotest/...`) on the branch and on an export of `main` (own database each), the failure sets are **identical, 30 tests each** (fixture/harness/actor-API/gateway scenarios; CI scopes this job to `TestGitHTTP_*` because the rest "is not green on main today"). Both `TestBootstrapInternalAdmin_*` scenarios pass. A parallel-package run showed 5 extra failures that pass standalone (cross-package sharing of one database). |
| `SPINE_TEST_DATABASE_URL=…/s013_integ go test -tags integration -count=1 -p 1 ./adapters/store/` | not green, **pre-existing and unrelated** (`adapters/` untouched): `TestRunModeDatabaseDefault` (`workflow_version_label` NULL scan) and an intermittent `TestEventSubscription_SigningSecretEncryption`; identical on an export of `main`. No integration-tagged tests exist in the touched packages. |
| `golangci-lint run ./core/auth/... ./service/internal/gateway/... ./service/cmd/spine/...` (branch vs `main` export, uncapped) | 98 vs 105 issues; **0 new** (7 gofmt fewer) |
| `golangci-lint run --build-tags scenario ./service/scenariotest/scenarios/` | 59 vs 60; **0 new** |
| `golangci-lint run --enable-only=gosec` (same two scopes) | 0 issues on branch and `main` |
| `gofmt -l $(git diff --name-only main...HEAD \| grep '\.go$')` | empty |
| `git diff main...HEAD -- migrations/` | empty |
| `codex review --base main` | "No actionable regressions were identified in the changes." No P1/P2 findings, nothing rejected. |

### SMP compatibility gate (AC 18)

`/Users/bartoszszymichowski/42sid/i23/s-T013/smp`, detached at SMP `main` `7d4e7cc9`, `../spine` =
this worktree, no `vendor/`, no `tmp/`, no SMP file changed (`git status` clean after every step).
Databases: shared PostgreSQL 17.10 + pgvector, own `s013_smp_it` (platform migrations via
`go run ./dev/apply-migrations -set platform -dir migrations`) and `s013_spine_rt`
(`-set spine-runtime`), existing `smp_prov` role (not modified).

| Command | Result |
|---|---|
| `go build ./...`; `go build ./cmd/... ./internal/... ./scenariotest/... ./dev/...` | OK |
| `go vet ./...`; `go vet -tags integration ./...` | clean |
| `./dev/lint-spine-internals.sh`; `./dev/lint-spine-internals-self-test.sh` | OK; self-test OK (2 fixtures) |
| `go test -p 4 -count=1` on every SMP package depending on Spine `core/auth` (`go list -deps`: 28 packages, direct importers `internal/customer/spinecore` and `internal/customer/workspace`) plus `./internal/customer/workspace/... ./internal/recovery/... ./internal/customer/spinecore/... ./cmd/customer-app/...` | rc 0, 29 packages ok |
| `TEST_PROVISIONING_DSN=postgres://smp_prov:…@localhost:55630/postgres go run ./dev/testgate -v -- -race -count=1 -run '^TestCreateInitialActors_Integration_' ./internal/customer/workspace/` | **4 passed, 0 skipped** — `SeedsIdempotentAndEnqueues`, `TokenHashCollisionFailsClosed`, `IncompleteConfigSkipsSubscription`, `EncryptsSigningSecretAtRest` |
| `go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -- -race -count=1 -p 2 ./internal/customer/workspace/ ./internal/customer/workspace/workspacetest/ ./internal/recovery/` (`TEST_DATABASE_URL`/`TEST_PLATFORM_DB`=`s013_smp_it`, `TEST_PROVISIONING_DSN`, `TEST_MINIO_*` = a temporary MinIO from the local `minio/minio` image, removed afterwards) | PASS: 778 tests, 1 allowed skip (live AWS KMS) |
| `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race -count=1 -p 4 ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (`TEST_SPINE_DB`=`s013_spine_rt`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN`) | PASS: 1539 tests, 1 allowed skip (the parked placeholder) |

No `ws_*` database was left behind by the provisioning suites. SMP's seed path is unaffected in
substance: its one bearer per workspace makes the sweep a no-op except after an SMP-side rotation,
and no live SMP path consults `auth.tokens`.

### Not run

- SMP's full CI `integration` job beyond the suites above (the remaining platform-DB packages do
  not depend on Spine `core/auth`), the `faultinject` failure-boundary step, and SMP's images and
  RDS-emulation jobs. They re-run under SMP TASK-012's `.spine-revision` bump.
- `TEST_AWS_OBJECT_STORE_*` (needs a real AWS account), as in SMP CI.

### Follow-ups recorded

- `SMP_ADMIN_TOKEN_PREVIOUS` two-phase rotation (OQ2 option ii); bearer generation numbers (OQ5).
- Matching `required_skills` by immutable `skill_id` (Decision 13); domain events for identity
  changes (Decision 11); an actor status API that treats `smp-admin` as platform-operator-only;
  reserving the `smp-admin` id on `POST /actors`; gateway-wide path-param unescaping.
- SMP: if SMP ever adds a verifier for `auth.tokens`, pass its process start as
  `SupersededBefore` (Design §13).

### Review round 1 (2026-10-07)

Three reviewers (correctness and SMP compatibility, adversarial security, completeness and tests)
examined the branch at `b61e1f2`. Every finding was checked against the code first. Commits:
`a1ceb9e` (core/auth), `c5ca291` (spine serve), `160aece` (gateway), `2f78020` (CI), `26e9a84`
(docs), then this notes commit.

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P2 | `bootstrap.go`: when the new bearer B is seeded first and a replica still carrying A then inserts A, A's row is newer than B's and no B-carrying replica ever revokes it; Design §7's bound is wrong for that order and the docs omit it | **Fixed (documented + made visible).** Confirmed by reading the sweep: the "not after the current row" rule spares A on every later load. It cannot be closed by time ordering — from the data, A is indistinguishable from a newer bearer that a stale replica must not revoke — so the rule stays (owner question 5; generation numbers are the full fix). The sweep now logs such a row at **Warn** with its `token_id` when no rollout explains it (no cutoff, or created before the cutoff); the rolling-deploy case (after the cutoff) stays quiet, and the line omits the current row's id (it may be a legacy hash-derived id). Design §7 carries an inline correction; KNOWN-LIMITATIONS, runbook §5.6 ((b) and new (e): find the Warn, revoke the named row, never by age) and the CHANGELOG (Changed, Upgrade notes) describe both residues. `TestBootstrapSecurity_StaleBearerInsertedAfterCurrentIsFlagged` drives R2(B), R1(A), R2 reload (quiet), R3 after the rollout (A still live, Warn names A), admin revoke (stays revoked). |
| 2 | P3 | `serve_resolver.go`: a failed sweep is logged and swallowed; "re-runs on the next workspace load" is false for a busy workspace (rebuild only on idle eviction, invalidation or restart) | **Fixed.** `bootstrapInternalAdmin` now retries a failed bootstrap in the background (`retryInternalAdminBootstrap`: 1s doubling to 1 min, unbounded count) until success or `ServiceSet` close (`AppendCloser` cancels an attempt in flight; `ss.Done` also covers a builder-failure teardown, which skips appended closers). Collisions are not retried. Comment rewritten. Tests: `TestBootstrapInternalAdmin_RetriesUntilTheRotationSweepSucceeds` (two transient list failures, then the old row is revoked without a rebuild), `TestRetryInternalAdminBootstrap_StopsWhenTheServiceSetCloses` (done / cancel), `TestBootstrapInternalAdmin_CollisionIsNotRetried`. |
| 3 | P3 | `BootstrapAdminConfig` zero value is not today's behaviour (INIT-023 opt-in rule); new failure path for SMP's provisioning | **Fixed (recorded as the sanctioned exception).** The initiative already lists this exact change under §7b **Rule 4 — shared-behaviour changes** ("reach SMP at the first bump … whatever the opt-in"; "Nothing in SMP observes this"), so it is not an opt-in by design. The CHANGELOG "For embedders" section and the `SupersededBefore` doc now say so explicitly, state what an embedder that adds a verifier of `auth.tokens` must do (pass its process start; never-un-revoke means a mistyped re-run revokes the real bearer and a rollback stays refused), and name the two extra store calls as a new failure path that a retrying caller (SMP's provisioning step) completes on the next call. No SMP file can be changed here; SMP TASK-012 re-runs AC 18. An opt-in `RevokeSuperseded` flag was not added: it would contradict the initiative's own classification and leave status preservation and random ids non-opt-in anyway. |
| 4 | P3 | `ci.yml`: the Postgres proof for AC 16 never runs in CI | **Fixed.** The Postgres scenario job gains a second step running `^TestBootstrapInternalAdmin_` that fails on any skip and requires both scenarios to pass (the job id/name and the `TestGitHTTP_` step are unchanged, so required checks keep matching). Both steps were run locally with `bash -eo pipefail` against a fresh database, as CI runs them. |
| 5 | P3 | Same as 2 (adversarial angle; sibling replica losing the insert race never sweeps) | **Fixed** with 2. The retry also completes the sibling's load: the second attempt finds the row (existing-row branch) and sweeps. |
| 6 | P3 | `handlers_skills.go`: PATCH and deprecate write every column back; concurrent admin requests undo each other, widened by the rename guard's scans | **Fixed (narrowed to the pre-task window).** Both handlers re-read the skill immediately before `UpdateSkill` (`rereadSkillForWrite`) and apply the request to the fresh row, so a concurrent deprecation survives a PATCH; a name that changed since the first read answers **409 `conflict`** with nothing written (the guard or reference check decided on a stale name). A fully atomic fix needs a conditional `UPDATE` in `adapters/store` (a store change that reaches the library SMP embeds), recorded as a follow-up and in KNOWN-LIMITATIONS; the design-check claim "no un-deprecate path" is annotated. `TestSkillMutations_ConcurrentChangeIsNotOverwritten` (4 rows) fails on `b61e1f2`'s handler. |
| 7 | P2 | No test for the sweep when the current bearer's row already exists | **Fixed.** `TestBootstrapSecurity_SweepRunsWhenCurrentRowExists` (two live rows A older, B newer; load with B; A revoked, B live, no re-insert), with and without a cutoff. |
| 8 | P2 | No test for "never revoke a row newer than current" | **Fixed.** `TestBootstrapSecurity_NeverRevokesRowNewerThanCurrent` (load with the older bearer A; B stays live; Warn names B), with no cutoff and with a cutoff after both rows. |
| 9 | P3 | No test for `{actor_id}` decoding on DELETE | **Fixed.** AC 3 test: operator self-removal through `op%2D1` → 403 `self_skill_change_forbidden`, skill kept, refusal audited; AC 4 test: admin removal through `contributor%2D1` → 204, the decoded actor's grant removed, audit names the decoded id. |
| 10 | P3 | Same as 4 | **Fixed** with 4. |
| 11 | P3 | CHANGELOG claims a JSON 400 `invalid_params` for a malformed escape | **Fixed.** Reworded: over HTTP net/http answers a plain-text 400 before routing; only an in-process caller gets the JSON `invalid_params`. |
| 12 | P3 | `api-operations.md` says "refusals are audit-logged" | **Fixed.** Now "self-change refusals (and `actor.create` ceiling refusals)"; other role refusals are plain 403s without an audit line. |

**Acceptance-criteria gaps raised by the review.**
- **AC 16 (`make test-scenario`).** Not real as a regression: re-run on this head and on an export
  of `main` (own databases `s013_r1full` / `s013_r1main`, `-p 1`), both fail the **identical 30
  tests** (harness, engine and scenario packages; fixture, actor-API and gateway scenarios), and
  both `TestBootstrapInternalAdmin_*` pass. Repairing the suite is outside this task (CI scopes the
  job for the same reason). The bootstrap scenarios now run in CI (finding 4).
- **AC 20 / Design §7 residue.** Fixed with finding 1.
- **AC 18.** The reviewer could not run the DB suites within the database-naming constraint (the
  SMP allocator creates `ws_*` databases). This round ran them on a **throwaway**
  `pgvector/pgvector:pg18` container (local image, removed afterwards), so nothing was created on
  the shared server: see the gate table.
- **AC 3 `%ZZ` → 400 `invalid_params` over the wire.** Not real as a defect: already Deviation 2;
  the CHANGELOG now says the same (finding 11).
- **Design §7 rules without a test / DELETE decode untested.** Fixed with findings 7, 8 and 9.

**Mutations (round 1),** all with `go test -overlay` on mutated copies; **14 of 14 killed**:

| Mutation | Killed by |
|---|---|
| existing-row branch returns `nil, nil` (no sweep) | `…_SweepRunsWhenCurrentRowExists`, `…_NeverRevokesRowNewerThanCurrent`, `…_StaleBearerInsertedAfterCurrentIsFlagged` |
| "newer than current" rule removed | `…_NeverRevokesRowNewerThanCurrent`, `…_StaleBearer…` |
| Warn removed | `…_NeverRevokesRowNewerThanCurrent`, `…_StaleBearer…` |
| Warn regardless of the cutoff | `…_StaleBearer…` (Warn during the rollout) |
| background retry not started | `…_RetriesUntilTheRotationSweepSucceeds` |
| retry ignores `done` | `…_StopsWhenTheServiceSetCloses/done_closed` |
| retry continues after a collision | `…_CollisionIsNotRetried` |
| first-attempt collision starts a retry | `…_CollisionIsNotRetried` |
| workspace id not added to the bootstrap context | `…_CoreLogLinesNameTheWorkspace` |
| `b61e1f2`'s `handlers_skills.go` (no re-read) | `TestSkillMutations_ConcurrentChangeIsNotOverwritten` (4 rows) |
| no re-read in PATCH | same, 3 rows |
| no re-read in deprecate | same, the rename row |
| re-read without the name check | same, 2 rows |
| remove route uses the raw `chi.URLParam` | AC 3 and AC 4 tests |

**Also changed.** `bootstrapInternalAdmin` adds the workspace id to the bootstrap context: the pool
builder's context named no workspace, so core/auth's lines (including the pre-existing revoked-
current Error the runbook tells operators to act on per workspace) did not say which workspace.
Named in the CHANGELOG (log shape).

**Gates (review round 1, on `26e9a84`).**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | "engine constructed and exercised; library surface is sound." |
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | rc 0 |
| `go test -race -p 4 -count=1 ./core/auth/ ./service/internal/gateway/ ./service/cmd/spine/` | ok ×3 |
| CI scenario job, both steps, `bash -eo pipefail`, `SPINE_TEST_DATABASE_URL=…/s013_r1scen` | `TestGitHTTP_*` 19 pass, `TestBootstrapInternalAdmin_*` 2 pass, no skip |
| `go test -race -tags scenario -p 4 -run '^(TestBootstrapInternalAdmin_\|TestGitHTTP_)' ./service/scenariotest/scenarios/` (`s013_r1scen`) | ok |
| `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` on the branch (`s013_r1full`) and on an export of `main` (`s013_r1main`) | both rc 1, **identical 30-test failure set** (pre-existing) |
| integration-tagged tests in touched packages | none exist (`core/auth`, `service/internal/gateway`, `service/cmd/spine`) |
| `golangci-lint run` on the three touched package trees, branch vs `main` export | 98 vs 105 issues, **0 new** |
| `golangci-lint run --enable-only=gosec` (touched trees; scenarios with `--build-tags scenario`) | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| `git diff main...HEAD -- migrations/`; `store.AuthStore` | empty; unchanged (no `adapters/` change at all) |
| `codex review --base main` (plain, on `26e9a84`) | "No actionable regressions were found in the changes relative to the supplied merge base." No P1/P2 findings; nothing rejected. |

**SMP compatibility gate (round 1).** `/Users/bartoszszymichowski/42sid/i23/s-T013/smp` at
`7d4e7cc9`, `../spine` = this worktree, no `vendor/` or `tmp/`, `git status` clean throughout. The
only round-1 change that reaches SMP is in `core/auth/bootstrap.go`: a Warn in a branch SMP's
single-bearer calls never take, and comments.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | OK, clean |
| `./dev/lint-spine-internals.sh`; `./dev/lint-spine-internals-self-test.sh` | OK; self-test OK (2 fixtures) |
| `go test -p 4 -count=1` on the 28 SMP packages that depend on Spine `core/auth` (`go list -deps`) plus `./internal/customer/workspace/... ./internal/recovery/... ./internal/customer/spinecore/... ./cmd/customer-app/...` | rc 0, 29 packages ok |
| Throwaway cluster: `pgvector/pgvector:pg18` on 127.0.0.1:19613 (local image, `--pull never`), roles and `smp_it` / `spine_runtime_it` per SMP `dev/README.md`, migrations via `dev/apply-migrations` | provisioned |
| `TEST_PROVISIONING_DSN=postgres://smp_prov:…@localhost:19613/postgres go run ./dev/testgate -v -- -race -count=1 -run '^TestCreateInitialActors_Integration_' ./internal/customer/workspace/` | **4 passed, 0 skipped** (`SeedsIdempotentAndEnqueues`, `TokenHashCollisionFailsClosed`, `IncompleteConfigSkipsSubscription`, `EncryptsSigningSecretAtRest`) |
| `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race -count=1 -p 4 ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` on the throwaway cluster) | PASS: 1446 tests, 1 allowed skip |
| `ws_*` leak check on the throwaway cluster; `docker rm -f` | none; container removed |

The shared server's databases `s013_r1scen`, `s013_r1full` and `s013_r1main` were dropped.

**Follow-ups added.** A conditional (compare-and-set) skill `UPDATE` in `adapters/store` behind an
optional interface, to close the remaining PATCH/deprecate read-write window.
