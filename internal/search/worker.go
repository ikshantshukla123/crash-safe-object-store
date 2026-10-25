package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/queue"
)

const maxEmbeddedBytes = 32 << 10

// Worker turns version content into embeddings. Jobs are at-least-once and the
// upsert is keyed by version_id, so duplicate work is harmless.
type Worker struct {
	q        *dbgen.Queries
	nodes    []placement.Node
	embedder Embedder
	queue    *queue.Embeddings
	log      *slog.Logger
	metrics  *metrics.Registry
}

func NewWorker(q *dbgen.Queries, nodes []placement.Node, embedder Embedder, jobs *queue.Embeddings, log *slog.Logger, reg *metrics.Registry) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{q: q, nodes: nodes, embedder: embedder, queue: jobs, log: log, metrics: reg}
}

// Run drains the Redis queue and periodically re-enqueues versions that have
// no durable embedding row.
func (w *Worker) Run(ctx context.Context) {
	w.Sweep(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Sweep(ctx)
		default:
		}

		id, err := w.queue.Dequeue(ctx, 2*time.Second)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				w.log.WarnContext(ctx, "embedding dequeue", "err", err)
				time.Sleep(time.Second)
			}
			continue
		}
		if id == "" {
			continue
		}
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := w.ProcessVersion(taskCtx, id); err != nil {
			w.log.WarnContext(taskCtx, "embedding version", "version_id", id, "err", err)
		}
		cancel()
	}
}

// Sweep re-enqueues versions without embeddings, so a Redis flush cannot lose
// work permanently.
func (w *Worker) Sweep(ctx context.Context) {
	ids, err := w.q.ListVersionsMissingEmbedding(ctx, 100)
	if err != nil {
		w.log.WarnContext(ctx, "list versions missing embeddings", "err", err)
		return
	}
	for _, id := range ids {
		if err := w.queue.Enqueue(ctx, uuidString(id)); err != nil {
			w.log.WarnContext(ctx, "sweep enqueue embedding", "err", err)
		}
	}
}

// ProcessVersion embeds one version.
func (w *Worker) ProcessVersion(ctx context.Context, versionID string) error {
	parsed, err := parseUUID(versionID)
	if err != nil {
		return nil
	}
	info, err := w.q.GetVersionForEmbedding(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get version: %w", err)
	}
	if info.IsDeleteMarker {
		return nil
	}
	content, err := w.readContent(ctx, parsed)
	if err != nil {
		return err
	}
	text := info.BucketName + "/" + info.Key + "\n" + content
	vec, model, err := w.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if err := w.q.UpsertEmbedding(ctx, dbgen.UpsertEmbeddingParams{
		VersionID: parsed,
		Content:   truncate(content, maxEmbeddedBytes),
		Model:     model,
		Embedding: VectorString(vec),
	}); err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	if w.metrics != nil {
		w.metrics.EmbeddingsDone.Add(1)
	}
	return nil
}

func (w *Worker) readContent(ctx context.Context, versionID pgtype.UUID) (string, error) {
	chunks, err := w.q.ListChunksForVersion(ctx, versionID)
	if err != nil {
		return "", fmt.Errorf("list chunks: %w", err)
	}
	var out []byte
	for _, chunk := range chunks {
		if len(out) >= maxEmbeddedBytes {
			break
		}
		data, err := w.readChunk(ctx, chunk)
		if err != nil {
			return "", err
		}
		out = append(out, data...)
	}
	return truncate(string(out), maxEmbeddedBytes), nil
}

func (w *Worker) readChunk(ctx context.Context, chunk dbgen.Chunk) ([]byte, error) {
	replicas, err := w.q.ListReplicasForChunk(ctx, chunk.ID)
	if err != nil {
		return nil, err
	}
	for _, replica := range replicas {
		if replica.Status != "healthy" || replica.NodeStatus != "up" {
			continue
		}
		node, ok := w.node(uuidString(replica.NodeID))
		if !ok {
			continue
		}
		rc, err := node.Store.Get(ctx, uuidString(chunk.ID))
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, chunk.SizeBytes+1))
		rc.Close()
		if readErr != nil || int64(len(data)) != chunk.SizeBytes {
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != chunk.Sha256 {
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("no healthy source for chunk %s", uuidString(chunk.ID))
}

func (w *Worker) node(id string) (placement.Node, bool) {
	for _, n := range w.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func parseUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// Requeue adds a version back to the queue. Exposed for handlers and tests.
func (w *Worker) Requeue(ctx context.Context, versionID string) {
	if err := w.queue.Enqueue(ctx, versionID); err != nil {
		w.log.WarnContext(ctx, "requeue embedding", "version_id", versionID, "err", err)
	}
}
