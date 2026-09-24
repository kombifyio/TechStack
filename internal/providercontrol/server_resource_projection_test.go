package providercontrol

import (
	"errors"
	"testing"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestDecommissionFinalizationAcceptsExactDecommissioningOrTerminalHead(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name      string
		head      receiptRuntimeServerHead
		wantEvent bool
		wantErr   bool
	}{
		{name: "decommissioning", head: receiptRuntimeServerHead{LifecycleState: string(serverregistry.LifecycleDecommissioning), DesiredState: string(serverregistry.DesiredAbsent)}, wantEvent: true},
		{name: "terminal", head: receiptRuntimeServerHead{LifecycleState: string(serverregistry.LifecycleDecommissioned), DesiredState: string(serverregistry.DesiredAbsent), DecommissionedAt: &now}},
		{name: "active", head: receiptRuntimeServerHead{LifecycleState: string(serverregistry.LifecycleActive), DesiredState: string(serverregistry.DesiredAbsent)}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotEvent, err := decommissionFinalizationServerEventRequired(test.head)
			if gotEvent != test.wantEvent || (err != nil) != test.wantErr {
				t.Fatalf("finalization head = event:%t err:%v", gotEvent, err)
			}
		})
	}
}

func TestRuntimeTargetFromProviderReceiptUsesSemanticBindingRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		providerID string
		resources  []providerexecutor.ResourceBinding
		serverID   string
		publicIP   string
	}{
		{
			name:       "ionos native kinds",
			providerID: "ionos",
			resources: []providerexecutor.ResourceBinding{
				{BindingID: "datacenter", Kind: "ionos.datacenter", NativeRef: "/cloudapi/v6/datacenters/dc-1"},
				{BindingID: runtimeServerBindingID, Kind: "ionos.server", NativeRef: "/cloudapi/v6/datacenters/dc-1/servers/server-1"},
				{BindingID: runtimePublicIPBindingID, Kind: "ionos.public-ip", NativeRef: "ionos-ip://192.0.2.10"},
			},
			serverID: "server-1",
			publicIP: "192.0.2.10",
		},
		{
			name:       "centron native kinds",
			providerID: "centron",
			resources: []providerexecutor.ResourceBinding{
				{BindingID: runtimeServerBindingID, Kind: "centron.server", NativeRef: "centron-server://managed-1/31415"},
				{BindingID: runtimePublicIPBindingID, Kind: "centron.public-ip", NativeRef: "centron-ip://198.51.100.23"},
			},
			serverID: "31415",
			publicIP: "198.51.100.23",
		},
		{
			name:       "provider native kinds remain opaque to core",
			providerID: "future-provider",
			resources: []providerexecutor.ResourceBinding{
				{BindingID: runtimeServerBindingID, Kind: "future.compute-instance", NativeRef: "future-resource://region-a/instances/instance-9"},
				{BindingID: runtimePublicIPBindingID, Kind: "future.address", NativeRef: "future-ip://203.0.113.44"},
			},
			serverID: "instance-9",
			publicIP: "203.0.113.44",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := runtimeTargetTestRecord(test.providerID)
			got, err := runtimeTargetFromProviderReceipt(record, providerexecutor.Receipt{Resources: test.resources})
			if err != nil {
				t.Fatalf("project runtime target: %v", err)
			}
			if got.EngineVMID != test.serverID || got.PublicIP != test.publicIP || got.ProviderID != test.providerID {
				t.Fatalf("unexpected runtime target: %#v", got)
			}
			if got.TenantID != record.Command.TenantID || got.LeaseID != record.Command.LeaseID ||
				got.RuntimeServerID != record.Command.RuntimeServerID ||
				got.ResourceGenerationID != record.Command.ResourceGenerationID {
				t.Fatalf("runtime target lost command custody: %#v", got)
			}
		})
	}
}

func TestRuntimeTargetFromProviderReceiptRejectsAmbiguousOrMalformedRoles(t *testing.T) {
	t.Parallel()

	validServer := providerexecutor.ResourceBinding{
		BindingID: runtimeServerBindingID, Kind: "provider.server", NativeRef: "provider-server://host/instance-1",
	}
	validIP := providerexecutor.ResourceBinding{
		BindingID: runtimePublicIPBindingID, Kind: "provider.public-ip", NativeRef: "provider-ip://192.0.2.10",
	}
	tests := []struct {
		name      string
		resources []providerexecutor.ResourceBinding
	}{
		{name: "missing server", resources: []providerexecutor.ResourceBinding{validIP}},
		{name: "missing public ip", resources: []providerexecutor.ResourceBinding{validServer}},
		{name: "duplicate server role", resources: []providerexecutor.ResourceBinding{validServer, validServer, validIP}},
		{name: "duplicate public ip role", resources: []providerexecutor.ResourceBinding{validServer, validIP, validIP}},
		{name: "server has no path", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://host"}, validIP,
		}},
		{name: "server points to a collection", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "/provider/v1/servers/"}, validIP,
		}},
		{name: "server is a bare identifier", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "instance-1"}, validIP,
		}},
		{name: "server has a network path without scheme", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "//host/instance-1"}, validIP,
		}},
		{name: "server scheme has no authority", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server:///instances/instance-1"}, validIP,
		}},
		{name: "server is opaque", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server:instance-1"}, validIP,
		}},
		{name: "server has encoded path", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://host/instances%2Finstance-1"}, validIP,
		}},
		{name: "server has userinfo", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://user@host/instance-1"}, validIP,
		}},
		{name: "server has query", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://host/instance-1?revision=2"}, validIP,
		}},
		{name: "server has fragment", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://host/instance-1#fragment"}, validIP,
		}},
		{name: "server has control character", resources: []providerexecutor.ResourceBinding{
			{BindingID: runtimeServerBindingID, NativeRef: "provider-server://host/instance-1\n"}, validIP,
		}},
		{name: "public ip is invalid", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://not-an-ip"},
		}},
		{name: "public ip is ipv6", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://[2001:db8::1]"},
		}},
		{name: "public ip is unspecified", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://0.0.0.0"},
		}},
		{name: "public ip is loopback", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://127.0.0.1"},
		}},
		{name: "public ip is link local", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://169.254.10.20"},
		}},
		{name: "public ip is private", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://10.10.0.5"},
		}},
		{name: "public ip is shared address space", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://100.64.0.5"},
		}},
		{name: "public ip is multicast", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://224.0.0.1"},
		}},
		{name: "public ip has a port", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://192.0.2.10:443"},
		}},
		{name: "public ip has a path", resources: []providerexecutor.ResourceBinding{
			validServer, {BindingID: runtimePublicIPBindingID, NativeRef: "provider-ip://192.0.2.10/address"},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtimeTargetFromProviderReceipt(runtimeTargetTestRecord("provider"), providerexecutor.Receipt{
				Resources: test.resources,
			})
			if !errors.Is(err, ErrCleanupCustody) {
				t.Fatalf("expected cleanup custody error, got %v", err)
			}
		})
	}
}

func runtimeTargetTestRecord(providerID string) OperationRecord {
	return OperationRecord{Command: providerexecutor.Command{
		TenantID: "tenant-1", LeaseID: "lease-1", RuntimeServerID: "server-runtime-1",
		ResourceGenerationID: "resource-generation-1", ProviderID: providerID,
	}}
}
