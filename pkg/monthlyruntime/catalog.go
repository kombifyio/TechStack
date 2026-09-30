// Package monthlyruntime owns TechStack's subscription-backed runtime products.
package monthlyruntime

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

const (
	MetadataKeyRuntimeLane        = "runtime_lane"
	MetadataKeyRuntimeOfferingID  = "runtime_offering_id"
	MetadataKeyBillingCadence     = "billing_cadence"
	MetadataKeyServerMode         = "server_mode"
	MetadataKeyBillingMode        = "billing_mode"
	MetadataKeyProviderID         = providercatalog.ProviderIDField
	MetadataKeyLeaseProvider      = providercatalog.LegacyLeaseProviderField
	MetadataKeyProviderRegion     = "provider_region"
	MetadataKeyIONOSDatacenter    = "ionos_datacenter"
	MetadataKeySimulateProviderID = providercatalog.LegacySimulateProviderIDField
	MetadataKeySimulateLifecycle  = "simulate_node_lifecycle"

	ServerModeManagedCloud = "managed-cloud"
	ProviderCentron        = providercatalog.ProviderCentron
	ProviderIONOS          = providercatalog.ProviderIONOS
	DefaultIONOSDatacenter = "de/fra"
	NodeLifecyclePVM       = "pvm"
	BillingSubscription    = "subscription"
)

// Offering is one customer-facing managed-runtime product. Its VCPUs,
// MemoryMB and DiskGB are the custom server size for providers without a
// recorded fixed package (Centron today). A provider with a fixed package
// provisions that package instead; PackageForProvider is the only size a
// provider adapter may use.
type Offering struct {
	ID             serverruntime.RuntimeOfferingID `json:"id"`
	Name           string                          `json:"name"`
	BillingCadence serverruntime.BillingCadence    `json:"billing_cadence"`
	Image          string                          `json:"image"`
	VCPUs          int                             `json:"vcpus"`
	MemoryMB       int                             `json:"memory_mb"`
	DiskGB         int                             `json:"disk_gb"`
	Region         string                          `json:"region"`
}

func Catalog() []Offering {
	return []Offering{
		{
			ID:             serverruntime.RuntimeOfferingStandard,
			Name:           "Monthly Runtime Standard",
			BillingCadence: serverruntime.BillingCadenceMonthly,
			Image:          "ubuntu-24.04",
			// StackKits declares the Cloud Kit minimum as 2 vCPU / 4 GB / 20 GB
			// (PROVIDER-CATALOG §11). IONOS Standard is Basic Cube S, exactly
			// that floor; a live Cloud Kit lane on Cube S must confirm it
			// before customers get Standard. The custom 4 vCPU / 8 GiB size
			// below stays for Centron until its package mapping is recorded.
			VCPUs:    4,
			MemoryMB: 8192,
			DiskGB:   80,
			Region:   "de-fra",
		},
		{
			ID:             serverruntime.RuntimeOfferingPremium,
			Name:           "Monthly Runtime Premium",
			BillingCadence: serverruntime.BillingCadenceMonthly,
			Image:          "ubuntu-24.04",
			VCPUs:          4,
			MemoryMB:       8192,
			DiskGB:         320,
			Region:         "de-fra",
		},
	}
}

// ProviderPackage is the machine one provider provisions for one offering.
// Template names a fixed provider package the adapter must resolve and verify
// at runtime; an empty Template means a custom size from the offering.
type ProviderPackage struct {
	ProviderID string
	OfferingID serverruntime.RuntimeOfferingID
	Template   string
	// Location pins where the package can be created; empty means the lease's
	// region.
	Location string
	VCPUs    int
	MemoryMB int
	DiskGB   int
}

// IONOSCubeDatacenter is where Basic Cubes are created. IONOS location de/fra
// has no cube feature (create answers 403 "427 Access Denied as the location
// does not support the cube feature"); its Frankfurt sub-location de/fra/2
// (frankfurt-east) lists "cube" in GET /locations (verified 2026-09-24).
const IONOSCubeDatacenter = "de/fra/2"

