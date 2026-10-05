package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken covers every reason a token was rejected. Callers must not
// tell the client which reason: "expired" and "bad signature" are both just
// 401, because the difference is useful only to an attacker.
var ErrInvalidToken = errors.New("invalid token")

// Issuer mints and verifies HS256 tokens.
//
// HS256 is symmetric: the same secret signs and verifies. That is fine here
// because one process does both. An asymmetric algorithm would only be needed
// if a separate service had to verify tokens without being able to mint them.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	// HS256 keys shorter than the 256-bit hash output weaken the signature,
	// and short secrets are brute-forceable offline from a single token.
	if len(secret) < 32 {
		return nil, errors.New("jwt secret must be at least 32 bytes")
	}
	if ttl <= 0 {
		return nil, errors.New("jwt ttl must be positive")
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

// Issue returns a signed token whose subject is the user id.
func (i *Issuer) Issue(userID string, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(i.ttl)
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Subject verifies a token and returns the user id it was issued for.
func (i *Issuer) Subject(token string) (string, error) {
	parsed, err := jwt.ParseWithClaims(token, &jwt.RegisteredClaims{},
		func(t *jwt.Token) (any, error) {
			// Without this check the library would accept whatever algorithm
			// the token header claims. That is the classic JWT confusion
			// attack: a forged token saying alg=none, or alg=HS256 against an
			// RSA public key, verifies against a server that trusts the
			// header. Only accept the one algorithm we actually issue.
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
			}
			return i.secret, nil
		},
		// Belt and braces: reject any other algorithm at the parser level too.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	claims, ok := parsed.Claims.(*jwt.RegisteredClaims)
	if !ok || claims.Subject == "" {
		return "", fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}
	return claims.Subject, nil
}
