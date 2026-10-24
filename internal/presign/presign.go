// Package presign signs and verifies stateless S3-style URLs.
package presign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var (
	ErrExpired       = errors.New("presigned url expired")
	ErrInvalid       = errors.New("presigned url invalid")
	ErrExpiryTooLong = errors.New("presigned url expiry too long")
)

const (
	queryKeyID     = "X-Key-Id"
	queryExpires   = "X-Expires"
	querySignature = "X-Signature"
)

// Signer mints and verifies presigned URLs with HMAC-SHA256.
type Signer struct {
	keyID     string
	secret    []byte
	maxExpiry time.Duration
	now       func() time.Time
}

func New(keyID, secret string, maxExpiry time.Duration) (*Signer, error) {
	if keyID == "" {
		return nil, errors.New("presign key id must not be empty")
	}
	if len(secret) < 16 {
		return nil, errors.New("presign secret must be at least 16 bytes")
	}
	if maxExpiry <= 0 {
		return nil, errors.New("presign max expiry must be positive")
	}
	return &Signer{
		keyID:     keyID,
		secret:    []byte(secret),
		maxExpiry: maxExpiry,
		now:       time.Now,
	}, nil
}

// Sign returns the query parameters that authenticate method + path +
// versionID until expires.
func (s *Signer) Sign(method, path, versionID string, expires time.Time) url.Values {
	unix := expires.Unix()
	sig := s.signature(method, path, versionID, unix)
	q := url.Values{}
	q.Set(queryKeyID, s.keyID)
	q.Set(queryExpires, strconv.FormatInt(unix, 10))
	q.Set(querySignature, sig)
	return q
}

// URL builds a presigned URL from base plus path.
func (s *Signer) URL(base, method, path, versionID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("presign ttl must be positive")
	}
	if ttl > s.maxExpiry {
		return "", ErrExpiryTooLong
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse presign base url: %w", err)
	}
	u.Path = path
	u.RawQuery = s.Sign(method, path, versionID, s.now().Add(ttl)).Encode()
	return u.String(), nil
}

// Verify checks a request's presign parameters, including signature, key id
// and expiry.
func (s *Signer) Verify(r *http.Request, versionID string) error {
	q := r.URL.Query()
	if q.Get(queryKeyID) != s.keyID {
		return fmt.Errorf("%w: unknown key id", ErrInvalid)
	}
	rawExpires := q.Get(queryExpires)
	expires, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: bad expiry", ErrInvalid)
	}
	want := s.signature(r.Method, r.URL.Path, versionID, expires)
	got := q.Get(querySignature)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return fmt.Errorf("%w: bad signature", ErrInvalid)
	}
	now := s.now()
	exp := time.Unix(expires, 0)
	if !exp.After(now) {
		return ErrExpired
	}
	if exp.Sub(now) > s.maxExpiry {
		return ErrExpiryTooLong
	}
	return nil
}

func (s *Signer) signature(method, path, versionID string, expires int64) string {
	mac := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%d", method, path, versionID, expires)
	return hex.EncodeToString(mac.Sum(nil))
}
