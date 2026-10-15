package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// A legacy signed observation at an already committed source position must
// retain its exact replay digest after adding optional coverage evidence.
func TestGuardInventoryLegacyReplayDigestSurvivesOptionalCoverage(t *testing.T) {
	command, err := normalizeGuardInventoryProjection(guardInventoryProjectionTestCommand(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 1, true, "app"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := guardInventoryProjectionDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.Event.ExpectedRevision = 0
	command.Event.Runtime.LastHeartbeatAt = nil
	// This is the previous wire shape, retaining its declared field order.
	legacy := struct {
		Event                  ServerEvent
		Node                   Node
		Services               []GuardInventoryServiceProjection
		ManifestObserved       bool
		DiscoveryObserved      bool
		DiscoveredServiceCount int
		ServiceSource          string
	}{command.Event, command.Node, command.Services, command.ManifestObserved, command.DiscoveryObserved, command.DiscoveredServiceCount, command.ServiceSource}
	encoded, err := json.Marshal(struct {
		Schema  string `json:"schema"`
		Command any    `json:"command"`
	}{guardProjectionSchema, legacy})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("legacy observation can no longer replay its committed digest")
	}
	command.DiscoveryComplete = true
	covered, err := guardInventoryProjectionDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	if covered == digest {
		t.Fatal("new positive coverage evidence did not bind the observation")
	}
}
