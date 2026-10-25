package coordinator

import (
	"context"
	"fmt"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
)

// PurgeChunks removes every chunk file from every node. Demo reset only.
func (c *Coordinator) PurgeChunks(ctx context.Context) error {
	for _, node := range c.nodes {
		purger, ok := node.Store.(objects.Purger)
		if !ok {
			continue
		}
		if err := purger.PurgeAll(ctx); err != nil {
			return fmt.Errorf("purge node %s: %w", node.Name, err)
		}
	}
	return nil
}

// ResetWAL clears resolved WAL segments. Demo reset only.
func (c *Coordinator) ResetWAL(ctx context.Context) error {
	if c.wal == nil {
		return nil
	}
	return c.wal.Reset(ctx)
}
