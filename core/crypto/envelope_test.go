package crypto_test

// INIT-023 TASK-009 (audit P3-10): the enc:v2 envelope and the key ring.
//
// TestV2_TransplantAcrossWorkspacesFails and
// TestV2_SwapAcrossSubscriptionsFails are the committed ports of the
// 2026-10-06 audit PoCs poc/crypto-secrets-auth/cipher_transplant_test.go
// and poc/gap1-crypto-secrets-reaud/crypto_poc_test.go
// (TestPoC_CiphertextSwapAcrossRowsDecrypts). The PoCs exercise the
// legacy enc:v1 API, which keeps its AAD-less behaviour on purpose; the
// ports exercise the enc:v2 path that a bound store now uses.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	spinecrypto "github.com/bszymi/spine/core/crypto"
)

const purpose = "runtime.event_subscriptions.signing_secret"

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, spinecrypto.EncryptionKeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func ring(t *testing.T, primary []byte, prev ...[]byte) *spinecrypto.SecretCipher {
	t.Helper()
	c, err := spinecrypto.NewSecretCipherRing(primary, prev...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seal(t *testing.T, c *spinecrypto.SecretCipher, pt string, aad []byte) string {
	t.Helper()
	s, err := c.Seal(pt, aad)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Golden vector. The committed envelope below was computed independently
// of this package (Python `cryptography`: HKDF-SHA256 + AESGCM over the
// documented labels), then confirmed against Seal with the same nonce
// (TestV2_GoldenVectorSeal in the internal test file). Changing either
// HKDF info string, the GCM-AAD label, the AssociatedData encoding or
// the wire layout makes this test fail.
var (
	goldenKey       = sequentialBytes(0x00, 32) // 00 01 02 … 1f
	goldenKeyID     = "ada7649825e43683"
	goldenPlaintext = "whsec_golden-vector"
	goldenEnvelope  = "enc:v2:ada7649825e43683:oKGio6SlpqeoqaqrRppE7umWPTjFfwvZG7yc9mLvWM0A+GGZQBqEEVv5aZQzvmc="
	goldenAADHex    = "7370696e652d6161642f312a72756e74696d652e6576656e745f737562736372697074696f6e732e7369676e696e675f7365637265740977732d676f6c64656e0a7375622d676f6c64656e"
)

var v2Shape = regexp.MustCompile(`^enc:v2:[0-9a-f]{16}:[A-Za-z0-9+/]+=*$`)

func sequentialBytes(start byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func TestV2_GoldenVector(t *testing.T) {
	id, err := spinecrypto.KeyID(goldenKey)
	if err != nil || id != goldenKeyID {
		t.Fatalf("KeyID(golden) = %q, %v; want %q", id, err, goldenKeyID)
	}
	aad := spinecrypto.AssociatedData(purpose, "ws-golden", "sub-golden")
	if got := hexString(aad); got != goldenAADHex {
		t.Fatalf("AssociatedData encoding changed:\n got %s\nwant %s", got, goldenAADHex)
	}
	if !v2Shape.MatchString(goldenEnvelope) {
		t.Fatalf("golden envelope does not match the documented shape")
	}
	c := ring(t, goldenKey)
	got, err := c.Open(goldenEnvelope, aad)
	if err != nil {
		t.Fatalf("golden envelope no longer opens: %v", err)
	}
	if got.Plaintext != goldenPlaintext || got.Envelope != spinecrypto.EnvelopeV2 || got.KeyID != goldenKeyID || !got.Primary {
		t.Fatalf("golden open = %+v", got)
	}
	// A fresh Seal has the documented shape and names the key.
	fresh := seal(t, c, "x", aad)
	if !v2Shape.MatchString(fresh) || !strings.HasPrefix(fresh, "enc:v2:"+goldenKeyID+":") {
		t.Fatalf("Seal output %q does not match the documented shape", fresh)
	}
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&0x0f])
	}
	return string(out)
}

// Port of TestPoC_CiphertextTransplantAcrossWorkspaces: one
// deployment-wide key (SMP shares one cipher across every workspace
// runtime DB), workspace A's sealed secret pasted into workspace B's row
// under the same subscription id (DB-per-workspace makes that possible).
func TestV2_TransplantAcrossWorkspacesFails(t *testing.T) {
	c := ring(t, bytes.Repeat([]byte{0x42}, 32))
	const secretA = "whsec_workspace_A_per_workspace_credential"
	rowA := seal(t, c, secretA, spinecrypto.AssociatedData(purpose, "ws-A", "sub-1"))

	_, err := c.Open(rowA, spinecrypto.AssociatedData(purpose, "ws-B", "sub-1"))
	if !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("transplanted v2 ciphertext opened in another workspace: err=%v", err)
	}
	got, err := c.Open(rowA, spinecrypto.AssociatedData(purpose, "ws-A", "sub-1"))
	if err != nil || got.Plaintext != secretA || got.Envelope != spinecrypto.EnvelopeV2 || !got.Primary {
		t.Fatalf("own row did not open: %+v %v", got, err)
	}
}

