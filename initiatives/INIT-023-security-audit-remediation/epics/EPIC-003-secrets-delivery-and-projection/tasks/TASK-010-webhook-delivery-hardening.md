---
id: TASK-010
type: Task
title: "Opt-in v2 webhook signatures matching SMP's receiver, plus delivery-queue, breaker, SSRF and redaction fixes"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-07
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
  - type: related_to
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/tasks/TASK-003-webhook-dispatcher.md
  - type: related_to
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/tasks/TASK-002-circuit-breaker.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-026-harden-webhook-target-url-validation.md
  - type: related_to
    target: /docs/integration-guide.md
---

# TASK-010 — Opt-in v2 webhook signatures matching SMP's receiver, plus delivery-queue, breaker, SSRF and redaction fixes

---

## Purpose

This task hardens `adapters/delivery`. It closes five P3 findings from the 2026-10-06 security
audit:

- **P3-11 (Spine half)** — signatures can be replayed;
- **P3-12** — the circuit breaker can wedge in half-open;
- **P3-5** — the delivery queue can be poisoned;
- **P3-25** — the SSRF target validator leaks resolved addresses and misses IPv6 transition
  addresses;
- **P3-26** — secret URL parts leak into error columns and logs.

Line numbers refer to Spine `61994fe` unless marked "SMP", which means SMP `80995559`. The
audit's proofs of concept are under `tmp/init027-orchestration/spine-audit/poc/` in the SMP tree.
The design-phase prototype and regression tests are under
`tmp/init027-orchestration/spine-init023/_work/task010/`.

### P3-11: the signature covers only the body, so a captured delivery replays forever

- `deliver` signs only the payload: `computeHMAC(entry.Payload, sub.SigningSecret)` →
  `X-Spine-Signature: sha256=<hex>` (`adapters/delivery/webhook_dispatcher.go:218-222`,
  `:325-329`).
- The MAC input has no timestamp, nonce, delivery id or URL. Two deliveries of the same event
  carry byte-identical signatures (`poc/gap1-crypto-secrets-reaud/dispatcher_replay_poc_test.go`,
  re-run on `61994fe`: PASS).
- **Attacker:** anyone who once sees a signed delivery can replay it to the receiver at any later
  time. Examples:
  - a TLS-terminating proxy or load-balancer log on the receiver side;
  - a request-logging middleware;
  - a misdirected or mirrored receiver.
- **Impact:** a replayed request authenticates as Spine. How much harm follows depends on the
  receiver's idempotency.
- **SMP:** SMP's receiver already has a v2 replay guard. It enforces a ±5-minute timestamp
  window, rejects a reused nonce per workspace, and binds both into the MAC
  (`internal/customer/runner/event_receiver.go:328-371`, `:433-471`, `:694-712`, SMP). The guard
  never engages, because Spine never sends `X-Webhook-Timestamp` / `X-Webhook-Nonce`. Every Spine
  delivery takes the v1 branch (`:707-711`, SMP).

### P3-12: a half-open probe that exits early wedges the subscription

- `AllowDelivery` moves an open circuit to half-open and grants a single probe
  (`adapters/delivery/circuit_breaker.go:74-80`). While half-open, every other call returns
  `false` (`:82-83`).
- Three exit paths after the probe is granted record neither success nor failure:
  - resolver error (`webhook_dispatcher.go:190-195`);
  - URL-validator rejection (`:200-204`);
  - request build error (`:207-212`).
- After any of them the breaker stays half-open for good. The subscription delivers nothing more
  until the dispatcher is rebuilt.
- Reproduced by `poc/delivery-scheduler/poc_hotloop_test.go`
  (`TestPoC_HalfOpenProbeEarlyReturnWedgesSubscription`): 30 polls, 0 endpoint hits, breaker
  half-open.
- **Embedded reach.** In SMP each warm workspace has one dispatcher per pool pair
  (`internal/customer/eventdelivery/worker.go:416-421`, SMP). One sequence wedges that
  workspace's step dispatch until the pair restarts:
  1. A receiver outage opens the circuit, for example SMP answering 503 on a platform-DB blip.
  2. A runtime-DB blip then hits the resolver during the probe.

### P3-5: non-retryable and circuit-held deliveries are re-claimed ahead of newer rows

- `ClaimDeliveries` claims rows that are `status IN ('pending','failed') AND (next_retry_at IS
  NULL OR next_retry_at <= now()) ORDER BY created_at LIMIT n`
  (`adapters/store/postgres_delivery.go:23-58`).
- Four paths write a row back as immediately claimable. Each is a hot loop, and once there are
  at least `concurrency` (5) such rows, every other subscription in the workspace starves.
  1. **Non-retryable 4xx** → `failed` with `next_retry_at = NULL` (`webhook_dispatcher.go:302-309`).
     The TLS-config error path takes the same branch (`:226-233`).
  2. **Circuit open** → `pending` with `NULL`. `attempt_count` is bumped although no attempt was
     made (`:183-187`, and `postgres_delivery.go:63` increments on every status write).
  3. **Resolver error**, including a deleted subscription → `failed` with `NULL`
     (`webhook_dispatcher.go:190-195`). Queue rows have no foreign key to `event_subscriptions`
     (`migrations/015_event_delivery_tables.sql:4-18`), and `DeleteSubscription` does not touch
     the queue (`postgres_subscriptions.go:91-94`).
  4. **URL rejected / request build error** → `failed` with `NULL` (`:200-212`).
- **Retention never reaps any of them:** it deletes only `delivered`/`dead`
  (`postgres_delivery.go:281`, `:289`).
- **Reproduced against real Postgres** (`poc/verify-r1-10/`, re-run in this design phase on a
  throwaway PostgreSQL 17):
  - five 410 rows starve every other subscription;
  - pause, delete, restart and retention do not recover the queue;
  - orphaned rows hot-loop;
  - an open circuit inflates `attempt_count` (21–26 against a budget of 5).
  - The unit-level PoCs in `poc/delivery-scheduler/poc_hotloop_test.go` also pass on `61994fe`.
- **Attacker, standalone:** an admin-configured endpoint (A5) that answers 4xx wedges the
  workspace's whole delivery queue, including other tenants' and integrators' subscriptions.
- **Embedded:** each runtime DB carries one subscription (`smp-internal`), so there is no
  cross-subscription victim. The hot loop is still live:
  - SMP's own receiver answers 401 during a credential-rotation window
    (`worker.go:113-121`, SMP).
  - It answers 403 for a cross-workspace actor and 400 for an invalid payload
    (`event_receiver.go`, SMP). Those two are permanent.
  - Five permanent-4xx rows therefore stall that workspace's step dispatch for good.

> **Design-check note (2026-10-06) — the per-path fixes alone do not close the starvation.**
> The root cause is twofold: rows are re-claimable immediately, and `ClaimDeliveries` orders by
> `created_at` alone, so any subscription's old claimable rows take every claim slot ahead of
> another subscription's newer rows. §2's outcome table fixes the first cause for each listed
> path. It leaves the second cause open for rows held behind an open circuit:
> - Held rows are deferred to one shared probe instant (`NextAttemptAt` = `lastFailureAt` +
>   60 s), so they all become claimable together.
> - They are then re-claimed oldest-first, 5 per poll.
> - Once a subscription holds more than concurrency × (recovery timeout / poll interval)
>   = 5 × 60 = **300** rows, the oldest 300 cycle forever. No newer row of any other
>   subscription is ever claimed.
>
> Reproduced against the prototype (`_work/task010/designcheck/zz_t010_designcheck_test.go`
> `TestDC_HeldBacklogStarvesHealthySubscription`):
> - 400 held rows starve a healthy subscription for all 900 simulated polls (15 minutes);
> - 100 held rows delay it 21 polls.
>
> An A5 endpoint that answers 503 and is subscribed to a busy workspace reaches 300 rows easily.
> §2a (fair claim) closes it.

### P3-25: the target validator is a DNS oracle and misses IPv6 transition addresses

- `ValidateURL` resolves the host and returns the `CheckAddr` error to the caller
  (`adapters/delivery/targeturl.go:141-152`). That error names the address, for example
  `target_url: private address 10.1.2.3 not permitted` (`:169-196`).
- `SafeDialContext` returns the same message (`:240-244`). It reaches the subscription-test
  response (`service/internal/gateway/handlers_subscriptions.go:359-366`) and delivery
  `last_error`.
- DNS failures wrap `net.DNSError` (`:233-236`), whose text names the resolver's address
  (`lookup x on 10.0.0.2:53: …`).
- **Attacker:** a standalone workspace admin, who holds the `subscription.*` permissions
  (`core/auth/permissions.go:86-89`). In shared mode that admin learns the operator's internal
  DNS and resolver addresses.
- `CheckAddr` classifies only native IPv4/IPv6 ranges (`:169-196`). These forms bypass it
  (checked with Go 1.26 `net.IP` in this design phase):
  - NAT64 `64:ff9b::a9fe:a9fe`, which maps to IMDS;
  - 6to4 `2002:a00:1::`;
  - IPv4-compatible `::10.0.0.1`;
  - Teredo;
  - `0.0.0.0/8` other than `0.0.0.0`.
- On a NAT64/DNS64 network, a hostname with such an AAAA record reaches IMDS or private services.

### P3-26: secret path and query tokens of webhook URLs land in error columns and logs

- `client.Do` errors are `*url.Error`, whose text contains the full target URL, for example
  `Post "https://hooks.example/services/T/B/<secret>?token=<secret>": …`.
- That text is written to:
  - `event_delivery_queue.last_error` and `event_delivery_log.error`
    (`webhook_dispatcher.go:241-245` → `handleFailure` `:291-300`, `:308`, `:321`);
  - the error log (`:242`);
  - the subscription-test JSON and its log line (`handlers_subscriptions.go:342-345`,
    `:359-366`).
- `ValidateURL`'s parse error echoes the URL as well (`targeturl.go:87-90`), and the bootstrap
  logs the target URL (`bootstrap.go:110`).
- **Who reads it:** anyone with delivery-history access (`GET /subscriptions/{id}/deliveries…`)
  or log access. Slack- and Discord-style webhook URLs carry their credential in the path.
- **Embedded:** SMP's only target URL is its own internal receiver and holds no secret.

> **Design-check note (2026-10-06) — who actually gains from P3-26.** The delivery-history
> endpoints need `subscription.read` (`handlers_delivery_history.go:15`, `:44`, `:122`).
> `subscription.read` is also the permission whose `subscriptionResponse` returns the full
> `target_url` (`handlers_subscriptions.go:378-391`), so an API reader of `last_error` learns nothing
> new. The redaction protects:
> - operator logs and log pipelines;
> - database dumps and backups of the error columns;
> - in SMP, workspace **members**, who see `last_error` in the run page's dispatch-failure banner
>   (`spineembedded/proxy.go:1535-1543`, SMP). The banner also shows the resolver and dial error
>   text that §2 and §4 now sanitise (I5).

## Design

### 0. Invariants

- **I1. v1 stays byte-identical by default.** A subscription whose metadata has no
  `signature_version`, or has `"v1"`, gets exactly today's headers: `X-Spine-Signature:
  sha256=<hex HMAC(body)>`, no `X-Webhook-*` headers, and no signature when the secret is empty.
- **I2. v2 is SMP's existing wire format, byte for byte.** It is defined by `hmacMatchesAny` and
  `stripSigPrefix` (`event_receiver.go:694-722`, SMP) and pinned by golden vectors (AC2).
- **I3. Every claimed row leaves `deliver` in one of three states:**
  - `delivered`;
  - `dead` (terminal);
  - `failed`/`pending` with `next_retry_at` strictly in the future.
- **I4. Every granted half-open probe is resolved** (success, failure or release) on every exit
  path.
- **I5. No caller-facing error, error column or log line produced by `adapters/delivery` or the
  subscription handlers carries:**
  - a resolved IP address of a DNS-named target;
  - a DNS resolver's address;
  - the path, query or fragment of a target URL.
- **I6. No runtime schema change.** No migration and no new status value. `dead` is the existing
  terminal state (`migrations/015_event_delivery_tables.sql:17`).

> **Design-check note (2026-10-06) — invariants tightened and added.**
> - **I3 qualifier.** I3 holds when the status write succeeds. A lost write (pool closed
>   mid-flight) still leaves the row `delivering`; that needs `claimed_at` (out of scope, I6).
> - **I5 extended.** I5 also covers two more kinds of text:
>   - **(d)** the address in a failed connect. Today `SafeDialContext` returns the dialer's
>     `*net.OpError`, which reads `dial tcp <resolved-ip>:<port>: …`. The prototype keeps this,
>     so a DNS-named target that is allowlisted, or public, still reveals its resolved address
>     (`TestDC_DialErrorLeaksResolvedAddress`). The fix is in §4.
>   - **(e)** infrastructure error text from the subscription resolver. pgconn connect errors
>     name the DB `host:port`, user and database; decrypt errors are also covered. Today and in
>     the prototype, this text goes verbatim into `last_error`, which SMP members see. The fix is
>     in §2.
> - **I7 (new) — Fair claim.** A subscription's claimable rows never delay another
>   subscription's claimable row by more than one poll, however many they are. Per-subscription
>   FIFO is kept. A single-subscription queue (SMP's shape) is claimed in exactly today's order.
>   See §2a.
> - **I8 (new) — Stable HTTP outcome text.** For an HTTP-status outcome, `last_error` and the
>   log row's `error` stay exactly `HTTP <code>` (`webhook_dispatcher.go:257`). SMP TASK-006's
>   rotation requeue matches `LastError == "HTTP 401"` and pins it in its AC12.

> **Review-round-1 note (2026-10-07) — I5 and I7 as built did not hold in full.**
> - **I5(d) extends past connect.** A read or write failure after a successful connect (a reset,
>   a broken pipe, a reset during the TLS handshake) is a `*net.OpError` reading
>   `read tcp <spine-ip:port>-><resolved-ip:port>: …`. `SafeDialContext` now returns a wrapped
>   connection whose errors name the caller's `host:port` instead, and `RedactError` re-renders
>   any other `*net.OpError` in the chain. The dialer's Debug line keeps the address for the
>   operator, as §4 intends.
> - **I7 restated.** The fair claim alone bounds the delay only while fewer than `concurrency`
>   (5) subscriptions are held: each held subscription re-deferred only the row it was handed, so
>   it always had a due row older than any healthy row, and five of them took every slot.
>   Amended: a subscription the dispatcher cannot deliver to (open circuit, unreadable
>   subscription) has its **whole due queue** held in one attempt-free statement
>   (`DeliveryStore.HoldDueDeliveries`), so it costs about two claim slots per probe cycle
>   however many rows it holds. A healthy subscription's due row waits at most one poll while
>   fewer than `concurrency` subscriptions are held, and at most `ceil(2 × held / concurrency)`
>   polls otherwise.

> **Review-round-2 note (2026-10-07) — I7 also covers subscriptions whose deliveries can never
> succeed.** The round-1 statement held only for subscriptions the dispatcher *holds*. A permanent
> verdict on a subscription (deleted, invalid metadata, refused target URL, a request that cannot be
> built, unusable TLS or signing configuration) dead-lettered only the claimed row, so `concurrency`
> or more such subscriptions with old backlogs took every claim slot until their backlogs drained
> one row per poll each (reviewer: at 5 × 300 rows the first healthy delivery came at poll 300).
> Amended: the first such row dead-letters the subscription's whole due queue in one statement
> (§2 round-2 note), so such a subscription costs one claim slot, once. A healthy subscription's
> due row then waits at most `ceil(k / concurrency)` polls behind `k` of them.

### 1. P3-11 — opt-in v2 signatures (`adapters/delivery/signature.go`, new)

**Opt-in.** The opt-in is a per-subscription key in the existing
`runtime.event_subscriptions.metadata` jsonb (`migrations/016_event_subscriptions.sql:13`):

```json
{ "signature_version": "v2" }
```

- Absent, `""` or `"v1"` means v1. `"v2"` means v2.
- Any other value, or a non-string, is a permanent configuration error. It wraps a new sentinel,
  `ErrInvalidSubscriptionMetadata`.
- The key sits beside the existing `tls` block that `parseSubscriptionTLS` reads
  (`tls.go:42-65`).

**Parser.** A new `parseSubscriptionMetadata(metadata []byte) (subscriptionMetadata, error)`
returns `{TLS *SubscriptionTLSConfig; SignatureVersion string}` with the version normalised.

- `parseSubscriptionTLS` keeps its contract by delegating to it, so `tls_test.go` keeps passing.
- `StoreSubscriptionResolver.GetSubscription` (`subscription_store.go:49-64`) calls it and fills a
  new field, `SubscriptionDetail.SignatureVersion string` (`webhook_dispatcher.go:20-28`).

**Exported, additive API** (used by the gateway, by SMP's adaptation task, and by out-of-tree
receivers):

```go
const (
    SignatureVersionV1          = "v1"
    SignatureVersionV2          = "v2"
    MetadataKeySignatureVersion = "signature_version"
    HeaderSignature             = "X-Spine-Signature"
    HeaderTimestamp             = "X-Webhook-Timestamp"
    HeaderNonce                 = "X-Webhook-Nonce"
)
var ErrInvalidSubscriptionMetadata = errors.New("invalid subscription metadata")

