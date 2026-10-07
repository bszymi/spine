package store

// Bound subscription signing secrets (INIT-023 TASK-009, audit P3-10 and
// P3-1).
//
// runtime.event_subscriptions.signing_secret is the HMAC key the webhook
// dispatcher signs every delivery with. Without a binding the store keeps
// its historical behaviour: it writes enc:v1 (no associated data) when a
// cipher is configured and reads plaintext and enc:v1. enc:v1 binds
// nothing, so with one deployment-wide key a value copied from workspace
// A's database into workspace B's opens in B, and B then signs with A's
// key (P3-10).
//
// SetSubscriptionSecretBinding switches the store to the enc:v2 envelope,
// whose associated data is
//
//	AssociatedData("runtime.event_subscriptions.signing_secret", <bound workspace id>, <subscription id>)
//
// The workspace id comes from the binding — process configuration, the
// workspace whose database this store was opened for — never from the
// row (the row's workspace_id column is writable by the very database
// writer this defends against, and UpdateSubscription does not even
// persist it). ReencryptSubscriptionSecrets is the explicit sweep that
// converts existing plaintext and enc:v1 rows (P3-1: "re-encrypt on the
// next update" never happened).
//
// Invariants (TASK-009 §0):
//   - I2: the bound workspace id comes from configuration only.
//   - I3: a value that does not open under the binding is an error. It is
//     never treated as plaintext, blanked, or re-sealed.
//   - I4: nothing rewrites a row it could not open; strict converts no
//     legacy row; an envelope is never accepted as a write input; an
//     update that keeps the secret leaves the stored bytes alone, so the
//     sweep — which records every conversion — is the only path from a
//     legacy value to enc:v2.
//   - I5: the sweep never overwrites a value that changed after it read it.
//   - I6: no plaintext or stored value appears in an error or a report.
//   - I8: under a binding, no read returns an envelope as the secret, and
//     the sweep never writes one.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
)

// SecretEnvelopeMode selects how a bound store treats legacy values.
//
//	mode       write    read enc:v2   read enc:v1 / plaintext   sweep target
//	migrate    enc:v2   open          accepted                  enc:v2 under the primary key
//	strict     enc:v2   open          refused                   enc:v2; legacy rows reported, not converted
//	downgrade  enc:v1   open          accepted                  enc:v1 under the primary key
//
// migrate (the zero value) trusts the legacy rows present when it runs:
// it cannot tell a pre-upgrade enc:v1 row from a planted copy of another
// workspace's pre-upgrade enc:v1 value, so it converts both. The audit
// P3-10 transplant is therefore closed only once the store runs strict,
// or once every key that ever sealed an enc:v1 value has left the ring.
// downgrade prepares a rollback to a binary that predates enc:v2.
type SecretEnvelopeMode string

const (
	// SecretEnvelopeMigrate writes enc:v2, accepts legacy values, and
	// sweeps them to enc:v2. The default.
	SecretEnvelopeMigrate SecretEnvelopeMode = "migrate"
	// SecretEnvelopeStrict writes enc:v2 and refuses plaintext and
	// enc:v1 values (ErrLegacySecretRefused).
	SecretEnvelopeStrict SecretEnvelopeMode = "strict"
	// SecretEnvelopeDowngrade writes enc:v1, reads everything, and
	// sweeps enc:v2 rows back to enc:v1 so an older binary can read
	// them.
	SecretEnvelopeDowngrade SecretEnvelopeMode = "downgrade"
)

// ParseSecretEnvelopeMode parses a configured mode, case-insensitively
// and ignoring surrounding whitespace. "" is SecretEnvelopeMigrate.
func ParseSecretEnvelopeMode(s string) (SecretEnvelopeMode, error) {
	switch m := SecretEnvelopeMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return SecretEnvelopeMigrate, nil
	case SecretEnvelopeMigrate, SecretEnvelopeStrict, SecretEnvelopeDowngrade:
		return m, nil
	default:
		return "", fmt.Errorf("unknown secret envelope mode %q (want migrate, strict or downgrade)", s)
	}
}

