package db

import (
	_ "embed"
	"encoding/json"
	"errors"
)

//go:embed delivery_forward_manifest.json
var deliveryForwardManifest []byte

// DeliveryForwardBatch is the reviewed payload of the owning database artifact.
// Callers cannot select SQL, additional versions or a different pending batch.
func DeliveryForwardBatch() ([]ForwardMigrationArtifact, error) {
	var manifest struct {
		Migrations []ForwardMigrationArtifact `json:"migrations"`
	}
	if err := json.Unmarshal(deliveryForwardManifest, &manifest); err != nil {
		return nil, err
	}
	if _, err := forwardBatchBodies(manifest.Migrations); err != nil {
		return nil, err
	}
	return manifest.Migrations, nil
}

func DeliveryForwardDatabaseHost() (string, error) {
	var manifest struct {
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(deliveryForwardManifest, &manifest); err != nil {
		return "", err
	}
	if manifest.Hostname == "" {
		return "", errors.New("delivery database host is unbound")
	}
	return manifest.Hostname, nil
}
