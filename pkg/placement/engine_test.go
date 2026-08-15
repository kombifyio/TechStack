package placement

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestPlaceServices_SingleService(t *testing.T) {
	engine := NewPlacementEngine()

	// Setup: 1 worker with good resources
	workers := []core.Worker{
		{
			ID:     "worker-1",
			Name:   "test-worker",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           8,
				RAM:           16384,
				Disk:          500,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
			Usage: core.WorkerUsage{
				CPU:          2,
				RAM:          2048,
				Disk:         50,
				ServiceCount: 1,
			},
		},
	}

	// Setup: 1 simple service
	services := []core.ServiceSpec{
		{
			Name: "nginx",
			Type: "reverse-proxy",
		},
	}

	requirements := &core.RequirementsSpec{
		StackKit: "basement-kit",
		RequiredWorkers: core.WorkerRequirements{
			MinCloudServers: 0,
			MinLocalServers: 1,
			MinCPU:          1,
			MinRAM:          512,
		},
	}

	// Execute
	placements, quality, err := engine.PlaceServices(requirements, workers, services)

	// Assert
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(placements) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(placements))
	}
	if placements[0].WorkerID != "worker-1" {
		t.Errorf("expected worker-1, got %s", placements[0].WorkerID)
	}
	if quality < 50 {
		t.Errorf("expected quality >= 50, got %d", quality)
	}
}

func TestPlaceServices_NoSuitableWorker(t *testing.T) {
	engine := NewPlacementEngine()

	// Setup: Worker with insufficient resources
	workers := []core.Worker{
		{
			ID:     "worker-1",
			Name:   "tiny-worker",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           1,
				RAM:           256,
				Disk:          4,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
		},
	}

	// Setup: Service needs more than worker has
	services := []core.ServiceSpec{
		{
			Name: "postgres",
			Type: "database",
		},
	}

	requirements := &core.RequirementsSpec{
		StackKit: "basement-kit",
		RequiredWorkers: core.WorkerRequirements{
			MinCloudServers: 0,
			MinLocalServers: 1,
			MinCPU:          2,
			MinRAM:          2048,
		},
	}

	// Execute
	_, _, err := engine.PlaceServices(requirements, workers, services)

	// Assert - should fail
	if err == nil {
		t.Fatal("expected error for insufficient resources, got nil")
	}
}

func TestPlaceServices_DependencyOrdering(t *testing.T) {
	engine := NewPlacementEngine()

	workers := []core.Worker{
		{
			ID:     "worker-1",
			Name:   "main-worker",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           16,
				RAM:           32768,
				Disk:          1000,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
		},
	}

	// Setup: Frontend depends on Backend, Backend depends on Database
	services := []core.ServiceSpec{
		{
			Name:  "frontend",
			Type:  "nextcloud",
			Needs: []string{"backend"},
		},
		{
			Name:  "backend",
			Type:  "reverse-proxy",
			Needs: []string{"database"},
		},
		{
			Name: "database",
			Type: "postgres",
		},
	}

	requirements := &core.RequirementsSpec{
		StackKit: "basement-kit",
		RequiredWorkers: core.WorkerRequirements{
			MinCloudServers: 0,
			MinLocalServers: 1,
			MinCPU:          8,
			MinRAM:          8192,
		},
	}

	// Execute
	placements, _, err := engine.PlaceServices(requirements, workers, services)

	// Assert
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(placements) != 3 {
		t.Fatalf("expected 3 placements, got %d", len(placements))
	}

	// Verify ordering: database → backend → frontend
	if placements[0].Service.Name != "database" {
		t.Errorf("expected database first, got %s", placements[0].Service.Name)
	}
	if placements[1].Service.Name != "backend" {
		t.Errorf("expected backend second, got %s", placements[1].Service.Name)
	}
	if placements[2].Service.Name != "frontend" {
		t.Errorf("expected frontend third, got %s", placements[2].Service.Name)
	}
}

