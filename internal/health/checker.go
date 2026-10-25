// Package health monitors storage nodes and records their status in Postgres.
// Only this loop writes storage_nodes.status.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
)

// Scrubber is the fast check run when a node comes back up.
type Scrubber interface {
	ScrubNode(ctx context.Context, node placement.Node) (int, error)
}

type nodeState struct {
	status    string
	failures  int
	successes int
}

// Checker pings nodes and flips their database status on threshold crossings.
type Checker struct {
	q             *dbgen.Queries
	nodes         []placement.Node
	log           *slog.Logger
	interval      time.Duration
	downThreshold int
	upThreshold   int
	scrubber      Scrubber

	mu     sync.Mutex
	states map[string]*nodeState
}

func New(q *dbgen.Queries, nodes []placement.Node, log *slog.Logger, interval time.Duration, downThreshold, upThreshold int, scrubber Scrubber) *Checker {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if downThreshold <= 0 {
		downThreshold = 3
	}
	if upThreshold <= 0 {
		upThreshold = 2
	}
	return &Checker{
		q:             q,
		nodes:         nodes,
		log:           log,
		interval:      interval,
		downThreshold: downThreshold,
		upThreshold:   upThreshold,
		scrubber:      scrubber,
		states:        make(map[string]*nodeState),
	}
}

// Run checks immediately and then on every interval until ctx is cancelled.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.CheckOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.CheckOnce(ctx)
		}
	}
}

// CheckOnce is exposed for tests and for the worker startup path.
func (c *Checker) CheckOnce(ctx context.Context) {
	rows, err := c.q.ListStorageNodes(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "health: list nodes", "err", err)
		return
	}
	dbStatus := make(map[string]string, len(rows))
	for _, row := range rows {
		dbStatus[row.Name] = row.Status
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, node := range c.nodes {
		state, ok := c.states[node.Name]
		if !ok {
			status := dbStatus[node.Name]
			if status == "" {
				status = "up"
			}
			state = &nodeState{status: status}
			c.states[node.Name] = state
		}

		err := checkNode(ctx, node)
		if err != nil {
			state.failures++
			state.successes = 0
			if state.status == "up" && state.failures >= c.downThreshold {
				c.transition(ctx, node, state, "down")
			}
			continue
		}

		state.successes++
		state.failures = 0
		if state.status == "down" && state.successes >= c.upThreshold {
			c.transition(ctx, node, state, "up")
			if c.scrubber != nil {
				go c.fastScrub(node)
			}
		}
	}
}

func checkNode(ctx context.Context, node placement.Node) error {
	checker, ok := node.Store.(interface {
		Healthy(context.Context) error
	})
	if !ok {
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return checker.Healthy(pingCtx)
}

func (c *Checker) transition(ctx context.Context, node placement.Node, state *nodeState, status string) {
	if err := c.q.SetStorageNodeStatus(ctx, dbgen.SetStorageNodeStatusParams{
		Name:   node.Name,
		Status: status,
	}); err != nil {
		c.log.ErrorContext(ctx, "health: set node status", "node_id", node.ID, "status", status, "err", err)
		return
	}
	c.log.InfoContext(ctx, "storage node status changed", "node_id", node.ID, "node", node.Name, "status", status)
	state.status = status
}

func (c *Checker) fastScrub(node placement.Node) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	n, err := c.scrubber.ScrubNode(ctx, node)
	if err != nil {
		c.log.WarnContext(ctx, "fast scrub after node recovery", "node_id", node.ID, "checked", n, "err", err)
		return
	}
	c.log.InfoContext(ctx, "fast scrub complete", "node_id", node.ID, "checked", n)
}

// Status is the last in-memory state for a node, used in tests.
func (c *Checker) Status(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.states[name]; ok {
		return s.status
	}
	return ""
}

func (c *Checker) String() string {
	return fmt.Sprintf("health checker: %d nodes, interval %s", len(c.nodes), c.interval)
}
