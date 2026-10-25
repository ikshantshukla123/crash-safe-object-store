// Package gc removes chunk bytes that no live metadata can reference.
// It runs in the API process because only the API owns the WAL writer, and GC
// deletions must be logged before they touch node files.
package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

// Worker sweeps deleted chunks and orphaned files.
type Worker struct {
	q            *dbgen.Queries
	nodes        []placement.Node
	log          *slog.Logger
	wal          *wal.Log
	grace        time.Duration
	orphanGrace  time.Duration
	batch        int32
	lastOrphanAt time.Time
}

func New(q *dbgen.Queries, nodes []placement.Node, log *slog.Logger, logWAL *wal.Log, grace, orphanGrace time.Duration, batch int32) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if grace <= 0 {
		grace = 24 * time.Hour
	}
	if orphanGrace <= 0 {
		orphanGrace = 24 * time.Hour
	}
	if batch <= 0 {
		batch = 100
	}
	return &Worker{q: q, nodes: nodes, log: log, wal: logWAL, grace: grace, orphanGrace: orphanGrace, batch: batch}
}

// Run executes immediately and then every interval.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce performs one database-garbage sweep and, at most hourly, one node
// file orphan sweep.
func (w *Worker) RunOnce(ctx context.Context) {
	if err := w.q.MarkUploadsExpired(ctx); err != nil {
		w.log.WarnContext(ctx, "gc: abort expired uploads", "err", err)
	}
	n, err := w.deleteGarbage(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "gc: delete garbage", "deleted", n, "err", err)
	}
	if time.Since(w.lastOrphanAt) >= time.Hour {
		w.lastOrphanAt = time.Now()
		if err := w.sweepOrphans(ctx); err != nil {
			w.log.WarnContext(ctx, "gc: orphan sweep", "err", err)
		}
	}
}

func (w *Worker) deleteGarbage(ctx context.Context) (int, error) {
	rows, err := w.q.ListGarbageChunks(ctx, dbgen.ListGarbageChunksParams{
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-w.grace), Valid: true},
		Limit:     w.batch,
	})
	if err != nil {
		return 0, fmt.Errorf("list garbage chunks: %w", err)
	}
	deleted := 0
	for _, chunk := range rows {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		// A staged chunk may have been committed after the query. Never
		// delete bytes that a version now references.
		current, err := w.q.GetChunk(ctx, chunk.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("recheck chunk %s: %w", uuidString(chunk.ID), err)
		}
		if current.State == "committed" {
			continue
		}
		replicas, err := w.q.ListReplicasForChunk(ctx, chunk.ID)
		if err != nil {
			return deleted, fmt.Errorf("list replicas: %w", err)
		}
		nodeIDs := make([]string, 0, len(replicas))
		for _, replica := range replicas {
			nodeIDs = append(nodeIDs, uuidString(replica.NodeID))
		}
		if err := w.deleteFiles(ctx, []string{uuidString(chunk.ID)}, nodeIDs); err != nil {
			return deleted, err
		}
		if err := w.q.MarkChunkDeleted(ctx, chunk.ID); err != nil {
			return deleted, fmt.Errorf("mark chunk deleted: %w", err)
		}
		if err := w.q.DeleteChunkRow(ctx, chunk.ID); err != nil {
			return deleted, fmt.Errorf("delete chunk row: %w", err)
		}
		deleted++
	}
	return deleted, nil
}

func (w *Worker) sweepOrphans(ctx context.Context) error {
	refs, err := w.q.ListReferencedChunkIDs(ctx)
	if err != nil {
		return fmt.Errorf("list referenced chunks: %w", err)
	}
	referenced := make(map[string]bool, len(refs))
	for _, id := range refs {
		referenced[uuidString(id)] = true
	}
	cutoff := time.Now().Add(-w.orphanGrace)
	for _, node := range w.nodes {
		lister, ok := node.Store.(objects.Lister)
		if !ok {
			continue
		}
		chunks, err := lister.List(ctx)
		if err != nil {
			w.log.WarnContext(ctx, "gc: list node chunks", "node_id", node.ID, "err", err)
			continue
		}
		for _, chunk := range chunks {
			if referenced[chunk.ID] || !chunk.Modified.Before(cutoff) {
				continue
			}
			if err := w.deleteFiles(ctx, []string{chunk.ID}, []string{node.ID}); err != nil {
				w.log.WarnContext(ctx, "gc: delete orphan", "chunk_id", chunk.ID, "node_id", node.ID, "err", err)
			}
		}
	}
	return nil
}

func (w *Worker) deleteFiles(ctx context.Context, chunkIDs, nodeIDs []string) error {
	if w.wal != nil {
		if _, err := w.wal.AppendSync(ctx, wal.RecordBeginDelete, wal.DeleteIntent{ChunkIDs: chunkIDs, NodeIDs: nodeIDs}); err != nil {
			return fmt.Errorf("append delete intent: %w", err)
		}
	}
	for _, id := range nodeIDs {
		node, ok := w.node(id)
		if !ok {
			continue
		}
		for _, chunkID := range chunkIDs {
			if err := node.Store.Delete(ctx, chunkID); err != nil && !objects.IsNotFound(err) {
				return fmt.Errorf("delete chunk %s from %s: %w", chunkID, node.Name, err)
			}
		}
	}
	if w.wal != nil {
		if _, err := w.wal.Append(ctx, wal.RecordDoneDelete, wal.DeleteIntent{ChunkIDs: chunkIDs, NodeIDs: nodeIDs}); err != nil {
			w.log.WarnContext(ctx, "gc: append done delete", "err", err)
		}
	}
	return nil
}

func (w *Worker) node(id string) (placement.Node, bool) {
	for _, n := range w.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	uid, err := uuid.FromBytes(u.Bytes[:])
	if err != nil {
		return ""
	}
	return uid.String()
}
