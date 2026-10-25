package coordinator_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/cache"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
)

func TestRollbackMovesCurrentPointerIdempotently(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "rollback.txt", "text/plain", "v1", bytes.NewReader([]byte("first")))
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	second, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "rollback.txt", "text/plain", "v2", bytes.NewReader([]byte("second")))
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if got := string(h.get(t, "rollback.txt")); got != "second" {
		t.Fatalf("current = %q, want second", got)
	}

	versions, err := h.coord.ListVersions(ctx, h.ownerID, h.bucket.Name, "rollback.txt")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 || versions[0].ID != second.ID || versions[1].ID != first.ID {
		t.Fatalf("versions = %+v, want newest first", versions)
	}

	for i := 0; i < 2; i++ {
		if _, err := h.coord.Rollback(ctx, h.ownerID, h.bucket.Name, "rollback.txt", first.ID.String()); err != nil {
			t.Fatalf("rollback %d: %v", i, err)
		}
		if got := string(h.get(t, "rollback.txt")); got != "first" {
			t.Fatalf("after rollback %d = %q, want first", i, got)
		}
	}

	versions, err = h.coord.ListVersions(ctx, h.ownerID, h.bucket.Name, "rollback.txt")
	if err != nil {
		t.Fatalf("list after rollback: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("rollback created a version: got %d, want 2", len(versions))
	}

	other, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "other.txt", "text/plain", "other", bytes.NewReader([]byte("other")))
	if err != nil {
		t.Fatalf("put other: %v", err)
	}
	if _, err := h.coord.Rollback(ctx, h.ownerID, h.bucket.Name, "rollback.txt", other.ID.String()); !errors.Is(err, coordinator.ErrNotFound) {
		t.Fatalf("cross-object rollback error = %v, want ErrNotFound", err)
	}
}

func TestSmallObjectCacheAndInvalidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	store := newMemStore()
	reg := metrics.New()
	c, err := coordinator.New(h.db.Pool, h.q, slog.New(slog.DiscardHandler), h.walLog, h.nodes, coordinator.Config{
		Replicas: 3, WriteQuorum: 2, MaxPartSize: 16 << 20, MultipartExpiry: time.Hour,
		Cache: store, CacheTTL: time.Minute, SmallObjectLimit: 1 << 20, Metrics: reg,
	})
	if err != nil {
		t.Fatalf("coordinator with cache: %v", err)
	}

	put := func(body, idem string) {
		t.Helper()
		if _, err := c.PutObject(ctx, h.ownerID, h.bucket.Name, "cached.txt", "text/plain", idem, bytes.NewReader([]byte(body))); err != nil {
			t.Fatalf("put %s: %v", idem, err)
		}
	}
	read := func() string {
		t.Helper()
		reader, err := c.GetObject(ctx, h.ownerID, h.bucket.Name, "cached.txt")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}

	put("cache me", "one")
	if got := read(); got != "cache me" {
		t.Fatalf("read = %q", got)
	}
	snap := reg.Snapshot()
	if snap.CacheMisses != 1 || snap.CacheHits != 0 {
		t.Fatalf("after first read hits=%d misses=%d, want 0/1", snap.CacheHits, snap.CacheMisses)
	}
	if got := read(); got != "cache me" {
		t.Fatalf("cached read = %q", got)
	}
	snap = reg.Snapshot()
	if snap.CacheHits != 1 || snap.CacheMisses != 1 {
		t.Fatalf("after cached read hits=%d misses=%d, want 1/1", snap.CacheHits, snap.CacheMisses)
	}

	put("new bytes", "two")
	if got := read(); got != "new bytes" {
		t.Fatalf("read after overwrite = %q, want new bytes", got)
	}
	if err := c.DeleteObject(ctx, h.ownerID, h.bucket.Name, "cached.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.GetObject(ctx, h.ownerID, h.bucket.Name, "cached.txt"); !errors.Is(err, coordinator.ErrNotFound) {
		t.Fatalf("get after delete error = %v, want ErrNotFound (stale meta cache?)", err)
	}
}

type memStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

var _ cache.Store = (*memStore)(nil)

func newMemStore() *memStore { return &memStore{values: make(map[string][]byte)} }

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}

func (m *memStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = append([]byte(nil), value...)
	return nil
}

func (m *memStore) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		delete(m.values, key)
	}
	return nil
}
