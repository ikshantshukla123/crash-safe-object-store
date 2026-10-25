// Package replicator copies chunks to replicas recorded as pending by the
// write path, turning them into healthy copies in the background.
package replicator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
)

// Replicator pulls pending rows, copies from a healthy source and verifies the
// bytes before marking the replica healthy.
type Replicator struct {
	q           *dbgen.Queries
	nodes       []placement.Node
	log         *slog.Logger
	metrics     *metrics.Registry
	batch       int32
	concurrency int
}

func New(q *dbgen.Queries, nodes []placement.Node, log *slog.Logger, reg *metrics.Registry, batch, concurrency int32) *Replicator {
	if log == nil {
		log = slog.Default()
	}
	if batch <= 0 {
		batch = 50
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Replicator{q: q, nodes: nodes, log: log, metrics: reg, batch: batch, concurrency: int(concurrency)}
}

// RunOnce processes one batch and returns how many replicas became healthy.
func (r *Replicator) RunOnce(ctx context.Context) (int, error) {
	pending, err := r.q.ListPendingReplicas(ctx, r.batch)
	if err != nil {
		return 0, fmt.Errorf("list pending replicas: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	repaired := 0
	for _, row := range pending {
		row := row
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ok, err := r.replicate(ctx, row)
			if err != nil {
				r.log.WarnContext(ctx, "replicate chunk", "chunk_id", uuidString(row.ChunkID), "node_id", uuidString(row.NodeID), "err", err)
				return
			}
			if ok {
				mu.Lock()
				repaired++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return repaired, nil
}

func (r *Replicator) replicate(ctx context.Context, row dbgen.ListPendingReplicasRow) (bool, error) {
	chunk, err := r.q.GetChunk(ctx, row.ChunkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get chunk: %w", err)
	}
	if chunk.State == "deleted" {
		return false, r.q.SetReplicaStatus(ctx, dbgen.SetReplicaStatusParams{
			ChunkID: row.ChunkID,
			NodeID:  row.NodeID,
			Status:  "missing",
		})
	}

	target, ok := r.node(uuidString(row.NodeID))
	if !ok || target.ID == "" {
		return false, nil
	}
	sources, err := r.q.ListReplicasForChunk(ctx, row.ChunkID)
	if err != nil {
		return false, fmt.Errorf("list replicas: %w", err)
	}

	var lastErr error
	for _, source := range sources {
		if source.Status != "healthy" || source.NodeStatus != "up" || uuidString(source.NodeID) == target.ID {
			continue
		}
		sourceNode, ok := r.node(uuidString(source.NodeID))
		if !ok {
			continue
		}
		start := time.Now()
		if err := objects.CopyVerified(ctx, sourceNode.Store, target.Store, uuidString(chunk.ID), chunk.SizeBytes, chunk.Sha256); err != nil {
			lastErr = err
			continue
		}
		if err := r.q.UpsertChunkReplica(ctx, dbgen.UpsertChunkReplicaParams{
			ChunkID: chunk.ID,
			NodeID:  row.NodeID,
			Status:  "healthy",
		}); err != nil {
			return false, fmt.Errorf("mark replica healthy: %w", err)
		}
		if r.metrics != nil {
			r.metrics.ObserveRepair(time.Since(start))
		}
		return true, nil
	}
	if lastErr != nil {
		return false, fmt.Errorf("no source succeeded: %w", lastErr)
	}
	return false, nil
}

func (r *Replicator) node(id string) (placement.Node, bool) {
	for _, n := range r.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}

func uuidString(id pgtype.UUID) string {
	return uuid.UUID(id.Bytes).String()
}
