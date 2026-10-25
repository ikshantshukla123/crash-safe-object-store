package search

import (
	"context"
	"fmt"
	"math"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

// Searcher embeds a query and asks pgvector for the closest owner-owned
// objects. The index and the ordering use the same cosine distance operator.
type Searcher struct {
	q        *dbgen.Queries
	embedder Embedder
}

func NewSearcher(q *dbgen.Queries, embedder Embedder) *Searcher {
	return &Searcher{q: q, embedder: embedder}
}

func (s *Searcher) Search(ctx context.Context, ownerID, query string, limit int32) ([]dbgen.SearchObjectsRow, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	owner, err := parseUUID(ownerID)
	if err != nil {
		return nil, fmt.Errorf("invalid owner: %w", err)
	}
	vec, model, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.SearchObjects(ctx, dbgen.SearchObjectsParams{
		QueryEmbedding: VectorString(vec),
		OwnerID:        owner,
		LimitCount:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if model == "local-hash-1536" {
		// The offline fallback is lexical, not semantic, so its cosine values
		// sit lower than OpenAI's. Scale them for stable dashboard tiers.
		for i := range rows {
			rows[i].Score = math.Min(1, rows[i].Score*3.0)
		}
	}
	return rows, nil
}

// Tier maps a cosine score to the dashboard's semantic label.
func Tier(score float64) string {
	switch {
	case score >= 0.80:
		return "High Potential"
	case score >= 0.60:
		return "Good"
	case score >= 0.40:
		return "Worth Exploring"
	default:
		return "Low"
	}
}
