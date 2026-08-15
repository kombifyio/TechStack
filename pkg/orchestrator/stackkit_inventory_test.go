package orchestrator

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestSyncStackKitRuntimeInventoryStoreUpsertsNodeAndServices(t *testing.T) {
	store := controlplane.NewMemoryStore()
	req := stackKitRegistrySyncRequest{
		TenantID:  "tenant-1",
		StackID:   "stack-1",
		StackName: "Media Stack",
		Target: stackKitRuntimeTarget{
			Hostname:  "media.example.test",
			PublicIP:  "203.0.113.10",
			PrivateIP: "10.0.0.10",
		},
		Services: []stackKitServiceProjection{
			{
				Name:        "jellyfin",
				DisplayName: "Jellyfin",
				Type:        "media",
				URL:         "https://jellyfin.example.test",
				Port:        8096,
				Status:      "running",
				Metadata:    map[string]any{"health": "ok"},
			},
		},
	}

	if err := syncStackKitRuntimeInventoryStore(context.Background(), store, req); err != nil {
		t.Fatalf("syncStackKitRuntimeInventoryStore: %v", err)
	}
	if err := syncStackKitRuntimeInventoryStore(context.Background(), store, req); err != nil {
		t.Fatalf("second syncStackKitRuntimeInventoryStore: %v", err)
	}

	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Address != "203.0.113.10" || nodes[0].Metadata["source"] != "stackkit_outputs" {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
	services, err := store.ListServicesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 1 || services[0].ServiceKey != "jellyfin" || services[0].URL != "https://jellyfin.example.test" {
		t.Fatalf("unexpected services: %#v", services)
	}
	if services[0].Metadata["display_name"] != "Jellyfin" || services[0].Metadata["port"] != 8096 {
		t.Fatalf("unexpected service metadata: %#v", services[0].Metadata)
	}
}

func TestSyncStackKitRuntimeInventoryStoreUpsertsNodeWithoutServices(t *testing.T) {
	store := controlplane.NewMemoryStore()
	req := stackKitRegistrySyncRequest{
		TenantID:  "tenant-1",
		StackID:   "stack-1",
		StackName: "Managed Stack",
		Target: stackKitRuntimeTarget{
			Hostname: "managed.example.test",
			PublicIP: "203.0.113.11",
		},
	}

	if err := syncStackKitRuntimeInventoryStore(context.Background(), store, req); err != nil {
		t.Fatalf("syncStackKitRuntimeInventoryStore: %v", err)
	}
	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Address != "203.0.113.11" || nodes[0].Status != "online" {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
	services, err := store.ListServicesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 0 {
		t.Fatalf("services = %#v, want none", services)
	}
}

func TestSyncStackKitRuntimeInventoryFromJobUpsertsStoreWithoutPocketBaseStack(t *testing.T) {
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:       1,
		RegistryStore: store,
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:         "job-1",
		Type:       jobs.JobTypeProvision,
		TargetID:   "stack-1",
		TargetName: "Managed Stack",
		Result: map[string]interface{}{
			"runtime_public_ip": "203.0.113.20",
			"stackkit_outputs": map[string]interface{}{
				"services": []interface{}{
					map[string]interface{}{
						"name":   "vaultwarden",
						"url":    "https://vault.example.test",
						"status": "running",
					},
				},
			},
		},
	}

	if err := orch.syncStackKitRuntimeInventoryFromJob("tenant-1", job); err != nil {
		t.Fatalf("syncStackKitRuntimeInventoryFromJob: %v", err)
	}
	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Address != "203.0.113.20" {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
	services, err := store.ListServicesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 1 || services[0].ServiceKey != "vaultwarden" || services[0].URL != "https://vault.example.test" {
		t.Fatalf("unexpected services: %#v", services)
	}
}

func TestSyncStackKitRuntimeInventoryFromJobReadsServiceLinksMap(t *testing.T) {
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:       1,
		RegistryStore: store,
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:         "job-1",
		Type:       jobs.JobTypeDeploy,
		TargetID:   "stack-1",
		TargetName: "Managed Stack",
		Result: map[string]interface{}{
			"runtime_public_ip": "203.0.113.20",
			"stackkit_outputs": map[string]interface{}{
				"service_links": map[string]interface{}{
					"login_gateway": map[string]interface{}{
						"url":          "https://login.example.test",
						"display_name": "First Login",
						"type":         "identity",
					},
					"komodo":      "https://komodo.example.test",
					"vaultwarden": "https://vault.example.test",
				},
			},
		},
	}

	if err := orch.syncStackKitRuntimeInventoryFromJob("tenant-1", job); err != nil {
		t.Fatalf("syncStackKitRuntimeInventoryFromJob: %v", err)
	}
	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %#v, want one", nodes)
	}
	services, err := store.ListServicesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 3 {
		t.Fatalf("services = %#v, want three", services)
	}
	byKey := map[string]controlplane.Service{}
	for _, service := range services {
		byKey[service.ServiceKey] = service
	}
	if byKey["login_gateway"].URL != "https://login.example.test" || byKey["login_gateway"].Metadata["display_name"] != "First Login" {
		t.Fatalf("unexpected login gateway service: %#v", byKey["login_gateway"])
	}
	if byKey["komodo"].URL != "https://komodo.example.test" || byKey["komodo"].Metadata["type"] != "paas" {
		t.Fatalf("unexpected komodo service: %#v", byKey["komodo"])
	}
	if byKey["vaultwarden"].URL != "https://vault.example.test" {
		t.Fatalf("unexpected vaultwarden service: %#v", byKey["vaultwarden"])
	}
}

