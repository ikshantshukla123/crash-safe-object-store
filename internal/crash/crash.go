// Package crash injects process exits at known write-path points.
package crash

import (
	"log/slog"
	"os"
)

const (
	AfterWALSync  = "after_wal_sync"
	AfterNodeAck  = "after_node_ack"
	AfterDBCommit = "after_db_commit"
)

// Point kills the process when CRASH_AT names this point. It is a no-op in
// every other process, so production binaries can keep the call sites.
func Point(name string) {
	if os.Getenv("CRASH_AT") != name {
		return
	}
	slog.Error("crash injection", "point", name)
	os.Exit(1)
}