// SubscriptionSecretBinding binds event_subscriptions.signing_secret to
// the workspace this store serves.
type SubscriptionSecretBinding struct {
	// WorkspaceID is the trusted scope sealed into every value: the
	// workspace whose runtime database this store was opened for, from
	// process configuration. Never take it from a database row.
	// Every store that reads or writes one workspace's subscriptions
	// must use the same id, or the other's values fail closed.
	WorkspaceID string
	// Mode is the legacy policy; "" means SecretEnvelopeMigrate.
	Mode SecretEnvelopeMode
}

// subscriptionSecretPurpose is the AssociatedData purpose of a signing
// secret. Part of the wire format: changing it orphans every bound row.
const subscriptionSecretPurpose = "runtime.event_subscriptions.signing_secret" //nolint:gosec // G101: an associated-data label naming the column, not a credential.

var (
	// ErrSecretCipherRequired: the store is bound but has no cipher, or
	// the sweep was asked to run without one. A binding means "seal";
	// without a key the store refuses rather than writing plaintext.
	ErrSecretCipherRequired = errors.New("subscription secret binding requires a secret cipher")
	// ErrNoSecretBinding: ReencryptSubscriptionSecrets on a store with no
	// binding. Nothing is touched.
	ErrNoSecretBinding = errors.New("subscription secrets are not bound to a workspace")
	// ErrLegacySecretRefused: a plaintext or enc:v1 value read in strict
	// mode.
	ErrLegacySecretRefused = errors.New("legacy signing secret (plaintext or enc:v1) refused in strict mode")
	// ErrEnvelopeInput: a bound write was given a value that is already
	// an envelope (enc:v1, enc:v2 or a reserved unknown version). Bound
	// writes take the plaintext secret only; an opaque envelope from
	// elsewhere is never stored, nor opened and re-sealed for this row.
	ErrEnvelopeInput = errors.New("signing secret input is already an encryption envelope; pass the plaintext secret")
	// ErrNestedEnvelope: a stored value opens to another envelope that is
	// not this row's own enc:v2 value (see openBound).
	ErrNestedEnvelope = errors.New("signing secret opens to a nested envelope that does not belong to this row")

	errSubscriptionIDRequired = errors.New("subscription id is required for a bound signing secret")
)

// SetSubscriptionSecretBinding switches this store's subscription signing
// secrets to the enc:v2 envelope bound to b.WorkspaceID (see the package
// comment above). It refuses an empty workspace id and an unknown mode;
// "" means SecretEnvelopeMigrate. It does not validate the cipher: a
// bound store with no cipher refuses every non-empty secret
// (ErrSecretCipherRequired) when it is used.
//
// Like SetSecretCipher, call it before the store is shared between
// goroutines. A store with no binding behaves exactly as before
// TASK-009.
func (s *PostgresStore) SetSubscriptionSecretBinding(b SubscriptionSecretBinding) error {
	if strings.TrimSpace(b.WorkspaceID) == "" {
		return errors.New("subscription secret binding: workspace id is required")
	}
	switch b.Mode {
	case "":
		b.Mode = SecretEnvelopeMigrate
	case SecretEnvelopeMigrate, SecretEnvelopeStrict, SecretEnvelopeDowngrade:
	default:
		return fmt.Errorf("subscription secret binding: unknown mode %q", b.Mode)
	}
	s.secretBinding = &b
	return nil
}

// SubscriptionSecretBinding returns the store's binding and whether one
// is set.
func (s *PostgresStore) SubscriptionSecretBinding() (SubscriptionSecretBinding, bool) {
	if s.secretBinding == nil {
		return SubscriptionSecretBinding{}, false
	}
	return *s.secretBinding, true
}

func subscriptionSecretAAD(workspaceID, subscriptionID string) []byte {
	return spinecrypto.AssociatedData(subscriptionSecretPurpose, workspaceID, subscriptionID)
}

