package routes

import (
	"testing"
	"time"
)

func networkHost(privateIP, fingerprint string) workerInventoryHost {
	host := workerInventoryHost{PrivateIP: privateIP}
	if fingerprint != "" {
		host.Network = &workerInventoryNetwork{Gateway: "192.168.1.1", CIDR: "192.168.1.0/24", Fingerprint: fingerprint}
	}
	return host
}

// A server that turns up on another network is recorded once: the stored host
// facts of the first move inventory already carry the new fingerprint, so the
// same fingerprint twice yields no second event (and no second rebind).
func TestDetectNetworkMoveRecordsOnceAndKeepsBothSides(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	before := inventoryHostMap(networkHost("192.168.1.20", "aaaa"))
	after := networkHost("10.0.0.5", "bbbb")

	move := detectNetworkMove(before, after, now)
	if move == nil {
		t.Fatal("a changed fingerprint must be detected as a move")
	}
	if move["rebind_status"] != networkRebindQueued {
		t.Fatalf("rebind_status = %v, want queued", move["rebind_status"])
	}
	if previous := move["previous"].(map[string]any); previous["ip"] != "192.168.1.20" || previous["gateway"] != "192.168.1.1" {
		t.Fatalf("previous point lost: %#v", previous)
	}
	if current := move["current"].(map[string]any); current["ip"] != "10.0.0.5" {
		t.Fatalf("current point lost: %#v", current)
	}

	if again := detectNetworkMove(inventoryHostMap(after), after, now.Add(time.Minute)); again != nil {
		t.Fatalf("the same fingerprint twice must not record another move: %#v", again)
	}
}

// Without a fingerprint the private address decides, and a first inventory (no
// stored host) or a side with nothing to compare never counts as a move.
func TestDetectNetworkMoveFallsBackToPrivateIPAndIgnoresMissingEvidence(t *testing.T) {
	now := time.Now()
	stored := inventoryHostMap(networkHost("192.168.1.20", ""))
	if detectNetworkMove(stored, networkHost("192.168.1.20", ""), now) != nil {
		t.Fatal("an unchanged private address is not a move")
	}
	if detectNetworkMove(stored, networkHost("10.0.0.5", ""), now) == nil {
		t.Fatal("a changed private address without fingerprints is a move")
	}
	if detectNetworkMove(nil, networkHost("10.0.0.5", "bbbb"), now) != nil {
		t.Fatal("a first inventory has nothing to move from")
	}
	if detectNetworkMove(stored, networkHost("", ""), now) != nil {
		t.Fatal("an inventory without any network evidence is not a move")
	}
}