func SignV1(secret string, body []byte) string                    // "sha256=" + hex(HMAC(secret, body))
func SignV2(secret, timestamp, nonce string, body []byte) string  // "v2=" + hex(HMAC(secret, ts+"."+nonce+"."+body))
func ValidateSubscriptionMetadata(metadata []byte) error           // nil, or wraps ErrInvalidSubscriptionMetadata
func SignatureVersionOf(metadata []byte) (string, error)           // "v1" | "v2"
func WithSignatureVersion(metadata []byte, version string) ([]byte, error) // sets the key; preserves every other key
```

- `computeHMAC` (`webhook_dispatcher.go:325-329`) becomes a one-line wrapper over the same MAC,
  so `TestComputeHMAC` keeps passing.
- The HMAC key is `[]byte(secret)`, exactly as today.
  - SMP stores the per-workspace credential as `string(secret.Reveal())`
    (`worker.go:494`, `provision_steps_runtime.go:480-486`, SMP).
  - It verifies with `key.Reveal()` (`event_receiver.go:604`, SMP).
  - So the key bytes are identical on both sides.

**Signing per attempt.** A new unexported `signRequest(h http.Header, version, secret string,
body []byte, now time.Time, rnd io.Reader) error` is called in `deliver` **after** `clientFor` and
**immediately before** `client.Do`.

- **v1:** today's behaviour: `X-Spine-Signature: sha256=…` only when `secret != ""`.
- **v2:**
  - `secret == ""` → `errV2WithoutSecret`. The delivery is dead-lettered (§2) and never sent
    unsigned.
  - `nonce` = `hex.EncodeToString` of 16 bytes read with `io.ReadFull(rnd, …)`
    (`crypto/rand.Reader` in production). That is 32 lowercase hex characters, which SMP
    hex-decodes to 16 bytes (`event_receiver.go:434-442`, SMP; `replayguard/store.go:59`, SMP;
    BYTEA column `migrations/072_webhook_delivery_nonces.sql:18-23`, SMP). A read error is
    retryable (§2).
  - `ts` = `strconv.FormatInt(now.Unix(), 10)`: decimal Unix seconds at send time, inside SMP's
    ±5 minutes (`event_receiver.go:33`, `:648-666`, SMP).
  - Headers: `X-Webhook-Timestamp: ts`, `X-Webhook-Nonce: nonce`,
    `X-Spine-Signature: v2=<hex HMAC(secret, ts + "." + nonce + "." + body)>`.
  - The body is `entry.Payload`, the same bytes passed to `http.NewRequestWithContext`.
- **Freshness.** Timestamp and nonce are never persisted. Each retry is a new `deliver` call with a
  fresh pair. So are an operator replay (`handlers_delivery_history.go:102-108`) and SMP's redrive
  (`dispatchstore.go:135`, SMP). The receiver's dedupe therefore never sees a legitimate retry as
  a replay.
- **No dual headers.** v2 does not also send the v1 MAC: SMP rejects a v1 MAC whenever the v2
  headers are present (`event_receiver.go:696-705`, SMP). `X-Spine-Delivery` stays stable across
  attempts for receivers that dedupe on it.
- **Testability.** `WebhookDispatcher` gains unexported `now func() time.Time` (default
  `time.Now`) and `rand io.Reader` (default `crypto/rand.Reader`).

**Bootstrap support** (`bootstrap.go`). `BootstrapConfig` gains `SignatureVersion string`.

- **Empty** means "leave the stored value alone". Existing callers and the existing tests are
  unchanged.
- **Non-empty** must be `v1` or `v2`; otherwise an error.
  - **On create** (`:91-104`): the metadata becomes `{"source":"bootstrap","signature_version":v}`.
  - **On update** (`:61-81`): `changed` also becomes true when `SignatureVersionOf(existing.Metadata)
    != v`. The metadata is then rewritten with `WithSignatureVersion`, preserving `source`, `tls`
    and any other key.

  > **Design-check note (2026-10-06).**
  > - **Unparseable metadata on update.** If the existing metadata does not parse,
  >   `SignatureVersionOf` and `WithSignatureVersion` both fail. Bootstrap then replaces the
  >   metadata with `{"source":"bootstrap","signature_version":v}` and does not return the error.
  >   SMP's pre-dispatch ensure blocks dispatch on any bootstrap error (`worker.go:375-390`, SMP),
  >   so an error here would wedge that workspace's step dispatch on a row that never self-heals.
  > - **Merge order with TASK-009.** TASK-009 also edits `BootstrapInternalSubscription`'s
  >   `changed` check (`bootstrap.go:61-70`, its §2) and `handlers_subscriptions.go`. The two tasks
  >   land one after the other, and the later one rebases. They do not conflict in meaning:
  >   TASK-009 changes how the secret is stored, and this task adds a metadata key.

**Gateway (standalone, `service/internal/gateway/handlers_subscriptions.go`).**

- **Request types** (`:17-29`): `Metadata` changes from `[]byte` to `json.RawMessage`.
  - Today the `[]byte` field only decodes a base64 JSON string, although
    `docs/integration-guide.md:350-360` documents an object. An object fails with "cannot
    unmarshal object into … []uint8" (checked in this design phase).
  - A new `decodeSubscriptionMetadata` accepts a JSON object (the documented form) or a JSON
    string holding base64 of an object (today's wire form, kept for compatibility). It treats
    `null` or absent as "not supplied".
- **Create** (`:75-78`) and **update** (`:206-208`) run `delivery.ValidateSubscriptionMetadata`
  and answer 400 `invalid_params` on error. Update still replaces metadata wholesale, as today.
- **`subscriptionResponse`** (`:378-391`) adds `"signature_version"`, so operators can see which
  format a receiver gets. On unparseable metadata the value is `"invalid"`.

### 2. P3-5 — every claimed row reaches a terminal or future-scheduled state

**Store (`adapters/store`).** Add one method to `DeliveryStore` (`interfaces.go:193-206`) and to
`PostgresStore` (`postgres_delivery.go`):

```go
// DeferDelivery re-schedules a claimed delivery without consuming an attempt.
DeferDelivery(ctx context.Context, deliveryID, reason string, until time.Time) error
```

```sql
UPDATE runtime.event_delivery_queue
SET status = 'pending', last_error = $2, next_retry_at = $3
WHERE delivery_id = $1 AND status = 'delivering'
```

- There is no `attempt_count` bump.
- The `status = 'delivering'` guard means a row that an operator replayed, or SMP redrove, in the
  meantime is never overwritten.
- Fakes to update:
  - `adapters/delivery/webhook_dispatcher_test.go:20-99`, `:369-387`;
  - `subscriber_test.go:497-…`;
  - `retention_test.go:52-…`;
  - `service/cmd/spine/stubstore_test.go:373-…`;
  - `service/internal/gateway/stubstore_test.go:349-…`;
  - `service/internal/gateway/handlers_subscriptions_test.go:123-…`.
- SMP implements no `DeliveryStore`. Its `executionActorStore` embeds the `store.Store`
  interface (`internal/customer/spinecore/execution_actor_store.go:40-44`, SMP), so it compiles
  unchanged (§7).

**Dispatcher outcome table (`webhook_dispatcher.go:175-322`).**

| Path | Today (`61994fe`) | After |
|---|---|---|
| Circuit holds the row (open, or another probe in flight) | `pending`, `NULL`, attempt+1 (`:183-187`) | Row older than `MaxCircuitHold` (default 24 h, from `entry.CreatedAt`) → **dead** `circuit open: held longer than 24h0m0s`. Otherwise `DeferDelivery(…, "circuit open", breaker.NextAttemptAt(sub, pollInterval))`: the open circuit's probe time, or now + poll interval while a probe is in flight. **No attempt consumed.** |
| Resolver: subscription not found (`*domain.SpineError` with `Code == ErrNotFound`, from `store/pgutil.go:16-21`) | `failed`, `NULL` (`:190-195`) | **dead** `subscription not found` |
| Resolver: metadata invalid (`errors.Is(err, ErrInvalidSubscriptionMetadata)`) | `failed`, `NULL` | **dead** with the parse reason |
| Resolver: any other error (DB, decrypt) | `failed`, `NULL` | `handleFailure(retryable=true)`: backoff, then **dead** at the budget |
| URL rejected by `ValidateURL` | `failed`, `NULL` (`:200-204`) | **dead** (redacted reason, §5) |
| `http.NewRequestWithContext` error | `failed`, `NULL` (`:207-212`) | **dead** (redacted reason) |
| v2 without a secret | — | **dead** `signature_version v2 requires a signing secret` |
| Nonce read error | — | `handleFailure(retryable=true)` |
| Per-subscription TLS config error | `handleFailure(retryable=false)` → `failed`, `NULL` (`:226-233`) | **dead** (breaker failure still recorded) |
| HTTP non-retryable status | `failed`, `NULL` (`:302-309`) | **dead** |
| HTTP retryable, budget left | `failed`, backoff (`:313-321`) | unchanged (the retry time comes from `d.now`) |
| HTTP retryable, budget exhausted | `dead` | unchanged |

- **One terminal write path.** A new `deadLetter(ctx, entry, reason)` writes one
  `event_delivery_log` row (no status code, error = the reason) and
  `UpdateDeliveryStatus(…, "dead", reason, nil)`. The history API therefore always explains a
  dead row.
- **`handleFailure` (`:302-309`).** The branch `if !retryable { status = "failed" }` is deleted.
  Every non-retryable or exhausted failure is `dead`.
- **`isRetryable` (`retry.go:48-59`)** also treats `408 Request Timeout` and `425 Too Early` as
  retryable. Before this task a misclassified 4xx was retried by accident (the hot loop). After it
  a misclassified 4xx is dead-lettered at once, so these two conventionally transient codes must
  ride the backoff.
- **`DispatcherConfig.MaxCircuitHold time.Duration`.** Zero selects the exported
  `DefaultMaxCircuitHold = 24 * time.Hour` (owner question 1). The standalone wiring
  (`service/cmd/spine/serve_builders.go:383-385`, `serve_resolver.go:299-301`) and SMP's wiring
  pass nothing and get the default.
- **Existing rows.** Rows already stuck as `failed`/`NULL` from earlier releases are claimed once
  more and then follow the new table. The queue heals itself with no migration.

> **Design-check note (2026-10-06) — resolver error text.** In the "Resolver: any other error"
> row, the prototype writes `"resolve subscription: " + err.Error()` into `last_error` and the log
> row. A pgconn connect error reads ``failed to connect to `user=… database=…`: <host>:<port> …``,
> and a decrypt error is also covered (TASK-009 Decision 9 makes a row that does not open fail its
> read). So that text would reach SMP members and standalone tenant admins. **Change:**
> - the error columns get a fixed reason, `resolve subscription: temporarily unavailable`;
> - the full error goes only to the operator log, at Error with `subscription_id`;
> - the `not_found` and invalid-metadata reasons keep their texts, which contain no
>   infrastructure detail. (I5(e), AC10.)

> **Design-check note (2026-10-06) — known edges of the circuit hold.**
> - **Old replays dead-letter at once.** The hold cap is measured from `created_at`, and the
>   queue has no column for "held since". So a row older than `MaxCircuitHold` that is replayed
>   (`handlers_delivery_history.go:102-108`) or redriven (`dispatchstore.go:135`, SMP) while its
>   circuit is open is dead-lettered again on its next claim, with no HTTP attempt and the reason
>   `circuit open: held longer than …`. Redriving after the endpoint recovers works, because the
>   probe gets an attempt. This is documented in the guide (§6) and noted in Owner question 1.
> - **Probes are real attempts, and that is deliberate.** While the circuit stays open, the
>   oldest held row is the probe each cycle, and each failed probe takes one of that row's
>   attempts. In a long outage, roughly one row per few probe cycles reaches `dead`.
>   - Making probes free was considered and rejected: one poison row whose payload always gets
>     a 5xx would then be re-chosen as the probe forever (it is the oldest), and it would keep
>     the circuit open for every other row until `MaxCircuitHold`.
>   - The SMP consequence is in §7 B2.

> **Review-round-1 note (2026-10-07) — two rows of the outcome table changed.**
> - **Circuit holds the row.** Besides deferring the claimed row, the dispatcher holds the
>   subscription's other due rows (pending/failed, `next_retry_at` NULL or before the probe
>   time) to the same instant through the new `DeliveryStore.HoldDueDeliveries`, without
>   touching `attempt_count`; rows of the subscription older than `MaxCircuitHold` are
>   dead-lettered in the same statement and get a delivery-log row each. This closes the I7 gap
>   above. Decision 16 had rejected a bulk deferral *instead of* the fair claim; both are now
>   kept, because neither alone covers both shapes.
> - **Resolver: any other error (DB, decrypt).** Not `dead` at the budget any more. The row
>   still records an attempt per failure and follows the backoff (1, 2, 4, 8 s), so once the
>   budget is spent it is `failed` with `attempt_count` at the budget — what SMP's
>   stuck-dispatch banner shows — but it is then retried every minute and delivered as soon as
>   the subscription can be read again; it goes `dead` only after `MaxCircuitHold`
>   (`resolve subscription: temporarily unavailable: held longer than 24h0m0s`). The rest of the
>   subscription's due queue is held to the same retry instant. Reason: before this task such
>   rows were retried every poll and delivered once the fault cleared; dead-lettering them after
>   ~15 s turned a fixable operator-side fault (an SMP replica without its encryption key, a
>   TASK-009 key-ring rollout) into lost dispatches that each need a manual redrive, a
>   shared-behaviour change Rule 4 did not sanction (cross-repo rule 2).

> **Review-round-2 note (2026-10-07) — permanent verdicts take the due queue; the hold cap
> counts from the start of the hold.**
> - **Whole-queue dead-letter.** Every outcome-table row that dead-letters for a reason that is a
>   function of the subscription, not of the row — subscription not found, metadata invalid, URL
>   rejected, request build error, TLS config error, v2 without a secret or an unknown version at
>   signing — now also dead-letters every other row of the subscription that is due by the next
>   poll, in one `HoldDueDeliveries` statement with `ExpireBefore = Until = now + poll interval`,
>   each with the same reason and a delivery-log row, `attempt_count` untouched. Rows scheduled
>   later are left alone: they are not claimable now, and each meets the same verdict when due. No
>   new store method and no SQL change. The entropy-read failure stays per row (transient).
> - **`MaxCircuitHold` is measured from the start of the hold.** Measured from `created_at`, a
>   row queued more than 24 h ago was dead-lettered by the first fault it met, and the bulk hold
>   expired the rest of the aged backlog with it — after a day-long dispatcher outage, or in an SMP
>   workspace re-warmed after a day, one resolver blip turned every aged dispatch into a dead row.
>   A row is now held since the later of its `created_at` and the start of its subscription's
>   current hold as this process observed it: the circuit opening (`CircuitBreaker.OpenedAt`, kept
>   across failed and released probes) or the first of the current run of resolver failures
>   (`unreadableSince`, cleared by a successful read). The bulk hold expires rows only once the
>   hold itself is older than the cap, with cut-off `now − cap`, so the rule is exact for rows the
>   dispatcher never claimed. The start lives in memory: a restart starts it again, so the cap can
>   come late, never early (`KNOWN-LIMITATIONS.md`).
> - **The "old replays dead-letter at once" edge narrows.** An old row replayed or redriven while
>   its circuit is open is dead-lettered again only if the outage itself has lasted longer than the
>   cap on the claiming dispatcher.

#### 2a. Fair claim (design-check addition, 2026-10-06; closes the residual P3-5 starvation)

`ClaimDeliveries` (`postgres_delivery.go:23-58`) ranks the claimable rows within each
subscription by `created_at` and claims in `(rank, created_at)` order. That is round-robin across
subscriptions, with FIFO kept inside each subscription.

```sql
UPDATE runtime.event_delivery_queue
SET status = 'delivering'
WHERE delivery_id IN (
    SELECT delivery_id FROM runtime.event_delivery_queue
    WHERE delivery_id IN (
        SELECT delivery_id FROM (
            SELECT delivery_id, created_at,
                   row_number() OVER (PARTITION BY subscription_id ORDER BY created_at) AS rn
            FROM runtime.event_delivery_queue
            WHERE status IN ('pending','failed')
              AND (next_retry_at IS NULL OR next_retry_at <= now())
        ) ranked
        ORDER BY rn, created_at
        LIMIT $1
    )
    AND status IN ('pending','failed')
    FOR UPDATE SKIP LOCKED
)
RETURNING delivery_id, subscription_id, event_id, event_type, payload,
          status, attempt_count, next_retry_at, last_error, created_at, delivered_at
