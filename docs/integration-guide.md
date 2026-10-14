# Spine Management Platform Integration Guide

This guide covers everything needed to build a management platform that integrates with Spine. It documents the credential helper protocol, workspace management API, environment variables, and deployment patterns.

---

## 1. Architecture Overview

### What Spine Owns

Spine is the **execution authority**. It owns:

- Git repository state (commits, branches, merges)
- Artifact lifecycle (create, validate, status transitions)
- Workflow execution (runs, steps, outcomes)
- Actor assignment and step delivery
- Projection sync (Git -> database cache)

### What the Management Platform Owns

The management platform owns:

- User accounts and platform-level auth
- Git hosting and credential provisioning
- Workspace lifecycle (create, provision, deactivate)
- Monitoring dashboards and alerting
- Container orchestration (Kubernetes, Docker Swarm, etc.)

### Deployment Modes

**Dedicated mode** (one Spine instance per workspace):
- Simplest setup. One container, one workspace, one database.
- Workspace identity set via `SMP_WORKSPACE_ID` env var at container creation.
- Credentials configured via `SPINE_GIT_CREDENTIAL_HELPER` (recommended) or `SPINE_GIT_PUSH_TOKEN` (fallback; scrubbed from process env at startup).

**Shared mode** (multiple workspaces per Spine instance):
- Single Spine instance serves multiple workspaces via a registry database.
- Workspace identity passed per-request via `X-Workspace-ID` header.
- `smp_workspace_id` set per workspace via `POST /workspaces`.
- Credentials resolved per-push from workspace config.

---

## 2. Git Push Credential Integration

### Credential Resolution Chain

Spine resolves push credentials in priority order:

1. **External credential helper** (`SPINE_GIT_CREDENTIAL_HELPER`) -- Git calls an external program (one of: `cache`, `store`, `osxkeychain`, `manager`, `pass`) to retrieve credentials. **Recommended for production.** The token is never resident in Spine's process environment.
2. **Built-in token** (`SPINE_GIT_PUSH_TOKEN`) -- Read once at startup, copied into an in-memory GIT\_ASKPASS helper, and then scrubbed from `os.Environ()` so the token is no longer visible to child processes, `/proc/<pid>/environ`, or core dumps. A step up from env-resident tokens but still inferior to a credential helper. When a credential helper is also configured, the token is ignored with a warning at startup.
3. **Git native** -- Whatever the user configured in their git config (SSH keys, credential store, etc.). Spine does nothing extra.
4. **None** -- Push skipped gracefully. Run completes without pushing.

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `SPINE_GIT_CREDENTIAL_HELPER` | Credential helper name (`cache`, `store`, `osxkeychain`, `manager`, `pass`). Recommended mode. | not set |
| `SMP_WORKSPACE_ID` | Workspace identifier for credential lookup (set by platform) | not set |
| `SPINE_GIT_PUSH_TOKEN` | Static PAT/deploy token for HTTPS push (standalone mode). Scrubbed from process env at startup; ignored when `SPINE_GIT_CREDENTIAL_HELPER` is also set. | not set |
| `SPINE_GIT_PUSH_USERNAME` | Username for token auth | `x-access-token` |
| `SPINE_GIT_PUSH_ENABLED` | Set to `false` to skip push entirely | `true` |

### Git Credential Helper Protocol

Spine configures the selected helper per-repo via `git config --local credential.helper <name>`. Only the short names in the allowlist are accepted: `cache`, `store`, `osxkeychain`, `manager`, `pass`. Free-form paths to custom helper scripts are refused at startup — git treats `credential.helper` values as "run this program," so allowing arbitrary strings is a remote-code-execution surface.

**Input** (from Git, via stdin):
```
protocol=https
host=github.com
```

**Output** (from the selected helper, via stdout):
```
protocol=https
host=github.com
username=x-access-token
password=<token>
```

### Populating the Helper's Backing Store

Since Spine no longer invokes a custom helper script, platform-side credential provisioning becomes a sidecar concern: populate the backing store the chosen helper reads from. Common patterns:

- **`store`** — write `~/.git-credentials` (one `https://user:token@host` line per entry) from a sidecar that fetches tokens from your platform API on workspace activation.
- **`cache`** — prime the in-memory cache by shell-invoking `git credential approve` on startup.
- **`osxkeychain` / `manager` / `pass`** — use the native tooling for the respective secret store.

### Security Requirements

- Never log tokens or credentials. Spine redacts URLs containing credentials in all log output.
- Never write tokens to disk. Remote URL rewriting is per-push, in-memory only.
- `SPINE_GIT_CREDENTIAL_HELPER` only accepts the allowlisted short names (`cache`, `store`, `osxkeychain`, `manager`, `pass`). Arbitrary paths or shell strings are refused at startup — git treats `credential.helper` values as "run this program," so the allowlist is the RCE gate.
- `SPINE_GIT_PUSH_TOKEN` is read once at startup, copied into an in-memory `GIT_ASKPASS` helper, and scrubbed from `os.Environ()`. It is not visible to child processes or `/proc/<pid>/environ`.

---

## 2.1 Secrets at Rest {#secrets-at-rest}

Spine encrypts sensitive fields before persisting them. Currently this applies to `event_subscriptions.signing_secret` (the HMAC key used to sign outbound webhooks); a DB compromise alone is not enough to forge webhook payloads.

### Key Management

- **Key source**: `SPINE_SECRET_ENCRYPTION_KEY` — base64-encoded 32 bytes (AES-256), supplied out-of-band from the database. Generate with `openssl rand -base64 32`. This is the **primary** key: every new or re-encrypted value is sealed with it.
- **Previous keys**: `SPINE_SECRET_PREVIOUS_KEYS` — a comma-separated list of base64 keys that still **decrypt** values sealed before a rotation, but never seal. Together the keys form a key ring. Setting previous keys without a primary key is a startup error.
- **Key ids**: each key has a derived, non-secret 16-hex-character id, `kid` (an HKDF-SHA256 output of the key). `spine serve` logs `primary_key_id` and `previous_key_ids` at startup; operators configure no labels. Errors name a key by its position, never by its value.
- **Production gate**: Starting with `SPINE_ENV=production` and no key is refused at startup. Other environments log a warning and fall back to plaintext so local development does not require extra configuration.
- **Escrow**: losing a key means the values it sealed cannot be decrypted. Back up every key, including retired ones (see *Key rotation* below), alongside other production secrets (cloud KMS, a secrets manager).

### Wire Formats

| Prefix | Written by | Format | Binds |
|---|---|---|---|
| *(none)* | no key configured | plaintext | nothing |
| `enc:v1:` | releases before INIT-023 TASK-009, and `downgrade` mode | `base64(nonce ‖ AES-256-GCM ciphertext ‖ tag)` under the raw key | nothing — the value opens wherever the key is configured |
| `enc:v2:` | this release (`migrate`, `strict`) | `enc:v2:<kid>:base64(nonce ‖ ciphertext ‖ tag)` under an HKDF subkey of the key | the workspace id, the subscription id, the envelope version and the key id |

An `enc:v2` value's GCM associated data covers the envelope version, the `kid`, the column (`runtime.event_subscriptions.signing_secret`), the **workspace id** and the **subscription id**. The workspace id is the one this process serves (`SPINE_WORKSPACE_ID` in file mode, the registry/binding id in shared mode) — never the row's `workspace_id` column, which a database writer could change. So a value copied from another workspace's database, or from another row, fails authentication and is refused; it is never treated as plaintext. This is what closes the transplant of audit finding P3-10 for `enc:v2` values (see *When P3-10 is closed* below).

`enc:v<digits>:` prefixes are reserved: an unknown version is refused, not read as plaintext.

### Envelope Modes — `SPINE_SECRET_ENVELOPE_MODE`

