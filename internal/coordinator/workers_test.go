package coordinator_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/gc"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/repair"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/replicator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/scrubber"
)

func TestReplicatorCopiesPendingReplica(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := []byte("replicate me across nodes")
	version, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "worker.txt", "text/plain", "worker", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	chunk := h.chunkForVersion(t, version.ID.String())

	if err := os.Remove(chunkPath(h.roots[2], chunk)); err != nil {
		t.Fatalf("remove node3 copy: %v", err)
	}
	if err := h.q.SetReplicaStatus(ctx, dbgen.SetReplicaStatusParams{
		ChunkID: mustUUID(t, chunk), NodeID: mustUUID(t, h.nodes[2].ID), Status: "pending",
	}); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	r := replicator.New(h.q, h.nodes, slog.New(slog.DiscardHandler), nil, 50, 4)
	n, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("replicator: %v", err)
	}
	if n != 1 {
		t.Fatalf("replicated %d, want 1", n)
	}
	data, err := os.ReadFile(chunkPath(h.roots[2], chunk))
	if err != nil {
		t.Fatalf("node3 copy missing after replication: %v", err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("node3 bytes = %q, want %q", data, body)
	}
	if healthy := h.healthyReplicas(t, chunk); healthy != 3 {
		t.Fatalf("healthy replicas = %d, want 3", healthy)
	}
	if n, err := r.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second run = (%d,%v), want (0,nil)", n, err)
	}
}

func TestScrubberMarksCorruptAndRepairRestores(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	version, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "scrub.txt", "text/plain", "scrub", bytes.NewReader([]byte("scrub payload")))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	chunk := h.chunkForVersion(t, version.ID.String())
	if err := os.WriteFile(chunkPath(h.roots[1], chunk), []byte("not the payload"), 0o600); err != nil {
		t.Fatalf("corrupt node2: %v", err)
	}

	s := scrubber.New(h.q, h.nodes, slog.New(slog.DiscardHandler), nil, 50)
	if _, err := s.ScrubNode(ctx, h.nodes[1]); err != nil {
		t.Fatalf("scrub node2: %v", err)
	}
	if status := h.replicaStatus(t, chunk, h.nodes[1].ID); status != "corrupt" {
		t.Fatalf("node2 status = %q, want corrupt", status)
	}

	r := repair.New(h.q, h.nodes, slog.New(slog.DiscardHandler), nil, 20, 3)
	n, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if n != 1 {
		t.Fatalf("repaired %d, want 1", n)
	}
	if status := h.replicaStatus(t, chunk, h.nodes[1].ID); status != "healthy" {
		t.Fatalf("node2 status after repair = %q, want healthy", status)
	}
	data, err := os.ReadFile(chunkPath(h.roots[1], chunk))
	if err != nil {
		t.Fatalf("read repaired node2: %v", err)
	}
	if string(data) != "scrub payload" {
		t.Fatalf("repaired bytes = %q", data)
	}
}

func TestGCDeletesStagedChunksAndOrphans(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	chunkID := uuid.New()
	chunk := chunkID.String()
	body := []byte("garbage staged chunk")
	sum := sha256.Sum256(body)
	if _, err := h.db.Pool.Exec(ctx, `
		INSERT INTO chunks (id, chunk_index, size_bytes, sha256, state, created_at)
		VALUES ($1, 0, $2, $3, 'staged', now() - interval '2 hours')`,
		chunkID, len(body), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("insert staged chunk: %v", err)
	}
	for i, node := range h.nodes {
		if err := h.q.UpsertChunkReplica(ctx, dbgen.UpsertChunkReplicaParams{
			ChunkID: pgtype.UUID{Bytes: chunkID, Valid: true},
			NodeID:  mustUUID(t, node.ID),
			Status:  "healthy",
		}); err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
		path := chunkPath(h.roots[i], chunk)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
	}

	orphan := "deadbeef-0000-0000-0000-000000000000"
	orphanPath := chunkPath(h.roots[0], orphan)
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatalf("age orphan: %v", err)
	}

	w := gc.New(h.q, h.nodes, log, h.walLog, time.Hour, time.Hour, 100)
	w.RunOnce(ctx)

	var count int
	if err := h.db.Pool.QueryRow(ctx, "SELECT count(*) FROM chunks WHERE id = $1", chunkID).Scan(&count); err != nil {
		t.Fatalf("count chunk: %v", err)
	}
	if count != 0 {
		t.Fatalf("staged chunk row survived GC")
	}
	for i := range h.nodes {
		if _, err := os.Stat(chunkPath(h.roots[i], chunk)); !os.IsNotExist(err) {
			t.Fatalf("node%d chunk file survived GC (err=%v)", i+1, err)
		}
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphan survived GC (err=%v)", err)
	}
}

func (h *harness) chunkForVersion(t *testing.T, versionID string) string {
	t.Helper()
	var chunk pgtype.UUID
	if err := h.db.Pool.QueryRow(context.Background(),
		"SELECT id FROM chunks WHERE version_id = $1 AND state = 'committed'", versionID).Scan(&chunk); err != nil {
		t.Fatalf("find chunk for version %s: %v", versionID, err)
	}
	return uuid.UUID(chunk.Bytes).String()
}

func (h *harness) healthyReplicas(t *testing.T, chunk string) int {
	t.Helper()
	var n int
	if err := h.db.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM chunk_replicas WHERE chunk_id = $1 AND status = 'healthy'", chunk).Scan(&n); err != nil {
		t.Fatalf("count healthy replicas: %v", err)
	}
	return n
}

func (h *harness) replicaStatus(t *testing.T, chunk, nodeID string) string {
	t.Helper()
	var status string
	if err := h.db.Pool.QueryRow(context.Background(),
		"SELECT status FROM chunk_replicas WHERE chunk_id = $1 AND node_id = $2", chunk, nodeID).Scan(&status); err != nil {
		t.Fatalf("replica status: %v", err)
	}
	return status
}

func chunkPath(root, chunk string) string {
	return filepath.Join(root, chunk[:2], chunk)
}