// boundOpen is a stored value opened under the binding.
type boundOpen struct {
	// plaintext is the secret.
	plaintext string
	// outer describes the stored value's own envelope and the ring key
	// that opened it.
	outer spinecrypto.Opened
	// repaired: the stored value was an envelope around this row's own
	// enc:v2 value, and plaintext is the inner secret (I8).
	repaired bool
}

// openBound opens a stored value under the binding and applies the mode.
// The returned outer.Envelope is set on error too. Errors wrap a sentinel
// and carry no part of the value.
//
// Nested envelopes (I8, TASK-009 Decision 14). A binary that predates
// enc:v2 reads a v2 row as plaintext (its Decrypt returns unknown
// prefixes verbatim) and, on write-back, seals that text as enc:v1 —
// producing enc:v1(enc:v2:…). Returning the opened value would make the
// envelope text the HMAC key. So when the opened value is itself an
// envelope:
//   - an enc:v2 value that opens under this very row's associated data
//     with a ring key is unwrapped, once; the inner value must then be a
//     plain secret. It cannot launder anything: it is authenticated to
//     this row.
//   - anything else is ErrNestedEnvelope.
//
// The mode applies to the outer envelope: in strict mode an outer enc:v1
// is refused (ErrLegacySecretRefused) before any unwrapping.
func (s *PostgresStore) openBound(subscriptionID, stored string) (boundOpen, error) {
	b := s.secretBinding
	env := spinecrypto.ClassifyEnvelope(stored)
	if env == spinecrypto.EnvelopeEmpty {
		return boundOpen{outer: spinecrypto.Opened{Envelope: env}}, nil
	}
	if s.cipher == nil {
		return boundOpen{outer: spinecrypto.Opened{Envelope: env}}, ErrSecretCipherRequired
	}
	if subscriptionID == "" {
		return boundOpen{outer: spinecrypto.Opened{Envelope: env}}, errSubscriptionIDRequired
	}
	aad := subscriptionSecretAAD(b.WorkspaceID, subscriptionID)
	op, err := s.cipher.Open(stored, aad)
	if err != nil {
		return boundOpen{outer: op}, err
	}
	if b.Mode == SecretEnvelopeStrict && (op.Envelope == spinecrypto.EnvelopePlaintext || op.Envelope == spinecrypto.EnvelopeV1) {
		return boundOpen{outer: op}, ErrLegacySecretRefused
	}
	res := boundOpen{plaintext: op.Plaintext, outer: op}
	switch inner := spinecrypto.ClassifyEnvelope(op.Plaintext); inner {
	case spinecrypto.EnvelopeEmpty, spinecrypto.EnvelopePlaintext:
		return res, nil
	case spinecrypto.EnvelopeV2:
		in, err := s.cipher.Open(op.Plaintext, aad)
		if err == nil {
			switch spinecrypto.ClassifyEnvelope(in.Plaintext) {
			case spinecrypto.EnvelopeEmpty, spinecrypto.EnvelopePlaintext:
				res.plaintext = in.Plaintext
				res.repaired = true
				return res, nil
			}
		}
		return boundOpen{outer: op}, fmt.Errorf("%w (inner value is %s)", ErrNestedEnvelope, inner)
	default:
		return boundOpen{outer: op}, fmt.Errorf("%w (inner value is %s)", ErrNestedEnvelope, inner)
	}
}

// openSubscriptionSecret is the read path of GetSubscription and the
// list methods. With no binding it is the historical decrypt, unchanged.
// With a binding a value that does not open fails the call (TASK-009
// Decision 9: skipping the row would silently drop its events; blanking
// it would send unsigned deliveries or wipe the secret on a
// read-modify-write caller).
func (s *PostgresStore) openSubscriptionSecret(subscriptionID, stored string) (string, error) {
	if s.secretBinding == nil {
		return s.decryptSubscriptionSecret(stored)
	}
	r, err := s.openBound(subscriptionID, stored)
	if err != nil {
		return "", fmt.Errorf("subscription %q (%s): %w", subscriptionID, r.outer.Envelope, err)
	}
	return r.plaintext, nil
}

