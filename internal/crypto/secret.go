// Package crypto provides symmetric encryption for secrets persisted
// at rest. The threat model is a database compromise: an attacker who
// exfiltrates `event_subscriptions` rows must not be able to forge
// webhook signatures without also compromising the separately-stored
// encryption key (SPINE_SECRET_ENCRYPTION_KEY).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// EncryptionKeySize is the required AES-256 key size in bytes.
const EncryptionKeySize = 32

// ciphertextPrefix tags wire-format ciphertext so Decrypt can
// distinguish encrypted blobs from legacy plaintext rows and migrate
// them transparently on next write.
const ciphertextPrefix = "enc:v1:"

// SecretCipher wraps an AES-GCM AEAD for small at-rest secrets. The
// zero value is not usable — construct via NewSecretCipher.
type SecretCipher struct {
	aead cipher.AEAD
}

// NewSecretCipher builds a cipher from a 32-byte key.
func NewSecretCipher(key []byte) (*SecretCipher, error) {
	if len(key) != EncryptionKeySize {
		return nil, fmt.Errorf("secret encryption key must be %d bytes, got %d", EncryptionKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &SecretCipher{aead: aead}, nil
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

// Encrypt seals plaintext with a fresh random nonce and returns a
// prefixed, base64-encoded ciphertext safe for TEXT columns.
func (c *SecretCipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", errors.New("nil cipher")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	blob := append(nonce, sealed...)
	return ciphertextPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt reverses Encrypt. Values missing the prefix are returned
// verbatim so pre-migration plaintext rows keep working until they are
// rewritten; IsEncrypted lets callers decide whether to re-encrypt on
// next write.
func (c *SecretCipher) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, ciphertextPrefix) {
		return value, nil
	}
	if c == nil {
		return "", errors.New("encrypted value found but cipher is not configured")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, ciphertextPrefix))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns+c.aead.Overhead() {
		return "", errors.New("ciphertext too short")
	}
	nonce, sealed := raw[:ns], raw[ns:]
	plaintext, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted reports whether value carries the ciphertext prefix.
// Storage callers use it to decide whether a legacy plaintext row
// should be re-encrypted on its next update.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, ciphertextPrefix)
}