| Mode | Writes | Reads `enc:v2` | Reads `enc:v1` / plaintext | Sweep converts to |
|---|---|---|---|---|
| `migrate` *(default)* | `enc:v2` | yes | accepted | `enc:v2` under the primary key |
| `strict` | `enc:v2` | yes | **refused** | `enc:v2`; legacy rows are reported, not converted |
| `downgrade` | `enc:v1` | yes | accepted | `enc:v1` under the primary key (rollback preparation) |

The value is case-insensitive; anything else refuses to start. Without a key the mode is ignored (with a warning).

### The Re-encryption Sweep

Rows written before a key existed are **not** re-encrypted by later writes (the bootstrap and the update paths skip unchanged secrets — audit finding P3-1). Instead, every time Spine opens a workspace store it runs a sweep:

- **File mode**: at startup, before the internal subscription bootstrap.
- **Shared mode** (`WORKSPACE_RESOLVER=db|platform-binding`): when a process builds a workspace's services — on the first request for it after the process starts, and again after the workspace is evicted (idle for `SPINE_WS_POOL_IDLE_TIMEOUT`, default 10 minutes, or, in `platform-binding` mode, a binding invalidation, which reaches only the replica that receives it). A request to a workspace the process already holds does **not** sweep it and logs nothing.
- **On demand**: `spine secrets reencrypt` (file mode) or `spine secrets reencrypt --all-workspaces` (`WORKSPACE_RESOLVER=db` only: every workspace in the registry, `SPINE_REGISTRY_DATABASE_URL` — **active and inactive**, unlike `spine migrate --all-workspaces`, because an inactive workspace's rows still need the key that sealed them). The command prints one line of counters per workspace and one line per refused row (subscription ids and reasons are printed quoted, so a value planted in a workspace database cannot forge or erase an output line), and exits non-zero unless every workspace is clean.
  - **`SPINE_SECRET_ENVELOPE_MODE` must be set explicitly for the command, to the mode `spine serve` runs with.** The command writes every workspace database and cannot see serve's mode; it refuses to run with the variable unset rather than assume `migrate` (which would convert — and so launder — legacy rows a `strict` deployment refuses) or inherit a stray `downgrade` (which would rewrite every `enc:v2` row to `enc:v1`, which `strict` then refuses).
  - **In file mode the workspace id must be stated too:** set `SPINE_WORKSPACE_ID` to the value `spine serve` runs with, or pass `--workspace default` for a deployment that leaves it unset. The id is part of every `enc:v2` value's associated data, so the command refuses to fall back to `default` (an ops job whose environment was copied from `spine migrate`, which needs no workspace id, would otherwise seal every legacy row under `default`, report the workspace clean, and leave a serve bound to its real id refusing every row). The command prints the id it binds in its first line (`workspace_id=`).
  - `--all-workspaces` refuses to run unless `WORKSPACE_RESOLVER=db`, the only mode in which `spine serve` binds registry ids; in file mode serve binds `SPINE_WORKSPACE_ID`, and sweeping a registry would seal rows under ids serve refuses.
  - An **inactive** workspace whose database can no longer be resolved or reached (a decommissioned tenant whose database was dropped but whose registry row was kept) counts as not clean. Once you have confirmed it is retired, name it in `--allow-unreachable-inactive=<id>[,<id>…]`: it is then reported as acknowledged (and listed in the summary) instead of failing the run. The flag never covers an active workspace or one that is reachable — a reachable workspace is always swept and judged on its rows. Alternatively, remove the retired workspace's registry row.
  - **`WORKSPACE_RESOLVER=platform-binding` has no registry to enumerate**, so the command cannot verify it; workspaces are swept only when a process builds them. See *Verifying every workspace* in the runbooks below.

The sweep re-seals every row that is not yet in the mode's envelope under the primary key: plaintext rows, `enc:v1` rows, rows under a previous key. It never overwrites a value that changed after it read it (compare-and-swap; a concurrent `rotate-secret` wins), does not bump `updated_at`, and runs under a 10-second limit so a held row lock cannot stall startup. It logs a summary (`subscription secrets swept`, with every counter), one `Info` record per converted row (`subscription secret converted`: `subscription_id`, `from`, `from_key_id`) and one `Error` record per refused row. No log line, report or error ever contains a secret or a stored value.

**The sweep is the only path that turns a legacy value into `enc:v2`.** A write that keeps a subscription's secret — `PATCH`, `pause`, `activate`, the internal bootstrap realigning a target URL — writes the other columns and leaves the stored secret's bytes untouched (judged against the secret the caller read, so a `rotate-secret` landing in between is not undone); a write that changes the secret (`rotate-secret`, a bootstrap token change) replaces the stored value outright. So every conversion of a plaintext or `enc:v1` value appears in the sweep's per-row log.

A row that does not open — sealed for another workspace or row, under a key no longer in the ring, a legacy value in `strict`, an unknown version — is **refused and left untouched**. It is not skipped: a read of it fails, and because the webhook fan-out lists the workspace's active subscriptions per event type and a list fails as a whole, **one refused active row stops the workspace's webhook delivery for every event type that row subscribes to** until it is fixed: `POST /subscriptions/{id}/rotate-secret` replaces the stored secret **without reading it**, so it works on a refused row (`PATCH`, `pause` and `activate` read the row first and fail until it is rotated); or restore the key, or correct the workspace id; or delete the subscription. The sweep's `Error` log names it. After a clean sweep in `migrate` mode Spine logs a hint to switch to `strict` (at `Warn` when `SPINE_ENV=production`).

**Trust boundary.** In `migrate` mode the sweep trusts the legacy (`enc:v1` and plaintext) rows present when it runs. It cannot tell a genuine pre-upgrade `enc:v1` row from a copy of **another workspace's** pre-upgrade `enc:v1` value planted in this database — `enc:v1` binds nothing — so it converts both into valid `enc:v2` rows for this workspace. This lasts for as long as `migrate` is active: every sweep converts whatever legacy row it finds.

**The conversion log is the audit trail.** Its *baseline* is a workspace's first clean sweep in `migrate` (or `strict`) mode after the last period in which **anything wrote `enc:v1` or plaintext legitimately**: a replica in `downgrade` mode (which writes and sweeps to `enc:v1` on purpose — the several-replica upgrade, a rollback preparation, the workspace-id runbook), a binary older than this release (a rollback, an overlapping rollout), or a deployment without a key. Each such period resets the baseline; the first clean `migrate` sweep after it is the new reference point, and the `from=enc:v1` conversions that sweep logs are expected (a value planted *during* such a period is indistinguishable from them, so keep those periods short). A `from=plaintext` or `from=enc:v1` conversion logged **after** the baseline was not written by this release in its normal posture: treat it as a possible planted copy and rotate that subscription's signing secret (`POST /subscriptions/{id}/rotate-secret`).

### When P3-10 Is Closed {#when-p3-10-is-closed}

The default `migrate` mode does **not** close audit finding P3-10 on its own. An attacker who holds a pre-upgrade backup of workspace A (an `enc:v1` copy of its column) and write access to workspace B's database can still plant A's value in B, and the next `migrate` sweep accepts it. Either of these closes the path **for values planted from then on**:

1. `SPINE_SECRET_ENVELOPE_MODE=strict` is set — no plaintext or `enc:v1` value is accepted again; **or**
2. every key that ever sealed an `enc:v1` value has been removed from the ring (see *Key rotation*) — such values then no longer open.

Neither undoes a plant that a `migrate` sweep already converted: that row is a valid `enc:v2` row for B and stays valid under `strict` and after a key rotation (the rotation re-keys it like any other row). So both closing steps are preceded by the conversion-log review above, with `rotate-secret` on every subscription it flags. P3-10 is closed once one of the two steps is in place **and** that review has been done.

### Deployment Assumptions

- **One workspace per runtime database** (ADR-012). The registry does not enforce a unique `database_url`. If two workspaces are registered on one runtime database, each refuses the other's rows once bound, so both workspaces' webhook delivery fails with refused rows. That is intended: such a topology already fans each workspace's events out to the other's subscriptions. Give every workspace its own database.
- **The binding is only as unique as the workspace id.** In file mode the id defaults to `default`. Two single-workspace deployments that share one key and both use `default` can transplant `enc:v2` values between them. Use a **distinct key per deployment**, or else a distinct `SPINE_WORKSPACE_ID`.
- **Changing the workspace id** makes its `enc:v2` rows fail closed (the id is part of the binding). See the runbook below.
- **No binary older than this release may run alongside it.** An older binary reads an `enc:v2` value as plaintext and signs with the envelope text; the receiver rejects the delivery (typically `401`), the dispatcher treats a 4xx other than `429` as a permanent failure, and the event is lost. This includes the overlap of a rolling update with one replica — see the upgrade runbooks.

### Runbooks

Every `spine secrets reencrypt` run below uses the same `SPINE_SECRET_ENCRYPTION_KEY`, `SPINE_SECRET_PREVIOUS_KEYS` and **`SPINE_SECRET_ENVELOPE_MODE`** as `spine serve`, and in file mode the same **`SPINE_WORKSPACE_ID`** (the command refuses to run with the mode unset, or in file mode with the workspace id unset and no `--workspace`).

**Verifying every workspace** (used by the steps below). Verify only once the rollout of the change being verified has **completed** — no process with the previous setting remains. While processes with the old and new setting overlap, an old-setting process that first loads a workspace after a new-setting process swept it sweeps it back: to `enc:v1` (a `downgrade` process during the switch to `migrate`), to the old primary key (a step-2 process during a key promotion), or to `enc:v2` (a `migrate` process during a rollback's switch to `downgrade`). Nothing fails while both settings run, but nothing sweeps that workspace again until some process rebuilds it.

- File mode: `spine secrets reencrypt` exits 0. The command sweeps the workspace itself, so its result is the current state.
- `WORKSPACE_RESOLVER=db`: `spine secrets reencrypt --all-workspaces` exits 0 (acknowledge decommissioned inactive workspaces with `--allow-unreachable-inactive`). The command sweeps every workspace itself.
- `WORKSPACE_RESOLVER=platform-binding`: there is no registry to enumerate, and a request to a workspace a process already holds does not sweep it, so a clean record logged during the rollout does not show the current state.
  1. If processes with the old and new setting ran at the same time (any rolling update), **restart every process** once the rollout has completed, so each one rebuilds, and so sweeps, every workspace it next loads. Skip this only after a stop-the-world restart (one replica with Kubernetes `strategy: Recreate`).
  2. Load **every** bound workspace (one request each).
  3. For each workspace, take the **latest** `subscription secrets swept` record across **all** replicas. It must come from a process started after the rollout completed, and show `refused=0` and `conflicts=0` (and, where the step asks, the expected `mode`, `primary_key_id` and `current` count). A workspace without such a record is **not** verified.

**Upgrade (one replica).** This procedure requires that the old and new process **never run at the same time**: stop the old process before the new one starts (Kubernetes `strategy: {type: Recreate}`; the default `RollingUpdate` starts the new pod while the old one still dispatches). If any overlap is possible, use the several-replica procedure instead.

1. Deploy. The default mode is `migrate`.
2. Each store build sweeps its workspace: plaintext and `enc:v1` rows become `enc:v2`.
3. Before switching to `strict`:
   - Verify every workspace (above) with `SPINE_SECRET_ENVELOPE_MODE=migrate`.
   - Review the per-row conversion log against its baseline (see *The conversion log is the audit trail*). For every `from=plaintext` or `from=enc:v1` conversion logged after a workspace's baseline, run `POST /subscriptions/{id}/rotate-secret` before switching: its value may be a planted copy the sweep converted.
4. Set `SPINE_SECRET_ENVELOPE_MODE=strict` and restart. From then on an `enc:v1` value copied from a pre-upgrade backup, or a plaintext value planted in the database, is refused.

**Upgrade (several replicas, or any rollout where old and new processes overlap).** A binary older than this release reads an `enc:v2` value as plaintext and would sign with the envelope text, so no old replica may see one.

1. Deploy every replica with `SPINE_SECRET_ENVELOPE_MODE=downgrade`: all can read `enc:v2`, none writes it.
2. Once no old replica remains, switch to `migrate`. While this rollout overlaps `downgrade` replicas, a `downgrade` replica may sweep a workspace back to `enc:v1` (expected; nothing fails). Each workspace's conversion-log baseline is therefore its first clean `migrate` sweep **after the last `downgrade` replica stopped**: it converts the `enc:v1` rows `downgrade` wrote, as expected. In `platform-binding` mode that is the sweep that follows the restart in *Verifying every workspace*.
3. Later, switch to `strict` as in step 3 of the one-replica procedure.

**Key rotation.** Every process that reads a workspace must hold every key its rows may be sealed under. A process whose ring lacks K2 refuses a row sealed under K2 (`ErrUnknownKeyID`), and the webhook fan-out then fails for that workspace: events are not enqueued, and queued deliveries cannot read their subscription, so the dispatcher retries them on the backoff schedule and then every minute (recorded as `resolve subscription: temporarily unavailable`) and dead-letters them once held 24 hours (§6.1, "Retry and failure handling"). The first process that runs with K2 as primary re-keys rows to K2 as soon as it opens a workspace, so **K2 must be in every process's ring before any process makes it primary**.

1. Generate K2 (`openssl rand -base64 32`).
2. **Distribute K2 (read-only).** Set `SPINE_SECRET_PREVIOUS_KEYS=K2` (K1 stays `SPINE_SECRET_ENCRYPTION_KEY`) and roll it out to **every** process, including any job that runs `spine secrets reencrypt`. Nothing is re-keyed yet. Skip this step only if old and new processes never run at the same time (a stop-the-world restart; with one replica, Kubernetes `strategy: Recreate`).
3. **Promote K2.** Once no process without K2 remains, set `SPINE_SECRET_ENCRYPTION_KEY=K2` and `SPINE_SECRET_PREVIOUS_KEYS=K1`, and roll it out. A sweep by a K2-primary process re-keys the workspace's rows to K2 (in shared mode a process sweeps a workspace only when it builds it). While this rollout overlaps step 2's processes, a step-2 process that first loads a workspace after a K2-primary process swept it re-keys its rows back to K1; both rings read both keys, so nothing fails. The rows reach K2 **only when a K2-primary process sweeps the workspace again after the last step-2 process has stopped** — completing the rollout does not do this by itself. Step 4 does: in file and `db` mode the command re-sweeps every workspace; in `platform-binding` mode the restart in *Verifying every workspace* makes every process rebuild every workspace.
4. Once step 3's rollout has completed, verify every workspace (above): every workspace clean under `primary_key_id=kid(K2)`. Do not continue to step 6 until it is.
5. If K1 ever sealed `enc:v1` values and the deployment is not yet `strict`, review the conversion log against its baseline and `rotate-secret` every flagged subscription **before** dropping K1 (see *When P3-10 Is Closed*): a plant converted earlier was re-keyed to K2 in step 3 and survives the drop.
6. Remove K1 from `SPINE_SECRET_PREVIOUS_KEYS` and roll it out. This also makes every `enc:v1` value written under K1 unopenable — the second way to close P3-10 for future plants.
7. Keep K1 in escrow for as long as a backup taken before step 3 may be restored. To restore such a backup, put K1 back in the ring and run `migrate` until the sweep is clean.
8. Rotating a subscription's *webhook signing secret* is a separate operation: `POST /subscriptions/{id}/rotate-secret`.

**Rollback below this release.**

1. Set `SPINE_SECRET_ENVELOPE_MODE=downgrade` and restart. If the deployment runs `strict` and old and new processes can overlap, first roll out `migrate` everywhere: a `strict` process refuses the `enc:v1` rows a `downgrade` process writes and sweeps to. While the `downgrade` rollout overlaps `migrate` processes, a `migrate` process may sweep a workspace back to `enc:v2`.
2. Once step 1's rollout has completed, verify every workspace (above) with `SPINE_SECRET_ENVELOPE_MODE=downgrade`: every workspace reports `current` equal to its number of non-empty rows, and `refused=0`. A row left in `enc:v2` is what the old binary would sign with as text.
3. Roll back the binary.

Without this procedure, an old binary signs every webhook with the literal `enc:v2:…` text and receivers reject it. The standalone internal subscription heals on the old binary's bootstrap; API-created subscriptions need `rotate-secret`. On a later re-upgrade the `downgrade` period resets the conversion-log baseline.

**Rollback that skipped `downgrade`.** Rows the old binary wrote back become `enc:v1(enc:v2:…)`. On re-upgrade, a row whose inner value is that row's own `enc:v2` is repaired (read correctly, and rewritten by the sweep — counted as `repaired`). A row that cannot be repaired is refused and needs `rotate-secret`.

**Changing the workspace id** (`SPINE_WORKSPACE_ID` in file mode; a re-registered workspace in shared mode).

1. Run in `downgrade` under the old id, and verify every workspace (above) with `SPINE_SECRET_ENVELOPE_MODE=downgrade` under the old id.
2. Change the id.
3. Switch back to `migrate`. The first clean `migrate` sweep under the new id is the new conversion-log baseline.

### Webhook TLS Configuration

Per-subscription TLS config lives in the `metadata` JSONB column of `event_subscriptions` under the `tls` key. Set it when creating/updating a subscription to:

- **Pin the server's SPKI hash**: `{"tls":{"pinned_spki_sha256":"<lowercase hex>"}}` — the hex-encoded SHA-256 of the leaf certificate's DER Subject Public Key Info. The webhook dispatcher rejects any certificate whose SPKI hash does not match. A value that is not hex is refused with `400 invalid_params`.
- **Supply a custom CA bundle** (for internal CAs / private networks): `{"tls":{"ca_bundle_pem":"-----BEGIN CERTIFICATE-----..."}}`. A bundle that holds no certificate is refused with `400 invalid_params`.

Both fields are optional; when neither is set, the dispatcher uses the system trust store. See `adapters/delivery/tls.go` for the full validator.

---

## 3. Workspace Management API

All workspace endpoints require the operator token (`SPINE_OPERATOR_TOKEN`) as a Bearer token in the `Authorization` header.

Base path: `/api/v1`

### Create Workspace

```
POST /workspaces
Authorization: Bearer <operator-token>
Content-Type: application/json

{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "git_url": "https://github.com/acme/spine-repo.git",
  "smp_workspace_id": "ws-acme-prod-001"
}
```

Response (201):
```json
{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "status": "inactive",
  "message": "workspace created \u2014 run provisioning to activate"
}
```

The workspace is created as **inactive**. It must be provisioned (database created, repo cloned) before it can serve traffic.

### List Workspaces

```
GET /workspaces
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspaces": [
    {
      "workspace_id": "acme-prod",
      "display_name": "Acme Production",
      "status": "active"
    }
  ]
}
```

### Get Workspace

```
GET /workspaces/{workspace_id}
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspace_id": "acme-prod",
  "display_name": "Acme Production",
  "status": "active",
  "repo_path": "/var/spine/repos/acme-prod",
  "database_host": "spine-db:5432/spine_ws_acme_prod"
}
```

### Deactivate Workspace

```
POST /workspaces/{workspace_id}/deactivate
Authorization: Bearer <operator-token>
```

Response (200):
```json
{
  "workspace_id": "acme-prod",
  "status": "inactive"
}
```

Deactivation immediately stops serving requests for this workspace and invalidates caches.

### Process Metrics

```
GET /platform/metrics
Authorization: Bearer <operator-token>
```

Returns the full Prometheus export of the process: every workspace's `spine_workspace_pool_*` series plus the process-global counters, gauges and histograms. It answers `401` for a missing or wrong operator token and `503` when `SPINE_OPERATOR_TOKEN` is unset.

`GET /system/metrics` (workspace-scoped, `system.metrics`, operator role) returns the full export only in single-workspace mode. In shared multi-workspace mode (`WORKSPACE_RESOLVER=db` or `platform-binding`) it returns only the caller's own workspace `spine_workspace_pool_*` series: the full export names every tenant and aggregates every tenant's activity (INIT-023 TASK-015). Point a shared-mode scraper at `/platform/metrics`.

---

## 4. Actor and Auth Integration

### Token-Based Authentication

All API requests (except health/metrics and workspace management) require a bearer token.

**Create a token:**
```
POST /api/v1/tokens
Authorization: Bearer <existing-token-with-token.create>
Content-Type: application/json

{
  "actor_id": "actor-001",
  "name": "CI Integration",
  "expires_in": "720h"
}
```

Response:
```json
{
  "token_id": "tok-abc123",
  "token": "spine_<plaintext-token>",
  "expires_at": "2026-05-10T20:00:00Z"
}
```

The plaintext token is shown **once** at creation. Store it securely.

**Use the token:**
```
GET /api/v1/artifacts
Authorization: Bearer spine_<plaintext-token>
```

**Workspace-scoped requests** (shared mode): Include `X-Workspace-ID` header:
```
GET /api/v1/artifacts
Authorization: Bearer spine_<plaintext-token>
X-Workspace-ID: acme-prod
```

### Actor Model

Actors represent entities (humans, AI agents, automated systems) that interact with Spine.

**Roles:** `reader`, `contributor`, `reviewer`, `operator`, `admin`

Each role grants specific permissions:
- `reader` -- read artifacts, query state
- `contributor` -- create/update artifacts, start runs, submit results
- `reviewer` -- all contributor permissions + accept/reject tasks
- `operator` -- all reviewer permissions + system operations, run cancellation, manual step assignment, and registering actors with a role strictly below its own (reader, contributor, reviewer)
- `admin` -- all permissions, including token management, operator and admin actors, the skill catalogue and actor skill grants

**Skills:** Actors can have skills assigned (e.g., "go-development", "code-review"). Workflows can require specific skills for step assignment, enabling skill-based routing of work. Because skills decide who may claim review and approval steps, managing them is admin-only: creating, updating, renaming or deprecating a skill (`POST`/`PATCH /api/v1/skills…`, `POST /api/v1/skills/{id}/deprecate`) and granting or removing an actor's skill (`POST`/`DELETE /api/v1/actors/{actor_id}/skills/{skill_id}`). A non-admin actor can never change its own skills (403 with `reason=self_skill_change_forbidden`). Renaming or deprecating a skill that an active workflow requires answers 409 unless `?force=true` is passed. An integration that let agents register their own skills must move that to an admin token.

---

## 5. Projection Database Access

Spine maintains a projection database that mirrors Git state into PostgreSQL tables for efficient querying. The management platform can connect to these tables read-only.

### Key Tables

**`projection.artifacts`** -- Cached artifact state
```sql
SELECT artifact_path, artifact_id, artifact_type, title, status, metadata, source_commit
FROM projection.artifacts
WHERE artifact_type = 'Task' AND status = 'Pending';
```

**`projection.workflows`** -- Parsed workflow definitions
```sql
SELECT workflow_path, workflow_id, name, version, definition
FROM projection.workflows
WHERE status = 'active';
```

**`projection.sync_state`** -- Sync cursor and status
```sql
SELECT last_synced_commit, last_synced_at, status
FROM projection.sync_state
WHERE id = 'global';
```

### Runtime Tables

**`runtime.runs`** -- Workflow execution state
```sql
SELECT run_id, task_path, status, current_step_id, started_at, completed_at
FROM runtime.runs
WHERE status = 'active';
```

**`runtime.step_executions`** -- Step-level execution state
```sql
SELECT execution_id, run_id, step_id, actor_id, status, outcome_id
FROM runtime.step_executions
WHERE run_id = 'run-abc123';
```

### Connection Configuration

The database URL is the same as `SPINE_DATABASE_URL`. For read-only access, create a separate PostgreSQL role with SELECT-only permissions.

---

## 6. Event Integration

Three push/pull mechanisms cover different integration styles. All three emit the same canonical event schema defined in [`architecture/event-schemas.md`](../architecture/event-schemas.md).

Enable the delivery system by setting `SPINE_EVENT_DELIVERY=true`. Without that flag, only projection polling (§6.4) is available.

### 6.1 Webhook Subscriptions

HTTP POST delivery to operator-configured URLs with HMAC-signed payloads, exponential-backoff retries, and per-subscription circuit breaking.

**Subscription CRUD** — all routes require a bearer token with subscription-management permissions:

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/v1/subscriptions` | Create subscription |
| `GET` | `/api/v1/subscriptions` | List subscriptions |
| `GET` | `/api/v1/subscriptions/{id}` | Get one |
| `PATCH` | `/api/v1/subscriptions/{id}` | Update (partial) |
| `DELETE` | `/api/v1/subscriptions/{id}` | Delete |
| `POST` | `/api/v1/subscriptions/{id}/activate` | Status → `active` |
| `POST` | `/api/v1/subscriptions/{id}/pause` | Status → `paused` |
| `POST` | `/api/v1/subscriptions/{id}/rotate-secret` | New signing secret (returned once) |
| `POST` | `/api/v1/subscriptions/{id}/test` | Send a ping event synchronously (the ping is not signed) |
| `GET`  | `/api/v1/subscriptions/{id}/deliveries` | List delivery attempts |
| `GET`  | `/api/v1/subscriptions/{id}/deliveries/{delivery_id}` | Delivery detail |
| `POST` | `/api/v1/subscriptions/{id}/deliveries/{delivery_id}/replay` | Re-queue a failed delivery |
| `GET`  | `/api/v1/subscriptions/{id}/stats` | Aggregate counts |

**Create body**:

```json
{
  "name": "smp-bridge",
  "target_url": "https://smp.example.com/spine/events",
  "event_types": ["step_assigned", "step_completed", "run_failed"],
  "metadata": {
    "signature_version": "v2",
    "tls": {
      "pinned_spki_sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
    }
  }
}
```

`event_types` empty/missing = receive all events. The create response returns the generated `signing_secret` exactly once (`whsec_` prefix + 64 hex chars); store it securely — subsequent reads never include it again. Use `POST /rotate-secret` to replace it.

**Metadata.** `metadata` is a JSON object. For compatibility with earlier clients, a JSON string holding base64 of the object is also accepted; both forms store the same value. `POST` and `PATCH` validate it as the dispatcher will read it — an unknown or non-string `signature_version` (key spelled exactly, lowercase), a non-hex `tls.pinned_spki_sha256`, a `tls.ca_bundle_pem` without a certificate, or a key given in more than one letter case (`tls` and `TLS`, or two spellings of a field inside `tls`) is `400 invalid_params`. `PATCH` replaces `metadata` as a whole. Subscription responses include `"signature_version"`: `"v1"`, `"v2"`, or `"invalid"` when the stored metadata would not pass that validation — the dispatcher then dead-letters every delivery of the subscription.

**Outbound webhook request** (sent by Spine to `target_url`):

- `POST <target_url>`
- `Content-Type: application/json`
- `X-Spine-Event: <event_type>`
- `X-Spine-Delivery: <delivery_id>` — stable across retries of one delivery; receivers may deduplicate on it.
- The signature headers of the subscription's signature version (below).
- Body: the canonical event object (see [event-schemas.md](../architecture/event-schemas.md)) — `{event_id, type, timestamp, actor_id?, run_id?, artifact_path?, trace_id?, payload?}`.

#### Signature versions

A subscription selects its signature format with `metadata.signature_version`. Absent, `null`, `""` or `"v1"` means **v1**, the default; `"v2"` means **v2**. Any other value is refused by the API, and a stored row with such a value has its deliveries dead-lettered — never sent unsigned and never downgraded.

| | v1 (default) | v2 (recommended) |
|---|---|---|
| `X-Spine-Signature` | `sha256=<hex>` | `v2=<hex>` |
| `X-Webhook-Timestamp` | — | decimal Unix seconds at send time, e.g. `1700000000` |
| `X-Webhook-Nonce` | — | 32 lowercase hex characters (16 random bytes) |
| MAC input (HMAC-SHA256, key = the `signing_secret` string's bytes) | the raw body | `timestamp + "." + nonce + "." + raw body`, using the exact header strings |
| Without a `signing_secret` | sent unsigned | not sent: the delivery is dead-lettered |

v1 signs only the body, so a captured request verifies forever: anyone who once sees a signed delivery (a TLS-terminating proxy log, a request-logging middleware, a mirrored receiver) can replay it. v2 binds a send-time timestamp and a single-use nonce into the MAC. Each attempt — every retry, an operator replay of a dead delivery — is signed afresh with a new timestamp and nonce; nothing about the signature is stored. A v2 request carries only the v2 MAC. v2 is the format the Spine Management Platform's step-event receiver verifies.

Opt in by creating or updating the subscription with `"metadata": {"signature_version": "v2"}` (plus any other keys you keep, since `PATCH` replaces `metadata` as a whole).

Golden vector: secret `whsec_test`, timestamp `1700000000`, nonce `000102030405060708090a0b0c0d0e0f`, body `{"event_id":"evt-1"}` → `v2=7259d9ee00d6dfc7d6cd0be62fac13dada610df1455f5bf2b23866fa5949b288` (v1 of the same body: `sha256=b5fb135192a36cba13b173dac6aadcc588b98253b1f45e016139e5208503c61e`).

**Receiver checklist (v2):**

1. Require both `X-Webhook-Timestamp` and `X-Webhook-Nonce`. When they are present, accept **only** a v2 MAC — never fall back to the body-only v1 MAC, or a captured v1 signature replays with fresh fake headers.
2. Parse the timestamp as decimal seconds and refuse it outside ±5 minutes of your clock (keep receiver clocks NTP-synchronised).
3. Hex-decode the nonce (refuse anything shorter than 16 bytes) and refuse a nonce you have already accepted within the window; record it only after the MAC verifies.
4. Compute the MAC over the raw header strings and the raw body bytes, and compare with `hmac.Equal` (constant time).
5. Use a distinct `signing_secret` per subscription: v2 binds neither the URL nor the subscription, so a shared secret lets a request captured at one endpoint replay at another inside the window.

**Retry and failure handling**:

- Retryable responses: 5xx, network errors, `408`, `425` and `429` (`429` honours `Retry-After`).
- Non-retryable: any other 4xx → the delivery goes `dead` on that attempt.
- Backoff schedule: `1s, 2s, 4s, 8s, 16s` (exponential, capped at 16s).
- Attempt cap: **5** for domain events (`artifact_*`, `run_*`, `step_*`), **2** for operational events. A delivery that exhausts it goes `dead`.
- Deliveries that can never succeed go `dead` at once instead of being retried: the subscription was deleted, its `metadata` is invalid, its `target_url` is refused by the URL validator, or its TLS or signing configuration cannot be used. The first such delivery takes every other delivery of the subscription that is due with it — anything queued and not scheduled for a later retry — in the same step, each with a delivery-log row giving the reason; replay them once the subscription is fixed. A refusal of a host name is based on what it resolves to at that moment, so a DNS answer that points at a non-public address even briefly dead-letters the subscription's due deliveries.
- A transient failure to read the subscription (database or decryption — for example a server started without its encryption key) is not the endpoint's fault. The delivery is retried on the backoff schedule and then every minute, recorded as `resolve subscription: temporarily unavailable`, and delivered as soon as the subscription can be read again; it goes `dead` only after the 24-hour hold limit below, counted from the first failed read of the current run of failures. These retries count toward its attempt cap, so once the subscription is readable a failed delivery dead-letters it. Its other queued deliveries wait for the same retry without spending attempts.
- `dead` is terminal: retention deletes it after the retention window, and `POST …/deliveries/{delivery_id}/replay` re-queues it. Every `dead` delivery has a delivery-log row that says why.
- Error text in `last_error`, the delivery log and the subscription-test response never contains the path, query or fragment of `target_url` (`Post "https://hooks.example/[redacted]": …`), a resolved IP address, Spine's own source address, or a DNS resolver's address. Connection failures name the target's own host and port, both when connecting (`target_url: connect hooks.example:443: …`) and after (`read tcp hooks.example:443: read: connection reset by peer`). An HTTP-status outcome is recorded as exactly `HTTP <code>`.

**Per-subscription circuit breaker** — opens after **10** consecutive failures, stays open for **60s**, then half-opens to probe with a single delivery. A success closes it; a probe failure re-opens it. A probe that ends without reaching the endpoint (the subscription could not be read, the URL was refused) returns the circuit to open, so the next poll probes again.

While the circuit is open, queued deliveries for that subscription are **held**: the first one the dispatcher picks up defers the subscription's whole queue to the circuit's next probe time, without consuming any attempt, so the deliveries are sent once the endpoint recovers. Two edges:

- A delivery held longer than **24 hours** goes `dead` (`circuit open: held longer than 24h0m0s`, or `resolve subscription: temporarily unavailable: held longer than 24h0m0s` for an unreadable subscription) without an attempt. The hold is counted from when the circuit opened (or, for an unreadable subscription, from the first failed read of the current run of failures), or from when the delivery was queued if that is later: a backlog queued long before an outage — after Spine itself was down for a day, say — is held and delivered, not dead-lettered as the outage begins. The dispatcher keeps the start of a hold in memory, so a restart starts the 24 hours again. A delivery older than 24 hours that is replayed during an outage that has itself lasted more than 24 hours is dead-lettered again; replay after the endpoint has recovered. The limit is `DispatcherConfig.MaxCircuitHold` for embedders.
- Probes are real attempts: during a long outage the oldest held delivery is the probe each cycle and spends one of its attempts per failed probe, so some deliveries reach their attempt cap and go `dead`.

**Claim order.** The dispatcher claims due deliveries round-robin across subscriptions (oldest first within each subscription). A subscription it cannot deliver to for a while (open circuit, unreadable subscription) has its whole queue held until its next attempt, so it takes about two claim slots per probe cycle however many deliveries it has queued; one whose deliveries can never succeed (above) has its whole due queue dead-lettered by the first of them, so it takes one claim slot. A healthy subscription's delivery therefore waits at most one poll while fewer than five subscriptions (the dispatcher's concurrency) are held, and about `2 × held subscriptions ÷ 5` polls at a probe instant otherwise. With several Spine replicas on one database the circuit breaker is per replica, and a replica whose circuit is open holds the subscription's queue for every replica until its probe time.

**Target URL validation.** `target_url` must be `https` (or `http` for a host in `SPINE_WEBHOOK_ALLOWED_HOSTS`) and must not reach loopback, link-local (including cloud metadata), private, CGNAT, benchmarking, multicast, unspecified or `0.0.0.0/8` addresses — checked at create/update/test and again at every connect. IPv6 transition addresses are judged by the IPv4 address they embed (NAT64 `64:ff9b::/96`, 6to4 `2002::/16`, IPv4-compatible `::/96`, SIIT `::ffff:0:0:0/96`); RFC 8215 local-use NAT64, Teredo and site-local `fec0::/10` are refused outright. A host name that resolves to a refused address is rejected with a generic message that names neither the address nor its class. A NAT64 deployment that uses an operator-chosen network-specific prefix cannot be recognised from the address alone: operators running DNS64/NAT64 with such a prefix must rely on network egress policy.

### 6.2 SSE Event Stream

Long-lived Server-Sent Events for dashboards and IDE integrations.

```
GET /api/v1/events/stream?types=step_assigned,step_completed
Authorization: Bearer <token>
Last-Event-ID: <optional replay cursor>
```

- Auth: caller needs the `events.read` permission.
- `types=`: comma-separated filter; omit for all events.
- `Last-Event-ID`: on reconnect, replays up to the last 100 missed events after that cursor.
- Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`.
- Heartbeat: `: keepalive` comment every 30s. Per-event write deadline is 5s — slow clients get dropped rather than back-pressuring the emitter.
- Per-actor concurrency cap: default 5, tune via `SPINE_SSE_MAX_CONN_PER_ACTOR` (≤0 disables the cap — not recommended). In shared multi-workspace mode the cap is counted per (workspace, actor): the same actor ID in another workspace (every workspace has its own `smp-admin`) has its own slots.
- Frame safety (INIT-023 TASK-015). Event IDs embed workflow identifiers (step, divergence-point and branch IDs). An event whose ID contains a control character, invalid UTF-8, U+0085, U+2028 or U+2029 is sent **without its `id:` line** (an unsafe event type likewise loses its `event:` line); its JSON `data` still carries the true `event_id`. The client's last event ID therefore stays at the previous event, and a reconnect replays from there (at-least-once). `data:` lines never contain a raw line break, and U+0085, U+2028 and U+2029 inside JSON strings are written as `\u0085`, `\u2028` and `\u2029`, which decode to the same value. See `KNOWN-LIMITATIONS.md` for the replay residual.

### 6.3 Pull-Based Event Log

Stateless cursor-paginated API for consumers that prefer polling over long-lived connections.

```
GET /api/v1/events?after=<cursor>&types=step_assigned,run_failed&limit=100
Authorization: Bearer <token>
```

| Query | Default | Notes |
|-------|---------|-------|
| `after` | beginning of log | Cursor returned by a prior response |
| `types` | all | Comma-separated event types |
| `limit` | 50 | 1–1000 |

Response:

```json
{
  "events": [ { "event_id": "...", "type": "step_assigned", ... } ],
  "next_cursor": "ev-0000001234",
  "has_more": true
}
```

Resume by echoing `next_cursor` as the next `after` value. The log is retention-capped server-side — schedule consumers to poll frequently enough to stay within the window.

### 6.4 Projection Polling (fallback)

If webhook/SSE/pull are unavailable, detect changes by polling `projection.sync_state`:

```sql
SELECT last_synced_commit, last_synced_at
FROM projection.sync_state
WHERE id = 'global';
```

Compare `last_synced_commit` with your last-seen value; re-query tables on change. Recommended cadence: 5–10 s during activity, back off to 30 s after ten idle polls, reset on change.

---

## 7. Git HTTP Serve Endpoint

Spine ships a git smart-HTTP server so runner containers can clone workspace repositories directly — no SSH keys, no external git hosting, no additional credentials. Push (`git-receive-pack`) is available but off by default; see below.

### Route Patterns

- **Shared mode**: `GET /git/{workspace_id}/info/refs?service=git-upload-pack`, `POST /git/{workspace_id}/git-upload-pack`
- **Single mode**: `GET /git/info/refs?service=git-upload-pack`, `POST /git/git-upload-pack` (workspace is implicit)
- Dumb protocol objects (`HEAD`, `objects/*`) are also served via GET

### Push (`git-receive-pack`)

The endpoint ships with push **disabled** (`http.receivepack=false` pinned in the repo config). Any `git push` attempt returns **403** with a message naming the flag.

Set `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (accepted values: `1`, `true`, `yes`, `on`) to reach the push endpoint. Flipping this on is a deliberate opt-in — an existing deployment that upgrades past this change keeps the read-only behaviour it had before.

Auth on push is **stricter** than on clone/fetch: the trusted-CIDR bypass does not apply to `git-receive-pack`. Every push must carry `Authorization: Bearer <token>` so an actor identity is pinned for audit and the pre-receive branch-protection check — a runner trusted for token-less cloning still needs a token to push.

**Branch-protection pre-receive enforcement.** When the flag is on, every push is intercepted at the HTTP layer before `git-http-backend` writes any ref: the command section of the request body is parsed into `(old, new, ref)` triples, each triple is classified (`delete` if `new == 0000...`, else `direct-write`), and each is evaluated against the projection-backed `branchprotect.Policy`. Any Deny rejects the **entire** push (pre-receive semantics — no partial application), and the client sees the rejection as `remote: branch-protection: <rule> denies <branch>` lines plus a per-ref `ng <ref> pre-receive hook declined`. Before the policy runs, every ref update is also admitted by **ref class** (INIT-023 TASK-016), whether or not a policy is wired: tags, notes and other non-branch refs, `spine/init` and the rest of the reserved `spine/**` namespace are operator-only; a run, planning or divergence branch can be pushed only by the actor bound to its current step (or an operator) and deleted only by an operator. Refs under `spine/*` still skip the *policy* — they are out of scope for user-authored rules (ADR-009 §3) — but not this admission. Refusals read `remote: ref-admission: …` (see `docs/git-push-guide.md` §5).

**Operator override on push.** The Git-path equivalent of `write_context.override` is `git push -o spine.override=true`. Operators (role `operator+`) bypass a matching rule for that single push; contributors who set the same option are rejected with a distinct "override not authorised" message, not silently accepted. Every honored override emits a `branch_protection.override` governance event with the full ADR-009 §4 payload, including a `pre_receive_ref` block carrying the `(old_sha, new_sha, ref)` the client tried to push — the Git path does **not** rewrite client-produced commits to add a trailer, so the event is the sole record on this path. Unused overrides (flag set on a push that did not need it) emit nothing.

**Upgrading past ref-class admission (INIT-023 TASK-016).** Admission refuses *new* shadow refs; refs pushed before the upgrade persist. On each workspace repository, list the non-branch refs and the doubly-qualified or `HEAD`-named refs:

```bash
git -C <repo> for-each-ref --format='%(refname)' | grep -v '^refs/heads/'
git -C <repo> for-each-ref --format='%(refname)' refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs
git -C <repo> for-each-ref --format='%(refname)' | grep -E '(^|/)HEAD(/|$)'
```

Delete every tag or other non-branch ref whose short name equals a branch name (for example `refs/tags/main` or `refs/tags/spine/run/…`), and every ref the second and third commands print: a remote-side `refs/heads/refs/heads/<b>`, `refs/tags/refs/heads/<b>` or `refs/refs/heads/<b>` makes every client push of `<b>` fail with `dst refspec … matches more than one` today. An operator can delete them over the push endpoint (`git push origin --delete <ref>`), or with `git -C <repo> update-ref -d <ref>` on the server. INIT-023 TASK-002 makes the client-side bare-name lookups shadow-proof as well.

### Authentication

A **push always presents a bearer** (`Authorization: Bearer <token>`), validated and attached as the actor for ref admission, branch protection and audit; no setting lets a push skip it. A clone or fetch is accepted on one of three paths:

1. **Bearer token** — `Authorization: Bearer <token>` validated against the workspace's actor tokens (or the server-level operator token).
2. **Trusted-CIDR bypass (direct peers only)** — when the TCP peer is inside `SPINE_GIT_HTTP_TRUSTED_CIDRS` **and connected directly**, no bearer is required. Set this only to the runners' own addresses. Unset (the default) means every caller needs a token. Since INIT-023 TASK-015 a proxied request is never token-less:
   - a request whose peer is a declared reverse proxy (`SPINE_TRUSTED_PROXY_CIDRS`) needs a token, and no `X-Forwarded-For` address is trusted for git access (a proxy that passes the client's header through would let the client name a runner address); `spine serve` logs a startup warning for any overlap between the two lists, because token-less clones from the overlap stop working;
   - a request that carries `X-Forwarded-For`, `Forwarded`, `X-Real-IP`, `X-Forwarded-Host`, `X-Forwarded-Proto` or `Via` came through some proxy and needs a token too (a rate-limited warning names `SPINE_TRUSTED_PROXY_CIDRS` when such a request comes from a trusted-CIDR address);
   - a TCP-level proxy adds no header and cannot be detected. Never trust a Docker bridge gateway (the `.1` of the network): Docker's userland proxy connects to published ports from it, so trusting it lets any process on the host clone without a token. `docker-compose.override.yaml.example` shows runner-only recipes.
3. **`SPINE_GIT_HTTP_DEV_NO_AUTH=1`** — local development only: clones and fetches need no token. `spine serve` refuses to start with it unless `SPINE_ENV=development`. Pushes still need a bearer.

`SPINE_DEV_MODE` does not affect `/git` (it used to drop git authentication for clone and push).

Example clone from a runner container on the same Docker network. The stock `docker-compose.yaml` trusts no address, so the clone presents a bearer (`git -c` applies the header to this command only; it is not written to the clone's `.git/config`):

```bash
git -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" clone \
  http://spine:8080/git/ws-abc123 \
  --branch spine/run/task-001-implement-x \
  --depth 1 \
  /workspace
```

The same command without the header works only from a runner whose own address is in `SPINE_GIT_HTTP_TRUSTED_CIDRS` and that connects directly (path 2 above; `docker-compose.override.yaml.example` shows the runner-only recipes). Otherwise it fails with `401` "authorization required for external git access".

### Limits

- **Concurrency**: default 5 in-flight pack operations per process; additional requests get `503 Service Unavailable` with `Retry-After: 5`. Configured at construction time (`Config.MaxConcurrent`); not currently an env var.
- **Per-request timeout**: default 30s (`Config.OpTimeout`).
- **Push gate**: when `SPINE_GIT_RECEIVE_PACK_ENABLED` is unset/false, `git-receive-pack` is rejected before hitting CGI.

### Observability

Every clone is logged with source IP, resolved `repoPath`, requested branch, and duration. Long-tail connections are surfaced in slog output for capacity tuning.

---

## 8. Deployment Patterns

### Docker Compose -- Dedicated Mode

```yaml
services:
  spine:
    build: .
    # Bind loopback-only; front with a reverse proxy (see
    # SPINE_TRUSTED_PROXY_CIDRS) if the service must be public.
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      - SPINE_ENV=production
      - SPINE_DATABASE_URL=postgres://spine:spine@spine-db:5432/spine?sslmode=require
      - SPINE_REPO_PATH=/repo
      - SPINE_SERVER_PORT=8080
      - SPINE_LOG_LEVEL=info
      - SPINE_OPERATOR_TOKEN=${SPINE_OPERATOR_TOKEN}      # 32+ chars
      - SPINE_SECRET_ENCRYPTION_KEY=${SECRET_ENC_KEY}     # openssl rand -base64 32
      # Recommended push-auth mode: short-name credential helper.
      # Falls back to SPINE_GIT_PUSH_TOKEN only if no helper is configured.
      - SPINE_GIT_CREDENTIAL_HELPER=store
      - SPINE_GIT_PUSH_ENABLED=true
    volumes:
      - repo:/repo
    depends_on:
      spine-db:
        condition: service_healthy

  spine-db:
    image: postgres:18-bookworm
    environment:
      - POSTGRES_USER=spine
      - POSTGRES_PASSWORD=spine
      - POSTGRES_DB=spine
    volumes:
      - pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spine"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
  repo:
```

### Kubernetes -- Shared Mode with Credential Helper

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spine
spec:
  replicas: 1
  # Recreate stops the old pod before starting the new one. Required for
  # the one-replica secret-envelope upgrade (§2.1 Runbooks): a rolling
  # update overlaps an older binary that cannot read enc:v2 values.
  strategy:
    type: Recreate
  template:
    spec:
      containers:
        - name: spine
          image: spine:latest
          ports:
            - containerPort: 8080
          env:
            - name: SPINE_ENV
              value: "production"
            - name: SPINE_WORKSPACE_MODE
              value: "shared"
            - name: SPINE_REGISTRY_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: registry-db-url  # must use sslmode=require or stronger
            - name: SPINE_OPERATOR_TOKEN
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: operator-token   # 32+ chars
            - name: SPINE_SECRET_ENCRYPTION_KEY
              valueFrom:
                secretKeyRef:
                  name: spine-secrets
                  key: secret-encryption-key  # base64 32 bytes
            - name: SPINE_GIT_CREDENTIAL_HELPER
              value: "store"            # allowlisted helper name; no path/script needed
            - name: SPINE_SERVER_PORT
              value: "8080"
            - name: SPINE_LOG_LEVEL
              value: "info"
          volumeMounts:
            - name: repos
              mountPath: /var/spine/repos
      volumes:
        - name: repos
          persistentVolumeClaim:
            claimName: spine-repos
```

> The helper allowlist (TASK-022) replaced the previous free-form path model. If your platform needs to talk to a remote secret store, run a sidecar that populates the appropriate backing store for `cache`/`store`/`osxkeychain`/`manager`/`pass` — Spine will call that helper by name only.

### Environment Variable Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SPINE_DATABASE_URL` | Yes (single mode) | - | PostgreSQL connection string for workspace database. Must use `sslmode=require` or stronger in production; `sslmode=disable` is refused at startup unless `SPINE_INSECURE_LOCAL=1`. |
| `SPINE_WORKSPACE_MODE` | No | `single` | `single` or `shared` |
| `SPINE_WORKSPACE_ID` | No for `spine serve`; **yes** for `spine secrets reencrypt` in file mode (or `--workspace`) | `default` (serve only) | Workspace ID in single mode. It is part of every `enc:v2` signing secret's associated data, so `spine secrets reencrypt` refuses to assume `default`; set it to serve's value, or pass `--workspace default`. |
| `SPINE_REGISTRY_DATABASE_URL` | Yes (shared mode) | - | PostgreSQL URL for workspace registry. Same TLS requirements as `SPINE_DATABASE_URL`. |
| `SPINE_REPO_PATH` | No | `.` | Path to Git repository |
| `SPINE_SERVER_PORT` | No | `8080` | HTTP server port |
| `SPINE_LOG_LEVEL` | No | `info` | Log level: debug, info, warn, error |
| `SPINE_LOG_FORMAT` | No | `json` | Log format: json or text |
| `SPINE_ENV` | No | - | Runtime environment: `production`, `staging`, `development`. When `production`, `SPINE_DEV_MODE` is refused and `SPINE_SECRET_ENCRYPTION_KEY` is required. |
| `SPINE_DEV_MODE` | No | - | `1` or `true` lets unauthenticated requests through the `/api/v1` auth gate. Dev-only; refused at startup when `SPINE_ENV=production`. Does not affect the `/git` endpoint. |
| `SPINE_GIT_HTTP_DEV_NO_AUTH` | No | - | `1`, `true`, `yes` or `on` lets every client clone/fetch over `/git` without a bearer. Development only: refused at startup unless `SPINE_ENV=development`. Pushes still need a bearer. |
| `SPINE_SECRET_ENCRYPTION_KEY` | Yes (production) | - | Base64-encoded 32-byte AES-256 key for at-rest secret encryption (webhook signing secrets). Generate with `openssl rand -base64 32`. See [Secrets at Rest](#secrets-at-rest). |
| `SPINE_SECRET_PREVIOUS_KEYS` | No | - | Comma-separated base64 32-byte keys that still decrypt values sealed before a key rotation; they never seal. Requires `SPINE_SECRET_ENCRYPTION_KEY`. See [Secrets at Rest](#secrets-at-rest). |
| `SPINE_SECRET_ENVELOPE_MODE` | No for `spine serve`; **yes** for `spine secrets reencrypt` | `migrate` (serve only) | `migrate`, `strict` or `downgrade`: how legacy (`enc:v1`, plaintext) signing secrets are treated and which envelope is written. `strict` is required to close audit finding P3-10. `spine secrets reencrypt` refuses to run without it; set it to serve's mode. See [Envelope Modes](#secrets-at-rest). |
| `SPINE_OPERATOR_TOKEN` | No | - | Static bearer token for system-level endpoints (workspace CRUD, `GET /api/v1/platform/metrics`). Minimum 32 characters; shorter values are refused at startup. Unset: operator endpoints return 503. |
| `SMP_ADMIN_TOKEN` | No | - | Platform bearer seeded as the admin actor `smp-admin` into every workspace this process loads (db / platform-binding modes), so the management platform authenticates on its first request. Must be at least 32 characters with at least 8 distinct characters (generate with `openssl rand -hex 32`): a weaker value refuses to start when `SPINE_ENV=production` and logs a warning otherwise. Rotating it revokes the previous bootstrap bearer in each workspace on that workspace's first load after the restart, so switch the platform's bearer in the same change window (see the operator runbook, "Rotating `SMP_ADMIN_TOKEN`"). Unsetting it does not revoke the existing bearer. A suspended or deactivated `smp-admin` stays that way. Unset: no bootstrap. |
| `SPINE_INSECURE_LOCAL` | No | - | Set to `1` only for local development with `sslmode=disable` DB URLs. |
| `SPINE_TRUSTED_PROXY_CIDRS` | No | - | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-For` (every header line) picks the rate-limit bucket. A request from one of them never gets token-less git access. Leave unset if directly internet-facing. |
| `SPINE_GIT_HTTP_TRUSTED_CIDRS` | No | - | Comma-separated CIDRs whose **direct** peers may clone/fetch over `/git` without a bearer token (runner addresses only). Never applies to pushes, to a peer in `SPINE_TRUSTED_PROXY_CIDRS`, or to a request carrying a forwarding header; never include a Docker bridge gateway. Unset = require tokens from every caller. See §7. |
| `SPINE_GIT_CREDENTIAL_HELPER` | No | - | Credential helper name, one of: `cache`, `store`, `osxkeychain`, `manager`, `pass`. Recommended production mode. Non-allowlisted values are refused at startup. |
| `SPINE_GIT_PUSH_TOKEN` | No | - | Static PAT/deploy token for HTTPS push. Scrubbed from process env after startup; ignored when `SPINE_GIT_CREDENTIAL_HELPER` is also set. |
| `SPINE_GIT_PUSH_USERNAME` | No | `x-access-token` | Username for token-based push auth |
| `SPINE_GIT_PUSH_ENABLED` | No | `true` | Set to `false` to disable git push |
| `SMP_WORKSPACE_ID` | No | - | Management platform workspace ID (dedicated mode) |
| `SPINE_ORPHAN_THRESHOLD` | No | - | Duration before recovering orphaned steps |
| `SPINE_STEP_ENTRY_POLICY` | No | `strict` | Engine step-entry policy (INIT-023 TASK-007). `strict` (or unset): assign/claim enforce run status, actor eligibility, execution mode, preconditions, `required_skills` on assign and separation of duties; cancelled/failed runs close their live steps; the gateway's engine-less assign fallback is disabled. `legacy`: pre-INIT-023 behaviour, logged as a `WARN` at startup (migration escape hatch, to be removed). Any other value refuses to start. See [Security Model](/architecture/security-model.md) §4.4.1. |
| `SPINE_MIGRATIONS_DIR` | No | `migrations` | Path to schema migrations |
| `SPINE_SSE_MAX_CONN_PER_ACTOR` | No | `5` | Per-actor cap on concurrent SSE stream connections, counted per (workspace, actor) in shared mode (≤0 disables the cap) |
| `SPINE_EVENT_DELIVERY` | No | `false` | Feature flag — set to `true` to enable webhook / SSE / pull-log delivery. Without it, only projection polling works. |