// Port of TestPoC_CiphertextSwapAcrossRowsDecrypts: two rows of one
// workspace.
func TestV2_SwapAcrossSubscriptionsFails(t *testing.T) {
	c := ring(t, randomKey(t))
	ct := seal(t, c, "secret-A", spinecrypto.AssociatedData(purpose, "ws-X", "sub-A"))
	if _, err := c.Open(ct, spinecrypto.AssociatedData(purpose, "ws-X", "sub-B")); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("v2 ciphertext opened for another subscription: %v", err)
	}
	if got, err := c.Open(ct, spinecrypto.AssociatedData(purpose, "ws-X", "sub-A")); err != nil || got.Plaintext != "secret-A" {
		t.Fatalf("own row did not open: %+v %v", got, err)
	}
}

func TestAssociatedData_Canonical(t *testing.T) {
	pairs := [][2][]byte{
		{spinecrypto.AssociatedData(purpose, "ws-A", "1"), spinecrypto.AssociatedData(purpose, "ws-A1", "")},
		{spinecrypto.AssociatedData(purpose, "ws-A", ""), spinecrypto.AssociatedData(purpose, "ws-A")},
		{spinecrypto.AssociatedData("p", "q"), spinecrypto.AssociatedData("pq")},
		{spinecrypto.AssociatedData(purpose, "a\x00b"), spinecrypto.AssociatedData(purpose, "a", "b")},
	}
	for i, p := range pairs {
		if bytes.Equal(p[0], p[1]) {
			t.Errorf("pair %d: associated data is ambiguous across field boundaries", i)
		}
	}
	if len(spinecrypto.AssociatedData("")) == 0 {
		t.Error("AssociatedData must never be empty")
	}
	// Long fields use a multi-byte uvarint length and still round-trip.
	long := strings.Repeat("w", 300)
	c := ring(t, randomKey(t))
	ct := seal(t, c, "s", spinecrypto.AssociatedData(purpose, long))
	if _, err := c.Open(ct, spinecrypto.AssociatedData(purpose, long+"x")); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("long-field AAD not bound: %v", err)
	}
}

func TestV2_RequiresAAD(t *testing.T) {
	c := ring(t, randomKey(t))
	if _, err := c.Seal("x", nil); !errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("Seal without AAD: %v", err)
	}
	if _, err := c.Seal("x", []byte{}); !errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("Seal with empty AAD: %v", err)
	}
	ct := seal(t, c, "x", spinecrypto.AssociatedData(purpose, "w", "s"))
	if _, err := c.Open(ct, nil); !errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("Open v2 without AAD: %v", err)
	}
	// The legacy API must not pass a v2 envelope through as "plaintext"
	// (at 61994fe it did, and the envelope text became the HMAC key).
	got, err := c.Decrypt(ct)
	if err == nil || got != "" {
		t.Fatalf("legacy Decrypt returned a v2 envelope: %q, %v", got, err)
	}
	if !errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("Decrypt(v2) error should wrap ErrMissingAAD: %v", err)
	}
	var nilCipher *spinecrypto.SecretCipher
	if _, err := nilCipher.Decrypt(ct); !errors.Is(err, spinecrypto.ErrNoCipher) {
		t.Fatalf("nil Decrypt(v2): %v", err)
	}
	// A zero-value cipher reports ErrNoCipher too, not ErrMissingAAD
	// (documented in the CHANGELOG's embedder notes).
	var zero spinecrypto.SecretCipher
	if _, err := zero.Decrypt(ct); !errors.Is(err, spinecrypto.ErrNoCipher) || errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("zero-value Decrypt(v2): %v", err)
	}
}

