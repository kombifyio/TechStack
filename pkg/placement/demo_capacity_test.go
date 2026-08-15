package placement

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// twoCoreHost is the smallest machine homelab kits promise to run on: a true
// 2 vCPU / 4GB VPS as Guard reports it (3858MB usable, 76GB disk). This is
// deliberately smaller than the live demo host so the invariant holds for the
// weakest supported hardware, not just for one rented VM.
func twoCoreHost() core.Worker {
	w := core.Worker{ID: "worker-2core", Name: "basement", Status: "online"}
	w.Capabilities.CPU = 2
	w.Capabilities.RAM = 3858
	w.Capabilities.Disk = 76
	w.Capabilities.Arch = "amd64"
	w.Capabilities.OS = "ubuntu"
	return w
}

// wizardDefaultKitServices mirrors defaultBaseKitVerifiedServices in pkg/jobs:
// the wizard emits generic types (media/auth/cache) with specific names, which
// is exactly the shape the estimator has to understand.
func wizardDefaultKitServices() []core.ServiceSpec {
	return []core.ServiceSpec{
		{Name: "traefik", Type: "reverse-proxy"},
		{Name: "pocket-id", Type: "auth"},
		{Name: "vaultwarden", Type: "auth"},
		{Name: "immich-server", Type: "media"},
		{Name: "immich-ml", Type: "media"},
		{Name: "immich-postgres", Type: "database"},
		{Name: "immich-redis", Type: "cache"},
		{Name: "otel-collector", Type: "monitoring"},
	}
}

// cloudKitDefaultServices is the broader cloud-kit shape including the extra
// convenience services.
func cloudKitDefaultServices() []core.ServiceSpec {
	return []core.ServiceSpec{
		{Name: "traefik", Type: "traefik"},
		{Name: "homepage", Type: "homepage"},
		{Name: "uptime-kuma", Type: "uptime-kuma"},
		{Name: "whoami", Type: "whoami"},
		{Name: "vaultwarden", Type: "vaultwarden"},
		{Name: "immich", Type: "immich"},
		{Name: "immich-ml", Type: ServiceTypeImmichML},
		{Name: "immich-db", Type: "postgres"},
		{Name: "files", Type: "cloudreve"},
	}
}

// The product decision this test pins (2026-07-31): homelab kits are for
// homelab hardware, and lots of homelab hardware has two cores. The full
// default service set must place on a genuine 2 vCPU / 4GB / 76GB machine.
// Reservations model steady-state usage; burst load time-shares.
func TestFullDefaultKitFitsA2Core4GBHost(t *testing.T) {
	engine := NewPlacementEngine().WithProvisionedContainerRuntime()

	for name, services := range map[string][]core.ServiceSpec{
		"wizard_default": wizardDefaultKitServices(),
		"cloud_kit":      cloudKitDefaultServices(),
	} {
		placements, quality, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{twoCoreHost()}, services)
		if err != nil {
			t.Fatalf("%s does not fit a 2-core/4GB host: %v", name, err)
		}
		if len(placements) != len(services) {
			t.Fatalf("%s: placed %d services, want %d", name, len(placements), len(services))
		}
		if quality <= 0 {
			t.Fatalf("%s: quality = %d, want a positive score", name, quality)
		}
	}
}

// The wizard emits type "media" for immich-ml; without the name-based match it
// would get the small generic default and overcommit the host.
func TestImmichMLEstimateMatchesByNameOverGenericType(t *testing.T) {
	byName := estimateServiceRequirements(core.ServiceSpec{Name: "immich-ml", Type: "media"})
	byType := estimateServiceRequirements(core.ServiceSpec{Type: ServiceTypeImmichML})
	if byName != byType {
		t.Fatalf("name-based estimate %+v differs from canonical %+v", byName, byType)
	}
	if byName.RAM < 1024 {
		t.Fatalf("immich-ml RAM reservation %dMB lost its real footprint", byName.RAM)
	}
}

// Capacity is still finite: a host that genuinely cannot hold the set is still
// rejected, so this is a better model rather than a disabled check.
func TestPlacementStillRefusesAHostThatIsTooSmall(t *testing.T) {
	tiny := twoCoreHost()
	tiny.Capabilities.CPU = 1
	tiny.Capabilities.RAM = 512

	engine := NewPlacementEngine().WithProvisionedContainerRuntime()
	_, _, err := engine.PlaceServices(
		&core.RequirementsSpec{}, []core.Worker{tiny}, cloudKitDefaultServices())
	if err == nil {
		t.Fatal("a 1 core / 512MB host accepted the full cloud-kit; capacity is no longer enforced")
	}
	// The rejection names the arithmetic instead of a bare "requirements not
	// met" (kombify-Techstack-jpm4).
	if !strings.Contains(err.Error(), "CPU") || !strings.Contains(err.Error(), "RAM") {
		t.Fatalf("rejection should state the missing resources, got: %v", err)
	}
}

// Reservations accumulate across placements rather than each service seeing an
// empty host.
func TestPlacementAccumulatesReservations(t *testing.T) {
	engine := NewPlacementEngine().WithProvisionedContainerRuntime()
	host := twoCoreHost()
	// Room for a couple of tiny services, not for the ML sidecar's RAM.
	host.Capabilities.CPU = 1
	host.Capabilities.RAM = 1024

	_, _, err := engine.PlaceServices(&core.RequirementsSpec{}, []core.Worker{host}, []core.ServiceSpec{
		{Name: "a", Type: "whoami"},
		{Name: "b", Type: "whoami"},
		{Name: "ml", Type: ServiceTypeImmichML}, // 400m + 1024MB: cannot fit alongside
	})
	if err == nil {
		t.Fatal("reservations did not accumulate; the machine-learning service should not have fit")
	}
}
