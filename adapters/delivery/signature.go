package delivery

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook signature versions (INIT-023 TASK-010, audit P3-11).
//
// v1 — the original format, and still the default:
//
//	X-Spine-Signature: sha256=<hex HMAC-SHA256(secret, body)>
//
// The MAC covers the body only, so a captured delivery verifies forever:
// anyone who once sees a signed request (a TLS-terminating proxy log, a
// request-logging middleware, a mirrored receiver) can replay it.
//
// v2 — opt-in per subscription through metadata.signature_version:
//
//	X-Webhook-Timestamp: <decimal Unix seconds at send time>
//	X-Webhook-Nonce:     <32 lowercase hex chars = 16 random bytes>
//	X-Spine-Signature:   v2=<hex HMAC-SHA256(secret, ts + "." + nonce + "." + body)>
//
// This is byte for byte the format SMP's step-event receiver already
// verifies (event_receiver.go hmacMatchesAny/stripSigPrefix, SMP): the
// receiver enforces a ±5-minute timestamp window and rejects a reused
// nonce, and both values are inside the MAC, so a replay is refused and a
// forged fresh header pair fails verification. The pair is generated per
// attempt and never persisted, so a legitimate retry (or an operator
// replay / SMP redrive of the same row) is a fresh request, never a
// replay. v2 sends ONLY the v2 MAC: SMP rejects a v1 MAC whenever the v2
// headers are present, and one header cannot carry both.
const (
	SignatureVersionV1 = "v1"
	SignatureVersionV2 = "v2"

	// MetadataKeySignatureVersion is the event_subscriptions.metadata
	// key that opts a subscription into a signature version. Absent,
	// null, "" and "v1" mean v1; "v2" means v2; anything else is a
	// permanent configuration error (ErrInvalidSubscriptionMetadata).
	MetadataKeySignatureVersion = "signature_version"

	HeaderSignature = "X-Spine-Signature"
	HeaderTimestamp = "X-Webhook-Timestamp"
	HeaderNonce     = "X-Webhook-Nonce"

	// signatureNonceBytes is the v2 nonce length before hex encoding.
	// SMP hex-decodes the header and stores the raw bytes (BYTEA), so 16
	// random bytes = 32 hex characters.
	signatureNonceBytes = 16
)

// ErrInvalidSubscriptionMetadata marks a permanent subscription
// configuration error: metadata that is not a JSON object, an unknown or
// non-string signature_version, a malformed tls block. The dispatcher
// dead-letters a delivery whose subscription fails with it (retrying
// cannot fix configuration), and the gateway answers 400 for it.
var ErrInvalidSubscriptionMetadata = errors.New("invalid subscription metadata")

// errV2WithoutSecret: a v2 opt-in with no signing secret is refused
// rather than sent unsigned. Falling back to an unsigned (or v1) request
// would silently defeat an operator's explicit request for replay
// protection (TASK-010 Decision 5).
var errV2WithoutSecret = errors.New("signature_version v2 requires a signing secret")

// subscriptionMetadata is the parsed, validated form of
// event_subscriptions.metadata that the dispatcher acts on.
type subscriptionMetadata struct {
	// TLS is nil unless the tls block sets at least one field.
	TLS *SubscriptionTLSConfig
	// SignatureVersion is normalised: always SignatureVersionV1 or
	// SignatureVersionV2.
	SignatureVersion string
}

// parseMetadataObject decodes metadata into its top-level keys. Empty,
// whitespace-only and JSON null metadata are an empty object (a NULL or
// '{}' column both mean "no options"). Anything else must be a JSON
// object.
func parseMetadataObject(metadata []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(metadata)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalidSubscriptionMetadata)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidSubscriptionMetadata, err)
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj, nil
}

