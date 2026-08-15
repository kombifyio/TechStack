package jobs

import (
	"strings"
	"testing"
)

func TestStackKitSpecBytesKeepsProviderlessLocalSpecSimple(t *testing.T) {
	spec := map[string]interface{}{
		"name":     "local-stack",
		"stackkit": "basement-kit",
		"mode":     "simple",
		"runtime":  "docker",
		"nodes": []interface{}{
			map[string]interface{}{
				"name": "main",
				"role": "standalone",
			},
		},
	}

	data, err := stackKitSpecBytesForPayload(spec)
	if err != nil {
		t.Fatalf("stackKitSpecBytesForPayload: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "mode: simple") {
		t.Fatalf("stack spec =\n%s\nwant mode: simple", text)
	}
	if spec["mode"] != "simple" {
		t.Fatalf("source spec mode mutated to %q", spec["mode"])
	}
}

func TestStackKitSpecBytesSetsBootstrappedModeForManagedRuntime(t *testing.T) {
	spec := map[string]interface{}{
		"name":     "managed-stack",
		"stackkit": "cloud-kit",
		"mode":     "simple",
		"runtime":  "docker",
		"metadata": map[string]interface{}{
			metadataKeyServerProvisionMode: serverProvisionModeKombifyCloud,
			metadataKeyServerMode:          serverModeMonthlyRuntime,
			metadataKeyRuntimeLane:         serverModeMonthlyRuntime,
		},
	}

	data, err := stackKitSpecBytesForPayload(spec)
	if err != nil {
		t.Fatalf("stackKitSpecBytesForPayload: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "mode: bootstrapped") {
		t.Fatalf("stack spec =\n%s\nwant mode: bootstrapped", text)
	}
	if spec["mode"] != "simple" {
		t.Fatalf("source spec mode mutated to %q", spec["mode"])
	}
}
