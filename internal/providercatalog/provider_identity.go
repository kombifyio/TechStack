package providercatalog

import (
	"errors"
	"fmt"
)

const (
	// ProviderCentron is the stable vendor identity for Centron.
	ProviderCentron = "centron"
	// ProviderIONOS is the stable vendor identity for IONOS.
	ProviderIONOS = "ionos"

	// ProviderIDField is the only provider identity field accepted on fresh
	// managed-runtime product writes.
	ProviderIDField = "provider_id"
	// LegacyLeaseProviderField is retained only to identify and reject the old
	// executable write field. Historical read models may still expose it as an
	// opaque label.
	LegacyLeaseProviderField = "lease_provider"
	// LegacySimulateProviderIDField is retained only to identify and reject the
	// old Simulate-coupled write field. It is never a provider identity source.
	LegacySimulateProviderIDField = "simulate_provider_id"
)

var (
	ErrProviderIDRequired       = errors.New("provider_id is required")
	ErrUnsupportedProviderID    = errors.New("unsupported provider_id")
	ErrCompositeProviderID      = errors.New("composite provider alias is not executable")
	ErrConflictingProviderIDs   = errors.New("conflicting provider_id values")
	ErrLegacyProviderWriteField = errors.New("legacy provider write fields are not accepted")
)

// CanonicalProviderID validates one required, exact provider identity. It does
// not normalize composite aliases, case, or whitespace.
func CanonicalProviderID(raw string) (string, error) {
	switch raw {
	case ProviderCentron, ProviderIONOS:
		return raw, nil
	case "":
		return "", ErrProviderIDRequired
	case "centron-managed", "ionos-managed":
		return "", fmt.Errorf("%w: %q", ErrCompositeProviderID, raw)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedProviderID, raw)
	}
}

// OptionalCanonicalProviderID validates an optional provider identity. Empty
// means unspecified; every non-empty value must already be canonical.
func OptionalCanonicalProviderID(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	return CanonicalProviderID(raw)
}

// ResolveCanonicalProviderID validates all supplied provider identity values
// and returns their single agreed value. Empty values are ignored. At least one
// canonical identity is required.
func ResolveCanonicalProviderID(values ...string) (string, error) {
	resolved := ""
	for _, value := range values {
		canonical, err := OptionalCanonicalProviderID(value)
		if err != nil {
			return "", err
		}
		if canonical == "" {
			continue
		}
		if resolved != "" && canonical != resolved {
			return "", fmt.Errorf("%w: %q and %q", ErrConflictingProviderIDs, resolved, canonical)
		}
		resolved = canonical
	}
	if resolved == "" {
		return "", ErrProviderIDRequired
	}
	return resolved, nil
}

// ValidateNoLegacyProviderFields rejects values supplied through obsolete
// executable provider fields. It deliberately does not convert them into a
// provider_id.
func ValidateNoLegacyProviderFields(values ...string) error {
	for _, value := range values {
		if value != "" {
			return fmt.Errorf("%w: %s and %s must be empty", ErrLegacyProviderWriteField, LegacyLeaseProviderField, LegacySimulateProviderIDField)
		}
	}
	return nil
}

// IsCanonicalProviderID reports whether raw is an exact accepted provider ID.
func IsCanonicalProviderID(raw string) bool {
	_, err := CanonicalProviderID(raw)
	return err == nil
}