```

- **Verified on PostgreSQL 17** in this design check (`_work/task010/designcheck/fairclaim.sql`).
  - 400 claimable rows of one subscription, all older than one row of another: the first claim
    of 5 includes the newer subscription's row.
  - A single-subscription queue is claimed in exactly today's order.
  - A row deferred into the future is never claimed.
  - Postgres refuses `FOR UPDATE` beside a window function at the same query level, so the
    window sits in a subquery and the lock is taken one level up.
- **Cost.** With 100 000 claimable rows across 50 subscriptions, the claim takes about 35 ms,
  against about 19 ms today (`fairclaim-perf.sql`). That is acceptable for a 1 s poll. The
  implementer records the measured numbers in Implementation Notes.
- **Concurrency.** `LIMIT` is applied before `SKIP LOCKED`. When two dispatchers race, which is
  possible with SMP replicas, a dispatcher can claim fewer than `limit` rows in that poll. It
  never claims a row twice. The outer `status IN (…)` re-check runs under the lock (READ
  COMMITTED re-evaluation).
- **Effect on SMP: none.** Each runtime DB carries one subscription, so the ranking reduces to
  `created_at`.
- **Effect on standalone.** Claim order across subscriptions changes from global FIFO to
  round-robin. Global cross-subscription order was never documented and never relied on: no store
  test asserts it (grep, this design check).
- **Interaction with §2.** The outcome table is still needed. Fair claim bounds the harm a bad
  subscription does to others, and the table stops a bad row from burning its own subscription's
  slots and attempts.
- **Prototype check.** With fair claim modelled in the regression fake,
  `TestDC_FairClaimClosesHeldBacklogStarvation` delivers at least 899 of 900 healthy rows at 100,
  400 and 5000 held rows.
- **Fake stores.** Any fake whose `ClaimDeliveries` models ordering must mirror the ranking. Of
  the committed tests, that is the `t010Store` of the queue regression. The existing fakes listed
  above do not model ordering and need no change.

### 3. P3-12 — the probe is always resolved (`adapters/delivery/circuit_breaker.go`)

- **Permit API.** Add a `Permit` and make `deliver` use it:

  ```go
  func (cb *CircuitBreaker) Acquire(ctx context.Context, subscriptionID string) (*Permit, bool)
  func (p *Permit) Success(ctx context.Context) // == RecordSuccess; first resolution wins
  func (p *Permit) Failure(ctx context.Context) // == RecordFailure; first resolution wins
  func (p *Permit) Release(ctx context.Context) // no endpoint outcome
  func (cb *CircuitBreaker) NextAttemptAt(subscriptionID string, fallback time.Duration) time.Time
  ```

  - `Acquire` keeps `AllowDelivery`'s transitions (`:62-86`). It also marks the permit that took
    the circuit to half-open as the probe.
  - `Release` acts only on an unresolved probe. It returns half-open to **open** and keeps
    `lastFailureAt`, so the next `Acquire` may probe again at once. For a non-probe permit, or a
    resolved one, it does nothing.
- **Dispatcher.** `deliver` calls `permit, ok := d.breaker.Acquire(…)`; on `!ok` it takes the
  deferral row of §2; otherwise it calls `defer permit.Release(ctx)`.
  - The HTTP outcomes call `permit.Success` and `permit.Failure`, replacing `RecordSuccess` and
    `RecordFailure` at `:229`, `:243`, `:254`, `:264`.
  - Early returns (resolver, URL, request build, signing) resolve through the deferred `Release`.
  - They do not count as endpoint failures. A runtime-DB blip must not trip the breaker of a
    healthy endpoint.
- **Testable clock.** `CircuitBreaker` gains an unexported `now func() time.Time` and uses it
  instead of `time.Now` / `time.Since` (`:75`, `:121`).
- **Compatibility.** `AllowDelivery`, `RecordSuccess`, `RecordFailure` and `State` stay exported
  and unchanged. `AllowDelivery` is documented as not for dispatch use, since it cannot resolve a
  probe. Nothing outside `adapters/delivery` calls them in either repository (grep, this design
  phase).

> **Design-check note (2026-10-06) — `Release` must be conditional.** `Release` moves the
> circuit from half-open to open only while the state is still half-open. Another permit may
> have resolved the episode first. For example, a request sent before the circuit opened can
> succeed late and close it, and an unconditional `Release` would then re-open a closed circuit.
> The prototype already checks `e.state == CircuitHalfOpen`
> (`TestDC_ReleaseAfterOtherPermitClosedCircuit` passes), and AC14 now pins this.
> - No generation counter is needed. A resolver call has no timeout of its own, so a probe stuck
>   in it for more than 60 s can `Release` during a later half-open episode. The only effect is
>   that the later episode moves half-open to open while its own probe is in flight, and at most
>   one extra concurrent probe may then be admitted. Each probe's `Success` or `Failure` still
>   sets the state, so no wedge results.
> - The SMP grep is re-confirmed. SMP's `internal/customer/webhook` has its own unrelated
>   `CircuitBreaker` with `Allow`/`OnSuccess`/`OnFailure`.

### 4. P3-25 — no addresses in caller-facing errors; transition addresses classified (`targeturl.go`)

**Classification** (`CheckAddr`, `:159-198`). The class checks move into
`blockedClass(ip) string`, which runs three steps:

1. Classify `ip` itself as today: loopback, unspecified, multicast, link-local, private, then the
   extra non-public IPv4 ranges.
2. For a non-IPv4 address, return `"non-public"` if it lies in:
   - the RFC 8215 local-use NAT64 prefix `64:ff9b:1::/48` (its embedding is operator-defined);
   - Teredo `2001::/32`.
3. Classify the IPv4 address that `embeddedIPv4(ip)` extracts:
   - NAT64 well-known prefix `64:ff9b::/96` → last 32 bits (RFC 6052);
   - 6to4 `2002::/16` → bits 16–47 (RFC 3056);
   - IPv4-compatible `::/96` → last 32 bits (RFC 4291 §2.5.5.1).

   IPv4-mapped addresses are already folded by `To4`.

- `extraNonPublicV4` (`:204-207`) gains `0.0.0.0/8`, RFC 1122 "this network".
- Public NAT64 and 6to4 targets, such as `64:ff9b::808:808` and `2002:808:808::1`, stay allowed:
  IPv6-only deployments reach IPv4 webhooks through NAT64.

**Error shape.**

- `CheckAddr` returns `*BlockedAddressError{Class string; ip net.IP}`.
  - Its `Error()` is `target_url: <class> address not permitted`, with no address.
  - The class substrings that `TestTargetValidator_CheckAddr_RejectsDangerousRanges` matches are
    kept.
- **`ValidateURL`, DNS-named host** (`:147-151`): any blocked answer returns
  `target_url: host resolves to a non-public address (allowlist it via SPINE_WEBHOOK_ALLOWED_HOSTS
  if intended)`.
  - Neither the address nor the class is given; even the class is extra oracle bits.
  - Host, class and IP go only to the operator log via `observe.Logger(ctx)`.
  - A literal-IP host (`:129-134`) still gets the class, because the caller supplied the address.
- **`SafeDialContext`** (`:240-244`) returns a new sentinel, `ErrBlockedDestination`
  (`target_url: destination address not permitted`), and logs the detail at Warn. That string is
  what reaches `last_error`, delivery-log rows and the subscription-test response.
- **DNS failures** (`:233-236`) become `target_url: resolve <host>: no such host | timeout |
  temporary failure | lookup failed`. The reason comes from the `*net.DNSError` flags. The
  resolver address in `DNSError.Error()` is never included.
- **Parse errors** (`:87-90`) report `url.Error.Err` only, never the URL (see §5).
- What remains of the oracle is one bit, accepted or rejected. That is inherent to any
  create-time check and is accepted.

> **Design-check note (2026-10-06) — two gaps in §4, both reproduced against the prototype.**
>
> **1. Connect failures still name the resolved address (I5(d)).** After the address checks pass,
> `SafeDialContext` returns the dialer's `lastErr` (`targeturl.go:254-264`). That is a
> `*net.OpError` reading `dial tcp <resolved-ip>:<port>: connect: …`, and it reaches `last_error`,
> the delivery log and the subscription-test JSON.
> - `TestDC_DialErrorLeaksResolvedAddress` reproduces it: an allowlisted `localhost` on a closed
>   port gives `last_error` =
>   `Post "http://localhost:<p>/[redacted]": dial tcp 127.0.0.1:<p>: connect: connection refused`.
> - In shared mode, this hands the private address of any operator-allowlisted host
>   (`SPINE_WEBHOOK_ALLOWED_HOSTS`) to every tenant admin.
> - In SMP, `workers.go:256` reads the allowlist, and the compose default allowlists the
>   internal receiver `customer-app` (`docker-compose.yaml:441`, SMP). Members therefore see the
>   container address in the run banner.
>
> **Change:** on dial failure, `SafeDialContext` returns a `*DialError{HostPort, Err}`.
> - `HostPort` is the caller's `host:port`; `Err` is the `OpError`'s inner `.Err` (for example,
>   `connect: connection refused` or an i/o timeout).
> - Its `Error()` is `target_url: connect <host>:<port>: <inner>`.
> - It implements `Timeout()` and `Unwrap()` by delegating to the inner error, so `net.Error`
>   semantics hold for any caller.
> - The resolved address goes to the operator log at Debug.
>
> **2. Two more address forms pass `CheckAddr`** (checked with go1.26 `net.IP` and the
> prototype: `TestDC_SIITAndSiteLocalBlocked` fails):
> - IPv4-translated (SIIT) `::ffff:0:a.b.c.d` (`::ffff:0:0:0/96`, RFC 2765/6145). `To4` does not
>   fold it, and it is not in `::/96`.
> - Deprecated site-local `fec0::/10` (RFC 3879). Neither `IsPrivate` nor `IsLinkLocalUnicast`
>   covers it.
>
> **Change:** `embeddedIPv4` also decodes `::ffff:0:0:0/96` (last 32 bits), and step 2 adds
> `fec0::/10` to the outright-blocked list.
>
> **Residual, documented only.** A NAT64 that uses an operator-chosen network-specific prefix
> (RFC 6052 §2.2) cannot be recognised generically. The guide (§6) states that operators running
> DNS64/NAT64 with such a prefix must rely on network egress policy.

### 5. P3-26 — redact target URLs everywhere errors are stored or logged (`adapters/delivery/redact.go`, new)

**Helpers.**

```go
func RedactURL(raw string) string                 // "scheme://host[:port]" + "/[redacted]" when any path, query or fragment exists
func RedactError(err error, targetURL string) string
```

- `RedactURL` drops userinfo. Unparseable input gives `[unparseable-url]`.
- `RedactError` walks the `*url.Error` chain and re-renders each level as
  `Op "RedactURL(URL)": <inner>`. That covers the request URL and any redirect target. As a
  backstop, it also replaces the raw target, its `strconv.Quote` form, its path and its raw query
  in the final string.

**Applied at:**

- `webhook_dispatcher.go`: the URL-rejected log and dead-letter reason, the request-build log and
  reason, and the `client.Do` log line plus the `handleFailure` message (`:200-212`, `:241-245`).
  Every log attribute is the redacted string, never the raw `err`.
- `targeturl.go:87-90`: parse errors omit the URL.
- `handlers_subscriptions.go:342-345`, `:359-366`: the test endpoint's JSON `error` and its Warn
  log.
- `bootstrap.go:110`: `target_url` is logged as `RedactURL(cfg.EventURL)`.

**Scope.** Redaction happens at write time. Rows written before this task are not rewritten (owner
question 2). Under §2 they now reach `dead` and are reaped by retention after 7 days.

### 6. Documentation

- **`docs/integration-guide.md` §6.1** (`:350-383`):
  - a "Signature versions" subsection: v1 and v2 header tables, the exact MAC input, how to opt in
    through `metadata.signature_version` (object form), and a receiver checklist (both headers
    present, decimal timestamp within ±5 min, hex nonce of 16 or more bytes deduplicated per
    receiver, `hmac.Equal`, v2 headers ⇒ v2 MAC only);
  - the "Retry and failure handling" list corrected:
    - non-retryable → `dead`;
    - 408 and 425 retryable;
    - orphaned or misconfigured subscriptions → `dead`;
    - circuit-held rows deferred to the next probe, without consuming attempts, up to
      `MaxCircuitHold`.
    The current text ("marked `failed`", "skip the subscription rather than queueing") is wrong
    today.
  - the metadata example fixed to the real key `pinned_spki_sha256` (the guide shows
    `spki_sha256`).
- **`CHANGELOG.md`**: an entry under "Changed / Security".

> **Design-check note (2026-10-06) — documentation additions.**
> - **The pin example is wrong twice, in two places.** Both `:115` and `:358` use `spki_sha256`
>   and both say the value is **base64**. `parseSubscriptionTLS` reads `pinned_spki_sha256` and
>   hex-decodes it (`tls.go:28`, `:59-62`). Fix the key and the encoding (lowercase hex) at both
>   lines. A value pasted from today's docs fails `ValidateSubscriptionMetadata` with a 400, which
>   is correct.
> - **Fair claim.** Document the round-robin claim across subscriptions (§2a).
> - **Circuit holds.** Document the hold edges from §2: an old row replayed or redriven while
>   the circuit is open is dead-lettered without an attempt, and probes consume attempts.
> - **Receiver checklist.** Add "use a distinct secret per subscription". v2 binds neither the
>   URL nor the subscription, so a shared secret allows cross-endpoint replay inside the window.
> - **NAT64.** Add the network-specific-prefix residual from §4.

### 7. SMP impact, and what SMP TASK-006 must change

**Compile.** The changes are additive, plus one method on an interface SMP does not implement.
SMP was built (`go build -mod=mod ./cmd/... ./internal/...`) against the prototype of this task,
and the delivery-adjacent unit suites passed:

- `internal/customer/eventdelivery`;
- `stepevents`;
- `runner`;
- `spinecore`;
- `workspace`.

SMP's local `vendor/` directory is gitignored and shadows `../spine` in local builds. Test
against a Spine branch with `-mod=mod` or after `go mod vendor`. CI has no vendor directory.

Caution: with `-mod=mod`, name packages explicitly (`./cmd/... ./internal/...`), never `./...`.
`./...` also matches the Go files under `tmp/`, and `-mod=mod` then rewrites SMP's
`go.mod`/`go.sum`. This happened once in this design phase and was reverted to `HEAD`.

**Behaviour SMP inherits at its first `.spine-revision` bump past this task's merge commit,
whatever the opt-in:**

- **B1. A 4xx from SMP's receiver now dead-letters on the first attempt.** Today it hot-loops
  until it succeeds. SMP relies on the hot loop in two places:
  - **Credential rotation window.** `worker.go:113-121` and `cmd/customer-app/secrets_cli.go:136-151`
    (SMP) state that every delivery 401s until the periodic re-align (`secretRealignInterval = 1
    min`, `worker.go:122`) and that events "are not lost" because of the hot loop. After this task
    those step dispatches go `dead`. They appear in the run page's dispatch-failure banner and can
    be redriven by hand (`dispatchstore.go:72-83`, SMP), but they are not delivered automatically.
  - **Transient receiver failures answered with 401:**
    - `no_secret_db`, from a secret-store lookup error (`event_receiver.go:612-615` → `:392`, SMP);
    - `replay_nonce_record_failed`, from a nonce-store DB error (`:468`, SMP).

    Today these hot-loop until the DB recovers. After this task they dead-letter.
- **B2. A circuit-held row keeps its retry budget.** It is no longer inflated and then
  dead-lettered by its first real failure. Rows held behind an open circuit are delivered once the
  probe succeeds, for up to 24 h. This is strictly better for SMP than today.
- **B3. Orphaned, malformed and URL-rejected rows go `dead`** instead of hot-looping. SMP's
  stuck-dispatch predicate already counts `dead` (`dispatchstore.go:79`, SMP), so the run page
  shows them.
- **B4. `last_error` text for transport errors is redacted** (`Post "http://host:port/[redacted]":
  …`). SMP shows `last_error` on the run page (`StuckStepDispatch.LastError`), and its target URL
  carries no secret, so only the text changes.

> **Design-check note (2026-10-06) — inherited behaviour, completed.**
> - **B2 nuance.** "Strictly better" holds against today, where every hold burns an attempt.
>   During an outage that keeps SMP's circuit open, the oldest held `step_assigned` row is still
>   the probe each cycle (§2, "Probes are real attempts"). About one row per three to five probe
>   cycles therefore reaches `dead` and appears in the run banner for a manual redrive. This is
>   also the path after R1 turns `no_secret_db` and `replay_nonce_record_failed` into 503s. Today
>   those 401s hot-loop and lose nothing, but they starve the workspace's dispatch while they do.
> - **B4 extended.**
>   - Resolver failures now write the fixed `resolve subscription: temporarily unavailable`
>     instead of the pgconn or decrypt text (§2 note).
>   - Connect failures now name `host:port` instead of the container IP (§4 note).
>   - No SMP code matches on either text. SMP TASK-006 matches only `HTTP 401`, which I8 keeps.
> - **B5 (new) — clock skew under v2.** After R3, a receiving replica whose clock is more than
>   5 minutes off answers `401 replay_window_exceeded` to every v2 delivery, and those deliveries
>   dead-letter on the first attempt. Today v1 has no timestamp, so this cannot happen. NTP sync
>   across SMP replicas becomes a deployment prerequisite, and SMP TASK-006 should say so.
>   Answering 503 for the skew case is an option for TASK-006: the status code shown to a
>   rejected attacker replay does not matter.
> - **B6 (new) — fair claim (§2a).** No effect on SMP: there is one subscription per runtime DB.
> - **B7 (review round 1) — whole-queue holds.** An open circuit, or an unreadable subscription,
>   holds the subscription's whole due queue in the runtime DB until the next attempt. With
>   several SMP replicas the breaker is per replica, so a replica-local fault (its circuit open,
>   or no encryption key) delays `smp-internal`'s due rows for the other replicas too, by up to
>   the 60 s recovery time per cycle; rows that arrive between holds stay claimable at once.
> - **B8 (review round 1) — unreadable subscription.** A transient resolver failure is retried
>   with backoff and then every minute (today: every poll), still counts attempts (so the run
>   banner shows it once `attempt_count` reaches the budget, as today), and is delivered
>   automatically when the fault clears; it goes `dead` only after 24 h. R2 needs no extension.
> - **B9 (review round 1) — listed in Rule 4.** A TLS-config error dead-letters (today: `failed`,
>   retried every poll), and the 24 h `MaxCircuitHold` cap makes held rows terminal (today they
>   never became terminal from holding alone). Both, with B7 and B8, are now in the initiative's
>   Rule 4 TASK-010 row and in the CHANGELOG's "For embedders" list.
> - **R5 contract test (review round 1).** Its `contractQueue` fake implements
>   `store.DeliveryStore` method by method, so the committed copy must add
>   `HoldDueDeliveries` (a no-op `return nil, nil` is enough for the contract). SMP's own tree
>   implements no `DeliveryStore` and is unaffected.
> - **Line reference.** The periodic re-align tick is `worker.go:407` (SMP). `:508` is
>   `ensureSigningSecret`'s definition. The blocking pre-dispatch ensure is `:375-390`.
> - **R2 and R3 status in SMP TASK-006** (`batch7/E4-TASK-006-…md`).
>   - R3 runs both in the blocking ensure and on every periodic tick. That self-heals a
>     read-modify-write race in which an older replica's `AlignSubscriptionSecret` or
>     `ensureScopedTargetURL` writes back v1 metadata it read before the opt-in.
>   - R2 re-queues dead rows whose `LastError == "HTTP 401"` and whose `created_at` is within
>     1 hour, but only on a pass that changed something.
>   - The queue has no dead-at column. So R2 misses two cases: a 401 that lands just after that
>     pass, and a row older than 1 hour that was held and then 401'd. Each is a narrow window,
>     and the row stays redrivable from the banner. Dual-key verification is the lossless
>     alternative, and choosing it remains TASK-006's call.

> **Review-round-2 note (2026-10-07) — inherited behaviour, amended.**
> - **B3 extended (B10).** An orphaned, malformed or URL-rejected row (and a TLS- or
>   signing-config failure) now dead-letters the subscription's whole due queue in one statement,
>   not one row per claim. SMP has one subscription per runtime DB and reaches none of these
>   verdicts in normal operation (it never deletes `smp-internal`, writes its metadata through
>   bootstrap, and allowlists its own target). Where one is reached, the end state equals
>   per-row dead-lettering — every due row `dead` within `N / 5` polls — except for a fix that
>   lands mid-drain.
> - **B8 and B9 amended.** The 24 h cap counts from the later of `created_at` and the start of the
>   hold on that replica (circuit opening, first failed read), in memory. An SMP replica restarted
>   with the wrong encryption key therefore retries an aged backlog for 24 h instead of
>   dead-lettering it at the first failed read, and a workspace re-warmed after a day keeps its
>   aged rows through a fresh outage. This moves held rows' terminal time later, toward today's
>   behaviour (held rows never become terminal from holding alone). The Rule 4 row says so.
> - **R5 handoff.** Round 2 adds no `DeliveryStore` method. The committed contract test still needs
>   the round-1 addition to its `contractQueue` fake, which the prototype under `_work/` and the
>   SMP TASK-006 design do not yet carry (reviewer round 2):
>   `func (q *contractQueue) HoldDueDeliveries(context.Context, spinestore.DeliveryHold) ([]spinestore.DeliveryEntry, error) { return nil, nil }`.

**Required SMP changes (SMP TASK-006).**

- **R1 — Transient receiver failures answer 503.** `no_secret_db` and
  `replay_nonce_record_failed` must answer 503 so Spine's backoff applies. Signature mismatches,
  replay rejections and malformed headers stay 401.
- **R2 — The rotation window loses nothing.**
  - Recommended: when `AlignSubscriptionSecret` reports `changed == true` (the periodic re-align
    in `worker.go:508`, and `alignSubscriptionAfterRotate` in `secrets_cli.go:151`), re-queue that
    workspace's `smp-internal` rows that went `dead` with `last_error = 'HTTP 401'` since the
    rotation. Reuse the redrive UPDATE shape of `dispatchstore.go:135`.
  - Alternative: dual-key verification for a grace window after rotation.
  - SMP TASK-006 chooses.
- **R3 — Opt in.**
  - New workspaces: pass `SignatureVersion: "v2"` in both `BootstrapInternalSubscription` calls
    (`provision_steps_runtime.go:480-486`, `worker.go:494`).
  - Existing rows: add a pre-dispatch ensure beside `ensureSigningSecret` (`worker.go:508`) that
    applies `delivery.WithSignatureVersion(…, "v2")` and `UpdateSubscription`, so no dispatcher
    starts on a v1 row.
- **R4 — Require v2.** On the per-caller path, reject v1 MACs (the "require v2 step-event
  signatures" item of SMP TASK-006). Do it only after R3 has run everywhere: with several
  replicas, an old replica keeps sending v1 until it is replaced. Update the worker-test receivers
  (`worker_test.go:378`, `:432`, SMP) to verify v2.
- **R5 — Commit the contract test.** Commit
  `internal/customer/runner/spine_webhook_v2_contract_test.go`; the prototype is
  `_work/task010/smp/spine_webhook_v2_contract_test.go` (AC7). SMP CI's `go test` job, run against
  the pinned `.spine-revision`, is then the compatibility gate.
- **R6 — Correct the stale comments** that describe the hot loop:
  - `event_receiver.go:402-410`;
  - `worker.go:113-121`;
  - `secrets_cli.go:136-151`;
  - `routes_public.go:707-710`.

  SMP's choice of 503 over 4xx on those routes stays correct.

**Ordering hazard.** Spine `main` is linear. Any SMP bump past this task's merge commit, whichever
SMP task performs it (TASK-006, 008, 009, 010, 011 or 012), inherits B1. **R1 and R2 must
therefore be merged in SMP before the first such bump.** The safe sequencing is either:

- SMP TASK-006 lands before any other SMP bump task, with R1 and R2 as its first commit; or
- R1 and R2 move into whichever SMP task bumps first.

R1 and R2 are safe on the old Spine too. A 503 rides the old backoff, and the R2 requeue finds
nothing, because the old Spine never dead-letters a 401.

### 8. Regression tests from the audit PoCs (committed in this task)

Each PoC below becomes a committed test with its assertion inverted. In this design phase each one
was run against `61994fe` with `-overlay` and **fails there**, then run against the prototype and
**passes**. The overlay JSONs are in `_work/task010/`.

| Audit PoC | Committed test (prototype file in `_work/task010/regression/`) | 61994fe | prototype |
|---|---|---|---|
| `poc/delivery-scheduler/poc_hotloop_test.go`: `Permanent4xxHotLoopStarvesQueue` | `adapters/delivery/queue_regression_test.go` `TestNonRetryable4xxDeadLettersAndDoesNotStarve` (`zz_t010_queue_regression_test.go`) | FAIL | PASS |
| same file: `RetryableFailureCircuitOpenStarvesQueue` | `TestCircuitOpenDefersWithoutStarvingOrInflating` | FAIL | PASS |
| same file: `DeletedSubscriptionRowsPoisonQueue` | `TestOrphanedRowsDeadLetter` | FAIL | PASS |
| same file: `HalfOpenProbeEarlyReturnWedgesSubscription` | `TestHalfOpenProbeEarlyReturnDoesNotWedge`, `TestHalfOpenProbeRejectedURLReleasesProbe` | FAIL | PASS |
| `poc/gap1-crypto-secrets-reaud/dispatcher_replay_poc_test.go` | `adapters/delivery/signature_regression_test.go` `TestV2OptInSignsFreshTimestampAndNonce`, `TestUnknownSignatureVersionFailsClosed`, `TestV2WithoutSecretFailsClosed`; guard `TestV1RemainsDefault` | FAIL (guard passes on both) | PASS |
| `poc/verify-r1-10/poc_queue_starvation_test.go` (three scenarios, real Postgres) | `adapters/delivery/queue_integration_test.go` (`//go:build integration`, `store.NewTestStore`) `TestPermanent4xxDeadLettersAndIsReaped`, `TestCircuitOpenDoesNotStarve`, `TestOrphanedRowsDeadLetter` (`zz_t010_queue_pg_regression_test.go`) | FAIL (3/3 runs) | PASS (3/3 runs) |
| none (finding text, P3-25 / P3-26) | `TestTransitionAddressesBlocked`, `TestValidatorDoesNotReportResolvedAddresses`, `TestSecretURLPartsRedacted` (`zz_t010_ssrf_redact_regression_test.go`) | FAIL | PASS |

