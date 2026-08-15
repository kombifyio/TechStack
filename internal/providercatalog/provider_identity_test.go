package providercatalog

import (
	"errors"
	"testing"
)

func TestCanonicalProviderIDAcceptsOnlyExactVendorIdentity(t *testing.T) {
	t.Parallel()

	for _, providerID := range []string{ProviderCentron, ProviderIONOS} {
		providerID := providerID
		t.Run(providerID, func(t *testing.T) {
			t.Parallel()
			got, err := CanonicalProviderID(providerID)
			if err != nil {
				t.Fatalf("CanonicalProviderID(%q): %v", providerID, err)
			}
			if got != providerID {
				t.Fatalf("CanonicalProviderID(%q) = %q", providerID, got)
			}
		})
	}
}

func TestCanonicalProviderIDRejectsAliasesAndNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  error
	}{
		{value: "", want: ErrProviderIDRequired},
		{value: "centron-managed", want: ErrCompositeProviderID},
		{value: "ionos-managed", want: ErrCompositeProviderID},
		{value: "IONOS", want: ErrUnsupportedProviderID},
		{value: " ionos", want: ErrUnsupportedProviderID},
		{value: "digitalocean", want: ErrUnsupportedProviderID},
	}
	for _, test := range tests {
		test := test
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonicalProviderID(test.value); !errors.Is(err, test.want) {
				t.Fatalf("CanonicalProviderID(%q) error = %v, want %v", test.value, err, test.want)
			}
			if IsCanonicalProviderID(test.value) {
				t.Fatalf("IsCanonicalProviderID(%q) = true", test.value)
			}
		})
	}
}

func TestResolveCanonicalProviderIDRequiresAgreement(t *testing.T) {
	t.Parallel()

	got, err := ResolveCanonicalProviderID("", ProviderIONOS, ProviderIONOS)
	if err != nil {
		t.Fatalf("ResolveCanonicalProviderID: %v", err)
	}
	if got != ProviderIONOS {
		t.Fatalf("ResolveCanonicalProviderID = %q", got)
	}

	if _, err := ResolveCanonicalProviderID(ProviderIONOS, ProviderCentron); !errors.Is(err, ErrConflictingProviderIDs) {
		t.Fatalf("conflicting values error = %v", err)
	}
	if _, err := ResolveCanonicalProviderID("", ""); !errors.Is(err, ErrProviderIDRequired) {
		t.Fatalf("empty values error = %v", err)
	}
}

func TestValidateNoLegacyProviderFields(t *testing.T) {
	t.Parallel()

	if err := ValidateNoLegacyProviderFields("", ""); err != nil {
		t.Fatalf("empty legacy fields: %v", err)
	}
	if err := ValidateNoLegacyProviderFields("ionos-managed", ""); !errors.Is(err, ErrLegacyProviderWriteField) {
		t.Fatalf("legacy field error = %v", err)
	}
	if err := ValidateNoLegacyProviderFields("", ProviderIONOS); !errors.Is(err, ErrLegacyProviderWriteField) {
		t.Fatalf("simulate field error = %v", err)
	}
}
