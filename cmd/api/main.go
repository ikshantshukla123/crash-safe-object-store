// Command api is the HTTP API and the single write coordinator (CLAUDE.md §3.2).
//
// Phase 1 is the walking skeleton: configuration, structured logging, a
// Postgres pool, a Redis client, and /health. Object and bucket routes arrive
// in Phase 2.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/config"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

func main() {
	// main stays tiny: everything that can fail lives in run, so there is a
	// single exit point and deferred cleanup actually runs. Calling os.Exit
	// inside run would skip every defer.
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	// Cancelled on SIGINT/SIGTERM. Docker sends SIGTERM on `compose down`, so
	// this is the signal that must trigger a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := newPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Warn("closing redis", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           newRouter(log, dbgen.New(pool), rdb),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout: object downloads are streamed and a fixed deadline
		// would truncate large reads. Per-request deadlines are used instead.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.APIAddr, "demo_mode", cfg.DemoMode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen and serve: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.ShutdownTimeout)
	}

	// A fresh context: ctx is already cancelled, so reusing it would abort
	// in-flight requests immediately instead of draining them.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

func newPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	// pgxpool.New is lazy, so without this ping a bad DATABASE_URL would not
	// surface until the first request.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func newRouter(log *slog.Logger, q *dbgen.Queries, rdb *redis.Client) http.Handler {
	r := chi.NewRouter()

	// RequestID first: everything downstream, including the panic recoverer,
	// should be able to quote the same op_id (§14).
	//
	// chi's RealIP is deliberately absent: it is deprecated and spoofable,
	// since it trusts X-Forwarded-For regardless of whether a proxy set it.
	// When Phase 8 needs the client IP for rate limiting, parse it from a
	// trusted-proxy allowlist instead.
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)

	r.Get("/health", handleHealth(log, q, rdb))
	return r
}
