# Operator Runbook — Multi-Repository Lifecycle

This runbook covers the operator-facing tasks introduced by [INIT-014](/initiatives/INIT-014-multi-repository-workspaces/initiative.md): registering code repositories, inspecting catalog and runtime binding state, recovering partial-merge runs, rotating credentials, and deregistering repositories. It assumes the deployment has applied migrations through `023_partially_merged_run_status`, `024_add_step_execution_repository_id`, and `025_add_run_repository_baselines` — the run lifecycle and partial-merge recovery paths read columns added by all three.

The product model is in [`/product/multi-repository-workspaces.md`](/product/multi-repository-workspaces.md). The architecture is in [`/architecture/multi-repository-integration.md`](/architecture/multi-repository-integration.md). Runbook entries link back into those for design rationale; the runbook focuses on what to type and what to expect back.

---

## 1. Prerequisites and Roles

- Authenticated against the workspace. Workspace identity is supplied via the `--workspace` flag on the CLI or the `SPINE_WORKSPACE_ID` env var; the API takes it from the `X-Workspace-ID` header.
- A bearer token (`SPINE_TOKEN`) is required. Write operations need a token whose role grants the relevant capability: `repository.create`, `repository.update`, `repository.deactivate`, `run.merge.resolve`, or `run.merge.retry`. Read operations need `repository.read`. The `operator` role bundles all of these.
- The workspace primary repository (`kind: spine`) is provisioned as part of workspace creation and cannot be registered or deregistered through this runbook ([ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) §2.1).
- All commands assume the Spine CLI is on `$PATH`. The CLI talks to the workspace gateway. Where examples show a raw `curl`, the equivalent CLI call is shown alongside.

---

## 2. Registering a Code Repository

### 2.1 Goal

Add a code repository (`kind: code`) to a workspace so it becomes a valid target for tasks that declare it in their `repositories:` field.

### 2.2 v0.x deployment note

This section of the runbook documents the operational contract for code-repo registration. The stock v0.x `spine serve` binary does not yet wire all the pieces needed to make that contract end-to-end usable. Three independent gaps:

- **Gateway endpoints unwired.** `gateway.ServerConfig.RepositoryManager` is optional and not constructed by the default serve path. `POST /api/v1/repositories` and the rest of `/api/v1/repositories/...` return `503 Service Unavailable` with `repository manager not configured` until an operator-supplied serve configuration constructs and injects a `repository.Manager`.
- **Validator catalog source.** The validation engine in default serve uses `validation.PrimaryOnlyCatalogSnapshot` rather than reading `/.spine/repositories.yaml`. RE-001 only accepts `repositories: [spine]`; any task declaring code-repo IDs is rejected at run start. Editing `/.spine/repositories.yaml` and the runtime `repositories` table directly does not help here — the validator's catalog snapshot is built independently and would still report code-repo IDs as unknown.
- **Active-runs gate.** `repository.NewManager` defaults to `NopRunReferenceChecker`, so even if the manager is wired, deactivation does not block on in-flight runs. See §6.

What that means in practice for v0.x: operators who need multi-repo workflows today must build a custom serve binary that (a) constructs and wires `repository.Manager`, (b) replaces `validation.PrimaryOnlyCatalogSnapshot` with the Git-backed catalog loader (a TODO inside the serve binary, see `cmd/spine/cmd_serve.go`), (c) replaces the registry's catalog loader — `cmd/spine/cmd_serve.go` constructs `repoRegistry` with `repository.ParseCatalog(nil, repoSpec)`, so even after fixing the validator, the registry that `WithRepositoryResolver` and the Git pool consume still treats code repos as unknown until that loader reads `/.spine/repositories.yaml` for real, and (d) injects a production `RunReferenceChecker`. The runbook entries below describe the contract that surface presents; until the auto-wire lands in the stock build, none of the CLI/API examples will work against a default `spine serve`.

### 2.3 What gets written

Per [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md), each registered repository has two records:

