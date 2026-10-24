package placement

import "testing"

func TestOrderPutsUpNodesFirst(t *testing.T) {
	nodes := []Node{
		{ID: "1", Name: "node3", Status: "down"},
		{ID: "2", Name: "node1", Status: "up"},
		{ID: "3", Name: "node2", Status: "up"},
	}
	ordered := Order(nodes)
	if ordered[0].Name != "node1" || ordered[1].Name != "node2" || ordered[2].Name != "node3" {
		t.Fatalf("order = %s, %s, %s", ordered[0].Name, ordered[1].Name, ordered[2].Name)
	}
	picked := Pick(nodes, 2)
	if len(picked) != 2 || picked[0].Name != "node1" || picked[1].Name != "node2" {
		t.Fatalf("pick = %+v", picked)
	}
}

func TestNodeIDIsStable(t *testing.T) {
	a := NodeID("node1")
	b := NodeID("node1")
	c := NodeID("node2")
	if a != b {
		t.Errorf("NodeID is not stable: %s then %s", a, b)
	}
	if a == c {
		t.Errorf("different names produced the same id: %s", a)
	}
}