- **Compilation against `61994fe`.** The opt-in tests go through the real
  `StoreSubscriptionResolver` and subscription metadata, and the queue fakes only add a method. So
  every test above compiles against `61994fe` and fails on its assertions, not on a build error.
- **`poc/gap1-crypto-secrets-reaud/delivery_store_poc_test.go` is not this task's.** Its three
  tests prove P3-1 and P3-10 (secrets at rest), which belong to **TASK-009**. TASK-010 only reuses
  its fake-querier pattern, to drive the real `PostgresStore → StoreSubscriptionResolver` path in
  AC5.
- **Existing tests that encode the old behaviour must change:**
  - `webhook_dispatcher_test.go:304-345` expects `failed` and must expect `dead`;
  - `targeturl_test.go:46` expects "loopback" for `https://localhost/hook` and must expect
    "non-public address";
  - `targeturl_test.go:175-176` expects "loopback" in the dial error and must expect
    `ErrBlockedDestination`.

  These are the only three Spine tests that fail on the prototype (full `go test ./adapters/...
  ./core/... ./service/...`, this design phase).

> **Design-check note (2026-10-06) — verification and additional regressions.**
>
> **Re-run in this design check.**
> - `overlay-regression-proto.json`: all 13 `TestT010_*` pass.
> - `overlay-regression-head.json`: 12 tests run on `61994fe` (the golden file needs `SignV2`
>   and is not overlaid). 11 fail, and the guard `TestT010_V1RemainsDefault` passes.
> - `overlay-proto.json`: in `adapters/delivery`, exactly the three named existing tests fail.
> - The SMP contract test passes against the prototype through the vendor overlay with no
>   `-mod=mod`, and SMP's tree stays clean. It fails at HEAD with `not a v2 delivery`.
> - The prototype covers only `adapters/delivery` and `adapters/store`. The gateway half of §1/§4/§5
>   is not prototyped. The existing gateway test that sends base64 metadata
>   (`handlers_subscriptions_test.go:474`) must keep passing through the compatibility path.
>
> **Commit these tests too.** Prototypes are in `_work/task010/designcheck/`, run with
> `overlay-designcheck-proto.json`. The first three fail on the prototype as designed and must pass
> after the amendments.
>
> | Design-check probe | Committed test | prototype as designed |
> |---|---|---|
> | `TestDC_HeldBacklogStarvesHealthySubscription` (400 held rows ⇒ starved 900/900 polls) | `queue_regression_test.go` `TestHeldBacklogDoesNotStarveOtherSubscriptions`; the fake mirrors §2a | FAIL |
> | `TestDC_DialErrorLeaksResolvedAddress` | `targeturl_test.go` `TestDialErrorOmitsResolvedAddress`, plus the gateway subscription-test case | FAIL |
> | `TestDC_SIITAndSiteLocalBlocked` | rows added to `TestTransitionAddressesBlocked` | FAIL |
> | `TestDC_ReleaseAfterOtherPermitClosedCircuit` | `circuit_breaker_test.go` `TestPermitReleaseAfterOtherPermitClosed` (guard) | PASS |
> | `TestDC_FairClaimClosesHeldBacklogStarvation` (100/400/5000 held rows) | folded into the first row | PASS with the fair fake |
> | `fairclaim.sql` (PostgreSQL 17) | AC11 scenario 4 in `queue_integration_test.go` | n/a |
>
> `TestDC_RedirectForwardsV2Headers` is informational only. It confirms that a 307 forwards a
> fresh v2 signature and nonce to the redirect target, which supports the redirect follow-up.

## Decisions

1. **DECIDED — The opt-in lives in subscription metadata, not a new column or a global flag.**
   - *Rejected: a `signature_version` column.* It needs a runtime migration, and SMP vendors
     Spine's runtime migrations (`internal/customer/workspace/spinemigrations/`, SMP).
   - *Rejected: a dispatcher-wide env switch.* One dispatcher serves SMP's receiver and arbitrary
     external receivers. Flipping all of them would break receivers outside SMP, which the
     programme forbids.
2. **DECIDED — v2 is SMP's existing format exactly** (`X-Spine-Signature: v2=`, `X-Webhook-Timestamp`,
   `X-Webhook-Nonce`, MAC over `ts.nonce.body`).
   - *Rejected: a Stripe-style single header `t=…,v1=…`.* SMP's receiver and its tests would have
     to change, and so would every out-of-tree verifier.
   - *Rejected: adding the delivery id or the URL to the MAC input.* SMP's format is fixed. The
     event id is already inside the signed body. SMP binds the target workspace through the URL
     plus a per-workspace key.
3. **DECIDED — Sign at send time, every attempt; persist nothing.**
   - *Rejected: storing ts/nonce on the row.* A retry later than 5 minutes would fail the skew
     window, and a stored nonce makes the retry look like a replay.
4. **DECIDED — v2 sends only the v2 MAC.**
   - *Rejected: v1 and v2 together.* SMP rejects a v1 MAC when the v2 headers are present, and one
     header name cannot carry both.
5. **DECIDED — Fail closed on an unknown version, and on v2 without a secret.** The delivery goes
   `dead` and the gateway answers 400. Silently falling back to v1 or to unsigned would defeat an
   operator's explicit request for replay protection.
6. **DECIDED — Use the existing `dead` status for every terminal failure.**
   - *Rejected: a new `rejected` status.* It needs a CHECK-constraint migration
     (`015_…sql:17`), and SMP's stuck-dispatch predicate and the operator replay endpoint already
     treat `dead` as terminal and replayable (`handlers_delivery_history.go:102`).
7. **DECIDED — Circuit holds do not consume attempts.** They are deferred to the next probe time,
   with an age cap.
   - *Rejected: counting a hold as an attempt.* No new store method would be needed, but with the
     5-attempt budget and a 60 s recovery every queued step dispatch would die after about 5
     minutes of receiver outage. That weakens the hold that SMP benefits from today.
   - *Rejected: holding with no cap.* Rows would never become terminal.
8. **DECIDED — `DeferDelivery` is a required `DeliveryStore` method.**
   - *Rejected: an optional interface with fallback.* Test fakes would silently take the
     attempt-bumping fallback and hide regressions. The design-phase prototype dispatcher detects
     the method through an optional interface only so the store interface could stay unedited;
     the Postgres prototype implements the real method.
   - *Rejected: widening `UpdateDeliveryStatus`.* That changes every caller, including the
     gateway's replay.
9. **DECIDED — A deleted subscription dead-letters at once; transient resolver errors back off.**
   - *Rejected: an FK with `ON DELETE CASCADE` on the queue.* It is a schema change, it leaves
     rows already orphaned in place, and it silently erases delivery history on subscription
     delete.
10. **DECIDED — Resolve the breaker with a `Permit` and a deferred `Release`.**
    - *Rejected: counting early returns as endpoint failures.* A runtime-DB blip would then trip a
      healthy endpoint's breaker.
    - *Rejected: a half-open timeout.* It still wedges for the length of the timeout and adds a
      clock policy.
11. **DECIDED — DNS-named targets get a generic message; literal IPs keep the class.** The detail
    goes to the operator log.
    - *Rejected: dropping the create-time DNS check.* It is what gives an early 400 for
      `localhost`-style targets.
12. **DECIDED — Decode NAT64-WKP, 6to4 and IPv4-compatible addresses; reject local-use NAT64 and
    Teredo outright; add `0.0.0.0/8`.** Public NAT64 and 6to4 stay allowed: blocking all of
    `64:ff9b::/96` would break webhooks from IPv6-only deployments.
13. **DECIDED — Redact at write time, keeping scheme, host and port.**
    - *Rejected: read-time redaction in the API.* Logs cannot be redacted at read time.
    - *Rejected: hashing the path.* It adds nothing an operator can use.
14. **DECIDED — The gateway accepts metadata as an object or as legacy base64.** It makes the
    documented form work without breaking a client that learned the current `[]byte` behaviour.
15. **DECIDED — 408 and 425 become retryable.** A misclassified status is now dead-lettered rather
    than hot-retried, so the classification has to be right.
16. **DECIDED (design-check addition, 2026-10-06) — Fair claim in `ClaimDeliveries` (§2a), not
    only per-path fixes.**
    - *Rejected: bulk-deferring a held subscription's whole backlog in one statement.* It is
      cheaper per probe cycle, but it adds a second store method plus a bulk age cap. It also
      protects only against circuit holds, not against the next pattern that leaves old rows
      claimable, such as a large legitimate backlog draining.
    - *Rejected: excluding open-circuit subscriptions from the claim.* The breaker is in-memory
      and per process, so the claim would need a new parameter. Each SMP replica has its own
      breaker. The age cap would also need a separate sweep.
    - *Rejected: jittering the deferral time.* It only moves the threshold. It does not remove it.
17. **DECIDED (design-check addition, 2026-10-06) — Caller-facing errors name the caller's
    `host:port`, never a resolved address, never infrastructure error text.** The dialer's
    `*net.OpError` and the resolver's DB or decrypt error are re-rendered at the source. The
    detail goes to the operator log. Read-time redaction is rejected for the same reason as in
    Decision 13.

## Owner questions

The design works without answers, using the stated defaults.

1. **How long may an open circuit hold a delivery (`MaxCircuitHold`)?**
   - *Default:* 24 hours. Then the row goes `dead` and is replayable.
   - Shorter dead-letters SMP step dispatches during long receiver outages, which then need a
     manual redrive. Longer keeps rows queued for days.
   - The value is code-level config (`DispatcherConfig.MaxCircuitHold`). No env variable is added
     unless asked.
   - *Design-check note (2026-10-06):* the cap is measured from `created_at`. A row older than
     the cap that is replayed or redriven while its circuit is still open is dead-lettered again
     with no attempt (§2 note). The default therefore also decides how long an operator must wait
     for the endpoint to recover before a redrive of an old row can succeed. A per-row "held
     since" column would remove this edge, but it is a schema change (I6). It is not proposed.
   - *Review-round-2 note (2026-10-07):* the cap is now measured from the later of `created_at`
     and the start of the hold as the dispatcher process observed it (circuit opening, first failed
     read), kept in memory (§2 round-2 note). The edge above applies only when the outage itself
     is older than the cap; a restart starts the count again.
2. **Should rows written before this task have their `last_error` / `event_delivery_log.error`
   scrubbed?**
   - *Default:* no rewrite. Those rows now reach `dead` (§2) and retention removes them after its
     window (7 days by default).
   - The task's Implementation Notes record a one-off `UPDATE … SET last_error =
     regexp_replace(…)` that an operator can run. Rewriting history in a migration would be a
     runtime migration that SMP vendors.
3. **Should a future release make v2 the default for new subscriptions created through the
   standalone API?**
   - *Default:* not in this task. v1 stays the default, as the programme requires, and the guide
     recommends v2.
   - Revisit once external receivers have had a release to adopt it.

## Acceptance Criteria

**Signatures (P3-11)**

1. **v2 wire format.** For a subscription whose metadata contains `"signature_version":"v2"`,
   `TestV2OptInSignsFreshTimestampAndNonce` delivers the same event twice through the real
   `StoreSubscriptionResolver` and asserts the following.
   - **Signature header:** `X-Spine-Signature` starts with `v2=`. It equals the hex HMAC-SHA256 of
     `ts + "." + nonce + "." + body`, computed by an independent in-test oracle (not `SignV2`)
     that mirrors SMP's `hmacMatchesAny` v2 branch.
   - **Timestamp:** `X-Webhook-Timestamp` is canonical decimal Unix seconds
     (`FormatInt(ParseInt(x)) == x`) and within ±5 s of the test's clock at receipt.
   - **Nonce:** `X-Webhook-Nonce` hex-decodes to 16 or more bytes.
   - **Freshness:** the nonces differ and the signatures differ across the two attempts.
   - **Mutations:**
     - dropping the nonce from the MAC fails this test;
     - a constant nonce fails it;
     - a timestamp taken an hour early fails it.
2. **Golden vectors** (`signature_test.go`).
   - `` SignV2("whsec_test", "1700000000", "000102030405060708090a0b0c0d0e0f", []byte(`{"event_id":"evt-1"}`)) ``
     equals
     `v2=7259d9ee00d6dfc7d6cd0be62fac13dada610df1455f5bf2b23866fa5949b288`.
   - `SignV1("whsec_test", …same body)` equals
     `sha256=b5fb135192a36cba13b173dac6aadcc588b98253b1f45e016139e5208503c61e`.
   - Both values were computed independently in the design phase and match the prototype.
3. **v1 unchanged by default.** For metadata absent, `{}`, `{"source":"bootstrap"}` and
   `{"signature_version":"v1"}`, `TestV1RemainsDefault` sees exactly
   `sha256=<HMAC(body)>` and no `X-Webhook-*` header. The existing
   `TestWebhookDispatcher_SuccessfulDelivery`, `TestWebhookDispatcher_NoSignatureWithoutSecret`
   and `TestComputeHMAC` pass **unmodified**.
4. **Fail closed.**
   - Metadata `{"signature_version":"v3"}`, a non-string version, or v2 with an empty secret:
     the endpoint receives nothing and the row is `dead` with an explanatory `last_error`.
   - `POST`/`PATCH /api/v1/subscriptions` with such metadata answers 400 `invalid_params`.
   - Both the object form and the base64-string form of valid metadata are accepted and stored
     identically.
   - `subscriptionResponse` reports `signature_version`.
   - Mutation: making v2 with an empty secret send unsigned fails `TestV2WithoutSecretFailsClosed`.
5. **Metadata helpers.**
   - `` WithSignatureVersion([]byte(`{"source":"bootstrap","tls":{"pinned_spki_sha256":"ab"}}`), "v2") ``
     preserves `source` and `tls` and sets the key.
   - `SignatureVersionOf` returns `v1` for absent or empty.
   - The real `PostgresStore`, driven by the fake querier of
     `poc/gap1-crypto-secrets-reaud/delivery_store_poc_test.go`, plus `StoreSubscriptionResolver`,
     yields `SubscriptionDetail.SignatureVersion == "v2"` for a v2 row.
6. **Bootstrap.**
   - With `BootstrapConfig.SignatureVersion = "v2"`, a fresh bootstrap writes
     `{"source":"bootstrap","signature_version":"v2"}`.
   - A second call with an unchanged config issues no `UpdateSubscription`.
   - Switching an existing row from v1 to v2 rewrites only the version key.
   - With `SignatureVersion = ""` the metadata is never touched, and all existing
     `bootstrap_test.go` cases pass unmodified.
   - *Design-check addition (2026-10-06):* an existing `smp-internal` row with unparseable
     metadata and `SignatureVersion = "v2"` is rewritten to
     `{"source":"bootstrap","signature_version":"v2"}`, and bootstrap returns nil.
7. **Contract with SMP's receiver.**
   - `internal/customer/runner/spine_webhook_v2_contract_test.go` drives Spine's real
     `WebhookDispatcher` and `StoreSubscriptionResolver`, opted in through metadata, into SMP's
     real `EventReceiver` mounted on a chi router with a per-workspace key and the in-memory
     replay recorder. It asserts:
     - two attempts of one delivery are both accepted, and two distinct nonces are recorded;
     - a verbatim replay of a captured delivery gets 401 `replay_nonce_reused`;
     - a tampered body gets 401;
     - a v1-default subscription is still accepted with no nonce recorded.
   - The test (prototype at `_work/task010/smp/`) is run green against this task's branch from the
     SMP tree with `go test -mod=mod`, and its output is recorded in Implementation Notes.
   - The same test fails against `61994fe` ("not a v2 delivery"). It was verified both ways in
     this design phase.
   - SMP commits it with the `.spine-revision` bump in SMP TASK-006 (§7 R5).

**Queue (P3-5)**

8. **Non-retryable → dead.** `TestNonRetryable4xxDeadLettersAndDoesNotStarve` runs five 410 rows
   ahead of one healthy row over 50 polls. It asserts:
   - the healthy endpoint gets exactly 1 POST;
   - the 410 endpoint gets exactly 5 POSTs;
   - every 410 row is `dead` with `attempt_count == 1`.

   Also:
   - The TLS-config error path ends in `dead`.
   - `TestWebhookDispatcher_RejectsPersistedUnsafeURL` is updated to expect `dead`.
   - Mutation: restoring `status = "failed"` for non-retryable fails this AC.
9. **Circuit holds.**
   - `TestCircuitOpenDefersWithoutStarvingOrInflating`: 15 rows answered 503, then one healthy row
     ⇒ the circuit opens, and the healthy row is delivered.
   - Every held row has `attempt_count == 0` and `next_retry_at` after the store's clock.
   - A held row older than `MaxCircuitHold` goes `dead` (`circuit open: held longer than …`)
     without an HTTP attempt.
   - **Mutations:**
     - making `DeferDelivery` bump the attempt count fails this AC;
     - deferring with `NULL` fails it.

   - *Review-round-2 amendment (2026-10-07):* "older than `MaxCircuitHold`" means held longer
     than the cap, counted from the later of `created_at` and the circuit opening.
     `TestCircuitHoldCapDeadLetters` runs each cap with an outage older than the cap (old row
     `dead`), younger than the cap and just begun (old row deferred);
     `TestCircuitHoldCapCountsFromOutageStart` drives a real 503 outage through failed probes;
     `TestCircuitOpenedAt` pins `OpenedAt`.

   **9a. Fair claim, no starvation at scale** (*design-check addition, 2026-10-06*).
   `TestHeldBacklogDoesNotStarveOtherSubscriptions` sets up a regression fake whose
   `ClaimDeliveries` mirrors §2a, one shared fake clock for store, dispatcher and breaker, and
   N ∈ {100, 400, 5000} rows of a subscription held behind an open circuit (503 endpoint). One new
   healthy-subscription row is enqueued per simulated second for 900 polls.
   - At least 899 healthy rows are delivered, each within one poll of being enqueued.
   - No held row's `attempt_count` exceeds `MaxRetriesDomain`.
   - Mutation: reverting the fake and `ClaimDeliveries` to `ORDER BY created_at` fails it at
     N = 400. (The prototype as designed starves all 900 polls at N = 400.)
   - *Review-round-1 addition (2026-10-07):* the test also runs 4×100, 5×61, 5×80, 6×60, 10×40
     and 10×400 held subscriptions × rows. Every healthy row is delivered within one poll below
     `concurrency` held subscriptions and within `ceil(2 × held / 5)` polls otherwise. Controls:
     FIFO claim with per-row deferral starves at 1×400, and the fair claim with per-row deferral
     starves at 5×61 (0/900, the reviewers' reproduction). A real-Postgres scenario with 6×100
     held rows delivers a new row within 3 polls.
   - *Review-round-2 addition (2026-10-07):* `TestDeadLetteredBacklogsDoNotStarveOtherSubscriptions`
     runs 4, 5, 6 and 10 subscriptions × 300 rows whose deliveries can never succeed (deleted,
     invalid metadata, refused URL), one healthy row per simulated second for 600 polls: every
     healthy row is delivered within `ceil(k / 5)` polls and every bad row is `dead` with one log
     row. Control: per-row dead-lettering waits 300 polls at 5×300.
     `TestPermanentVerdictDeadLettersWholeDueQueue` covers all seven verdicts. A real-Postgres
     scenario with 6 deleted subscriptions × 100 rows delivers a new row within 2 polls.
10. **Orphans and resolver errors.**
    - `TestOrphanedRowsDeadLetter`: a `not_found` resolver for five rows ⇒ all five are `dead`,
      the healthy row is delivered, and the resolver is called at most six times over 100 polls.
    - A transient resolver error schedules backoff (`next_retry_at` in the future) and is `dead`
      once `MaxRetriesDomain` is reached.
      *Review-round-1 amendment (2026-10-07):* it is not `dead` at the budget; past it the row
      stays `failed` with `attempt_count` ≥ the budget, is retried every minute, is delivered once
      the resolver recovers, and goes `dead` only after `MaxCircuitHold` (§2 review-round-1 note).
      *Review-round-2 amendment (2026-10-07):* "after `MaxCircuitHold`" means after the
      subscription has been unreadable for that long (from this dispatcher's first failed read of
      the current run), not after the row has been queued that long.
      `TestAgedBacklogSurvivesTransientFaults` (20 rows queued 25 h ago, one failed read: 0 dead,
      20 delivered) and `TestUnreadableSubscriptionHoldsBacklog` (an aged row is retried, then
      `dead` once unreadable for 24 h 1 min) distinguish the two.
    - Invalid metadata ⇒ `dead`.
    - Mutation: retrying `not_found` fails this AC.
    - *Design-check addition (2026-10-06):* a transient resolver error carries the text
      ``failed to connect to `user=u database=d`: 10.9.8.7:5432 …``. `last_error` and the log
      row's `error` must both equal `resolve subscription: temporarily unavailable`, with no
      `10.9.8.7`, `user=` or `database=`. The full text appears only in the captured operator
      log. Mutation: writing `err.Error()` fails this AC.
11. **Real-Postgres regression.** `adapters/delivery/queue_integration_test.go`
    (`//go:build integration`, `SPINE_TEST_DATABASE_URL`) passes under `make test-integration`.
    1. **410 scenario:** smp-internal gets 5 POSTs, the partner gets 5, and the partner rows are
       `dead` with attempt 1. `DeleteExpiredDeliveries(now+1h)` then deletes all 10 rows.
    2. **503 scenario:** the circuit opens, then the open circuit handles 5 partner rows, then 5
       new healthy rows are all delivered.
       - Max partner `attempt_count` ≤ `MaxRetriesDomain`.
       - No partner row is claimable at once.
    3. **Orphan scenario:** all orphans are `dead`, and smp-internal gets 5 POSTs.
    4. **Fair-claim scenario** (*design-check addition, 2026-10-06*): 400 claimable rows of
       subscription A, all older than one row of subscription B, plus one A row deferred an hour
       into the future.
       - The first `ClaimDeliveries(5)` returns B's row.
       - It never returns the deferred row.
       - With only A's rows present, it returns them in `created_at` order.

    The same file fails on `61994fe` (verified 3/3 runs in this design phase). Scenario 4 also
    fails on `61994fe`.
