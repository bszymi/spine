---
id: TASK-009
type: Task
title: "enc:v2 secret envelope (key id + AAD + key ring) and subscription-secret re-encryption sweep"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-007-hash-webhook-signing-secret-on-store.md
---

# TASK-009 — enc:v2 secret envelope (key id + AAD + key ring) and subscription-secret re-encryption sweep

---

## Purpose

This task fixes the Spine half of two P3 items from the 2026-10-06 security audit:

- **P3-10:** the `enc:v1` envelope has no associated data (AAD) and no key id.
- **P3-1:** adding the encryption key later never re-encrypts existing rows.

The SMP half of both items is SMP INIT-027 EPIC-004 TASK-006 (see §8).

Line numbers refer to Spine `61994fe` and SMP `80995559` unless stated. "SMP" means
`spine-management-platform`.

There is one at-rest secret today: `runtime.event_subscriptions.signing_secret`. It is the HMAC
key the webhook dispatcher signs every delivery with (`adapters/delivery/webhook_dispatcher.go:218-222`).
SMP's `/internal/step-events/{workspace_id}` receiver verifies that HMAC against the named
workspace's key (`internal/customer/runner/event_receiver.go:93-104`, `:313`, `:328`).

### P3-10 — the envelope binds nothing and names no key

**Where.** `core/crypto/secret.go`.

- `Encrypt` seals with `nil` associated data (`:84`). The envelope is
  `"enc:v1:" + base64(nonce || ciphertext)` (`:24`, `:86`). It carries no key id.
- `SecretCipher` holds exactly one AEAD (`:28-30`). `Decrypt` opens with that key only (`:93-114`).
  So a key cannot rotate: every row must be decrypted with the old key and re-saved under the new
  one by hand. The current docs say exactly that (`docs/integration-guide.md:109`).
- `Decrypt` returns any value without the `enc:v1:` prefix verbatim, even when a cipher is
  configured (`:94-96`).

**Deployment fact that makes it cross-tenant.** SMP parses `SPINE_SECRET_ENCRYPTION_KEY` once and
shares that one cipher across every workspace runtime database:

- it is built at `cmd/customer-app/boot.go:407-425`;
- the provisioning seed uses it at `routes_platform.go:344` → `provision_steps_runtime.go:418-419`;
- the delivery worker uses it at `workers.go:255` → `eventdelivery/worker.go:351-352`.

Standalone shared mode does the same: one cipher from `serve_config.go:235-248` is installed on
every per-workspace store (`service/internal/workspace/pool.go:821-823`). Each workspace has its
own database (ADR-012 context: "DB-per-workspace").

**Attacker.** Someone who has both of these:

- write access to workspace B's runtime database (stolen per-workspace DB credentials, a DB
  operator, or a tampered restore); and
- a copy of workspace A's `signing_secret` column (a backup, snapshot or replica read of A).

Neither is a Spine API capability. That is why this is P3.

**Impact** (code-traced, not executed end-to-end):

1. The attacker pastes A's ciphertext into B's `smp-internal` row. Each workspace has its own
   database, so they can even reuse A's `subscription_id`.
2. B's dispatcher decrypts the value with the shared key. The audit PoC
   `poc/crypto-secrets-auth/cipher_transplant_test.go` shows this succeeds.
3. B's dispatcher now signs B's queued deliveries with A's secret. With B's database writable,
   the attacker controls the payload (`runtime.event_delivery_queue.payload`). They also control
   `target_url`, and can point it at `/internal/step-events/{A}`. That passes SMP's default
   webhook allowlist (`SPINE_WEBHOOK_ALLOWED_HOSTS=customer-app`, SMP `docker-compose.yaml:441`).
4. The result is a step-event that verifies as workspace A's. The audit notes this can poison the
   runner queue or spoof notifications (REPORT §3 P3-1, §5 "not fully traced").
5. The v1 signature covers only the body (P3-11, TASK-010), so nothing else in the request stops
   this.

**Secondary impact.** Keys never rotate in practice. A key that has leaked (for example through an
old `.env`) stays valid for every row forever.

**Verified.** The PoCs below still reproduce on `61994fe`. They were re-run on 2026-10-06 with
`go test -overlay`:

- `poc/crypto-secrets-auth/cipher_transplant_test.go`
- `poc/gap1-crypto-secrets-reaud/crypto_poc_test.go` (`TestPoC_CiphertextSwapAcrossRowsDecrypts`
  and `TestPoC_PlaintextAcceptedWithCipherConfigured`)

### P3-1 (Spine half) — adding the key later never re-encrypts existing rows

**Where.** `adapters/store/postgres_subscriptions.go:34-49`.

- `encryptSubscriptionSecret` encrypts only on a write (`:35-38`). `decryptSubscriptionSecret`
  passes plaintext through (`:44-49`).
- The comment at `:41-43` and the docs claim plaintext rows "will be re-encrypted on their next
  UpdateSubscription call" (`docs/integration-guide.md:108`; `.env.example:84-85`). That update
  never comes:
  - `BootstrapInternalSubscription` compares the *decrypted* secret and returns early when nothing
    changed (`adapters/delivery/bootstrap.go:61-70`);
  - SMP's realign does the same (`internal/customer/stepevents/secrets.go:175-177`).
- The audit PoC `poc/verify-r2-1/r21_plaintext_secret_poc_test.go` shows it: "after 3 keyed
  bootstraps: UPDATEs=0, raw column encrypted=false, raw==key=true". So a deployment that starts
  without a key and adds one later (SMP's dev default is no key: `docker-compose.yaml:343`) keeps
  its signing secrets in plaintext indefinitely.
- INIT-008 EPIC-004 TASK-007 had an acceptance criterion "Existing rows migrated via one-shot
  migration or admin command". It was never delivered.

**Attacker and impact.** Anyone who reads a database dump or backup can forge the dispatcher's
signature (r21 PoC step 1, `forged==legit: true`). That makes the key encrypt only new rows, which
is exactly the gap P3-1 names.

**Not in this task.** SMP stores plaintext *by default* because its key is optional
(`boot.go:412-415`). That is SMP's half of P3-1 and belongs to SMP TASK-006. Spine standalone
already refuses to start in production without a key (`serve_config.go:237-240`).

---

## Design

### 0. Invariants

- **I1 — Additive.** Every exported identifier SMP uses keeps its signature and behaviour:
  - `crypto.ParseEncryptionKey`, `crypto.NewSecretCipher`, `*crypto.SecretCipher`,
    `crypto.EncryptionKeySize`;
  - `(*store.PostgresStore).SetSecretCipher`, `store.NewPostgresStoreWithQuerier`;
  - the `store.SubscriptionStore` interface;
  - `delivery.BootstrapInternalSubscription`.

  A store with no binding (§2) behaves exactly as at `61994fe`: it writes `enc:v1` and reads
  plaintext and `enc:v1`.
- **I2 — Trusted binding.** The workspace bound into a v2 envelope comes from process
  configuration: the workspace whose database the store was opened for. It never comes from a
  database row.
- **I3 — Fail closed.** A v2 value that does not open under this store's binding is an error. It is
  never treated as plaintext, never blanked, and never re-sealed.
- **I4 — No laundering.** Nothing rewrites a row it could not open. In `strict` mode, nothing
  converts a legacy row.
- **I5 — No lost writes.** The sweep never overwrites a value that changed after it read it.
- **I6 — No secret material** (plaintext or stored value) appears in errors, logs or sweep reports.
- **I7 — No runtime schema change.** v2 values fit the existing `signing_secret text` column
  (`migrations/016_event_subscriptions.sql:11`).

> **Design-check note (2026-10-06) — I4 and a new I8.**
> - §2's original write rule did not meet I4. In `migrate` mode, an `enc:v1` value copied from
>   *another* workspace opens, because v1 has no AAD, and was then re-sealed as this row's `enc:v2`
>   (probe X4, see "Design check"). §2 now refuses every envelope input under a binding.
> - **I8 — Never nest.** Under a binding, no read returns a value that `ClassifyEnvelope` reports
>   as anything except `EnvelopePlaintext`, and the sweep never writes one. An old binary's
>   write-back can nest an envelope inside another (probes X1 and X5). §2 and §3 now repair or
>   refuse such rows.

### 1. `core/crypto`: the `enc:v2` envelope and the key ring

**Wire format.**

```
enc:v2:<kid>:<base64std( nonce[12] || AES-256-GCM ciphertext || tag[16] )>
```

- `kid` is 16 lowercase hex characters: `hex(HKDF-SHA256(ikm=key, salt=nil, info="spine/secret-envelope/v2/key-id", L=8))`.
  It is derived, stable and non-secret, so operators configure no labels.
- **AEAD key.** `HKDF-SHA256(ikm=key, salt=nil, info="spine/secret-envelope/v2/aes-256-gcm", L=32)`.
  This keeps v2 key use separate from v1, which uses the raw key with `nil` AAD.
- **GCM associated data.** `"spine/secret-envelope/v2" || 0x00 || kid || 0x00 || callerAAD`.
  The version and key id are authenticated: relabelling an envelope with another ring key's id
  fails authentication.
- **Caller AAD.** `AssociatedData(purpose, fields...)` builds
  `"spine-aad/1" || uvarint(len(purpose)) || purpose || (uvarint(len(f)) || f)…`.
  Field boundaries cannot be confused: `("ws-A","1")` is not `("ws-A1","")`.
- **Nonces.** Random 96-bit nonces from `crypto/rand`, as v1. Volume is tiny, one row per
  subscription.
- **Reserved prefixes.** `enc:v<digits>:` is reserved. Unknown versions are refused by `Open`.
  Legacy `Decrypt` keeps returning them verbatim, as at `61994fe`.

`crypto/hkdf` is in the standard library (Spine is `go 1.26`, `go.mod:3`), so `core/crypto` stays
stdlib-only.

**API.** Changes go in `core/crypto/secret.go`, optionally split into a new `envelope.go`.

| Identifier | Status | Behaviour |
|---|---|---|
| `EncryptionKeySize`, `ParseEncryptionKey` | unchanged | — |
| `type SecretCipher` | same name, now a key ring | `keys[0]` is the primary (write) key; the rest are decrypt-only. Key bytes are not kept after construction. A zero value returns an error from every method instead of panicking. |
| `NewSecretCipher(key)` | unchanged signature | Returns a one-key ring. |
| `NewSecretCipherRing(primary []byte, previous ...[]byte)` | **new** | Validates every key size, de-duplicates identical keys (for example the primary listed again during a rotation), and refuses two different keys with the same `kid`. Errors name the key's position, never its bytes. |
| `KeyID(key []byte) (string, error)` | **new** | The 16-hex `kid`. |
| `(*SecretCipher).PrimaryKeyID() string`, `KeyIDs() []string` | **new** | For logs. Both are nil-safe. |
| `AssociatedData(purpose string, fields ...string) []byte` | **new** | Canonical encoding (above). |
| `(*SecretCipher).Seal(plaintext string, aad []byte) (string, error)` | **new** | `enc:v2` under the primary key. Empty `aad` → `ErrMissingAAD`. |
| `(*SecretCipher).Open(value string, aad []byte) (Opened, error)` | **new** | Accepts every format and reports what it found; policy is the caller's. Empty → `{Envelope: EnvelopeEmpty}`. Plaintext → `{Plaintext: value, Envelope: EnvelopePlaintext}`. `enc:v1` → tries every ring key, primary first (`aad` is ignored; v1 had none). `enc:v2` → looks up the key by `kid` (`ErrUnknownKeyID`) and requires `aad` (`ErrMissingAAD`). A failed tag check → `ErrEnvelopeAuth`; a bad shape → `ErrMalformedEnvelope`; an unknown version → `ErrUnsupportedEnvelope`. A nil cipher with v1 or v2 → `ErrNoCipher`. |
| `type Opened struct{ Plaintext string; Envelope Envelope; KeyID string; Primary bool }` | **new** | `KeyID` and `Primary` identify the ring key that opened a v1 or v2 value. |
| `type Envelope` + `EnvelopeEmpty/Plaintext/V1/V2/Unknown`, `ClassifyEnvelope(string) Envelope` | **new** | Classifies without opening. |
| `ErrNoCipher` | **new sentinel** | Same message as today (`"encrypted value found but cipher is not configured"`). |
| `ErrMissingAAD`, `ErrUnknownKeyID`, `ErrEnvelopeAuth`, `ErrMalformedEnvelope`, `ErrUnsupportedEnvelope` | **new sentinels** | — |
| `(*SecretCipher).Encrypt` | unchanged | Still `enc:v1` under the primary key. It is still used in legacy mode and in `downgrade` mode. |
| `(*SecretCipher).Decrypt` | two changes | (a) `enc:v1` tries every ring key, not only the single key. (b) `enc:v2` now returns an error wrapping `ErrMissingAAD`; at `61994fe` it came back verbatim as "plaintext" and would have been used as an HMAC key. Plaintext and unknown-prefix pass-through is unchanged. |
| `IsEncrypted` | widened | True for `enc:v1:` **and** `enc:v2:`. |

The legacy `Encrypt`/`Decrypt` pair is **not** marked `Deprecated:`. That would raise SA1019 in
Spine's own legacy-mode store path. The doc comments direct new callers to `Seal`/`Open`.

Implementation note: `uint64(len(x))` in `AssociatedData` may draw a gosec G115 report. Use a
helper or a reasoned `//nolint:gosec`; `make lint-security` must stay clean.

> **Design-check note (2026-10-06) — zero value and key retention.**
> - The table says a zero-value `SecretCipher` "returns an error from every method". The design
>   prototype panics (index out of range) in `Encrypt`, `Decrypt` (v1), `Seal` and
>   `PrimaryKeyID`. The original AC1 listed only `Encrypt`, `Seal` and `Open`. AC1 now covers every
>   method: `PrimaryKeyID` returns `""` and `KeyIDs` returns `nil`. At `61994fe`, a zero-value
>   `Encrypt` also panics, so the error behaviour is new, not a regression fix.
> - The table also says "key bytes are not kept after construction". The prototype keeps a copy in
>   `ringKey.raw` for its duplicate check. Instead, compare the *input* slices while `NewSecretCipherRing`
>   runs, and keep only the derived AEADs and ids. Check this in review; no AC tests it.

### 2. `adapters/store`: bound subscription secrets

The new code goes in `adapters/store/postgres_subscription_secrets.go`. There is one new field on
`PostgresStore` (`postgres.go:28-42`): `secretBinding *SubscriptionSecretBinding`.

```go
type SecretEnvelopeMode string
const (
    SecretEnvelopeMigrate   SecretEnvelopeMode = "migrate"   // default
    SecretEnvelopeStrict    SecretEnvelopeMode = "strict"
    SecretEnvelopeDowngrade SecretEnvelopeMode = "downgrade"
)
type SubscriptionSecretBinding struct {
    WorkspaceID string             // trusted scope (I2); required
    Mode        SecretEnvelopeMode // "" = migrate
}
func (s *PostgresStore) SetSubscriptionSecretBinding(b SubscriptionSecretBinding) error
var ErrSecretCipherRequired, ErrNoSecretBinding, ErrLegacySecretRefused error
```

**AAD.** `crypto.AssociatedData("runtime.event_subscriptions.signing_secret", binding.WorkspaceID, subscriptionID)`.

- `subscription_id` is the primary key (`016_event_subscriptions.sql:5`) and is never updated.
  `UpdateSubscription` filters on it (`postgres_subscriptions.go:84`).
- The row's `workspace_id` column is **not** used. A database writer controls it, and
  `UpdateSubscription` does not even persist it (`postgres_subscriptions.go:80-87`; SMP relies on
  that at `eventdelivery/worker.go:434-442`).

**Modes** (only when a binding is set):

| Mode | Write | Read `enc:v2` | Read `enc:v1` / plaintext | Sweep target |
|---|---|---|---|---|
| `migrate` | `enc:v2` | open (AAD-checked, any ring key) | accepted | `enc:v2` under the primary key |
| `strict` | `enc:v2` | open | **refused** (`ErrLegacySecretRefused`) | `enc:v2` under the primary key; legacy rows are reported, not converted |
| `downgrade` | `enc:v1` | open | accepted | `enc:v1` under the primary key (rollback preparation, §6) |

**Read path.** `GetSubscription` (`:67`) and `scanSubscriptions` (`:147`) call
`openSubscriptionSecret(sub.SubscriptionID, stored)`.

- With no binding, it is today's `decryptSubscriptionSecret`, unchanged.
- With a binding, it calls `cipher.Open` with the row AAD, then applies the mode.
- Errors wrap the sentinel and name the subscription id and the envelope, never the value.
- As today, one row that fails to open fails the whole call (`GetSubscription` and
  `scanSubscriptions` both return the error). Decision 9 explains why rows are not skipped or
  blanked.

**Write path.** `CreateSubscription` (`:14`) and `UpdateSubscription` (`:76`) call
`sealSubscriptionSecret(sub.SubscriptionID, secret)`.

- With no binding, it is today's `encryptSubscriptionSecret`, unchanged (including its
  pass-through of values that are already envelopes).
- With a binding:
  - `""` is stored as `""`.
  - A nil cipher → `ErrSecretCipherRequired` (fail closed: a binding means "seal").
  - An empty `SubscriptionID` → error.
  - An input that is already an envelope is opened with *this row's* AAD and mode, then re-sealed.
    One that does not open is refused, so an opaque envelope from elsewhere cannot be written
    through the API (I4).
  - Otherwise the value is sealed per the mode.

`SetSecretCipher` (`postgres.go:44-51`) and the order of the two setters are unchanged. Validation
happens when the store is used.