// The legacy v1 surface stays as it was at 61994fe for SMP's current
// callers: Encrypt emits enc:v1, Decrypt passes plaintext and unknown
// prefixes through.
func TestLegacy_EncryptStillV1AndDecryptPassesThrough(t *testing.T) {
	c := ring(t, randomKey(t))
	ct, err := c.Encrypt("p")
	if err != nil || !strings.HasPrefix(ct, "enc:v1:") {
		t.Fatalf("Encrypt no longer emits enc:v1: %q %v", ct, err)
	}
	if got, err := c.Decrypt(ct); err != nil || got != "p" {
		t.Fatalf("v1 roundtrip: %q %v", got, err)
	}
	for _, v := range []string{"", "legacy-plaintext", "enc:v9:opaque", "enc:value"} {
		if got, err := c.Decrypt(v); err != nil || got != v {
			t.Fatalf("Decrypt(%q) pass-through changed: %q %v", v, got, err)
		}
	}
	if !spinecrypto.IsEncrypted(ct) || !spinecrypto.IsEncrypted(seal(t, c, "p", []byte("a"))) {
		t.Fatal("IsEncrypted must recognise enc:v1 and enc:v2")
	}
	if spinecrypto.IsEncrypted("enc:v9:x") || spinecrypto.IsEncrypted("plain") {
		t.Fatal("IsEncrypted must not recognise unknown versions or plaintext")
	}
	single, err := spinecrypto.NewSecretCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewSecretCipher(all-zero) must keep working for SMP tests: %v", err)
	}
	if len(single.KeyIDs()) != 1 || single.PrimaryKeyID() == "" {
		t.Fatalf("one-key ring: %v", single.KeyIDs())
	}
	// Wrong key on v1 is an authentication failure, still an error.
	other := ring(t, randomKey(t))
	if _, err := other.Decrypt(ct); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("wrong-key v1: %v", err)
	}
	// A short or undecodable v1 value is still an error; it now wraps
	// ErrMalformedEnvelope (documented in the CHANGELOG's embedder notes).
	for _, v := range []string{"enc:v1:AAAA", "enc:v1:!!not-base64!!"} {
		if _, err := c.Decrypt(v); !errors.Is(err, spinecrypto.ErrMalformedEnvelope) {
			t.Fatalf("Decrypt(%q) should wrap ErrMalformedEnvelope: %v", v, err)
		}
	}
}

