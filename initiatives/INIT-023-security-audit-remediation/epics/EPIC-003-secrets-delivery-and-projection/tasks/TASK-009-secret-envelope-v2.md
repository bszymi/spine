---
id: TASK-009
type: Task
title: "enc:v2 secret envelope (key id + AAD + key ring) and subscription-secret re-encryption sweep"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
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

