package serviceregistry

import (
	"testing"
	"time"
)

func TestPlacementShapesAreDisjoint(t *testing.T) {
	if err := ValidatePlacement("server-1", Placement{TargetKind: TargetKindServer}); err != nil {
		t.Fatalf("server placement invalid: %v", err)
	}
	if err := ValidatePlacement("", Placement{TargetKind: TargetKindServer}); err == nil {
		t.Fatal("cloud/server placement without server_id must fail")
	}
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	managed := Placement{
		TargetKind: TargetKindManagedWorkload, ProviderID: "cloudflare",
		ManagedTargetRef: "worker:demo", ProviderReceiptRef: "receipt:1",
		SLAPolicyRef: "sla:planned", BackupPolicyRef: "backup:planned",
		EvidenceRef: "provider-receipt:1", ObservedAt: &now,
	}
	if err := ValidatePlacement("", managed); err != nil {
		t.Fatalf("managed workload invalid: %v", err)
	}
	if err := ValidatePlacement("server-1", managed); err == nil {
		t.Fatal("managed workload with server_id must fail")
	}
}

func TestMissingPlacementEvidenceStaysUnknown(t *testing.T) {
	placement := NormalizePlacement("", Placement{})
	if placement.TargetKind != TargetKindUnknown {
		t.Fatalf("placement = %#v, want unknown", placement)
	}
	if err := ValidatePlacement("", placement); err != nil {
		t.Fatalf("unknown placement invalid: %v", err)
	}
}
