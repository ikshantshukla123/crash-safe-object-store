// Package search builds embeddings and answers owner-scoped semantic search.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Embedder turns text into a fixed-width vector.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, string, error)
	Dimensions() int
}

// NewEmbedder returns OpenAI when a key is configured, otherwise a
// deterministic local hashing embedder so the demo works offline.
func NewEmbedder(model, apiKey string, dimensions int) (Embedder, error) {
	if dimensions <= 0 {
		dimensions = 1536
	}
	if dimensions != 1536 {
		return nil, fmt.Errorf("embedding dimension must be 1536 for the schema, got %d", dimensions)
	}
	if apiKey == "" {
		return &Local{model: "local-hash-1536", dims: dimensions}, nil
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAI{model: model, key: apiKey, dims: dimensions, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Local is a deterministic bag-of-tokens embedder. It is not a semantic model;
// it keeps the pipeline, pgvector query and dashboard testable without a
// network key. Production uses OpenAI.
type Local struct {
	model string
	dims  int
}

func (l *Local) Dimensions() int { return l.dims }
func (l *Local) Model() string   { return l.model }

func (l *Local) Embed(_ context.Context, text string) ([]float32, string, error) {
	vec := make([]float32, l.dims)
	tokens := tokenize(text)
	features := make(map[string]float32, len(tokens)*2)
	for i, token := range tokens {
		features[token] = 1
		if i+1 < len(tokens) {
			features[token+" "+tokens[i+1]] = 1.6
		}
	}
	for feature, weight := range features {
		h := fnv.New64a()
		_, _ = h.Write([]byte(feature))
		sum := h.Sum64()
		idx := int(sum % uint64(l.dims))
		sign := float32(1)
		if sum&(1<<63) != 0 {
			sign = -1
		}
		vec[idx] += sign * weight
	}
	normalize(vec)
	return vec, l.model, nil
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v * v)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range vec {
		vec[i] *= inv
	}
}

// OpenAI calls the embeddings API over plain HTTP; one endpoint does not
// justify a vendor SDK and its dependency tree.
type OpenAI struct {
	model  string
	key    string
	dims   int
	client *http.Client
}

func (o *OpenAI) Dimensions() int { return o.dims }

type openAIRequest struct {
	Model      string `json:"model"`
	Input      string `json:"input"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type openAIResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, string, error) {
	body, err := json.Marshal(openAIRequest{Model: o.model, Input: text, Dimensions: o.dims})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("openai embeddings: %w", err)
	}
	defer resp.Body.Close()
	var out openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("decode openai embeddings: %w", err)
	}
	if resp.StatusCode != http.StatusOK || len(out.Data) == 0 || len(out.Data[0].Embedding) != o.dims {
		msg := "empty response"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return nil, "", fmt.Errorf("openai embeddings status %d: %s", resp.StatusCode, msg)
	}
	return out.Data[0].Embedding, o.model, nil
}

// VectorString formats a vector for pgvector's text input.
func VectorString(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%.7f", v)
	}
	b.WriteByte(']')
	return b.String()
}