- A **catalog entry** in `/.spine/repositories.yaml` on the primary Spine repo's `main` branch. This is the governed source of truth for identity (`id`, `kind`, `name`, `default_branch`, optional `role` / `description`).
- A **runtime binding row** in `runtime.repositories` carrying operational state (`clone_url`, `credentials_ref`, `local_path`, `status`).

`POST /api/v1/repositories` writes both. If the binding write fails the catalog write is rolled back so the two stores stay consistent.

### 2.4 Steps

1. **Pick an `id`.** Workspace-scoped, lowercase alphanumeric with single internal hyphens, max 64 chars. `spine` is reserved for the primary repo.
2. **Provision the credential reference** (optional). If the clone URL needs authentication, configure your secret-client backend to hold the credential and note the reference path (e.g., `vault://spine/payments-service/git-token`). See [`docs/integration-guide.md`](/docs/integration-guide.md) §6 for credential helper protocol details and [ADR-010](/architecture/adr/ADR-010-secret-client-abstraction.md) / [ADR-011](/architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md) for the abstraction.
3. **Pick a `local_path`.** The on-disk path Spine will clone into within the workspace's storage volume. It must not already exist.
4. **Register.**

   CLI:

   ```
   spine repository register payments-service \
     --name "Payments Service" \
     --default-branch main \
     --clone-url https://github.com/acme/payments-service.git \
     --credentials-ref vault://spine/payments-service/git-token \
     --local-path /var/spine/repos/payments-service \
     --role service \
     --description "Core payment processing API"
   ```

   API:

   ```
   POST /api/v1/repositories
   Authorization: Bearer <operator-token>
   Content-Type: application/json

   {
     "id": "payments-service",
     "name": "Payments Service",
     "default_branch": "main",
     "clone_url": "https://github.com/acme/payments-service.git",
     "credentials_ref": "vault://spine/payments-service/git-token",
     "local_path": "/var/spine/repos/payments-service",
     "role": "service",
     "description": "Core payment processing API"
   }
   ```

5. **Confirm.** A successful response is `201 Created` with the merged repository view (catalog identity + binding operational fields). Any userinfo embedded in the clone URL is redacted from the response — operators should prefer `credentials_ref` over `https://user:pw@host` URLs precisely because the catalog never carries the password.

### 2.5 Failure modes

| Symptom | Cause | Remedy |
|---------|-------|--------|
| `409 Conflict` with "repository already exists" | The `id` is already in the catalog (active or inactive). Note: §6 deactivation does NOT remove the catalog entry, so re-registering the same ID after deactivation also returns this 409 in v0.x | Pick a different ID. Full removal is reserved for a future API ([`/architecture/multi-repository-integration.md`](/architecture/multi-repository-integration.md) §6.3) |
| `400 Bad Request` with invalid clone URL | `clone_url` does not parse, or scheme is unsupported | Use `https://`, `ssh://`, `git://`, `file://`, or SCP-like (`user@host:path`) form |
| `400 Bad Request` with invalid ID | `id` violates the catalog regex | Use `^[a-z0-9]+(-[a-z0-9]+)*$`; max 64 chars |
| `403 Forbidden` | Token lacks `repository.create` capability | Issue a token with the `operator` role |
| `500 Internal Server Error` followed by retry success | Binding write failed; catalog write was rolled back | The rollback already happened — the same call can safely be re-issued |

---

## 3. Inspecting Catalog vs Runtime Binding State

### 3.1 Goal

Understand what Spine knows about a repository — its identity (catalog) and its operational details (binding).

### 3.2 List all repositories

```
spine repository list
# or
GET /api/v1/repositories
```

Response is an object with an `items` array of merged views: `{"items": [{"id": "...", "kind": "...", ...}, ...]}`. Each entry shows `id`, `kind`, `name`, `default_branch`, `clone_url` (redacted), `credentials_ref`, `local_path`, `status` (`active` or `inactive`), plus `role` / `description` when set. The primary `spine` repo always appears with `kind: spine` and `status: active`.

### 3.3 Inspect a single repository

```
spine repository inspect payments-service
# or
GET /api/v1/repositories/payments-service
```

Returns the same merged view for one ID. Use this to confirm a registration or check whether a binding has been deactivated.

