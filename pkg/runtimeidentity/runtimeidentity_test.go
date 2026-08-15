package runtimeidentity

import "testing"

func TestLeaseServerIDIsStableAndLeaseScoped(t *testing.T) {
	first := LeaseServerID(" lease-centron-1 ")
	if first == "" || first != LeaseServerID("lease-centron-1") {
		t.Fatalf("lease server id must be stable: %q", first)
	}
	if first == LeaseServerID("lease-centron-2") {
		t.Fatalf("different leases must not share server identity: %q", first)
	}
	if got := LeaseServerID(""); got != "" {
		t.Fatalf("empty lease id = %q, want empty", got)
	}
}

func TestStackServerIDIsStableAndInstanceScoped(t *testing.T) {
	primary := StackServerID("stack-1", "primary")
	if primary == "" || primary != StackServerID("stack-1", "primary") || primary != StackServerID("stack-1", "") {
		t.Fatalf("primary stack server identity is not stable: %q", primary)
	}
	if primary == StackServerID("stack-1", "storage") || primary == StackServerID("stack-2", "primary") {
		t.Fatal("stack server identity must be scoped by stack and instance")
	}
}

func TestServiceIDIsStableAndInstanceScoped(t *testing.T) {
	first := ServiceID("stack-1", "server-1", "vaultwarden", "default")
	if first == "" || first != ServiceID("stack-1", "server-1", "vaultwarden", "default") {
		t.Fatalf("ServiceID is not stable: %q", first)
	}
	if first == ServiceID("stack-1", "server-1", "vaultwarden", "secondary") {
		t.Fatal("service instances must not share an identity")
	}
	if first != ServiceID("stack-1", "server-1", " VaultWarden ", "DEFAULT") {
		t.Fatal("service identity must normalize key and instance casing")
	}
	if got := ServiceID("", "server-1", "vaultwarden", "default"); got != "" {
		t.Fatalf("ServiceID without stack = %q, want empty", got)
	}
}
