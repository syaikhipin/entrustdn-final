package membership_test

import (
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Password hashing is the only credential crypto in the platform: tests pin
// the observable contract — the same password verifies against its hash, the
// wrong one does not, and hashes are salted so they never repeat.

func TestHashPasswordRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{name: "simple password", password: "harvest-2026"},
		{name: "with spaces and unicode", password: "dairy & grain 🌾"},
		{name: "long passphrase", password: strings.Repeat("correct horse battery staple ", 8)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := membership.HashPassword(tt.password)
			if err != nil {
				t.Fatalf("HashPassword() error = %v", err)
			}
			if hash == tt.password || hash == "" {
				t.Fatalf("hash = %q, want a derived value distinct from the password", hash)
			}
			if !membership.CheckPassword(hash, tt.password) {
				t.Errorf("CheckPassword(hash, password) = false, want true")
			}
		})
	}
}

func TestCheckPasswordRejectsWrongPassword(t *testing.T) {
	hash, err := membership.HashPassword("harvest-2026")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	tests := []struct {
		name     string
		password string
	}{
		{name: "wrong password", password: "harvest-2027"},
		{name: "case differs", password: "Harvest-2026"},
		{name: "empty", password: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if membership.CheckPassword(hash, tt.password) {
				t.Errorf("CheckPassword(%q) = true, want false", tt.password)
			}
		})
	}
}

func TestCheckPasswordRejectsMalformedHash(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{name: "empty", hash: ""},
		{name: "not the scheme", hash: "plaintext"},
		{name: "too few parts", hash: "pbkdf2-sha256$210000$salt"},
		{name: "bad iteration count", hash: "pbkdf2-sha256$many$xx$yy"},
		{name: "bad base64", hash: "pbkdf2-sha256$210000$!!!$???"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if membership.CheckPassword(tt.hash, "harvest-2026") {
				t.Errorf("CheckPassword(%q) = true, want false for malformed hash", tt.hash)
			}
		})
	}
}

func TestHashPasswordSaltsSoHashesDiffer(t *testing.T) {
	a, err := membership.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	b, err := membership.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if a == b {
		t.Errorf("two hashes of the same password are identical: %q", a)
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := membership.HashPassword(""); err == nil {
		t.Fatal("HashPassword(\"\") error = nil, want an error")
	}
}

func TestDummyCheckAcceptsAnyInput(t *testing.T) {
	// DummyCheck exists purely to equalize login-path work when the account
	// is unknown; it must never panic and never report anything.
	tests := []struct {
		name     string
		password string
	}{
		{name: "normal password", password: "harvest-2026"},
		{name: "empty string", password: ""},
		{name: "unicode", password: "🌾 dairy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			membership.DummyCheck(tt.password) // must not panic
		})
	}
}
