package vmleases

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimelease"
	productlease "github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestRuntimeContractStripsProductStateAndBindsExactGeneration(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	lease := productlease.Lease{
		ID: "lease-1", Subject: productlease.Subject{Kind: productlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"},
		Resource:     productlease.ResourceRef{ProviderID: "ionos-managed", EngineVMID: "provider-secret-handle"},
		DesiredState: productlease.DesiredStateRunning, BillingMode: productlease.BillingModeSubscription,
		LifecycleClass: productlease.LifecycleClassSubscription, RestartPolicy: productlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: productlease.RecreatePolicyManual, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), RenewedAt: now.Add(-time.Minute),
		Metadata: map[string]string{MetadataKeyResourceGenerationID: "a58debb7-0d79-4a0f-b20d-bdf09b67d790"},
	}
	projection, err := RuntimeContract(lease, 4, "server-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Revision != 4 || projection.ServerID != runtimelease.RuntimeServerID("server-1") || projection.ResourceGenerationID != runtimelease.ResourceGenerationID("a58debb7-0d79-4a0f-b20d-bdf09b67d790") {
		t.Fatalf("projection lost fencing identity: %#v", projection)
	}
	if err := projection.Validate(now); err != nil {
		t.Fatalf("runtimelease validation: %v", err)
	}
}