### 3.4 Reading the catalog directly

The catalog is committed under governance. To see what was last written by Spine, read `/.spine/repositories.yaml` from the primary repository's authoritative branch. The engine merge target is hardcoded to `main` in v0.x (`internal/engine/merge.go::authoritativeBranch`), so for stock deployments the command is:

```
git show main:.spine/repositories.yaml
```

If your deployment has been customized so the primary repo's authoritative branch is something other than `main`, substitute that branch name in the `git show` invocation.

The catalog file is the **identity** source of truth. The runtime binding row is the **operational** source of truth. The merged view returned by the API is the canonical operator-facing combination.

### 3.5 Failure modes

| Symptom | Cause | Remedy |
|---------|-------|--------|
| `404 Not Found` on `inspect` | ID does not exist in the catalog | Confirm the ID via `spine repository list` |
| Repository appears in `list` with `status: inactive` | Binding has been deactivated (§6) | A deactivated repo cannot be a run target. There is no public reactivation API in v0.x; treat deactivation as one-way and plan accordingly |
| Catalog entry shown without binding fields | Catalog row exists but binding is missing (rare; indicates a partial registration) | Open an issue; the catalog/binding consistency invariant is supposed to hold |

---

## 4. Partial-Merge Recovery — End-to-End Walkthrough

### 4.1 Goal

A multi-repo run reached `partially-merged` because one or more code repositories' merges failed permanently. Resume the run forward by clearing the failure on a per-repo basis, or cancel cleanly if the work needs to be abandoned.

### 4.2 Reference contract

This walkthrough is the operational counterpart of [`/architecture/error-handling-and-recovery.md`](/architecture/error-handling-and-recovery.md) §5.4. The supported recovery surface is exactly two orchestrator APIs (each with a CLI counterpart):

- `POST /api/v1/runs/{run_id}/repositories/{repository_id}/retry` (`spine run merge retry`) — flips the per-repo outcome from `failed` to `pending`. The next scheduler tick re-attempts the merge.
- `POST /api/v1/runs/{run_id}/repositories/{repository_id}/resolve` (`spine run merge resolve`) — flips the outcome to `resolved-externally` and records the operator's audit reason. Use this when the conflict was resolved out of band (the operator has merged the branch through some other channel, or decided the work is no longer required).

Both APIs write a primary-repo audit ledger commit and an audit event so the operator action is queryable.

### 4.3 Walkthrough

1. **Detect.** Either an alert fires on the `run_partially_merged` event, or operators inspect the run state. The default `spine run inspect` table view does NOT include the per-repo merge outcomes; use the JSON output or a direct API call to see them:

   ```
   spine run inspect run-2026-05-07-abc123 -o json
   # or
   curl -H "Authorization: Bearer $SPINE_TOKEN" \
        "$SPINE_URL/api/v1/runs/run-2026-05-07-abc123"
   ```

   The JSON response includes `status: partially-merged`, the run branch, and a `merge_outcomes` block with one entry per affected repository. Each entry carries `status` (`merged`, `failed`, `pending`, `skipped`, `resolved-externally`), and for `failed` rows, `failure_class` and `failure_detail`.

2. **Read the failure details.** A typical `failed` outcome looks like:

   ```yaml
   - repository_id: payments-service
     status: failed
     attempts: 3
     failure_class: merge_conflict
     failure_detail: "merge of spine/run/task-042-rate-limiting-abc123 into main: conflict in services/api/handler.go"
     source_branch: spine/run/task-042-rate-limiting-abc123
     target_branch: main
   ```

3. **Decide the path.** Two options, picked per failed repository:
   - **Retry path** — Spine should re-attempt the merge after some external state has changed (the source branch was rebased, the target branch was advanced through a separate fix, etc.). Use `retry`.
   - **Resolve path** — the operator has resolved the conflict through some other channel (e.g., manually merged the run branch into the target, or decided the work is obsolete). Use `resolve` with a `--commit-sha` if the operator merged through a different branch and wants Spine to record where the work landed.

