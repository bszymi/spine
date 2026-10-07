// Package crypto provides symmetric encryption for secrets persisted
// at rest. The threat model is a database compromise: an attacker who
// exfiltrates `event_subscriptions` rows must not be able to forge
// webhook signatures without also compromising the separately-stored
// encryption key (SPINE_SECRET_ENCRYPTION_KEY).
//
// Two envelopes exist (INIT-023 TASK-009, audit P3-10):
//
//   - enc:v1 (Encrypt / Decrypt): AES-256-GCM under the raw key with no
//     associated data. A v1 value is bound to nothing, so a copy of one
//     row opens anywhere the same key is configured. It is kept for
//     callers that have no binding (embedders before they adopt v2, the
//     legacy store path, the downgrade/rollback mode).
//   - enc:v2 (Seal / Open, envelope.go): AES-256-GCM under an HKDF
//     subkey, naming the key that sealed it and authenticating the
//     caller's associated data (AssociatedData), so a value only opens
//     for the scope it was sealed for.
//
// A SecretCipher is a key ring: the first key seals, every key opens.
// That is what makes rotation possible without a manual
// decrypt-everything step (see docs/integration-guide.md §2.1).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// EncryptionKeySize is the required AES-256 key size in bytes.
const EncryptionKeySize = 32

// ciphertextPrefix tags enc:v1 wire-format ciphertext so Decrypt can
// distinguish encrypted blobs from legacy plaintext rows.
const ciphertextPrefix = "enc:v1:"

// gcmNonceSize and gcmTagSize are the standard AES-GCM sizes every ring
// key uses (cipher.NewGCM). Fixing them lets a v1 value be length-checked
// once before it is tried against each ring key.
const (
	gcmNonceSize = 12
	gcmTagSize   = 16
)

// ErrNoCipher is returned when a value carries an encryption envelope
// but no usable cipher is configured. The message is the one Decrypt has
// always returned for that case.
var ErrNoCipher = errors.New("encrypted value found but cipher is not configured")

// errEmptyRing is returned by every method of a zero-value SecretCipher
// (a SecretCipher{} literal rather than one built by NewSecretCipher or
// NewSecretCipherRing). Methods return it instead of indexing an empty
// ring and panicking.
var errEmptyRing = errors.New("secret cipher has no keys; construct it with NewSecretCipher or NewSecretCipherRing")

// errNilCipher is the historical Encrypt-on-nil message, kept verbatim.
var errNilCipher = errors.New("nil cipher")

// ringKey is one key of the ring. Only the derived AEADs and the
// non-secret key id are kept: the raw key bytes are not retained after
// construction (the AES key schedule inside each AEAD is unavoidable).
type ringKey struct {
	id string
	v1 cipher.AEAD // raw key, nil associated data: the enc:v1 envelope
	v2 cipher.AEAD // HKDF subkey (v2AEADInfo): the enc:v2 envelope
}

// SecretCipher is an AES-256-GCM key ring for small at-rest secrets.
// keys[0] is the primary key: Encrypt and Seal always use it. Every key
// in the ring can open (Decrypt, Open), which is what lets an operator
// rotate: new primary, old key kept as "previous" until every value has
// been re-sealed under the primary.
//
// The zero value is not usable: every method returns an error (no
// panic), PrimaryKeyID returns "" and KeyIDs returns nil. Construct it
// with NewSecretCipher or NewSecretCipherRing. A built SecretCipher is
// immutable and safe for concurrent use.
type SecretCipher struct {
	keys []ringKey
}

// keyIDFunc derives a key id. It is KeyID in production; it is a
// variable only so a test can force two different keys onto one id and
// prove NewSecretCipherRing refuses the collision.
var keyIDFunc = KeyID

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return aead, nil
}

func checkKeySize(key []byte) error {
	if len(key) != EncryptionKeySize {
		return fmt.Errorf("secret encryption key must be %d bytes, got %d", EncryptionKeySize, len(key))
	}
	return nil
}

// newRingKey derives one ring entry. Errors never include key bytes.
func newRingKey(key []byte) (ringKey, error) {
	if err := checkKeySize(key); err != nil {
		return ringKey{}, err
	}
	v1, err := newGCM(key)
	if err != nil {
		return ringKey{}, err
	}
	sub, err := deriveV2Key(key)
	if err != nil {
		return ringKey{}, err
	}
	v2, err := newGCM(sub)
	clear(sub)
	if err != nil {
		return ringKey{}, err
	}
	id, err := keyIDFunc(key)
	if err != nil {
		return ringKey{}, err
	}
	return ringKey{id: id, v1: v1, v2: v2}, nil
}

// NewSecretCipher builds a cipher from a 32-byte key: a ring of one key.
// Its signature and its enc:v1 output are unchanged from before the key
// ring existed.
func NewSecretCipher(key []byte) (*SecretCipher, error) {
	k, err := newRingKey(key)
	if err != nil {
		return nil, err
	}
	return &SecretCipher{keys: []ringKey{k}}, nil
}