// sealSubscriptionSecret is the write path of CreateSubscription and
// UpdateSubscription. With no binding it is the historical encrypt,
// unchanged (including its pass-through of values that are already
// envelopes). With a binding:
//   - "" is stored as "";
//   - no cipher → ErrSecretCipherRequired (fail closed);
//   - no subscription id → error;
//   - an envelope input → ErrEnvelopeInput, in every mode. Opening it
//     with this row's associated data and re-sealing would turn a foreign
//     enc:v1 value (no associated data, so it opens anywhere) into this
//     row's enc:v2 (design-check probe X4). No caller in Spine or SMP
//     passes an envelope;
//   - otherwise the plaintext is sealed for the mode.
func (s *PostgresStore) sealSubscriptionSecret(subscriptionID, secret string) (string, error) {
	if s.secretBinding == nil {
		return s.encryptSubscriptionSecret(secret)
	}
	if secret == "" {
		return "", nil
	}
	if s.cipher == nil {
		return "", ErrSecretCipherRequired
	}
	if subscriptionID == "" {
		return "", errSubscriptionIDRequired
	}
	if env := spinecrypto.ClassifyEnvelope(secret); env != spinecrypto.EnvelopePlaintext {
		return "", fmt.Errorf("subscription %q: %w (input is %s)", subscriptionID, ErrEnvelopeInput, env)
	}
	return s.sealTarget(subscriptionID, secret)
}

// boundSecretUnchanged reports whether subscriptionID's stored signing
// secret opens under the binding and mode to exactly secret, in which case
// a bound UpdateSubscription leaves the column untouched. A missing row or
// a stored value that does not open (refused) is "changed": the caller's
// secret then replaces it, as a bound write always did. The comparison is
// constant-time; neither value appears in an error.
func (s *PostgresStore) boundSecretUnchanged(ctx context.Context, subscriptionID, secret string) (bool, error) {
	var stored string
	err := s.pool.QueryRow(ctx,
		`SELECT signing_secret FROM runtime.event_subscriptions WHERE subscription_id = $1`,
		subscriptionID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read subscription %q signing secret: %w", subscriptionID, err)
	}
	r, err := s.openBound(subscriptionID, stored)
	if err != nil {
		// A refused stored value is replaced by the caller's secret; it
		// is not an update error.
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(r.plaintext), []byte(secret)) == 1, nil
}

