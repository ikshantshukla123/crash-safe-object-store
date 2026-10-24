// Package placement decides which storage nodes receive a write.
package placement

import (
	"sort"

	"github.com/google/uuid"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
)

// Node is one storage node known to the coordinator.
type Node struct {
	ID     string
	Name   string
	Status string
	Store  objects.ChunkStore
}

// Order returns healthy nodes first, then down nodes, both by name. The order
// is deterministic so two coordinators make the same choice and so tests can
// assert it.
func Order(nodes []Node) []Node {
	out := append([]Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "up") != (out[j].Status == "up") {
			return out[i].Status == "up"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Pick returns the first n nodes in placement order.
func Pick(nodes []Node, n int) []Node {
	ordered := Order(nodes)
	if n > len(ordered) {
		n = len(ordered)
	}
	return ordered[:n]
}

// NodeID derives the stable database id of a node from its name.
func NodeID(name string) string {
	return uuid.NewSHA1(uuid.MustParse("2f1c5f4e-6f4f-4f4f-9f4f-000000000003"), []byte(name)).String()
}
