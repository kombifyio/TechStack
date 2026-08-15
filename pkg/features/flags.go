// Package features provides feature flag management for kombifyTechstack.
// It implements security-by-default feature toggles with consent tracking
// using OpenFeature standards and in-memory flag evaluation.
package features

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
)

// RiskLevel indicates the security risk level of a feature
type RiskLevel string

const (
	RiskLevelLow    RiskLevel = "low"
	RiskLevelMedium RiskLevel = "medium"
	RiskLevelHigh   RiskLevel = "high"
)

// Category classifies features by their type
type Category string

const (
	CategorySecurity Category = "security"
	CategoryBeta     Category = "beta"
	CategoryUX       Category = "ux"
)

// FeatureDefinition describes a feature flag with its metadata
type FeatureDefinition struct {
	Key             string    `json:"key"`
	Name            string    `json:"name"`
	DefaultValue    bool      `json:"default_value"`
	RequiresConsent bool      `json:"requires_consent"`
	RequiresAdmin   bool      `json:"requires_admin"`
	RiskLevel       RiskLevel `json:"risk_level"`
	Description     string    `json:"description"`
	Category        Category  `json:"category"`
}

// FlagState represents the current state of a feature flag for a user
type FlagState struct {
	Key             string    `json:"key"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Locked          bool      `json:"locked"` // true if requires admin and user is not admin
	RequiresConsent bool      `json:"requires_consent"`
	HasConsent      bool      `json:"has_consent"`
	RiskLevel       RiskLevel `json:"risk_level"`
	Description     string    `json:"description"`
	Category        Category  `json:"category"`
}

// SecurityFeatures defines all security-critical features (Default: OFF - must be explicitly enabled)
var SecurityFeatures = map[string]FeatureDefinition{
	"network_discovery": {
		Key:             "network_discovery_enabled",
		Name:            "Network Discovery",
		DefaultValue:    false,
		RequiresConsent: true,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelHigh,
		Description:     "Scans local network for devices and open ports. May trigger security alerts on corporate networks.",
		Category:        CategorySecurity,
	},
	"raw_commands": {
		Key:             "raw_command_execution",
		Name:            "Raw Command Execution",
		DefaultValue:    false,
		RequiresConsent: true,
		RequiresAdmin:   true,
		RiskLevel:       RiskLevelHigh,
		Description:     "Execute arbitrary shell commands on connected nodes. Use with extreme caution.",
		Category:        CategorySecurity,
	},
	"ssh_tunnel": {
		Key:             "ssh_tunnel_creation",
		Name:            "SSH Tunnel Creation",
		DefaultValue:    false,
		RequiresConsent: true,
		RequiresAdmin:   true,
		RiskLevel:       RiskLevelHigh,
		Description:     "Create SSH tunnels to nodes for remote access. Opens network connections.",
		Category:        CategorySecurity,
	},
	"cloud_backup": {
		Key:             "cloud_backup_enabled",
		Name:            "Cloud Backup",
		DefaultValue:    false,
		RequiresConsent: true,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Backup data to external S3-compatible storage. Data leaves local network.",
		Category:        CategorySecurity,
	},
}

// BetaFeatures defines experimental features (Default: OFF - opt-in only)
var BetaFeatures = map[string]FeatureDefinition{
	"native_v2_wizard": {
		Key:             "native_v2_wizard",
		Name:            "Native v2 Wizard",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Creation wizard emits native StackKits Architecture v2 specs via server-side projection. Beta feature.",
		Category:        CategoryBeta,
	},
	"cloudflare_tunnel": {
		Key:             "cloudflare_tunnel_enabled",
		Name:            "Cloudflare Tunnel",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Quick Tunnel for worker registration without port forwarding. Beta feature.",
		Category:        CategoryBeta,
	},
	"self_healing": {
		Key:             "self_healing_enabled",
		Name:            "Automatic Remediation",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   true,
		RiskLevel:       RiskLevelMedium,
		Description:     "Rule-based problem detection and remediation workflows. Alpha feature.",
		Category:        CategoryBeta,
	},
	"ha_stackkit": {
		Key:             "stackkit_ha_enabled",
		Name:            "HA Homelab StackKit",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Kubernetes-based high-availability StackKit. Beta feature.",
		Category:        CategoryBeta,
	},
	"custom_domains": {
		Key:             "custom_domains_enabled",
		Name:            "Custom Domains",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Custom domain mapping for deployed stacks. Requires PRO tier or above.",
		Category:        CategoryBeta,
	},
	"auto_scaling": {
		Key:             "auto_scaling_enabled",
		Name:            "Auto Scaling",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Automatic resource scaling for stacks. Requires ENTERPRISE tier.",
		Category:        CategoryBeta,
	},
	"monthly_runtime": {
		Key:             serverruntime.FeatureTechStackMonthlyRuntime,
		Name:            "Monthly Runtime",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Enables subscription-based monthly server runtime orchestration.",
		Category:        CategoryBeta,
	},
	"monthly_runtime_centron": {
		Key:             serverruntime.FeatureTechStackMonthlyRuntimeCentron,
		Name:            "Monthly Runtime: Centron",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Enables Centron as a TechStack Monthly Runtime provider.",
		Category:        CategoryBeta,
	},
	"monthly_runtime_ionos": {
		Key:             serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
		Name:            "Monthly Runtime: IONOS",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Enables IONOS as a TechStack Monthly Runtime provider.",
		Category:        CategoryBeta,
	},
	"monthly_runtime_cloudkit": {
		Key:             edgeFlagTechStackManagedRuntimeCloudKit,
		Name:            "Monthly Runtime: Cloud Kit",
		DefaultValue:    false,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelMedium,
		Description:     "Enables Cloud Kit deployments on TechStack Monthly Runtime.",
		Category:        CategoryBeta,
	},
}

// UXFeatures defines user experience features (Default: ON)
var UXFeatures = map[string]FeatureDefinition{
	"onboarding_wizard": {
		Key:             "onboarding_wizard",
		Name:            "Onboarding Wizard",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Guided first-time setup wizard.",
		Category:        CategoryUX,
	},
	"keyboard_shortcuts": {
		Key:             "keyboard_shortcuts",
		Name:            "Keyboard Shortcuts",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Enable j/k navigation and other keyboard shortcuts.",
		Category:        CategoryUX,
	},
	"dark_mode": {
		Key:             "dark_mode_available",
		Name:            "Dark Mode",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Enable dark/light theme switching.",
		Category:        CategoryUX,
	},
	"use_case_photos": {
		Key:             "use_case_photos",
		Name:            "Use Case: Personal Photo Memories",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the personal photo library use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_media": {
		Key:             "use_case_media",
		Name:            "Use Case: Personal Media Streaming",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the personal media streaming use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_vault": {
		Key:             "use_case_vault",
		Name:            "Use Case: Vaultwarden Password Manager",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the Vaultwarden password manager use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_files": {
		Key:             "use_case_files",
		Name:            "Use Case: Private File Storage",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the private file storage use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_smart_home": {
		Key:             "use_case_smart_home",
		Name:            "Use Case: Smart Home",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the advanced Smart Home use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_ai": {
		Key:             "use_case_ai",
		Name:            "Use Case: Local AI Lab",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the advanced local AI use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_dev": {
		Key:             "use_case_dev",
		Name:            "Use Case: Developer Platform",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the advanced developer platform use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_mail": {
		Key:             "use_case_mail",
		Name:            "Use Case: Mail Server",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the advanced mail server use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
	"use_case_game": {
		Key:             "use_case_game",
		Name:            "Use Case: Game Servers",
		DefaultValue:    true,
		RequiresConsent: false,
		RequiresAdmin:   false,
		RiskLevel:       RiskLevelLow,
		Description:     "Show the advanced game servers use case in the Easy Wizard.",
		Category:        CategoryUX,
	},
}

// Service manages feature flags using OpenFeature
type Service struct {
	client      *openfeature.Client
	provider    memprovider.InMemoryProvider
	store       Store
	definitions map[string]FeatureDefinition
	mu          sync.RWMutex
	log         *logger.Logger

	// Simple TTL cache for user feature states (reduces DB load)
	cache    map[string]*userFlagCache
	cacheMu  sync.RWMutex
	cacheTTL time.Duration
}

// userFlagCache holds cached feature states for a user
type userFlagCache struct {
	states    map[string]FlagState
	expiresAt time.Time
}

const defaultCacheTTL = 10 * time.Second

// ServiceConfig holds configuration for the feature flag service
type ServiceConfig struct {
	AllowUserOverrides bool `json:"allow_user_overrides" yaml:"allow_user_overrides"`
}

const (
	edgeFlagTechStackManagedRuntime         = "techstack.managed.runtime"
	edgeFlagTechStackManagedRuntimeCloudKit = "techstack.managed.runtime.cloudkit"
	// Legacy Auth0 metadata alias. New TechStack grants use Cloud Kit.
	edgeFlagTechStackManagedRuntimeBaseKit = "techstack.managed.runtime.basekit"
	edgeFlagTechStackManagedRuntimeCentron = "techstack.managed.runtime.centron"
	edgeFlagTechStackManagedRuntimeIONOS   = "techstack.managed.runtime.ionos"
	edgeFlagMonthlyRuntimeStandard         = "sim.monthly.runtime.standard"
	edgeFlagMonthlyRuntimePremium          = "sim.monthly.runtime.premium"
	edgeFlagManagedPVMCentron              = "sim.provider.cloud.managed.pvm.centron"
	edgeFlagManagedPVMIONOS                = "sim.provider.cloud.managed.pvm.ionos"
	edgeFlagAllFeatures                    = "all_features"
	edgeFlagWildcard                       = "*"
)

var edgeFlagAliases = map[string][]string{
	"monthly_runtime": {
		serverruntime.FeatureTechStackMonthlyRuntime,
		edgeFlagTechStackManagedRuntime,
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	serverruntime.FeatureTechStackMonthlyRuntime: {
		"monthly_runtime",
		edgeFlagTechStackManagedRuntime,
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	edgeFlagTechStackManagedRuntime: {
		"monthly_runtime",
		serverruntime.FeatureTechStackMonthlyRuntime,
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	"monthly_runtime_cloudkit": {
		edgeFlagTechStackManagedRuntimeCloudKit,
		edgeFlagTechStackManagedRuntimeBaseKit,
		serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
		"monthly_runtime_basekit",
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	edgeFlagTechStackManagedRuntimeCloudKit: {
		"monthly_runtime_cloudkit",
		edgeFlagTechStackManagedRuntimeBaseKit,
		serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
		"monthly_runtime_basekit",
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	"monthly_runtime_basekit": {
		"monthly_runtime_cloudkit",
		edgeFlagTechStackManagedRuntimeCloudKit,
		edgeFlagTechStackManagedRuntimeBaseKit,
		serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	serverruntime.FeatureTechStackMonthlyRuntimeBaseKit: {
		"monthly_runtime_cloudkit",
		edgeFlagTechStackManagedRuntimeCloudKit,
		edgeFlagTechStackManagedRuntimeBaseKit,
		"monthly_runtime_basekit",
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	edgeFlagTechStackManagedRuntimeBaseKit: {
		"monthly_runtime_cloudkit",
		edgeFlagTechStackManagedRuntimeCloudKit,
		serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
		"monthly_runtime_basekit",
		edgeFlagMonthlyRuntimeStandard,
		edgeFlagMonthlyRuntimePremium,
	},
	"monthly_runtime_centron": {
		serverruntime.FeatureTechStackMonthlyRuntimeCentron,
		edgeFlagTechStackManagedRuntimeCentron,
		edgeFlagManagedPVMCentron,
	},
	serverruntime.FeatureTechStackMonthlyRuntimeCentron: {
		"monthly_runtime_centron",
		edgeFlagTechStackManagedRuntimeCentron,
		edgeFlagManagedPVMCentron,
	},
	edgeFlagTechStackManagedRuntimeCentron: {
		"monthly_runtime_centron",
		serverruntime.FeatureTechStackMonthlyRuntimeCentron,
		edgeFlagManagedPVMCentron,
	},
	"monthly_runtime_ionos": {
		serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
		edgeFlagTechStackManagedRuntimeIONOS,
		edgeFlagManagedPVMIONOS,
	},
	serverruntime.FeatureTechStackMonthlyRuntimeIONOS: {
		"monthly_runtime_ionos",
		edgeFlagTechStackManagedRuntimeIONOS,
		edgeFlagManagedPVMIONOS,
	},
	edgeFlagTechStackManagedRuntimeIONOS: {
		"monthly_runtime_ionos",
		serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
		edgeFlagManagedPVMIONOS,
	},
}

var featureDefinitionAliases = map[string]string{
	edgeFlagTechStackManagedRuntime:                     "monthly_runtime",
	"techstack.monthly.runtime":                         "monthly_runtime",
	edgeFlagTechStackManagedRuntimeCloudKit:             "monthly_runtime_cloudkit",
	"techstack.monthly.runtime.cloudkit":                "monthly_runtime_cloudkit",
	edgeFlagTechStackManagedRuntimeBaseKit:              "monthly_runtime_cloudkit",
	"techstack.monthly.runtime.basekit":                 "monthly_runtime_cloudkit",
	serverruntime.FeatureTechStackMonthlyRuntimeBaseKit: "monthly_runtime_cloudkit",
	"monthly_runtime_basekit":                           "monthly_runtime_cloudkit",
	edgeFlagTechStackManagedRuntimeCentron:              "monthly_runtime_centron",
	"techstack.monthly.runtime.centron":                 "monthly_runtime_centron",
	edgeFlagTechStackManagedRuntimeIONOS:                "monthly_runtime_ionos",
	"techstack.monthly.runtime.ionos":                   "monthly_runtime_ionos",
}

// NewService creates a new feature flag service
func NewService(store Store, cfg ServiceConfig) (*Service, error) {
	log := logger.Get().WithComponent("features")

	// Merge all feature definitions
	definitions := make(map[string]FeatureDefinition)
	for k, v := range SecurityFeatures {
		definitions[k] = v
	}
	for k, v := range BetaFeatures {
		definitions[k] = v
	}
	for k, v := range UXFeatures {
		definitions[k] = v
	}

	// Build in-memory flag configuration
	flags := make(map[string]memprovider.InMemoryFlag)
	for _, def := range definitions {
		flags[def.Key] = memprovider.InMemoryFlag{
			State:          memprovider.Enabled,
			DefaultVariant: boolToVariant(def.DefaultValue),
			Variants: map[string]any{
				"on":  true,
				"off": false,
			},
		}
	}

	// Initialize in-memory provider
	provider := memprovider.NewInMemoryProvider(flags)
	if err := openfeature.SetProviderAndWait(provider); err != nil {
		return nil, fmt.Errorf("failed to set OpenFeature provider: %w", err)
	}

	client := openfeature.NewClient("techstack")

	svc := &Service{
		client:      client,
		provider:    provider,
		store:       store,
		definitions: definitions,
		log:         log,
		cache:       make(map[string]*userFlagCache),
		cacheTTL:    defaultCacheTTL,
	}

	log.Info("feature flag service initialized",
		"definitions", len(definitions),
		"security_features", len(SecurityFeatures),
		"beta_features", len(BetaFeatures),
		"ux_features", len(UXFeatures),
		"edge_flags", "enabled",
	)

	return svc, nil
}

// IsEnabled checks if a feature is enabled for a user
func (s *Service) IsEnabled(ctx context.Context, featureKey string, userID string) (bool, error) {
	localKey, def, exists := s.resolveFeature(featureKey)
	if !exists {
		return false, fmt.Errorf("unknown feature: %s", featureKey)
	}
	if edgeEnabled, found := edgeFlagValue(ctx, featureKey, localKey, def.Key); found {
		s.log.Debug("feature resolved by signed edge flags",
			"feature", localKey,
			"user", userID,
			"enabled", edgeEnabled,
		)
		return edgeEnabled, nil
	}

	// Build evaluation context
	evalCtx := openfeature.NewEvaluationContext(userID, map[string]any{
		"userId": userID,
	})

	// Get base flag value from OpenFeature
	value := s.client.Boolean(ctx, def.Key, def.DefaultValue, evalCtx)

	// Track whether this evaluation was explicitly overridden by the user.
	// We use this to decide whether consent-gating should apply.
	hasUserOverride := false

	// Check user override if store is available
	if s.store != nil {
		userEnabled, err := s.store.GetUserFlag(ctx, userID, localKey)
		if err == nil && userEnabled != nil {
			hasUserOverride = true
			value = *userEnabled
		}
	}

	// If feature requires consent and is enabled, verify consent exists.
	// IMPORTANT: Consent should only gate *opt-in enabling*.
	// For default-enabled features (DefaultValue=true) we allow them to run without consent,
	// and require consent only when the user explicitly enables (override=true),
	// or when the system default is OFF and the feature is turned ON.
	shouldGateByConsent := def.RequiresConsent && value && (hasUserOverride || !def.DefaultValue)
	if shouldGateByConsent {
		if s.store != nil {
			hasConsent, err := s.store.HasUserConsent(ctx, userID, localKey)
			if err != nil || !hasConsent {
				s.log.Debug("feature requires consent but none found",
					"feature", localKey,
					"user", userID,
				)
				return false, nil
			}
		}
	}

	return value, nil
}

// GetDefinition returns the definition for a feature
func (s *Service) GetDefinition(featureKey string) (*FeatureDefinition, bool) {
	_, def, exists := s.resolveFeature(featureKey)
	if !exists {
		return nil, false
	}
	return &def, true
}

func (s *Service) resolveFeature(featureKey string) (string, FeatureDefinition, bool) {
	key := strings.TrimSpace(featureKey)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if def, exists := s.definitions[key]; exists {
		return key, def, true
	}
	if localKey, exists := featureDefinitionAliases[key]; exists {
		if def, ok := s.definitions[localKey]; ok {
			return localKey, def, true
		}
	}
	for localKey, def := range s.definitions {
		if strings.TrimSpace(def.Key) == key {
			return localKey, def, true
		}
	}
	return key, FeatureDefinition{}, false
}

// getCachedFlags returns cached flags for a user if still valid
func (s *Service) getCachedFlags(userID string, isAdmin bool) (map[string]FlagState, bool) {
	cacheKey := fmt.Sprintf("%s:%v", userID, isAdmin)

	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	cached, exists := s.cache[cacheKey]
	if !exists || time.Now().After(cached.expiresAt) {
		return nil, false
	}
	return cached.states, true
}

// setCachedFlags stores flags in the cache
func (s *Service) setCachedFlags(userID string, isAdmin bool, states map[string]FlagState) {
	cacheKey := fmt.Sprintf("%s:%v", userID, isAdmin)

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	s.cache[cacheKey] = &userFlagCache{
		states:    states,
		expiresAt: time.Now().Add(s.cacheTTL),
	}
}

// GetAllFlags returns all flags with their current state for a user
// Optimized to use batch queries to avoid N+1 database calls
// Results are cached for a short TTL to reduce database load
func (s *Service) GetAllFlags(ctx context.Context, userID string, isAdmin bool) (map[string]FlagState, error) {
	_, edgeFlagsPresent := edgeauth.FlagsFromContext(ctx)

	// Check cache first
	if !edgeFlagsPresent {
		if cached, ok := s.getCachedFlags(userID, isAdmin); ok {
			return cached, nil
		}
	}

	result := make(map[string]FlagState)

	// Collect all feature keys that need consent checking
	var consentRequiredKeys []string
	var allKeys []string
	for key, def := range s.definitions {
		allKeys = append(allKeys, key)
		if def.RequiresConsent {
			consentRequiredKeys = append(consentRequiredKeys, key)
		}
	}

	// Batch fetch user preferences and consents (2 queries instead of up to 2*N)
	var userPrefs map[string]bool
	var userConsents map[string]bool

	if s.store != nil {
		var err error
		userPrefs, err = s.store.GetUserFlags(ctx, userID, allKeys)
		if err != nil {
			s.log.Warn("failed to batch fetch user flags, falling back", "error", err)
			userPrefs = make(map[string]bool)
		}

		if len(consentRequiredKeys) > 0 {
			userConsents, err = s.store.GetUserConsentsMap(ctx, userID, consentRequiredKeys)
			if err != nil {
				s.log.Warn("failed to batch fetch consents, falling back", "error", err)
				userConsents = make(map[string]bool)
			}
		} else {
			userConsents = make(map[string]bool)
		}
	}

	// Build flag states using prefetched data
	for key, def := range s.definitions {
		// Determine enabled state
		enabled := def.DefaultValue
		hasUserOverride := false
		lockedByEdge := false

		if edgeEnabled, found := edgeFlagValue(ctx, key, def.Key); found {
			enabled = edgeEnabled
			lockedByEdge = true
		}

		// Check user override from prefetched data
		if !lockedByEdge {
			if userEnabled, exists := userPrefs[key]; exists {
				hasUserOverride = true
				enabled = userEnabled
			}
		}

		// Check consent from prefetched data
		hasConsent := userConsents[key] // false if not in map

		// If feature requires consent and is enabled, verify consent exists.
		// See IsEnabled() for rationale: only gate opt-in enabling.
		shouldGateByConsent := def.RequiresConsent && enabled && !hasConsent && (hasUserOverride || !def.DefaultValue)
		if shouldGateByConsent {
			enabled = false
		}

		result[key] = FlagState{
			Key:             key,
			Name:            def.Name,
			Enabled:         enabled,
			Locked:          lockedByEdge || (def.RequiresAdmin && !isAdmin),
			RequiresConsent: def.RequiresConsent,
			HasConsent:      hasConsent,
			RiskLevel:       def.RiskLevel,
			Description:     def.Description,
			Category:        def.Category,
		}
	}

	// Cache the result
	if !edgeFlagsPresent {
		s.setCachedFlags(userID, isAdmin, result)
	}

	return result, nil
}

// GetFlagsByCategory returns flags grouped by category
func (s *Service) GetFlagsByCategory(ctx context.Context, userID string, isAdmin bool) (map[Category][]FlagState, error) {
	allFlags, err := s.GetAllFlags(ctx, userID, isAdmin)
	if err != nil {
		return nil, err
	}

	result := map[Category][]FlagState{
		CategorySecurity: {},
		CategoryBeta:     {},
		CategoryUX:       {},
	}

	for _, state := range allFlags {
		result[state.Category] = append(result[state.Category], state)
	}

	return result, nil
}

// Shutdown cleans up resources
func (s *Service) Shutdown() {
	openfeature.Shutdown()
	s.log.Info("feature flag service shutdown")
}

// Helper functions

func boolToVariant(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func edgeFlagValue(ctx context.Context, keys ...string) (bool, bool) {
	flags, ok := edgeauth.FlagsFromContext(ctx)
	if !ok || len(flags.Flags) == 0 {
		return false, false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value, exists := flags.Flags[key]; exists {
			return value, true
		}
	}
	if edgeFlagRequestsTechStackMonthlyRuntime(keys...) && edgeFlagsIncludeAllFeatures(flags.Flags) {
		return true, true
	}
	if edgeFlagRequestsMonthlyRuntimeEntitlement(keys...) {
		found := false
		enabled := false
		for _, key := range []string{edgeFlagMonthlyRuntimeStandard, edgeFlagMonthlyRuntimePremium} {
			if value, exists := flags.Flags[key]; exists {
				found = true
				enabled = enabled || value
			}
		}
		if found {
			return enabled, true
		}
	}
	for _, key := range expandEdgeFlagKeys(keys...) {
		if value, exists := flags.Flags[key]; exists {
			return value, true
		}
	}
	return false, false
}

func edgeFlagsIncludeAllFeatures(flags map[string]bool) bool {
	for _, key := range []string{edgeFlagAllFeatures, edgeFlagWildcard} {
		if flags[key] {
			return true
		}
	}
	return false
}

func edgeFlagRequestsMonthlyRuntimeEntitlement(keys ...string) bool {
	for _, key := range keys {
		switch strings.TrimSpace(key) {
		case "monthly_runtime",
			serverruntime.FeatureTechStackMonthlyRuntime,
			edgeFlagTechStackManagedRuntime,
			"monthly_runtime_cloudkit",
			edgeFlagTechStackManagedRuntimeCloudKit,
			"monthly_runtime_basekit",
			serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
			edgeFlagTechStackManagedRuntimeBaseKit:
			return true
		}
	}
	return false
}

func edgeFlagRequestsTechStackMonthlyRuntime(keys ...string) bool {
	for _, key := range keys {
		switch strings.TrimSpace(key) {
		case "monthly_runtime",
			serverruntime.FeatureTechStackMonthlyRuntime,
			edgeFlagTechStackManagedRuntime,
			"monthly_runtime_cloudkit",
			edgeFlagTechStackManagedRuntimeCloudKit,
			"monthly_runtime_basekit",
			serverruntime.FeatureTechStackMonthlyRuntimeBaseKit,
			edgeFlagTechStackManagedRuntimeBaseKit,
			"monthly_runtime_centron",
			serverruntime.FeatureTechStackMonthlyRuntimeCentron,
			edgeFlagTechStackManagedRuntimeCentron,
			"monthly_runtime_ionos",
			serverruntime.FeatureTechStackMonthlyRuntimeIONOS,
			edgeFlagTechStackManagedRuntimeIONOS:
			return true
		}
	}
	return false
}

func expandEdgeFlagKeys(keys ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(keys))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, key := range keys {
		add(key)
		for _, alias := range edgeFlagAliases[strings.TrimSpace(key)] {
			add(alias)
		}
	}
	return out
}
