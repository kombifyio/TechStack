package serverregistry

import (
	"testing"
	"time"
)

func TestDisplayNamePrefersHostingerSRVIdentity(t *testing.T) {
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	target, ok := HostingerExternalVPSTarget("hostinger-vps:srv1161760", now)
	if !ok {
		t.Fatal("expected Hostinger target")
	}
	got := DisplayName("t3-code-ryzen", "hostinger-vps:srv1161760", target, "t3-code-ryzen")
	if got != "srv1161760" {
		t.Fatalf("DisplayName = %q, want srv1161760", got)
	}
}

func TestDisplayNameUsesObservedSRVWhenProviderRefHasNoNumber(t *testing.T) {
	got := DisplayName("t3-code-ryzen", "hostinger", RuntimeTarget{}, "srv1161760.hstgr.io")
	if got != "srv1161760" {
		t.Fatalf("DisplayName = %q, want observed SRV identity", got)
	}
}

func TestDisplayNameKeepsLocalHostnameWithoutProviderIdentity(t *testing.T) {
	got := DisplayName("t3-code-ryzen", "", RuntimeTarget{}, "t3-code-ryzen")
	if got != "t3-code-ryzen" {
		t.Fatalf("DisplayName = %q, want enrolled hostname", got)
	}
}
