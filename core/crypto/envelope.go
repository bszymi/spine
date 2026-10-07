package crypto

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The enc:v2 envelope (INIT-023 TASK-009, audit P3-10).
//
// Wire format:
//
//	enc:v2:<kid>:<base64std( nonce[12] || AES-256-GCM ciphertext || tag[16] )>
//
// where:
//
//   - kid = hex(HKDF-SHA256(ikm=key, salt=nil, info=v2KeyIDInfo, L=8)):
//     16 lowercase hex characters, derived (operators configure no
//     labels), stable, and safe to log.
//   - AEAD key = HKDF-SHA256(ikm=key, salt=nil, info=v2AEADInfo, L=32),
//     so v2 never uses the raw key that v1 seals with.
//   - GCM associated data = v2AADLabel || 0x00 || kid || 0x00 || callerAAD.
//     The version and the key id are authenticated: relabelling a value
//     with another ring key's id fails authentication instead of being
//     tried under that key.
//   - callerAAD is mandatory (ErrMissingAAD) and is built with
//     AssociatedData so field boundaries cannot be confused.
//
// Every string below is part of the wire format. Changing one makes
// every stored enc:v2 value unopenable; TestV2_GoldenVector pins them.
const (
	v2Prefix    = "enc:v2:"
	keyIDHexLen = 16
	v2AEADInfo  = "spine/secret-envelope/v2/aes-256-gcm"
	v2KeyIDInfo = "spine/secret-envelope/v2/key-id"
	v2AADLabel  = "spine/secret-envelope/v2"
	aadLabel    = "spine-aad/1"
)

// Sentinel errors returned by Open, Seal and (for v2 input) Decrypt.
// None of them, nor any error this package wraps them in, carries key
// material, plaintext or any part of the stored value.
var (
	// ErrMissingAAD: Seal or Open of an enc:v2 value without associated data.
	ErrMissingAAD = errors.New("secret envelope v2 requires associated data")
	// ErrUnknownKeyID: the enc:v2 value names a key id that is not in the ring.
	ErrUnknownKeyID = errors.New("secret envelope key id is not in the key ring")
	// ErrEnvelopeAuth: GCM authentication failed — wrong key, wrong
	// associated data (for example a value copied from another
	// workspace or row), or a tampered value.
	ErrEnvelopeAuth = errors.New("secret envelope failed authentication (wrong key, wrong associated data, or tampered value)")
	// ErrMalformedEnvelope: the value has an envelope prefix but not the
	// envelope's shape (bad key id, bad base64, too short).
	ErrMalformedEnvelope = errors.New("malformed secret envelope")
	// ErrUnsupportedEnvelope: a reserved "enc:v<N>:" prefix this build
	// does not implement.
	ErrUnsupportedEnvelope = errors.New("unsupported secret envelope version")
)

// Envelope classifies a stored value without opening it.
type Envelope int

const (
	// EnvelopeEmpty is the empty string.
	EnvelopeEmpty Envelope = iota
	// EnvelopePlaintext is any non-empty value without a reserved
	// "enc:v<N>:" prefix.
	EnvelopePlaintext
	// EnvelopeV1 is an "enc:v1:" value (Encrypt).
	EnvelopeV1
	// EnvelopeV2 is an "enc:v2:" value (Seal).
	EnvelopeV2
	// EnvelopeUnknown is a reserved "enc:v<digits>:" prefix other than
	// v1 and v2.
	EnvelopeUnknown
)

// String returns a short, log-safe name: "empty", "plaintext",
// "enc:v1", "enc:v2" or "unknown".
func (e Envelope) String() string {
	switch e {
	case EnvelopeEmpty:
		return "empty"
	case EnvelopePlaintext:
		return "plaintext"
	case EnvelopeV1:
		return "enc:v1"
	case EnvelopeV2:
		return "enc:v2"
	default:
		return "unknown"
	}
}

// MarshalText renders the String form, so structured logs and JSON show
// "enc:v1" rather than an integer.
func (e Envelope) MarshalText() ([]byte, error) {
	return []byte(e.String()), nil
}