4. **Take the action.** Both commands require an audit `--reason`:

   Retry:

   ```
   spine run merge retry run-2026-05-07-abc123 payments-service \
     --reason "branch rebased onto current main; retrying"
   ```

   Resolve externally:

   ```
   spine run merge resolve run-2026-05-07-abc123 payments-service \
     --reason "operator merged via GitHub UI after manual conflict resolution" \
     --commit-sha 9a6654a2d1f3e7c0b8a5f8e3d4c2b1a0f9e8d7c6
   ```

   Each command returns a `MergeRecoveryResult` payload with the new outcome state and a `ready_to_resume` boolean indicating whether the run is now eligible for the scheduler to pick back up.

5. **Wait for resume.** The scheduler's periodic sweep (`Scheduler.retryCommittingRuns`) re-issues `git.retry_partial_merge` automatically, gated on `codeRepoOutcomesAllowResume` — every non-primary outcome must be non-`failed`. Retry advances each cleared repo to `pending`, then to `merged` (or back to `failed` if the conflict persists). Re-inspect the run after one tick interval to confirm forward progress.

6. **Repeat for additional repos.** If multiple code repos failed, run the appropriate `retry` or `resolve` command for each. The resume gate is closed until all are cleared.

7. **Confirm full completion.** Once every repo merges, the run advances to `completed`. `CleanupRunBranch` runs against the recorded outcomes and deletes run branches across all affected repos. The primary-repo audit history retains the recovery ledger commits.

### 4.4 Cancel path

If forward recovery is not appropriate, cancel the run:

```
spine run cancel run-2026-05-07-abc123
```

`CancelRun` flips the run to `cancelled` and invokes `CleanupRunBranch`. Per-repo branches are deleted for every outcome that was not `failed`; failed branches stay preserved for forensic inspection ([`/architecture/multi-repository-integration.md`](/architecture/multi-repository-integration.md) §4.5).

### 4.5 Failure modes

| Symptom | Cause | Remedy |
|---------|-------|--------|
| `409 Conflict` "repository … is already merged" on `retry` | The repo already reached a terminal-success state | No action needed; check `run inspect` to confirm |
| `409 Conflict` "repository … is resolved-externally" on `retry` | The repo is already in `resolved-externally`; operator action superseded by an earlier resolve | Use `retry` only on `failed` outcomes |
| Run stays in `partially-merged` after retry | Another code repo still has `failed` outcome | Clear all failed repos before resume can fire |
| Scheduler does not advance the run | `Scheduler.retryCommittingRuns` is idle (deployment without scheduler enabled) | Ensure the scheduler loop is running; manually invoke `RunRetryCycle` via the admin endpoint if available |
| Branch tip on the code repo was fixed but run still parked | The `RepositoryMergeOutcome.Status` row is still `failed`; the resume gate keys off the row, not the branch tip | Issue `retry` to flip the row to `pending` |
| `403 Forbidden` | Token lacks `run.merge.resolve` or `run.merge.retry` capability | Issue a token with the `operator` role |

---

## 5. Rotating Credentials

### 5.1 Goal

Replace the credential a registered repository uses to clone or push, ideally without disrupting in-flight runs.

### 5.2 Two rotation models

Per [ADR-010](/architecture/adr/ADR-010-secret-client-abstraction.md) and [ADR-011](/architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md), credentials are referenced by `credentials_ref` and dereferenced through the secret client per Git operation:

- **Value rotation behind a stable ref.** The reference (e.g., `vault://spine/payments-service/git-token`) stays the same; the secret backend rotates the value. Spine resolves the new value on the next clone or push automatically. **No Spine action is required for this case.**
- **Reference rotation.** The reference itself changes (new vault path, different secret store, etc.). Update the binding via `PUT /api/v1/repositories/{id}` with a new `credentials_ref`.

### 5.3 Reference rotation steps

1. **Provision the new secret reference** in the secret backend before touching Spine. Ensure it dereferences to the new credential.
2. **Update the binding.**

   API:

   ```
   PUT /api/v1/repositories/payments-service
   Authorization: Bearer <operator-token>
   Content-Type: application/json

   {
     "credentials_ref": "vault://spine/payments-service/git-token-2026-rotated"
   }
   ```

   The CLI does not currently expose a `repository update` command — use the API for credential reference rotation.