func TestClassifyEnvelope(t *testing.T) {
	cases := []struct {
		in   string
		want spinecrypto.Envelope
	}{
		{"", spinecrypto.EnvelopeEmpty},
		{"whsec_x", spinecrypto.EnvelopePlaintext},
		{"enc:v1:AAAA", spinecrypto.EnvelopeV1},
		{"enc:v2:0123456789abcdef:AAAA", spinecrypto.EnvelopeV2},
		{"enc:v9:AAAA", spinecrypto.EnvelopeUnknown},
		{"enc:v10:AAAA", spinecrypto.EnvelopeUnknown},
		{"enc:value", spinecrypto.EnvelopePlaintext},
		{"enc:v:AAAA", spinecrypto.EnvelopePlaintext},
		{"enc:v2", spinecrypto.EnvelopePlaintext},
		{"ENC:v2:x", spinecrypto.EnvelopePlaintext},
		{" enc:v2:x", spinecrypto.EnvelopePlaintext},
	}
	for _, tc := range cases {
		if got := spinecrypto.ClassifyEnvelope(tc.in); got != tc.want {
			t.Errorf("ClassifyEnvelope(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if s, _ := spinecrypto.EnvelopeV1.MarshalText(); string(s) != "enc:v1" {
		t.Errorf("MarshalText = %q", s)
	}
}

func TestOpen_FormatTable(t *testing.T) {
	c := ring(t, randomKey(t))
	aad := spinecrypto.AssociatedData(purpose, "w", "s")
	v2 := seal(t, c, "s2", aad)
	kid := c.PrimaryKeyID()
	payload := v2[len("enc:v2:")+17:]
	cases := []struct {
		name    string
		in      string
		wantErr error
		wantEnv spinecrypto.Envelope
		wantPT  string
	}{
		{"empty", "", nil, spinecrypto.EnvelopeEmpty, ""},
		{"plaintext", "whsec_plain", nil, spinecrypto.EnvelopePlaintext, "whsec_plain"},
		{"v2", v2, nil, spinecrypto.EnvelopeV2, "s2"},
		{"unknown version", "enc:v9:AAAA", spinecrypto.ErrUnsupportedEnvelope, spinecrypto.EnvelopeUnknown, ""},
		{"v2 no payload separator", "enc:v2:" + kid, spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v2 short kid", "enc:v2:zz:" + payload, spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v2 uppercase kid", "enc:v2:" + strings.ToUpper(kid) + ":" + payload, spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v2 unknown kid", "enc:v2:0000000000000000:" + payload, spinecrypto.ErrUnknownKeyID, spinecrypto.EnvelopeV2, ""},
		{"v2 bad base64", "enc:v2:" + kid + ":!!!!", spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v2 non-canonical base64", "enc:v2:" + kid + ":" + payload + "\n", spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v2 too short", "enc:v2:" + kid + ":AAAA", spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV2, ""},
		{"v1 bad base64", "enc:v1:!!!!", spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV1, ""},
		{"v1 too short", "enc:v1:AAAA", spinecrypto.ErrMalformedEnvelope, spinecrypto.EnvelopeV1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Open(tc.in, aad)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Open: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Open error = %v, want %v", err, tc.wantErr)
			}
			if got.Envelope != tc.wantEnv || got.Plaintext != tc.wantPT {
				t.Fatalf("Open = %+v", got)
			}
			if err != nil && tc.in != "" && strings.Contains(err.Error(), tc.in) {
				t.Fatalf("error echoes the stored value: %v", err)
			}
		})
	}
	// Payload tampering is an authentication failure.
	raw, _ := base64.StdEncoding.DecodeString(payload)
	raw[len(raw)-1] ^= 0x01
	tampered := "enc:v2:" + kid + ":" + base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Open(tampered, aad); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("tampered v2: %v", err)
	}
	// A nil cipher reports plaintext but refuses envelopes.
	var nilCipher *spinecrypto.SecretCipher
	if got, err := nilCipher.Open("p", aad); err != nil || got.Plaintext != "p" {
		t.Fatalf("nil Open(plaintext): %+v %v", got, err)
	}
	for _, v := range []string{v2, "enc:v1:AAAA"} {
		if _, err := nilCipher.Open(v, aad); !errors.Is(err, spinecrypto.ErrNoCipher) {
			t.Fatalf("nil Open(%.7s…): %v", v, err)
		}
	}
}

