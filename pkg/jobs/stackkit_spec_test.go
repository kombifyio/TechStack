package jobs

import (
	"strings"
	"testing"
)

func TestStackKitSpecBytesRefusesV1DocumentWithUseCases(t *testing.T) {
	_, err := stackKitSpecBytesForPayload(map[string]interface{}{
		"name":     "local-stack",
		"stackkit": "basement-kit",
		"useCases": []interface{}{"photos"},
		"mode":     "simple",
	})
	if err == nil {
		t.Fatal("mixed v1+useCases payload must not become the CLI handoff")
	}
}

func TestStackKitSpecBytesSetsBootstrappedModeForManagedRuntime(t *testing.T) {
	spec := map[string]interface{}{
		"name":        "managed-stack",
		"stackkit":    "cloud-kit",
		"mode":        "simple",
		"runtime":     "docker",
		"provider_id": "centron",
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