func TestPlaceServices_LoadBalancing(t *testing.T) {
	engine := NewPlacementEngine()

	// Setup: 3 workers with equal capabilities
	workers := []core.Worker{
		{
			ID:     "worker-1",
			Name:   "worker-1",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           8,
				RAM:           16384,
				Disk:          500,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
			Usage: core.WorkerUsage{
				CPU:          0,
				RAM:          0,
				Disk:         0,
				ServiceCount: 0,
			},
		},
		{
			ID:     "worker-2",
			Name:   "worker-2",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           8,
				RAM:           16384,
				Disk:          500,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
			Usage: core.WorkerUsage{
				CPU:          0,
				RAM:          0,
				Disk:         0,
				ServiceCount: 0,
			},
		},
		{
			ID:     "worker-3",
			Name:   "worker-3",
			Type:   "worker",
			Status: "online",
			Capabilities: core.WorkerCapabilities{
				CPU:           8,
				RAM:           16384,
				Disk:          500,
				Arch:          "amd64",
				DockerVersion: "24.0.0",
			},
			Usage: core.WorkerUsage{
				CPU:          0,
				RAM:          0,
				Disk:         0,
				ServiceCount: 0,
			},
		},
	}

	// Setup: 6 similar services
	services := []core.ServiceSpec{
		{Name: "service-1", Type: "reverse-proxy"},
		{Name: "service-2", Type: "reverse-proxy"},
		{Name: "service-3", Type: "reverse-proxy"},
		{Name: "service-4", Type: "reverse-proxy"},
		{Name: "service-5", Type: "reverse-proxy"},
		{Name: "service-6", Type: "reverse-proxy"},
	}

	requirements := &core.RequirementsSpec{
		StackKit: "basement-kit",
		RequiredWorkers: core.WorkerRequirements{
			MinCloudServers: 0,
			MinLocalServers: 3,
			MinCPU:          1,
			MinRAM:          512,
		},
	}

	// Execute
	placements, _, err := engine.PlaceServices(requirements, workers, services)

	// Assert
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Count services per worker
	workerLoad := make(map[string]int)
	for _, p := range placements {
		workerLoad[p.WorkerID]++
		t.Logf("Service %s → Worker %s (score: %d)", p.Service.Name, p.WorkerName, p.Score)
	}

	// Verify load is distributed across all workers
	if len(workerLoad) != 3 {
		t.Errorf("expected services on all 3 workers, got %d workers", len(workerLoad))
	}

	// Each worker should have at least 1 service
	for workerID, count := range workerLoad {
		if count < 1 {
			t.Errorf("worker %s has no services", workerID)
		}
		if count > 3 {
			t.Errorf("worker %s has too many services (%d), expected <=3", workerID, count)
		}
	}

	// Total should be 6
	total := 0
	for _, count := range workerLoad {
		total += count
	}
	if total != 6 {
		t.Errorf("expected 6 total services, got %d", total)
	}
}

func TestScoreResourceBalance(t *testing.T) {
	engine := NewPlacementEngine()

	worker := core.Worker{
		Capabilities: core.WorkerCapabilities{
			CPU: 8,
			RAM: 16384,
		},
	}

	// scoreResourceBalance works in millicores, so an 8-core worker has a
	// budget of 8000 and "half used" is 4000.
	tests := []struct {
		name      string
		allocated core.WorkerUsage
		req       serviceRequirement
		wantMin   int
	}{
		{
			name: "ideal_50_percent",
			allocated: core.WorkerUsage{
				CPU: 2000,
				RAM: 4096,
			},
			req: serviceRequirement{
				CPU: 2000,
				RAM: 4096,
			},
			wantMin: 90,
		},
		{
			name: "avoid_overload_95_percent",
			allocated: core.WorkerUsage{
				CPU: 7000,
				RAM: 15360,
			},
			req: serviceRequirement{
				CPU: 1000,
				RAM: 512,
			},
			wantMin: 0,
		},
		{
			name: "avoid_underutilized_10_percent",
			allocated: core.WorkerUsage{
				CPU: 0,
				RAM: 0,
			},
			req: serviceRequirement{
				CPU: 1000,
				RAM: 512,
			},
			wantMin: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := engine.scoreResourceBalance(worker, &tt.allocated, tt.req)
			if score < tt.wantMin {
				t.Errorf("scoreResourceBalance() = %d, want >= %d", score, tt.wantMin)
			}
		})
	}
}

func TestEstimateServiceRequirements(t *testing.T) {
	tests := []struct {
		name    string
		svcType string
		wantCPU int
		wantRAM int
		wantGPU bool
	}{
		// CPU is in millicores and reservations are steady-state: peak-sized
		// reservations made a normal kit unplaceable on the 2-core machines
		// homelab kits target.
		{
			name:    "database",
			svcType: "postgres",
			wantCPU: 300,
			wantRAM: 512,
			wantGPU: false,
		},
		{
			name:    "reverse_proxy",
			svcType: "traefik",
			wantCPU: 100,
			wantRAM: 128,
			wantGPU: false,
		},
		{
			name:    "unknown_service",
			svcType: "custom-app",
			wantCPU: 200,
			wantRAM: 256,
			wantGPU: false,
		},
		{
			name:    "machine_learning_is_burstable_not_a_dedicated_core",
			svcType: ServiceTypeImmichML,
			wantCPU: 400,
			wantRAM: 1024,
			wantGPU: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := core.ServiceSpec{Type: tt.svcType}
			req := estimateServiceRequirements(svc)

			if req.CPU != tt.wantCPU {
				t.Errorf("CPU = %d, want %d", req.CPU, tt.wantCPU)
			}
			if req.RAM != tt.wantRAM {
				t.Errorf("RAM = %d, want %d", req.RAM, tt.wantRAM)
			}
			if req.RequiresGPU != tt.wantGPU {
				t.Errorf("RequiresGPU = %v, want %v", req.RequiresGPU, tt.wantGPU)
			}
		})
	}
}
