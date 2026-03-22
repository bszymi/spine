package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// GenerateToken creates a cryptographically random token string.
// Returns 32 random bytes hex-encoded (64 characters).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken computes the SHA-256 hash of a raw token string.
// SHA-256 is appropriate for API tokens (unlike passwords) because tokens are
// 256-bit random values — they cannot be brute-forced or found in rainbow tables.
// bcrypt/argon2 would add unnecessary latency for every authenticated request.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
