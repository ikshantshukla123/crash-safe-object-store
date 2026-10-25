package coordinator_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/search"
)

func TestEmbeddingWorkerAndSearchAreIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := []byte("Quarterly revenue report for the north region. Revenue grew across every district.")
	version, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "reports/revenue.txt", "text/plain", "search", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	other, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "reports/security.txt", "text/plain", "search-other",
		bytes.NewReader([]byte("Security audit remediation tracker and access review notes.")))
	if err != nil {
		t.Fatalf("put other: %v", err)
	}

	embedder, err := search.NewEmbedder("text-embedding-3-small", "", 1536)
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	reg := metrics.New()
	worker := search.NewWorker(h.q, h.nodes, embedder, nil, slog.New(slog.DiscardHandler), reg)
	for i := 0; i < 2; i++ {
		if err := worker.ProcessVersion(ctx, version.ID.String()); err != nil {
			t.Fatalf("process version %d: %v", i, err)
		}
	}
	var rows int
	if err := h.db.Pool.QueryRow(ctx, "SELECT count(*) FROM object_embeddings WHERE version_id = $1", version.ID).Scan(&rows); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if rows != 1 {
		t.Fatalf("embedding rows = %d, want 1 after duplicate processing", rows)
	}
	if err := worker.ProcessVersion(ctx, other.ID.String()); err != nil {
		t.Fatalf("process other: %v", err)
	}

	searcher := search.NewSearcher(h.q, embedder)
	hits, err := searcher.Search(ctx, h.ownerID, "quarterly revenue", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Key != "reports/revenue.txt" {
		t.Fatalf("top hit = %s, want revenue report", hits[0].Key)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("scores not ordered: %v", hits)
	}
	if hits[0].Score < 0.8 {
		t.Fatalf("top score = %.3f, want high-confidence", hits[0].Score)
	}
	if tier := search.Tier(hits[0].Score); tier != "High Potential" {
		t.Fatalf("tier = %q, want High Potential", tier)
	}

	missing, err := h.q.ListVersionsMissingEmbedding(ctx, 10)
	if err != nil {
		t.Fatalf("list missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("versions still missing embeddings: %v", missing)
	}
}
