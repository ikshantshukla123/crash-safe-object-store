package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "a-test-secret-that-is-long-enough-32"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the plaintext password appears inside the hash")
	}
	if err := VerifyPassword("correct horse battery staple", hash); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := VerifyPassword("wrong password", hash); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong password error = %v, want ErrMismatch", err)
	}
}

// Equal passwords must not produce equal hashes, or a stolen database would
// reveal which users share a password.
func TestPasswordIsSalted(t *testing.T) {
	a, err := HashPassword("same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical, so no salt was applied")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "$argon2id$v=19$broken", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if err := VerifyPassword("x", bad); err == nil {
			t.Errorf("VerifyPassword(%q) succeeded, want an error", bad)
		}
	}
}

func newTestIssuer(t *testing.T, ttl time.Duration) *Issuer {
	t.Helper()
	iss, err := NewIssuer(testSecret, ttl)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss
}

func TestTokenRoundTrip(t *testing.T) {
	iss := newTestIssuer(t, 15*time.Minute)
	token, expiresAt, err := iss.Issue("user-123", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Error("token expires in the past")
	}

	sub, err := iss.Subject(token)
	if err != nil {
		t.Fatalf("Subject: %v", err)
	}
	if sub != "user-123" {
		t.Errorf("subject = %q, want %q", sub, "user-123")
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	iss := newTestIssuer(t, time.Minute)
	// Issued an hour ago with a one minute lifetime.
	token, _, err := iss.Issue("user-123", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := iss.Subject(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenFromAnotherSecretRejected(t *testing.T) {
	attacker, err := NewIssuer("a-different-secret-also-long-enough!", time.Minute)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	token, _, err := attacker.Issue("admin", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := newTestIssuer(t, time.Minute).Subject(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("foreign token error = %v, want ErrInvalidToken", err)
	}
}

// The classic JWT attack: forge a token with alg=none and no signature. A
// parser that trusts the header will accept it as authentic.
func TestAlgNoneTokenRejected(t *testing.T) {
	forged, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject:   "admin",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("building forged token: %v", err)
	}
	if _, err := newTestIssuer(t, time.Minute).Subject(forged); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("alg=none token error = %v, want ErrInvalidToken", err)
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	iss := newTestIssuer(t, time.Minute)
	token, _, err := iss.Issue("user-123", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Flip a character in the payload; the signature no longer matches.
	parts := strings.Split(token, ".")
	parts[1] = "eyJzdWIiOiJhZG1pbiJ9"
	if _, err := iss.Subject(strings.Join(parts, ".")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("tampered token error = %v, want ErrInvalidToken", err)
	}
}

func TestNewIssuerRejectsWeakConfig(t *testing.T) {
	if _, err := NewIssuer("too-short", time.Minute); err == nil {
		t.Error("NewIssuer accepted a short secret, want an error")
	}
	if _, err := NewIssuer(testSecret, 0); err == nil {
		t.Error("NewIssuer accepted a zero ttl, want an error")
	}
}
