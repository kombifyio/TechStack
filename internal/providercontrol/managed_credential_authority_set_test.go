package providercontrol

import "testing"

// A handle minted for the wrong provider would attest custody the platform does
// not hold, so a mis-keyed registration must read as absent rather than as the
// keyed provider's authority.
func TestManagedCredentialAuthoritySetRejectsMisKeyedRegistrations(t *testing.T) {
	set := ManagedCredentialAuthoritySet{
		"ionos":   {ProviderID: "ionos"},
		"centron": {ProviderID: "ionos"},
		"stray":   {ProviderID: ""},
	}
	if _, ok := set.Authority("ionos"); !ok {
		t.Fatal("correctly keyed authority was not resolved")
	}
	for _, providerID := range []string{"centron", "stray", "hetzner", "", "  "} {
		if authority, ok := set.Authority(providerID); ok {
			t.Fatalf("provider %q resolved to %+v, want absent", providerID, authority)
		}
	}
}

// Each provider must resolve only its own authority; a populated set must not
// answer for providers it does not carry.
func TestManagedCredentialAuthoritySetIsPerProvider(t *testing.T) {
	set := ManagedCredentialAuthoritySet{
		"ionos":   {ProviderID: "ionos", Version: "dcd-v1"},
		"centron": {ProviderID: "centron", Version: "ccloud-v1"},
	}
	for providerID, wantVersion := range map[string]string{
		"ionos": "dcd-v1", "centron": "ccloud-v1",
	} {
		authority, ok := set.Authority(providerID)
		if !ok || authority.Version != wantVersion {
			t.Fatalf("provider %q resolved to %+v (ok=%v), want version %q",
				providerID, authority, ok, wantVersion)
		}
	}
	if _, ok := (ManagedCredentialAuthoritySet{}).Authority("ionos"); ok {
		t.Fatal("empty set resolved an authority")
	}
	if _, ok := ManagedCredentialAuthoritySet(nil).Authority("ionos"); ok {
		t.Fatal("nil set resolved an authority")
	}
}