// signatureVersionFromObject reads and normalises signature_version.
//
// The key is matched exactly. A case variant ("Signature_Version",
// "SIGNATURE_VERSION", ...) is refused rather than ignored: encoding/json
// matches struct tags case-insensitively, so a reader that decoded the
// metadata into a struct would see the variant while this one would not.
// Refusing it keeps every reader of the column agreeing on one value, and
// an operator who mistyped the key gets a 400 instead of a silent v1.
func signatureVersionFromObject(obj map[string]json.RawMessage) (string, error) {
	for k := range obj {
		if k != MetadataKeySignatureVersion && strings.EqualFold(k, MetadataKeySignatureVersion) {
			return "", fmt.Errorf("%w: key %q must be spelled %q", ErrInvalidSubscriptionMetadata, k, MetadataKeySignatureVersion)
		}
	}
	raw, ok := obj[MetadataKeySignatureVersion]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return SignatureVersionV1, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("%w: signature_version must be a string", ErrInvalidSubscriptionMetadata)
	}
	return normaliseSignatureVersion(v)
}

// normaliseSignatureVersion maps the accepted spellings onto the two
// versions and refuses everything else. Matching is exact: "V2" is not
// "v2".
func normaliseSignatureVersion(v string) (string, error) {
	switch v {
	case "", SignatureVersionV1:
		return SignatureVersionV1, nil
	case SignatureVersionV2:
		return SignatureVersionV2, nil
	}
	return "", fmt.Errorf("%w: unknown signature_version %q (want %q or %q)",
		ErrInvalidSubscriptionMetadata, v, SignatureVersionV1, SignatureVersionV2)
}

// parseSubscriptionMetadata parses everything the dispatcher reads from a
// subscription's metadata: the tls block and the signature version. Every
// error wraps ErrInvalidSubscriptionMetadata.
//
// The tls block is decoded through the same struct as before this task,
// so its semantics (including encoding/json's case-insensitive field
// matching) are unchanged for existing rows.
func parseSubscriptionMetadata(metadata []byte) (subscriptionMetadata, error) {
	out := subscriptionMetadata{SignatureVersion: SignatureVersionV1}
	obj, err := parseMetadataObject(metadata)
	if err != nil {
		return out, err
	}
	if out.SignatureVersion, err = signatureVersionFromObject(obj); err != nil {
		return out, err
	}
	if len(obj) == 0 {
		return out, nil
	}
	var wrapper struct {
		TLS *SubscriptionTLSConfig `json:"tls"`
	}
	if err := json.Unmarshal(metadata, &wrapper); err != nil {
		return out, fmt.Errorf("%w: parse: %v", ErrInvalidSubscriptionMetadata, err)
	}
	tlsCfg := wrapper.TLS
	if tlsCfg == nil || (tlsCfg.PinnedSPKISHA256 == "" && tlsCfg.CABundlePEM == "") {
		return out, nil
	}
	if tlsCfg.PinnedSPKISHA256 != "" {
		if _, err := hex.DecodeString(tlsCfg.PinnedSPKISHA256); err != nil {
			return out, fmt.Errorf("%w: pinned_spki_sha256 is not valid hex: %v", ErrInvalidSubscriptionMetadata, err)
		}
	}
	out.TLS = tlsCfg
	return out, nil
}

// ValidateSubscriptionMetadata reports whether metadata is acceptable for
// a webhook subscription: nil, or an error wrapping
// ErrInvalidSubscriptionMetadata. It applies the dispatcher's own parse
// (so whatever it accepts will not dead-letter as misconfigured) plus one
// create-time check the dispatcher only discovers at send time: a
// ca_bundle_pem that holds no certificate.
func ValidateSubscriptionMetadata(metadata []byte) error {
	md, err := parseSubscriptionMetadata(metadata)
	if err != nil {
		return err
	}
	if md.TLS != nil && md.TLS.CABundlePEM != "" {
		if ok := x509.NewCertPool().AppendCertsFromPEM([]byte(md.TLS.CABundlePEM)); !ok {
			return fmt.Errorf("%w: no certificates parsed from tls.ca_bundle_pem", ErrInvalidSubscriptionMetadata)
		}
	}
	return nil
}