12. **`DeferDelivery`.**
    - A store integration test shows: `status` becomes `pending`, `next_retry_at` is the given
      instant, `last_error` is the reason, `attempt_count` is unchanged, and a row not in
      `delivering` is left untouched.
    - Every `DeliveryStore` fake is updated.
    - `go build ./...` passes in Spine.
    - SMP builds against the branch with `go build -mod=mod ./cmd/... ./internal/...`.
13. **408 and 425 are retryable** (the `retry_test.go` table); 400, 401, 403, 404 and 410 are not.

**Breaker (P3-12)**

14. **Probe resolution.**
    - `TestHalfOpenProbeEarlyReturnDoesNotWedge`: open circuit past its recovery time, resolver
      fails once ⇒ the row is delivered on a later poll and the breaker is `closed`.
    - `TestHalfOpenProbeRejectedURLReleasesProbe`: the breaker is not `half-open` after a
      URL-rejected probe, and the row is `dead`.
    - Unit tests for `Permit` show:
      - `Release` after `Success` or `Failure` does nothing;
      - `Release` of a non-probe permit does nothing;
      - `Release` of a probe returns the circuit to `open` with `lastFailureAt` unchanged;
      - `NextAttemptAt` returns the probe time while open and `now + fallback` while half-open.
    - The existing `circuit_breaker_test.go` cases pass unmodified.
    - Mutation: removing `defer permit.Release(ctx)` fails both regression tests.
    - *Design-check addition (2026-10-06):* `TestPermitReleaseAfterOtherPermitClosed`:
      1. take a non-probe permit while the circuit is closed;
      2. trip the circuit and age it past recovery;
      3. take a probe;
      4. call the early permit's `Success`, then the probe's `Release`.

      The state must be `closed`. Mutation: making `Release` set `open` unconditionally fails it.

**SSRF validator (P3-25)**

15. **Transition addresses.** `TestTransitionAddressesBlocked` rejects:
    - `64:ff9b::a00:1`, `64:ff9b::7f00:1`, `64:ff9b::a9fe:a9fe`;
    - `2002:a00:1::1`, `2002:7f00:1::`, `2002:a9fe:a9fe::1`;
    - `::10.0.0.1`, `::127.0.0.1`, `::a9fe:a9fe`, `::ffff:10.0.0.1`;
    - `64:ff9b:1::a00:1`, `2001:0:4136:e378:8000:63bf:f5ff:fffe`, `0.1.2.3`.

    It allows `8.8.8.8`, `64:ff9b::808:808`, `2002:808:808::1` and `2606:4700:4700::1111`.
    The existing `TestTargetValidator_CheckAddr_RejectsDangerousRanges` passes unmodified.
    Mutation: removing the embedded-IPv4 step fails this AC.
    *Design-check addition (2026-10-06):* it also rejects:
    - the SIIT forms `::ffff:0:a00:1`, `::ffff:0:7f00:1` and `::ffff:0:a9fe:a9fe`;
    - site-local `fec0::1`.

    It allows `::ffff:0:808:808`. The prototype as designed accepts all four rejected addresses
    (`TestDC_SIITAndSiteLocalBlocked`).
16. **No address in caller-facing errors.** `TestValidatorDoesNotReportResolvedAddresses` asserts:
    - The `ValidateURL("https://localhost/hook")` error contains neither `127.0.0.1`, `::1` nor
      `loopback`.
    - The `CheckAddr` error for `10.11.12.13` does not contain the address.
    - The `SafeDialContext` error for `localhost:443` contains no address.

    Also:
    - A unit test of the DNS-failure formatter, fed a `*net.DNSError{Server: "10.0.0.2:53", …}`,
      gives text that omits `10.0.0.2`.
    - A gateway test shows `POST /subscriptions/{id}/test` against a target that resolves to
      loopback returns an `error` with no address.
    - `targeturl_test.go:46` and `:175-176` are updated as §8 states.
    - Mutation: putting the resolved detail back into the message fails this AC.
    - *Design-check addition (2026-10-06):* `TestDialErrorOmitsResolvedAddress` uses a validator
      that allowlists `localhost` and a closed port. It asserts:
      - the dispatcher's `last_error`, the delivery-log `Error` and the gateway
        subscription-test JSON `error` contain neither `127.0.0.1` nor `[::1]`;
      - the error names `localhost:<port>`;
      - `errors.As(err, &netErr)` still reports `Timeout()` correctly for a dial timeout.

      The prototype as designed leaks `dial tcp 127.0.0.1:<port>`
      (`TestDC_DialErrorLeaksResolvedAddress`). Mutation: returning the dialer's `lastErr` fails
      this AC.
    - *Review-round-1 addition (2026-10-07):* a target that accepts the connection, reads the
      request and resets it gives no `127.0.0.1` / `[::1]` in the validator client's own error,
      `last_error`, the delivery log, the non-Debug operator log lines (default and
      per-subscription TLS client) or the gateway subscription-test JSON and log line; the error
      names `localhost:<port>`, is still a `*net.OpError` and unwraps to `ECONNRESET`.
      Mutation: returning the raw connection fails it.

**Redaction (P3-26)**

17. **Redaction.** `TestSecretURLPartsRedacted` sends a delivery to
    `http://127.0.0.1:1/services/T000/B000/s3cr3tPathToken?token=s3cr3tQueryToken` (connection
    refused). Neither secret appears in:
    - `last_error`;
    - any delivery-log `Error`;
    - the captured `slog` output.

    Also:
    - `ValidateURL("https://exa mple.com/s3cr3tPathToken")` does not echo the path.
    - A gateway test shows the subscription-test endpoint's JSON `error` and its log line omit
      both secrets.
    - A `RedactURL` table covers: path only, query only, fragment only, userinfo, port, root
      path `/` (kept as is), and unparseable input.
    - Mutation: using `err.Error()` on the `client.Do` path fails this AC.

**Gates and documentation**

18. **Documentation.** `docs/integration-guide.md` §6.1 documents:
    - v1 and v2 with the exact MAC input and the receiver checklist;
    - the opt-in in object form;
    - the corrected failure-handling and circuit-hold semantics;
    - the `pinned_spki_sha256` key.

    `CHANGELOG.md` has an entry.
    *Design-check addition (2026-10-06):* `grep -n 'spki_sha256' docs/integration-guide.md`
    shows only `pinned_spki_sha256`, with hex values, at both former sites (`:115`, `:358`). The
    guide also documents everything listed in the §6 design-check note:
    - the fair claim;
    - the two circuit-hold edges;
    - "distinct secret per subscription";
    - the NAT64 network-specific-prefix residual.
19. **Gates.** All of these pass:
    - `go build ./...`, `go vet ./...`, `go test ./...`;
    - `go test -race ./adapters/delivery/... ./adapters/store/...`;
    - `make test-integration` for `adapters/delivery` and `adapters/store`;
    - `./dev/lint-boundaries.sh`;
    - `go build ./cmd/embed-smoketest && ./bin/embed-smoketest`.

    `migrations/` is unchanged, so no runtime schema change is made. The mutation results for
    ACs 1, 4, 8, 9, 10, 14, 15, 16 and 17 are recorded in Implementation Notes; all 12 were killed
    by the prototype tests in this design phase.
    *Design-check addition (2026-10-06):* the mutations added in ACs 9a, 10, 14, 15 and 16 are
    recorded too. The ACs 9a, 15 and 16 additions fail on the prototype as designed, as shown by
    the `TestDC_*` probes in `_work/task010/designcheck/`. The implementer also records the §2a
    claim timing on a 100 000-row claimable backlog.
20. **Stable HTTP outcome text (I8)** (*design-check addition, 2026-10-06*). A dispatcher test
    covers 401 (non-retryable ⇒ `dead`), 503 (retryable, scheduled) and 503 with the budget
    exhausted (`dead`). In each case `last_error` and the delivery-log `error` equal exactly
    `HTTP 401` or `HTTP 503`. SMP TASK-006's rotation requeue depends on `HTTP 401`. Mutation:
    changing the format string fails this AC.

## Out of scope / follow-ups

- **SMP adoption.** R1–R6 in §7 belong to SMP TASK-006. The `.spine-revision` bump belongs to
  whichever SMP task bumps first, subject to the ordering hazard in §7.
- **P3-1 / P3-10** (secrets at rest, `enc:v2`, the re-encryption sweep) and the
  `delivery_store_poc_test.go` PoCs belong to TASK-009.
- **Signed test pings.** `POST /subscriptions/{id}/test` sends an unsigned ping
  (`handlers_subscriptions.go:337-349`). A receiver that enforces signatures rejects it. Follow-up:
  sign the ping in the subscription's configured version.
- **Redirects carry the signature headers.** Go's `http.Client` copies every non-sensitive header
  of the initial request onto each followed redirect, cross-host included (`makeHeadersCopier`).
  Each hop is re-validated by `CheckRedirect`. Follow-up: stop following redirects on deliveries.
- **Unbounded response drain.** `io.Copy(io.Discard, resp.Body)` (`webhook_dispatcher.go:248`) is
  limited only by the 10 s client timeout. Follow-up: cap it, as SMP's own worker does with
  `io.LimitReader(…, 64 KiB)`.
- **Operator replay keeps `attempt_count`.** The replay endpoint (`handlers_delivery_history.go:108`)
  does not reset the count, so a replayed exhausted row is dead-lettered by its first failure.
  SMP's redrive resets it to 0. Follow-up: align the two.
- **Paused subscriptions still deliver queued rows.** The dispatcher ignores subscription status
  for rows already enqueued. This is pre-existing semantics, and the fan-out already skips
  non-active subscriptions.
- **No `claimed_at` column.** Without one, a `delivering` row whose status write was lost is
  never reclaimed (`worker.go:44-55`, SMP). That needs a schema change, which is out of scope
  under I6.
- **Full target URLs in the read API.** `subscriptionResponse` returns the full `target_url`,
  secret parts included, to `subscription.read` (admin). This is by design for editing; a
  redacted list view is a possible follow-up.
- **Batch-barrier head-of-line blocking** (*design-check addition, 2026-10-06; not an audit
  finding*).
  - `poll` waits for every delivery in its batch (`wg.Wait`, `webhook_dispatcher.go:163-171`).
  - An A5 endpoint that answers just under `HTTPTimeout` (10 s), even with a 200, therefore
    throttles the whole workspace's dispatch to about `concurrency` (5) deliveries per 10 s. A slow success
    never trips the breaker, and fair claim does not help, because one slow row per batch is
    enough.
  - Same impact class as P3-5, different mechanism. Follow-up: claim into free semaphore slots
    instead of fixed batches, and give per-subscription in-flight caps. File it as its own task
    if the owner wants it covered.
- **Redirects carry the signature headers.** *Design-check note (2026-10-06):*
  - Confirmed for v2 by `TestDC_RedirectForwardsV2Headers`: a 307 hands the redirect target a
    fresh v2 signature and nonce. If the original receiver issued the redirect before recording
    the nonce, the pair is replayable there for 5 minutes.
  - The follow-up above stands. It is not taken here, because returning `ErrUseLastResponse`
    would dead-letter deliveries to receivers that rely on an http→https or trailing-slash
    redirect today.
- **NAT64 network-specific prefixes** (*design-check addition, 2026-10-06*). These cannot be
  recognised generically. The guide states that egress policy is the control (§4 note).

## Design check

Adversarial check on 2026-10-06 against Spine `61994fe` and SMP `80995559`, read-only on both
repositories. Both trees were clean afterwards, apart from Spine's pre-existing untracked
`.gomodcache/`. The designer's text is kept, and every change is marked *Design-check note /
addition (2026-10-06)* in place. Probes are in `_work/task010/designcheck/`:
`zz_t010_designcheck_test.go` (run with `overlay-designcheck-proto.json`), plus `fairclaim.sql` and
`fairclaim-perf.sql`, which ran on a throwaway PostgreSQL 17.

**Verified, no change needed.**
- **Spine line references.** Every Spine file:line reference in Purpose and §1–§5 was re-read at
  `61994fe` and matches:
  - dispatcher `:175-322`, breaker `:62-86`, `targeturl.go:87-90`, `:129-152`, `:159-207`,
    `:233-244`;
  - `postgres_delivery.go:23-67`, `:281-289`, migration `015:4-18`, `016:13`;
  - `bootstrap.go:61-110`, `subscription_store.go:49-64`, `tls.go:42-65`, `interfaces.go:193`,
    `pgutil.go` `notFoundOr`;
  - gateway `:17-29`, `:75`, `:206`, `:342-345`, `:359-366`, `:378-391`;
  - `permissions.go:86-89`, and both wiring sites.
- **SMP line references.** `event_receiver.go:33`, `:328-371`, `:392`, `:402-410`, `:433-471`,
  `:604-615`, `:648-666`, `:694-722`, plus `worker.go:113-122`, `:416-421`, `:494`,
  `provision_steps_runtime.go:480-486`, `secrets_cli.go:136-151`, `dispatchstore.go:72-146` and
  `routes_public.go:699-714`. The one drift is noted in §7: the periodic tick is `:407`.
- **Golden vectors (AC2).** Recomputed independently with Python `hmac`; both match.
- **Regression claims (§8).** They reproduce: 13/13 `TestT010_*` pass on the prototype, and 11 of
  the 12 that compile at `61994fe` fail there. With `overlay-proto.json`, exactly the three named
  existing tests fail.
- **SMP contract test.** It passes against the prototype through the vendor overlay, with no
  `-mod=mod` and SMP's tree untouched, and fails at HEAD with `not a v2 delivery`.
- **Wire format.** v2 matches SMP's `hmacMatchesAny` byte for byte. The skew check runs before
  verification, the nonce is recorded after the tenant lookup, and the MAC covers the raw header
  strings, so case or sign variants of the nonce or timestamp cannot replay. No other Spine code
  path signs a webhook (grep).
- **Store implementers.** SMP implements no `DeliveryStore`. It only embeds `store.Store`, so
  `DeferDelivery` compiles there. The Spine fakes to update are exactly the six listed in §2.
- **Gateway compatibility.** The only existing gateway test that sends metadata sends base64
  (`handlers_subscriptions_test.go:474`), which the compatibility path keeps. Subscriptions are
  not in `api/spec.yaml`.

**Changed, and why.**
1. **A bypass of the P3-5 fix: starvation at scale** (Purpose note, I7, §2a, Decision 16, AC9a,
   AC11.4).
   - Circuit-held rows share one probe instant and are re-claimed oldest-first, 5 per poll.
   - Above 300 held rows, a healthy subscription is starved indefinitely: 900/900 polls at
     400 rows, against the prototype.
   - Fix: a fair (per-subscription round-robin) `ClaimDeliveries`. It was verified in Postgres 17
     (about 35 ms against 19 ms at 100k claimable rows), and in the prototype at 100, 400 and
     5000 held rows. It is SMP-neutral, because SMP has one subscription per runtime DB.