> **Design-check note (2026-10-06) — write path: refuse envelope inputs under a binding.**
>
> This replaces the bullet "An input that is already an envelope is opened with this row's AAD and
> mode, then re-sealed".
> - Under a binding, a non-empty input that `ClassifyEnvelope` reports as `V1`, `V2` or `Unknown`
>   is refused with a new sentinel, `ErrEnvelopeInput`, in every mode. Nothing is written.
> - **Why.** The old rule re-sealed a *foreign* `enc:v1` input as this row's v2 in `migrate` mode,
>   because v1 carries no AAD. Probe X4 shows it. That broke I4 and the sentence above that "an
>   opaque envelope from elsewhere cannot be written through the API".
> - **No legitimate caller passes an envelope** (grep, 2026-10-06):
>   - Spine's gateway generates every secret (`handlers_subscriptions.go:61`, `:292`), and its
>     update request has no secret field (`:24-29`).
>   - `BootstrapInternalSubscription` passes `cfg.Token`.
>   - SMP's `AlignSubscriptionSecret` writes the credential. `ensureScopedTargetURL` and SMP
>     TASK-006 step 2 write back the plaintext they listed.
> - With no binding, today's pass-through is unchanged (I1).
>
> **Read path: nested envelopes (I8).** Under a binding, when a value opens and its plaintext is
> itself an envelope:
> - If the inner value is `enc:v2` and opens under **this row's** AAD with a ring key, the inner
>   plaintext is the secret. Unwrap at most one level.
>   - This repairs what an old binary writes after a rollback that skipped `downgrade`. Old
>     `Decrypt` returns `enc:v2:…` as-is, and old `IsEncrypted` does not recognise it, so the old
>     write path wraps it as `enc:v1(enc:v2:…)` (probe X1).
>   - It cannot launder anything: the inner value is authenticated to this row.
> - Otherwise the read fails with a new store sentinel, `ErrNestedEnvelope`.
> - The mode check applies to the **outer** envelope as before. In `strict` mode, an outer `enc:v1`
>   is still refused with `ErrLegacySecretRefused`.
> - Without this rule, the prototype returned the literal `enc:v2:…` string as the HMAC key
>   (probe X5), and the sweep re-sealed it as a secret (probe X1). Every signature was then
>   silently wrong.

Fix the comments at `postgres_subscriptions.go:30-33` and `:41-43`: the "re-encrypted on their
next UpdateSubscription" claim is replaced by a pointer to the sweep.

### 3. `adapters/store`: the re-encryption sweep (exported; standalone and SMP TASK-006 call it)

```go
type SecretSweepIssue struct { SubscriptionID string; Envelope crypto.Envelope; KeyID string; Reason string }
type SecretSweepReport struct {
    Scanned, Rewritten, Current, Empty, Conflicts int
    FromPlaintext, FromV1, Rekeyed, ToV1 int // breakdown of Rewritten
    Refused []SecretSweepIssue
}
func (r SecretSweepReport) Clean() bool // len(Refused)==0 && Conflicts==0
func (s *PostgresStore) ReencryptSubscriptionSecrets(ctx context.Context) (SecretSweepReport, error)
```

1. It requires a binding and a cipher. Otherwise it returns `ErrNoSecretBinding` or
   `ErrSecretCipherRequired` and touches nothing.
2. It reads every row first:
   `SELECT subscription_id, signing_secret FROM runtime.event_subscriptions ORDER BY subscription_id`.
   It closes the cursor before writing, so it never holds a cursor and an `Exec` on a small gated
   pool at the same time (`WorkspaceDBPool`, ADR-012).
3. For each row it opens the value with the binding's AAD and mode.
   - If the value fails to open (wrong or removed key, AAD mismatch from a transplant, legacy
     input in `strict`, unknown envelope), the row is added to `Refused` and left untouched (I4).
   - Empty values count as `Empty`.
   - Values already in the target envelope under the primary key count as `Current`; nothing is
     written.
   - Every other row is re-sealed and written with a compare-and-swap:
     `UPDATE runtime.event_subscriptions SET signing_secret = $2 WHERE subscription_id = $1 AND signing_secret = $3`.
     - Zero rows affected counts as a `Conflicts` entry: a concurrent rotation or update won (I5),
       and the next sweep looks at the row again.
     - `updated_at` is deliberately not bumped. The secret itself is unchanged.
4. DB errors are returned with the partial report.

**Trust boundary (stated in the docs).** In `migrate` mode, the sweep trusts the legacy rows present
when it runs. It cannot tell a legitimate pre-upgrade `enc:v1` row from a planted copy, and nothing
can. After the operator switches to `strict`, or rotates the key and drops the old one, no legacy
row is accepted again (§6).

> **Design-check note (2026-10-06) — the laundering persists, and the sweep needs a per-row
> record.**
>
> **The trust boundary lasts the whole time `migrate` is active, not just the first sweep.**
> - The sweep runs on every store build. In shared mode that is every first request and every
>   reload after idle eviction; in SMP it is every pair start.
> - So a copy of another workspace's pre-upgrade `enc:v1` value, planted at **any** time before the
>   `strict` flip, becomes a clean v2 row bound to the victim workspace.
> - `strict` then accepts that row. Probe X2 shows it.
> - Consequence: in the default `migrate` posture, the audit's P3-10 attacker still succeeds. That
>   attacker holds "a backup, snapshot or replica read of A" taken before the upgrade, which is
>   exactly a v1 copy.
> - P3-10 is closed only once `strict` is set, or once every key that sealed a v1 value has left
>   the ring. The documentation and the CHANGELOG must say this (AC16).
>
> **Report and log each conversion.** `SecretSweepReport` gains these fields:
>
> ```go
> Converted []SecretSweepConversion // one per Rewritten row
> Repaired  int                     // nested-envelope rows unwrapped (I8, §2 note)
>
> type SecretSweepConversion struct {
>     SubscriptionID string
>     From           crypto.Envelope
>     FromKeyID      string // "" for plaintext
> }
> ```
>
> - Neither type carries secret material.
> - `PrepareSubscriptionSecrets` logs one `Info` line per conversion.
> - This gives an auditable trail. A `FromPlaintext` or `FromV1` conversion logged *after* a
>   workspace's first clean sweep was not written by this release: it is either a planted value or
>   an old binary's write. The `strict` runbook (§6) tells the operator to rotate that
>   subscription's signing secret before flipping.
>
> **Nested envelopes in the sweep.**
> - A row repaired by the §2 rule is re-sealed to the target with the *inner* plaintext. It counts
>   in `Rewritten` and in `Repaired`.
> - A row refused with `ErrNestedEnvelope` goes to `Refused` and is left untouched.

### 4. Standalone wiring (`service/`)

**Configuration** (`service/cmd/spine/serve_config.go`):

- `loadSecretCipher` (`:235-248`) also reads `SPINE_SECRET_PREVIOUS_KEYS`. This is a
  comma-separated list of base64 keys; whitespace is trimmed and empty entries are ignored.
  - Each entry is parsed with `ParseEncryptionKey`.
  - The ring is built with `NewSecretCipherRing(primary, previous...)`.
  - Errors name the entry index, never the value.
  - `SPINE_SECRET_PREVIOUS_KEYS` without `SPINE_SECRET_ENCRYPTION_KEY` is a startup error.
  - The production gate (`:237-240`) is unchanged.
- New `loadSecretEnvelopeMode()` reads `SPINE_SECRET_ENVELOPE_MODE`: empty → `migrate`; `migrate`,
  `strict` or `downgrade` are accepted (case-insensitive); anything else is a startup error.
- `serve_cmd.go:50-56`: after the cipher is built, also load the mode.
  - With a key, log `secret envelope configured` with `primary_key_id`, `previous_key_ids` and
    `mode`.
  - Without a key, a non-default mode logs a warning that it is ignored. The existing plaintext
    warning stays.

**One shared helper.** New file `service/internal/workspace/subscription_secrets.go`; `service/cmd/spine`
already imports this package:

```go
// PrepareSubscriptionSecrets binds st's subscription secrets to workspaceID and runs the sweep.
// cipher == nil → no-op (plaintext dev posture, unchanged). Binding errors are returned
// (programming error). Sweep errors are logged and returned; callers do not fail startup on them.
func PrepareSubscriptionSecrets(ctx context.Context, st *store.PostgresStore, cipher *spinecrypto.SecretCipher,
    workspaceID string, mode store.SecretEnvelopeMode, log *slog.Logger) (store.SecretSweepReport, error)
```

**What the helper logs:**

- one line, `subscription secrets swept`, with `workspace_id`, `mode`, `primary_key_id` and every
  report counter;
- one `Error` per refused row, with `subscription_id`, `envelope`, `key_id` and `reason`;
- in `migrate` mode, after a clean sweep, an `Info` hint: "all signing secrets are enc:v2 under
  the primary key; set SPINE_SECRET_ENVELOPE_MODE=strict to refuse legacy envelopes". When
  `SPINE_ENV=production`, the hint is logged at `Warn`.

**Call sites** (both run before anything reads a signing secret):

- **Single-workspace (file resolver).** `buildStore` (`serve_builders.go:457-479`) gains a `mode`
  parameter. After `pgStore.SetSecretCipher(secretCipher)` (`:477`) it calls
  `PrepareSubscriptionSecrets(ctx, pgStore, secretCipher, cfg.ID, mode, log)`. `cfg.ID` is the
  resolver's id: `SPINE_WORKSPACE_ID`, default `"default"` (`file_provider.go:41-44`), the same id
  the gateway uses (`handlers_subscriptions.go:71-74`).
  - This runs before `startEventDelivery`'s bootstrap (`serve_builders.go:367-375`).
  - The call site `serve_cmd.go:95` passes the mode.
- **Shared mode (db / platform-binding).** Add `SecretEnvelopeMode store.SecretEnvelopeMode` to
  `PoolConfig` (next to `SecretCipher`, `pool.go:233-236`) and to `ServicePool` (`:174`, `:283`).
  Thread it into `buildServiceSet` (`:789`; call site `:524`; test call sites `pool_test.go:242`,
  `:1656`).
  - After `pgStore.SetSecretCipher(cipher)` (`:822`), call
    `PrepareSubscriptionSecrets(ctx, pgStore, cipher, cfg.ID, mode, observe.Logger(ctx))`.
  - This runs before the builder hook (`:1031-1032`) wires delivery (`serve_resolver.go:277-288`).
  - Wire the field from `buildWorkspaceResolver` (`serve_resolver.go:408-415`, `:445-452`).
  - A workspace is swept each time its `ServiceSet` is built: the first request after start, and
    again after idle eviction. The sweep costs one small `SELECT` per build.

> **Design-check note (2026-10-06) — a time limit on the sweep, and the shared-mode operator
> command.**
>
> **Time limit.**
> - `buildServiceSet` runs under the pool's long-lived context (`pool.go:524` passes `p.ctx`), and
>   every request for the workspace waits on `initializeEntry`.
> - The sweep's compare-and-swap `UPDATE` waits on the row lock of any open transaction that holds
>   the row, for example an operator's `psql` session or a stuck writer. That would stall the
>   workspace's first request indefinitely. In file mode it would stall startup.
> - So `PrepareSubscriptionSecrets` runs the sweep under its own
>   `context.WithTimeout(ctx, subscriptionSweepTimeout)` (a 10 s package constant).
> - On timeout it logs and returns the error. The store stays bound, startup continues, and the next
>   build sweeps again.
>
> **Shared-mode command (Owner question 2, default changed to *include*).**
> - In shared mode, `strict` and dropping an old key are both unverifiable until every workspace has
>   been loaded.
> - A premature `strict` flip leaves an idle workspace's legacy rows refused, and the sweep does not
>   convert them in `strict`. Because of the fail-whole-call list (Decision 9), that workspace's
>   `ListActiveSubscriptionsByEventType` then fails, so **all** of its webhook fan-out stops.
>   Dropping an old key too early does the same through `ErrUnknownKeyID`.
> - So this task adds `spine secrets reencrypt --all-workspaces`, modelled on
>   `spine migrate --all-workspaces` (`cmd_migrate.go:66-160`). For each registry workspace it:
>   - opens the workspace DB;
>   - builds the ring from the same env;
>   - binds with the workspace id and the configured mode;
>   - sweeps and prints the counters, never values.
> - It exits non-zero unless every workspace's report is `Clean()`.
> - Without `--all-workspaces`, it sweeps the single file-mode workspace.
> - See AC23.

### 5. Documentation

- **`docs/integration-guide.md` §2.1 (`:100-109`)** is rewritten. It covers:
  - both wire formats and what the AAD binds (workspace scope + subscription id);
  - the key ring and `kid`;
  - `SPINE_SECRET_PREVIOUS_KEYS` and `SPINE_SECRET_ENVELOPE_MODE`;
  - the sweep and its trust boundary;
  - the runbooks in §6.

  The false "no data migration is required / re-encrypted on next update" sentence is removed. The
  env table at `:608-610` gains the two new variables.
- **`.env.example:78-88`:** the same correction, plus the two new variables, commented out.
- **`README.md`:** `:51` gains the two variables. Fix the stale `internal/crypto` path at `:233` to
  `core/crypto`.
- **`architecture/security-model.md` §5.3 (`:217-222`):** one bullet stating that the at-rest
  encryption key rotates through a ring (old and new keys valid together, as `:222` requires).
- **`CHANGELOG.md`** staging area: a **Security** entry. It covers P3-10 and P3-1, the new
  variables, the behaviour change of `Decrypt` on `enc:v2:`, and the upgrade and rollback notes.

> **Design-check note (2026-10-06) — three statements the docs must make** (AC16).
> 1. **When P3-10 is actually closed.** Only after `strict`, or after the retirement of every key
>    that sealed an `enc:v1` value. In `migrate` mode, a pre-upgrade v1 copy of another
>    workspace's column still transplants, and the next sweep launders it into valid v2 (§3 note,
>    probe X2).
> 2. **One workspace per runtime database** (ADR-012). `workspace_registry.database_url` has no
>    uniqueness constraint (`migrations/registry/001_workspace_registry.sql:11`). Two workspaces
>    registered on one database refuse each other's rows once bound (probe X3).
>    - That fail-closed outcome is intended: such a topology already fans each workspace's events
>      out to the other's subscriptions, because `ListActiveSubscriptionsByEventType` has no
>      workspace filter.
>    - The docs must say so, so that the error is not a surprise.
> 3. **The binding is only as unique as the workspace id.** In file mode the id defaults to
>    `"default"` (`file_provider.go:41-44`). Two single-workspace deployments that share one key
>    and both use `"default"` can transplant v2 values between them. Use a distinct key per
>    deployment, or else a distinct `SPINE_WORKSPACE_ID`.

### 6. Data, format and migration (no schema change)

There is no migration file. Existing `enc:v1` and plaintext rows keep working in the default mode.
The operator procedures below go into §2.1 of the integration guide.

- **Upgrade (one replica).**
  1. Deploy. The default mode is `migrate`.
  2. Each store build sweeps its workspace. Plaintext and v1 rows become v2.
  3. When every workspace has logged a clean sweep with zero legacy rows, set
     `SPINE_SECRET_ENVELOPE_MODE=strict`.
  4. From then on, an `enc:v1` value copied from a pre-upgrade backup, or a plaintext value planted
     in the DB, is refused.
- **Upgrade (several replicas).**
  1. Deploy with `SPINE_SECRET_ENVELOPE_MODE=downgrade` first. Every replica can then read v2, but
     nothing writes it yet.
  2. Switch to `migrate`.
  3. Later, switch to `strict`.
- **Key rotation.**
  1. Generate K2.
  2. Set `SPINE_SECRET_ENCRYPTION_KEY=K2` and `SPINE_SECRET_PREVIOUS_KEYS=K1`, then restart. The
     sweep re-keys every row. In shared mode, this happens as each workspace is loaded.
  3. When every workspace has logged a clean sweep with `kid(K2)`, remove K1 from the ring.
  4. Keep K1 in escrow as long as a backup taken before step 2 may be restored. To restore such a
     backup, put K1 back in the ring and use `migrate` until the sweep is clean.
  5. Dropping K1 also makes every `enc:v1` value written under K1 unopenable. This is the second way
     to close the v1-transplant path.
  6. Rotating the *webhook signing secret* is a separate operation (`POST /subscriptions/{id}/rotate-secret`,
     `handlers_subscriptions.go:275-299`).
- **Rollback below this release.**
  1. Set `SPINE_SECRET_ENVELOPE_MODE=downgrade` and restart. In shared mode, load every workspace.
  2. Confirm `current` equals the number of non-empty rows.
  3. Roll back the binary.

  An old binary treats `enc:v2:` as plaintext (`secret.go:94-96`). Without this procedure, every
  webhook would be signed with the literal envelope string and rejected by its receiver. The
  standalone internal subscription would self-heal on the old binary's bootstrap
  (`bootstrap.go:61-81`); API-created ones would need `rotate-secret`.
- **Changing the workspace id** (`SPINE_WORKSPACE_ID` in file mode; a re-registered workspace in
  shared mode).
  1. Run in `downgrade` under the old id.
  2. Change the id.
  3. Switch back to `migrate`.

  Otherwise the v2 rows fail closed (I3). That is deliberate: the id is part of the binding.

> **Design-check note (2026-10-06) — runbook amendments.**
> - **Upgrade, step 3 (before `strict`).**
>   - In shared mode, run `spine secrets reencrypt --all-workspaces` instead of waiting for every
>     workspace to load (§4 note).
>   - Review the per-row conversion log (§3 note). If a workspace logged a `plaintext` or `enc:v1`
>     conversion after its first clean sweep, run `rotate-secret` on that subscription before
>     flipping. Its value may be a planted copy that the sweep laundered.
> - **Key rotation, step 3.** In shared mode, "every workspace has logged a clean sweep" means a
>   clean `spine secrets reencrypt --all-workspaces` run.
> - **Rollback that skipped `downgrade`.**
>   - Rows the old binary wrote back become `enc:v1(enc:v2:…)`.
>   - On re-upgrade, the §2 nested-envelope rule repairs rows whose inner value is this row's own v2,
>     and the sweep rewrites them (`Repaired`).
>   - Rows that cannot be repaired are reported and need `rotate-secret`.

### 7. Backward compatibility

- **Library callers (SMP).** There is no source change and no behaviour change until they call
  `SetSubscriptionSecretBinding`. The two deltas reachable without a binding are:
  - `Decrypt("enc:v2:…")` now errors instead of returning the envelope. No such value exists in
    an unbound deployment.
  - `IsEncrypted` is true for `enc:v2:`. SMP does not call it; grep of SMP `cmd/` and `internal/`
    finds none.
- **Error strings.** `Decrypt`'s authentication failure becomes
  `decrypt: <ErrEnvelopeAuth text>` instead of `decrypt: cipher: message authentication failed`.
  No test in either repo matches the old text (grep, 2026-10-06).
- **Mixed versions.** Covered by `downgrade` (§6). Any binary older than this one must never see a
  v2 row.
- **Standalone tests.** The existing `TestEventSubscription_SigningSecretEncryption`
  (`adapters/store/postgres_integration_test.go:1266-1357`) uses no binding. It must keep passing
  unchanged, and it is the legacy-mode guard at integration level.