// NewSecretCipherRing builds a key ring. primary seals; primary and every
// previous key open. Every key must be EncryptionKeySize bytes.
//
//   - A previous key identical to the primary or to an earlier previous
//     key is dropped (an operator listing the primary again mid-rotation
//     is harmless).
//   - Two different keys with the same key id are refused: the id is how
//     Open picks the key for an enc:v2 value, so a collision would make a
//     value's key ambiguous. KeyID is a 64-bit HKDF output, so this only
//     guards against the astronomically unlikely and against bugs.
//
// Errors name the key's position ("primary key", "previous key N",
// 1-based), never its bytes. The input slices are compared only while
// this function runs; they are not retained.
func NewSecretCipherRing(primary []byte, previous ...[]byte) (*SecretCipher, error) {
	pk, err := newRingKey(primary)
	if err != nil {
		return nil, fmt.Errorf("primary key: %w", err)
	}
	c := &SecretCipher{keys: []ringKey{pk}}
	accepted := [][]byte{primary}
	for i, k := range previous {
		pos := i + 1
		if err := checkKeySize(k); err != nil {
			return nil, fmt.Errorf("previous key %d: %w", pos, err)
		}
		dup := false
		for _, have := range accepted {
			if subtle.ConstantTimeCompare(have, k) == 1 {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		rk, err := newRingKey(k)
		if err != nil {
			return nil, fmt.Errorf("previous key %d: %w", pos, err)
		}
		for _, have := range c.keys {
			if have.id == rk.id {
				return nil, fmt.Errorf("previous key %d: key id %s collides with a different key already in the ring", pos, rk.id)
			}
		}
		c.keys = append(c.keys, rk)
		accepted = append(accepted, k)
	}
	return c, nil
}

// PrimaryKeyID returns the id of the sealing key, or "" for a nil or
// zero-value cipher. Safe to log.
func (c *SecretCipher) PrimaryKeyID() string {
	if c == nil || len(c.keys) == 0 {
		return ""
	}
	return c.keys[0].id
}

// KeyIDs returns the ring's key ids, primary first, or nil for a nil or
// zero-value cipher. Safe to log.
func (c *SecretCipher) KeyIDs() []string {
	if c == nil || len(c.keys) == 0 {
		return nil
	}
	out := make([]string, len(c.keys))
	for i, k := range c.keys {
		out[i] = k.id
	}
	return out
}

// sealReady reports why c cannot seal, or nil.
func (c *SecretCipher) sealReady() error {
	if c == nil {
		return errNilCipher
	}
	if len(c.keys) == 0 {
		return errEmptyRing
	}
	return nil
}

// openReady reports why c cannot open an envelope, or nil. Both cases
// match ErrNoCipher.
func (c *SecretCipher) openReady() error {
	if c == nil {
		return ErrNoCipher
	}
	if len(c.keys) == 0 {
		return fmt.Errorf("%w: %w", ErrNoCipher, errEmptyRing)
	}
	return nil
}

// ParseEncryptionKey decodes a base64-encoded key and verifies length.
// Accepts both standard and URL-safe base64.
func ParseEncryptionKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("encryption key is empty")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		key, err = base64.URLEncoding.DecodeString(encoded)
	}
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode base64 key: %w", err)
	}
	if len(key) != EncryptionKeySize {
		return nil, fmt.Errorf("decoded key is %d bytes, expected %d", len(key), EncryptionKeySize)
	}
	return key, nil
}

// Encrypt seals plaintext in the legacy enc:v1 envelope under the
// primary key, with a fresh random nonce, and returns a prefixed,
// base64-encoded ciphertext safe for TEXT columns.
//
// enc:v1 binds no associated data: the value opens wherever the key is
// configured (audit P3-10). New callers should use Seal, which binds the
// value to its scope. Encrypt remains for unbound stores and for the
// downgrade (rollback-preparation) mode.
func (c *SecretCipher) Encrypt(plaintext string) (string, error) {
	if err := c.sealReady(); err != nil {
		return "", err
	}
	k := c.keys[0]
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	blob := k.v1.Seal(nonce, nonce, []byte(plaintext), nil)
	return ciphertextPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt is the legacy reader. Values with no envelope — plaintext,
// empty, or an unrecognised "enc:v<N>:" prefix — are returned verbatim,
// exactly as before the key ring existed, so pre-encryption rows keep
// working on unbound stores. An enc:v1 value is opened with every ring
// key, primary first.
//
// An enc:v2 value is an error wrapping ErrMissingAAD: it can only be
// opened with its associated data (Open). Before TASK-009 Decrypt
// returned such a value verbatim, as if it were plaintext, and a caller
// would have used the envelope text itself as the secret.
//
// New callers should use Open, which reports which envelope it found and
// leaves the policy to the caller.
func (c *SecretCipher) Decrypt(value string) (string, error) {
	switch ClassifyEnvelope(value) {
	case EnvelopeV1:
		if err := c.openReady(); err != nil {
			return "", err
		}
		pt, _, err := c.openV1(value)
		return pt, err
	case EnvelopeV2:
		if err := c.openReady(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("decrypt: %w (an enc:v2 value opens only through Open with its associated data)", ErrMissingAAD)
	default:
		return value, nil
	}
}

// openV1 opens an enc:v1 value with each ring key in order and returns
// the plaintext and the index of the key that opened it. c must be
// ready (openReady). Errors carry no part of the value.
func (c *SecretCipher) openV1(value string) (string, int, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, ciphertextPrefix))
	if err != nil {
		return "", -1, fmt.Errorf("%w: decode ciphertext: %w", ErrMalformedEnvelope, err)
	}
	if len(raw) < gcmNonceSize+gcmTagSize {
		return "", -1, fmt.Errorf("%w: ciphertext too short", ErrMalformedEnvelope)
	}
	nonce, sealed := raw[:gcmNonceSize], raw[gcmNonceSize:]
	for i, k := range c.keys {
		pt, err := k.v1.Open(nil, nonce, sealed, nil)
		if err == nil {
			return string(pt), i, nil
		}
	}
	return "", -1, fmt.Errorf("decrypt: %w", ErrEnvelopeAuth)
}

// IsEncrypted reports whether value carries a recognised encryption
// envelope (enc:v1 or enc:v2). Unbound storage callers use it to avoid
// encrypting a value twice.
func IsEncrypted(value string) bool {
	switch ClassifyEnvelope(value) {
	case EnvelopeV1, EnvelopeV2:
		return true
	default:
		return false
	}
}