func TestSyncStackKitRuntimeInventoryFromJobWritesPlatformNodes(t *testing.T) {
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:       1,
		RegistryStore: store,
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:         "job-1",
		Type:       jobs.JobTypeDeploy,
		TargetID:   "stack-1",
		TargetName: "Demo Stack",
		Result: map[string]interface{}{
			"runtime_public_ip": "203.0.113.20",
			"platform_nodes": []interface{}{
				map[string]interface{}{
					"name":           "ionos-worker",
					"role":           "worker",
					"public_ip":      "203.0.113.21",
					"lease_provider": "ionos-managed",
				},
			},
			"stackkit_outputs": map[string]interface{}{
				"services": []interface{}{
					map[string]interface{}{
						"name":    "vaultwarden",
						"url":     "https://vault.kombified.com",
						"node_id": "Demo Stack",
					},
					map[string]interface{}{
						"name":    "immich",
						"url":     "https://photos.kombified.com",
						"node_id": "ionos-worker",
					},
				},
			},
		},
	}

	if err := orch.syncStackKitRuntimeInventoryFromJob("tenant-1", job); err != nil {
		t.Fatalf("syncStackKitRuntimeInventoryFromJob: %v", err)
	}
	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %#v, want main + worker", nodes)
	}
	nodesByName := map[string]controlplane.Node{}
	for _, node := range nodes {
		nodesByName[node.Name] = node
	}
	if nodesByName["Demo Stack"].Role != "main" || nodesByName["ionos-worker"].Role != "worker" {
		t.Fatalf("unexpected nodes by name: %#v", nodesByName)
	}
	if nodesByName["ionos-worker"].Address != "203.0.113.21" {
		t.Fatalf("worker address = %q, want 203.0.113.21", nodesByName["ionos-worker"].Address)
	}

	services, err := store.ListServicesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	byKey := map[string]controlplane.Service{}
	for _, service := range services {
		byKey[service.ServiceKey] = service
	}
	if byKey["vaultwarden"].NodeID != nodesByName["Demo Stack"].ID {
		t.Fatalf("vaultwarden node = %q, want main %q", byKey["vaultwarden"].NodeID, nodesByName["Demo Stack"].ID)
	}
	if byKey["immich"].NodeID != nodesByName["ionos-worker"].ID {
		t.Fatalf("immich node = %q, want worker %q", byKey["immich"].NodeID, nodesByName["ionos-worker"].ID)
	}
}
