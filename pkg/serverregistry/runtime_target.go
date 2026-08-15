package serverregistry

import (
	"fmt"
	"strings"
	"time"
)

// EnvironmentClass identifies the physical hosting boundary of a runtime
// server. It intentionally has no "managed" value: a Kombify-operated VPS is
// still a cloud VM, while provider-native workloads have no Server aggregate.
type EnvironmentClass string

// Offering identifies the commercial/runtime shape within an environment.
type Offering string

// AvailabilityOwner owns the underlying infrastructure availability promise.
type AvailabilityOwner string

// OperationsOwner owns the day-to-day workload operation for a server.
type OperationsOwner string

const (
	EnvironmentLocal   EnvironmentClass = "local"
	EnvironmentCloud   EnvironmentClass = "cloud"
	EnvironmentUnknown EnvironmentClass = "unknown"

	OfferingSelfOwnedDevice Offering = "self_owned_device"
	OfferingExternalVPS     Offering = "external_vps"
	OfferingManagedVPS      Offering = "managed_vps"

	AvailabilityCustomer AvailabilityOwner = "customer"
	AvailabilityProvider AvailabilityOwner = "provider"

	OperationsCustomer OperationsOwner = "customer"
	OperationsKombify  OperationsOwner = "kombify"
)

// RuntimeTarget is the evidence-backed hosting classification of one runtime
// server. It is deliberately separate from lifecycle, connection and health:
// a cloud target can be healthy or offline, and an unclassified target must
// remain unknown instead of being guessed as local.
type RuntimeTarget struct {
	EnvironmentClass  EnvironmentClass
	Offering          Offering
	ProviderID        string
	ProviderTargetRef string
	AvailabilityOwner AvailabilityOwner
	OperationsOwner   OperationsOwner
	EvidenceRef       string
	ObservedAt        *time.Time
}

// UnknownRuntimeTarget is the fail-closed default for missing placement
// evidence. It must not be rendered or treated as a local device.
func UnknownRuntimeTarget() RuntimeTarget {
	return RuntimeTarget{EnvironmentClass: EnvironmentUnknown}
}

// NormalizeRuntimeTarget removes transport-only variation without inventing a
// classification. Unknown values stay invalid and are rejected by validation.
func NormalizeRuntimeTarget(target RuntimeTarget) RuntimeTarget {
	target.EnvironmentClass = EnvironmentClass(normalizeRuntimeTargetValue(string(target.EnvironmentClass)))
	target.Offering = Offering(normalizeRuntimeTargetValue(string(target.Offering)))
	target.ProviderID = normalizeRuntimeTargetValue(target.ProviderID)
	target.ProviderTargetRef = strings.TrimSpace(target.ProviderTargetRef)
	target.AvailabilityOwner = AvailabilityOwner(normalizeRuntimeTargetValue(string(target.AvailabilityOwner)))
	target.OperationsOwner = OperationsOwner(normalizeRuntimeTargetValue(string(target.OperationsOwner)))
	target.EvidenceRef = strings.TrimSpace(target.EvidenceRef)
	if target.ObservedAt != nil {
		observedAt := target.ObservedAt.UTC()
		target.ObservedAt = &observedAt
	}
	return target
}

// CloneRuntimeTarget keeps the aggregate read model immutable to callers.
func CloneRuntimeTarget(target RuntimeTarget) RuntimeTarget {
	if target.ObservedAt != nil {
		observedAt := *target.ObservedAt
		target.ObservedAt = &observedAt
	}
	return target
}

// RuntimeTargetEqual compares the normalized durable target shape, including
// the evidence observation time.
func RuntimeTargetEqual(left, right RuntimeTarget) bool {
	left = NormalizeRuntimeTarget(left)
	right = NormalizeRuntimeTarget(right)
	if left.EnvironmentClass != right.EnvironmentClass || left.Offering != right.Offering ||
		left.ProviderID != right.ProviderID || left.ProviderTargetRef != right.ProviderTargetRef ||
		left.AvailabilityOwner != right.AvailabilityOwner || left.OperationsOwner != right.OperationsOwner ||
		left.EvidenceRef != right.EvidenceRef {
		return false
	}
	if left.ObservedAt == nil || right.ObservedAt == nil {
		return left.ObservedAt == right.ObservedAt
	}
	return left.ObservedAt.UTC().Equal(right.ObservedAt.UTC())
}

// RuntimeTargetIntentPresent reports whether a command explicitly carries a
// target classification. An empty target is a no-op patch; an explicit
// EnvironmentUnknown clears a previously classified target.
func RuntimeTargetIntentPresent(target RuntimeTarget) bool {
	return strings.TrimSpace(string(target.EnvironmentClass)) != "" ||
		strings.TrimSpace(string(target.Offering)) != "" ||
		strings.TrimSpace(target.ProviderID) != "" ||
		strings.TrimSpace(target.ProviderTargetRef) != "" ||
		strings.TrimSpace(string(target.AvailabilityOwner)) != "" ||
		strings.TrimSpace(string(target.OperationsOwner)) != "" ||
		strings.TrimSpace(target.EvidenceRef) != "" || target.ObservedAt != nil
}