func TestV2_KeyRing(t *testing.T) {
	k1, k2 := randomKey(t), randomKey(t)
	old := ring(t, k1)
	aad := spinecrypto.AssociatedData(purpose, "w", "s")
	v2old := seal(t, old, "s3cret", aad)
	v1old, err := old.Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := spinecrypto.KeyID(k1)
	id2, _ := spinecrypto.KeyID(k2)

	rotated := ring(t, k2, k1)
	if ids := rotated.KeyIDs(); len(ids) != 2 || ids[0] != id2 || ids[1] != id1 || rotated.PrimaryKeyID() != id2 {
		t.Fatalf("ring ids = %v", ids)
	}
	got, err := rotated.Open(v2old, aad)
	if err != nil || got.Plaintext != "s3cret" || got.Primary || got.KeyID != id1 {
		t.Fatalf("previous-key v2 did not open as non-primary: %+v %v", got, err)
	}
	got, err = rotated.Open(v1old, nil)
	if err != nil || got.Plaintext != "s3cret" || got.Envelope != spinecrypto.EnvelopeV1 || got.Primary || got.KeyID != id1 {
		t.Fatalf("previous-key v1 did not open as non-primary: %+v %v", got, err)
	}
	if pt, err := rotated.Decrypt(v1old); err != nil || pt != "s3cret" {
		t.Fatalf("legacy Decrypt with previous key: %q %v", pt, err)
	}
	if s := seal(t, rotated, "x", aad); !strings.HasPrefix(s, "enc:v2:"+rotated.PrimaryKeyID()+":") {
		t.Fatal("Seal did not embed the primary key id")
	}
	if s, _ := rotated.Encrypt("x"); func() bool { p, e := ring(t, k2).Decrypt(s); return e != nil || p != "x" }() {
		t.Fatal("Encrypt did not use the primary key")
	}

	dropped := ring(t, k2)
	if _, err := dropped.Open(v2old, aad); !errors.Is(err, spinecrypto.ErrUnknownKeyID) {
		t.Fatalf("removed key still opens v2: %v", err)
	}
	if _, err := dropped.Open(v1old, nil); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("removed key still opens v1: %v", err)
	}

	// Listing the primary again (or a previous key twice) is de-duplicated.
	dup := ring(t, k2, k2, k1, k1)
	if ids := dup.KeyIDs(); len(ids) != 2 {
		t.Fatalf("duplicates not removed: %v", ids)
	}
}

// Header tampering: relabelling a v2 value with another ring key's id
// fails authentication; it is not tried as that key's ciphertext.
func TestV2_KeyIDRelabelFails(t *testing.T) {
	k1, k2 := randomKey(t), randomKey(t)
	c := ring(t, k1, k2)
	aad := spinecrypto.AssociatedData(purpose, "w", "s")
	ct := seal(t, c, "x", aad)
	id2, _ := spinecrypto.KeyID(k2)
	relabelled := strings.Replace(ct, c.PrimaryKeyID(), id2, 1)
	if _, err := c.Open(relabelled, aad); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("relabelled v2 opened: %v", err)
	}
}

func TestNewSecretCipherRing_Errors(t *testing.T) {
	good := randomKey(t)
	secretish := bytes.Repeat([]byte("K"), 31)
	cases := []struct {
		name    string
		primary []byte
		prev    [][]byte
		want    string
	}{
		{"short primary", secretish, nil, "primary key: "},
		{"short previous", good, [][]byte{randomKey(t), secretish}, "previous key 2: "},
		{"nil previous", good, [][]byte{nil}, "previous key 1: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spinecrypto.NewSecretCipherRing(tc.primary, tc.prev...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %q", err, tc.want)
			}
			if strings.Contains(err.Error(), string(secretish)) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(secretish)) {
				t.Fatalf("error leaks key bytes: %v", err)
			}
		})
	}
	if _, err := spinecrypto.KeyID(make([]byte, 16)); err == nil {
		t.Fatal("KeyID accepted a short key")
	}
}

// Port of TestPoC_NonceUniqueness (gap1), for v2.
func TestV2_NonceUniqueness(t *testing.T) {
	c := ring(t, randomKey(t))
	aad := spinecrypto.AssociatedData(purpose, "w", "s")
	seen := make(map[string]struct{}, 20000)
	for i := 0; i < 20000; i++ {
		ct := seal(t, c, "p", aad)
		raw, err := base64.StdEncoding.DecodeString(ct[strings.LastIndex(ct, ":")+1:])
		if err != nil {
			t.Fatal(err)
		}
		n := string(raw[:12])
		if _, dup := seen[n]; dup {
			t.Fatalf("nonce repeated at iteration %d", i)
		}
		seen[n] = struct{}{}
	}
}
