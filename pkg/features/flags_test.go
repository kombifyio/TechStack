package features

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

// MockStore is a test implementation of Store
type MockStore struct {
	flags    map[string]map[string]bool // userID -> featureKey -> enabled
	consents map[string]map[string]bool // userID -> featureKey -> hasConsent
}

func NewMockStore() *MockStore {
	return &MockStore{
		flags:    make(map[string]map[string]bool),
		consents: make(map[string]map[string]bool),
	}
}

func (m *MockStore) GetUserFlag(ctx context.Context, userID, featureKey string) (*bool, error) {
	if userFlags, ok := m.flags[userID]; ok {
		if enabled, exists := userFlags[featureKey]; exists {
			return &enabled, nil
		}
	}
	return nil, nil
}

func (m *MockStore) HasUserConsent(ctx context.Context, userID, featureKey string) (bool, error) {
	if userConsents, ok := m.consents[userID]; ok {
		return userConsents[featureKey], nil
	}
	return false, nil
}

// GetUserFlags implements batch flag retrieval for testing
func (m *MockStore) GetUserFlags(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if userFlags, ok := m.flags[userID]; ok {
		for _, key := range featureKeys {
			if enabled, exists := userFlags[key]; exists {
				result[key] = enabled
			}
		}
	}
	return result, nil
}

// GetUserConsentsMap implements batch consent retrieval for testing
func (m *MockStore) GetUserConsentsMap(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if userConsents, ok := m.consents[userID]; ok {
		for _, key := range featureKeys {
			if hasConsent, exists := userConsents[key]; exists && hasConsent {
				result[key] = true
			}
		}
	}
	return result, nil
}

// SeedUserFlag seeds a recorded per-user preference (test setup only —
// the production write path is Cloudflare Edge/OpenFeature entitlements).
func (m *MockStore) SeedUserFlag(userID, featureKey string, enabled bool) {
	if m.flags[userID] == nil {
		m.flags[userID] = make(map[string]bool)
	}
	m.flags[userID][featureKey] = enabled
}

// SeedConsent seeds a recorded consent (test setup only).
func (m *MockStore) SeedConsent(userID, featureKey string) {
	if m.consents[userID] == nil {
		m.consents[userID] = make(map[string]bool)
	}
	m.consents[userID][featureKey] = true
}

func TestIsEnabled_DefaultValues(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := context.Background()
	userID := "test-user"

	tests := []struct {
		name     string
		feature  string
		expected bool
	}{
		// Security/Beta features default to OFF (security-by-default)
		// Users must explicitly enable them
		{"network_discovery default", "network_discovery", false},
		{"cloud_backup default", "cloud_backup", false},
		// UX features default to ON
		{"onboarding_wizard default", "onboarding_wizard", true},
		{"keyboard_shortcuts default", "keyboard_shortcuts", true},
		{"dark_mode default", "dark_mode", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, err := svc.IsEnabled(ctx, tt.feature, userID)
			if err != nil {
				t.Errorf("IsEnabled returned error: %v", err)
			}
			if enabled != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, enabled)
			}
		})
	}
}

func TestIsEnabled_RequiresConsent(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := context.Background()
	userID := "test-user"

	// Network discovery requires consent.
	// Consent gates explicit opt-in enabling (user override), not default-enabled behavior.

	// Explicit enable (override=true) without consent should be blocked.
	store.SeedUserFlag(userID, "network_discovery", true)
	enabled, _ := svc.IsEnabled(ctx, "network_discovery", userID)
	if enabled {
		t.Error("Feature should be disabled without consent when explicitly enabled")
	}

	// Grant consent
	store.SeedConsent(userID, "network_discovery")

	enabled, _ = svc.IsEnabled(ctx, "network_discovery", userID)
	if !enabled {
		t.Error("Feature should be enabled with consent and user preference")
	}
}

func TestIsEnabled_AcceptsCanonicalFeatureKey(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := context.Background()
	userID := "test-user"

	// Preferences are stored under the local feature key; IsEnabled must
	// resolve the canonical (go-common) key to the same flag.
	store.SeedUserFlag(userID, "monthly_runtime", true)

	enabled, err := svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntime, userID)
	if err != nil {
		t.Fatalf("IsEnabled canonical key: %v", err)
	}
	if !enabled {
		t.Fatal("monthly runtime should be enabled through canonical key")
	}
}

func TestIsEnabled_ResolvesMonthlyRuntimeFromSignedEdgeFlags(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"sim.monthly.runtime.standard":           true,
			"sim.provider.cloud.managed.pvm.ionos":   true,
			"sim.provider.cloud.managed.pvm.centron": false,
		},
	})

	enabled, err := svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntime, "auth0|user-1")
	if err != nil {
		t.Fatalf("IsEnabled monthly runtime: %v", err)
	}
	if !enabled {
		t.Fatal("monthly runtime should be enabled by signed edge entitlement flag")
	}
	enabled, err = svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntimeIONOS, "auth0|user-1")
	if err != nil {
		t.Fatalf("IsEnabled ionos: %v", err)
	}
	if !enabled {
		t.Fatal("IONOS provider should be enabled by signed edge entitlement flag")
	}
	enabled, err = svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntimeCentron, "auth0|user-1")
	if err != nil {
		t.Fatalf("IsEnabled centron: %v", err)
	}
	if enabled {
		t.Fatal("Centron provider should be disabled by signed edge entitlement flag")
	}
}