// ionosBasicCubes is the owner decision of 2026-09-24 (PROVIDER-CATALOG §11):
// IONOS provisions only Basic Cubes, never a custom VCPU size.
var ionosBasicCubes = map[serverruntime.RuntimeOfferingID]ProviderPackage{
	serverruntime.RuntimeOfferingStandard: {
		ProviderID: ProviderIONOS, OfferingID: serverruntime.RuntimeOfferingStandard,
		Template: "Basic Cube S", Location: IONOSCubeDatacenter, VCPUs: 2, MemoryMB: 4096, DiskGB: 120,
	},
	serverruntime.RuntimeOfferingPremium: {
		ProviderID: ProviderIONOS, OfferingID: serverruntime.RuntimeOfferingPremium,
		Template: "Basic Cube M", Location: IONOSCubeDatacenter, VCPUs: 4, MemoryMB: 8192, DiskGB: 240,
	},
}

// PackageForProvider resolves the exact machine a provider provisions for an
// ordered offering. An unknown offering or provider fails closed.
func PackageForProvider(providerID, offeringID string) (ProviderPackage, error) {
	offering, err := ResolveOffering(offeringID)
	if err != nil {
		return ProviderPackage{}, err
	}
	switch strings.TrimSpace(providerID) {
	case ProviderIONOS:
		pkg, ok := ionosBasicCubes[offering.ID]
		if !ok {
			return ProviderPackage{}, fmt.Errorf("monthlyruntime: offering %q has no IONOS package", offering.ID)
		}
		return pkg, nil
	case ProviderCentron:
		return ProviderPackage{
			ProviderID: ProviderCentron, OfferingID: offering.ID,
			VCPUs: offering.VCPUs, MemoryMB: offering.MemoryMB, DiskGB: offering.DiskGB,
		}, nil
	default:
		return ProviderPackage{}, fmt.Errorf("monthlyruntime: provider %q has no package for offering %q", providerID, offering.ID)
	}
}

func OfferingByID(id serverruntime.RuntimeOfferingID) (Offering, bool) {
	for _, offering := range Catalog() {
		if offering.ID == id {
			return offering, true
		}
	}
	return Offering{}, false
}

// ResolveOffering requires an explicit catalog product for cost-bearing
// provider work. Provider custody defaults are not an order and must never be
// used as a silent replacement for a missing or unknown offering.
func ResolveOffering(id string) (Offering, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return Offering{}, fmt.Errorf("monthlyruntime: runtime offering is required")
	}
	offering, ok := OfferingByID(serverruntime.RuntimeOfferingID(trimmed))
	if !ok {
		return Offering{}, fmt.Errorf("monthlyruntime: unsupported runtime offering %q", trimmed)
	}
	return offering, nil
}

func OfferingForMinimumResources(minVCPUs, minMemoryMB int) (Offering, bool) {
	var selected Offering
	for _, offering := range Catalog() {
		if minVCPUs > 0 && offering.VCPUs < minVCPUs {
			continue
		}
		if minMemoryMB > 0 && offering.MemoryMB < minMemoryMB {
			continue
		}
		if selected.ID == "" || offering.VCPUs < selected.VCPUs ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB < selected.MemoryMB) {
			selected = offering
		}
	}
	return selected, selected.ID != ""
}

func LargestOffering() (Offering, bool) {
	var selected Offering
	for _, offering := range Catalog() {
		if selected.ID == "" || offering.VCPUs > selected.VCPUs ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB > selected.MemoryMB) ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB == selected.MemoryMB && offering.DiskGB > selected.DiskGB) {
			selected = offering
		}
	}
	return selected, selected.ID != ""
}

func DefaultOfferingID() serverruntime.RuntimeOfferingID {
	return serverruntime.RuntimeOfferingStandard
}

func OfferingIDFromMetadata(metadata map[string]string) serverruntime.RuntimeOfferingID {
	if metadata == nil {
		return DefaultOfferingID()
	}
	if raw := strings.TrimSpace(metadata[MetadataKeyRuntimeOfferingID]); raw != "" {
		return serverruntime.RuntimeOfferingID(raw)
	}
	return DefaultOfferingID()
}

