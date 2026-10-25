// Package repair restores the replication factor for chunks that lost copies.
package repair

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
)

// Repairer recreates missing replicas from a verified healthy source. It never
// marks a replica healthy without copying and hashing the bytes first.
type Repairer struct {
	q       *dbgen.Queries
	nodes   []placement.Node
	log     *slog.Logger
	metrics *metrics.Registry
	batch   int32
	target  int32
}

func New(q *dbgen.Queries, nodes []placement.Node, log *slog.Logger, reg *metrics.Registry, batch, target int32) *Repairer {
	if log == nil {
		log = slog.Default()
	}
	if batch <= 0 {
		batch = 20
	}
	if target <= 0 {
		target = 3
	}
	return &Repairer{q: q, nodes: nodes, log: log, metrics: reg, batch: batch, target: target}
}

// RunOnce repairs one batch, most at-risk chunks first.
func (r *Repairer) RunOnce(ctx context.Context) (int, error) {
	chunks, err := r.q.ListUnderReplicatedChunks(ctx, dbgen.ListUnderReplicatedChunksParams{
		TargetReplicas: r.target,
		LimitCount:     r.batch,
	})
	if err != nil {
		return 0, fmt.Errorf("list under-replicated chunks: %w", err)
	}
	repaired := 0
	for _, chunk := range chunks {
		n, err := r.repairChunk(ctx, chunk)
		if err != nil {
			r.log.WarnContext(ctx, "repair chunk", "chunk_id", uuidString(chunk.ID), "err", err)
			if r.metrics != nil {
				r.metrics.RepairFailures.Add(1)
			}
			continue
		}
		repaired += n
	}
	return repaired, nil
}

func (r *Repairer) repairChunk(ctx context.Context, chunk dbgen.ListUnderReplicatedChunksRow) (int, error) {
	replicas, err := r.q.ListReplicasForChunk(ctx, chunk.ID)
	if err != nil {
		return 0, fmt.Errorf("list replicas: %w", err)
	}

	var source placement.Node
	for _, replica := range replicas {
		if replica.Status != "healthy" || replica.NodeStatus != "up" {
			continue
		}
		node, ok := r.node(uuidString(replica.NodeID))
		if ok {
			source = node
			break
		}
	}
	if source.ID == "" {
		if r.metrics != nil {
			r.metrics.DataLossAlerts.Add(1)
		}
		r.log.ErrorContext(ctx, "DATA LOSS RISK: no healthy source for chunk",
			"chunk_id", uuidString(chunk.ID), "healthy_replicas", chunk.HealthyReplicas)
		return 0, nil
	}

	healthyOn := make(map[string]bool, len(replicas))
	for _, replica := range replicas {
		if replica.Status == "healthy" && replica.NodeStatus == "up" {
			healthyOn[uuidString(replica.NodeID)] = true
		}
	}

	repaired := 0
	for _, node := range r.nodes {
		if node.ID == source.ID || healthyOn[node.ID] {
			continue
		}
		if !r.nodeUp(ctx, node.ID) {
			continue
		}
		start := time.Now()
		if err := objects.CopyVerified(ctx, source.Store, node.Store, uuidString(chunk.ID), chunk.SizeBytes, chunk.Sha256); err != nil {
			return repaired, fmt.Errorf("copy to %s: %w", node.Name, err)
		}
		if err := r.q.UpsertChunkReplica(ctx, dbgen.UpsertChunkReplicaParams{
			ChunkID: chunk.ID,
			NodeID:  mustUUID(node.ID),
			Status:  "healthy",
		}); err != nil {
			return repaired, fmt.Errorf("mark %s healthy: %w", node.Name, err)
		}
		if r.metrics != nil {
			r.metrics.ObserveRepair(time.Since(start))
		}
		repaired++
		healthyOn[node.ID] = true
	}
	return repaired, nil
}

func (r *Repairer) nodeUp(ctx context.Context, id string) bool {
	rows, err := r.q.ListStorageNodes(ctx)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if uuidString(row.ID) == id {
			return row.Status == "up"
		}
	}
	return false
}

func (r *Repairer) node(id string) (placement.Node, bool) {
	for _, n := range r.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}

func parseUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func mustUUID(s string) pgtype.UUID {
	u, err := parseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// Interval is the default tick.
const Interval = 15 * time.Second
