package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func newTestCipher(t *testing.T) *SecretCipher {
	t.Helper()
	key := make([]byte, EncryptionKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	c, err := NewSecretCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return c
}

func TestNewSecretCipher_RejectsWrongKeySize(t *testing.T) {
	cases := []int{0, 16, 24, 31, 33, 64}
	for _, n := range cases {
		if _, err := NewSecretCipher(make([]byte, n)); err == nil {
			t.Fatalf("expected error for key size %d", n)
		}
	}
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	c := newTestCipher(t)
	plaintexts := []string{"", "a", "webhook signing secret value", strings.Repeat("x", 4096)}
	for _, pt := range plaintexts {
		ct, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if !IsEncrypted(ct) {
			t.Fatalf("ciphertext missing prefix: %q", ct)
		}
		got, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != pt {
			t.Fatalf("roundtrip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestEncrypt_ProducesFreshNonce(t *testing.T) {
	c := newTestCipher(t)
	a, _ := c.Encrypt("same-input")
	b, _ := c.Encrypt("same-input")
	if a == b {
		t.Fatalf("two encryptions of the same plaintext produced identical output: %q", a)
	}
}

func TestDecrypt_PassesThroughLegacyPlaintext(t *testing.T) {
	c := newTestCipher(t)
	// Pre-migration rows are plaintext. Decrypt must return them
	// verbatim so the switchover does not require a data migration.
	got, err := c.Decrypt("legacy-plaintext-secret")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "legacy-plaintext-secret" {
		t.Fatalf("expected pass-through, got %q", got)
	}
}

func TestDecrypt_RejectsTamperedCiphertext(t *testing.T) {
	c := newTestCipher(t)
	ct, err := c.Encrypt("important")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Flip a byte in the ciphertext body — GCM must reject it.
	payload := strings.TrimPrefix(ct, "enc:v1:")
	raw, _ := base64.StdEncoding.DecodeString(payload)
	raw[len(raw)-1] ^= 0xFF
	tampered := "enc:v1:" + base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

func TestDecrypt_RejectsShortCiphertext(t *testing.T) {
	c := newTestCipher(t)
	if _, err := c.Decrypt("enc:v1:AAAA"); err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}

func TestDecrypt_NilCipherWithCiphertextFails(t *testing.T) {
	var c *SecretCipher
	if _, err := c.Decrypt("enc:v1:anything"); err == nil {
		t.Fatal("expected error when cipher nil but value is encrypted")
	}
	// Plaintext passthrough does not require a configured cipher.
	got, err := c.Decrypt("plaintext")
	if err != nil {
		t.Fatalf("expected plaintext passthrough without cipher, got err: %v", err)
	}
	if got != "plaintext" {
		t.Fatalf("got %q", got)
	}
}

func TestParseEncryptionKey_AcceptsBase64Variants(t *testing.T) {
	raw := make([]byte, EncryptionKeySize)
	for i := range raw {
		raw[i] = byte(i)
	}
	encodings := []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	}
	for _, enc := range encodings {
		got, err := ParseEncryptionKey(enc)
		if err != nil {
			t.Fatalf("parse %q: %v", enc, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("decoded mismatch for %q", enc)
		}
	}
}

func TestParseEncryptionKey_RejectsWrongLength(t *testing.T) {
	// 16 bytes, not 32 — reject.
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := ParseEncryptionKey(short); err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}

func TestParseEncryptionKey_RejectsEmpty(t *testing.T) {
	if _, err := ParseEncryptionKey("   "); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestParseEncryptionKey_RejectsNonBase64(t *testing.T) {
	if _, err := ParseEncryptionKey("not$$valid$$base64!!"); err == nil {
		t.Fatal("expected error for non-base64 input")
	}
}
