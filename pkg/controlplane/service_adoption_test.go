package controlplane

import (
	"strings"
	"testing"
)

func TestGuardObservationRetainsAdoptionOnlyForSameFullContainer(t *testing.T) {
	for _, replacement := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), "aaaaaaaaaaaa", ""} {
		t.Run(replacement, func(t *testing.T) {
			store := NewMemoryStore()
			ctx := t.Context()
			source := strings.Repeat("a", 64)
			_, err := store.UpsertServiceRuntime(ctx, ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "docker/ha", Source: "observed", ManagementState: "observed", ObservedState: "running", Metadata: map[string]any{"container_id": source}})
			if err != nil {
				t.Fatal(err)
			}
			adopted, err := store.ApplyServiceEvent(ctx, ServiceEvent{TenantID: "tenant-1", ServiceID: "service-1", Authority: ServiceEventAuthorityControlPlane, Runtime: ServiceRuntime{ManagementState: "managed", DesiredState: "running", Capabilities: []string{"stop"}, Metadata: map[string]any{"container_id": source, "application_adoption": map[string]any{"status": "verified"}, "application_adoption_job_id": "adopt-1"}}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.ApplyServiceEvent(ctx, ServiceEvent{TenantID: "tenant-1", ServiceID: "service-1", Authority: ServiceEventAuthorityGuard, Runtime: ServiceRuntime{ObservedState: "stopped", Metadata: map[string]any{"runtime_identity": map[string]any{"container_id": replacement}, "application_adoption_job_id": "forged", "application_adoption": map[string]any{"status": "verified"}, "application_intent_revision": int64(999)}}})
			if err != nil {
				t.Fatal(err)
			}
			if replacement == source {
				if got.Service.ManagementState != "managed" || got.Service.Metadata["application_adoption_job_id"] != "adopt-1" || got.Service.Metadata["application_intent_revision"] != adopted.Service.Metadata["application_intent_revision"] || len(got.Service.Capabilities) == 0 {
					t.Fatal("same source lost adoption or intent fence")
				}
			} else if got.Service.ManagementState != "observed" || got.Service.Metadata["application_adoption"] != nil || len(got.Service.Capabilities) > 0 {
				t.Fatal("replacement or unidentified source retained adoption control")
			}
		})
	}
}
