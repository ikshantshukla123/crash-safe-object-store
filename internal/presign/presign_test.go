package presign

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

const testSecret = "a-test-presign-secret-32-bytes!!"

func newSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := New("k1", testSecret, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSignAndVerify(t *testing.T) {
	s := newSigner(t)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }

	signed, err := s.URL("http://example.com", http.MethodGet, "/buckets/b/objects/key.txt", "", time.Hour)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed: %v", err)
	}
	req := &http.Request{Method: http.MethodGet, URL: u}
	if err := s.Verify(req, ""); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	s := newSigner(t)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	signed, err := s.URL("http://example.com", http.MethodGet, "/buckets/b/objects/key.txt", "", time.Hour)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	u, _ := url.Parse(signed)
	u.Path = "/buckets/b/objects/other.txt"
	if err := s.Verify(&http.Request{Method: http.MethodGet, URL: u}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("tampered path error = %v, want ErrInvalid", err)
	}

	u, _ = url.Parse(signed)
	u.RawQuery = u.RawQuery[:len(u.RawQuery)-2] + "00"
	if err := s.Verify(&http.Request{Method: http.MethodGet, URL: u}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("tampered signature error = %v, want ErrInvalid", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	s := newSigner(t)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	signed, err := s.URL("http://example.com", http.MethodGet, "/buckets/b/objects/key.txt", "", time.Hour)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	u, _ := url.Parse(signed)
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if err := s.Verify(&http.Request{Method: http.MethodGet, URL: u}, ""); !errors.Is(err, ErrExpired) {
		t.Errorf("expired error = %v, want ErrExpired", err)
	}
}

func TestURLRejectsLongExpiry(t *testing.T) {
	s := newSigner(t)
	if _, err := s.URL("http://example.com", http.MethodGet, "/buckets/b/objects/key.txt", "", 8*24*time.Hour); !errors.Is(err, ErrExpiryTooLong) {
		t.Errorf("long expiry error = %v, want ErrExpiryTooLong", err)
	}
}

func TestVerifyRejectsWrongKeyID(t *testing.T) {
	s := newSigner(t)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	signed, _ := s.URL("http://example.com", http.MethodGet, "/buckets/b/objects/key.txt", "", time.Hour)
	u, _ := url.Parse(signed)
	q := u.Query()
	q.Set("X-Key-Id", "other")
	u.RawQuery = q.Encode()
	if err := s.Verify(&http.Request{Method: http.MethodGet, URL: u}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("wrong key id error = %v, want ErrInvalid", err)
	}
}
