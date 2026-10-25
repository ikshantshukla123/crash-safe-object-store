// Package queue carries idempotent background jobs through Redis. Losing a
// job is recoverable: sweepers re-enqueue work that has no durable result.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const embeddingKey = "queue:embeddings"

type Embeddings struct {
	client *redis.Client
}

func NewEmbeddings(client *redis.Client) *Embeddings {
	return &Embeddings{client: client}
}

func (q *Embeddings) Enqueue(ctx context.Context, versionID string) error {
	if versionID == "" {
		return errors.New("enqueue embedding: empty version id")
	}
	if err := q.client.LPush(ctx, embeddingKey, versionID).Err(); err != nil {
		return fmt.Errorf("enqueue embedding %s: %w", versionID, err)
	}
	return nil
}

// Dequeue blocks for at most timeout. redis.Nil means the queue was empty.
func (q *Embeddings) Dequeue(ctx context.Context, timeout time.Duration) (string, error) {
	res, err := q.client.BRPop(ctx, timeout, embeddingKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dequeue embedding: %w", err)
	}
	if len(res) != 2 {
		return "", fmt.Errorf("unexpected BRPOP result length %d", len(res))
	}
	return res[1], nil
}
