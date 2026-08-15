package monthlyruntime

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

func TestCatalogExposesStandardAndPremiumOfferings(t *testing.T) {
	standard, ok := OfferingByID(serverruntime.RuntimeOfferingStandard)
	if !ok {
		t.Fatal("expected standard offering")
	}
	if standard.ID != serverruntime.RuntimeOfferingStandard ||
		standard.VCPUs != 2 ||
		standard.MemoryMB != 4096 ||
		standard.DiskGB != 80 ||
		standard.Image != "ubuntu-24.04" ||
		standard.Region != "de-fra" ||
		standard.BillingCadence != serverruntime.BillingCadenceMonthly {
		t.Fatalf("standard offering = %+v", standard)
	}

	premium, ok := OfferingByID(serverruntime.RuntimeOfferingPremium)
	if !ok {
		t.Fatal("expected premium offering")
	}
	if premium.ID != serverruntime.RuntimeOfferingPremium ||
		premium.VCPUs != 4 ||
		premium.MemoryMB != 8192 ||
		premium.DiskGB != 320 ||
		premium.Image != "ubuntu-24.04" ||
		premium.Region != "de-fra" ||
		premium.BillingCadence != serverruntime.BillingCadenceMonthly {
		t.Fatalf("premium offering = %+v", premium)
	}
}

func TestNormalizeMetadataDefaultsLegacyManagedCloudToStandard(t *testing.T) {
	metadata := map[string]string{
		"server_mode": "managed-cloud",
	}

	normalized := NormalizeMetadata(metadata, serverruntime.RuntimeOfferingID(""))

	if normalized["server_mode"] != serverruntime.RuntimeLaneMonthly {
		t.Fatalf("server_mode = %q, want monthly-runtime", normalized["server_mode"])
	}
	if normalized["runtime_lane"] != serverruntime.RuntimeLaneMonthly {
		t.Fatalf("runtime_lane = %q, want monthly-runtime", normalized["runtime_lane"])
	}
	if normalized["runtime_offering_id"] != string(serverruntime.RuntimeOfferingStandard) {
		t.Fatalf("runtime_offering_id = %q, want standard", normalized["runtime_offering_id"])
	}
	if normalized["billing_cadence"] != string(serverruntime.BillingCadenceMonthly) {
		t.Fatalf("billing_cadence = %q, want monthly", normalized["billing_cadence"])
	}
	if metadata["runtime_lane"] != "" {
		t.Fatalf("NormalizeMetadata mutated input metadata: %+v", metadata)
	}
}

func TestNormalizeFreshMetadataPreservesCanonicalIONOSProvider(t *testing.T) {
	metadata := map[string]string{
		"provider_id":      ProviderIONOS,
		"provider_region":  "newark",
		"ionos_datacenter": "",
	}

	normalized, err := NormalizeFreshMetadata(metadata, serverruntime.RuntimeOfferingStandard)
	if err != nil {
		t.Fatalf("NormalizeFreshMetadata: %v", err)
	}

	if normalized["provider_id"] != ProviderIONOS {
		t.Fatalf("provider_id = %q, want %q", normalized["provider_id"], ProviderIONOS)
	}
	if normalized["lease_provider"] != "" || normalized["simulate_provider_id"] != "" {
		t.Fatalf("fresh metadata emitted legacy provider fields: %+v", normalized)
	}
	if normalized["ionos_datacenter"] != "us/ewr" {
		t.Fatalf("ionos_datacenter = %q, want us/ewr", normalized["ionos_datacenter"])
	}
	if normalized["provider_region"] != "us/ewr" {
		t.Fatalf("provider_region = %q, want us/ewr", normalized["provider_region"])
	}
	if got := ProviderFromMetadata(normalized); got != ProviderIONOS {
		t.Fatalf("ProviderFromMetadata = %q, want %q", got, ProviderIONOS)
	}
}

func TestNormalizeFreshMetadataRejectsCompositeAliasesAndLegacyFields(t *testing.T) {
	t.Parallel()

	for name, metadata := range map[string]map[string]string{
		"composite provider_id": {MetadataKeyProviderID: "ionos-managed"},
		"legacy lease field":    {MetadataKeyProviderID: ProviderIONOS, MetadataKeyLeaseProvider: "ionos-managed"},
		"legacy simulate field": {MetadataKeyProviderID: ProviderIONOS, MetadataKeySimulateProviderID: ProviderIONOS},
	} {
		metadata := metadata
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NormalizeFreshMetadata(metadata, serverruntime.RuntimeOfferingStandard)
			if err == nil {
				t.Fatal("NormalizeFreshMetadata succeeded")
			}
		})
	}
	if _, err := NormalizeFreshMetadata(map[string]string{}, serverruntime.RuntimeOfferingStandard); !errors.Is(err, providercatalog.ErrProviderIDRequired) {
		t.Fatalf("missing provider error = %v", err)
	}
}