2. **An I5 / P3-25 leak the design missed: connect errors name the resolved address** (I5(d),
   §4 note 1, Decision 17, AC16).
   - `SafeDialContext` returns the dialer's `OpError`, so an allowlisted DNS name exposes its
     private IP to tenant admins.
   - In SMP, the `customer-app` container IP reaches members via the run banner.
   - Fix: a `DialError` that names the caller's `host:port`.
3. **Infrastructure error text in caller-facing columns** (I5(e), §2 note, AC10).
   - Resolver DB and decrypt errors (pgconn text names host, user and database) went verbatim
     into `last_error`, which SMP members see.
   - Fix: a fixed reason in the columns, with the detail in the operator log.
4. **Two more address forms bypass `CheckAddr`** (§4 note 2, AC15): SIIT `::ffff:0:0:0/96` and
   site-local `fec0::/10`. Both were accepted by the prototype; both are now rejected.
5. **A contract SMP depends on that the design did not pin** (I8, AC20). SMP TASK-006's rotation
   requeue matches `last_error == "HTTP 401"`.
6. **Robustness and clarity.**
   - Bootstrap self-heals unparseable metadata, where it previously wedged SMP's blocking ensure
     (§1 note, AC6).
   - `Release` must be conditional on half-open (§3 note, AC14 guard).
   - The circuit-hold edges are documented (§2 note, Owner question 1).
   - SMP inherited behaviours B2 (probe victims), B4 (new texts), B5 (NTP under v2) and B6
     (fair claim neutral) are spelled out.
   - The guide's pin example is wrong in key **and** encoding at two sites (§6 note, AC18).
   - The P3-26 audience is corrected: API readers already see the full URL (Purpose note).
   - The TASK-009 merge order is noted (§1 note).
7. **Scope recorded, not taken.**
   - Batch-barrier head-of-line blocking by a slow endpoint. It is not an audit finding; it is a
     follow-up candidate for the owner.
   - NAT64 network-specific prefixes.
   - Redirect forwarding of v2 headers, now confirmed by a probe.

## Implementation Notes

*(Implementation, 2026-10-07. Branch `init/INIT-023-TASK-010-webhook-delivery-hardening`, cut
from Spine local `main` `ee3c249` (`61994fe` + the INIT-023 scaffold + the merged TASK-001). Run on
macOS, go 1.26.5. The local PostgreSQL is 17.10 (pgvector); CI uses 18 — no pg18-only feature is
used. The orchestrator closes the task after merge.)*

### Orchestrator rulings and owner-question defaults

Recorded as **defaults pending the owner**:

- **Scope.** Audit P3-11 (Spine half), P3-12, P3-5, P3-25, P3-26. v2 is opt-in per subscription in
  `event_subscriptions.metadata` jsonb; no schema change (`git diff main...HEAD -- migrations` is
  empty).
- **OQ1** — `MaxCircuitHold` is 24 h measured from `created_at`: `DefaultMaxCircuitHold`, overridable
  only in code through `DispatcherConfig.MaxCircuitHold`. No environment variable.
- **OQ2** — rows written before this task are not rewritten. The one-off statements an operator
  can run are below.
- **OQ3** — v1 stays the default signature for new standalone subscriptions; the integration guide
  recommends v2.
- **Batch-barrier head-of-line blocking** is a recorded follow-up (`KNOWN-LIMITATIONS.md` →
  "Webhook Delivery"), not fixed here.

### What was built

| Commit | Change |
|---|---|
| `8dd4b9b` | §1 (P3-11). `adapters/delivery/signature.go` (new): constants, `ErrInvalidSubscriptionMetadata`, `SignV1`/`SignV2`, `ValidateSubscriptionMetadata`, `SignatureVersionOf`, `WithSignatureVersion`, `parseSubscriptionMetadata`, `signRequest`. `SubscriptionDetail.SignatureVersion`; the resolver fills it; `parseSubscriptionTLS` delegates; `computeHMAC` wraps `SignV1`; the dispatcher signs immediately before `client.Do` with injectable `now`/`rand`; `BootstrapConfig.SignatureVersion`. Tests: `signature_test.go` (golden vectors), `signature_regression_test.go`, `metadata_store_test.go`, `bootstrap_signature_test.go`, `regression_fakes_test.go` |
| `13b8779` | §3 (P3-12). `CircuitBreaker.Acquire`, `Permit` (`Success`/`Failure`/`Release`, first resolution wins, `Release` conditional on half-open), `NextAttemptAt`, injectable clock; the dispatcher defers `permit.Release`. Tests: `circuit_breaker_permit_test.go`, `breaker_regression_test.go` |
| `3e4b11c` | §2 + §2a (P3-5). `DeliveryStore.DeferDelivery` + Postgres implementation; fair `ClaimDeliveries`; the outcome table (one `deadLetter` path with a log row; fixed resolver text; circuit holds deferred and capped); 408/425 retryable; every `DeliveryStore` fake updated. Tests: `queue_regression_test.go`, `queue_integration_test.go` (`//go:build integration`); `TestWebhookDispatcher_RejectsPersistedUnsafeURL` now expects `dead`; `retry_test.go` rows |
| `403a37f` | §4 (P3-25). `BlockedAddressError`, `ErrBlockedDestination`, `DialError`, `blockedClass`/`classifyOne`/`embeddedIPv4`, generic DNS-name refusal, DNS-failure formatter, parse errors without the URL. Tests: `ssrf_regression_test.go`; `targeturl_test.go:46` and `:175-176` updated as §8 states |
| `829dc7b` | §5 (P3-26). `adapters/delivery/redact.go` (new): `RedactURL`, `RedactError`; applied in the dispatcher, the gateway subscription test (JSON and log) and the bootstrap log. Tests: `redact_test.go`, gateway `handlers_subscriptions_hardening_test.go` |
| `b6b36b0` | §1 gateway half: `Metadata` is `json.RawMessage`; `decodeSubscriptionMetadata` (object or legacy base64, compacted, validated → 400 `invalid_params`); `subscriptionResponse` reports `signature_version` |
| `b7f76ac` | §6: `docs/integration-guide.md` §2.1 (TLS keys) and §6.1; `CHANGELOG.md`; `KNOWN-LIMITATIONS.md` |
| `1b223ec`, `a884d8c`, `d082e97` | Hardening found in self-review and by the mutation run (below), plus golangci-lint fixes in new code |

### Per-AC evidence

1. **v2 wire format.** `TestV2OptInSignsFreshTimestampAndNonce` (`signature_regression_test.go`): two
   attempts through the real `StoreSubscriptionResolver` with `{"source":"bootstrap","signature_version":"v2"}`;
   canonical decimal timestamp within ±5 s, nonce hex of ≥16 bytes, `v2=` MAC equal to an
   independent oracle (`smpV2MAC`, not `SignV2`), distinct nonces and signatures, rows `delivered`.
   Mutations killed: nonce dropped from the MAC, constant nonce, timestamp an hour early.
2. **Golden vectors.** `TestSignatureGoldenVectors` (`signature_test.go`) pins both values;
   `TestSignRequest/v2` re-derives the v2 vector through `signRequest`.
