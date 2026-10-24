package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

// Recovery resolves in-doubt WAL operations against Postgres and the nodes.
type Recovery struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	nodes  []placement.Node
	log    *slog.Logger
	quorum int
}

func NewRecovery(pool *pgxpool.Pool, q *dbgen.Queries, log *slog.Logger, nodes []placement.Node, quorum int) *Recovery {
	if log == nil {
		log = slog.Default()
	}
	return &Recovery{pool: pool, q: q, nodes: nodes, log: log, quorum: quorum}
}

var _ wal.Resolver = (*Recovery)(nil)

func (r *Recovery) node(id string) (placement.Node, bool) {
	for _, n := range r.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}

func (r *Recovery) probe(ctx context.Context, intent wal.PutIntent) map[string]string {
	statusByNode := make(map[string]string, len(intent.NodeIDs))
	for _, id := range intent.NodeIDs {
		node, ok := r.node(id)
		if !ok {
			continue
		}
		rc, err := node.Store.Get(ctx, intent.ChunkID)
		if err != nil {
			statusByNode[id] = "missing"
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, intent.SizeBytes+1))
		rc.Close()
		if readErr != nil || int64(len(data)) != intent.SizeBytes {
			statusByNode[id] = "corrupt"
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != intent.SHA256 {
			statusByNode[id] = "corrupt"
			continue
		}
		statusByNode[id] = "healthy"
	}
	return statusByNode
}

func (r *Recovery) goodCount(statuses map[string]string) int {
	n := 0
	for _, status := range statuses {
		if status == "healthy" {
			n++
		}
	}
	return n
}

func (r *Recovery) ResolvePut(ctx context.Context, intent wal.PutIntent) error {
	statuses := r.probe(ctx, intent)
	good := r.goodCount(statuses)
	chunkUUID, err := parseUUID(intent.ChunkID)
	if err != nil {
		return fmt.Errorf("parse intent chunk id: %w", err)
	}

	chunk, err := r.q.GetChunk(ctx, chunkUUID)
	switch {
	case err == nil:
		return r.resolveExistingChunk(ctx, intent, chunk, statuses, good)
	case errors.Is(err, pgx.ErrNoRows):
		return r.resolveMissingChunk(ctx, intent, chunkUUID, statuses, good)
	default:
		return fmt.Errorf("get chunk %s: %w", intent.ChunkID, err)
	}
}

func (r *Recovery) resolveExistingChunk(ctx context.Context, intent wal.PutIntent, chunk dbgen.Chunk, statuses map[string]string, good int) error {
	if chunk.State == "deleted" {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return nil
	}
	if good >= r.quorum {
		if err := r.ensureReplicas(ctx, chunk.ID, statuses); err != nil {
			return err
		}
		if chunk.State == "committed" {
			return nil
		}
		return r.finishPart(ctx, intent, chunk.ID, statuses)
	}
	if chunk.State == "committed" {
		r.log.ErrorContext(ctx, "in-doubt committed chunk has fewer than quorum copies",
			"chunk_id", intent.ChunkID, "copies", good, "quorum", r.quorum)
		return nil
	}
	r.deleteFromNodes(ctx, intent.ChunkID, statuses)
	return r.abortPart(ctx, intent, chunk.ID)
}

func (r *Recovery) resolveMissingChunk(ctx context.Context, intent wal.PutIntent, chunkID pgtype.UUID, statuses map[string]string, good int) error {
	if good < r.quorum {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return nil
	}
	uploadUUID, err := parseUUID(intent.UploadID)
	if err != nil {
		return fmt.Errorf("parse upload id: %w", err)
	}
	upload, err := r.q.GetUpload(ctx, uploadUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get upload %s: %w", intent.UploadID, err)
	}
	if upload.Status != "open" {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin recovery transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := r.q.WithTx(tx)

	row, err := qtx.UpsertStagedChunk(ctx, dbgen.UpsertStagedChunkParams{
		ID:         chunkID,
		ChunkIndex: intent.ChunkIndex,
		SizeBytes:  intent.SizeBytes,
		Sha256:     intent.SHA256,
	})
	if err != nil {
		return fmt.Errorf("recover staged chunk: %w", err)
	}
	if row.State == "deleted" {
		return nil
	}
	if err := qtx.UpsertUploadPart(ctx, dbgen.UpsertUploadPartParams{
		UploadID:  upload.ID,
		PartNo:    intent.PartNo,
		ChunkID:   chunkID,
		SizeBytes: intent.SizeBytes,
		Sha256:    intent.SHA256,
	}); err != nil {
		return fmt.Errorf("recover upload part: %w", err)
	}
	if err := r.ensureReplicasTx(ctx, qtx, chunkID, statuses); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recovery transaction: %w", err)
	}
	return nil
}