### 8. What changes for SMP

**Now (this task's compatibility gate).** At SMP `80995559`, with `replace ../spine` at this
task's branch head, nothing changes:

- `boot.go:414-425`, `dev_cli.go:299-309` and `secrets_cli.go:199-209` still build one-key
  ciphers.
- `provision_steps_runtime.go:418-419`, `eventdelivery/worker.go:351-352` and
  `secrets_cli.go:198-208` install them with no binding. They therefore keep writing `enc:v1` (or
  plaintext without a key) and keep reading plaintext and `enc:v1`.
- SMP's fan-out never decrypts. It reads `subscription_id` only (`spinecore/eventemitter.go:144-150`).

Verified in the design phase against a prototype overlay:

- `go build ./cmd/... ./internal/...` passes;
- `go vet` of `cmd/customer-app`, `internal/customer/{eventdelivery,workspace,stepevents}` passes;
- the unit tests of those three `internal/customer` packages pass.

The DB-backed tests were skipped, because `TEST_PROVISIONING_DSN` was unset. AC18 makes them
mandatory.

**SMP TASK-006 (P3-1.smp, P3-10.smp, P3-11.smp) must then:**

1. **Build the ring** at the three cipher sites (`boot.go:414-425`, `dev_cli.go:299-309`,
   `secrets_cli.go:199-209`), with `NewSecretCipherRing` plus a previous-keys variable.
   Recommended name: the same `SPINE_SECRET_PREVIOUS_KEYS`.
2. **Bind every cipher-installed store** to the SMP workspace id: the provisioner (`ws.ID`,
   `provision_steps_runtime.go:418-419`), the worker (`workspaceID`, `worker.go:351-352`) and the
   realign/recovery CLI (`workspaceID`, `secrets_cli.go:198-210`).
   - All three must use the same id and mode. A mismatch makes the rows unreadable (I3).
   - Route all three through one helper (for example in `internal/customer/stepevents`) so they
     cannot diverge.
3. **Run `ReencryptSubscriptionSecrets`** inside the worker's blocking pre-dispatch ensure loop
   (`worker.go:375-390`), so each warm workspace is converted before its first dispatch.
   - The ensure path itself lists every row (`ensureScopedTargetURL` → `ListSubscriptions`,
     `worker.go:458-460`). A row refused in `strict` mode therefore blocks the pair's dispatch
     until fixed. Choose `strict` only after a clean sweep.
4. **Provide an all-workspaces sweep** for rotation and verification (for example
   `customer-app secrets reencrypt`), because SMP owns the workspace list.
5. **Decide encrypt-by-default** (the SMP half of P3-1; at present `boot.go:412-413` keeps
   plaintext when the key is unset). Keep retired keys in escrow per ADR-035 §7, which already
   lists `SPINE_SECRET_ENCRYPTION_KEY` as escrow-required (SMP `architecture/adrs/ADR-035-coordinated-recovery-point-contract.md:257-259`).
6. **Bump `.spine-revision`** to a Spine commit that contains this task and TASK-010. SMP TASK-012
   makes the final INIT-023 bump.

> **Design-check note (2026-10-06) — SMP handoff, checked against `batch7/E4-TASK-006`.**
>
> **Already covered there:** canonical-id binding through one constructor (its I1/I2, Decision 1);
> `downgrade` before rollback (its §10); a missing key or a degenerate key refused outside dev; an
> all-workspaces sweep.
>
> **Re-verified on 2026-10-06 against the prototype overlay:** SMP `go build ./cmd/... ./internal/...`
> and `go vet` of the four packages pass. The unit tests of `internal/customer/{eventdelivery,stepevents,workspace}`
> pass (`TEST_PROVISIONING_DSN` was unset).
>
> **New items for SMP TASK-006:**
> 1. **Refuse equal keys.** Refuse to boot when `SPINE_SECRET_ENCRYPTION_KEY` (or any previous key)
>    decodes to the same 32 bytes as `SECRET_ENCRYPTION_KEY`. This is code-traced, not executed:
>    - SMP's legacy `platform.internal_credentials` rows and `workspace_secrets` rows are AES-GCM
>      with nil AAD under `SECRET_ENCRYPTION_KEY`, as separate `ciphertext` and `nonce` columns
>      (`internal/customer/internalcreds/store.go:608-629`, `internal/secrets/store.go:579-600`).
>    - With equal keys, an attacker can re-frame such a row as `"enc:v1:" + base64(nonce || ciphertext)`.
>      In `migrate` mode it opens as a signing secret.
>    - That row can be workspace A's `spine_webhook` credential, which is A's *receiver* key. The
>      attacker needs only a platform-DB backup, and no runtime-DB copy of A.
>    - SMP TASK-006's dev compose sets both keys to 32 zero bytes, so they are equal in dev.
>    - v2's HKDF subkey already separates the two key uses. Only v1 acceptance is exposed, so the
>      risk ends at `strict`.
> 2. **A rolling deploy or a rollback without `downgrade` nests envelopes.** An old SMP binary's
>    `ensureScopedTargetURL` write-back wraps a v2 row as `enc:v1(enc:v2:…)`. Its `AlignSubscriptionSecret`
>    overwrites the row with the credential, which heals it. Spine's §2 nested rule repairs the
>    remaining rows on re-upgrade. SMP's single-replica guard keeps this to the rollback case.
> 3. **`ErrEnvelopeInput`** (§2 note) does not affect SMP: every SMP write passes a plaintext
>    secret.

### 9. Coordination and ordering

- **No dependencies.** This task touches `core/crypto`, `adapters/store` (subscriptions only),
  `service/cmd/spine` and `service/internal/workspace`.
- **TASK-010 (webhook v2 signatures, P3-11) also touches `adapters/delivery`.** This task adds a
  test file there only (§AC10). No shared production file.
- **TASK-015 (shared-mode tenant scoping) also touches `service/internal/workspace`.** This task's
  `pool.go` edit is limited to the `PoolConfig`/`ServicePool` field and the `buildServiceSet`
  call, so merge conflicts, if any, are mechanical.
- **SMP TASK-006 starts after this task merges.**

---

## Decisions

1. **DECIDED — The workspace in the AAD comes from process configuration, not the row's
   `workspace_id` column.**
   - Rejected: binding the AAD to the row's columns. The attacker of P3-10 writes B's database and
     can set `workspace_id` (and `subscription_id`) to A's values, so the transplant would still
     open.
   - The column is also not persisted by `UpdateSubscription`.
   - In both deployments, each store is opened for one known workspace (ADR-012 DB-per-workspace),
     so the trusted scope is free.
2. **DECIDED — `subscription_id` is in the AAD too.**
   - It is immutable and costs nothing.
   - It stops naive swaps between rows of the same workspace.
   - It does not try to defend against a writer who owns that same workspace's database: that
     writer can already change `target_url`.
3. **DECIDED — The key id is derived (HKDF) rather than operator-assigned.**
   - Rejected: `kid=base64key` labels. They add config, invite label reuse, and need a migration
     story for existing keys.
   - A derived id is stable, collision-checked at ring build, and safe to log. It is a one-way
     64-bit HKDF output.
4. **DECIDED — AES-256-GCM with an HKDF subkey, random 96-bit nonces.**
   - Rejected: XChaCha20-Poly1305. It needs `golang.org/x/crypto` (not a Spine dependency today)
     and gains nothing at one-row-per-subscription volume.
   - Rejected: AES-GCM-SIV. It is not in the standard library.
   - The subkey keeps v1 (raw key, nil AAD) and v2 key use separate.
5. **DECIDED — Turn `SecretCipher` into the ring in place, rather than adding a new type.**
   SMP stores `*spinecrypto.SecretCipher` in struct fields and signatures (`deps.go:149`,
   `worker.go:162`, `provisioner.go:333`, `provisioner_wiring.go:133`). They keep compiling, and
   `NewSecretCipher` becomes the one-key case.
6. **DECIDED — v2 is opt-in per store through a binding**, rather than a global switch or a change
   to `Encrypt`.
   - A global flip would change SMP's behaviour on the next `.spine-revision` bump, outside SMP
     TASK-006.
   - Changing `Encrypt` to emit v2 would have no AAD to bind.
7. **DECIDED — Legacy acceptance is controlled by trusted process config (`strict`), not by a
   marker in the database.**
   - Rejected: "auto-strict after the first clean sweep". It would have to persist that fact. A
     DB marker can be deleted by the very DB writer it defends against. An in-process flag resets
     on every restart, and the next startup sweep would then launder a planted legacy row.
   - Key rotation is the other way to retire v1 values (§6).
8. **DECIDED — The sweep is an explicit compare-and-swap pass, not "re-encrypt on next update".**
   - The status quo never fires (P3-1).
   - A SQL migration cannot do it, because it would need the key.
   - CAS keeps a concurrent `rotate-secret` from being reverted.
   - The sweep does not bump `updated_at`, because the secret is unchanged.
9. **DECIDED — A row that does not open fails its read call, as a wrong-key row does today.**
   - Rejected: skipping the row in list results. Skipped rows silently lose events.
   - Rejected: blanking the secret. That would send unsigned deliveries
     (`webhook_dispatcher.go:219`), or wipe the secret on read-modify-write callers. SMP's
     `ensureScopedTargetURL` and `AlignSubscriptionSecret` both `UpdateSubscription` a listed row.
   - The limitation of the fail-whole-call behaviour is recorded in the follow-ups.
10. **DECIDED — Ship a `downgrade` mode.** It costs one branch in the write path and one in the
    sweep, and it gives a safe rollback and a multi-replica rollout path. Without it, a rollback
    would force every webhook receiver to re-key (§6). This is Owner question 3.
11. **DECIDED — `ParseEncryptionKey` is not changed.** It still accepts degenerate keys such as
    all-zero ones (gap1 PoC `TestPoC_AllZeroKeyAccepted`). That is not a numbered audit finding,
    and SMP's own test uses an all-zero key (`create_initial_actors_test.go:526`). See follow-ups.
12. **DECIDED — No schema change.** The envelope is self-describing inside the existing text
    column, and the policy lives in configuration.
13. **DECIDED (design check, 2026-10-06) — Under a binding, a write whose input is an envelope is
    refused (`ErrEnvelopeInput`).**
    - Rejected: "open with this row's AAD, then re-seal". In `migrate` mode that launders a foreign
      `enc:v1` value, because v1 has no AAD (probe X4).
    - Rejected: refusing only v1 inputs. No caller in Spine or SMP passes any envelope (§2 note),
      so the simpler rule loses nothing.
14. **DECIDED (design check, 2026-10-06) — Nested envelopes: unwrap one level when the inner value
    is this row's own v2; otherwise refuse (`ErrNestedEnvelope`).**
    - Rejected: always refusing. In SMP, a refused row fails `ListSubscriptions`. That blocks
      `AlignSubscriptionSecret`, which could otherwise heal the row, so a single accidental
      rollback would wedge the workspace's delivery until manual repair.
    - Rejected: unlimited unwrapping. Each extra level is attack surface with no producer.
15. **DECIDED (design check, 2026-10-06) — The sweep is time-limited and records each
    conversion.**
    - The time limit stops a row lock from stalling workspace initialisation.
    - The per-row record makes the `migrate`-window laundering (probe X2) auditable. Nothing can
      prevent it without persisted state (Decision 7).

## Owner questions

The design works without answers, using the stated defaults.

1. **Default `SPINE_SECRET_ENVELOPE_MODE` for standalone in this release.**
   - *Default:* `migrate`. Strict mode is the documented closing step, and production gets a `Warn`
     hint after a clean sweep.
   - `strict` cannot be the upgrade default, because it would refuse every existing row without
     converting it.
   - Alternative: make the next minor release default to `strict` (follow-up).

   > **Design-check note (2026-10-06).** This answer decides whether P3-10 is closed by default.
   > - In `migrate` mode, the audit's own attacker defeats the fix. That attacker holds a
   >   pre-upgrade copy of another workspace's column, which is an `enc:v1` value, and the next
   >   sweep turns the plant into a valid v2 row (probe X2).
   > - With `migrate` as the default, the CHANGELOG must say plainly that P3-10 is closed only after
   >   `strict` or key retirement (AC16).
   > - The owner should also decide when the follow-up "strict by default" ships.
2. **An operator command for shared mode** (`spine secrets reencrypt [--all-workspaces]`, built
   the same way as `spine migrate --all-workspaces`, `cmd_migrate.go:66-160`).
   - *Default:* not in this task. Shared mode sweeps each workspace when it is loaded, and the
     rotation runbook keeps the previous key until every workspace has logged a clean sweep.
   - Without the command, idle workspaces delay dropping an old key.

   > **Design-check note (2026-10-06): default changed to *include* (AC23).**
   > - Without the command, a shared-mode operator cannot verify that every workspace is clean.
   > - A premature `strict` flip, or an early key drop, then stops **all** webhook fan-out for every
   >   idle workspace (§4 note).
   > - The command is mechanical, following `cmd_migrate.go`.
   > - The owner can still descope it. In that case the docs must say that `strict` and key drops
   >   in shared mode need every workspace loaded and its sweep log checked.
3. **Rollback support through `downgrade` mode.**
   - *Default:* include it (Decision 10).
   - Alternative: drop it, and document that a rollback below this release requires rotating every
     API-created webhook secret.

> **Orchestrator ruling (2026-10-07) — owner questions take their defaults, pending the owner.**
> 1. **Q1:** `SPINE_SECRET_ENVELOPE_MODE` defaults to `migrate` for standalone in this release. The
>    CHANGELOG states plainly that P3-10 closes only after `strict` or old-key retirement;
>    strict-by-default is a recorded follow-up (KNOWN-LIMITATIONS, Out of scope).
> 2. **Q2:** `spine secrets reencrypt [--all-workspaces]` is included (AC23).
> 3. **Q3:** the `downgrade` mode is included.

## Acceptance Criteria

Every new test runs in plain `go test ./...` (CI runs only that, `.github/workflows/ci.yml`),
except the tests marked *integration*. Those use `-tags integration` and are run locally against
the test DB, and the run is recorded in the Implementation Notes.

1. **API surface and compatibility.**
   - Every identifier in the §1 table exists with the stated signature.
   - `NewSecretCipher`, `Encrypt`, `Decrypt`, `ParseEncryptionKey`, `EncryptionKeySize`,
     `SetSecretCipher`, `NewPostgresStoreWithQuerier`, the `SubscriptionStore` interface and
     `BootstrapInternalSubscription` have unchanged signatures.
   - `core/crypto` imports only the standard library.
   - `make lint-boundaries` and `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest`
     pass. *(Design-check fix: the original command built into `./embed-smoketest`, not `./bin/`;
     the form above is the one CI uses, `.github/workflows/ci.yml:50-53`.)*
   - A zero-value `SecretCipher{}` returns an error, with no panic, from `Encrypt`, `Decrypt` (on an
     `enc:v1:` value), `Seal` and `Open`. Its `PrimaryKeyID()` returns `""` and its `KeyIDs()`
     returns `nil`. *(Design-check: the prototype panics in four of these.)*
2. **Wire format is pinned** (`core/crypto`, `TestV2_GoldenVector`).
   - A committed fixed key, AAD and `enc:v2` string open to a committed plaintext.
   - `KeyID(fixedKey)` equals a committed 16-hex value.
   - The envelope matches `^enc:v2:[0-9a-f]{16}:[A-Za-z0-9+/]+=*$`.
   - *Mutation check:* changing either HKDF info string, or the GCM-AAD label, fails the test.
3. **P3-10 regression at the crypto level.** These are committed ports of
   `poc/crypto-secrets-auth/cipher_transplant_test.go` and of `TestPoC_CiphertextSwapAcrossRowsDecrypts`
   in `poc/gap1-crypto-secrets-reaud/crypto_poc_test.go`.
   - `TestV2_TransplantAcrossWorkspacesFails`: one deployment-wide key; a value sealed for
     `(ws-A, sub-1)` fails to open with `(ws-B, sub-1)`, and `errors.Is(err, ErrEnvelopeAuth)`
     holds. The own row still opens with `Envelope==EnvelopeV2` and `Primary`.
   - `TestV2_SwapAcrossSubscriptionsFails`: the same check for `(ws-X, sub-A)` versus `(ws-X, sub-B)`.
   - `TestAssociatedData_Canonical`: `("ws-A","1")` ≠ `("ws-A1","")`.
   - *Mutation check:* making `v2AAD` ignore the caller AAD fails both port tests.
4. **AAD is mandatory for v2 and the legacy API is strict about v2.**
   - `Seal(_, nil)` and `Open(v2, nil)` return `ErrMissingAAD`.
   - `Decrypt(v2)` returns an error, not the input.
   - `Decrypt("legacy-plaintext")` and `Encrypt` behave exactly as on `61994fe`: pass-through, and
     an `enc:v1:` prefix.
   - `ClassifyEnvelope` table: `""`, `whsec_x`, `enc:v1:…`, `enc:v2:…`, `enc:v9:…` (Unknown),
     `enc:value` (Plaintext).
   - `Open("enc:v9:…")` returns `ErrUnsupportedEnvelope`.
   - *Mutation check:* restoring the `61994fe` pass-through of `enc:v2:` in `Decrypt` fails
     `TestV2_RequiresAAD`.
5. **Key ring.**
   - With ring `(K2, K1)`, a K1 `enc:v2` value and a K1 `enc:v1` value open with
     `Primary == false`.
   - `Seal` embeds `PrimaryKeyID()`.
   - With ring `(K2)` alone, the K1 v2 value returns `ErrUnknownKeyID` and the K1 v1 value returns
     `ErrEnvelopeAuth`.
   - Relabelling a v2 value with another ring key's `kid` returns `ErrEnvelopeAuth`.
   - Listing the primary again in `previous` is de-duplicated.
   - A forced `kid` collision between different keys (through an unexported test hook) is refused
     at construction.
   - The 20 000-iteration nonce-uniqueness port (`TestPoC_NonceUniqueness`) passes for v2.
6. **Store binding and legacy mode** (`adapters/store`, unit tests with an in-memory querier
   modelled on `poc/verify-r2-1`).
   - `SetSubscriptionSecretBinding` refuses an empty `WorkspaceID` and an unknown mode, and maps
     `""` to `migrate`.
   - `TestLegacyMode_Unchanged`: with no binding, `CreateSubscription` writes `enc:v1:`, and
     `ListSubscriptions` returns both a v1 row and a plaintext row correctly.
     `ReencryptSubscriptionSecrets` returns `ErrNoSecretBinding` and writes nothing.
   - A bound store with a nil cipher refuses a non-empty write with `ErrSecretCipherRequired`.
   - The existing *integration* test `TestEventSubscription_SigningSecretEncryption` passes
     unmodified.
7. **P3-10 regression at the store level** (`TestP310_CrossWorkspaceTransplantRefused`).
   - Two stores share one cipher and are bound to `ws-A` and `ws-B`, over two separate in-memory
     databases.
   - A's row `sub-1` is copied into B's row `sub-1`, and B's `workspace_id` column is also set to
     `"ws-A"`. Then:
     - `GetSubscription` fails with `ErrEnvelopeAuth`;
     - `ListSubscriptions` and `ListActiveSubscriptionsByEventType` fail;
     - B's sweep reports the row in `Refused` and does not rewrite it (no laundering).
   - *Mutation checks:*
     - binding the AAD to `""` instead of the workspace fails the test;
     - sealing with `enc:v1` under a binding (this is `61994fe`'s behaviour) fails the test.
8. **Strict mode** (`TestP310_StrictRefusesLegacyEnvelopes`).
   - In a `strict` store, an `enc:v1` value sealed for another workspace and a planted plaintext
     value are each refused by `GetSubscription` with `ErrLegacySecretRefused`.
   - The sweep reports both rows in `Refused`, and the stored bytes are unchanged.
   - *Mutation check:* removing the strict check fails the test.
9. **Writes under a binding refuse envelope inputs** *(rewritten by the design check, Decision 13)*.
   - **`TestBoundWrite_RefusesEnvelopeInput`:** in each of `migrate`, `strict` and `downgrade`,
     `CreateSubscription` and `UpdateSubscription` are called with three secrets:
     - an `enc:v1` value this ring opens, sealed for another workspace;
     - an `enc:v2` value sealed for this very row;
     - `enc:v9:x`.
   - Each call returns `ErrEnvelopeInput` and leaves the stored bytes unchanged.
   - A plaintext secret is sealed per the mode. An empty secret is stored as `""`.
   - *Mutation check:* restoring the original rule ("open with this row's AAD, then re-seal") fails
     the foreign-v1 case in `migrate` mode. That is probe X4.
   - Unbound stores keep the `61994fe` pass-through. `TestLegacyMode_Unchanged` asserts it.
10. **P3-1 regression (sweep).**
    - **`TestP31_SweepEncryptsPlaintextAndV1`** (store). This is the port of `poc/verify-r2-1` step 2.
      - Setup: a plaintext row written by a store without a key, a v1 row written by a legacy
        keyed store, and an empty row.
      - A `migrate` sweep reports `Rewritten==2`, `Empty==1` and `Clean()`.
      - Both rows are then `enc:v2:<primary kid>:` and read back to their original secrets.
      - A second sweep performs **no writes** (`Current==2`, Exec count unchanged).
    - **`TestP31_KeyAddedLater_SweepEncryptsBootstrapRow`** (`adapters/delivery`). This is the
      port of the full r21 PoC.
      - `BootstrapInternalSubscription` runs with no key, then a keyed store is bound and swept.
      - The raw column is then v2.
      - `computeHMAC(body, rawColumn) != computeHMAC(body, token)`: the dump no longer forges.
      - A later keyed bootstrap reads the token back and issues no `UPDATE`.
    - *Mutation checks:*
      - a no-op sweep (this is `61994fe`'s behaviour) fails both tests;
      - removing the "already current" check fails the idempotence assertion.
11. **Compare-and-swap.**
    - Unit `TestP31_SweepLosesToConcurrentWrite`: a test hook rotates the secret between the
      sweep's read and its write. The sweep reports `Conflicts==1` and `Rewritten==0`, and the
      row reads back as the rotated secret.
    - *Integration:* the same against real Postgres. A second connection updates the row inside an
      open transaction that commits before the sweep's `UPDATE`. Same assertions.
    - *Integration (design check):* a lock-wait variant. The second connection's transaction still
      holds the row lock when the sweep's `UPDATE` starts, and commits afterwards. This exercises
      Postgres's re-evaluation of the `WHERE` clause after the lock is released. Same assertions.
    - *Mutation check:* dropping `AND signing_secret = $3` fails the integration test.
12. **Rotation and downgrade sweeps.**
    - **`TestP310_RotationRekeysToPrimary`.**
      - Rows sealed under K1 (one v2, one v1) are swept by ring `(K2, K1)`, giving
        `Rewritten==2` and `Rekeyed>=1`.
      - A `strict` store with ring `(K2)` alone then reads both.
    - **`TestP310_DowngradeRewritesV2ToV1`.**
      - A `downgrade` sweep turns a v2 row into `enc:v1:`.
      - An unbound legacy store (what a pre-TASK-009 binary does) reads it back correctly.
13. **Standalone configuration** (`service/cmd/spine`, `t.Setenv`).
    - `SPINE_SECRET_PREVIOUS_KEYS` with two valid keys yields a three-key ring whose
      `KeyIDs()[0]` is the primary's id.
    - A malformed entry fails with an error naming its index. The error text contains no
      base64 key material (asserted).
    - `SPINE_SECRET_PREVIOUS_KEYS` without a primary fails.
    - `SPINE_SECRET_ENVELOPE_MODE=Strict` is accepted; `bogus` fails.
    - The production gate without a key still fails.
14. **Standalone wiring.**
    - **Unit** (`service/internal/workspace`, in-memory querier):
      - `PrepareSubscriptionSecrets` with a cipher converts a plaintext row to v2 bound to the
        given workspace id;
      - with a nil cipher it sets no binding, writes nothing, and the row stays plaintext and
        readable (the dev posture is unchanged);
      - a sweep `Query` error is returned and logged, not panicked;
      - *(design check)* with a querier whose `Exec` blocks until its context is done, the helper
        returns an error wrapping `context.DeadlineExceeded` within `subscriptionSweepTimeout` + 1 s,
        and the store remains bound and readable. *Mutation check:* removing the timeout makes this
        test hit the `go test` timeout;
      - *(design check)* each converted row produces one `Info` record. The record carries
        `subscription_id`, `from` and `from_key_id`, and has no value field.
    - ***Integration*** (`service/cmd/spine`) — `buildStore` with the file resolver,
      `SPINE_WORKSPACE_ID=ws-int` and a key:
      - a pre-seeded plaintext row is `enc:v2` after `buildStore` returns;
      - opening it with workspace `ws-int` succeeds, and opening it with `default` fails.
    - ***Integration*** (`service/internal/workspace`) — `buildServiceSet` with a DB-backed
      `Config` and a cipher:
      - the same row conversion happens before the builder hook runs. A builder that reads the
        raw column observes `enc:v2`.
      - *Mutation check:* removing either `PrepareSubscriptionSecrets` call fails its integration
        test.
15. **No secret material leaks** (I6).
    - A sweep over a fixture with refused rows runs with a captured `slog` handler.
    - Neither the captured output, nor `SecretSweepReport`, nor any returned error string,
      contains any fixture plaintext or stored value.
    - Same for the `GetSubscription` error of a refused row.
16. **Documentation.** Every item in §5 is done:
    - `docs/integration-guide.md:108`'s "no data migration is required" claim is gone;
    - the §6 runbooks are present;
    - `README.md:233` reads `core/crypto`;
    - `CHANGELOG.md` has a Security entry naming P3-10 and P3-1;
    - the comments at `postgres_subscriptions.go:30-33` and `:41-43` are corrected;
    - *(design check)* the three statements of the §5 design-check note are present:
      - the CHANGELOG and integration guide both say that P3-10 is closed only after `strict` or
        the retirement of every key that sealed v1;
      - the one-workspace-per-runtime-database assumption, with what happens when it is broken;
      - a distinct key per deployment, or else a distinct `SPINE_WORKSPACE_ID`;
    - *(design check)* the §6 runbook amendments are present.
17. **No schema change.** `git diff --stat $(git merge-base origin/main HEAD)..HEAD -- migrations/` is
    empty on this task's branch. *(Design-check fix: the original form, `61994fe..HEAD`, would also
    count migrations that other INIT-023 tasks merge first.)*
18. **SMP compatibility gate.** Run at SMP `80995559` with `replace ../spine` at this task's
    branch head; nothing is committed to SMP. Record each of these in the Implementation Notes:
    - `go build ./cmd/... ./internal/...` passes.
    - `go vet ./cmd/customer-app/ ./internal/customer/eventdelivery/ ./internal/customer/workspace/ ./internal/customer/stepevents/`
      passes.
    - `go test ./internal/customer/eventdelivery/... ./internal/customer/workspace/... ./internal/customer/stepevents/... ./cmd/customer-app/...`
      passes **with `TEST_PROVISIONING_DSN` set**, so that the DB-backed tests run rather than
      skip. These include `TestWorker_Integration_DeliversEncryptedSubscription`,
      `TestWorker_Integration_RequiresCipherToDecrypt` and `TestCreateInitialActors_Integration_EncryptsSigningSecretAtRest`
      (`create_initial_actors_test.go:511-581`), which together cover:
      - the provisioning seed writing `enc:v1`;
      - the worker decrypting existing `enc:v1` and plaintext rows;
      - the realign path.
19. **Full Spine gates.**
    - `go test ./...`, `go vet ./...`, `make lint` (golangci-lint incl. gosec) and
      `make lint-security` pass.
    - `make test-integration` passes locally against the test DB (recorded).
20. **Fail-first evidence and PoC parity** (recorded in the Implementation Notes).
    - **Committed ports.** They do not compile against `61994fe`, which is expected for an
      additive API. Their behaviour is shown failing on `61994fe` semantics by these mutations:
      - "seal `enc:v1` under a binding" (AC7);
      - "no-op sweep" (AC10).

      Run the remaining mutations listed in AC2–AC14 and AC21–AC22 too, and record each result.
      *(Design check: AC21 and AC22 added.)*
    - **The original audit PoCs, re-run unchanged** with `go test -overlay` against the merge
      commit:
      - `cipher_transplant` and gap1's two P3-10 tests **still pass**, because they exercise the
        intentionally retained AAD-less `Encrypt`/`Decrypt` API. Their committed ports exercise
        the v2 path (AC3, AC7) that the store now uses.
      - The r21 PoC **still passes**, because it never runs the sweep. Its port does (AC10).
      - *(Design-check addition)* the three store-level PoCs in
        `poc/gap1-crypto-secrets-reaud/delivery_store_poc_test.go` **still pass**, because they use
        unbound stores: `TestPoC_StoreLevelCiphertextSwap` (P3-10),
        `TestPoC_LegacyPlaintextRowNeverReencrypted` (P3-1) and
        `TestPoC_NilCipherPersistsSigningSecretPlaintext` (P3-1, SMP's half).
        - Their bound counterparts are AC7, AC10 and SMP TASK-006 respectively.
        - The design check re-ran all six PoCs on 2026-10-06 against the prototype overlay. All pass,
          as predicted.

      These outcomes are expected and must be recorded with this explanation, so that a reviewer
      does not read them as "fix ineffective".

    **Design-phase evidence (2026-10-06).** A prototype of §1–§3 and drafts of the AC3–AC12 tests
    exist under the SMP checkout's
    `tmp/init027-orchestration/spine-init023/_work/task009/` (`proto/`, `overlay-proto.json`,
    `mut/`). Results:
    - all draft tests passed;
    - each of these mutations failed exactly the intended tests:
      - AAD without the workspace;
      - strict check removed;
      - "current" check removed;
      - `Decrypt` v2 pass-through;
      - v1-only sealing;
      - no-op sweep;
    - the full Spine `go test ./...` passed with the prototype overlay;
    - SMP built and vetted against it.

    These files are a starting point, not a mandate.

21. **Nested envelopes (I8; design check, Decision 14).** Committed ports of probes X1 and X5 from
    `_work/task009-check/adversarial_test.go`. The setup uses a bound `migrate` store. Each test
    sets the row to `enc:v1(<its own enc:v2 value>)`, which is what a `61994fe` binary writes back.
    - **`TestNested_OwnRowV2Repaired`.**
      - `GetSubscription` returns the original secret.
      - A sweep reports `Rewritten==1` and `Repaired==1`.
      - The raw column is then a single-level `enc:v2:<primary kid>:` value.
      - A second sweep writes nothing.
    - **`TestNested_ForeignV2Refused`.** The inner value is workspace A's v2, and the store is bound
      to B.
      - `GetSubscription` fails with `ErrNestedEnvelope`.
      - The sweep lists the row in `Refused` and leaves its bytes unchanged.
    - **`TestNested_NeverReturnsEnvelope`.** The test covers every mode and every fixture in AC6–AC12
      and AC21. No bound read returns a `SigningSecret` for which `ClassifyEnvelope` reports anything
      other than `EnvelopePlaintext` or `EnvelopeEmpty`. No sweep writes a value whose opened
      plaintext is an envelope.
    - **`strict` mode.** The same nested row (an outer `enc:v1`) is refused with
      `ErrLegacySecretRefused`.
    - *Mutation checks:*
      - removing the nested check makes `TestNested_NeverReturnsEnvelope` fail. That is the
        prototype's behaviour, probe X5.
      - unwrapping without the AAD check makes `TestNested_ForeignV2Refused` fail.
22. **Per-row conversion record (design check, Decision 15).**
    - **`TestSweep_ReportsEachConversion`.** The fixture has a plaintext row, a v1 row under the
      primary, a v1 row under a previous key, a v2 row under a previous key, and a current row.
      - `Converted` lists exactly the four rewritten ids, with the right `From` and `FromKeyID`
        (`""` for plaintext).
      - `len(Converted) == Rewritten`.
      - The current row is absent.
    - **The X2 trust boundary is pinned as documented behaviour.** A v1 value sealed for another
      workspace is planted in a `migrate` store. The sweep converts it, and its conversion appears in
      `Converted` with `From == EnvelopeV1`.
      - A test comment names the §3 design-check note, so that a future reader does not mistake
        this for an oversight.
    - AC15's leak check also covers `Converted` and the per-row log records.
23. **`spine secrets reencrypt` (design check; the default answer to Owner question 2).**
    - ***Integration*** (`service/cmd/spine`): a registry with two workspaces on two databases.
      - Workspace 1 holds plaintext and v1 rows.
      - Workspace 2 holds one row that is an `enc:v2` value sealed for workspace 1, a transplant.
      - Running `reencrypt --all-workspaces` with a key:
        - converts workspace 1, and its output names workspace 1 as clean;
        - reports workspace 2's row as refused and leaves it untouched;
        - exits non-zero.
      - A second run after the bad row is deleted exits 0.
      - The output contains no secret or stored value.
    - **Unit:** without `--all-workspaces` and without a key, the command exits non-zero with a
      message naming `SPINE_SECRET_ENCRYPTION_KEY`, and touches nothing.
    - If the owner descopes this command, delete this AC and keep the doc requirement from the
      Owner question 2 design-check note.

## Out of scope / follow-ups

- **SMP adoption (SMP TASK-006).** It covers:
  - binding at the three store sites;
  - the ring and the worker-side sweep;
  - an all-workspaces sweep command;
  - encrypting by default (P3-1.smp);
  - the `strict` decision;
  - escrow of retired keys (ADR-035);
  - the `.spine-revision` bump.

  SMP TASK-012 makes the final INIT-023 bump.
- **Webhook signature v2 (P3-11).** This is TASK-010. It is independent of the at-rest envelope.
- **`spine secrets reencrypt [--all-workspaces]`** for standalone shared mode (Owner question 2).
  *Design-check note (2026-10-06): moved in scope by default (AC23). It stays here only if the owner
  descopes it.*
- **Strict by default** in a later standalone release (Owner question 1).
- **Per-row tolerance in list paths.** A single unopenable row fails
  `ListActiveSubscriptionsByEventType`, and with it the standalone fan-out for the whole workspace.
  The lister needs only ids and event types (`adapters/delivery/subscription_store.go:22-34`). A
  non-decrypting listing method would narrow the blast radius, but it changes the
  `SubscriptionStore` interface (five test fakes). SMP's emitter already reads only ids.
- **Weak-key guard.** Refusing all-identical-byte keys in `loadSecretCipher` when
  `SPINE_ENV=production`. This is cheap, but it is not an audit finding, and `ParseEncryptionKey`
  must stay permissive for SMP's tests (Decision 11).
- **External KMS key wrapping.** Not needed for the findings. The `kid` leaves room for it.
- **Other at-rest secrets.** There are none in Spine today. `AssociatedData(purpose, …)` is ready
  for them.
- *(Design check)* **A diagnostic for shared databases.** When a refused v2 row's `workspace_id`
  column names a different non-empty workspace, the sweep's `Reason` could say "database may be
  shared between workspaces".
  - It would be diagnostic only: the column is never used to open a value.
  - It is optional. The docs statement in AC16 is the requirement.

## Design check

**Adversarial check of 2026-10-06.** It checked the design text against Spine `61994fe`, SMP
`80995559`, the audit report §3 (P3-1, P3-10) and the PoCs. It also probed the design's own
prototype (`_work/task009/`) with new tests in `_work/task009-check/`:

- `adversarial_test.go`, run with `overlay-check.json`;
- `zero_test.go`, run with `overlay-zero.json`;
- the original PoCs, run with `overlay-pocs.json`.

**Verdict.** The core construction is sound and does close P3-10's v2 path:

- an HKDF subkey;
- the `kid` and the version label authenticated in the GCM AAD;
- the workspace taken from trusted configuration, not the row;
- `subscription_id` in the AAD;
- a compare-and-swap sweep.

Spot checks of 40+ `file:line` references in both repositories all held, with one exception (the
AC1 build path). The things that still had to change are below; each is amended in place.

| # | Finding (probe) | Change |
|---|---|---|
| X4 | **Write-path laundering.** In `migrate` mode, the §2 rule "open with this row's AAD, then re-seal" accepted a *foreign* `enc:v1` input and stored it as this row's v2. That contradicted I4. Not reachable through any current API (no caller passes envelopes), but it is a live sink for any embedder. | §2 note, I4 note, Decision 13, AC9 rewritten. |
| X1, X5 | **Nested envelope after a rollback without `downgrade`.** A `61994fe` binary's write-back wraps v2 as `enc:v1(enc:v2:…)`. The prototype returned the literal `enc:v2:…` string as the HMAC key, and its sweep re-sealed it. Every signature was then silently wrong, and SMP's ensure loop would wedge. | New I8, §2 read-path rule, §3 note, Decision 14, AC21. |
| X2 | **`migrate` mode does not close P3-10 for the audit's own attacker.** That attacker holds a pre-upgrade backup, which is a v1 copy. Every sweep in `migrate` launders a v1 plant into a valid v2, and `strict` later accepts it. The design called this a "trust boundary" but did not show that it persists or that the attacker in the report is exactly this one. | §3 note (persistence and a per-row `Converted` record), §5 statement 1, §6 runbook, Decision 15, Owner question 1 note, AC16, AC22. |
| X3 | **Two workspaces on one runtime database** (the registry has no unique `database_url`) refuse each other's rows once bound. This is intended fail-closed behaviour, but it was undocumented. Also, file mode's `"default"` id makes a key shared across deployments transplantable. | §5 statements 2 and 3, AC16. |
| — | **A row lock can stall the sweep without limit**, and with it workspace initialisation (`initializeEntry` under `p.ctx`). | §4 note, AC14. |
| — | **Shared-mode `strict` and key drop could not be verified.** A premature flip stops all webhook fan-out for idle workspaces. | Owner question 2 default changed to *include*; §4 note, AC23. |
| — | **The zero-value claim did not hold.** The prototype panics in `Encrypt`, `Decrypt`, `Seal` and `PrimaryKeyID`. It also keeps the raw key bytes, which the §1 table says are not kept. | §1 note, AC1. |
| — | **AC fixes.** AC1's smoke-test path was wrong. AC17's diff base would count other tasks' migrations. AC11 lacked a lock-wait variant. AC20 omitted the three gap1 store-level PoCs, which the check re-ran and confirmed still pass. | AC1, AC11, AC17, AC20. |
| — | **SMP handoff.** Equal `SPINE_SECRET_ENCRYPTION_KEY` and `SECRET_ENCRYPTION_KEY` (both are all-zero in SMP TASK-006's dev compose) let legacy platform-credential ciphertext be re-framed as `enc:v1`. That includes a workspace's *receiver* key, and it opens in `migrate`. Code-traced. | §8 note, items 1–3 for SMP TASK-006. |

**Checked and confirmed, no change:**

- The prototype passes its own tests.
- SMP builds and vets against the overlay, and its affected unit tests pass.
- No test in either repository matches the old crypto error strings.
- SMP never calls `IsEncrypted`.
- Spine's subscription secrets have exactly one sink, `GetSubscription` → `StoreSubscriptionResolver`
  → dispatcher. No transactional store path touches subscriptions.
- An unknown `kid`, a malformed `kid` and an `enc:v3` value each fail closed (probe X6).

## Implementation Notes

*(Implementation, 2026-10-07. Branch `init/INIT-023-TASK-009-secret-envelope-v2`, cut from Spine
local `main` `ee3c249` (`61994fe` + the INIT-023 scaffold + merged TASK-001). Diff base
`git diff main...HEAD`. macOS, go 1.26.5, golangci-lint 2.11.4. Spine integration and scenario
runs used the shared PostgreSQL 17.10 (`s009_*` databases); the SMP DB suites ran in a disposable
`pgvector/pgvector:pg18` container (PG 18.4, CI's image), removed afterwards.)*

### What was built

| Commit | Change |
|---|---|
| `c3e43a6` | `core/crypto`: `SecretCipher` becomes a key ring in place (`NewSecretCipherRing`, `KeyID`, `PrimaryKeyID`, `KeyIDs`; raw key bytes not retained; zero value returns errors). New `envelope.go`: `enc:v2` (`Seal`/`Open`, `AssociatedData`, `Opened`, `Envelope`/`ClassifyEnvelope`, the six sentinels). `Decrypt` opens v1 with every ring key and refuses v2; `IsEncrypted` covers v2. Tests: `envelope_test.go`, `envelope_internal_test.go`. |
| `38bbe03` | `adapters/store`: `postgres_subscription_secrets.go` (binding, modes, `ParseSecretEnvelopeMode`, bound read/write paths with the §2 design-check rules, nested-envelope repair, the CAS sweep with `Converted`/`Repaired`); `secretBinding` field; read/write paths routed through it; stale comments fixed. Tests: `subscription_secrets_test.go` (in-memory), `postgres_subscription_secrets_integration_test.go` (real Postgres CAS). |
| `fab0abb` | `adapters/delivery/p31_sweep_test.go`: port of the r2-1 PoC (test-only; no `adapters/delivery` production file touched). |
| `6c4f3f5`, `ad2fd0d`, `8406f1b` | Hygiene only: `gofmt` of the unsorted import blocks of `pool.go`, `serve_resolver.go`, `serve_builders.go`, `main.go`, `gateway/handlers_subscriptions.go` (unformatted on `main`), split out so the functional diffs stay minimal and the changed-files `gofmt -l` gate is empty. |
| `912b51a` | Standalone wiring: `SPINE_SECRET_PREVIOUS_KEYS`, `SPINE_SECRET_ENVELOPE_MODE`, the `secret envelope configured` log, `workspace.PrepareSubscriptionSecrets` (bind + sweep under a 10 s limit + per-row logs + strict hint), `buildStore` and `buildServiceSet` call sites, `PoolConfig.SecretEnvelopeMode`, and `spine secrets reencrypt [--all-workspaces]` (`cmd_secrets.go`). Tests: `secret_envelope_config_test.go`, `subscription_secrets_test.go` (workspace), `secrets_integration_test.go`, `subscription_secrets_integration_test.go`. |
| `fc1c856` | Docs: integration guide §2.1 rewrite + env table, `.env.example`, README (variables, CLI, `core/crypto`), security-model §5.3, CHANGELOG Security entry, KNOWN-LIMITATIONS. |
| `dfd85cc` | codex round 1 fixes (see *Review*): inactive workspaces swept (`DBProvider.ResolveAnyStatus`); `PostgresStore.ReplaceSubscriptionSecret` and a `rotate-secret` handler that does not read the refused value. |
| `0c9925c` | `DBProvider.ResolveAnyStatus` registry test. |

No migration, column or table (AC17). `go.mod`/`go.sum` unchanged; `core/crypto` stays stdlib-only.

### Per-AC evidence

1. **API and compatibility.** Every §1 identifier exists with the stated signature; `NewSecretCipher`,
   `Encrypt`, `Decrypt`, `ParseEncryptionKey`, `EncryptionKeySize`, `SetSecretCipher`,
   `NewPostgresStoreWithQuerier`, `SubscriptionStore` and `BootstrapInternalSubscription` are
   unchanged (SMP builds and vets unmodified, below). `core/crypto` imports only the standard
   library (`crypto/hkdf`, `crypto/subtle`, …). `./dev/lint-boundaries.sh` → `lint-boundaries: OK`;
   embed-smoketest passes. `TestSecretCipher_ZeroValueNeverPanics`: no panic and an error from
   `Encrypt`, `Decrypt` (v1, v2), `Seal`, `Open` (v1, v2); `PrimaryKeyID()==""`, `KeyIDs()==nil`.
   Key bytes: the ring keeps only the derived AEADs and the id; the duplicate check compares the
   input slices during `NewSecretCipherRing` (`subtle.ConstantTimeCompare`) and keeps nothing.
2. **Golden vector.** `TestV2_GoldenVector` opens a committed envelope that was computed
   **independently** with Python `cryptography` (HKDF-SHA256 + AESGCM over the documented labels),
   checks `KeyID(00..1f) == ada7649825e43683`, the `AssociatedData` byte encoding and the documented
   regex. `TestV2_GoldenVectorSeal` (internal) reproduces the envelope byte for byte from the fixed
   nonce. Mutations: each HKDF info string and the GCM-AAD label → both golden tests FAIL.
3. **P3-10 crypto ports.** `TestV2_TransplantAcrossWorkspacesFails` (port of
   `cipher_transplant_test.go`), `TestV2_SwapAcrossSubscriptionsFails` (port of
   `TestPoC_CiphertextSwapAcrossRowsDecrypts`), `TestAssociatedData_Canonical` (four ambiguity
   pairs, multi-byte uvarint). Mutation "`v2AAD` ignores the caller AAD" → both ports FAIL.
4. **AAD mandatory; legacy API strict about v2.** `TestV2_RequiresAAD` (`Seal(nil)`, `Seal([]byte{})`,
   `Open(v2,nil)` → `ErrMissingAAD`; `Decrypt(v2)` errors wrapping `ErrMissingAAD`; nil cipher →
   `ErrNoCipher`). `TestLegacy_EncryptStillV1AndDecryptPassesThrough`, `TestClassifyEnvelope`
   (incl. `enc:v10:`, `ENC:v2:`, ` enc:v2:`), `TestOpen_FormatTable` (13 rows incl. `enc:v9` →
   `ErrUnsupportedEnvelope`, short/uppercase kid, non-canonical base64, no value echo in errors).
   Mutation "Decrypt v2 pass-through" → `TestV2_RequiresAAD` FAILS.
5. **Key ring.** `TestV2_KeyRing` (K1 v1 and v2 open under `(K2,K1)` with `Primary==false` and
   `KeyID==kid(K1)`; `Seal` embeds the primary kid; `(K2)` alone → `ErrUnknownKeyID` / `ErrEnvelopeAuth`;
   duplicates de-duplicated), `TestV2_KeyIDRelabelFails`, `TestNewSecretCipherRing_Errors`
   (positions named, no key bytes), `TestNewSecretCipherRing_RefusesKeyIDCollision` (unexported
   `keyIDFunc` hook; identical keys are not a collision), `TestV2_NonceUniqueness` (20 000).
6. **Binding and legacy mode.** `TestSetSubscriptionSecretBinding_Validation`,
   `TestParseSecretEnvelopeMode`, `TestLegacyMode_Unchanged` (unbound: `enc:v1` writes, v1 + plaintext
   list, envelope pass-through, sweep → `ErrNoSecretBinding` with zero Execs),
   `TestBoundStore_NilCipherFailsClosed`. *Integration:* `TestEventSubscription_SigningSecretEncryption`
   passes unmodified (on a fresh database — see *Pre-existing*).
7. **P3-10 store level.** `TestP310_CrossWorkspaceTransplantRefused` (two in-memory DBs, one key,
   B's `workspace_id` column rewritten to `ws-A`): Get/List/ListActive fail with `ErrEnvelopeAuth`; the
   sweep refuses and leaves the bytes. Mutations "AAD binds `""`" and "seal `enc:v1` under a binding
   (61994fe)" → FAIL.
8. **Strict.** `TestP310_StrictRefusesLegacyEnvelopes`; mutation "strict check removed" → FAIL.
9. **Envelope inputs refused.** `TestBoundWrite_RefusesEnvelopeInput` (3 modes × {foreign v1, own-row
   v2, `enc:v9:x`} × {Create, Update}; plaintext sealed per mode; `""` stored as `""`). Mutation "open
   with this row's AAD, then re-seal" (probe X4) → FAIL.
10. **P3-1 sweep.** `TestP31_SweepEncryptsPlaintextAndV1` (Rewritten 2, Empty 1, Clean; second sweep
    Current 2 with zero Execs) and `TestP31_KeyAddedLater_SweepEncryptsBootstrapRow` (delivery; also
    asserts the keyed bootstrap alone writes nothing, the dump no longer forges, the bound bootstrap
    issues no UPDATE, the resolver hands the token to the signer). Mutations "no-op sweep (61994fe)"
    → both FAIL; "already-current check removed" → `TestP31_SweepEncryptsPlaintextAndV1` FAILS.
11. **Compare-and-swap.** Unit `TestP31_SweepLosesToConcurrentWrite`; *integration*
    `TestSweepCAS_ConcurrentCommitWins` and `TestSweepCAS_LockWaitThenConcurrentCommitWins` (the second
    connection commits only once `pg_stat_activity` shows the sweep's UPDATE waiting on a `Lock`).
    Mutation "drop `AND signing_secret = $3`" (and its argument) → both integration tests FAIL. Also
    `TestSweep_RealPostgresConvertsAndIsIdempotent` (and `updated_at` not bumped).
12. **Rotation and downgrade.** `TestP310_RotationRekeysToPrimary` (Rewritten 2, Rekeyed 1, FromV1 1;
    strict `(K2)` reads both), `TestP310_DowngradeRewritesV2ToV1` (ToV1 1; an unbound legacy store
    reads it; second downgrade sweep Current 1).
13. **Configuration.** `TestLoadSecretCipher_PreviousKeysBuildRing` (3-key ring, `KeyIDs()[0]` is the
    primary, whitespace and empty entries ignored), `TestLoadSecretCipher_MalformedPreviousKeyNamesIndexNotValue`
    (index named, no key material), `TestLoadSecretCipher_PreviousKeysRequirePrimary`,
    `TestLoadSecretEnvelopeMode` (`Strict` ok, `bogus` fails naming the variable),
    `TestLoadSecretCipher_ProductionGateUnchanged`.
14. **Wiring.** Unit (`service/internal/workspace`): `TestPrepareSubscriptionSecrets_ConvertsAndBinds`,
    `_NilCipherIsNoOp`, `_QueryErrorReturnedAndLogged`, `_TimesOutOnBlockedWrite` (Exec blocks until
    ctx done → `context.DeadlineExceeded` within the limit + 1 s; store bound and readable),
    `_LogsEachConversionWithoutSecrets` (one `Info` record per conversion with exactly
    `subscription_id`/`from`/`from_key_id`/`repaired`), `_BindingErrorIsTyped`, `_ProductionHintIsWarn`,
    `TestSubscriptionSweepTimeout_Default` (10 s). *Integration:* `TestBuildStore_FileResolverBindsAndSweeps`
    (`SPINE_WORKSPACE_ID=ws-int`: row is v2 after `buildStore`, opens for `ws-int`, not for `default`)
    and `TestBuildServiceSet_SweepsBeforeBuilderHook` (the builder hook observes `enc:v2`; a bad mode
    fails the build). Mutations: removing either `PrepareSubscriptionSecrets` call → its integration
    test FAILS; removing the timeout → the blocked-write test hits the `go test` timeout.
15. **No secret material.** `TestSweep_NoSecretMaterialInReportOrErrors` (22 fixtures in strict:
    report + every `GetSubscription` error), the workspace log test above (captured JSON `slog`), and
    the AC23 integration test (command output + captured default logger) contain no fixture plaintext,
    stored payload or key.
16. **Docs.** Integration guide §2.1 rewritten (wire formats and binding, ring and kid, both
    variables, modes table, sweep + trust boundary, *When P3-10 Is Closed*, *Deployment Assumptions*
    with the one-database-per-workspace and distinct-key/`SPINE_WORKSPACE_ID` statements, all §6
    runbooks with the design-check amendments); the "no data migration is required / re-encrypted on
    next update" text is gone; env table gains both variables. `.env.example`, README (`:51` variables,
    `core/crypto` row, CLI), security-model §5.3 bullet, CHANGELOG Security entry naming P3-10 and P3-1
    and stating that `migrate` does not close P3-10, comments at `postgres_subscriptions.go`
    corrected. KNOWN-LIMITATIONS gains two Security Notes.
17. **No schema change.** `git diff --stat $(git merge-base origin/main HEAD)..HEAD -- migrations/`
    (merge base `61994fe`) is empty.
18. **SMP gate** — see below.
19. **Spine gates** — see below.
20. **Fail-first and PoC parity.** The committed ports do not compile against `61994fe` (additive
    API); their behaviour on `61994fe` semantics is shown by the mutations above, all run with
    `go test -overlay` (no source reverted): 18 mutations, every one FAILS exactly the intended
    test(s) (table in *Gates*). The original audit PoCs, overlaid unchanged on this branch, **still
    pass**, as the design predicted, because they exercise the deliberately retained AAD-less /
    unbound API: `TestPoC_CiphertextTransplantAcrossWorkspaces`; gap1 `TestPoC_CiphertextSwapAcrossRowsDecrypts`,
    `TestPoC_PlaintextAcceptedWithCipherConfigured` (also `_AllZeroKeyAccepted`, `_NonceUniqueness`);
    gap1 store-level `TestPoC_StoreLevelCiphertextSwap`, `TestPoC_LegacyPlaintextRowNeverReencrypted`,
    `TestPoC_NilCipherPersistsSigningSecretPlaintext`; r2-1 `TestR21_DumpedColumnForgesDispatcherSignature`,
    `TestR21_KeyAddedLaterLeavesPlaintext`. Their bound counterparts are AC3/AC7 (v2 path), AC10 (sweep)
    and SMP TASK-006 respectively. This is not "fix ineffective".
21. **Nested envelopes.** `TestNested_OwnRowV2Repaired` (Get returns the secret; sweep Rewritten 1,
    Repaired 1, single-level v2 under the primary; second sweep writes nothing),
    `TestNested_ForeignV2Refused`, `TestNested_StrictRefusesOuterV1`, `TestNested_NeverReturnsEnvelope`
    (3 modes × 22 fixtures = 66 subtests: no bound read returns an envelope; no sweep writes a value
    whose plaintext is an envelope). Mutations: nested check removed → `TestNested_NeverReturnsEnvelope`
    FAILS; unwrap without the AAD check → `TestNested_ForeignV2Refused` FAILS.
22. **Per-row record.** `TestSweep_ReportsEachConversion` (exactly the four rewritten ids with the right
    `From`/`FromKeyID`, `len(Converted)==Rewritten`, current row absent, breakdown and Scanned
    invariant); `TestSweep_X2TrustBoundaryPinned` (a foreign `enc:v1` plant in migrate is converted and
    recorded with `From==EnvelopeV1`; strict then reads it) — its comment names the §3 design-check
    note.
23. **`spine secrets reencrypt`.** *Integration* `TestSecretsReencrypt_AllWorkspaces`: registry +
    three workspace databases (ws-one plaintext + v1; ws-two a v2 transplant sealed for ws-one; ws-three
    **inactive** with a v1 row): exit non-zero naming only ws-two, `workspace ws-one: clean …
    rewritten=2`, `workspace ws-two: refused subscription sub-t envelope=enc:v2 key_id=…`, ws-three
    swept, transplant bytes unchanged; after deleting the bad row the second run exits 0 (`current=2`,
    `summary: workspaces=3 clean=3`); no secret in output or logs. Unit
    `TestSecretsReencrypt_RequiresKeyAndTouchesNothing` (no key → error naming
    `SPINE_SECRET_ENCRYPTION_KEY`, before any database) and `_SharedResolverNeedsAllWorkspaces`.

### Deviations and additions (with reasons)

- **`ErrSubscriptionSecretBinding` (new, `service/internal/workspace`).** The helper returns both
  binding and sweep errors; callers must fail on the first and tolerate the second (§4). A typed
  sentinel lets `buildStore` / `buildServiceSet` tell them apart.
- **`subscriptionSweepTimeout` is a package variable, not a constant** (still 10 s, pinned by
  `TestSubscriptionSweepTimeout_Default`), so the timeout test runs in 0.3 s instead of 10 s.
- **`SecretSweepConversion.Repaired bool`** added beside the three designed fields, so a repaired
  nested row is visible in the per-row log; it carries no secret material.
- **`SubscriptionSecretBinding()` getter** on `PostgresStore` (tests and embedders can verify a store
  is bound) and **`ParseSecretEnvelopeMode`** (shared by `spine serve` and the command; SMP TASK-006
  can reuse it).
- **A bound store with a nil cipher also refuses non-empty reads** (`ErrSecretCipherRequired`), not
  only writes: a binding means "sealed", and a plaintext read through a key-less bound store would
  silently bypass strict. The helper never binds without a cipher, so standalone never hits it.
- **`Open` accepts only canonical base64 for `enc:v2` and a lowercase-hex kid** (`ErrMalformedEnvelope`
  otherwise), so one sealed value has one stored spelling; v1 parsing is unchanged.
- **Sweep counters are by outer envelope**; a repaired row whose outer envelope was already the
  target under the primary key counts only in `Repaired` (documented on the type).
- **codex round 1 (accepted):** (P1) `--all-workspaces` now sweeps **inactive** workspaces too, via
  new `DBProvider.ResolveAnyStatus` (same ref validation and secret dereference as `Resolve`, any
  status, no cache) — otherwise a clean run could license dropping a key an inactive workspace still
  needs. (P2) `rotate-secret` read the row first and so could not repair a refused row, which the
  docs name as the fix; new `PostgresStore.ReplaceSubscriptionSecret` replaces the secret without
  opening the stored value and the gateway handler uses it when the store supports it (fallback kept;
  response unchanged). `PATCH`/`pause`/`activate` still read first and fail on a refused row until it
  is rotated (documented). These touch `service/internal/gateway` and `db_provider.go`, outside §9's
  file list; both edits are additive.
- **Hygiene commits** sort the import blocks of five files that were not `gofmt`-clean on `main`.

### Gates (all at the branch head unless noted)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | exit 0 |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` (no DB env) | exit 0, 38 packages ok, no FAIL |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all exit 0, no output |
| `go test -race -p 4 -count=1` on `core/crypto`, `adapters/store`, `adapters/delivery`, `service/cmd/spine`, `service/internal/workspace`, `service/internal/gateway` | all ok |
| `go test -tags integration -count=1` per touched package, each against its own fresh database (`s009_store`, `s009_dlv`, `s009_cmd`, `s009_ws` + `SPINE_REGISTRY_TEST_DATABASE_URL=s009_reg` migrated with `spine migrate`) | delivery, cmd/spine, workspace, crypto, gateway: ok. store: 58 PASS, 1 FAIL `TestRunModeDatabaseDefault` — **pre-existing**, fails identically on a `main` snapshot (`git archive main`) against a fresh database (`cannot scan NULL into *string`, `workflow_version_label`). Every TASK-009 integration test passes. |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' ./service/scenariotest/scenarios/` (the CI job) | 19 `TestGitHTTP_*` PASS, 0 skips |
| `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` (full suite, branch vs `main` snapshot, fresh `s009_scen` each) | 30 failures on both; the failing-test sets are **identical** (`diff` empty). CI notes the rest of the suite is not green on `main`; none of the touched packages has scenario tests. |
| `golangci-lint run` (and `--build-tags integration`) on the six touched package trees, branch vs `main` snapshot, line numbers stripped | 137 vs 143 (139 vs 145 tagged); **0 new issues** (the 6 fewer are the import-sort hygiene). |
| `golangci-lint run --enable-only=gosec ./...` (`make lint-security`) | 1 issue, identical on `main` (`core/workflow/service.go` G703); 0 new. One reasoned `//nolint:gosec` (G101 on the AAD purpose label `"runtime.event_subscriptions.signing_secret"`). G115 on `uint64(len(s))` is not raised, so no suppression was added there. |
| `gofmt -l` on every `.go` file in `git diff --name-only main...HEAD` | empty |
| AC17 `git diff --stat $(git merge-base origin/main HEAD)..HEAD -- migrations/` | empty |
| Mutations (`go test -overlay`, script in the scratchpad) | AC2 ×3, AC3, AC4, AC5, AC7 ×2, AC8, AC9, AC10 ×2, AC11, AC14 ×3, AC21 ×2: every one FAILS its intended test(s) (AC14 timeout: `go test` timeout) |
| Original audit PoCs overlaid unchanged (`go test -overlay`) | all 10 PoC test functions PASS (expected; see AC20). *(Review round 1: this row said 11; there are 10 functions. Re-run at the round-1 head: 10 PASS.)* |

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T009/smp` at SMP `main` `7d4e7cc9`, `replace ../spine`
→ this worktree (`go list -m` confirms), unmodified (`git status` clean).

- `go build ./...` exit 0; `go vet ./...` exit 0; `go vet -tags integration ./...` exit 0.
- SMP packages importing a changed Spine package (`core/crypto`, `adapters/store`, `adapters/delivery`,
  found with `go list -f` over Imports/TestImports/XTestImports): `cmd/customer-app`,
  `internal/customer/{eventdelivery,projectionwriter,spinecore,stepevents,workspace}`.
  `go test -p 4 -count=1` on all six: ok.
- DB suites (PG 18.4 container provisioned exactly like SMP CI's `integration` job: roles `platform`,
  `platform_audit_retention`, `smp_prov` NOSUPERUSER CREATEROLE CREATEDB; `smp_it` + `vector`,
  `spine_runtime_it`; `go run ./dev/apply-migrations -set platform` / `-set spine-runtime`):
  - `go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -allow-skip 'TEST_MINIO' -- -race -count=1 -p 4 ./internal/customer/{eventdelivery,stepevents,workspace,projectionwriter}/`
    with `TEST_DATABASE_URL`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` → **PASS**, 627 tests, 6
    allowed skips (MinIO and live AWS only: MinIO was not started to stay off the live dev stack).
  - `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race -count=1 -p 4 ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/`
    with `TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` → **PASS**, 1539 tests.
  - `go test -race -count=1 ./cmd/customer-app/...` with `TEST_PROVISIONING_DSN`, `TEST_DATABASE_URL` → ok.
  - AC18's named tests ran (not skipped) and PASS: `TestWorker_Integration_DeliversEncryptedSubscription`,
    `TestWorker_Integration_RequiresCipherToDecrypt`, `TestCreateInitialActors_Integration_EncryptsSigningSecretAtRest`
    (it lives in `internal/customer/workspace`, not `cmd/customer-app`).

### Review

- `codex review --base main`, round 1: two findings, both accepted and fixed with tests (`dfd85cc`,
  above). Round 2 (after the fixes): "No actionable regressions were identified."
- Round 3, at `0c9925c` (the final code): "No actionable regressions were identified." No finding
  was rejected.

### Review round 1 (2026-10-07)

Three reviewers (correctness + SMP compatibility, adversarial security, completeness/tests) reported
14 findings (11 distinct: 2≡13, 4≡10, 6≡14) and 8 AC-gap notes against `120e442`. Each was
verified against the code before it was dispositioned. Commits: `21f684e` (store), `be90990`
(command), `a47d7d9` (wiring tests), `74a01df` (docs), then three codex follow-ups — `f4baca4`
(credential-free database errors), `0ef0733` (kept-secret judged from the caller's read),
`e99027d` (two-rollout key rotation) — and this notes commit.

| # | Sev | Finding (verified) | Disposition |
|---|---|---|---|
| 7 | P2 | **Read-modify-write laundering bypassed the per-row record.** Confirmed: in `migrate`, `GetSubscription` opens a planted foreign `enc:v1` value and a bound `UpdateSubscription` re-sealed it as this row's `enc:v2` (gateway `PATCH`/`pause`/`activate`; also a bootstrap that only realigns the URL, and SMP's `ensureScopedTargetURL` once bound). The next sweep saw a current row, so `Converted` was empty. | **FIXED** (`21f684e`, option (a); refined in `0ef0733` after codex round 2). A bound `UpdateSubscription` writes `signing_secret` only when the caller *set* a secret; when it kept the one it read, only the other columns are written, so the sweep — which records every conversion — is the only path from a legacy value to `enc:v2`. "Kept" is judged from the caller's own read: a value returned by a bound read (`GetSubscription`, the list calls) remembers the secret that read returned in two unexported `EventSubscription` fields, and `SigningSecret` is compared (constant-time) with that, whatever the row holds now; only a caller-built value falls back to comparing with the stored value (one extra `SELECT`). `21f684e` compared with a fresh read of the row, which codex round 2 showed a concurrent `rotate-secret` defeats (the stale planted secret was re-sealed over the rotation). Changed secret, refused stored value and missing row behave as before; unbound reads record nothing and unbound stores keep the single historical `UPDATE` (SMP unaffected; every `EventSubscription` literal in both repos is keyed). Tests: `TestBoundUpdate_UnchangedSecretKeepsStoredBytes` (PATCH/pause/activate after a plant made post-clean-sweep — next sweep converts **and records** it; rotation between read and update; list-then-update; caller-built fallback; current row not rewritten; changed secret; refused row; missing row) and *integration* `TestBoundUpdate_RealPostgresKeepsUnchangedSecret` (including the race); `TestLegacyMode_Unchanged` asserts unbound reads record nothing. Mutations (`-overlay`): dropping the kept branch fails both ("a read-modify-write re-sealed the stored secret"); ignoring or not recording the caller's read fails both race cases. Option (b) (a `Warn` on every bound read that accepts a legacy value after a clean sweep) was **not** added: with (a) the "every conversion is logged" statement holds, and (b) needs per-store "swept clean" state plus logging on the dispatcher's per-delivery read path; a plant read before the next sweep is the documented `migrate` trust boundary and is recorded by that sweep. Doc statements corrected (`74a01df`). |
| 1 | P3 | One-replica runbook assumed no overlap; the guide's manifest uses the default `RollingUpdate`, and a 401 from the receiver is a permanent delivery failure (`retry.go` `isRetryable`). Confirmed. | **FIXED** (`74a01df`): the one-replica procedure requires no overlap (stop old first; Kubernetes `strategy: Recreate`), otherwise the downgrade-first procedure; CHANGELOG likewise; the example manifest sets `strategy: {type: Recreate}` with a comment; *Deployment Assumptions* states why. |
| 2, 13 | P3 | The audit rule "a `from=enc:v1` conversion after the first clean sweep was not written by this release" ignored `downgrade` (which writes and sweeps to `enc:v1`), older binaries and keyless periods. Confirmed (`sealTarget` → `Encrypt` in downgrade). | **FIXED** (`74a01df`): the log has a *baseline* — the first clean `migrate`/`strict` sweep after the last `downgrade` period, older binary or keyless period; each such period resets it; only conversions after the baseline are flagged. Applied in the guide (sweep section, upgrade step 3, several-replica step 2, rollback, workspace-id runbook), CHANGELOG and KNOWN-LIMITATIONS. This supersedes the §3 design-check sentence "logged after a workspace's first clean sweep was not written by this release". |
| 3 | P3 | `--all-workspaces` did not check `WORKSPACE_RESOLVER`. Confirmed. | **FIXED** (`be90990`): refused unless `WORKSPACE_RESOLVER=db` (unset/`file`/other → error naming `WORKSPACE_RESOLVER=db`; `platform-binding` → error explaining there is no registry and how to verify), before the registry is opened. Test `TestSecretsReencrypt_AllWorkspacesNeedsDBResolver`; mutation (guard returns nil) fails it. |
| 4, 10 | P3 | Subscription id printed with `%s` from an attacker-writable database. Confirmed (`Reason` was also `%s` in the code, not `%q` as one report said). | **FIXED** (`be90990`): subscription id, key id and reason `%q`; resolve/connect/sweep errors `%q`; a registry id that fails `ValidateID` is printed quoted. Tests `TestSweepOneWorkspace_QuotesDatabaseText` (newline + ANSI escape + forged "clean"/"summary" lines stay on one line) and *integration* `TestSecretsReencrypt_SingleWorkspace` (a planted row whose id tries to forge a clean line). Mutations (`%s` back) fail them. |
| 5 | P3 | CHANGELOG embedder deltas imprecise. Confirmed: nil/zero cipher → `ErrNoCipher` for `Decrypt(enc:v2)`; unbound writes pass `enc:v2:`-prefixed inputs through verbatim (`IsEncrypted` widened); malformed `enc:v1` errors wrap `ErrMalformedEnvelope`. No SMP impact (hex credentials, no `enc:v2` producer). | **FIXED** (`74a01df`): delta list rewritten, plus the new bound-`UpdateSubscription` read. Pinned: `TestV2_RequiresAAD` (zero-value → `ErrNoCipher`, not `ErrMissingAAD`), `TestLegacy_EncryptStillV1AndDecryptPassesThrough` (malformed v1 → `ErrMalformedEnvelope`), `TestLegacyMode_Unchanged` (unbound `enc:v2` input stored verbatim; unbound update still one `UPDATE`). The pass-through itself was kept (design §1/§2: `IsEncrypted` widened, never double-encrypt), not reverted to a v1-only check. |
| 6, 14 | P3 | No verification path for `platform-binding` in the strict / key-drop / rollback runbooks (required by the Owner question 2 design-check note). Confirmed. | **FIXED** (`74a01df`): a shared *Verifying every workspace* step (file / db / platform-binding: load every bound workspace and check each one's latest `subscription secrets swept` record for `refused=0`, `conflicts=0` and the expected mode/key/current), used by the upgrade, key-rotation and rollback runbooks; a KNOWN-LIMITATIONS entry; the command's `platform-binding` refusal says the same. *(Review round 2: the `platform-binding` check was incomplete — a request to a workspace a process already holds does not sweep it, so after an overlapping rollout the check needs a restart and a post-rollout record; corrected in `0b7ecc9`.)* |
| 8 | P3 | `reencrypt` took its mode from the shell; unset meant `migrate`. Confirmed. | **FIXED** (`be90990`): `SPINE_SECRET_ENVELOPE_MODE` must be set (non-blank) for the command, checked after the key and before any database; docs, README, `.env.example` and the env table say it must equal serve's mode. Test `TestSecretsReencrypt_RequiresExplicitMode` (blank/whitespace, single and all-workspaces, nothing printed); mutation fails it. A separate `--allow-legacy-conversion` flag was not added: an explicit `migrate` is the operator's statement that conversion is intended. |
| 9 | P3 | "When P3-10 Is Closed" item 2 and the key-rotation runbook presented retirement as closure; plants converted earlier survive (re-keyed). Confirmed. | **FIXED** (`74a01df`): both closing steps are worded as closing the path *for future plants*; P3-10 is closed once one step is in place **and** the conversion-log review + `rotate-secret` is done; the key-rotation runbook gains that review as step 4, before K1 is dropped. |
| 11 | P3 | One unreachable inactive workspace made `--all-workspaces` fail forever. Confirmed (`ResolveAnyStatus`/`NewPostgresStore` ping failure → NOT CLEAN). | **FIXED** (`be90990`): resolve/connect failures are an *unreachable* outcome; `--allow-unreachable-inactive=<ids>` acknowledges named **inactive** ones (reported as ACKNOWLEDGED, listed in the summary as `acknowledged_unreachable=N (ids)`, unmatched ids noted); never an active or a reachable workspace; requires `--all-workspaces`. Documented, with retiring the registry row as the alternative. *Integration* `TestSecretsReencrypt_AllWorkspaces` now registers a decommissioned inactive `ws-four` (database never created): unclean without the flag, clean with it, and still unclean once its row is made active. Mutations (allow active; ignore the flag) fail it. |
| 12 | P3 | Nothing tested that the configured mode reaches the stores. Confirmed by the reviewer's `-overlay` drop. | **FIXED** (`a47d7d9`): both shared resolvers build their pool through one constructor, `sharedModePoolConfig` (no behaviour change). Tests: `TestSharedModePoolConfig_CarriesSecretEnvelope`, `TestNewServicePool_CarriesSecretEnvelopeMode`, *integration* `TestServicePool_BindsStoresInConfiguredMode` (PoolConfig → `Get` → `initializeEntry` → `buildServiceSet`, strict: binding strict, plaintext row refused and untouched) and `TestBuildStore_FileResolverBindsConfiguredMode`. Each of the five drops (`NewServicePool` field, `initializeEntry` argument, `sharedModePoolConfig` field, `buildStore` argument) fails its test. *(Review round 2: only partly fixed — `buildWorkspaceResolver` and the `serveCmd` call site stayed unpinned; completed in `e6bf793`.)* |

**AC-gap notes.**

- **AC19 (`make test-integration` passes) — not met literally; pre-existing; owner waiver requested.**
  Run as written this round (`go test -tags integration -p 4 -count=1 ./...`, only
  `SPINE_TEST_DATABASE_URL` set, one fresh database each) on the branch head and on a `git archive main`
  snapshot: 35 packages ok on both. Failing on **both**: `TestRunModeDatabaseDefault` (pre-existing,
  `workflow_version_label` NULL scan) and `TestAWSClient_Contract_LocalStack` (environment: the local
  LocalStack has no `secretsmanager`). The remaining failures **differ between the two runs**
  (branch: `TestExecutionProjection_RepositoriesRoundTrip`, `TestQueryGraphDepthLimit`; main:
  `TestCheckFreshness`, `TestStepExecutionCRUD`): `adapters/projection` and `adapters/store` share
  projection/run tables in the one database while running concurrently. Re-run standalone on fresh
  databases at the branch head: `adapters/projection` ok; `adapters/store` only
  `TestRunModeDatabaseDefault`. No failure touches `runtime.event_subscriptions` or a TASK-009 file.
- **AC18 (SMP DB-backed suites)** — re-run this round; see *Gates (review round 1)*.
- **Decision 15 / §3 note / AC22 intent** — closed by finding 7's fix.
- **AC16 key-rotation review step** — closed by finding 9's fix.
- **Untested single-workspace command path** — added: *integration* `TestSecretsReencrypt_SingleWorkspace`
  (file mode, `SPINE_WORKSPACE_ID=ws-file`: migrate converts and binds to `ws-file`, exit 0; strict
  refuses a planted plaintext row, leaves it, exit non-zero) and unit
  `TestSecretsReencrypt_WorkspaceFlagMustMatchFileWorkspace` (the `--workspace` mismatch guard fires
  before the database is opened).
- **"10 test functions, not 11"** — correct; the PoC row in *Gates* is fixed (10 functions; all PASS
  again at the round-1 head).
- **Lint evidence** — `golangci-lint run --new-from-rev main ./...` → 0 issues, plain and
  `--build-tags integration` (see below).

**Deviations introduced in round 1 (with reasons).**

- `store.EventSubscription` gains two unexported fields (`secretRead`, `readSecret`), set only by a
  bound read, and a bound `UpdateSubscription` writes `signing_secret` only when the caller set a
  secret (finding 7, codex round B). A caller-built value costs one extra `SELECT`. Bound stores
  only; documented for embedders. Unexported fields keep every keyed literal compiling; there is no
  unkeyed literal in either repository.
- `adapters/store/subscription.go` was gofmt'd (comment alignment) because it is now a changed file.
- codex follow-ups: `describeDBError` in the command (round A); the two-rollout key rotation and the
  `strict` → `migrate` → `downgrade` rollback with overlapping processes (round C, docs + a pinning
  test).
- `spine secrets reencrypt` now requires `SPINE_SECRET_ENVELOPE_MODE`, and `--all-workspaces`
  requires `WORKSPACE_RESOLVER=db` (findings 8, 3). AC23's integration test sets both.
- New flag `--allow-unreachable-inactive` (finding 11) and quoted output (findings 4/10); the AC23
  output format changed accordingly (`refused subscription "sub-t" envelope=enc:v2 key_id="…"`, and the
  summary gains `acknowledged_unreachable=N`).
- `serve_resolver.go`: the two shared-mode `PoolConfig` literals became `sharedModePoolConfig`
  (finding 12); this also touches the platform-binding branch, with identical fields.

### Gates (review round 1, at the round-1 head)

Head `e99027d` unless noted. Shared native PostgreSQL 17.10, `s009_r1*` databases (all dropped
afterwards); SMP DB suites in a disposable `pgvector/pgvector:pg18` container (PG 18.4), removed
afterwards.

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | exit 0 |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` (no DB env) | exit 0, 38 packages ok |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all exit 0 |
| `go test -race -p 4 -count=1` on `core/crypto`, `adapters/store`, `adapters/delivery`, `service/cmd/spine`, `service/internal/workspace`, `service/internal/gateway` | all ok |
| `go test -tags integration -count=1` per touched package, each on a fresh database (`SPINE_REGISTRY_TEST_DATABASE_URL` migrated with `spine migrate` for workspace) | crypto, delivery, cmd/spine, workspace, gateway ok; store: only the pre-existing `TestRunModeDatabaseDefault` fails. Every TASK-009 integration test ran and passed (`-v` checked). |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' ./service/scenariotest/scenarios/` (the CI job) | 19 PASS, 0 SKIP, 0 FAIL |
| AC19: `go test -tags integration -p 4 -count=1 ./...`, one fresh database, only `SPINE_TEST_DATABASE_URL` | branch `74a01df` and a `main` snapshot: 35 packages ok each; at `e99027d`: 35 ok, failing `TestRunModeDatabaseDefault`, `TestAWSClient_Contract_LocalStack` (both pre-existing, both on `main`) and `TestQueryGraphDepthLimit` (shared-database interference; differs run to run on branch and `main`, passes standalone). See *AC-gap notes*. |
| `golangci-lint run --new-from-rev main ./...` (plain and `--build-tags integration`) | 0 issues |
| `golangci-lint run --enable-only=gosec ./...` | 1 issue, pre-existing on `main` (`core/workflow/service.go` G703) |
| `gofmt -l` on every changed `.go` file | empty (`adapters/store/subscription.go`, unformatted on `main`, is now formatted) |
| AC17 `git diff --stat $(git merge-base origin/main HEAD)..HEAD -- migrations/` | empty |
| Mutations (`go test -overlay`) for every round-1 test | each FAILS its intended test(s): kept branch dropped (unit + integration); caller's read ignored / not recorded (race cases, unit + integration); explicit-mode check removed; resolver guard removed; `%q` → `%s` (unit + both integration command tests); acknowledgement allowed for active / ignored; `NewServicePool` mode field, `initializeEntry` argument, `sharedModePoolConfig` field, `buildStore` argument dropped; `describeDBError` bypassed (prints the query-parameter password) |
| Original audit PoCs overlaid unchanged | 10 PoC test functions PASS (expected, AC20) |
| SMP gate (SMP `7d4e7cc9`, `../spine` → this worktree, unmodified) | `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`: exit 0 at `e99027d`. Unit tests of the six SMP packages importing a changed Spine package (`cmd/customer-app`, `internal/customer/{eventdelivery,projectionwriter,spinecore,stepevents,workspace}`): ok (one `spinecore` failure under load during a concurrent codex run; standalone and the full six-package re-run pass). DB suites at `0ef0733` (the last production change SMP can import; `e99027d` adds a test and docs only), provisioned like SMP CI's integration job: `testgate … ./internal/customer/{eventdelivery,stepevents,workspace,projectionwriter}/` → PASS, 627 tests, 6 allowed skips (MinIO, live AWS); `testgate … ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` → PASS, 1539 tests, 1 allowed skip; `go test -race ./cmd/customer-app/...` with `TEST_PROVISIONING_DSN` → ok. (Also run at `74a01df`: same counts, PASS.) |

**codex review --base main (round 1 of this review).**

- Round A (at `74a01df`): one finding, **[P2] database configuration errors could print a password**
  — real: pgx v5.8.0 masks only a userinfo password, and a `pgconn.ParseConfigError` echoes a
  `?password=` query parameter. **FIXED** in `f4baca4` (`describeDBError`: a configuration error
  becomes a fixed message; every password-like DSN value is masked in other errors; used for the
  workspace and registry connect failures). Test `TestSecretsReencrypt_DatabaseErrorsCarryNoCredentials`
  (five DSN shapes, the helper, the registry path); without the fix it prints the password.
- Round B (at `f4baca4`): one finding, **[P1] metadata updates could still re-seal a stale legacy
  secret** when a rotation lands between the caller's read and the update — real (codex reproduced
  it). **FIXED** in `0ef0733` (see finding 7).
- Round C (at `0ef0733`): one finding, **[P2] key rotation loses deliveries during overlapping
  restarts** — real: the first K2-primary process re-keys rows, and a K1-only process then refuses
  them (`ErrUnknownKeyID`; the subscriber cannot list, queued deliveries fail permanently).
  **FIXED** in `e99027d`: the runbook distributes K2 as a previous key to every process first and
  promotes it only then (one rollout only for a stop-the-world restart); the rollback runbook goes
  `strict` → `migrate` → `downgrade` when processes overlap. Test `TestP310_TwoPhaseRotationOverlapReads`
  pins the premise (K1-only refuses K2 rows; the two phase rings read each other's rows through
  opposing sweeps, which settle on K2).
- Round D (at `e99027d`, the final code): "No actionable regressions were identified."
- No codex finding was rejected.

**Pre-existing, observed this round (not changed).** `spine serve` and `spine migrate` print or
log pgx connection errors as-is, so a database URL that passes its password as a query parameter
and fails to parse would show it there too (only this task's command was hardened, per the codex
finding). Recorded as a follow-up.

### Review round 2 (2026-10-07)

A second-round reviewer re-checked round 1 at `e8a8b9f` and reported two P3 findings and five gap
notes. I verified each one against the code before deciding on it. Commits: `e6bf793` (finding 2),
`0b7ecc9` (finding 1), `5a194ba` (codex round 2 P2), and this notes commit.

| # | Sev | Finding (verified) | Disposition |
|---|---|---|---|
| 1 | P3 | The key-rotation runbook said rows "settle on K2 once the rollout completes", and the `platform-binding` verifier said to "load every bound workspace" and read its latest sweep record. **Confirmed.** `ServicePool.Get` calls `PrepareSubscriptionSecrets` only from `buildServiceSet`, that is, when it builds an entry: the first request after the process starts, or after an eviction (idle, or a binding invalidation, which reaches only the replica that receives it). A `Get` for an entry the pool already holds does not sweep. A step-2 process that first loads a workspace late in the promote rollout re-keys it back to K1, and nothing re-keys it after the rollout. Loading an already-built workspace emits no record, and a clean K2 record from earlier in the rollout does not show the current state. The same shape holds for downgrade→migrate (rows left in `enc:v1`; `strict` then refuses them) and migrate→downgrade on rollback (rows left in `enc:v2`; the old binary signs with the envelope text). File and `db` mode are safe if the command runs after the rollout completes, because it sweeps again itself. | **FIXED** (`0b7ecc9`, docs + premise tests). `docs/integration-guide.md` §2.1: the sweep triggers now say a held workspace is not swept, and name the eviction causes. *Verifying every workspace* runs only after the rollout of the change has completed, and names the three overlap cases. File/`db` say that the command sweeps itself. `platform-binding` now has three steps: (1) restart every process once the rollout has completed (skip only after a stop-the-world restart); (2) load every workspace; (3) take the latest record across all replicas, which must come from a process started after the rollout and be clean. Key rotation step 3: the rows reach K2 only when a K2-primary sweep runs after the last step-2 process stops, and step 4 does that; step 4 waits for the rollout and gates step 6. Several-replica upgrade step 2: the baseline is the first clean `migrate` sweep after the last `downgrade` replica stopped. Rollback step 1 notes the `migrate`→`enc:v2` sweep-back, and step 2 waits for the rollout. The workspace-id runbook step 1 uses the verifier. The KNOWN-LIMITATIONS entry, the CHANGELOG (the command entry and "Keys rotate"), and the command's help text and `platform-binding` refusal say the same. Tests: `TestP310_TwoPhaseRotationOverlapReads` now also pins that a late distribute-phase sweep leaves the row on K1, that a K2-only ring then refuses it (`ErrUnknownKeyID`), and that one K2-primary sweep fixes it. *Integration* `TestServicePool_SweepsOnlyWhenItBuilds`: three `Get`s on a held workspace leave an `enc:v1` value written behind its back, and the first `Get` after `Evict` converts it. No code change: re-sweeping on every `Get` would put a database scan on every request, and an operator endpoint to force a sweep is a feature (recorded as a follow-up with the enumeration source). |
| 2 | P3 | Round-1 finding 12 was only partly fixed. **Confirmed.** No test reached `buildWorkspaceResolver` or the `serveCmd` call site. `""` is an untyped constant that converts to `SecretEnvelopeMode` and means migrate, so dropping the mode argument at `serve_cmd.go:90` or `serve_resolver.go:437` compiled and passed every test. | **FIXED** (`e6bf793`). (a) `buildServeWorkspaces` (`serve_builders.go`) is the start-up step that loads the key ring and mode from the environment and passes the same value to `buildWorkspaceResolver` and `buildStore`. `serveCmd` calls it with no mode argument and closes what it returns. The log lines and the `storeResolver` selection moved into it unchanged. (b) Both shared branches build their pool through one `newSharedPool` closure in `buildWorkspaceResolver`, the only place the cipher and mode enter. (c) `ServicePool.SubscriptionSecretConfig()` reports what the pool binds with (`service/internal`, not reachable from SMP). Tests: unit `TestBuildServeWorkspaces_SharedPoolCarriesConfiguredMode` drives `buildServeWorkspaces` through `platform-binding` (the branch that needs no database) for strict, downgrade, migrate, unset and no-key, plus a bad-mode refusal; it runs in the plain `go test ./...` CI uses. *Integration* `TestBuildServeWorkspaces_{File,DB,PlatformBinding}ResolverBindsConfiguredMode` go from the environment, with `strict`, through `buildServeWorkspaces`, then `Pool.Get` (db: a test registry; platform-binding: an `httptest` platform and the file secret backend), to a store bound strict to the right id, with a plaintext row seeded before start-up refused and untouched. Mutations (each restored afterwards): `""` at `newSharedPool` fails the unit test and the db and platform-binding integration tests; `""` at the `buildWorkspaceResolver` call likewise; `""` at the `buildStore` call fails the file integration test; a db branch that bypasses `newSharedPool` with `""` fails the db integration test; the same for platform-binding fails the unit test and the platform-binding integration test. |

**Gap notes.**

- Round-1 dispositions: agreed. No finding was "NOT REAL".
- Cheap gates: re-run at the round-2 head (below).
- **Integration tests not re-run by the reviewer** (the permission layer denied database credentials). They were re-run this round on fresh `s009_r2_*` databases, at `0b7ecc9` and again at `5a194ba`. Every TASK-009 integration test passed (checked with `-v`), including the round-1 additions `TestBoundUpdate_RealPostgresKeepsUnchangedSecret`, `TestServicePool_BindsStoresInConfiguredMode`, `TestBuildStore_FileResolverBindsConfiguredMode`, `TestSecretsReencrypt_SingleWorkspace` and `TestSecretsReencrypt_AllWorkspaces`, and the round-2 ones. The AC18 SMP DB suites were re-run too (below).
- **AC19 (`make test-integration`)**: unchanged. `TestRunModeDatabaseDefault` is still the pre-existing failure, failing the same way (`workflow_version_label` NULL scan). **The owner waiver is still outstanding.**
- AC→test mapping: unchanged. AC9's envelope-input refusal still fires before the kept-secret branch, because `UpdateSubscription` seals, and so validates, first. The round-2 snapshot refresh runs only after a successful `Exec`.
- **Remaining limit (finding 2):** Spine CI runs only `go test ./...` and the scenario job, not `-tags integration`. In CI, the unit test pins the shared-pool path (`platform-binding`, and `newSharedPool`, which db also uses). The file-mode `buildStore` hop, and a db branch that bypasses `newSharedPool`, are pinned only by the integration tests. A unit test for those needs a database: `NewDBProvider` pings, and `buildStore` binds only a store that connected.

**codex review --base main (round 2 of this review).**

- Round A (at `0b7ecc9`): one finding, **[P2] refresh the secret snapshot after a successful
  replacement**. **Real.** A value from a bound read remembers the secret that read returned, and a successful
  bound write did not refresh it. Reusing the value went wrong in two ways. Read A, set B, set A back: the last
  update was judged "kept", reported success, and left B in the row. After setting B, a
  metadata-only update saw B ≠ A, wrote B again, and reverted a rotation that landed in between. **FIXED** in
  `5a194ba`: a successful bound `UpdateSubscription` (either branch) and `CreateSubscription` record
  `SigningSecret` as the value's snapshot. A failed write records nothing, and unbound stores record nothing
  (`recordSecretRead` is a no-op without a binding), so SMP's unbound stores are unchanged. The creation
  case follows the same principle (a created value reused for an update would otherwise fall back to the
  stored value and revert a racing rotation). Tests, in `TestBoundUpdate_UnchangedSecretKeepsStoredBytes`:
  set then set back; update after a set keeps a later rotation; a created or caller-built value reused keeps
  a later rotation; a failed update records nothing, so a retry still writes (memSubs gains `updateErr`).
  `TestLegacyMode_Unchanged`: unbound create and update record nothing. *Integration*
  `TestBoundUpdate_RealPostgresKeepsUnchangedSecret`: set back on real Postgres. Mutations: dropping
  the record after a secret write fails two subtests and the integration test; after a kept update,
  or after create, fails the reuse subtest; recording on failure fails the failure subtest. CHANGELOG
  "For embedders" updated.
- Round B (at `5a194ba`, the final code): "No actionable regressions were identified."
- No codex finding was rejected.

**Deviations introduced in round 2 (with reasons).**

- `serveCmd` now loads `SPINE_CODE_REPO_BASE` (and the delivery config) **before** the key ring and
  mode, because `buildServeWorkspaces` needs both. Only the order of start-up errors and log lines changes
  when both are misconfigured; every check and message is the same.
- `serve_cmd.go` no longer imports `service/internal/workspace` (the store-resolver selection moved into
  `buildServeWorkspaces`).
- New accessor `ServicePool.SubscriptionSecretConfig()` (`service/internal`, so not part of the
  library surface SMP embeds).
- `CreateSubscription` and `UpdateSubscription` under a binding now refresh the unexported snapshot fields
  of the value they were given (codex round 2). The library surface does not change.

### Gates (review round 2, at `5a194ba` unless noted)

Shared native PostgreSQL 17.10, `s009_r2*` databases (all dropped afterwards). SMP DB suites ran in a
disposable `pgvector/pgvector:pg18` container (PG 18.4) from the local image, provisioned like SMP CI's
`integration` job, recreated fresh for the `5a194ba` run, and removed afterwards.

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | exit 0 |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` (no DB env) | exit 0, 38 packages ok (also at `0b7ecc9`) |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all exit 0 (also at `0b7ecc9`) |
| `go test -race -p 4 -count=1` on `core/crypto`, `adapters/store`, `adapters/delivery`, `service/cmd/spine`, `service/internal/workspace`, `service/internal/gateway` | all ok (`0b7ecc9`: all six; `5a194ba`: the five that depend on `adapters/store`, since `core/crypto` was unchanged) |
| `go test -tags integration -p 4 -count=1 -v` per touched package, each on a fresh database (`s009_r2_{store,dlv,cmd,crypto,gw,ws}`; workspace also `SPINE_REGISTRY_TEST_DATABASE_URL=s009_r2_reg`, migrated with `spine migrate --all-workspaces`) | at `0b7ecc9` and again at `5a194ba`: delivery 69 PASS, cmd/spine 55, crypto 26, gateway 533, workspace 147, all ok, 0 skips; store 61 PASS and 1 FAIL, the pre-existing `TestRunModeDatabaseDefault` |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (the CI job, fresh `s009_r2_scen`) | 19 PASS, 0 SKIP, 0 FAIL (also at `0b7ecc9`) |
| `golangci-lint run --new-from-rev main ./...`; and `--build-tags integration` on the six touched trees | 0 issues each (also at `0b7ecc9`) |
| `golangci-lint run --enable-only=gosec ./...` | 1 issue, pre-existing on `main` (`core/workflow/service.go:189` G703) |
| `gofmt -l` on every `.go` file in `git diff --name-only main...HEAD` | empty |
| AC17 `git diff --stat $(git merge-base main HEAD)..HEAD -- migrations/` | empty |
| Mutations (copy, edit, test, restore) | finding 2: five drops, each fails as listed above. Codex round 2: four, each fails as listed above. Finding 1: a `Get` that sweeps a held entry fails *integration* `TestServicePool_SweepsOnlyWhenItBuilds`; dropping the K2-primary sweep after the late distribute-phase sweep fails `TestP310_TwoPhaseRotationOverlapReads` (`ErrUnknownKeyID` on the K2-only ring) |
| SMP gate (SMP `7d4e7cc9`, `../spine` → this worktree, unmodified; `go list -m` confirms) | `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` exit 0 (at `0b7ecc9` and `5a194ba`). No SMP package imports `service/...` (`go list` over Imports/TestImports/XTestImports: 0). Unit tests of `cmd/customer-app` and `internal/customer/{eventdelivery,projectionwriter,spinecore,stepevents,workspace}`: ok (both heads). DB suites (both heads): `testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -allow-skip 'TEST_MINIO' -- -race … ./internal/customer/{eventdelivery,stepevents,workspace,projectionwriter}/` → **PASS**, 627 tests, 6 allowed skips (MinIO and live AWS only: the dev stack's MinIO was not used); `testgate -allow-skip 'Un-skip to add an operator-level round-trip' -- -race … ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` → **PASS**, 1539 tests, 1 allowed skip; `go test -race ./cmd/customer-app/...` with `TEST_PROVISIONING_DSN` → ok. AC18's named tests ran and PASS: `TestWorker_Integration_DeliversEncryptedSubscription`, `TestWorker_Integration_RequiresCipherToDecrypt`, `TestCreateInitialActors_Integration_EncryptsSigningSecretAtRest`. |
| `codex review --base main` | round A (`0b7ecc9`): one P2, fixed (`5a194ba`); round B (`5a194ba`): no actionable regressions |

### Pre-existing issues observed (not changed by this task)

- `TestEventSubscription_SigningSecretEncryption` is not re-runnable on one database
  (`CleanupTestData` does not clear `runtime.event_subscriptions`, so a second run hits a duplicate
  key). It passes on a fresh database. TASK-009's own integration tests scope their assertions to
  their own rows.
- `TestRunModeDatabaseDefault` fails on `main` and on this branch (above).
- 30 scenario tests fail on `main` and on this branch (identical sets).
- `setupRegistryTable` in `db_provider_test.go` creates a registry table without
  `smp_workspace_id`; the DBProvider tests pass only on a database migrated with
  `migrations/registry` (as used here).

### Follow-ups (recorded, not done)

Strict by default (owner question 1); per-row tolerance in list paths; weak-key guard; the
shared-database diagnostic in `Reason`; SMP TASK-006 adoption (§8, including the equal-keys refusal).
*Review round 1 additions:* an enumeration source for `platform-binding` verification (KNOWN-LIMITATIONS);
credential-free connection errors in `spine serve` / `spine migrate` (pre-existing, above); SMP
TASK-006 should build its realign/ensure updates from the values the bound store returns (so
"kept" is judged from the caller's read) and require an explicit mode for its all-workspaces sweep;
an owner waiver (or follow-up) for AC19's pre-existing `make test-integration` failures.
*Review round 2 additions:* an on-demand per-workspace sweep for `spine serve` (operator endpoint or
signal), so `platform-binding` verification does not need a restart of every process; a Spine CI job
that runs the `-tags integration` start-up wiring tests (`TestBuildServeWorkspaces_*`), so the file-mode
and db-branch hops are pinned in CI, not only locally.

### Integration (wave 2 merge) (2026-10-08)

Integration branch `init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`), cut from Spine
local `main` `ee3c249`. Merge order, each `git merge --no-ff` and built and tested before the next:
TASK-002 `b674b90`, TASK-016 `350fe87`, TASK-004 `50b9c8e`, TASK-009 `8925de9`, TASK-007 `2aca054`,
TASK-011 `c90c049`, TASK-010 `1e63692`, TASK-013 `85bf4b3`. Coordination commits: `3b88fb1`
(TreeEntryType joins the ref grammar), `0c26c91` (step-entry policy through the shared pool
constructor), `9e43640` (a refused signing secret holds its deliveries), `d61359d` (security model:
skills are a step-entry boundary), `1291e74` (codex round 1). Nothing was pushed; `main` and the task
branches are unchanged.

**This task in the integration.** Merge `8925de9`.

- *Conflicts:* `CHANGELOG.md`, `KNOWN-LIMITATIONS.md` and the import block of `serve_builders.go`.
- *With TASK-007 (`2aca054`):* TASK-007 threaded its policy into the two `NewServicePool` calls this
  task had replaced with `sharedModePoolConfig`/`newSharedPool`. The policy is now parsed first in
  `serveCmd` and passed `buildServeWorkspaces → buildWorkspaceResolver → sharedModePoolConfig →
  newPooledWorkspaceBuilder`, so the single constructor stays; this task's tests pass the strict
  policy. `0c26c91` pins that the pool's Builder installs the policy.
- *With TASK-011 (`c90c049`):* TASK-011's `pool_projection_wiring_test.go` called `buildServiceSet`
  without the envelope-mode argument; it passes `""` (migrate), as `pool_test.go` does.
- *With TASK-013 (`85bf4b3`):* TASK-013's side still carried `main`'s `loadSecretCipher` in
  `serveCmd`; dropped, since this task loads the ring in `buildServeWorkspaces`.
- *With TASK-010 (`9e43640`):* §2.1's key-rotation paragraph said queued deliveries "fail
  permanently" when a ring lacks a key. With TASK-010 an envelope refusal is an unreadable
  subscription: retried on backoff then every minute, the backlog held, dead only at the 24 h hold
  cap. The paragraph now says so; `TestSecretEnvelopeRefusalIsHeldNotDeadLettered` drives every
  refusal sentinel, wrapped as `GetSubscription` wraps it. TASK-010's bootstrap metadata heal writes
  back the secret it read, so this task leaves the stored bytes untouched; v2 signing uses the opened
  secret. The subscription handler keeps both the `rotate-secret` replacer and TASK-010's metadata
  decoding.
- `TestDBProvider_ResolveAnyStatus` ran (registry database migrated from `migrations/registry`).

**Gates on the integration branch** (code at `d61359d`; `1291e74` changes one `adapters/git` test
file, after which `go vet` and `go test -race ./adapters/git/...` were re-run, ok):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "library surface is sound" |
| `go test -p 4 -count=1 ./...` | exit 0 (40 packages ok) |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 25 packages the eight tasks touched | 24 ok; `service/scenariotest/scenarios` has only `scenario`-tagged files |
| `-tags integration`, one fresh `s2i_int_*` database per package | store 165 PASS + the pre-existing `TestRunModeDatabaseDefault`; delivery 314, projection 160, cmd/spine 168, workspace 223 (with `SPINE_REGISTRY_TEST_DATABASE_URL`, registry migrated from `migrations/registry`), testutil 4; 0 SKIP |
| CI `scenario-git-http` job | `TestGitHTTP_*` 20 PASS, 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` | 30 FAIL, the **same set** as a `git archive main` export on its own database (1479 vs 1475 PASS, 6 SKIP for the registry env in both) |
| `git-floor` job's two steps in `golang:1.26-bookworm` (git 2.39.5, read-only mounts, `GOPROXY=off`, `--pull=never`, `--rm`) | step 1: 73 PASS, 0 SKIP, 0 FAIL (includes the new `RefGrammar_TreeEntryType*`); step 2 all ok |
| `golangci-lint run --build-tags scenario` on the touched packages | `--new-from-rev main` reports one QF1006 at `adapters/delivery/webhook_dispatcher_test.go:349`, present on `main` (TASK-010 changed the literal on that line). Full sets compared by (file, linter, text): 298 issues vs `main`'s 348, nothing new except three `rangeValCopy` whose byte count moved 224 → 248 (`store.EventSubscription` grew in TASK-009) |
| `--enable-only=gosec` | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| `codex review --base main` | round 1: one P2 (macOS xcrun cache in the TASK-002 leftover checks), fixed in `1291e74`; round 2: "No actionable regressions were identified relative to the merge base." |
| SMP compat (`i23/s-int2/smp` = SMP `6ca0eeb7`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, never modified) | `go build ./...`; `go vet` plain, `integration`, `scenario`, `faultinject`: OK. `go test -race -p 4` on the 38 SMP packages depending on a changed Spine package (`go list -test -deps`) plus `./internal/customer/...` (67 packages): 62 ok, 5 without tests. `dev/testgate -allow-skip TEST_PROVISIONING_DSN -- -race` on spinecore, spineembedded, runner, executionreadiness (`s2i_smp_spine_runtime` and `s2i_smp_platform`, migrated with `dev/apply-migrations`): 1785 passed, 0 failed, 3 allowed skips. githttp `-tags integration` testgate: 208 passed. Extra: eventdelivery, projectionwriter, stepevents testgate: 35 passed, 6 allowed skips. `dev/lint-spine-internals.sh` OK. `TEST_PROVISIONING_DSN` suites not run (out of scope). |

All `s2i_*` databases were dropped afterwards; the only container started was removed.

### Cross-task review (wave 2) (2026-10-08)

**This task.** Finding 2: `spine secrets reencrypt` without `--all-workspaces` no longer binds a
guessed `default` workspace id.

Wave 2 cross-task review of the integration branch at `2fab682`, five lenses (governance-engine,
secrets-delivery, git-ref-surface, smp-compat, merge-ci). Each P1/P2 survived two adversarial
skeptics. Every finding was re-verified against the code before it was fixed. Fixes are on
`init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`). Nothing was pushed, and `main`
and the task branches are unchanged.

| # | Finding (lens) | Severity | Disposition | Commit | Regression test |
|---|---|---|---|---|---|
| 1 | Separation of duties counted only `manual`/`automated` executions as authorship, while TASK-016 lets the bound actor of every type but `review`/`internal` push the run branch: the evaluator of a `convergence` step (or of an untyped step) could push commits and then review the run (governance-engine) | P2 (both skeptics: P2) | **FIXED.** `isAuthorStepType` is `!refadmit.FrozenStepType`, used by `runAuthor`, the early return and the symmetric guard. KNOWN-LIMITATIONS, `architecture/actor-model.md`, CHANGELOG and the TASK-007 notes no longer claim full coverage, and they state the operator gap (operators push unbound and are invisible to SoD) | `b191be9` | `TestStepEntry_SoDAuthorshipCoversEveryPushableStepType` drives TASK-016's evaluator (orchestrator as step-definition source), then assign and claim, for every step type plus a live-reviewer case. Mutation: the old predicate fails 6 sub-tests. Also `TestIsAuthorStepType` |
| 2 | `spine secrets reencrypt` without `--all-workspaces` bound the FileProvider's `default` fallback when `SPINE_WORKSPACE_ID` was unset. A migrate sweep then sealed legacy rows under `default` and reported the workspace clean, and serve, bound to its real id, refused every row (secrets-delivery) | P2 (both skeptics: P3) | **FIXED.** The command refuses ("nothing was changed") unless `SPINE_WORKSPACE_ID` is set or `--workspace` is given (`--workspace default` stays valid), as it already did for the mode. The header line prints `workspace_id=`. Integration guide, `.env.example` and CHANGELOG updated | `808bd6b` | `TestSecretsReencrypt_SingleWorkspaceRequiresExplicitID` (refusal before any database is opened); integration `TestSecretsReencrypt_SingleWorkspaceNeverGuessesTheID` (the reviewer's reproduction: both rows untouched; with the id stated, serve reads them). Mutation fails both |
| 3 | An exploratory `branch_id` of `refs` (and a workflow-defined `refs`/`HEAD`) created `spine/<run>/<div>/refs`, which TASK-016 rule 0b refuses for every principal (git-ref-surface) | P3 | **FIXED for the API path:** `ValidateExploratoryBranchID` refuses `refs` (400). `HEAD` cannot match the lower-case grammar, and the run and divergence components the engine builds never equal either name. **Follow-up:** structured branch ids and divergence-point ids from a workflow definition are not validated. Checking them in `workflow.ValidateSemantic` changes validation output on SMP's `WorkflowWriter` path, and a `createBranchRecord` refusal changes embedded behaviour; under the INIT-023 rule both need an SMP-compat argument or an opt-in. The divergence-point id needs care too: one containing `/refs` produces the same component | `abfb014` | `TestValidateExploratoryBranchID_AgreesWithPushAdmission` classifies the ref of every admitted id with `refadmit.Classify` |
| 4 | `resolveRev` is branch-first, so a contributor-pushed `refs/heads/<n>` overrides a same-named operator tag in bare-name reads (`?ref=` on the artifact, list and workflow APIs). git and Spine at `61994fe` read the tag (git-ref-surface) | P3 | **DOCUMENTED, follow-up.** A new KNOWN-LIMITATIONS residual under "Git Ref Arguments" gives the workarounds: name tags in full, or protect the branch namespace with a rule. Not fixed in `resolveRev` because each option changes engine-internal resolution for every embedder. Refusing an ambiguous bare name lets anyone who can push a same-named tag deny reads of a run branch where tags are not operator-only. Preferring the tag reopens F10's tag substitution. Follow-up (owner: TASK-002): qualify user-supplied `?ref=` at the standalone gateway and refuse a bare name that is both a branch and a tag, which costs one extra `show-ref` on that path only | `4849960` | — |
| 5 | The first-sync workflow sweep judges rows at a head resolved before it lists them. It deleted a row another instance had projected at a newer head, then took the up-to-date early return without writing `sync_state`, so nothing re-diffed the window and the row stayed lost (smp-compat) | P3 | **FIXED.** The sweep reports whether it wrote a row. On the early-return path a sweep that wrote re-records `sync_state` (idle at the head it judged by), so if another instance advanced it, the next tick re-diffs and restores the row. A sweep that wrote nothing keeps the early return read-only. **Residual** (CHANGELOG): if the other instance's whole sync spans the sweep, the row can still be lost. That is the same window the diff path had before TASK-011. A compare-and-set on `sync_state` (an optional store capability) would close it; follow-up | `0fe5011` | `TestSweep_StaleHeadInstanceDoesNotLoseANewerRow` (two instances over one store; a stale-HEAD decorator lets the other instance sync between head resolution and the sweep's listing; mutation fails), `TestSweep_NoChangeKeepsTheEarlyReturnReadOnly` |
| 6 | The CHANGELOG's TASK-004 entry said SMP's `validateRepoRelativePath` already refuses `a//b.md`, `a/./b.md` and a trailing `/`. It refuses only empty, absolute, NUL and `..` paths, and only in front of SMP's `RealGitOperator` (smp-compat) | P3 | **FIXED.** The CHANGELOG now says SMP is unaffected because its embedded entry points pass canonical paths (`normalizeTaskPath`, `path.Clean` equality, `BuildArtifactPath`; checked at SMP `6ca0eeb7`). The TASK-004 §1a note is corrected | `4849960` | — |
| 7 | CHANGELOG statements that the merge made wrong: TASK-001 still said a control byte in `branch_id` is a 500, TASK-002 still "recommended" refusing object-id-named refs to TASK-016, and `TreeEntryType` (joined to the Rev grammar in `3b88fb1`) was missing from TASK-002's Rev lists and from TASK-011's `TreeEntryTyper` entry (merge-ci) | P3 | **FIXED** | `4849960` | — |

Also checked and unaffected: under the zero-value step-entry policy (SMP today) finding 1's change
is invisible, and no shipped Spine or SMP workflow has a `convergence` or untyped step. The
`spine serve` strict scenario suite gives the same results before and after (below).

**Gates** (code at `4849960`; the artifact notes were committed afterwards):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 25 packages touched since `main` | 24 ok; `service/scenariotest/scenarios` has only `scenario`-tagged files |
| `-tags integration`, one fresh `s2f_int_*` database per package | store 165 PASS plus the pre-existing `TestRunModeDatabaseDefault` failure; delivery 314; projection 162 (+2); cmd/spine 170 (+2); workspace 223 (with `SPINE_REGISTRY_TEST_DATABASE_URL`, registry migrated from `migrations/registry`); testutil 4; 0 SKIP |
| CI `scenario-git-http` job | `TestGitHTTP_*` 20 PASS, 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` | 30 FAIL, the **same set** as a `git archive 2fab682` export (the pre-review integration head) run on its own database: 1479 PASS and 6 SKIP in both |
| `golangci-lint run --build-tags scenario --new-from-rev main` on the touched packages | 1 issue: the QF1006 at `adapters/delivery/webhook_dispatcher_test.go:349`, present on `main`. The full set is 298 issues, as at the integration, and none is in a file this review changed. `--build-tags integration --new-from-rev 2fab682` on `service/cmd/spine` and `adapters/projection`: 0 |
| `--enable-only=gosec` | 0 issues |
| `gofmt -l` on every `.go` file changed since `2fab682` | empty |
| `codex review --base main` | "No actionable regressions were identified." |
| SMP compat (`i23/s-int2/smp` = SMP `6ca0eeb7`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, never modified) | `go build ./...`; `go vet` plain, `integration`, `scenario`, `faultinject`: OK. `go test -race -p 4` on the 67 packages (dependents of a changed Spine package plus `./internal/customer/...`): 62 ok, 5 without tests. `dev/testgate -allow-skip TEST_PROVISIONING_DSN -- -race` on spinecore, spineembedded, runner, executionreadiness (`s2f_smp_spine_runtime` and `s2f_smp_platform`, migrated with `dev/apply-migrations`): 1785 passed, 0 failed, 3 allowed skips. githttp `-tags integration` testgate: 208 passed. `dev/lint-spine-internals.sh` OK. `TEST_PROVISIONING_DSN` suites not run (out of scope) |

All `s2f_*` databases were dropped afterwards, and no container was started.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: stays In Progress.** Spine `main` was pushed on 2026-11-04 with the owner's
  approval. GitHub CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802`
  plus INIT-024 TASK-010's git-identity fix) passed all 8 jobs. Remaining (owner):
  - **AC19:** the waiver for `make test-integration`'s pre-existing failures
    (`TestRunModeDatabaseDefault`, and `TestAWSClient_Contract_LocalStack`, which needs LocalStack),
    still outstanding (review round 2).
