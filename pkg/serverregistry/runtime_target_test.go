package serverregistry

import (
	"testing"
	"time"
)

func TestRuntimeTargetClassification(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	hostinger, ok := HostingerExternalVPSTarget("hostinger-vps:srv1161760", now)
	if !ok || hostinger.EnvironmentClass != EnvironmentCloud || hostinger.Offering != OfferingExternalVPS {
		t.Fatalf("Hostinger target = %#v, ok=%v", hostinger, ok)
	}
	if err := ValidateRuntimeTarget(hostinger, ""); err != nil {
		t.Fatalf("Hostinger target invalid: %v", err)
	}

	managed := ManagedVPSRuntimeTarget("ionos", "vm-42", "lease-42", now)
	if managed.EnvironmentClass != EnvironmentCloud || managed.Offering != OfferingManagedVPS || managed.ProviderTargetRef != "vm-42" {
		t.Fatalf("managed VPS target = %#v", managed)
	}
	if err := ValidateRuntimeTarget(managed, "lease-42"); err != nil {
		t.Fatalf("managed VPS target invalid: %v", err)
	}
	if got := ManagedVPSRuntimeTarget("ionos", "", "lease-42", now); got.EnvironmentClass != EnvironmentUnknown {
		t.Fatalf("missing provider resource must stay unknown: %#v", got)
	}
}

func TestMissingRuntimeTargetEvidenceNeverDefaultsLocal(t *testing.T) {
	target := NormalizeRuntimeTarget(RuntimeTarget{})
	if target.EnvironmentClass != "" {
		t.Fatalf("omitted patch should remain omitted, got %#v", target)
	}
	unknown := UnknownRuntimeTarget()
	if unknown.EnvironmentClass != EnvironmentUnknown {
		t.Fatalf("unknown target = %#v", unknown)
	}
	if err := ValidateRuntimeTarget(unknown, ""); err != nil {
		t.Fatalf("unknown target invalid: %v", err)
	}
}