3. **v1 unchanged.** `TestV1RemainsDefault` (absent, `{}`, `{"source":"bootstrap"}`,
   `{"signature_version":"v1"}`). `TestWebhookDispatcher_SuccessfulDelivery`,
   `TestWebhookDispatcher_NoSignatureWithoutSecret`, `TestComputeHMAC` pass with their bodies
   unmodified (the file's fake gained `DeferDelivery`; gofmt realigned its struct).
4. **Fail closed.** `TestUnknownSignatureVersionFailsClosed` (`"v3"` and `2`: nothing sent, `dead`,
   log row explains), `TestV2WithoutSecretFailsClosed`; gateway `TestSubscriptionCreate_MetadataForms`
   (object and base64 stored byte-identically; 9 invalid forms → 400 `invalid_params`, nothing
   stored) and `TestSubscriptionUpdate_MetadataValidationAndSignatureVersion` (PATCH 400 leaves the
   row unchanged; `signature_version` `v1`/`v2`/`invalid`). The pre-existing base64 test
   `TestSubscriptionUpdate` passes unmodified. Mutations killed: v2 without secret sends unsigned;
   unknown version falls back to v1.
5. **Metadata helpers.** `TestWithSignatureVersion` (AC5 input preserves `source` and `tls`),
   `TestSignatureVersionOf` (absent/empty → `v1`), `TestStoreBackedResolverReportsV2`
   (`metadata_store_test.go`: real `PostgresStore` over the PoC's fake-querier pattern, bootstrap
   → resolver → `SignatureVersion == "v2"`).
6. **Bootstrap.** `bootstrap_signature_test.go`: fresh row writes
   `{"source":"bootstrap","signature_version":"v2"}`; an unchanged config issues no update; v1→v2
   rewrites only the key (and v2→v1); `SignatureVersion = ""` never touches metadata; unparseable
   metadata (`{not json`, `[]`, `"v2"`) is replaced and bootstrap returns nil; an unknown version is
   refused before the store is touched. `bootstrap_test.go` is untouched and passes. Mutation
   killed: empty version rewrites metadata.
7. **Contract with SMP's receiver.** The design-phase contract test
   (`_work/task010/smp/spine_webhook_v2_contract_test.go`) was run from the SMP checkout
   (`/Users/bartoszszymichowski/42sid/i23/s-T010/smp`, SMP `7d4e7cc9`, `../spine` = this worktree)
   with `go test -overlay <scratch>/overlay-contract.json -count=1 -run TestSpineV2Contract -v
   ./internal/customer/runner/` — the overlay adds the file without writing into SMP's tree:
   `TestSpineV2Contract_DispatcherToEventReceiver` PASS (two attempts accepted, two nonces
   recorded, verbatim replay 401 `replay_nonce_reused`, tampered body 401) and
   `TestSpineV2Contract_V1DefaultStillAccepted` PASS. With a second overlay that swaps `main`'s
   `adapters/delivery` sources back in, the same test FAILS with `not a v2 delivery`. No vendor
   directory exists in that checkout, so no `-mod=mod` was needed. SMP commits the file in its
   TASK-006 (§7 R5).
8. **Non-retryable → dead.** `TestNonRetryable4xxDeadLettersAndDoesNotStarve` (50 polls: healthy 1
   POST, 410 endpoint exactly 5, every 410 row `dead`/attempt 1/`HTTP 410`);
   `TestTLSConfigErrorDeadLetters` (dead, no request, breaker failure recorded);
   `TestWebhookDispatcher_RejectsPersistedUnsafeURL` expects `dead`. Mutation killed: `failed` for
   non-retryable.
9. **Circuit holds.** `TestCircuitOpenDefersWithoutStarvingOrInflating` (15×503 + 1 healthy,
   concurrency 1 so the order is exact: circuit open, healthy delivered, the 5 held rows `pending`
   with attempt 0 and `next_retry_at` after the store clock); `TestCircuitHoldCapDeadLetters`
   (default 24 h and a configured 1 h: the aged row is `dead` with
   `circuit open: held longer than …` and a log row, no HTTP attempt; the younger row is
   deferred). Mutations killed: hold via `UpdateDeliveryStatus` (bumps the attempt); defer with a
   zero time; no cap. SQL mutation: `DeferDelivery` bumping `attempt_count` is killed by
   `TestQueuePG_DeferDelivery` and `TestQueuePG_CircuitOpenDoesNotStarve`.
   **9a.** `TestHeldBacklogDoesNotStarveOtherSubscriptions`: N ∈ {100, 400, 5000}, one shared fake
   clock, one healthy row per simulated second for 900 polls — asserted: at least 899/900
   delivered, each within one poll of being enqueued, no held row over `MaxRetriesDomain`. The
   `control: fifo claim starves at held=400` subtest runs the same scenario with the old global
   `created_at` order in the fake and asserts it starves, so the scenario keeps discriminating.
   Mutation killed: fair fake reverted to FIFO. SQL: the old `ORDER BY created_at` claim fails
   `TestQueuePG_FairClaim` (first claim of 5 is all A rows).
10. **Orphans and resolver errors.** `TestOrphanedRowsDeadLetter` (5 orphans `dead` with
    `subscription not found`, healthy delivered, resolver called ≤ 6 times in 100 polls);
    `TestTransientResolverErrorBacksOffWithFixedText` (future retry, `dead` at
    `MaxRetriesDomain`; `last_error` and every log row equal
    `resolve subscription: temporarily unavailable`, none contains `10.9.8.7`, `user=`,
    `database=`; the full text is in the captured operator log); invalid metadata → `dead`
    (AC4 tests). Mutations killed: retrying `not_found`; `err.Error()` in the columns.
11. **Real Postgres.** `adapters/delivery/queue_integration_test.go`: `TestQueuePG_Permanent4xxDeadLettersAndIsReaped`
    (5+5 POSTs, partner rows `dead`/attempt 1, retention deletes 10), `TestQueuePG_CircuitOpenDoesNotStarve`
    (circuit opens, ≥5 rows held, 5 new internal rows delivered, max attempt ≤ budget, the sum of
    partner `attempt_count` equals the partner's real POSTs, no partner row claimable at once),
    `TestQueuePG_OrphanedRowsDeadLetter`, `TestQueuePG_FairClaim` (scenario 4: B's newer row in the
    first claim of 5, the deferred row never claimed, single-subscription claim = the 5 oldest).
    `RETURNING` order is not claim order, so claimed ids are compared as sets. All pass on
    `s010_delivery` and `s010_all`.
12. **`DeferDelivery`.** `TestQueuePG_DeferDelivery`: `pending`, the given instant, the reason,
    attempt count unchanged; `pending`/`failed`/`dead`/`delivered` rows untouched (an SQL mutation
    dropping the `status = 'delivering'` guard is killed). Fakes updated: `webhook_dispatcher_test.go`
    (both), `subscriber_test.go`, `retention_test.go`, `service/cmd/spine/stubstore_test.go`,
    `service/internal/gateway/stubstore_test.go` (the gateway's `subFakeStore` inherits it). `go build ./...`
    passes; SMP builds against the branch (below).
13. **408/425.** `TestIsRetryable` gained `408`, `425` (true) and `410` (false) rows; 400/401/403/404
    stay false. Mutation killed.
14. **Probe resolution.** `TestHalfOpenProbeEarlyReturnDoesNotWedge` (delivered, breaker `closed`),
    `TestHalfOpenProbeRejectedURLReleasesProbe` (not half-open, row `dead`); `circuit_breaker_permit_test.go`:
    `Release` after `Success`/`Failure` is a no-op, non-probe `Release` is a no-op, probe `Release` →
    `open` with `lastFailureAt` and the failure count unchanged and an immediate re-probe,
    `NextAttemptAt` (open → probe time, half-open and unknown → now+fallback),
    `TestPermitReleaseAfterOtherPermitClosed` (stays `closed`). `circuit_breaker_test.go` is
    untouched and passes. Mutations killed: removing `defer permit.Release(ctx)` fails both
    regression tests; unconditional `Release` fails the guard.
15. **Transition addresses.** `TestTransitionAddressesBlocked`: every listed form plus the SIIT forms
    and `fec0::1` refused (each a `*BlockedAddressError` whose text omits the address);
    `8.8.8.8`, `64:ff9b::808:808`, `2002:808:808::1`, `2606:4700:4700::1111`, `::ffff:0:808:808`
    allowed; the NAT64 IMDS literal is refused by `ValidateURL`. The existing
    `TestTargetValidator_CheckAddr_RejectsDangerousRanges` is unmodified. Mutations killed:
    removing the embedded-IPv4 step; dropping `fec0::/10`.
16. **No address in caller-facing errors.** `TestValidatorDoesNotReportResolvedAddresses`
    (`https://localhost/hook`, `10.11.12.13`, dial to `localhost:443` → `ErrBlockedDestination`);
    `TestPrivateAnswerNotReported` (injected resolver answering a private address: the generic
    refusal from `ValidateURL` and `ErrBlockedDestination` from the dialer, neither names the address
    nor the class); `TestDNSFailureOmitsResolverAddress` (formatter table with
    `Server: "10.0.0.2:53"`, plus the same failure driven through the dialer);
    `TestDialErrorOmitsResolvedAddress` (allowlisted `localhost`, closed port: `last_error` and log
    rows name `localhost:<port>`, never `127.0.0.1`/`[::1]`; `*DialError` unwraps to
    `ECONNREFUSED`; a dial timeout injected through `net.Dialer.Control` keeps `Timeout() == true`);
    gateway `TestSubscriptionTest_DialErrorOmitsResolvedAddress` and
    `TestSubscriptionTest_LoopbackHostRefusedWithoutAddress`. Mutations killed: the detail back in
    `ValidateURL`; the dialer returning the `CheckAddr` detail; the dialer returning `lastErr`; the
    DNS error wrapped with `%w` (this one survived the first run, see Deviations 8).
17. **Redaction.** `TestSecretURLPartsRedacted` (`http://127.0.0.1:1/services/T000/B000/s3cr3tPathToken?token=s3cr3tQueryToken`:
    neither secret in `last_error`, any log row or the captured `slog` output; `last_error` keeps
    `http://127.0.0.1:1/[redacted]`; parse and `%`-escape errors do not echo the path);
    `TestRedactURL` table (path, query, query with `/`, fragment, userinfo, userinfo+path, port, IPv6
    host, no path, root `/` kept, escaped path, unparseable, no host, empty); `TestRedactError`
    (client.Do shape, redirect URL, wrapped `*url.Error`, backstop forms, short parts);
    `TestRedirectTargetRedacted`; `TestBootstrapLogRedactsTargetURL`; gateway
    `TestSubscriptionTest_RedactsSecretURLParts` (JSON `error` and the Warn line). Mutation killed:
    `err.Error()` on the `client.Do` path.
18. **Documentation.** `docs/integration-guide.md` §6.1: signature versions (table, exact MAC input,
    golden vector, opt-in in object form, receiver checklist incl. "distinct secret per
    subscription"), corrected retry/failure handling (`dead` for non-retryable and misconfigured;
    408/425; redacted texts; exact `HTTP <code>`), circuit holds with both edges, the round-robin
    claim, target validation incl. the NAT64 network-specific-prefix residual, the metadata forms,
    and the unsigned test ping. `grep -n 'spki_sha256' docs/integration-guide.md` → only
    `pinned_spki_sha256`, hex, at `:115` and `:359` (the CA key is fixed to `ca_bundle_pem` too).
    `CHANGELOG.md` has a Security / Changed / For embedders entry.
19. **Gates.** See below. `migrations/` is unchanged.
20. **Stable HTTP text.** `TestHTTPOutcomeTextIsStable`: 401 → `dead`, 503 → `failed` and
    scheduled, 503 at the budget → `dead`; `last_error` and the log row are exactly `HTTP 401` /
    `HTTP 503`. Mutation killed: changed format string.

### Fair-claim timing (§2a)

100 000 claimable rows across 50 subscriptions, PostgreSQL 17.10, each statement in a rolled-back
transaction, on the shared (loaded) development machine:

| Run | Old claim (`61994fe`) | Fair claim as shipped |
|---|---|---|
| quiet machine | 19.5–20.4 ms | 38.7–40.0 ms for the shipped ranking, measured without the outer `next_retry_at` re-check (which evaluates at most 5 rows); 47 ms with a `delivery_id` tiebreak, which was dropped |
| under load (load avg ≈ 7.5) | 21–29 ms | 68–89 ms (exact shipped statement) |

Acceptable for a 1 s poll; a normal queue holds a handful of rows.

### Mutation results

All 24 code mutations plus 3 SQL mutations listed in the per-AC evidence are killed by the named
tests (script in the scratchpad: apply one textual mutation, run the named tests, restore). One
mutation — the DNS error wrapped with `%w` — first survived; see Deviations 8.

### Deviations and additions, with reasons

1. **`ClaimDeliveries` re-checks `next_retry_at` under the lock too**, not only `status`. A row
   another claimer took and deferred into the future between the ranking snapshot and the lock is
   then skipped rather than claimed early. No `delivery_id` tiebreak in the ranking (it cost ~8 ms
   at 100k rows and ordering ties carries no guarantee).
2. **Test placement.** The half-open regressions are in `breaker_regression_test.go` and
   `TestPermitReleaseAfterOtherPermitClosed` in `circuit_breaker_permit_test.go`, so
   `circuit_breaker_test.go` stays unmodified; `TestDialErrorOmitsResolvedAddress` is in
   `ssrf_regression_test.go` and `TestSecretURLPartsRedacted` in `redact_test.go`. The real-Postgres
   `DeferDelivery` test lives in `adapters/delivery/queue_integration_test.go`, not in
   `adapters/store`: the claim is queue-wide, `go test ./...` runs packages in parallel against one
   database, and keeping every queue-touching integration test in one package (which empties the
   queue per scenario, uses unique subscription ids and cleans them up) avoids cross-package
   interference.
3. **Case-variant `signature_version` keys are refused** (`ErrInvalidSubscriptionMetadata`;
   `WithSignatureVersion` removes them). `encoding/json` matches struct tags case-insensitively, so
   a struct reader and a map reader could otherwise disagree about one row. The `tls` block keeps
   its pre-existing (case-insensitive) decoding, so no stored TLS config changes meaning.
4. **`ValidateSubscriptionMetadata` also refuses a `tls.ca_bundle_pem` that holds no certificate**
   — a create-time 400 for a configuration the dispatcher would dead-letter at every send. The
   dispatcher's own parse is unchanged (it still fails at `clientFor`).
5. **Bootstrap heal.** When the existing metadata is a JSON object (even with a bad version value)
   it is rewritten in place, so a `tls` pin is never dropped; only non-object or unparseable
   metadata is replaced with the canonical value.
6. **Gateway.** Supplied metadata is compacted, so object and base64 forms store identical bytes;
   a base64 string that decodes to a non-object is a 400.
7. **`DialError`** also implements `Temporary()` (the `net.Error` interface needs it). The
   literal-IP refusal lost its doubled `target_url: target_url:` prefix.
8. **`TargetValidator` gained an unexported resolver hook** (`lookupIPAddr`, nil = system
   resolver). The cgo resolver on macOS reports no resolver address, so a regression back to
   `%w`-wrapping `*net.DNSError` survived the first mutation run; the hook lets the tests drive a
   resolver failure that names its server.
9. **`RedactError`'s backstop skips URL parts shorter than 4 bytes** (`/v1`, `a=1`, a one-letter
   fragment): replacing every occurrence garbled operator text, and such parts carry no meaningful
   secret; the URL itself and every `*url.Error` are always redacted.
10. **Hold-cap guards.** A row with no `created_at` is never aged out (deferred instead), and an
    unset cap falls back to the default, so a hand-built dispatcher cannot dead-letter every held row.
11. **Operator-supplied values echoed in metadata errors are truncated** to 32 runes.
12. **gofmt** was applied to every changed Go file; several were unformatted on `main` (import
    order), so their diffs include that.

### Gates (final run at `d082e97`)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | OK |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | PASS (38 packages ok) |
| `go test -race -p 4 -count=1 ./adapters/delivery/ ./adapters/store/ ./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| `SPINE_TEST_DATABASE_URL=…/s010_all go test -tags integration -race -count=1 ./adapters/delivery/` | PASS |
| same, `./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| same on a fresh `s010_store`, `./adapters/store/` | 1 FAIL, pre-existing: `TestRunModeDatabaseDefault` (`GetRun: cannot scan NULL into *string` for `workflow_version_label`) — fails identically with `main`'s store sources swapped in by `-overlay`; not touched by this task. On a reused database `TestEventSubscription_SigningSecretEncryption` also fails (fixed id `sub-enc-001` is never cleaned up), on `main` too |
| `SPINE_TEST_DATABASE_URL=…/s010_scenario go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (CI job `scenario-git-http`) | 19 PASS, 0 SKIP |
| `golangci-lint run` on the four touched packages | 126 issues against 136 on `main`; 0 new (diffed ignoring line numbers), 10 fixed (gofmt on touched files, one gocritic) |
| `golangci-lint run --enable-only=gosec` on the same | 0 issues |
| `golangci-lint run --build-tags integration ./adapters/delivery/...` | no issue in the new files |
| `gofmt -l $(git diff --name-only main...HEAD -- '*.go')` | empty |
| `codex review --base main` (round 1 at `1b223ec`, round 2 at `d082e97`) | no findings either round |

No scenario suite exercises webhook delivery, so none beyond `scenario-git-http` was run.

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T010/smp` at `7d4e7cc9` (no `vendor/`, no
`tmp/`), `../spine` = this worktree at `d082e97` (final run; an earlier run at `1b223ec` gave the
same results); its tree stayed clean (`git status --short` empty).

| Command | Result |
|---|---|
| `go build ./...` | OK |
| `go vet ./...`; `go vet -tags integration ./...` | clean |
| `go test -p 4 -count=1` on the 31 SMP packages that depend on `adapters/delivery` or `adapters/store` (`go list -deps`) — incl. `eventdelivery`, `stepevents`, `runner`, `spinecore`, `workspace`, `projectionwriter`, `spineembedded`, `cmd/customer-app` | all PASS (30 ok; `workspace/faultharness` has no tests) |
| AC7 contract test via `-overlay` (above) | PASS on the branch; FAIL (`not a v2 delivery`) with `main`'s delivery sources |
| `TEST_SPINE_DB=…/s010_spine_rt TEST_PLATFORM_DB=…/s010_smp_it go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip 'TEST_PROVISIONING_DSN' -- -race -count=1 -timeout 20m -p 4 ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (databases migrated with `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime`) | PASS: 1536 tests, 4 allowed skips |

**Not run, with the exact command.** SMP's provisioning-gated suites allocate one `ws_<id>_*`
database (and roles) per test through `TEST_PROVISIONING_DSN`, which the shared-database rules of
this run forbid (only `s010_*` databases). They include the suites that drive Spine's dispatcher
against a real runtime database (`eventdelivery` `TestWorker_Integration_*`, `spinecore`
`TestRedrive_RedeliversThroughSpineDispatcher` and the dispatch-store tests). SMP CI runs them as:
`TEST_DATABASE_URL=… TEST_PLATFORM_DB=… TEST_PROVISIONING_DSN=postgres://smp_prov:smp_prov@localhost:5432/postgres?sslmode=disable go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -- -race -count=1 -timeout 20m ./internal/customer/eventdelivery/ ./internal/customer/stepevents/ ./internal/customer/workspace/ ./internal/customer/workspace/workspacetest/ ./internal/customer/projectionwriter/ …` (ci.yml "Platform-DB integration suites") and the `spinecore` run above with `TEST_PROVISIONING_DSN` set. Read-through: none of those tests depends on the hot loop — `TestWorker_Integration_RequiresCipherToDecrypt` asserts the row never reaches `delivered`, which still holds when the 401 now dead-letters it; the redrive test answers 200. SMP's stuck-dispatch predicate (`status = 'dead' OR (failed AND attempt_count >= budget) OR stale delivering`) keeps working: permanent failures now arrive as `dead`.

Behaviour SMP inherits at its first bump past this task is exactly §7 B1–B6 (initiative §7b Rule 4
lists TASK-010): SMP TASK-006 R1 and R2 must be on SMP `main` first.

### OQ2: one-off scrub of rows written before this task

Optional; run per runtime database by an operator. It redacts the path/query/fragment of any
http(s) URL quoted in the error text, and the resolved / source / resolver addresses of the old
dial, post-connect (`read|write tcp <a>-><b>: `, added in review round 1) and DNS errors
(checked on PostgreSQL 17 against sample texts, including an IPv6 `write tcp [fd00::5]:…->…`):

```sql
UPDATE runtime.event_delivery_queue
SET last_error = regexp_replace(regexp_replace(regexp_replace(regexp_replace(last_error,
      '(https?://[^/?#"[:space:]]+)[/?#][^"[:space:]]*', '\1/[redacted]', 'g'),
      '(dial tcp[46]? )[^[:space:]]+?(: )', '\1[address]\2', 'g'),
      '((read|write) tcp[46]? )[^[:space:]]+->[^[:space:]]+?(: )', '\1[address]\3', 'g'),
      '(lookup [^[:space:]]+ on )[^[:space:]]+?(: )', '\1[resolver]\2', 'g')
WHERE last_error ~ 'https?://|dial tcp|(read|write) tcp|lookup .* on ';

UPDATE runtime.event_delivery_log
SET error = regexp_replace(regexp_replace(regexp_replace(regexp_replace(error,
      '(https?://[^/?#"[:space:]]+)[/?#][^"[:space:]]*', '\1/[redacted]', 'g'),
      '(dial tcp[46]? )[^[:space:]]+?(: )', '\1[address]\2', 'g'),
      '((read|write) tcp[46]? )[^[:space:]]+->[^[:space:]]+?(: )', '\1[address]\3', 'g'),
      '(lookup [^[:space:]]+ on )[^[:space:]]+?(: )', '\1[resolver]\2', 'g')
WHERE error ~ 'https?://|dial tcp|(read|write) tcp|lookup .* on ';
```

### Follow-ups recorded (not taken)

Signed test pings; not following redirects on deliveries (a 307 forwards a fresh v2 signature and
nonce); a capped response drain; operator replay resetting `attempt_count`; batch-barrier
head-of-line blocking (per-subscription in-flight caps); a `claimed_at` column (schema); NAT64
network-specific prefixes (egress policy). All are in `KNOWN-LIMITATIONS.md` → "Webhook Delivery".

### Review round 1 (2026-10-07)

Three reviewers (correctness and SMP compatibility, adversarial security, completeness and
tests) sent ten findings and ten acceptance-criteria gap notes, with heavy overlap. Deduplicated,
they are seven issues. Each was checked against the code before anything changed. All seven are
real. Fix commits: `3e1b634` (queue), `3d2a584` (addresses), `7e195c9` (metadata), `ee05da1`
(documentation). The design-text amendments are marked *Review-round-1 note / addition /
amendment* in I5/I7, §2, §7 and ACs 9a, 10 and 16.

**Per-finding disposition**

| # | Finding (severity, reporters) | Verdict | Disposition |
|---|---|---|---|
| 1 | Fair claim starves when `concurrency` or more subscriptions are held (P2, two reviewers; AC gaps I7/AC9a ×3) | Real. Reproduced with the regression fake: fair claim plus per-row deferral at 5×61 held rows delivers 0/900. That run is now a control subtest. | **Fixed** in `3e1b634`. New `DeliveryStore.HoldDueDeliveries` holds the subscription's whole due queue in one attempt-free statement (CTE with `FOR UPDATE SKIP LOCKED`, no schema change). Rows past `MaxCircuitHold` are dead-lettered in the same statement, each with a delivery-log row. Measured lag: 0 polls at 1–4 held subscriptions, 2 at 5–6, 4 at 10. The asserted bound is 1 below `concurrency` and `ceil(2 × held / 5)` otherwise. Guide and CHANGELOG overclaims are corrected. |
| 2 | Post-connect `*net.OpError` names the resolved and source addresses (P2, three reviewers; AC gaps I5/AC16 ×4) | Real. Reproduced here: `read tcp 127.0.0.1:52912->127.0.0.1:52908: read: connection reset by peer`. | **Fixed** in `3d2a584`. `SafeDialContext` returns `safeConn`, which re-renders Read/Write/Close `*net.OpError`s with no `Source` and with `Addr` = the caller's `host:port`. Type, Op, inner error and `Timeout()` are kept. `RedactError` adds a backstop for any other address-bearing `*net.OpError`. Tests cover the validator client directly, the dispatcher (default and TLS client) and the gateway test endpoint. |
| 3 | Transient resolver failure dead at the budget (~15 s) is an unsanctioned SMP behaviour change (P2) | Real. Rule 4's TASK-010 row did not list it, and cross-repo rule 2 forbids it. | **Fixed** in `3e1b634`, using a variant of the reviewer's first option. The row is not terminal before `MaxCircuitHold`: it is retried on backoff, then every minute, and delivered automatically once readable. Attempts are still counted (deviation R1-2), so SMP's banner predicate (`failed AND attempt_count >= budget`) shows it as it does today. The rest of the subscription's due queue is held to the same retry instant. AC10 is amended, R2 needs no extension, and §7 B8 records it. |
| 4 | CHANGELOG "For embedders" list incomplete (P3) | Real. Items (a)–(d) were all missing. | **Fixed.** (a)–(c) are listed in `ee05da1`, along with the whole-queue hold and its multi-replica effect, `HoldDueDeliveries`/`DeliveryHold` and the post-connect error shape. The initiative's Rule 4 row now lists them too. (d) is fixed in code (`3d2a584`) rather than documented as a break: `resolveError` omits the resolver address from its text but unwraps to the `*net.DNSError` and delegates `Timeout`/`Temporary`, so `errors.As` and `url.Error.Timeout()` work again. |
| 5 | `signature_version` derived from the version key only (P3, two reviewers) | Real. A base64 pin from the old docs was reported `"v1"` while every delivery went dead. | **Fixed** in `7e195c9`. The value is `"invalid"` whenever `ValidateSubscriptionMetadata` fails. Gateway cases cover a base64 pin, a non-hex v2 pin, an empty CA bundle and an ambiguous `tls` key. |
| 6 | `tls` block decoded case-insensitively through a struct, so its meaning depended on jsonb key order (P3) | Real. jsonb sorts keys shorter first, then bytewise, so `"TLS"` comes before `"tls"` and the gateway and the dispatcher read different values. | **Fixed** in `7e195c9`. The block is decoded from the single key that folds to `tls`. Two or more such keys, at the top level or for a field inside the block, are refused in either order. A single variant in any case is still accepted (deviation R1-3). |
| 7 | AC11: `make test-integration` red (completeness gap) | Not a defect of this task. | `TestRunModeDatabaseDefault` fails identically on `main` `ee3c249`: exported with `git archive main`, run on a fresh database, same `GetRun: … cannot scan NULL into *string` at `postgres_integration_test.go:702`. The rest of `adapters/store` integration passes with `-race` on a fresh database (`-skip '^TestRunModeDatabaseDefault$'`), and `adapters/delivery` integration passes in full. AC19 scopes `make test-integration` to those two packages. |
| — | SMP provisioning-gated suites never run (completeness gap) | Gap closed. | Run against a throwaway `pgvector/pgvector:pg18` container (local image, data on tmpfs, removed afterwards), provisioned exactly like SMP CI's integration job: roles `platform`, `platform_audit_retention` and `smp_prov` (NOSUPERUSER CREATEROLE CREATEDB), `smp_it` with `vector`, platform and spine-runtime migrations applied. Results are below. |
| — | "Verified, no gap" note (AC7, P3-12, I3, AC15) | Acknowledged. | No action. |

**Additional compatibility point found during the gate.** The design-phase AC7 contract test
(SMP TASK-006 R5 will commit it) has a `contractQueue` fake that implements
`store.DeliveryStore` method by method. It must add `HoldDueDeliveries`; a no-op is enough. A
scratch copy with that one method passes against this branch (below). SMP's own tree implements
no `DeliveryStore` and builds unchanged. This is recorded in §7.

**Deviations in this round, with reasons**

- **R1-1. `HoldDueDeliveries` is a required `DeliveryStore` method**, as `DeferDelivery` is
  (Decision 8). An optional interface would let a fake, or an embedder's store, silently fall
  back to per-row deferral and hide the I7 regression. The cost is that the contract-test fake
  above needs one method.
- **R1-2. The unreadable-subscription retry counts attempts.** The reviewer suggested consuming
  none. Counting keeps today's SMP visibility: before this task the hot loop drove
  `attempt_count` past the budget, and the run banner showed it. With no attempts consumed, the
  row would be invisible for 24 h. As today, an endpoint failure after the fault clears then
  dead-letters it. The guide says so.
- **R1-3. A single case variant of `tls` (or of a field inside it) is still accepted.** The
  reviewer suggested refusing every non-exact spelling, as for `signature_version`. Rows stored
  as `{"TLS":…}` keep their pre-task meaning. Only ambiguity changes meaning by key order, and
  ambiguity is refused.
- **R1-4. The whole-queue hold also covers the resolver path.** Without it, five unreadable
  subscriptions with backlogs would starve the queue the same way as finding 1.
- **R1-5. A held `failed` row keeps its `last_error`** (its last real outcome, for example
  `HTTP 503`). A held `pending` row gets the hold reason. `attempt_count` is never touched.
- **R1-6. Multi-replica effect (B7).** The breaker is per process. A replica-local fault holds
  the subscription's due queue for every replica, for up to 60 s per cycle. This is recorded in
  `KNOWN-LIMITATIONS.md` and in the CHANGELOG embedder list. SMP has one subscription per runtime
  database, so the starvation fix itself is neutral for SMP.
- **R1-7. The initiative's Rule 4 TASK-010 row is edited** (one table row in `initiative.md`).
  Rule 4 is the authoritative list of what SMP inherits. Owner and ordering are unchanged.

**Hold-statement cost** (PostgreSQL 17.10, rolled-back transactions, load average ≈ 12): a
first hold of 2 000 due rows of one subscription, beside 100 000 delivered rows of it and
98 000 other claimable rows, takes 67 ms. A repeat hold in the same cycle (nothing newly due)
takes 17 ms. A 100 000-row held backlog takes 1.6 s, once per probe cycle; per-row deferral
would instead do 100 000 single-row writes per cycle. Recorded in `KNOWN-LIMITATIONS.md`.

**Mutation results (round 1).** All are killed by the named tests. Each was a textual mutation
that was then restored. SQL mutations ran against real Postgres.

| Mutation | Killed by |
|---|---|
| `holdBacklog` made a no-op | `TestHeldBacklogDoesNotStarveOtherSubscriptions`, `TestUnreadableSubscriptionHoldsBacklog`, `TestCircuitHoldExpiresUnclaimedBacklog` |
| circuit path skips the backlog hold | `TestHeldBacklogDoesNotStarveOtherSubscriptions`, `TestCircuitHoldExpiresUnclaimedBacklog` |
| resolver path skips the backlog hold | `TestUnreadableSubscriptionHoldsBacklog` |
| resolver row dead at the budget (the pre-round-1 rule) | `TestTransientResolverErrorBacksOffWithFixedText` |
| bulk-expired rows get no delivery-log row | `TestCircuitHoldExpiresUnclaimedBacklog` |
| no `ExpireBefore` (no bulk expiry) | `TestCircuitHoldExpiresUnclaimedBacklog` |
| Postgres `HoldDueDeliveries` a no-op | `TestQueuePG_HoldDueDeliveries`, `TestQueuePG_HeldBacklogsOfManySubscriptionsDoNotStarve` |
| SQL: `FOR UPDATE` without `SKIP LOCKED` (blocks on a locked row) | `TestQueuePG_HoldDueDeliveries` |
| SQL: `failed` rows' `last_error` overwritten | `TestQueuePG_HoldDueDeliveries` |
| SQL: `attempt_count` bumped | `TestQueuePG_HoldDueDeliveries`, `TestQueuePG_HeldBacklogsOfManySubscriptionsDoNotStarve` |
| SQL: rows scheduled at or after `Until` also held | `TestQueuePG_HoldDueDeliveries` |
| SQL: subscription filter dropped | `TestQueuePG_HoldDueDeliveries`, `TestQueuePG_HeldBacklogsOfManySubscriptionsDoNotStarve` |
| SQL: nothing expired | `TestQueuePG_HoldDueDeliveries` |
| `SafeDialContext` returns the raw connection | `TestPostConnectErrorOmitsAddresses` (the validator client's own error; the backstop protects the dispatcher path) |
| `RedactError` `*net.OpError` backstop removed | `TestRedactErrorRewritesOpErrorAddresses` |
| `resolveError` renders the inner `*net.DNSError` | `TestDNSFailureOmitsResolverAddress`, `TestResolveErrorKeepsDNSErrorInChain` |
| `tls` decoded through the old whole-document struct | `TestTLSBlockDecodingIsOrderIndependent` |
| `signature_version` from the version key only | `TestSubscriptionUpdate_MetadataValidationAndSignatureVersion` |

**Gates (round 1; code at `ee05da1`, and the closing commit changes only this file)**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | OK |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | PASS (38 packages ok) |
| `go test -race -p 4 -count=1 ./adapters/delivery/ ./adapters/store/ ./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| `SPINE_TEST_DATABASE_URL=…/s010_delivery go test -tags integration -race -count=1 ./adapters/delivery/` | PASS (all `TestQueuePG_*`, including the two new tests) |
| same on `s010_gw`: `./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| same on a fresh `s010_store`: `./adapters/store/` | only `TestRunModeDatabaseDefault` fails, which is pre-existing on `main` (row 7 above); with `-race -skip '^TestRunModeDatabaseDefault$'`: PASS |
| `SPINE_TEST_DATABASE_URL=…/s010_scenario go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` | 19 PASS, 0 SKIP |
| `golangci-lint run --allow-parallel-runners` on the four touched packages | 125 issues against 136 on `main` (exported copy); 0 new (diffed ignoring line numbers); 11 fixed |
| same with `--build-tags integration` on `adapters/delivery`, `adapters/store` | 29 against 37; 0 new |
| `golangci-lint run --enable-only=gosec` on the four packages | 0 issues |
| `gofmt -l $(git diff --name-only main -- '*.go')` | empty |
| `codex review --base main` (after the four fix commits) | "No actionable regressions were identified" |

All `s010_*` databases (`s010_delivery`, `s010_store`, `s010_gw`, `s010_scenario`,
`s010_storemain`, `s010_perf`) were dropped afterwards.

**SMP compatibility gate (round 1).** The SMP checkout is `/Users/bartoszszymichowski/42sid/i23/s-T010/smp`
at `7d4e7cc9`, with `../spine` = this worktree. Its tree was clean afterwards.

| Command | Result |
|---|---|
| `go build ./...` | OK |
| `go vet ./...`; `-tags integration`; `-tags scenario`; `-tags faultinject` | all clean |
| `go test -p 4 -count=1` on the 29 packages whose non-test deps include `adapters/delivery` or `adapters/store`, plus the 7 more whose tests import them (`brain`, `clarity`, `notification`, `provenance`, `spineruntime`, `opshealth`, `recovery`) | all PASS (`workspace/faultharness` has no tests) |
| AC7 contract test via `-overlay` (scratch copy plus `HoldDueDeliveries` on its fake) | `TestSpineV2Contract_DispatcherToEventReceiver` PASS, `TestSpineV2Contract_V1DefaultStillAccepted` PASS |
| Container as above; `TEST_SPINE_DB=…/spine_runtime_it TEST_PLATFORM_DB=…/smp_it TEST_PROVISIONING_DSN=smp_prov@…/postgres go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race -count=1 -timeout 20m -p 4 ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (CI's "Spine runtime integration suites", now with the provisioning DSN, so spinecore's dispatch-store tests and `TestRedrive_RedeliversThroughSpineDispatcher` run) | PASS: 1539 tests, 1 allowed skip |
| `TEST_DATABASE_URL=TEST_PLATFORM_DB=…/smp_it TEST_PROVISIONING_DSN=… go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -allow-skip 'TEST_MINIO_' -- -race -count=1 -timeout 20m -p 4` on `eventdelivery` (all `TestWorker_Integration_*`, including `RequiresCipherToDecrypt` on the changed resolver path), `stepevents`, `workspace`, `workspace/workspacetest`, `projectionwriter`, `spineruntime`, `brain`, `notification`, `provenance`, `opshealth`, `recovery` | PASS: 998 tests, 7 allowed skips (MinIO and live AWS only; the live dev stack's MinIO was deliberately not used) |

### Review round 2 (2026-10-07)

One reviewer re-verified the round-1 dispositions and gates and sent two P3 findings and six gap
notes. Both findings are real and were reproduced with the reviewer's own proofs of concept
(scratchpad `rev2-t010/poc/zz_rev2_test.go`, run through `-overlay` without writing into the tree).
Fix commits: `e36c3f9` (finding 1), `19b3dfc` (finding 2), `c3d1457` (documentation). The
design-text amendments are marked *Review-round-2 note / addition / amendment* in I7, §2, Owner
question 1, §7 and ACs 9, 9a and 10. The initiative's Rule 4 TASK-010 row is updated with them.

**Per-finding disposition**

| # | Finding (severity) | Verdict | Disposition |
|---|---|---|---|
| 1 | Rows dead-lettered per row still starve the queue at `k ≥ concurrency` (P3) | Real. Reproduced: with 4 × 300 deleted-subscription rows the first healthy delivery is at poll 0; with 5 × 300 it is at poll 300, and with 6 × 300 at poll 360. | **Fixed** in `e36c3f9`. A permanent verdict on the subscription dead-letters the claimed row and, in one `HoldDueDeliveries` statement (`ExpireBefore = Until = now + poll interval`), every other row of the subscription due by the next poll. Each row gets the same reason and a log row, and `attempt_count` is untouched. This covers all seven such verdicts: deleted, invalid metadata, refused URL, request build error, TLS config error, v2 without a secret, and an unknown version at signing. With the reviewer's proof of concept after the fix, the first healthy delivery is at poll 1 for both 5 × 300 and 6 × 300. The CHANGELOG starvation claim now names both mechanisms. The alternative (`DeleteSubscription` dead-letters its rows) was not taken: it covers deletion only, and it leaves rows orphaned by earlier deletes. |
| 2 | `MaxCircuitHold` measured from `created_at` dead-letters aged rows at the first fault (P3) | Real. Reproduced: 20 rows queued 25 h ago, one failed read, then the read succeeds → 20 dead, 0 delivered, 1 resolver call. | **Fixed** in `19b3dfc`, by a different mechanism from the one suggested (deviation R2-1). The hold is measured from the later of `created_at` and the start of the subscription's current hold, as this process observed it. That start is `CircuitBreaker.OpenedAt` (new: set when the circuit opens from closed, kept across failed and released probes, zero while closed) or the dispatcher's `unreadableSince` (the first failed read of the current run, cleared by a successful read or a permanent verdict). The bulk hold expires rows only once the hold itself is older than the cap, with cut-off `now − cap`. A row is therefore expired exactly when `max(created_at, start) < now − cap`. Neither the SQL nor the schema changes. With the proof of concept after the fix: 0 dead, 20 delivered. |
| — | AC7 / §7 R5: the named prototype does not compile against HEAD (gap) | Confirmed: `go vet -overlay` in the SMP tree reports `*contractQueue does not implement store.DeliveryStore (missing method HoldDueDeliveries)`. | A handoff gap, not a code defect. The prototype (under SMP's `tmp/`) and the SMP TASK-006 design are outside this worktree, so this task cannot edit them. The handoff is now stated in three places: §7 (round-2 note, with the exact method); the CHANGELOG "For embedders" entry, which names the fake; and **Handoffs** below. Round 2 adds no `DeliveryStore` method. AC7 was re-run green at the round-2 head (SMP gate below). |
| — | I7 / AC9a: no test for `k ≥ concurrency` dead-lettered subscriptions (gap) | Real (finding 1). | **Closed** by `TestDeadLetteredBacklogsDoNotStarveOtherSubscriptions` (4/5/6/10 × 300 for deleted, invalid metadata and refused URL, plus a control), `TestPermanentVerdictDeadLettersWholeDueQueue` and `TestQueuePG_DeadLetteredBacklogsOfManySubscriptionsDoNotStarve`. |
| — | AC10 amendment: no test separates "held 24 h" from "queued 24 h ago" (gap) | Real (finding 2). | **Closed** by `TestAgedBacklogSurvivesTransientFaults`, the reworked `TestUnreadableSubscriptionHoldsBacklog`, `TestCircuitHoldCapDeadLetters` (each cap × three outage ages), `TestCircuitHoldExpiresUnclaimedBacklog` ("outage just began"), `TestCircuitHoldCapCountsFromOutageStart`, `TestCircuitOpenedAt`, `TestHoldStartedAt` and `TestQueuePG_FreshOutageHoldsAgedBacklog`. |
| — | Round-1 fixes, NOT REAL AC11, cheap gates, other ACs re-verified (notes) | Acknowledged. | No action. |

**Measured after the fix** (fake clock, concurrency 5, one healthy row per simulated second for
600 polls, maximum wait of a healthy row): 4 × 300: 0 polls; 5 × 300: 1; 6 × 300: 1;
10 × 300: 2. The same figures hold for the deleted, invalid-metadata and refused-URL kinds. The
control with per-row dead-lettering at 5 × 300 waits 300 polls.

**Deviations in this round, with reasons**

- **R2-1. Finding 2 uses an in-memory hold start, not the suggested `last_error` proxy.** The
  proxy dead-letters only when the row's `last_error` already equals the hold reason. An aged row
  would then still go `dead` at its second consecutive fault: a two-poll read blip, or one probe
  cycle into a fresh outage. It would also need an SQL predicate on the bulk expiry. The start of
  the hold is what "held for 24 h" actually means, and the breaker and dispatcher already keep
  per-subscription state in memory. The cost is that a restart starts the count again: the cap
  can come late, never early. `KNOWN-LIMITATIONS.md` records this, including its one unbounded
  case. A subscription that stays unreadable while its dispatcher restarts more often than daily
  keeps `failed` rows at the attempt budget, retried every minute and shown in SMP's banner.
- **R2-2. The whole-queue dead-letter reuses `HoldDueDeliveries`** (`ExpireBefore = Until =` the
  next poll). It adds no store method (so the R5 fake needs nothing new) and no SQL. Rows
  scheduled beyond the next poll are left alone: they are not claimable, so they cannot take
  slots, and each meets the same verdict when due. A row due now but stamped more than one poll
  ahead by a skewed clock is held to the next poll, not expired.
- **R2-3. The TLS-config and signing-config verdicts are included.** The reviewer named deleted,
  invalid metadata, refused URL and TLS. The rule applied is: every verdict that is a function of
  the subscription rather than of the row. The entropy-read failure stays per row because it is
  transient. The TLS path still records its breaker failure.
- **R2-4. A DNS-based URL refusal dead-letters the due queue too.** A host is refused on what it
  resolves to at that moment, so a briefly wrong DNS answer costs the subscription its due
  deliveries; they stay replayable. Keeping DNS refusals per row would leave the starvation
  open for that verdict. The integration guide and `KNOWN-LIMITATIONS.md` say so.
- **R2-5. `CircuitBreaker.OpenedAt` is new exported API** (additive). It is listed in the
  CHANGELOG.
- **R2-6. SMP.** SMP has one subscription per runtime DB, so finding 1's starvation never applies
  to it. Its verdict paths are not reached in normal operation. Where one is reached, the end
  state equals per-row dead-lettering except for a fix that lands mid-drain. Finding 2 moves the
  terminal time of held rows later, toward today's `main`, where holding alone never makes a row
  terminal. A replica restarted with the wrong encryption key now retries an aged backlog for
  24 h instead of dead-lettering it at the first failed read. §7 (B10, B8/B9 amended) and the
  Rule 4 row record both.

**Mutation results (round 2).** Each mutation is textual and was restored afterwards. Every one
is killed by each named test run alone. The real-Postgres column ran on `s010_delivery`.

| Mutation | Killed by | Real Postgres |
|---|---|---|
| `deadLetterRest` a no-op | `TestPermanentVerdictDeadLettersWholeDueQueue`, `TestDeadLetteredBacklogsDoNotStarveOtherSubscriptions` | `TestQueuePG_DeadLetteredBacklogsOfManySubscriptionsDoNotStarve` |
| `not_found` dead-letters only the claimed row | `…WholeDueQueue/deleted`, `…DoNotStarve…/deleted` | same |
| invalid metadata, refused URL, request build error, TLS config error, signing: each claimed-row only | the matching `…WholeDueQueue` subtest (and `…DoNotStarve…` for the first two) | — |
| `deadLetterRest` holds instead of expiring (no `ExpireBefore`) | `TestPermanentVerdictDeadLettersWholeDueQueue` | `TestQueuePG_OrphanedRowsDeadLetter`, `…DeadLetteredBacklogs…` |
| `heldTooLong` from `created_at` only (the round-1 rule) | `TestAgedBacklogSurvivesTransientFaults`, `TestCircuitHoldCapDeadLetters`, `TestHoldStartedAt` | `TestQueuePG_FreshOutageHoldsAgedBacklog` |
| bulk expiry from `created_at` only (the round-1 rule) | `TestAgedBacklogSurvivesTransientFaults`, `TestCircuitHoldExpiresUnclaimedBacklog`, `TestUnreadableSubscriptionHoldsBacklog` | `TestQueuePG_FreshOutageHoldsAgedBacklog` |
| unknown (zero) hold start counted from `created_at` | `TestHoldStartedAt` | — |
| a successful read does not clear `unreadableSince` | `TestAgedBacklogSurvivesTransientFaults` | — |
| `unreadableSince` not kept across failures | `TestUnreadableSubscriptionHoldsBacklog` | — |
| a failed probe moves `openedAt` | `TestCircuitOpenedAt`, `TestCircuitHoldCapCountsFromOutageStart` | — |
| `OpenedAt` reported while closed | `TestCircuitOpenedAt` | — |
| `openedAt` never set | `TestCircuitOpenedAt`, `TestCircuitHoldCapCountsFromOutageStart` | — |

**Handoffs (for the orchestrator; outside this worktree)**

- SMP TASK-006 R5: the committed `internal/customer/runner/spine_webhook_v2_contract_test.go` must
  give its `contractQueue` fake
  `func (q *contractQueue) HoldDueDeliveries(context.Context, spinestore.DeliveryHold) ([]spinestore.DeliveryEntry, error) { return nil, nil }`.
  The prototype under `_work/task010/smp/` and the SMP TASK-006 design should record this. With
  that method the test passes against this branch; without it, it fails to compile.

**Gates (round 2; code at `c3d1457`, and the closing commit changes only this file and the
initiative's Rule 4 row)**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | OK |
| `make build` | OK |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -p 4 -count=1 ./...` | PASS (38 packages ok) |
| `go test -race -p 4 -count=1 ./adapters/delivery/ ./adapters/store/ ./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| `SPINE_TEST_DATABASE_URL=…/s010_delivery go test -tags integration -race -count=1 ./adapters/delivery/` | PASS (all 9 `TestQueuePG_*`, including the two new ones) |
| same on `s010_gw`: `./service/internal/gateway/ ./service/cmd/spine/` | PASS |
| same on a fresh `s010_store`: `./adapters/store/` | only the pre-existing `TestRunModeDatabaseDefault` fails (`GetRun … cannot scan NULL into *string`, as on `main`, round-1 row 7); on a fresh database again with `-skip '^TestRunModeDatabaseDefault$'`: PASS |
| `SPINE_TEST_DATABASE_URL=…/s010_scenario go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` | 19 PASS, 0 SKIP |
| `golangci-lint run --allow-parallel-runners` on the four packages | 125 issues against 136 on `main` (fresh `git archive main` export); 0 new (diffed ignoring line numbers) |
| same with `--build-tags integration` on `adapters/delivery`, `adapters/store` | 29 against 37; 0 new |
| `golangci-lint run --enable-only=gosec` on the four packages (and with `--build-tags integration` on `adapters/delivery`) | 0 issues |
| `gofmt -l $(git diff --name-only main...HEAD -- '*.go')` | empty |
| `git diff --name-only main...HEAD -- migrations` | empty |
| `codex review --base main` (at `c3d1457`) | "No actionable regressions were identified." Its own runs of the gateway and `cmd/spine` suites could not listen on sockets in its sandbox; both pass in the rows above. |

All `s010_*` databases (`s010_delivery`, `s010_store`, `s010_gw`, `s010_scenario`, and
`s010_mut` for the real-Postgres mutation checks) were dropped afterwards.

**SMP compatibility gate (round 2).** The SMP checkout is `/Users/bartoszszymichowski/42sid/i23/s-T010/smp`
at `7d4e7cc9`, with `../spine` = this worktree at `c3d1457`. Its tree was clean afterwards.

| Command | Result |
|---|---|
| `go build ./...` | OK |
| `go vet ./...`; `go vet -tags integration ./...` | clean |
| `go test -p 4 -count=1` on the 31 packages whose deps include `adapters/delivery` or `adapters/store`, plus the 7 whose test deps do (`brain`, `clarity`, `notification`, `provenance`, `spineruntime`, `opshealth`, `recovery`) | all PASS (`workspace/faultharness` has no tests) |
| AC7 contract test via `-overlay` (scratch copy plus `HoldDueDeliveries` on its fake), `-run TestSpineV2Contract ./internal/customer/runner/` | `TestSpineV2Contract_DispatcherToEventReceiver` PASS, `TestSpineV2Contract_V1DefaultStillAccepted` PASS |
| The unmodified prototype via `-overlay` (`go vet`) | does not compile: `missing method HoldDueDeliveries` (the reviewer's gap; see Handoffs) |
| Throwaway `pgvector/pgvector:pg18` container (local image, data on tmpfs, removed afterwards), provisioned as SMP CI's integration job (roles `platform`, `platform_audit_retention`, `smp_prov` with the posture check `f`; `smp_it`, `smp_schema_it`, `spine_runtime_it`; `vector`; 115 platform and 26 spine-runtime migrations). `TEST_SPINE_DB=…/spine_runtime_it TEST_PLATFORM_DB=…/smp_it TEST_PROVISIONING_DSN=smp_prov@…/postgres go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race -count=1 -timeout 20m -p 4 ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` | PASS: 1539 tests, 1 allowed skip (spinecore's dispatch-store tests and `TestRedrive_RedeliversThroughSpineDispatcher` ran) |
| `TEST_DATABASE_URL=TEST_PLATFORM_DB=…/smp_it TEST_PROVISIONING_DSN=… go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -allow-skip 'TEST_MINIO_' -- -race -count=1 -timeout 20m -p 4` on `eventdelivery` (all `TestWorker_Integration_*`), `stepevents`, `workspace`, `workspace/workspacetest`, `projectionwriter`, `spineruntime`, `brain`, `notification`, `provenance`, `opshealth`, `recovery` | PASS: 998 tests, 7 allowed skips (MinIO and live AWS only) |
