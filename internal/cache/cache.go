// Package cache wraps Redis. Every value here is derived data: losing it must
// never lose a fact that Postgres owns.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrMiss means the key is not cached.
var ErrMiss = errors.New("cache miss")

// Store is the small surface the coordinator needs, so tests can use a map.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Redis is the production Store.
type Redis struct {
	client *redis.Client
}

var _ Store = (*Redis)(nil)

func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, fmt.Errorf("cache get %s: %w", key, err)
	}
	return b, nil
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set %s: %w", key, err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// MetaKey is the mutable pointer to the current version. It has a short TTL
// and is deleted after every successful metadata commit.
func MetaKey(bucket, key string) string {
	return "meta:" + bucket + ":" + key
}

// ObjectKey is immutable by version id and never needs invalidation.
func ObjectKey(versionID string) string {
	return "obj:" + versionID
}