// ReplaceSubscriptionSecret sets a subscription's signing secret to
// secret WITHOUT reading or opening the stored value, and bumps
// updated_at. It is the repair path for a row whose stored secret is
// refused (sealed for another workspace or row, under a removed key, a
// legacy value in strict mode): GetSubscription fails closed on such a
// row, so a read-modify-write rotation could never replace it. The new
// secret is sealed exactly as UpdateSubscription would seal it (bound:
// the mode's envelope for this row; unbound: the historical path). An
// empty secret is refused — a rotation must leave the row signed — and
// so is an envelope input under a binding (ErrEnvelopeInput). A missing
// subscription is a domain not-found error.
func (s *PostgresStore) ReplaceSubscriptionSecret(ctx context.Context, subscriptionID, secret string) error {
	if secret == "" {
		return errors.New("replacement signing secret is empty")
	}
	sealed, err := s.sealSubscriptionSecret(subscriptionID, secret)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE runtime.event_subscriptions SET signing_secret = $2, updated_at = now() WHERE subscription_id = $1`,
		subscriptionID, sealed)
	if err != nil {
		return err
	}
	return mustAffect(tag, "subscription not found")
}

// sealTarget seals plaintext in the mode's target envelope under the
// primary key. The binding and the cipher must be set.
func (s *PostgresStore) sealTarget(subscriptionID, plaintext string) (string, error) {
	if s.secretBinding.Mode == SecretEnvelopeDowngrade {
		return s.cipher.Encrypt(plaintext)
	}
	return s.cipher.Seal(plaintext, subscriptionSecretAAD(s.secretBinding.WorkspaceID, subscriptionID))
}

// targetEnvelope is the envelope the sweep converts rows to.
func (s *PostgresStore) targetEnvelope() spinecrypto.Envelope {
	if s.secretBinding.Mode == SecretEnvelopeDowngrade {
		return spinecrypto.EnvelopeV1
	}
	return spinecrypto.EnvelopeV2
}

// SecretSweepIssue names a row the sweep refused to convert and left
// untouched. It never carries secret material: Reason is the error text
// of a sentinel, KeyID is a non-secret key id.
type SecretSweepIssue struct {
	SubscriptionID string
	// Envelope is the stored value's envelope.
	Envelope spinecrypto.Envelope
	// KeyID is the ring key that opened the value (a refused legacy
	// value in strict mode), or the well-formed key id an enc:v2 value
	// names; "" otherwise.
	KeyID  string
	Reason string
}

// SecretSweepConversion records one rewritten row. It never carries
// secret material.
type SecretSweepConversion struct {
	SubscriptionID string
	// From is the stored value's envelope before the rewrite.
	From spinecrypto.Envelope
	// FromKeyID is the ring key that opened it ("" for plaintext).
	FromKeyID string
	// Repaired: the row was a nested envelope around its own enc:v2
	// value (I8) and was rewritten with the inner secret.
	Repaired bool
}

// SecretSweepReport summarises one ReencryptSubscriptionSecrets pass.
// Scanned == Rewritten + Current + Empty + Conflicts + len(Refused).
type SecretSweepReport struct {
	// Scanned is the number of rows read.
	Scanned int
	// Rewritten rows were re-sealed to the target envelope under the
	// primary key; one Converted entry each.
	Rewritten int
	// Current rows were already in the target envelope under the
	// primary key; nothing was written.
	Current int
	// Empty rows have no secret.
	Empty int
	// Conflicts are rows whose value changed between the sweep's read
	// and its compare-and-swap write; a concurrent writer won (I5) and
	// the next sweep looks at the row again.
	Conflicts int

	// Breakdown of Rewritten by the stored value's (outer) envelope:
	// FromPlaintext — plaintext; FromV1 — enc:v1 re-sealed to enc:v2;
	// ToV1 — enc:v2 re-sealed to enc:v1 (downgrade); Rekeyed — already
	// the target envelope but under a previous ring key. A repaired row
	// whose outer envelope was already the target under the primary key
	// is counted only in Repaired.
	FromPlaintext int
	FromV1        int
	ToV1          int
	Rekeyed       int
	// Repaired counts nested-envelope rows unwrapped and rewritten (I8).
	Repaired int

	// Converted lists every rewritten row, in subscription id order.
	Converted []SecretSweepConversion
	// Refused lists the rows that did not open under the binding and
	// mode; each was left untouched (I4).
	Refused []SecretSweepIssue
}

// Clean reports whether the pass found nothing left to do: no refused
// row and no lost compare-and-swap.
func (r SecretSweepReport) Clean() bool { return len(r.Refused) == 0 && r.Conflicts == 0 }

// ReencryptSubscriptionSecrets is the re-encryption sweep (P3-1): it
// rewrites every signing_secret that is not yet in the binding's target
// envelope under the primary key — plaintext rows written before a key
// existed, enc:v1 rows, rows sealed under a previous ring key, nested
// rows (I8) — and reports what it found.
//
//  1. It requires a binding (ErrNoSecretBinding) and a cipher
//     (ErrSecretCipherRequired); otherwise it touches nothing.
//  2. It reads every row first and closes the cursor before writing, so
//     it never holds a cursor and an Exec on a small gated pool at once.
//  3. A row that does not open under the binding and mode — a wrong or
//     removed key, a transplant from another workspace or row, a legacy
//     value in strict mode, a foreign nested envelope, an unknown
//     version — goes to Refused and is left untouched (I4).
//  4. Every other row not already current is re-sealed and written with
//     a compare-and-swap on the exact value read; zero rows affected is a
//     Conflict (I5). updated_at is not bumped: the secret is unchanged.
//
// A database error returns the partial report with the error. The pass
// is idempotent: a second run over converted rows writes nothing.
//
// Trust boundary: in migrate mode the sweep trusts the legacy rows
// present when it runs. A copy of another workspace's pre-upgrade enc:v1
// value planted at any time before the strict flip is converted into a
// valid enc:v2 row for this workspace; nothing can distinguish it from a
// genuine row. Every conversion is listed in Converted so it can be
// audited, and the sweep is the only bound path that turns a legacy value
// into enc:v2: a bound UpdateSubscription that does not change the secret
// leaves the stored bytes alone (see UpdateSubscription). Strict mode (or
// retiring every key that sealed enc:v1) closes the path for values
// planted from then on; values converted before stay valid, which is why
// the strict runbook reviews the conversion log first.
func (s *PostgresStore) ReencryptSubscriptionSecrets(ctx context.Context) (SecretSweepReport, error) {
	var rep SecretSweepReport
	if s.secretBinding == nil {
		return rep, ErrNoSecretBinding
	}
	if s.cipher == nil {
		return rep, ErrSecretCipherRequired
	}

	type storedSecret struct{ id, value string }
	rows, err := s.pool.Query(ctx, `SELECT subscription_id, signing_secret FROM runtime.event_subscriptions ORDER BY subscription_id`)
	if err != nil {
		return rep, fmt.Errorf("list subscription secrets: %w", err)
	}
	var all []storedSecret
	for rows.Next() {
		var r storedSecret
		if err := rows.Scan(&r.id, &r.value); err != nil {
			rows.Close()
			return rep, fmt.Errorf("scan subscription secret: %w", err)
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("list subscription secrets: %w", err)
	}

	target := s.targetEnvelope()
	for _, r := range all {
		rep.Scanned++
		op, err := s.openBound(r.id, r.value)
		if err != nil {
			rep.Refused = append(rep.Refused, SecretSweepIssue{
				SubscriptionID: r.id,
				Envelope:       op.outer.Envelope,
				KeyID:          op.outer.KeyID,
				Reason:         err.Error(),
			})
			continue
		}
		if op.outer.Envelope == spinecrypto.EnvelopeEmpty {
			rep.Empty++
			continue
		}
		if op.outer.Envelope == target && op.outer.Primary && !op.repaired {
			rep.Current++
			continue
		}
		sealed, err := s.sealTarget(r.id, op.plaintext)
		if err != nil {
			return rep, fmt.Errorf("re-seal subscription %q: %w", r.id, err)
		}
		tag, err := s.pool.Exec(ctx,
			`UPDATE runtime.event_subscriptions SET signing_secret = $2 WHERE subscription_id = $1 AND signing_secret = $3`,
			r.id, sealed, r.value)
		if err != nil {
			return rep, fmt.Errorf("rewrite subscription %q: %w", r.id, err)
		}
		if tag.RowsAffected() == 0 {
			rep.Conflicts++
			continue
		}
		rep.Rewritten++
		switch from := op.outer.Envelope; {
		case from == spinecrypto.EnvelopePlaintext:
			rep.FromPlaintext++
		case from == target && !op.outer.Primary:
			rep.Rekeyed++
		case from == spinecrypto.EnvelopeV1 && target == spinecrypto.EnvelopeV2:
			rep.FromV1++
		case from == spinecrypto.EnvelopeV2 && target == spinecrypto.EnvelopeV1:
			rep.ToV1++
		}
		if op.repaired {
			rep.Repaired++
		}
		rep.Converted = append(rep.Converted, SecretSweepConversion{
			SubscriptionID: r.id,
			From:           op.outer.Envelope,
			FromKeyID:      op.outer.KeyID,
			Repaired:       op.repaired,
		})
	}
	return rep, nil
}
