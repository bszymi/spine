package crypto

import (
	"errors"
	"strings"
	"testing"
)

// AC1: a zero-value SecretCipher returns an error from every method,
// never panics. (At 61994fe a zero-value Encrypt panicked too; the error
// behaviour is new.)
func TestSecretCipher_ZeroValueNeverPanics(t *testing.T) {
	var z SecretCipher
	aad := AssociatedData("p", "w", "s")
	mustNotPanic := func(name string, f func() error) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked on a zero-value SecretCipher: %v", name, r)
			}
		}()
		if err := f(); err == nil {
			t.Fatalf("%s returned no error on a zero-value SecretCipher", name)
		}
	}
	mustNotPanic("Encrypt", func() error { _, err := z.Encrypt("x"); return err })
	mustNotPanic("Decrypt(v1)", func() error {
		_, err := z.Decrypt("enc:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		if !errors.Is(err, ErrNoCipher) {
			t.Errorf("Decrypt(v1) zero-value error should match ErrNoCipher: %v", err)
		}
		return err
	})
	mustNotPanic("Decrypt(v2)", func() error { _, err := z.Decrypt("enc:v2:0123456789abcdef:AAAA"); return err })
	mustNotPanic("Seal", func() error { _, err := z.Seal("x", aad); return err })
	mustNotPanic("Open(v1)", func() error { _, err := z.Open("enc:v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", aad); return err })
	mustNotPanic("Open(v2)", func() error { _, err := z.Open("enc:v2:0123456789abcdef:AAAA", aad); return err })
	if id := z.PrimaryKeyID(); id != "" {
		t.Fatalf("zero-value PrimaryKeyID = %q", id)
	}
	if ids := z.KeyIDs(); ids != nil {
		t.Fatalf("zero-value KeyIDs = %v", ids)
	}
	// Plaintext pass-through does not need a key, as for a nil cipher.
	if got, err := z.Decrypt("plain"); err != nil || got != "plain" {
		t.Fatalf("zero-value Decrypt(plaintext) = %q, %v", got, err)
	}
	var n *SecretCipher
	if n.PrimaryKeyID() != "" || n.KeyIDs() != nil {
		t.Fatal("nil cipher ids must be empty")
	}
	if _, err := n.Seal("x", aad); err == nil {
		t.Fatal("nil Seal returned no error")
	}
	if _, err := n.Encrypt("x"); err == nil || err.Error() != "nil cipher" {
		t.Fatalf("nil Encrypt error changed: %v", err)
	}
}

// AC2 (internal half): Seal with the committed nonce reproduces the
// committed golden envelope byte for byte, so the encoder — not only the
// decoder — is pinned.
func TestV2_GoldenVectorSeal(t *testing.T) {
	key := make([]byte, EncryptionKeySize)
	nonce := make([]byte, gcmNonceSize)
	for i := range key {
		key[i] = byte(i)
	}
	for i := range nonce {
		nonce[i] = 0xa0 + byte(i)
	}
	c, err := NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.sealV2("whsec_golden-vector", AssociatedData("runtime.event_subscriptions.signing_secret", "ws-golden", "sub-golden"), nonce)
	if err != nil {
		t.Fatal(err)
	}
	const want = "enc:v2:ada7649825e43683:oKGio6SlpqeoqaqrRppE7umWPTjFfwvZG7yc9mLvWM0A+GGZQBqEEVv5aZQzvmc="
	if got != want {
		t.Fatalf("golden seal changed:\n got %s\nwant %s", got, want)
	}
	// The caller's nonce slice is not mutated by sealing.
	if nonce[0] != 0xa0 || nonce[gcmNonceSize-1] != 0xab {
		t.Fatal("sealV2 mutated the nonce")
	}
}

// AC5: two different keys that derive the same key id are refused at
// ring construction; the same key listed twice is not a collision.
func TestNewSecretCipherRing_RefusesKeyIDCollision(t *testing.T) {
	orig := keyIDFunc
	t.Cleanup(func() { keyIDFunc = orig })
	keyIDFunc = func(key []byte) (string, error) {
		if err := checkKeySize(key); err != nil {
			return "", err
		}
		return "0123456789abcdef", nil
	}
	k1 := make([]byte, EncryptionKeySize)
	k2 := make([]byte, EncryptionKeySize)
	k2[0] = 1
	if _, err := NewSecretCipherRing(k1, k2); err == nil || !strings.Contains(err.Error(), "previous key 1: key id 0123456789abcdef collides") {
		t.Fatalf("collision not refused: %v", err)
	}
	c, err := NewSecretCipherRing(k1, k1)
	if err != nil || len(c.keys) != 1 {
		t.Fatalf("identical key must be de-duplicated, not treated as a collision: %v", err)
	}
}