// NormalizeMetadata formats non-authoritative monthly-runtime metadata. Fresh
// product writes must call NormalizeFreshMetadata so invalid or legacy provider
// identity is returned as an error before persistence.
func NormalizeMetadata(metadata map[string]string, offeringID serverruntime.RuntimeOfferingID) map[string]string {
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	delete(out, MetadataKeyLeaseProvider)
	delete(out, MetadataKeySimulateProviderID)
	if offeringID == "" {
		offeringID = OfferingIDFromMetadata(metadata)
	}
	if _, ok := OfferingByID(offeringID); !ok {
		offeringID = DefaultOfferingID()
	}
	if out[MetadataKeyServerMode] == ServerModeManagedCloud || strings.TrimSpace(out[MetadataKeyServerMode]) == "" {
		out[MetadataKeyServerMode] = serverruntime.RuntimeLaneMonthly
	}
	out[MetadataKeyRuntimeLane] = serverruntime.RuntimeLaneMonthly
	out[MetadataKeyRuntimeOfferingID] = string(offeringID)
	out[MetadataKeyBillingCadence] = string(serverruntime.BillingCadenceMonthly)
	if strings.TrimSpace(out[MetadataKeyBillingMode]) == "" {
		out[MetadataKeyBillingMode] = BillingSubscription
	}
	if out[MetadataKeyProviderID] == "" {
		out[MetadataKeyProviderID] = ProviderCentron
	}
	if out[MetadataKeyProviderID] == ProviderIONOS {
		datacenter := NormalizeIONOSDatacenter(firstNonEmptyString(
			out[MetadataKeyIONOSDatacenter],
			out[MetadataKeyProviderRegion],
		))
		out[MetadataKeyIONOSDatacenter] = datacenter
		out[MetadataKeyProviderRegion] = datacenter
	}
	if strings.TrimSpace(out[MetadataKeySimulateLifecycle]) == "" {
		out[MetadataKeySimulateLifecycle] = NodeLifecyclePVM
	}
	return out
}

// NormalizeFreshMetadata validates exact provider identity before returning
// metadata suitable for a new managed-runtime write. Composite aliases and the
// legacy provider fields are rejected rather than translated.
func NormalizeFreshMetadata(metadata map[string]string, offeringID serverruntime.RuntimeOfferingID) (map[string]string, error) {
	if err := providercatalog.ValidateNoLegacyProviderFields(
		metadata[MetadataKeyLeaseProvider],
		metadata[MetadataKeySimulateProviderID],
	); err != nil {
		return nil, fmt.Errorf("monthlyruntime: %w", err)
	}
	providerID := metadata[MetadataKeyProviderID]
	if _, err := providercatalog.CanonicalProviderID(providerID); err != nil {
		return nil, fmt.Errorf("monthlyruntime: %w", err)
	}
	out := NormalizeMetadata(metadata, offeringID)
	out[MetadataKeyProviderID] = providerID
	return out, nil
}

func NormalizeIONOSDatacenter(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	normalized = strings.ReplaceAll(normalized, "_", "/")
	switch normalized {
	case "", "default", "de/fra", "de-fra", "fra", "frankfurt":
		return DefaultIONOSDatacenter
	case "de/txl", "de-txl", "txl", "berlin":
		return "de/txl"
	case "us/ewr", "us-ewr", "ewr", "newark":
		return "us/ewr"
	case "us/las", "us-las", "las", "las-vegas":
		return "us/las"
	case "de/fra/2", "de-fra-2", "fra2", "frankfurt-2":
		return "de/fra/2"
	default:
		return DefaultIONOSDatacenter
	}
}

func ProviderFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ProviderCentron
	}
	if value := strings.TrimSpace(metadata[MetadataKeyProviderID]); value != "" {
		return value
	}
	return ProviderCentron
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func IsMonthlyRuntimeMetadata(metadata map[string]string) bool {
	if metadata == nil {
		return false
	}
	return metadata[MetadataKeyRuntimeLane] == serverruntime.RuntimeLaneMonthly ||
		metadata[MetadataKeyServerMode] == serverruntime.RuntimeLaneMonthly ||
		metadata[MetadataKeyServerMode] == ServerModeManagedCloud
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