func (r *Recovery) finishPart(ctx context.Context, intent wal.PutIntent, chunkID pgtype.UUID, statuses map[string]string) error {
	uploadUUID, err := parseUUID(intent.UploadID)
	if err != nil {
		return fmt.Errorf("parse upload id: %w", err)
	}
	upload, err := r.q.GetUpload(ctx, uploadUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return r.abortPart(ctx, intent, chunkID)
	}
	if err != nil {
		return fmt.Errorf("get upload %s: %w", intent.UploadID, err)
	}
	if upload.Status != "open" {
		r.deleteFromNodes(ctx, intent.ChunkID, statuses)
		return r.abortPart(ctx, intent, chunkID)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin recovery transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := r.q.WithTx(tx)
	if err := qtx.UpsertUploadPart(ctx, dbgen.UpsertUploadPartParams{
		UploadID:  uploadUUID,
		PartNo:    intent.PartNo,
		ChunkID:   chunkID,
		SizeBytes: intent.SizeBytes,
		Sha256:    intent.SHA256,
	}); err != nil {
		return fmt.Errorf("recover upload part: %w", err)
	}
	if err := r.ensureReplicasTx(ctx, qtx, chunkID, statuses); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recovery transaction: %w", err)
	}
	return nil
}

func (r *Recovery) ensureReplicas(ctx context.Context, chunkID pgtype.UUID, statuses map[string]string) error {
	return r.ensureReplicasQ(ctx, r.q, chunkID, statuses)
}

func (r *Recovery) ensureReplicasTx(ctx context.Context, qtx *dbgen.Queries, chunkID pgtype.UUID, statuses map[string]string) error {
	return r.ensureReplicasQ(ctx, qtx, chunkID, statuses)
}

func (r *Recovery) ensureReplicasQ(ctx context.Context, q *dbgen.Queries, chunkID pgtype.UUID, statuses map[string]string) error {
	for id, status := range statuses {
		nodeUUID, err := parseUUID(id)
		if err != nil {
			return err
		}
		if err := q.UpsertChunkReplica(ctx, dbgen.UpsertChunkReplicaParams{
			ChunkID: chunkID,
			NodeID:  nodeUUID,
			Status:  status,
		}); err != nil {
			return fmt.Errorf("recover replica %s: %w", id, err)
		}
	}
	return nil
}

func (r *Recovery) abortPart(ctx context.Context, intent wal.PutIntent, chunkID pgtype.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin abort transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := r.q.WithTx(tx)
	if err := qtx.DeleteUploadPartByChunk(ctx, chunkID); err != nil {
		return fmt.Errorf("delete recovered upload part: %w", err)
	}
	if _, err := qtx.DeleteStagedChunk(ctx, chunkID); err != nil {
		return fmt.Errorf("delete recovered staged chunk: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit abort transaction: %w", err)
	}
	return nil
}

func (r *Recovery) deleteFromNodes(ctx context.Context, chunkID string, statuses map[string]string) {
	for id := range statuses {
		node, ok := r.node(id)
		if !ok {
			continue
		}
		if err := node.Store.Delete(ctx, chunkID); err != nil && !objects.IsNotFound(err) {
			r.log.WarnContext(ctx, "deleting partial chunk during recovery", "chunk_id", chunkID, "node_id", id, "err", err)
		}
	}
}

func (r *Recovery) ResolveDelete(ctx context.Context, intent wal.DeleteIntent) error {
	for _, chunkID := range intent.ChunkIDs {
		for _, nodeID := range intent.NodeIDs {
			node, ok := r.node(nodeID)
			if !ok {
				continue
			}
			if err := node.Store.Delete(ctx, chunkID); err != nil && !objects.IsNotFound(err) {
				return fmt.Errorf("delete recovered chunk %s from %s: %w", chunkID, nodeID, err)
			}
		}
		chunkUUID, err := parseUUID(chunkID)
		if err != nil {
			return err
		}
		if err := r.q.MarkChunkDeleted(ctx, chunkUUID); err != nil {
			return fmt.Errorf("mark recovered chunk deleted: %w", err)
		}
	}
	return nil
}