func TestIsEnabled_ResolvesMonthlyRuntimeWhenPremiumEntitled(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"sim.monthly.runtime.standard": false,
			"sim.monthly.runtime.premium":  true,
		},
	})

	enabled, err := svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntime, "auth0|user-1")
	if err != nil {
		t.Fatalf("IsEnabled monthly runtime: %v", err)
	}
	if !enabled {
		t.Fatal("monthly runtime should be enabled when premium entitlement is true")
	}
}

func TestIsEnabled_ResolvesManagedRuntimeDottedKeysFromSignedEdgeFlags(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"techstack.managed.runtime":          true,
			"techstack.managed.runtime.cloudkit": true,
			"techstack.managed.runtime.ionos":    true,
			"techstack.managed.runtime.centron":  false,
		},
	})
	userID := "auth0|managed-runtime"

	for name, featureKey := range map[string]string{
		"canonical runtime":   "techstack.managed.runtime",
		"legacy runtime":      serverruntime.FeatureTechStackMonthlyRuntime,
		"canonical Cloud Kit": "techstack.managed.runtime.cloudkit",
		"legacy BaseKit":      serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
		"canonical IONOS":     "techstack.managed.runtime.ionos",
		"legacy IONOS":        serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
	} {
		enabled, err := svc.IsEnabled(ctx, featureKey, userID)
		if err != nil {
			t.Fatalf("IsEnabled %s: %v", name, err)
		}
		if !enabled {
			t.Fatalf("%s should be enabled by dotted managed runtime entitlement", name)
		}
	}

	enabled, err := svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntimeCentron, userID)
	if err != nil {
		t.Fatalf("IsEnabled Centron: %v", err)
	}
	if enabled {
		t.Fatal("Centron should remain disabled when only the IONOS dotted entitlement is enabled")
	}
}

func TestIsEnabled_ResolvesLegacyBaseKitEntitlementAsCloudKit(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"techstack.managed.runtime.basekit": true,
		},
	})
	userID := "auth0|legacy-basekit"

	for _, featureKey := range []string{"monthly_runtime_cloudkit", "techstack.managed.runtime.cloudkit"} {
		enabled, err := svc.IsEnabled(ctx, featureKey, userID)
		if err != nil {
			t.Fatalf("IsEnabled %s: %v", featureKey, err)
		}
		if !enabled {
			t.Fatalf("%s should be enabled by legacy basekit entitlement", featureKey)
		}
	}
}

func TestIsEnabled_ResolvesTechStackMonthlyRuntimeFromAllFeaturesEntitlement(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"all_features": true,
		},
	})
	userID := "auth0|all-features"

	for name, featureKey := range map[string]string{
		"monthly runtime": serverruntime.FeatureTechStackMonthlyRuntime,
		"Cloud Kit":       "techstack.managed.runtime.cloudkit",
		"Centron":         serverruntime.FeatureTechStackMonthlyRuntimeCentron,
		"IONOS":           serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
	} {
		enabled, err := svc.IsEnabled(ctx, featureKey, userID)
		if err != nil {
			t.Fatalf("IsEnabled %s: %v", name, err)
		}
		if !enabled {
			t.Fatalf("%s should be enabled by all_features entitlement", name)
		}
	}

	unrelated, err := svc.IsEnabled(ctx, "self_healing", userID)
	if err != nil {
		t.Fatalf("IsEnabled unrelated feature: %v", err)
	}
	if unrelated {
		t.Fatal("all_features edge entitlement must not enable unrelated beta features")
	}

	flags, err := svc.GetAllFlags(ctx, userID, true)
	if err != nil {
		t.Fatalf("GetAllFlags: %v", err)
	}
	for _, featureKey := range []string{"monthly_runtime", "monthly_runtime_cloudkit", "monthly_runtime_centron", "monthly_runtime_ionos"} {
		state, exists := flags[featureKey]
		if !exists {
			t.Fatalf("GetAllFlags missing %s", featureKey)
		}
		if !state.Enabled {
			t.Fatalf("GetAllFlags should enable %s from all_features entitlement", featureKey)
		}
	}
	if flags["self_healing"].Enabled {
		t.Fatal("GetAllFlags must not enable unrelated beta features from all_features entitlement")
	}
}

func TestIsEnabled_ResolvesTechStackMonthlyRuntimeFromWildcardEntitlement(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{
		Flags: map[string]bool{
			"*": true,
		},
	})

	enabled, err := svc.IsEnabled(ctx, serverruntime.FeatureTechStackMonthlyRuntimeIONOS, "auth0|all-features")
	if err != nil {
		t.Fatalf("IsEnabled IONOS: %v", err)
	}
	if !enabled {
		t.Fatal("IONOS provider should be enabled by wildcard entitlement")
	}
}

func TestUnknownFeature(t *testing.T) {
	store := NewMockStore()
	svc, _ := NewService(store, ServiceConfig{})

	ctx := context.Background()

	_, err := svc.IsEnabled(ctx, "nonexistent_feature", "user")
	if err == nil {
		t.Error("Expected error for unknown feature")
	}
}
