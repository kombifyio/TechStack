package placement

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// freshManagedWorker is the shape a newly provisioned managed VM actually has:
// real resources reported by Guard, and no container runtime yet, because the
// StackKit rollout is what installs it.
func freshManagedWorker() core.Worker {
	w := core.Worker{ID: "worker-1", Name: "ubuntu", Status: "online"}
	w.Capabilities.CPU = 4
	w.Capabilities.RAM = 3858
	w.Capabilities.Disk = 76
	w.Capabilities.Arch = "amd64"
	w.Capabilities.OS = "ubuntu"
	w.Capabilities.DockerVersion = ""
	return w
}

func traefikService() core.ServiceSpec {
	return core.ServiceSpec{Name: "traefik", Type: "traefik"}
}

// The bug: every service defaults to RequiresDocker, Guard never reports a
// docker version, and the rollout that installs Docker could therefore never be
// planned. Live this surfaced as
// "no suitable worker found for service traefik (requirements not met)".
func TestPlacementRejectsAFreshManagedWorkerWithoutTheOption(t *testing.T) {
	engine := NewPlacementEngine()

	_, _, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{freshManagedWorker()}, []core.ServiceSpec{traefikService()})
	if err == nil {
		t.Fatal("expected the default engine to still treat Docker as a hard requirement")
	}
	if !strings.Contains(err.Error(), "no suitable worker") {
		t.Fatalf("error = %v, want the placement rejection", err)
	}
}

// With the rollout declaring that it provisions the runtime, the same worker is
// placeable.
func TestPlacementAcceptsAFreshManagedWorkerWhenTheRolloutProvisionsTheRuntime(t *testing.T) {
	engine := NewPlacementEngine().WithProvisionedContainerRuntime()

	placements, quality, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{freshManagedWorker()}, []core.ServiceSpec{traefikService()})
	if err != nil {
		t.Fatalf("PlaceServices: %v", err)
	}
	if len(placements) != 1 {
		t.Fatalf("placements = %d, want 1", len(placements))
	}
	if placements[0].WorkerID != "worker-1" {
		t.Fatalf("worker = %q, want worker-1", placements[0].WorkerID)
	}
	if quality <= 0 {
		t.Fatalf("quality = %d, want a positive score", quality)
	}
}

// The option relaxes ONLY the container runtime. Everything else that protects
// a placement still rejects.
func TestProvisionedRuntimeOptionDoesNotRelaxOtherPredicates(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*core.Worker)
	}{
		{"offline worker", func(w *core.Worker) { w.Status = "offline" }},
		{"too little ram", func(w *core.Worker) { w.Capabilities.RAM = 8 }},
		{"too little disk", func(w *core.Worker) { w.Capabilities.Disk = 1 }},
		{"wrong architecture", func(w *core.Worker) { w.Capabilities.Arch = "arm64" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := freshManagedWorker()
			test.mutate(&worker)
			engine := NewPlacementEngine().WithProvisionedContainerRuntime()

			if _, _, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{worker}, []core.ServiceSpec{traefikService()}); err == nil {
				t.Fatalf("%s was accepted; the option must only relax the container runtime", test.name)
			}
		})
	}
}

// A host that already reports a runtime is unaffected either way.
func TestPlacementStillAcceptsAWorkerThatAlreadyReportsDocker(t *testing.T) {
	worker := freshManagedWorker()
	worker.Capabilities.DockerVersion = "27.1.1"

	for name, engine := range map[string]*PlacementEngine{
		"default":             NewPlacementEngine(),
		"provisioned runtime": NewPlacementEngine().WithProvisionedContainerRuntime(),
	} {
		placements, _, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{worker}, []core.ServiceSpec{traefikService()})
		if err != nil {
			t.Fatalf("%s: PlaceServices: %v", name, err)
		}
		if len(placements) != 1 {
			t.Fatalf("%s: placements = %d, want 1", name, len(placements))
		}
	}
}
