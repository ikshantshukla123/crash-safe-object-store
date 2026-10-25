// Package scrubber re-hashes stored chunks, oldest-verified first.
package scrubber

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
)

// Scrubber verifies one replica at a time and repairs its status row.
type Scrubber struct {
	q       *dbgen.Queries
	nodes   []placement.Node
	log     *slog.Logger
	metrics *metrics.Registry
	batch   int32
}

func New(q *dbgen.Queries, nodes []placement.Node, log *slog.Logger, reg *metrics.Registry, batch int32) *Scrubber {
	if log == nil {
		log = slog.Default()
	}
	if batch <= 0 {
		batch = 50
	}
	return &Scrubber{q: q, nodes: nodes, log: log, metrics: reg, batch: batch}
}

// RunOnce scrubs one batch and returns how many replicas were checked.
func (s *Scrubber) RunOnce(ctx context.Context) (int, error) {
	rows, err := s.q.ListScrubCandidates(ctx, s.batch)
	if err != nil {
		return 0, fmt.Errorf("list scrub candidates: %w", err)
	}
	checked := 0
	for _, row := range rows {
		node, ok := s.node(uuidString(row.NodeID))
		if !ok {
			continue
		}
		if err := s.scrub(ctx, node, row.ID, row.SizeBytes, row.Sha256); err != nil {
			s.log.WarnContext(ctx, "scrub failed", "chunk_id", uuidString(row.ID), "node_id", node.ID, "err", err)
			if s.metrics != nil {
				s.metrics.ScrubFailures.Add(1)
			}
		}
		checked++
	}
	return checked, nil
}

// ScrubNode checks every chunk expected on one node. The health checker calls
// this on a down-to-up transition.
func (s *Scrubber) ScrubNode(ctx context.Context, node placement.Node) (int, error) {
	nodeUUID, err := parseUUID(node.ID)
	if err != nil {
		return 0, err
	}
	rows, err := s.q.ListChunksForNode(ctx, nodeUUID)
	if err != nil {
		return 0, fmt.Errorf("list chunks for node %s: %w", node.Name, err)
	}
	checked := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return checked, err
		}
		if err := s.scrub(ctx, node, row.ID, row.SizeBytes, row.Sha256); err != nil {
			s.log.WarnContext(ctx, "node scrub failed", "chunk_id", uuidString(row.ID), "node_id", node.ID, "err", err)
			if s.metrics != nil {
				s.metrics.ScrubFailures.Add(1)
			}
		}
		checked++
	}
	return checked, nil
}

func (s *Scrubber) scrub(ctx context.Context, node placement.Node, chunkID pgtype.UUID, size int64, sha string) error {
	rc, err := node.Store.Get(ctx, uuidString(chunkID))
	if err != nil {
		if objects.IsNotFound(err) {
			return s.setStatus(ctx, chunkID, node.ID, "missing")
		}
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, size+1))
	rc.Close()
	if readErr != nil {
		return readErr
	}
	if int64(len(data)) != size {
		if err := s.setStatus(ctx, chunkID, node.ID, "corrupt"); err != nil {
			return err
		}
		return fmt.Errorf("chunk has %d bytes, expected %d", len(data), size)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		if err := s.setStatus(ctx, chunkID, node.ID, "corrupt"); err != nil {
			return err
		}
		return fmt.Errorf("chunk checksum mismatch")
	}
	return s.setStatus(ctx, chunkID, node.ID, "healthy")
}

func (s *Scrubber) setStatus(ctx context.Context, chunkID pgtype.UUID, nodeID, status string) error {
	nodeUUID, err := parseUUID(nodeID)
	if err != nil {
		return err
	}
	return s.q.SetReplicaStatus(ctx, dbgen.SetReplicaStatusParams{
		ChunkID: chunkID,
		NodeID:  nodeUUID,
		Status:  status,
	})
}

func (s *Scrubber) node(id string) (placement.Node, bool) {
	for _, n := range s.nodes {
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

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// Interval is the default throttle between batches.
const Interval = 30 * time.Second