// HostingerExternalVPSTarget accepts only the canonical Hostinger provider
// binding forms already persisted on a RuntimeServer. It is intentionally not
// a fuzzy provider-name detector: weaker evidence remains unknown.
func HostingerExternalVPSTarget(providerRef string, observedAt time.Time) (RuntimeTarget, bool) {
	providerRef = strings.TrimSpace(providerRef)
	normalized := strings.ToLower(providerRef)
	if providerRef == "" || observedAt.IsZero() ||
		(normalized != "hostinger" && normalized != "hostinger-vps" &&
			!strings.HasPrefix(normalized, "hostinger:") &&
			!strings.HasPrefix(normalized, "hostinger-vps:")) {
		return RuntimeTarget{}, false
	}
	observedAt = observedAt.UTC()
	return RuntimeTarget{
		EnvironmentClass:  EnvironmentCloud,
		Offering:          OfferingExternalVPS,
		ProviderID:        "hostinger",
		ProviderTargetRef: providerRef,
		AvailabilityOwner: AvailabilityProvider,
		OperationsOwner:   OperationsCustomer,
		EvidenceRef:       "server-provider-binding:hostinger",
		ObservedAt:        &observedAt,
	}, true
}

// ManagedVPSRuntimeTarget builds the classification only for callers that
// already proved native provider-control custody of the exact lease and the
// provider's concrete VM/resource reference. It does not itself establish that
// custody. A lease ID is evidence of Kombify custody, not a provider target.
func ManagedVPSRuntimeTarget(providerID, providerTargetRef, leaseID string, observedAt time.Time) RuntimeTarget {
	providerID = normalizeRuntimeTargetValue(providerID)
	providerTargetRef = strings.TrimSpace(providerTargetRef)
	leaseID = strings.TrimSpace(leaseID)
	if providerID == "" || providerTargetRef == "" || leaseID == "" || observedAt.IsZero() {
		return UnknownRuntimeTarget()
	}
	observedAt = observedAt.UTC()
	return RuntimeTarget{
		EnvironmentClass:  EnvironmentCloud,
		Offering:          OfferingManagedVPS,
		ProviderID:        providerID,
		ProviderTargetRef: providerTargetRef,
		AvailabilityOwner: AvailabilityProvider,
		OperationsOwner:   OperationsKombify,
		EvidenceRef:       "runtime-lease:" + leaseID,
		ObservedAt:        &observedAt,
	}
}

// ValidateRuntimeTarget rejects classifications that would collapse a cloud
// VM into a Managed workload, claim ownership without evidence, or infer a
// local device from absent evidence.
func ValidateRuntimeTarget(target RuntimeTarget, leaseID string) error {
	target = NormalizeRuntimeTarget(target)
	leaseID = strings.TrimSpace(leaseID)
	knownEvidence := func() bool {
		return target.EvidenceRef != "" && target.ObservedAt != nil && !target.ObservedAt.IsZero()
	}

	switch target.EnvironmentClass {
	case EnvironmentUnknown:
		if target.Offering != "" || target.ProviderID != "" || target.ProviderTargetRef != "" ||
			target.AvailabilityOwner != "" || target.OperationsOwner != "" ||
			target.EvidenceRef != "" || target.ObservedAt != nil {
			return fmt.Errorf("unknown runtime target cannot carry provider, owner, or evidence fields")
		}
		return nil
	case EnvironmentLocal:
		if target.Offering != OfferingSelfOwnedDevice || target.ProviderID != "" || target.ProviderTargetRef != "" ||
			target.AvailabilityOwner != AvailabilityCustomer || target.OperationsOwner != OperationsCustomer || !knownEvidence() {
			return fmt.Errorf("local runtime target requires self_owned_device, customer ownership, and observed evidence")
		}
		return nil
	case EnvironmentCloud:
		if !knownEvidence() || target.ProviderID == "" || target.ProviderTargetRef == "" ||
			target.AvailabilityOwner != AvailabilityProvider {
			return fmt.Errorf("cloud runtime target requires provider binding, provider availability, and observed evidence")
		}
		switch target.Offering {
		case OfferingExternalVPS:
			if target.OperationsOwner != OperationsCustomer {
				return fmt.Errorf("external_vps requires customer operations ownership")
			}
			return nil
		case OfferingManagedVPS:
			if target.OperationsOwner != OperationsKombify || leaseID == "" {
				return fmt.Errorf("managed_vps requires Kombify operations ownership and an exact lease")
			}
			return nil
		default:
			return fmt.Errorf("cloud runtime target has unsupported offering %q", target.Offering)
		}
	default:
		return fmt.Errorf("unknown runtime environment class %q", target.EnvironmentClass)
	}
}

func normalizeRuntimeTargetValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