3. **Verify on the next operation.** Trigger a `spine repository inspect <id>` and confirm `credentials_ref` matches. The next clone or push will dereference the new ref.

### 5.4 In-flight runs

The conservative procedure is: **rotate the secret backend value first, then (if the reference itself changed) update the binding, then verify on the next clone or push**. Two constraints worth knowing:

- For value rotation behind a stable reference, no Spine-side action is needed. A push that is already in flight may still complete with the old value if it was already dereferenced; the next Git operation picks up the new value.
- For reference rotation, the binding update lands atomically. A Git operation that began before the binding update completes uses the prior reference; an operation that begins after sees the new one. Spine does not pause runs through the rotation, so plan the new reference to be valid before the update lands.

If a run's clone or push fails with `auth_failed` after rotation, the secret-client's `Invalidate(ref)` is the right next step — it drops any cached value for the affected reference so the next Git operation re-dereferences from the secret backend.

### 5.5 Failure modes

| Symptom | Cause | Remedy |
|---------|-------|--------|
| Run pushes start failing with `auth_failed` after rotation | Old reference still in `credentials_ref` and old value rotated away | Update the reference (§5.3) so Spine dereferences the new secret |
| `PUT` succeeds, then pushes start failing with `credentials_unavailable` | The new `credentials_ref` is malformed or points at a path the secret-client cannot dereference. The PUT path stores the string as-is — validation happens later, on the next Git operation, when the secret resolver tries to read the secret. | Inspect the failed Git operation's classification; correct the `credentials_ref` (issue another `PUT`) so the next attempt resolves cleanly. |
| `PUT` succeeds but pushes still fail with `auth_failed` | Secret backend returns the OLD value for the new reference, or the new value is wrong | Verify the secret backend has the new credential at the new path; if the old value is cached, call `Invalidate` on the secret-client (§5.4) and retry |

---

## 6. Deregistering a Repository

### 6.1 Goal

Remove a code repository from active use in a workspace. Because `id` is immutable in the catalog, deregistration is also the renaming primitive — deregister, then register under a new ID.

### 6.2 What deregister means today

The current API exposes `deactivate`, not delete. Deactivating flips the runtime binding row's `status` from `active` to `inactive`:

- The catalog entry in `/.spine/repositories.yaml` stays. Inactive bindings can still be inspected but cannot resolve for execution — a task that declares a deactivated repo in `repositories:` will fail validation at run start.
- The on-disk clone at `local_path` is preserved.
- **In-flight runs are NOT shielded from deactivation.** Subsequent code-repo operations within an open run resolve through `Registry.Lookup` / `repoClients.Client` on every step, and an inactive binding fails that lookup with `ErrRepositoryInactive`. Combined with the v0.x `NopRunReferenceChecker` caveat (§6.3 step 1), deactivating during a non-terminal run will succeed against a stock build and then break the run's next merge or cleanup attempt. Always verify no non-terminal run references the repo before deactivating.

The primary `spine` repository cannot be deactivated. The API rejects the request with `400 Bad Request`.

### 6.3 Steps

1. **Confirm no in-flight runs depend on the repository.** The Manager has a `RunReferenceChecker` hook that returns `412 Precondition Failed` when any non-terminal run (active / paused / committing / partially-merged) still references the target. **Important v0.x caveat**: the default checker is `NopRunReferenceChecker`, which always reports "no active runs", so a stock `spine serve` that wires the manager without a real checker will let `deactivate` succeed even when runs are still referencing the repo. Operators must either inject a production checker into the Manager construction or, until the production checker lands, perform a manual sweep — query open runs (`GET /api/v1/runs/{run_id}` for known runs) and confirm the target's `repository_id` does not appear in any of their `affected_repositories` lists before deactivating. There is no built-in API today for "all runs touching repo X".

2. **Deactivate.**

   CLI:

   ```
   spine repository deactivate payments-service
   ```

   The CLI prompts for confirmation. Use `--yes` to skip in scripts.

   API:

   ```
   POST /api/v1/repositories/payments-service/deactivate
   Authorization: Bearer <operator-token>
   ```

