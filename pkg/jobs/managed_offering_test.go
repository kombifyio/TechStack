package jobs

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

func TestManagedLeaseRequestPreservesSelectedOffering(t *testing.T) {
	for _, stackKit := range []bool{true, false} {
		for _, placement := range []string{"top", "metadata", "options", "conflict", "unknown", "malformed", "default"} {
			t.Run(stringBool(stackKit)+"/"+placement, func(t *testing.T) {
				config := map[string]any{"name": "qualification", "provider_id": "ionos", "server_mode": "monthly-runtime", "services": []any{"photos", "files", "vault"}}
				if stackKit {
					config["stackkit"] = "cloud-kit"
				}
				switch placement {
				case "top":
					config["runtime_offering_id"] = "monthly-runtime-premium"
				case "metadata", "options":
					config[placement] = map[string]any{"runtime_offering_id": "monthly-runtime-premium"}
				case "conflict":
					config["runtime_offering_id"] = "monthly-runtime-premium"
					config["metadata"] = map[string]any{"runtime_offering_id": "monthly-runtime-standard"}
				case "unknown":
					config["runtime_offering_id"] = "monthly-runtime-missing"
				case "malformed":
					config["runtime_offering_id"] = 4
				}
				request, err := PrimaryManagedLeaseRequestFromUIConfig(config, "stack-1", "qualification", "tenant-1", "owner-1")
				if placement == "conflict" || placement == "unknown" || placement == "malformed" {
					if err == nil || request.Provider != "" {
						t.Fatal("invalid offering produced a provider-capable request")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				pack, err := monthlyruntime.PackageForProvider(request.Provider, request.Metadata["runtime_offering_id"])
				if err != nil {
					t.Fatal(err)
				}
				wantRAM := 8192
				if placement == "default" {
					wantRAM = 4096
				}
				if pack.MemoryMB != wantRAM {
					t.Fatalf("selected provider package has %d MiB, want %d", pack.MemoryMB, wantRAM)
				}
			})
		}
	}
}

func stringBool(value bool) string {
	if value {
		return "stackkit"
	}
	return "wizard"
}
