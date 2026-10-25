package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/cache"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/queue"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

// Config holds the coordinator's replication and size limits.
type Config struct {
	Replicas        int
	WriteQuorum     int
	MaxPartSize     int64
	MultipartExpiry time.Duration
	Now             func() time.Time

	Cache            cache.Store
	CacheTTL         time.Duration
	SmallObjectLimit int64
	Metrics          *metrics.Registry
	Embeddings       *queue.Embeddings
}

// Coordinator owns every mutating object path.
type Coordinator struct {
	pool  *pgxpool.Pool
	q     *dbgen.Queries
	log   *slog.Logger
	wal   *wal.Log
	nodes []placement.Node
	cfg   Config
}

func New(pool *pgxpool.Pool, q *dbgen.Queries, log *slog.Logger, logWAL *wal.Log, nodes []placement.Node, cfg Config) (*Coordinator, error) {
	if pool == nil {
		return nil, fmt.Errorf("coordinator: nil pool")
	}
	if q == nil {
		return nil, fmt.Errorf("coordinator: nil queries")
	}
	if log == nil {
		log = slog.Default()
	}
	if cfg.Replicas <= 0 {
		return nil, fmt.Errorf("coordinator: replicas must be positive")
	}
	if cfg.WriteQuorum <= 0 || cfg.WriteQuorum > cfg.Replicas {
		return nil, fmt.Errorf("coordinator: write quorum must be between 1 and replicas")
	}
	if len(nodes) < cfg.Replicas {
		return nil, fmt.Errorf("coordinator: have %d nodes, need %d replicas", len(nodes), cfg.Replicas)
	}
	if cfg.MaxPartSize <= 0 {
		return nil, fmt.Errorf("coordinator: max part size must be positive")
	}
	if cfg.MultipartExpiry <= 0 {
		return nil, fmt.Errorf("coordinator: multipart expiry must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 60 * time.Second
	}
	if cfg.SmallObjectLimit < 0 {
		cfg.SmallObjectLimit = 0
	}
	return &Coordinator{
		pool:  pool,
		q:     q,
		log:   log,
		wal:   logWAL,
		nodes: nodes,
		cfg:   cfg,
	}, nil
}

func (c *Coordinator) WAL() *wal.Log {
	return c.wal
}

func (c *Coordinator) orderedNodes(ctx context.Context) ([]placement.Node, error) {
	rows, err := c.q.ListStorageNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage nodes: %w", err)
	}
	if len(rows) == 0 {
		return placement.Order(c.nodes), nil
	}
	byID := make(map[string]placement.Node, len(c.nodes))
	for _, n := range c.nodes {
		byID[n.ID] = n
	}
	out := make([]placement.Node, 0, len(rows))
	for _, row := range rows {
		n, ok := byID[uuidString(row.ID)]
		if !ok {
			continue
		}
		n.Status = row.Status
		out = append(out, n)
	}
	if len(out) == 0 {
		return placement.Order(c.nodes), nil
	}
	return placement.Order(out), nil
}

func (c *Coordinator) nodeByID(ctx context.Context, id string) (placement.Node, bool) {
	nodes, err := c.orderedNodes(ctx)
	if err != nil {
		return placement.Node{}, false
	}
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return placement.Node{}, false
}