// ClassifyEnvelope reports the envelope of a stored value without
// opening it. "enc:v<digits>:" is reserved: v1 and v2 are recognised,
// any other version is EnvelopeUnknown. Everything else non-empty —
// including near-misses such as "ENC:v2:", "enc:v2" or "enc:value" — is
// EnvelopePlaintext.
func ClassifyEnvelope(v string) Envelope {
	switch {
	case v == "":
		return EnvelopeEmpty
	case strings.HasPrefix(v, ciphertextPrefix):
		return EnvelopeV1
	case strings.HasPrefix(v, v2Prefix):
		return EnvelopeV2
	case isReservedVersion(v):
		return EnvelopeUnknown
	default:
		return EnvelopePlaintext
	}
}

// isReservedVersion matches the prefix "enc:v<one or more digits>:".
func isReservedVersion(v string) bool {
	rest, ok := strings.CutPrefix(v, "enc:v")
	if !ok {
		return false
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	return i > 0 && i < len(rest) && rest[i] == ':'
}

// KeyID returns the non-secret identifier of a key: 16 lowercase hex
// characters, hex(HKDF-SHA256(key, info="spine/secret-envelope/v2/key-id", L=8)).
// It is one-way and safe to log; enc:v2 values carry it so Open knows
// which ring key sealed them.
func KeyID(key []byte) (string, error) {
	if err := checkKeySize(key); err != nil {
		return "", err
	}
	b, err := hkdf.Key(sha256.New, key, nil, v2KeyIDInfo, keyIDHexLen/2)
	if err != nil {
		return "", fmt.Errorf("derive key id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// deriveV2Key derives the enc:v2 AES-256 subkey. The caller clears it.
func deriveV2Key(key []byte) ([]byte, error) {
	sub, err := hkdf.Key(sha256.New, key, nil, v2AEADInfo, EncryptionKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive v2 key: %w", err)
	}
	return sub, nil
}

// AssociatedData builds canonical associated data for Seal and Open:
//
//	"spine-aad/1" || uvarint(len(purpose)) || purpose || (uvarint(len(f)) || f)...
//
// Every field is length-prefixed, so ("ws-A", "1") and ("ws-A1", "")
// encode differently. purpose names what the secret is (for example the
// column it lives in); fields name the scope it is bound to (for example
// the workspace and the row id). The result is never empty.
func AssociatedData(purpose string, fields ...string) []byte {
	n := len(aadLabel) + binary.MaxVarintLen64 + len(purpose)
	for _, f := range fields {
		n += binary.MaxVarintLen64 + len(f)
	}
	b := make([]byte, 0, n)
	b = append(b, aadLabel...)
	b = appendLengthPrefixed(b, purpose)
	for _, f := range fields {
		b = appendLengthPrefixed(b, f)
	}
	return b
}

// appendLengthPrefixed appends uvarint(len(s)) || s. len() is never
// negative, so the int->uint64 conversion cannot wrap (gosec G115 agrees;
// make lint-security is clean without a suppression).
func appendLengthPrefixed(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// v2AAD is the GCM associated data for one enc:v2 value: the envelope
// label and the key id, then the caller's associated data.
func v2AAD(kid string, aad []byte) []byte {
	b := make([]byte, 0, len(v2AADLabel)+1+len(kid)+1+len(aad))
	b = append(b, v2AADLabel...)
	b = append(b, 0)
	b = append(b, kid...)
	b = append(b, 0)
	return append(b, aad...)
}

// Seal encrypts plaintext into an enc:v2 envelope under the primary key,
// binding aad (build it with AssociatedData). The value opens only
// through Open with the same aad and a ring that still holds the key.
// Empty aad returns ErrMissingAAD: an unbound v2 value would be no
// better than v1.
func (c *SecretCipher) Seal(plaintext string, aad []byte) (string, error) {
	if err := c.sealReady(); err != nil {
		return "", err
	}
	if len(aad) == 0 {
		return "", ErrMissingAAD
	}
	return c.sealV2(plaintext, aad, nil)
}

// sealV2 seals under the primary key. A nil nonce draws a random one;
// the golden-vector test passes a fixed nonce to pin the wire format.
func (c *SecretCipher) sealV2(plaintext string, aad, nonce []byte) (string, error) {
	k := c.keys[0]
	if nonce == nil {
		nonce = make([]byte, gcmNonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return "", fmt.Errorf("read nonce: %w", err)
		}
	}
	if len(nonce) != gcmNonceSize {
		return "", fmt.Errorf("nonce must be %d bytes", gcmNonceSize)
	}
	blob := k.v2.Seal(append([]byte(nil), nonce...), nonce, []byte(plaintext), v2AAD(k.id, aad))
	return v2Prefix + k.id + ":" + base64.StdEncoding.EncodeToString(blob), nil
}

// Opened is the result of Open.
type Opened struct {
	// Plaintext is the opened secret ("" for an empty value; the value
	// itself for EnvelopePlaintext).
	Plaintext string
	// Envelope is the stored value's envelope, also set on error.
	Envelope Envelope
	// KeyID is the ring key that opened an enc:v1 or enc:v2 value. On
	// an enc:v2 error it is the well-formed key id the value names
	// (useful to report "sealed under a key not in the ring"); otherwise
	// "" on error.
	KeyID string
	// Primary reports that the primary key opened the value.
	Primary bool
}

// Open reads a value in any supported format and reports what it found.
// It does not apply policy: whether plaintext or enc:v1 is acceptable is
// the caller's decision (see adapters/store's SecretEnvelopeMode).
//
//   - "" → {Envelope: EnvelopeEmpty}, nil.
//   - plaintext → {Plaintext: value, Envelope: EnvelopePlaintext}, nil.
//   - enc:v1 → tries every ring key, primary first; aad is ignored (v1
//     never had any). ErrEnvelopeAuth when no key opens it.
//   - enc:v2 → requires aad (ErrMissingAAD), looks the key up by the
//     value's key id (ErrUnknownKeyID), authenticates the key id, the
//     version and aad (ErrEnvelopeAuth).
//   - a reserved but unknown "enc:v<N>:" → ErrUnsupportedEnvelope.
//   - a bad shape → ErrMalformedEnvelope.
//   - a nil or zero-value cipher with enc:v1 or enc:v2 → ErrNoCipher.
func (c *SecretCipher) Open(value string, aad []byte) (Opened, error) {
	env := ClassifyEnvelope(value)
	switch env {
	case EnvelopeEmpty:
		return Opened{Envelope: env}, nil
	case EnvelopePlaintext:
		return Opened{Plaintext: value, Envelope: env}, nil
	case EnvelopeUnknown:
		return Opened{Envelope: env}, ErrUnsupportedEnvelope
	}
	if err := c.openReady(); err != nil {
		return Opened{Envelope: env}, err
	}
	if env == EnvelopeV1 {
		pt, idx, err := c.openV1(value)
		if err != nil {
			return Opened{Envelope: env}, err
		}
		return Opened{Plaintext: pt, Envelope: env, KeyID: c.keys[idx].id, Primary: idx == 0}, nil
	}
	return c.openV2(value, aad)
}

// openV2 opens an enc:v2 value. c must be ready.
func (c *SecretCipher) openV2(value string, aad []byte) (Opened, error) {
	out := Opened{Envelope: EnvelopeV2}
	if len(aad) == 0 {
		return out, ErrMissingAAD
	}
	kid, payload, ok := strings.Cut(strings.TrimPrefix(value, v2Prefix), ":")
	if !ok || !isKeyID(kid) {
		return out, fmt.Errorf("%w: key id is not %d lowercase hex characters", ErrMalformedEnvelope, keyIDHexLen)
	}
	out.KeyID = kid
	idx := -1
	for i, k := range c.keys {
		if k.id == kid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return out, fmt.Errorf("%w (key id %s)", ErrUnknownKeyID, kid)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	// Only the canonical encoding Seal writes is accepted, so one sealed
	// value has exactly one stored spelling.
	if err != nil || base64.StdEncoding.EncodeToString(raw) != payload {
		return out, fmt.Errorf("%w: payload is not canonical standard base64", ErrMalformedEnvelope)
	}
	if len(raw) < gcmNonceSize+gcmTagSize {
		return out, fmt.Errorf("%w: ciphertext too short", ErrMalformedEnvelope)
	}
	k := c.keys[idx]
	pt, err := k.v2.Open(nil, raw[:gcmNonceSize], raw[gcmNonceSize:], v2AAD(kid, aad))
	if err != nil {
		return out, ErrEnvelopeAuth
	}
	out.Plaintext = string(pt)
	out.Primary = idx == 0
	return out, nil
}

// isKeyID reports whether s has the shape KeyID produces.
func isKeyID(s string) bool {
	if len(s) != keyIDHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
