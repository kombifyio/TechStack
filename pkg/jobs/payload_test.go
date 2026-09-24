package jobs

import (
	"encoding/json"
	"testing"
)

func TestParseWorkersFromPayloadAcceptsNumericCapabilityRepresentations(t *testing.T) {
	workerPayload := func(capabilities map[string]interface{}) []map[string]interface{} {
		return []map[string]interface{}{{
			"id": "worker-1", "name": "local-worker", "type": "main", "provider": "local", "status": "online",
			"capabilities": capabilities,
		}}
	}
	var jsonCapabilities map[string]interface{}
	if err := json.Unmarshal([]byte(`{"cpu":8,"ram":8192,"disk":256,"arch":"amd64","os":"linux","dockerVersion":"24.0.0"}`), &jsonCapabilities); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	tests := []struct {
		name           string
		payload        interface{}
		cpu, ram, disk int
		dockerVersion  string
	}{
		{
			name: "in-memory integers",
			payload: workerPayload(map[string]interface{}{
				"cpu": 12, "ram": 15947, "disk": 1007, "arch": "amd64", "os": "linux", "dockerVersion": "docker-desktop",
			}),
			cpu: 12, ram: 15947, disk: 1007, dockerVersion: "docker-desktop",
		},
		{name: "JSON numbers", payload: workerPayload(jsonCapabilities), cpu: 8, ram: 8192, disk: 256, dockerVersion: "24.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workers, err := parseWorkersFromPayload(tt.payload)
			if err != nil {
				t.Fatalf("parseWorkersFromPayload: %v", err)
			}
			if len(workers) != 1 {
				t.Fatalf("workers length = %d, want 1", len(workers))
			}
			caps := workers[0].Capabilities
			if caps.CPU != tt.cpu || caps.RAM != tt.ram || caps.Disk != tt.disk {
				t.Fatalf("numeric capabilities = %#v", caps)
			}
			if caps.Arch != "amd64" || caps.OS != "linux" || caps.DockerVersion != tt.dockerVersion {
				t.Fatalf("runtime capabilities = %#v", caps)
			}
		})
	}
}
