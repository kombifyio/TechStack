package db

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed delivery_forward_manifest.json
var deliveryForwardManifest []byte

// DeliveryForwardManifestDigest binds both the ordered SQL identities and the
// provider target/CA. Git's canonical bytes use LF on every build host.
func DeliveryForwardManifestDigest() string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(bytes.ReplaceAll(deliveryForwardManifest, []byte("\r\n"), []byte("\n"))))
}

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

// DeliveryForwardDatabaseHost is the host of the active declared target
// (database_target); the Delivery adapter admits only its fixed pairs.
func DeliveryForwardDatabaseHost() (string, error) {
	var manifest struct {
		Target  string `json:"database_target"`
		Targets map[string]struct {
			Hostname string `json:"hostname"`
		} `json:"database_targets"`
	}
	if err := json.Unmarshal(deliveryForwardManifest, &manifest); err != nil {
		return "", err
	}
	host := manifest.Targets[manifest.Target].Hostname
	if host == "" {
		return "", errors.New("delivery database host is unbound")
	}
	return host, nil
}
