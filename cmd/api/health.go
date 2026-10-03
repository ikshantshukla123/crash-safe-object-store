package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

// healthResponse is the body of GET /health.
type healthResponse struct {
	Status    string            `json:"status"` // "ok" or "degraded"
	Checks    map[string]string `json:"checks"` // dependency -> "ok" or an error
	CheckedAt time.Time         `json:"checked_at"`
}

// handleHealth reports whether this process can reach its dependencies.
//
// This is a readiness check, not a liveness check: it returns 503 when
// Postgres or Redis is unreachable. deploy/smoke.sh (§17) and the Compose
// healthchecks both rely on that distinction, and reporting "ok" while the
// database is down would make every other signal untrustworthy.
//
// Note that a Redis failure also degrades health even though Redis is only
// derived data (§3.1). It is reported so the operator sees it; whether reads
// should still be served from Postgres alone is a Phase 7 decision.
func handleHealth(log *slog.Logger, q *dbgen.Queries, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bounded so a hung dependency cannot hold the probe open; Compose
		// healthchecks run on a short interval.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		checks := map[string]string{"postgres": "ok", "redis": "ok"}
		healthy := true

		if _, err := q.Ping(ctx); err != nil {
			checks["postgres"] = err.Error()
			healthy = false
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			checks["redis"] = err.Error()
			healthy = false
		}

		body := healthResponse{Status: "ok", Checks: checks, CheckedAt: time.Now().UTC()}
		status := http.StatusOK
		if !healthy {
			body.Status = "degraded"
			status = http.StatusServiceUnavailable
			log.WarnContext(ctx, "health check failed", "checks", checks)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			log.WarnContext(ctx, "writing health response", "err", err)
		}
	}
}