func TestNormalizeIONOSDatacenterDefaultsAndAliases(t *testing.T) {
	for raw, want := range map[string]string{
		"":          "de/fra",
		"frankfurt": "de/fra",
		"de-txl":    "de/txl",
		"berlin":    "de/txl",
		"us-ewr":    "us/ewr",
		"newark":    "us/ewr",
	} {
		if got := NormalizeIONOSDatacenter(raw); got != want {
			t.Fatalf("NormalizeIONOSDatacenter(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProvisioningSpecForOfferingUsesOfferingResources(t *testing.T) {
	spec, err := ProvisioningSpecForOffering(serverruntime.RuntimeOfferingPremium, "stack-vm", "main", map[string]string{
		"stack_id":    "stack-1",
		"provider_id": ProviderCentron,
	})
	if err != nil {
		t.Fatalf("ProvisioningSpecForOffering: %v", err)
	}
	if spec.Name != "stack-vm" || spec.Role != "main" {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.VCPUs != 4 || spec.MemoryMB != 8192 || spec.DiskGB != 320 {
		t.Fatalf("resources = %+v, want premium resources", spec)
	}
	if spec.Metadata["runtime_offering_id"] != string(serverruntime.RuntimeOfferingPremium) {
		t.Fatalf("metadata = %+v, want premium offering", spec.Metadata)
	}
}

func TestProvisioningSpecForOfferingUsesIONOSDatacenterRegion(t *testing.T) {
	spec, err := ProvisioningSpecForOffering(serverruntime.RuntimeOfferingStandard, "stack-vm", "main", map[string]string{
		"provider_id":      ProviderIONOS,
		"provider_region":  "us-ewr",
		"ionos_datacenter": "us-ewr",
	})
	if err != nil {
		t.Fatalf("ProvisioningSpecForOffering: %v", err)
	}
	if spec.Region != "us/ewr" {
		t.Fatalf("Region = %q, want us/ewr", spec.Region)
	}
	if spec.Metadata["provider_region"] != "us/ewr" || spec.Metadata["ionos_datacenter"] != "us/ewr" {
		t.Fatalf("metadata = %+v, want normalized IONOS datacenter", spec.Metadata)
	}
}

func TestOfferingForMinimumResourcesChoosesSmallestMatchingOffering(t *testing.T) {
	standard, ok := OfferingForMinimumResources(2, 4096)
	if !ok {
		t.Fatal("expected standard offering for small runtime")
	}
	if standard.ID != serverruntime.RuntimeOfferingStandard {
		t.Fatalf("offering = %q, want standard", standard.ID)
	}

	premium, ok := OfferingForMinimumResources(4, 8192)
	if !ok {
		t.Fatal("expected premium offering for larger runtime")
	}
	if premium.ID != serverruntime.RuntimeOfferingPremium {
		t.Fatalf("offering = %q, want premium", premium.ID)
	}
}

func TestOfferingForMinimumResourcesRejectsUnsupportedShape(t *testing.T) {
	if offering, ok := OfferingForMinimumResources(5, 4096); ok {
		t.Fatalf("offering = %+v, want unsupported", offering)
	}
	if offering, ok := OfferingForMinimumResources(2, 32768); ok {
		t.Fatalf("offering = %+v, want unsupported", offering)
	}
}

func TestLargestOfferingReturnsHighestCapacityOffering(t *testing.T) {
	offering, ok := LargestOffering()
	if !ok {
		t.Fatal("expected largest offering")
	}
	if offering.ID != serverruntime.RuntimeOfferingPremium {
		t.Fatalf("offering = %q, want premium", offering.ID)
	}
}

func TestProvisioningSpecForOfferingUsesProviderSafeName(t *testing.T) {
	spec, err := ProvisioningSpecForOffering(
		DefaultOfferingID(),
		"e2e-centron-provider-20260609073045",
		"main",
		map[string]string{"provider_id": ProviderCentron},
	)
	if err != nil {
		t.Fatalf("ProvisioningSpecForOffering: %v", err)
	}
	if len(spec.Name) > 20 {
		t.Fatalf("provisioning name length = %d, want <= 20 (%q)", len(spec.Name), spec.Name)
	}
	if spec.Name == "e2e-centron-provider-20260609073045" {
		t.Fatalf("provisioning name was not normalized: %q", spec.Name)
	}
	if spec.Name != "e2e-centron-038d896a" {
		t.Fatalf("provisioning name = %q, want stable capped hash name", spec.Name)
	}
}
