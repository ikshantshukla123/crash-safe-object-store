package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A dependency being down must surface as 503, never as a cheerful 200.
// The Compose healthcheck and deploy/smoke.sh both depend on this.
func TestHealthReturns503WhenDependenciesAreDown(t *testing.T) {
	// Port 1 is reserved and never listening, so both clients fail fast.
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatalf("building pool: %v", err)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer rdb.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handleHealth(discardLogger(), dbgen.New(pool), rdb)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Status != "degraded" {
		t.Errorf("status field = %q, want \"degraded\"", body.Status)
	}
	for _, dep := range []string{"postgres", "redis"} {
		if body.Checks[dep] == "ok" {
			t.Errorf("check %q reported ok, but it is unreachable", dep)
		}
	}
}

func TestHealthReturns200WhenDependenciesAreUp(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if dbURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR not set; run `make up` or rely on CI service containers")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("building pool: %v", err)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handleHealth(discardLogger(), dbgen.New(pool), rdb)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status field = %q, want \"ok\"; checks = %v", body.Status, body.Checks)
	}
}
