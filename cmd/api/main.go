// Command api is the HTTP API and the single write coordinator (CLAUDE.md §3.2).
//
// Phase 0 is scaffolding only: this entrypoint exists so the build, vet and CI
// pipeline are green before any real code lands. Phase 1 wires up config
// loading, the Chi router, migrations and /health.
package main

import (
	"log/slog"
	"os"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("api entrypoint reached", "phase", 0, "state", "scaffold")
}