3. **Confirm.** A successful response is `200 OK` with the merged view showing `status: inactive`. The catalog file in `/.spine/repositories.yaml` is unchanged.

### 6.4 Failure modes

| Symptom | Cause | Remedy |
|---------|-------|--------|
| `400 Bad Request` "primary 'spine' repository cannot be deactivated" | Operator targeted the primary repo by ID | Workspace lifecycle is the right tool for primary repo removal — see workspace administration |
| `412 Precondition Failed` "repository … has active runs referencing it" | A non-terminal run still references this repo AND the Manager was constructed with a production `RunReferenceChecker` | Wait for the run to complete, cancel it, or recover it (§4); v0.x stock builds use `NopRunReferenceChecker` and never raise this 412, so manual sweeping per step 1 is required there |
| `200 OK` returned but `inspect` already shows `inactive` | Repository was already deactivated; deactivation is idempotent | No-op; nothing to do |
| Operator wants to re-activate after deactivation | No public reactivation API. The catalog entry persists, so re-registering the same ID returns `409 Conflict`; `Update` deliberately preserves the inactive flag | Plan deactivation as a one-way operation in v0.x. The "deregister" surface in [`multi-repository-integration.md`](/architecture/multi-repository-integration.md) §6.3 is reserved for a future full-removal API |

---

## 7. Failure-Mode Reference

A consolidated cross-section, since some symptoms recur across operations.

| Symptom | Where it surfaces | Where the rule lives |
|---------|-------------------|----------------------|
| Unresolved credential reference | First clone or push attempt against the registered repo | [`/architecture/multi-repository-integration.md`](/architecture/multi-repository-integration.md) §3.4; ADR-010 / ADR-011 |
| Inactive repository in a task's `repositories:` list | Run start validation rejects with the inactive-repo class | `/internal/repository/validation.go`; product `/product/multi-repository-workspaces.md` §3.2 |
| Conflicting merges across repos | Run transitions to `partially-merged` and emits `run_partially_merged` | `/architecture/engine-state-machine.md` §2; recovery in §4 above |
| Repository ID rejected at registration | Catalog regex `^[a-z0-9]+(-[a-z0-9]+)*$` violated | [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) §2.1 |
| Catalog and binding diverged | `spine repository inspect` shows partial fields, registration returned 5xx | The Manager rolls back catalog writes when binding writes fail; persistent divergence is a bug to file |

---

## 8. Cross-References

- API routes — [`/internal/gateway/routes.go`](/internal/gateway/routes.go) is the canonical map (repository endpoints under `/api/v1/repositories`; merge-recovery endpoints under `/api/v1/runs/{run_id}/repositories/{repository_id}/{retry,resolve}`). The OpenAPI spec at [`/api/spec.yaml`](/api/spec.yaml) does not yet describe the repository or merge-recovery surfaces — those entries are pending a follow-on documentation task. Until then, the route file plus this runbook are the authoritative contract.
- CLI reference — `spine repository --help`, `spine run merge --help`.
- Product model — [`/product/multi-repository-workspaces.md`](/product/multi-repository-workspaces.md), [`/product/product-definition.md`](/product/product-definition.md) §5.6–§6.
- Architecture — [`/architecture/multi-repository-integration.md`](/architecture/multi-repository-integration.md), [`/architecture/engine-state-machine.md`](/architecture/engine-state-machine.md) §2, [`/architecture/error-handling-and-recovery.md`](/architecture/error-handling-and-recovery.md) §5.4.
- ADRs — [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md), [ADR-014](/architecture/adr/ADR-014-validation-policy-as-governed-artifact.md), [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md), [ADR-010](/architecture/adr/ADR-010-secret-client-abstraction.md), [ADR-011](/architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md).
- Adjacent runbook — [`docs/git-push-guide.md`](/docs/git-push-guide.md) for push-side credential handling.
- Adjacent integration — [`docs/integration-guide.md`](/docs/integration-guide.md) for management-platform setup, including credential helper protocol and workspace lifecycle.
