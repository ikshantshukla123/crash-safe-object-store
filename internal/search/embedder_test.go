package search

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestLocalEmbedderSimilarityAndShape(t *testing.T) {
	embedder, err := NewEmbedder("text-embedding-3-small", "", 1536)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	query, model, err := embedder.Embed(context.Background(), "quarterly revenue report")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if model != "local-hash-1536" {
		t.Fatalf("model = %q", model)
	}
	if len(query) != 1536 {
		t.Fatalf("dimensions = %d", len(query))
	}
	match, _, _ := embedder.Embed(context.Background(), "quarterly revenue report for the north region")
	miss, _, _ := embedder.Embed(context.Background(), "warehouse inventory reconciliation")
	if cosine(query, match) <= cosine(query, miss) {
		t.Fatalf("query should be closer to matching text")
	}
}

func TestVectorString(t *testing.T) {
	raw := VectorString([]float32{0.5, -1, 0})
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		t.Fatalf("vector = %q", raw)
	}
	if strings.Count(raw, ",") != 2 {
		t.Fatalf("vector = %q", raw)
	}
}

func TestTierBoundaries(t *testing.T) {
	cases := map[float64]string{
		0.95: "High Potential", 0.80: "High Potential",
		0.79: "Good", 0.60: "Good",
		0.59: "Worth Exploring", 0.40: "Worth Exploring",
		0.39: "Low",
	}
	for score, want := range cases {
		if got := Tier(score); got != want {
			t.Errorf("Tier(%v) = %q, want %q", score, got, want)
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