// SignatureVersionOf returns the normalised signature version a
// subscription's metadata selects: SignatureVersionV1 when the key is
// absent, null or empty, SignatureVersionV2 when it is "v2". Malformed
// metadata or an unknown version is an error wrapping
// ErrInvalidSubscriptionMetadata. Only the object shape and the version
// key are checked; use ValidateSubscriptionMetadata for the whole value.
func SignatureVersionOf(metadata []byte) (string, error) {
	obj, err := parseMetadataObject(metadata)
	if err != nil {
		return "", err
	}
	return signatureVersionFromObject(obj)
}

// WithSignatureVersion returns metadata with signature_version set to
// version ("v1" or "v2") and every other top-level key preserved
// verbatim (source, tls, anything an operator added). Case variants of
// the key are removed so the result parses. It fails when version is not
// v1/v2 or when metadata is not a JSON object (empty and null count as an
// empty object); the result is compact JSON with keys in sorted order.
func WithSignatureVersion(metadata []byte, version string) ([]byte, error) {
	if version != SignatureVersionV1 && version != SignatureVersionV2 {
		return nil, fmt.Errorf("%w: signature_version must be %q or %q, got %q",
			ErrInvalidSubscriptionMetadata, SignatureVersionV1, SignatureVersionV2, version)
	}
	obj, err := parseMetadataObject(metadata)
	if err != nil {
		return nil, err
	}
	for k := range obj {
		if strings.EqualFold(k, MetadataKeySignatureVersion) {
			delete(obj, k)
		}
	}
	v, err := json.Marshal(version)
	if err != nil {
		return nil, err
	}
	obj[MetadataKeySignatureVersion] = v
	return json.Marshal(obj)
}

// SignV1 returns the v1 X-Spine-Signature value:
// "sha256=" + hex(HMAC-SHA256(secret, body)).
func SignV1(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SignV2 returns the v2 X-Spine-Signature value:
// "v2=" + hex(HMAC-SHA256(secret, timestamp + "." + nonce + "." + body)).
// timestamp and nonce are the exact header strings sent with the request.
func SignV2(secret, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write([]byte(nonce))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "v2=" + hex.EncodeToString(mac.Sum(nil))
}

// newSignatureNonce reads signatureNonceBytes from r and hex-encodes them.
// io.ReadFull, not Read: a short read must be an error, never a shorter
// (weaker) nonce.
func newSignatureNonce(r io.Reader) (string, error) {
	b := make([]byte, signatureNonceBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// signRequest sets the signature headers for one delivery attempt. The
// dispatcher calls it immediately before client.Do, so the v2 timestamp
// is send time and every attempt gets a fresh nonce.
//
//   - v1 ("" or "v1"): X-Spine-Signature: sha256=… when secret != "";
//     no signature at all without a secret (unchanged behaviour).
//   - v2: timestamp, nonce and the v2 MAC. No secret is errV2WithoutSecret
//     (permanent); a nonce read failure is a transient error.
//   - anything else: an error wrapping ErrInvalidSubscriptionMetadata
//     (permanent). A custom SubscriptionResolver can hand the dispatcher
//     any string; an unknown one must not fall back to v1 or unsigned.
func signRequest(h http.Header, version, secret string, body []byte, now time.Time, rnd io.Reader) error {
	h.Del(HeaderSignature)
	h.Del(HeaderTimestamp)
	h.Del(HeaderNonce)
	v, err := normaliseSignatureVersion(version)
	if err != nil {
		return err
	}
	if v == SignatureVersionV1 {
		if secret != "" {
			h.Set(HeaderSignature, SignV1(secret, body))
		}
		return nil
	}
	if secret == "" {
		return errV2WithoutSecret
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	nonce, err := newSignatureNonce(rnd)
	if err != nil {
		return fmt.Errorf("signature nonce: %w", err)
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	h.Set(HeaderTimestamp, ts)
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, SignV2(secret, ts, nonce, body))
	return nil
}
