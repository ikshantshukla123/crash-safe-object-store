// Command api is the HTTP API and the single write coordinator.
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

	"github.com/ikshantshukla123/crash-safe-object-store/internal/auth"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/cache"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/config"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/gc"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/presign"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/queue"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/search"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
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

	q := dbgen.New(pool)
	nodes, err := registerNodes(ctx, q, cfg.StorageNodes)
	if err != nil {
		return err
	}

	recovery := coordinator.NewRecovery(pool, q, log, nodes, cfg.WriteQuorumW)
	walLog, walResult, err := wal.Recover(ctx, cfg.WALDir, cfg.WALSegmentBytes, recovery)
	if err != nil {
		return fmt.Errorf("recover wal: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := walLog.Close(closeCtx); err != nil {
			log.Error("closing wal", "err", err)
		}
	}()
	log.Info("wal recovered",
		"records", walResult.Records,
		"pending_puts", walResult.PendingPuts,
		"pending_deletes", walResult.PendingDels,
		"last_lsn", walResult.LastLSN,
		"truncated_tail", walResult.Truncated,
	)

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Warn("closing redis", "err", err)
		}
	}()

	reg := metrics.New()
	coord, err := coordinator.New(pool, q, log, walLog, nodes, coordinator.Config{
		Replicas:         cfg.ReplicasN,
		WriteQuorum:      cfg.WriteQuorumW,
		MaxPartSize:      cfg.MaxPartSizeBytes,
		MultipartExpiry:  cfg.MultipartExpiry,
		Cache:            cache.NewRedis(rdb),
		CacheTTL:         cfg.MetaCacheTTL,
		SmallObjectLimit: cfg.SmallObjectLimit,
		Metrics:          reg,
		Embeddings:       queue.NewEmbeddings(rdb),
	})
	if err != nil {
		return err
	}

	issuer, err := auth.NewIssuer(cfg.JWTSecret, cfg.JWTTTL)
	if err != nil {
		return err
	}
	signer, err := presign.New(cfg.PresignKeyID, cfg.PresignSecret, cfg.PresignMaxExpiry)
	if err != nil {
		return err
	}
	embedder, err := search.NewEmbedder(cfg.EmbeddingModel, cfg.OpenAIAPIKey, cfg.EmbeddingDim)
	if err != nil {
		return err
	}
	searcher := search.NewSearcher(q, embedder)

	go gc.New(q, nodes, log, walLog, cfg.GCOrphanGrace, cfg.GCOrphanGrace, int32(cfg.GCBatch)).Run(ctx, cfg.GCInterval)

	srv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           newRouter(log, q, rdb, coord, issuer, signer, reg, searcher, cfg.DemoToken),
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

func registerNodes(ctx context.Context, q *dbgen.Queries, configured []config.StorageNode) ([]placement.Node, error) {
	out := make([]placement.Node, 0, len(configured))
	for _, sn := range configured {
		id := placement.NodeID(sn.Name)
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse node id: %w", err)
		}
		if _, err := q.UpsertStorageNode(ctx, dbgen.UpsertStorageNodeParams{
			ID:      pgtype.UUID{Bytes: parsed, Valid: true},
			Name:    sn.Name,
			BaseUrl: sn.URL,
		}); err != nil {
			return nil, fmt.Errorf("register storage node %s: %w", sn.Name, err)
		}
		out = append(out, placement.Node{
			ID:     id,
			Name:   sn.Name,
			Status: "up",
			Store:  objects.NewNodeClient(id, sn.Name, sn.URL),
		})
	}
	return out, nil
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
