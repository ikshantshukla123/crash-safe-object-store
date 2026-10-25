// Command worker runs the health, replicator, repair, scrubber and embedding
// loops. GC runs in the API because only that process owns the WAL writer.
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/config"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/health"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/queue"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/repair"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/replicator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/scrubber"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/search"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorkers()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	reg := metrics.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create postgres pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	q := dbgen.New(pool)

	nodes, err := registerNodes(ctx, q, cfg.StorageNodes)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}

	scrub := scrubber.New(q, nodes, log, reg, int32(cfg.ScrubBatch))
	checker := health.New(q, nodes, log, cfg.HealthInterval, cfg.HealthDownThreshold, cfg.HealthUpThreshold, scrub)
	rep := replicator.New(q, nodes, log, reg, int32(cfg.ReplicatorBatch), int32(cfg.ReplicatorConcurrency))
	repairer := repair.New(q, nodes, log, reg, int32(cfg.RepairBatch), 3)
	embedder, err := search.NewEmbedder(cfg.EmbeddingModel, cfg.OpenAIAPIKey, cfg.EmbeddingDim)
	if err != nil {
		return err
	}
	embed := search.NewWorker(q, nodes, embedder, queue.NewEmbeddings(rdb), log, reg)

	go checker.Run(ctx)
	go loop(ctx, log, "replicator", 5*time.Second, func() {
		if n, err := rep.RunOnce(ctx); err != nil {
			log.Warn("replicator", "err", err)
		} else if n > 0 {
			log.Info("replicated replicas", "count", n)
		}
	})
	go loop(ctx, log, "repair", cfg.RepairInterval, func() {
		if n, err := repairer.RunOnce(ctx); err != nil {
			log.Warn("repair", "err", err)
		} else if n > 0 {
			log.Info("repaired replicas", "count", n)
		}
	})
	go loop(ctx, log, "scrubber", cfg.ScrubInterval, func() {
		if n, err := scrub.RunOnce(ctx); err != nil {
			log.Warn("scrubber", "err", err)
		} else if n > 0 {
			log.Info("scrubbed replicas", "count", n)
		}
	})
	go embed.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           workerHandler(reg),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("worker listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func loop(ctx context.Context, log *slog.Logger, name string, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	fn()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}

func workerHandler(reg *metrics.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/metrics", reg.Handler())
	mux.Handle("/stats", reg.SnapshotHandler())
	return mux
}

func registerNodes(ctx context.Context, q *dbgen.Queries, configured []config.StorageNode) ([]placement.Node, error) {
	out := make([]placement.Node, 0, len(configured))
	for _, sn := range configured {
		id := placement.NodeID(sn.Name)
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		if _, err := q.UpsertStorageNode(ctx, dbgen.UpsertStorageNodeParams{
			ID:      pgtype.UUID{Bytes: parsed, Valid: true},
			Name:    sn.Name,
			BaseUrl: sn.URL,
		}); err != nil {
			return nil, fmt.Errorf("register node %s: %w", sn.Name, err)
		}
		out = append(out, placement.Node{
			ID: id, Name: sn.Name, Status: "up",
			Store: objects.NewNodeClient(id, sn.Name, sn.URL),
		})
	}
	return out, nil
}
