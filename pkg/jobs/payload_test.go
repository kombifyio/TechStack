package jobs

import (
	"encoding/json"
	"testing"
)

func TestParseWorkersFromPayloadAcceptsInMemoryIntegerCapabilities(t *testing.T) {
	workers, err := parseWorkersFromPayload([]map[string]interface{}{
		{
			"id":       "worker-1",
			"name":     "local-worker",
			"type":     "main",
			"provider": "local",
			"status":   "online",
			"capabilities": map[string]interface{}{
				"cpu":           12,
				"ram":           15947,
				"disk":          1007,
				"arch":          "amd64",
				"os":            "linux",
				"dockerVersion": "docker-desktop",
			},
		},
	})
	if err != nil {
		t.Fatalf("parseWorkersFromPayload: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers length = %d, want 1", len(workers))
	}
	caps := workers[0].Capabilities
	if caps.CPU != 12 || caps.RAM != 15947 || caps.Disk != 1007 {
		t.Fatalf("capabilities = CPU %d RAM %d Disk %d, want 12/15947/1007", caps.CPU, caps.RAM, caps.Disk)
	}
	if caps.Arch != "amd64" || caps.OS != "linux" || caps.DockerVersion != "docker-desktop" {
		t.Fatalf("unexpected runtime capabilities: %#v", caps)
	}
}

func TestParseWorkersFromPayloadAcceptsJSONNumericCapabilities(t *testing.T) {
	var payload []interface{}
	if err := json.Unmarshal([]byte(`[
		{
			"id": "worker-1",
			"name": "local-worker",
			"type": "main",
			"provider": "local",
			"status": "online",
			"capabilities": {
				"cpu": 8,
				"ram": 8192,
				"disk": 256,
				"arch": "amd64",
				"os": "linux",
				"dockerVersion": "24.0.0"
			}
		}
	]`), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	workers, err := parseWorkersFromPayload(payload)
	if err != nil {
		t.Fatalf("parseWorkersFromPayload: %v", err)
	}
	caps := workers[0].Capabilities
	if caps.CPU != 8 || caps.RAM != 8192 || caps.Disk != 256 {
		t.Fatalf("capabilities = CPU %d RAM %d Disk %d, want 8/8192/256", caps.CPU, caps.RAM, caps.Disk)
	}
}
