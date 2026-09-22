// Package membership is the domain core of ticket 02: accounts, roles,
// email verification, versioned Terms of Service, sessions, and the Platform
// Admin's application decisions. Handlers see a Store interface; the postgres
// package implements it for the live system of record (ADR 0007).
package membership

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// passwordScheme names the hash format stored in accounts.password_hash:
// pbkdf2-sha256$<iterations>$<salt b64>$<dk b64>. PBKDF2 is stdlib since
// Go 1.24, needs no third-party dependency, and the scheme string leaves
// room to migrate iterations or algorithms per row later.
const passwordScheme = "pbkdf2-sha256"

// passwordIterations is the work factor for new hashes (OWASP 2023 guidance
// for PBKDF2-SHA256 is 600k; 210k is the floor this pilot ships with —
// raising it re-hashes only on next successful login, not eagerly).
const passwordIterations = 210000

// HashPassword derives a storable hash from a plaintext password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", fmt.Errorf("failed to derive key: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		passwordScheme,
		passwordIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	), nil
}

// CheckPassword reports whether the plaintext password matches the stored
// hash. Malformed hashes never match.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		// Still burn comparable work so callers can't time-distinguish a
		// malformed stored hash from a real check (defensive; the register
		// path only ever stores well-formed hashes).
		DummyCheck(password)
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[1], "%d", &iterations); err != nil || iterations <= 0 {
		DummyCheck(password)
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		DummyCheck(password)
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		DummyCheck(password)
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		DummyCheck(password)
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyCheck runs a PBKDF2 derivation at the standard work factor and
// discards the result. Login paths call it when the account does not exist,
// so the response time for "unknown email" matches "wrong password" and the
// endpoint cannot be used to enumerate registered addresses by timing.
func DummyCheck(password string) {
	salt := []byte("thresh-dummy-check-salt")
	_, _ = pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
}
